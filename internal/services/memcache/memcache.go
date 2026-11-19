// Package memcache emulates a subset of the Cloud Memorystore for Memcache API
// (memcache.googleapis.com/v1beta2): instance CRUD per location.
// Real instances take minutes to provision; this emulator resolves every
// mutation synchronously and always reports state READY. Mutations return a
// google.longrunning.Operation with done:true.
package memcache

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketInstances = "memcache.instances"

// NodeConfig holds per-node resource config.
type NodeConfig struct {
	CPUCount  int32 `json:"cpuCount,omitempty"`
	MemorySizeMb int32 `json:"memorySizeMb,omitempty"`
}

// Instance mirrors the real memcache#Instance resource.
type Instance struct {
	Name         string            `json:"name,omitempty"`
	DisplayName  string            `json:"displayName,omitempty"`
	NodeCount    int32             `json:"nodeCount,omitempty"`
	NodeConfig   *NodeConfig       `json:"nodeConfig,omitempty"`
	MemcacheVersion string         `json:"memcacheVersion,omitempty"`
	State        string            `json:"state,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	CreateTime   string            `json:"createTime,omitempty"`
	UpdateTime   string            `json:"updateTime,omitempty"`
}

// Operation mirrors google.longrunning.Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Response json.RawMessage `json:"response,omitempty"`
}

// Service is the Memcache emulator service.
type Service struct {
	db  *storage.DB
	seq int64
}

// New creates a new Memcache service backed by db.
func New(db *storage.DB) *Service { return &Service{db: db} }

// Register wires Memcache routes into mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /memcache/v1beta2/projects/{project}/locations/{location}/instances", s.createInstance)
	mux.HandleFunc("GET /memcache/v1beta2/projects/{project}/locations/{location}/instances", s.listInstances)
	mux.HandleFunc("GET /memcache/v1beta2/projects/{project}/locations/{location}/instances/{instance}", s.getInstance)
	mux.HandleFunc("PATCH /memcache/v1beta2/projects/{project}/locations/{location}/instances/{instance}", s.patchInstance)
	mux.HandleFunc("DELETE /memcache/v1beta2/projects/{project}/locations/{location}/instances/{instance}", s.deleteInstance)
}

func instanceKey(project, location, id string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, id)
}

func instanceName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, id)
}

func (s *Service) nextSeq() int64 {
	return atomic.AddInt64(&s.seq, 1)
}

func opName(project, location string, id int64) string {
	return fmt.Sprintf("projects/%s/locations/%s/operations/operation-%016d", project, location, id)
}

func (s *Service) writeOperation(w http.ResponseWriter, project, location string, inst Instance) {
	resp, _ := json.Marshal(inst)
	op := Operation{
		Name:     opName(project, location, s.nextSeq()),
		Done:     true,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	instanceID := r.URL.Query().Get("instanceId")

	var body Instance
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

	now := time.Now().UTC().Format(time.RFC3339)
	body.Name = instanceName(project, location, instanceID)
	body.State = "READY"
	if body.MemcacheVersion == "" {
		body.MemcacheVersion = "MEMCACHE_1_6"
	}
	if body.NodeCount == 0 {
		body.NodeCount = 1
	}
	body.CreateTime = now
	body.UpdateTime = now

	if err := s.db.Put(bucketInstances, instanceKey(project, location, instanceID), body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, body)
}

func (s *Service) listInstances(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)

	items := []Instance{}
	_ = s.db.List(bucketInstances, prefix, func(_ string, raw []byte) error {
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
	id := r.PathValue("instance")

	var inst Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, location, id), &inst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found: "+id)
		return
	}
	server.WriteJSON(w, 200, inst)
}

func (s *Service) patchInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("instance")

	var existing Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, location, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found: "+id)
		return
	}

	var body struct {
		DisplayName string            `json:"displayName"`
		NodeCount   int32             `json:"nodeCount"`
		Labels      map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.DisplayName != "" {
		existing.DisplayName = body.DisplayName
	}
	if body.NodeCount != 0 {
		existing.NodeCount = body.NodeCount
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)

	if err := s.db.Put(bucketInstances, instanceKey(project, location, id), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, existing)
}

func (s *Service) deleteInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("instance")

	var existing Instance
	found, _ := s.db.Get(bucketInstances, instanceKey(project, location, id), &existing)
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found: "+id)
		return
	}
	if err := s.db.Delete(bucketInstances, instanceKey(project, location, id)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, existing)
}
