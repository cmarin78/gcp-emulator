// Package cloudscheduler emula un subconjunto de la API de Cloud Scheduler
// (cloudscheduler.googleapis.com/v1): jobs, con un trigger manual ":run".
//
// Fase 11 (capa de comportamiento): a diferencia de las fases anteriores,
// un job con httpTarget y schedule ENABLED ahora dispara de verdad — un
// goroutine por job calcula el próximo fire time con internal/cronexpr y
// hace un HTTP request real al URI configurado en la hora que corresponde,
// sin necesitar Docker ni ninguna dependencia nueva. pubsubTarget sigue
// siendo solo shape (requeriría enrutar a través del propio Pub/Sub
// emulado, lo cual queda para una próxima iteración).
package cloudscheduler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cesar/gcp-emulator/internal/activity"
	"github.com/cesar/gcp-emulator/internal/cronexpr"
	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketJobs = "cloudscheduler.jobs"

// httpTarget mirrors the relevant subset of cloudscheduler#HttpTarget used
// to actually dispatch the job, decoded out of the passthrough JSON.
type httpTarget struct {
	URI        string            `json:"uri"`
	HTTPMethod string            `json:"httpMethod"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"` // base64, same wire format as the real API
}

// Job replica el recurso "Job" de la API v1 (subset: pubsubTarget se acepta
// como passthrough JSON sin disparar nada real; httpTarget sí se entrega).
type Job struct {
	Name            string          `json:"name"` // projects/{p}/locations/{l}/jobs/{j}
	Description     string          `json:"description,omitempty"`
	Schedule        string          `json:"schedule,omitempty"`
	TimeZone        string          `json:"timeZone,omitempty"`
	PubsubTarget    json.RawMessage `json:"pubsubTarget,omitempty"`
	HTTPTarget      json.RawMessage `json:"httpTarget,omitempty"`
	State           string          `json:"state"`
	LastAttemptTime string          `json:"lastAttemptTime,omitempty"`
	ScheduleTime    string          `json:"scheduleTime,omitempty"`
}

type Service struct {
	db         *storage.DB
	httpClient *http.Client

	mu    sync.Mutex
	stops map[string]chan struct{} // job name -> stop signal for its firing goroutine
}

func New(db *storage.DB) *Service {
	s := &Service{
		db:         db,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		stops:      make(map[string]chan struct{}),
	}
	// Resume firing for any job that was already ENABLED with a real
	// httpTarget before a restart — state lives in BoltDB, but the firing
	// goroutines themselves don't, so they need to be restarted here.
	_ = s.db.List(bucketJobs, "", func(key string, raw []byte) error {
		var job Job
		if err := json.Unmarshal(raw, &job); err != nil {
			return err
		}
		if job.State == "ENABLED" {
			s.startFiring(job)
		}
		return nil
	})
	return s
}

// startFiring launches (or restarts) the background goroutine that fires
// job.HTTPTarget on schedule. No-op if the job has no schedule or no real
// httpTarget to call.
func (s *Service) startFiring(job Job) {
	if job.Schedule == "" || len(job.HTTPTarget) == 0 {
		return
	}
	var ht httpTarget
	if err := json.Unmarshal(job.HTTPTarget, &ht); err != nil || ht.URI == "" {
		return
	}
	sch, err := cronexpr.Parse(job.Schedule)
	if err != nil {
		return // schedule no es un cron estándar de 5 campos; queda solo como shape
	}

	s.mu.Lock()
	if old, ok := s.stops[job.Name]; ok {
		close(old)
	}
	stop := make(chan struct{})
	s.stops[job.Name] = stop
	s.mu.Unlock()

	go s.fireLoop(job.Name, sch, ht, stop)
}

// stopFiring stops the firing goroutine for a job, if any (pause/delete).
func (s *Service) stopFiring(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stop, ok := s.stops[name]; ok {
		close(stop)
		delete(s.stops, name)
	}
}

func (s *Service) fireLoop(name string, sch *cronexpr.Schedule, ht httpTarget, stop chan struct{}) {
	for {
		next, err := sch.Next(time.Now().UTC())
		if err != nil {
			return
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
			s.dispatch(name, ht)
		}
	}
}

// dispatch performs the real HTTP call and records the attempt on the job,
// plus a real log entry + metric point via internal/activity so that
// Logging/Monitoring reflect what actually happened instead of staying
// empty stubs.
func (s *Service) dispatch(name string, ht httpTarget) {
	method := ht.HTTPMethod
	if method == "" {
		method = "POST"
	}
	var body []byte
	if ht.Body != "" {
		body, _ = base64.StdEncoding.DecodeString(ht.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, ht.URI, bytes.NewReader(body))
	status, severity := "ok", "INFO"
	if err == nil {
		for k, v := range ht.Headers {
			req.Header.Set(k, v)
		}
		if resp, derr := s.httpClient.Do(req); derr == nil {
			resp.Body.Close()
			if resp.StatusCode >= 400 {
				status, severity = fmt.Sprintf("http %d", resp.StatusCode), "ERROR"
			}
		} else {
			status, severity, err = derr.Error(), "ERROR", derr
		}
	} else {
		status, severity = err.Error(), "ERROR"
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var job Job
	found, gerr := s.db.Get(bucketJobs, name, &job)
	if gerr == nil && found {
		job.LastAttemptTime = now
		job.ScheduleTime = now
		_ = s.db.Put(bucketJobs, name, job)
	}

	project := activity.ProjectOf(name)
	activity.RecordLog(project, activity.LogEntry{
		LogName:     fmt.Sprintf("projects/%s/logs/cloudscheduler.googleapis.com%%2Fexecutions", project),
		Severity:    severity,
		TextPayload: fmt.Sprintf("job %s dispatched %s %s: %s", name, method, ht.URI, status),
		Resource:    map[string]any{"type": "cloud_scheduler_job", "labels": map[string]string{"job_id": name}},
	})
	activity.IncrCounter(project, "cloudscheduler.googleapis.com/job/execution_count", map[string]string{"job_name": name})
}

// Register monta las rutas de Cloud Scheduler, siguiendo los paths reales
// de cloudscheduler.googleapis.com.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/jobs", s.createJob)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/jobs", s.listJobs)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/jobs/{job}", s.getJob)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/jobs/{job}", s.deleteJob)
	// "{job}:run", "{job}:pause", "{job}:resume" no se pueden expresar como
	// un patrón mixto en el mux de Go; se captura el segmento completo y se
	// separa con strings.Cut (mismo patrón usado en Secret Manager/Pub/Sub).
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/jobs/{jobAction}", s.jobAction)
}

func jobName(project, location, job string) string {
	return fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, job)
}

func (s *Service) createJob(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")

	var body struct {
		Name         string          `json:"name"`
		Description  string          `json:"description"`
		Schedule     string          `json:"schedule"`
		TimeZone     string          `json:"timeZone"`
		PubsubTarget json.RawMessage `json:"pubsubTarget"`
		HTTPTarget   json.RawMessage `json:"httpTarget"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}

	// El nombre del job viene en el body (a diferencia de secretId en query),
	// igual que la API real: name = "projects/{p}/locations/{l}/jobs/{j}".
	jobID := body.Name
	if idx := strings.LastIndex(jobID, "/"); idx >= 0 {
		jobID = jobID[idx+1:]
	}
	if jobID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name (con el jobId) es requerido")
		return
	}

	name := jobName(project, location, jobID)
	var existing Job
	found, err := s.db.Get(bucketJobs, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "el job ya existe")
		return
	}

	job := Job{
		Name:         name,
		Description:  body.Description,
		Schedule:     body.Schedule,
		TimeZone:     body.TimeZone,
		PubsubTarget: body.PubsubTarget,
		HTTPTarget:   body.HTTPTarget,
		State:        "ENABLED",
	}
	if err := s.db.Put(bucketJobs, job.Name, job); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.startFiring(job)
	server.WriteJSON(w, 200, job)
}

func (s *Service) listJobs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/jobs/", project, location)
	jobs := []Job{}
	_ = s.db.List(bucketJobs, prefix, func(key string, raw []byte) error {
		var job Job
		if err := json.Unmarshal(raw, &job); err != nil {
			return err
		}
		jobs = append(jobs, job)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"jobs": jobs})
}

func (s *Service) getJob(w http.ResponseWriter, r *http.Request) {
	name := jobName(r.PathValue("project"), r.PathValue("location"), r.PathValue("job"))
	var job Job
	found, err := s.db.Get(bucketJobs, name, &job)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "job no encontrado")
		return
	}
	server.WriteJSON(w, 200, job)
}

func (s *Service) deleteJob(w http.ResponseWriter, r *http.Request) {
	name := jobName(r.PathValue("project"), r.PathValue("location"), r.PathValue("job"))
	if err := s.db.Delete(bucketJobs, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.stopFiring(name)
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Service) jobAction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	jobID, action, ok := strings.Cut(r.PathValue("jobAction"), ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}

	name := jobName(project, location, jobID)
	var job Job
	found, err := s.db.Get(bucketJobs, name, &job)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "job no encontrado")
		return
	}

	switch action {
	case "run":
		// Disparo real e inmediato del httpTarget configurado (además del
		// disparo automático por cron), igual que el ":run" de la API real.
		now := time.Now().UTC().Format(time.RFC3339)
		job.LastAttemptTime = now
		job.ScheduleTime = now
		if len(job.HTTPTarget) > 0 {
			var ht httpTarget
			if json.Unmarshal(job.HTTPTarget, &ht) == nil && ht.URI != "" {
				go s.dispatch(name, ht)
			}
		}
	case "pause":
		job.State = "PAUSED"
		s.stopFiring(name)
	case "resume":
		job.State = "ENABLED"
		s.startFiring(job)
	default:
		server.WriteError(w, 404, "NOT_FOUND", "acción no soportada: "+action)
		return
	}

	if err := s.db.Put(bucketJobs, name, job); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, job)
}
