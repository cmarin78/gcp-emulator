// Package dataflow emulates a subset of the Cloud Dataflow API
// (dataflow.googleapis.com/v1b3): pipeline jobs.
//
// Real Dataflow jobs spin up distributed workers, execute a beam pipeline,
// and can take minutes to hours. This emulator follows the same
// "shape-compatible, not behavior-complete" approach used throughout this
// project: every job is stored and returned correctly, but no pipeline is
// actually executed.
//
// Behavior:
//   - JOB_TYPE_BATCH jobs are created with state JOB_STATE_DONE immediately
//     (pipeline "already finished").
//   - JOB_TYPE_STREAMING jobs are created with state JOB_STATE_RUNNING (long
//     running, stays until cancelled).
//   - A PUT with requestedState=JOB_STATE_CANCELLED transitions any job to
//     JOB_STATE_CANCELLED.
//
// Both the current regional form (.../locations/{location}/jobs) and the
// legacy project-level form (.../jobs) are registered. Legacy create/get
// default to location "us-central1".
package dataflow

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketJobs          = "dataflow.jobs"
	defaultLegacyLocation = "us-central1"
)

// Job mirrors google.dataflow.v1b3.Job.
type Job struct {
	ID               string            `json:"id"`
	ProjectID        string            `json:"projectId,omitempty"`
	Name             string            `json:"name,omitempty"`
	Type             string            `json:"type,omitempty"`
	CurrentState     string            `json:"currentState,omitempty"`
	CurrentStateTime string            `json:"currentStateTime,omitempty"`
	CreateTime       string            `json:"createTime,omitempty"`
	Location         string            `json:"location,omitempty"`
	Environment      json.RawMessage   `json:"environment,omitempty"`
	Steps            json.RawMessage   `json:"steps,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	SDKVersion       *SDKVersion       `json:"sdkVersion,omitempty"`
}

// SDKVersion mirrors google.dataflow.v1b3.SdkVersion (optional metadata).
type SDKVersion struct {
	Version            string `json:"version,omitempty"`
	VersionDisplayName string `json:"versionDisplayName,omitempty"`
}

// Service holds the BoltDB handle and a monotonic job-ID counter.
type Service struct {
	db  *storage.DB
	seq int64
}

// New constructs a Dataflow emulator service.
func New(db *storage.DB) *Service {
	return &Service{db: db}
}

// Register mounts the Dataflow routes onto mux.
// Regional paths: /v1b3/projects/{project}/locations/{location}/jobs[/{job}]
// Legacy paths:   /v1b3/projects/{project}/jobs[/{job}]
func (s *Service) Register(mux *http.ServeMux) {
	// Regional (current API form).
	mux.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/jobs", s.createJob)
	mux.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/jobs", s.listJobs)
	mux.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/jobs/{job}", s.getJob)
	mux.HandleFunc("PUT /v1b3/projects/{project}/locations/{location}/jobs/{job}", s.updateJob)

	// Legacy (project-level, no location segment).
	mux.HandleFunc("POST /v1b3/projects/{project}/jobs", s.createJobLegacy)
	mux.HandleFunc("GET /v1b3/projects/{project}/jobs", s.listJobsLegacy)
	mux.HandleFunc("GET /v1b3/projects/{project}/jobs/{job}", s.getJobLegacy)
	mux.HandleFunc("PUT /v1b3/projects/{project}/jobs/{job}", s.updateJobLegacy)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func jobKey(project, location, id string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, id)
}

func (s *Service) nextID() string {
	return fmt.Sprintf("job-%016d", atomic.AddInt64(&s.seq, 1))
}

// initialState maps a job type to the state the emulator gives it on creation.
func initialState(jobType string) string {
	if jobType == "JOB_TYPE_STREAMING" {
		return "JOB_STATE_RUNNING"
	}
	// Batch jobs complete immediately in the emulator.
	return "JOB_STATE_DONE"
}

// ── Regional handlers ─────────────────────────────────────────────────────────

func (s *Service) createJob(w http.ResponseWriter, r *http.Request) {
	s.doCreateJob(w, r, r.PathValue("project"), r.PathValue("location"))
}

func (s *Service) listJobs(w http.ResponseWriter, r *http.Request) {
	s.doListJobs(w, r.PathValue("project"), r.PathValue("location"))
}

func (s *Service) getJob(w http.ResponseWriter, r *http.Request) {
	s.doGetJob(w, r.PathValue("project"), r.PathValue("location"), r.PathValue("job"))
}

func (s *Service) updateJob(w http.ResponseWriter, r *http.Request) {
	s.doUpdateJob(w, r, r.PathValue("project"), r.PathValue("location"), r.PathValue("job"))
}

// ── Legacy handlers (default location = us-central1) ─────────────────────────

func (s *Service) createJobLegacy(w http.ResponseWriter, r *http.Request) {
	s.doCreateJob(w, r, r.PathValue("project"), defaultLegacyLocation)
}

func (s *Service) listJobsLegacy(w http.ResponseWriter, r *http.Request) {
	// Legacy list scans all locations for the project.
	s.doListJobs(w, r.PathValue("project"), "")
}

func (s *Service) getJobLegacy(w http.ResponseWriter, r *http.Request) {
	s.doGetJob(w, r.PathValue("project"), defaultLegacyLocation, r.PathValue("job"))
}

func (s *Service) updateJobLegacy(w http.ResponseWriter, r *http.Request) {
	s.doUpdateJob(w, r, r.PathValue("project"), defaultLegacyLocation, r.PathValue("job"))
}

// ── Core operations ───────────────────────────────────────────────────────────

func (s *Service) doCreateJob(w http.ResponseWriter, r *http.Request, project, location string) {
	var body Job
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}

	jobType := body.Type
	if jobType == "" {
		jobType = "JOB_TYPE_BATCH"
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := s.nextID()
	job := Job{
		ID:               id,
		ProjectID:        project,
		Name:             body.Name,
		Type:             jobType,
		CurrentState:     initialState(jobType),
		CurrentStateTime: now,
		CreateTime:       now,
		Location:         location,
		Environment:      body.Environment,
		Steps:            body.Steps,
		Labels:           body.Labels,
		SDKVersion:       body.SDKVersion,
	}
	if err := s.db.Put(bucketJobs, jobKey(project, location, id), job); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, job)
}

func (s *Service) doListJobs(w http.ResponseWriter, project, location string) {
	// location == "" → list all locations for this project.
	prefix := project + "/"
	if location != "" {
		prefix = project + "/" + location + "/"
	}
	jobs := []Job{}
	_ = s.db.List(bucketJobs, prefix, func(_ string, raw []byte) error {
		var j Job
		if err := json.Unmarshal(raw, &j); err != nil {
			return err
		}
		jobs = append(jobs, j)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"jobs": jobs})
}

func (s *Service) doGetJob(w http.ResponseWriter, project, location, id string) {
	var job Job
	found, err := s.db.Get(bucketJobs, jobKey(project, location, id), &job)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "job not found: "+id)
		return
	}
	server.WriteJSON(w, 200, job)
}

func (s *Service) doUpdateJob(w http.ResponseWriter, r *http.Request, project, location, id string) {
	var existing Job
	found, err := s.db.Get(bucketJobs, jobKey(project, location, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "job not found: "+id)
		return
	}

	var body struct {
		RequestedState string            `json:"requestedState"`
		Labels         map[string]string `json:"labels,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}

	if body.RequestedState == "JOB_STATE_CANCELLED" || body.RequestedState == "JOB_STATE_DRAINING" {
		existing.CurrentState = "JOB_STATE_CANCELLED"
		existing.CurrentStateTime = time.Now().UTC().Format(time.RFC3339)
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}

	if err := s.db.Put(bucketJobs, jobKey(project, location, id), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}
