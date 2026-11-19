// Package composer emulates a subset of the Cloud Composer API
// (composer.googleapis.com/v1): environment CRUD per location.
// No real Airflow DAGs or workers are involved — this is shape-compatible only.
// Mutations return a google.longrunning.Operation with done:true, following the
// same approach used elsewhere in this project.
package composer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketEnvironments = "composer.environments"

// SoftwareConfig holds the Composer / Airflow version configuration.
type SoftwareConfig struct {
	ImageVersion         string            `json:"imageVersion,omitempty"`
	AirflowConfigOverrides map[string]string `json:"airflowConfigOverrides,omitempty"`
	EnvVariables         map[string]string `json:"envVariables,omitempty"`
	PypiPackages         map[string]string `json:"pypiPackages,omitempty"`
}

// NodeConfig holds Compute Engine node configuration.
type NodeConfig struct {
	Location    string `json:"location,omitempty"`
	MachineType string `json:"machineType,omitempty"`
	Network     string `json:"network,omitempty"`
	Subnetwork  string `json:"subnetwork,omitempty"`
	DiskSizeGb  int32  `json:"diskSizeGb,omitempty"`
}

// EnvironmentConfig holds the full environment configuration (subset).
type EnvironmentConfig struct {
	NodeCount      int32           `json:"nodeCount,omitempty"`
	NodeConfig     *NodeConfig     `json:"nodeConfig,omitempty"`
	SoftwareConfig *SoftwareConfig `json:"softwareConfig,omitempty"`
}

// EnvironmentState holds operational state info.
type EnvironmentState struct {
	State string `json:"state,omitempty"`
}

// Environment mirrors the real composer#Environment resource.
type Environment struct {
	Name       string             `json:"name,omitempty"`
	Config     *EnvironmentConfig `json:"config,omitempty"`
	Labels     map[string]string  `json:"labels,omitempty"`
	State      string             `json:"state,omitempty"`
	CreateTime string             `json:"createTime,omitempty"`
	UpdateTime string             `json:"updateTime,omitempty"`
}

// Operation mirrors google.longrunning.Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Response json.RawMessage `json:"response,omitempty"`
}

// Service is the Cloud Composer emulator service.
type Service struct {
	db  *storage.DB
	seq int64
}

// New creates a new Cloud Composer service backed by db.
func New(db *storage.DB) *Service { return &Service{db: db} }

// Register wires Cloud Composer routes into mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /composer/v1/projects/{project}/locations/{location}/environments", s.createEnvironment)
	mux.HandleFunc("GET /composer/v1/projects/{project}/locations/{location}/environments", s.listEnvironments)
	mux.HandleFunc("GET /composer/v1/projects/{project}/locations/{location}/environments/{environment}", s.getEnvironment)
	mux.HandleFunc("PATCH /composer/v1/projects/{project}/locations/{location}/environments/{environment}", s.patchEnvironment)
	mux.HandleFunc("DELETE /composer/v1/projects/{project}/locations/{location}/environments/{environment}", s.deleteEnvironment)
}

func envKey(project, location, name string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, name)
}

func envName(project, location, name string) string {
	return fmt.Sprintf("projects/%s/locations/%s/environments/%s", project, location, name)
}

func (s *Service) nextSeq() int64 {
	return atomic.AddInt64(&s.seq, 1)
}

func opName(project, location string, id int64) string {
	return fmt.Sprintf("projects/%s/locations/%s/operations/operation-%016d", project, location, id)
}

func (s *Service) writeOperation(w http.ResponseWriter, project, location string, env Environment) {
	resp, _ := json.Marshal(env)
	op := Operation{
		Name:     opName(project, location, s.nextSeq()),
		Done:     true,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createEnvironment(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")

	var body Environment
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}

	// Derive environment ID from the name field (last segment) or from the
	// URL-encoded name query param (Terraform passes it in the body as name).
	envID := body.Name
	if envID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name is required")
		return
	}
	// If name looks like a full resource path, use only the last segment as key
	// but keep the full path as the stored name.
	key := envKey(project, location, envID)

	var existing Environment
	found, err := s.db.Get(bucketEnvironments, key, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "environment already exists: "+envID)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	body.Name = envName(project, location, envID)
	body.State = "RUNNING"
	body.CreateTime = now
	body.UpdateTime = now

	if err := s.db.Put(bucketEnvironments, key, body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, body)
}

func (s *Service) listEnvironments(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)

	items := []Environment{}
	_ = s.db.List(bucketEnvironments, prefix, func(_ string, raw []byte) error {
		var env Environment
		if err := json.Unmarshal(raw, &env); err != nil {
			return err
		}
		items = append(items, env)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"environments": items})
}

func (s *Service) getEnvironment(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	name := r.PathValue("environment")

	var env Environment
	found, err := s.db.Get(bucketEnvironments, envKey(project, location, name), &env)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "environment not found: "+name)
		return
	}
	server.WriteJSON(w, 200, env)
}

func (s *Service) patchEnvironment(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	name := r.PathValue("environment")

	var existing Environment
	found, err := s.db.Get(bucketEnvironments, envKey(project, location, name), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "environment not found: "+name)
		return
	}

	var body struct {
		Labels map[string]string  `json:"labels"`
		Config *EnvironmentConfig `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	if body.Config != nil {
		existing.Config = body.Config
	}
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)

	if err := s.db.Put(bucketEnvironments, envKey(project, location, name), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, existing)
}

func (s *Service) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	name := r.PathValue("environment")

	var existing Environment
	found, _ := s.db.Get(bucketEnvironments, envKey(project, location, name), &existing)
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "environment not found: "+name)
		return
	}
	if err := s.db.Delete(bucketEnvironments, envKey(project, location, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	existing.State = "DELETING"
	s.writeOperation(w, project, location, existing)
}
