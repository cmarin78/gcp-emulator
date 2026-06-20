// Package filestore emulates a subset of the Cloud Filestore API
// (file.googleapis.com/v1): instances. FileShares and Networks are modeled
// as nested fields on the Instance resource (matching the real API: they
// aren't separate sub-resources with their own CRUD verbs). Real instances
// take minutes to provision; this emulator resolves every mutation
// synchronously and always reports state READY, following the same
// "shape-compatible, not behavior-complete" approach used elsewhere.
// Mutations return a google.longrunning.Operation, same shape as
// spanner.go/memorystore.go (the real Filestore API uses this too).
//
// Mounted under /file/v1/* rather than the bare /v1/* most other services
// in this emulator share: Filestore's REST resource path
// (projects/{project}/locations/{location}/instances) is byte-for-byte
// identical to Memorystore's, so registering both on the same bare /v1/*
// prefix panics http.ServeMux with a duplicate-route error at startup. The
// real APIs avoid this collision by living on different hosts
// (file.googleapis.com vs redis.googleapis.com); since this emulator
// multiplexes every API onto one process, Filestore gets its own prefix
// instead (same technique already used for Storage at /storage/v1/* and
// Compute at /compute/v1/*). Point the Terraform google provider's
// filestore_custom_endpoint at "<emulator>/file/v1/" accordingly.
package filestore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketInstances = "filestore.instances"

// FileShare mirrors file#FileShareConfig.
type FileShare struct {
	Name       string `json:"name"`
	CapacityGb int64  `json:"capacityGb"`
}

// NetworkConfig mirrors file#NetworkConfig.
type NetworkConfig struct {
	Network         string   `json:"network"`
	Modes           []string `json:"modes,omitempty"`
	ReservedIpRange string   `json:"reservedIpRange,omitempty"`
	IpAddresses     []string `json:"ipAddresses,omitempty"`
}

// Instance mirrors the relevant subset of file#Instance (zonal/regional,
// scoped to a location).
type Instance struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Tier        string            `json:"tier,omitempty"`
	FileShares  []FileShare       `json:"fileShares,omitempty"`
	Networks    []NetworkConfig   `json:"networks,omitempty"`
	State       string            `json:"state,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// Operation mirrors google.longrunning.Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /file/v1/projects/{project}/locations/{location}/instances", s.createInstance)
	mux.HandleFunc("GET /file/v1/projects/{project}/locations/{location}/instances", s.listInstances)
	mux.HandleFunc("GET /file/v1/projects/{project}/locations/{location}/instances/{instance}", s.getInstance)
	mux.HandleFunc("PATCH /file/v1/projects/{project}/locations/{location}/instances/{instance}", s.patchInstance)
	mux.HandleFunc("DELETE /file/v1/projects/{project}/locations/{location}/instances/{instance}", s.deleteInstance)
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

func (s *Service) writeOperation(w http.ResponseWriter, project, location, verb string, inst Instance) {
	meta, _ := json.Marshal(map[string]string{"target": inst.Name, "verb": verb})
	resp, _ := json.Marshal(inst)
	op := Operation{
		Name:     fmt.Sprintf("projects/%s/locations/%s/operations/op-%d", project, location, s.nextID()),
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
	if instanceID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "instanceId is required")
		return
	}
	var body struct {
		Description string            `json:"description"`
		Tier        string            `json:"tier"`
		FileShares  []FileShare       `json:"fileShares"`
		Networks    []NetworkConfig   `json:"networks"`
		Labels      map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if len(body.FileShares) == 0 || body.FileShares[0].Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "fileShares[0].name is required")
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
	shares := body.FileShares
	for i := range shares {
		if shares[i].CapacityGb == 0 {
			shares[i].CapacityGb = 1024
		}
	}
	networks := body.Networks
	for i := range networks {
		if len(networks[i].IpAddresses) == 0 {
			networks[i].IpAddresses = []string{fmt.Sprintf("10.%d.%d.2", len(instanceID)%255, (i+1)%255)}
		}
	}
	inst := Instance{
		Name:        instanceName(project, location, instanceID),
		Description: body.Description,
		Tier:        orDefault(body.Tier, "BASIC_HDD"),
		FileShares:  shares,
		Networks:    networks,
		State:       "READY",
		CreateTime:  time.Now().UTC().Format(time.RFC3339),
		Labels:      body.Labels,
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
		Description string            `json:"description"`
		FileShares  []FileShare       `json:"fileShares"`
		Labels      map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Description != "" {
		existing.Description = body.Description
	}
	if len(body.FileShares) > 0 {
		existing.FileShares = body.FileShares
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
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
