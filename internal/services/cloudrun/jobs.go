// Phase 9 of the roadmap: Cloud Run Jobs (google_cloud_run_v2_job) — a
// distinct resource from the Cloud Run *services* modeled in cloudrun.go,
// for batch/one-off workloads rather than request-serving ones. Reuses the
// Container/EnvVar shapes already defined for services. The manual ":run"
// action (used by gcloud run jobs execute and the provider's read-back of
// executions) always records an execution count and timestamp, matching
// the rest of this emulator's "shape-compatible, not behavior-complete"
// philosophy by default.
//
// Phase 14 extends ":run" with an opt-in (internal/realbackend.WantsReal,
// checked against the job's labels): when requested and Docker is
// available, it actually runs template.template.containers[0].image to
// completion via internal/realbackend/dockerrun.RunToCompletion — a
// one-shot run, never admitted into the Governor, since a Job execution
// isn't a long-running resource the way a Service is. On any failure (no
// opt-in, Docker unavailable, the run itself failing to start) this falls
// back to exactly the no-op behavior above.
package cloudrun

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/realbackend"
	"github.com/cesar/gcp-emulator/internal/realbackend/dockerrun"
	"github.com/cesar/gcp-emulator/internal/server"
)

const bucketJobs = "cloudrun.jobs"

// TaskTemplate mirrors google.cloud.run.v2.TaskTemplate.
type TaskTemplate struct {
	Containers     []Container `json:"containers"`
	ServiceAccount string      `json:"serviceAccount,omitempty"`
	MaxRetries     int         `json:"maxRetries,omitempty"`
	Timeout        string      `json:"timeout,omitempty"`
}

// ExecutionTemplate mirrors google.cloud.run.v2.ExecutionTemplate.
type ExecutionTemplate struct {
	Parallelism int               `json:"parallelism,omitempty"`
	TaskCount   int               `json:"taskCount,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Template    TaskTemplate      `json:"template"`
}

// Job mirrors google.cloud.run.v2.Job.
type Job struct {
	Name                   string            `json:"name"` // projects/{p}/locations/{l}/jobs/{j}
	UID                    string            `json:"uid"`
	Generation             string            `json:"generation"`
	Labels                 map[string]string `json:"labels,omitempty"`
	Annotations            map[string]string `json:"annotations,omitempty"`
	LaunchStage            string            `json:"launchStage,omitempty"`
	Template               ExecutionTemplate `json:"template"`
	CreateTime             string            `json:"createTime"`
	UpdateTime             string            `json:"updateTime"`
	ObservedGeneration     string            `json:"observedGeneration"`
	Reconciling            bool              `json:"reconciling"`
	TerminalCondition      Condition         `json:"terminalCondition"`
	ExecutionCount         int               `json:"executionCount"`
	LatestCreatedExecution string            `json:"latestCreatedExecution,omitempty"`
}

func (s *Svc) registerJobs(mux *http.ServeMux) {
	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/jobs", s.createJob)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/jobs", s.listJobs)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/jobs/{job}", s.getJob)
	mux.HandleFunc("PATCH /v2/projects/{project}/locations/{location}/jobs/{job}", s.updateJob)
	mux.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/jobs/{job}", s.deleteJob)
	// "{job}:run" can't be expressed as a mixed Go mux pattern; capture the
	// full segment and split on ":", same pattern used by Cloud Scheduler/
	// Secret Manager/Pub/Sub elsewhere in this codebase.
	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/jobs/{jobAction}", s.jobAction)
}

func jobName(project, location, job string) string {
	return fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, job)
}

func (s *Svc) createJob(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	jobID := r.URL.Query().Get("jobId")
	if jobID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "jobId es requerido")
		return
	}
	var body struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		LaunchStage string            `json:"launchStage"`
		Template    ExecutionTemplate `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if len(body.Template.Template.Containers) == 0 || body.Template.Template.Containers[0].Image == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "template.template.containers[0].image es requerido")
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
		server.WriteError(w, 409, "ALREADY_EXISTS", "job ya existe: "+name)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	job := Job{
		Name:               name,
		UID:                fmt.Sprintf("uid-%d", time.Now().UnixNano()),
		Generation:         "1",
		Labels:             body.Labels,
		Annotations:        body.Annotations,
		LaunchStage:        orDefault(body.LaunchStage, "GA"),
		Template:           body.Template,
		CreateTime:         now,
		UpdateTime:         now,
		ObservedGeneration: "1",
		Reconciling:        false,
		TerminalCondition:  Condition{Type: "Ready", State: "CONDITION_SUCCEEDED"},
	}
	if err := s.db.Put(bucketJobs, job.Name, job); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeJobOperation(w, project, location, job)
}

func (s *Svc) listJobs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/jobs/", project, location)
	items := []Job{}
	_ = s.db.List(bucketJobs, prefix, func(key string, raw []byte) error {
		var job Job
		if err := json.Unmarshal(raw, &job); err != nil {
			return err
		}
		items = append(items, job)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"jobs": items})
}

func (s *Svc) getJob(w http.ResponseWriter, r *http.Request) {
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

func (s *Svc) updateJob(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	name := jobName(project, location, r.PathValue("job"))
	var existing Job
	found, err := s.db.Get(bucketJobs, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "job no encontrado")
		return
	}
	var body struct {
		Labels      map[string]string  `json:"labels"`
		Annotations map[string]string  `json:"annotations"`
		Template    *ExecutionTemplate `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	if body.Annotations != nil {
		existing.Annotations = body.Annotations
	}
	if body.Template != nil {
		existing.Template = *body.Template
	}
	existing.Generation = bumpGeneration(existing.Generation)
	existing.ObservedGeneration = existing.Generation
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.Put(bucketJobs, existing.Name, existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeJobOperation(w, project, location, existing)
}

func (s *Svc) deleteJob(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	name := jobName(project, location, r.PathValue("job"))
	if err := s.db.Delete(bucketJobs, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.seq++
	op := Operation{Name: operationName(project, location, fmt.Sprintf("op-%d", s.seq)), Done: true}
	server.WriteJSON(w, 200, op)
}

// jobAction dispatches the manual "{job}:run" verb. Other verbs aren't
// modeled (Cloud Run Jobs' API surface here is intentionally minimal).
func (s *Svc) jobAction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	jobID, action, ok := strings.Cut(r.PathValue("jobAction"), ":")
	if !ok || action != "run" {
		server.WriteError(w, 404, "NOT_FOUND", "acción no soportada")
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
	job.ExecutionCount++
	execName := fmt.Sprintf("%s/executions/%s-%d", name, jobID, job.ExecutionCount)
	job.LatestCreatedExecution = execName
	if err := s.db.Put(bucketJobs, job.Name, job); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.seq++

	resp := map[string]any{"name": execName}
	if realRun := s.tryRunReal(r, &job); realRun != nil {
		resp["realRun"] = realRun
	}
	respBytes, _ := json.Marshal(resp)
	op := Operation{Name: operationName(project, location, fmt.Sprintf("op-%d", s.seq)), Done: true, Response: respBytes}
	server.WriteJSON(w, 200, op)
}

// RealRunResult is an emulator-only extension carried in the ":run"
// operation's response (under "realRun") — not part of the real Cloud
// Run Jobs API, which has no synchronous result to report since real
// executions are asynchronous. It only appears when the job opted into
// real execution (internal/realbackend.WantsReal) and Docker actually ran
// the image.
type RealRunResult struct {
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
}

// tryRunReal runs job's container image to completion via Docker when
// the caller opted in (realbackend.WantsReal, checked against
// job.Labels) and Docker is actually reachable. Returns nil — leaving the
// ":run" response exactly as it was before Phase 14 — on any failure: no
// opt-in, Docker unavailable, or the run itself failing to start. Unlike
// Cloud Run Services' tryStartReal, this never touches the Governor: a
// Job execution is a one-shot task, not a long-running resource.
func (s *Svc) tryRunReal(r *http.Request, job *Job) *RealRunResult {
	if !realbackend.WantsReal(r, job.Labels) {
		return nil
	}
	if len(job.Template.Template.Containers) == 0 || job.Template.Template.Containers[0].Image == "" {
		return nil
	}
	avail := realbackend.DetectDocker(r.Context())
	if !avail.Available {
		log.Printf("cloudrun: job %s pidió ejecución real pero Docker no está disponible (%s), sigue shape-only", job.Name, avail.Detail)
		return nil
	}
	container := job.Template.Template.Containers[0]
	timeout := jobRunTimeout(job.Template.Template.Timeout)
	exitCode, output, err := dockerrun.RunToCompletion(container.Image, envMapOf(container.Env), timeout)
	if err != nil {
		log.Printf("cloudrun: job %s pidió ejecución real pero docker run falló, sigue shape-only: %v", job.Name, err)
		return nil
	}
	return &RealRunResult{ExitCode: exitCode, Output: output}
}

// jobRunTimeout parses a protobuf-style Duration string (e.g. "600s", the
// format TaskTemplate.Timeout uses) into a time.Duration, falling back to
// dockerrun.DefaultRunTimeout when timeout is empty or unparseable.
func jobRunTimeout(timeout string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSuffix(timeout, "s"))
	if err != nil || secs <= 0 {
		return dockerrun.DefaultRunTimeout
	}
	return time.Duration(secs) * time.Second
}

func (s *Svc) writeJobOperation(w http.ResponseWriter, project, location string, job Job) {
	respBytes, _ := json.Marshal(job)
	s.seq++
	op := Operation{
		Name:     operationName(project, location, fmt.Sprintf("op-%d", s.seq)),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}
