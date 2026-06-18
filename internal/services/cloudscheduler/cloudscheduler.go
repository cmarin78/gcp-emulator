// Package cloudscheduler emula un subconjunto de la API de Cloud Scheduler
// (cloudscheduler.googleapis.com/v1): jobs, con un trigger manual ":run".
// Igual que Pub/Sub y los otros servicios async, esto es "shape-compatible,
// no behavior-complete": no hay un cron real disparando los jobs, solo CRUD
// y un :run que simula la ejecución actualizando el estado/timestamps.
package cloudscheduler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketJobs = "cloudscheduler.jobs"

// Job replica el recurso "Job" de la API v1 (subset: pubsubTarget/httpTarget
// se aceptan como passthrough JSON, sin disparar nada real).
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
	db *storage.DB
}

func New(db *storage.DB) *Service {
	return &Service{db: db}
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
		// No hay un disparo real (sin HTTP/Pub/Sub de verdad); se simula la
		// ejecución actualizando los timestamps, como pediría un test que
		// verifica que el job "corrió".
		now := time.Now().UTC().Format(time.RFC3339)
		job.LastAttemptTime = now
		job.ScheduleTime = now
	case "pause":
		job.State = "PAUSED"
	case "resume":
		job.State = "ENABLED"
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
