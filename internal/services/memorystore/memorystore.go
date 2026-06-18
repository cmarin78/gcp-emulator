// Package memorystore emulates a subset of the Memorystore for Redis API
// (redis.googleapis.com/v1): instances. Real instances take minutes to
// provision; this emulator resolves every mutation synchronously and always
// reports state READY, following the same "shape-compatible, not
// behavior-complete" approach used elsewhere in this project. Mutations
// return a google.longrunning.Operation, same shape as cloudbuild.go (the
// real Memorystore API also returns google.longrunning.Operation, unlike
// Cloud SQL's sqladmin#operation shape).
package memorystore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketInstances = "memorystore.instances"

// Instance mirrors the real redis#Instance resource (regional, scoped to a
// location). Only the most commonly used fields are modeled.
type Instance struct {
	Name              string `json:"name"`
	DisplayName       string `json:"displayName,omitempty"`
	Tier              string `json:"tier,omitempty"`
	MemorySizeGb      int64  `json:"memorySizeGb,omitempty"`
	RedisVersion      string `json:"redisVersion,omitempty"`
	Host              string `json:"host,omitempty"`
	Port              int64  `json:"port,omitempty"`
	State             string `json:"state,omitempty"`
	AuthorizedNetwork string `json:"authorizedNetwork,omitempty"`
	CreateTime        string `json:"createTime,omitempty"`
}

// Operation mirrors google.longrunning.Operation, same shape as
// cloudbuild.go's Operation (the real Memorystore API uses this too).
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type operationMetadata struct {
	Target string `json:"target"`
	Verb   string `json:"verb"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/instances", s.createInstance)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/instances", s.listInstances)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/instances/{instance}", s.getInstance)
	mux.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/instances/{instance}", s.patchInstance)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/instances/{instance}", s.deleteInstance)
	// Note: no dedicated GET .../operations/{operation} route here — that
	// exact path pattern is already registered by artifactregistry.go on
	// the shared /v1/* mux, and http.ServeMux panics on duplicate patterns.
	// Not a problem in practice: every mutation above already resolves
	// synchronously and returns done:true in its own response, so clients
	// have no real reason to poll.
}

func instanceKey(project, location, instance string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, instance)
}

func instanceName(project, location, instance string) string {
	return fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, instance)
}

func (s *Service) nextID() int64 {
	s.seq++
	return s.seq
}

func operationName(project, location string, id int64) string {
	return fmt.Sprintf("projects/%s/locations/%s/operations/op-%d", project, location, id)
}

func (s *Service) writeOperation(w http.ResponseWriter, project, location, verb string, inst Instance) {
	meta, _ := json.Marshal(operationMetadata{Target: inst.Name, Verb: verb})
	resp, _ := json.Marshal(inst)
	op := Operation{
		Name:     operationName(project, location, s.nextID()),
		Done:     true,
		Metadata: meta,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	instanceID := r.URL.Query().Get("instanceId")
	var body struct {
		DisplayName       string `json:"displayName"`
		Tier              string `json:"tier"`
		MemorySizeGb      int64  `json:"memorySizeGb"`
		RedisVersion      string `json:"redisVersion"`
		AuthorizedNetwork string `json:"authorizedNetwork"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if instanceID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "instanceId is required")
		return
	}
	var existing Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, location, instanceID), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "instance already exists: "+instanceID)
		return
	}
	inst := Instance{
		Name:              instanceName(project, location, instanceID),
		DisplayName:       body.DisplayName,
		Tier:              orDefault(body.Tier, "BASIC"),
		MemorySizeGb:      orDefaultInt(body.MemorySizeGb, 1),
		RedisVersion:      orDefault(body.RedisVersion, "REDIS_7_0"),
		Host:              fmt.Sprintf("10.%d.%d.10", len(instanceID)%255, len(project)%255),
		Port:              6379,
		State:             "READY",
		AuthorizedNetwork: body.AuthorizedNetwork,
		CreateTime:        time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.db.Put(bucketInstances, instanceKey(project, location, instanceID), inst); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "create", inst)
}

func (s *Service) listInstances(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)
	items := []Instance{}
	_ = s.db.List(bucketInstances, prefix, func(key string, raw []byte) error {
		var inst Instance
		if err := json.Unmarshal(raw, &inst); err != nil {
			return err
		}
		items = append(items, inst)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"instances": items})
}

func (s *Service) getInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	instance := r.PathValue("instance")
	var inst Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, location, instance), &inst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found")
		return
	}
	server.WriteJSON(w, 200, inst)
}

func (s *Service) patchInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	instance := r.PathValue("instance")
	var existing Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, location, instance), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found")
		return
	}
	var body struct {
		DisplayName  string `json:"displayName"`
		MemorySizeGb int64  `json:"memorySizeGb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.DisplayName != "" {
		existing.DisplayName = body.DisplayName
	}
	if body.MemorySizeGb != 0 {
		existing.MemorySizeGb = body.MemorySizeGb
	}
	if err := s.db.Put(bucketInstances, instanceKey(project, location, instance), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "update", existing)
}

func (s *Service) deleteInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	instance := r.PathValue("instance")
	var existing Instance
	_, _ = s.db.Get(bucketInstances, instanceKey(project, location, instance), &existing)
	if err := s.db.Delete(bucketInstances, instanceKey(project, location, instance)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if existing.Name == "" {
		existing.Name = instanceName(project, location, instance)
	}
	s.writeOperation(w, project, location, "delete", existing)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDefaultInt(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}
