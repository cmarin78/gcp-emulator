// Package bigtable emulates a subset of the Cloud Bigtable Admin API
// (bigtableadmin.googleapis.com/v2): instances, clusters per instance, and
// app profiles per instance. No real Bigtable storage is emulated — this is
// shape-compatible only, following the same approach used elsewhere in this
// project. Mutations return a google.longrunning.Operation with done:true.
package bigtable

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketInstances   = "bigtable.instances"
	bucketClusters    = "bigtable.clusters"
	bucketAppProfiles = "bigtable.appprofiles"
)

// Instance mirrors the real bigtable#Instance resource.
type Instance struct {
	Name        string            `json:"name,omitempty"`
	DisplayName string            `json:"displayName,omitempty"`
	State       string            `json:"state,omitempty"`
	Type        string            `json:"type,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
}

// Cluster mirrors the real bigtable#Cluster resource.
type Cluster struct {
	Name             string `json:"name,omitempty"`
	Location         string `json:"location,omitempty"`
	State            string `json:"state,omitempty"`
	ServeNodes       int32  `json:"serveNodes,omitempty"`
	StorageType      string `json:"defaultStorageType,omitempty"`
}

// AppProfile mirrors the real bigtable#AppProfile resource.
type AppProfile struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Etag        string `json:"etag,omitempty"`
}

// Operation mirrors google.longrunning.Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Response json.RawMessage `json:"response,omitempty"`
}

// Service is the Bigtable emulator service.
type Service struct {
	db  *storage.DB
	seq int64
}

// New creates a new Bigtable service backed by db.
func New(db *storage.DB) *Service { return &Service{db: db} }

// Register wires Bigtable routes into mux.
func (s *Service) Register(mux *http.ServeMux) {
	// Instances.
	mux.HandleFunc("POST /bigtable/v2/projects/{project}/instances", s.createInstance)
	mux.HandleFunc("GET /bigtable/v2/projects/{project}/instances", s.listInstances)
	mux.HandleFunc("GET /bigtable/v2/projects/{project}/instances/{instance}", s.getInstance)
	mux.HandleFunc("PATCH /bigtable/v2/projects/{project}/instances/{instance}", s.patchInstance)
	mux.HandleFunc("DELETE /bigtable/v2/projects/{project}/instances/{instance}", s.deleteInstance)

	// Clusters.
	mux.HandleFunc("POST /bigtable/v2/projects/{project}/instances/{instance}/clusters", s.createCluster)
	mux.HandleFunc("GET /bigtable/v2/projects/{project}/instances/{instance}/clusters", s.listClusters)
	mux.HandleFunc("GET /bigtable/v2/projects/{project}/instances/{instance}/clusters/{cluster}", s.getCluster)
	mux.HandleFunc("DELETE /bigtable/v2/projects/{project}/instances/{instance}/clusters/{cluster}", s.deleteCluster)

	// App profiles.
	mux.HandleFunc("POST /bigtable/v2/projects/{project}/instances/{instance}/appProfiles", s.createAppProfile)
	mux.HandleFunc("GET /bigtable/v2/projects/{project}/instances/{instance}/appProfiles", s.listAppProfiles)
	mux.HandleFunc("GET /bigtable/v2/projects/{project}/instances/{instance}/appProfiles/{profile}", s.getAppProfile)
	mux.HandleFunc("PATCH /bigtable/v2/projects/{project}/instances/{instance}/appProfiles/{profile}", s.patchAppProfile)
	mux.HandleFunc("DELETE /bigtable/v2/projects/{project}/instances/{instance}/appProfiles/{profile}", s.deleteAppProfile)
}

func (s *Service) nextSeq() int64 {
	return atomic.AddInt64(&s.seq, 1)
}

func opName(project string, id int64) string {
	return fmt.Sprintf("operations/projects/%s/instances/op-%016d", project, id)
}

func (s *Service) writeOperation(w http.ResponseWriter, project string, v any) {
	resp, _ := json.Marshal(v)
	op := Operation{
		Name:     opName(project, s.nextSeq()),
		Done:     true,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

// ── Instances ────────────────────────────────────────────────────────────────

func instanceKey(project, id string) string { return project + "/" + id }

func instanceName(project, id string) string {
	return fmt.Sprintf("projects/%s/instances/%s", project, id)
}

func instanceIDFromName(name string) string {
	parts := strings.Split(name, "/")
	return parts[len(parts)-1]
}

func (s *Service) createInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")

	var body struct {
		InstanceID string   `json:"instanceId"`
		Instance   Instance `json:"instance"`
		Clusters   map[string]Cluster `json:"clusters"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.InstanceID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "instanceId is required")
		return
	}

	var existing Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, body.InstanceID), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "instance already exists: "+body.InstanceID)
		return
	}

	inst := body.Instance
	inst.Name = instanceName(project, body.InstanceID)
	inst.State = "READY"
	if inst.Type == "" {
		inst.Type = "PRODUCTION"
	}
	inst.CreateTime = time.Now().UTC().Format(time.RFC3339)

	if err := s.db.Put(bucketInstances, instanceKey(project, body.InstanceID), inst); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	// Store any initial clusters.
	for clusterID, cl := range body.Clusters {
		cl.Name = fmt.Sprintf("projects/%s/instances/%s/clusters/%s", project, body.InstanceID, clusterID)
		cl.State = "READY"
		_ = s.db.Put(bucketClusters, fmt.Sprintf("%s/%s/%s", project, body.InstanceID, clusterID), cl)
	}

	s.writeOperation(w, project, inst)
}

func (s *Service) listInstances(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := project + "/"
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
	id := r.PathValue("instance")
	var inst Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, id), &inst)
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
	id := r.PathValue("instance")
	var existing Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, id), &existing)
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
		Labels      map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.DisplayName != "" {
		existing.DisplayName = body.DisplayName
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	if err := s.db.Put(bucketInstances, instanceKey(project, id), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Service) deleteInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("instance")
	var existing Instance
	found, _ := s.db.Get(bucketInstances, instanceKey(project, id), &existing)
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found: "+id)
		return
	}
	if err := s.db.Delete(bucketInstances, instanceKey(project, id)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	// Cascade delete child clusters and app profiles
	clusterPrefix := fmt.Sprintf("%s/%s/", project, id)
	var clusterKeys []string
	_ = s.db.List(bucketClusters, clusterPrefix, func(key string, _ []byte) error {
		clusterKeys = append(clusterKeys, key)
		return nil
	})
	for _, k := range clusterKeys {
		_ = s.db.Delete(bucketClusters, k)
	}

	var profileKeys []string
	_ = s.db.List(bucketAppProfiles, clusterPrefix, func(key string, _ []byte) error {
		profileKeys = append(profileKeys, key)
		return nil
	})
	for _, k := range profileKeys {
		_ = s.db.Delete(bucketAppProfiles, k)
	}

	server.WriteJSON(w, 200, map[string]any{})
}

// ── Clusters ─────────────────────────────────────────────────────────────────

func clusterKey(project, instance, id string) string {
	return fmt.Sprintf("%s/%s/%s", project, instance, id)
}

func (s *Service) createCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	clusterID := r.URL.Query().Get("clusterId")

	var body Cluster
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if clusterID == "" {
		// Fall back to id derived from body.Name if provided.
		clusterID = instanceIDFromName(body.Name)
	}
	if clusterID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "clusterId is required")
		return
	}

	body.Name = fmt.Sprintf("projects/%s/instances/%s/clusters/%s", project, instance, clusterID)
	body.State = "READY"
	if body.ServeNodes == 0 {
		body.ServeNodes = 1
	}
	if body.StorageType == "" {
		body.StorageType = "SSD"
	}

	if err := s.db.Put(bucketClusters, clusterKey(project, instance, clusterID), body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, body)
}

func (s *Service) listClusters(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	prefix := fmt.Sprintf("%s/%s/", project, instance)
	items := []Cluster{}
	_ = s.db.List(bucketClusters, prefix, func(_ string, raw []byte) error {
		var c Cluster
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		items = append(items, c)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"clusters": items})
}

func (s *Service) getCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	id := r.PathValue("cluster")
	var c Cluster
	found, err := s.db.Get(bucketClusters, clusterKey(project, instance, id), &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cluster not found: "+id)
		return
	}
	server.WriteJSON(w, 200, c)
}

func (s *Service) deleteCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	id := r.PathValue("cluster")
	var existing Cluster
	found, _ := s.db.Get(bucketClusters, clusterKey(project, instance, id), &existing)
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cluster not found: "+id)
		return
	}
	if err := s.db.Delete(bucketClusters, clusterKey(project, instance, id)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

// ── App Profiles ─────────────────────────────────────────────────────────────

func appProfileKey(project, instance, id string) string {
	return fmt.Sprintf("%s/%s/%s", project, instance, id)
}

func appProfileName(project, instance, id string) string {
	return fmt.Sprintf("projects/%s/instances/%s/appProfiles/%s", project, instance, id)
}

func (s *Service) createAppProfile(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	profileID := r.URL.Query().Get("appProfileId")

	var body AppProfile
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if profileID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "appProfileId is required")
		return
	}

	body.Name = appProfileName(project, instance, profileID)
	body.Etag = fmt.Sprintf("etag-%d", s.nextSeq())

	if err := s.db.Put(bucketAppProfiles, appProfileKey(project, instance, profileID), body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, body)
}

func (s *Service) listAppProfiles(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	prefix := fmt.Sprintf("%s/%s/", project, instance)
	items := []AppProfile{}
	_ = s.db.List(bucketAppProfiles, prefix, func(_ string, raw []byte) error {
		var ap AppProfile
		if err := json.Unmarshal(raw, &ap); err != nil {
			return err
		}
		items = append(items, ap)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"appProfiles": items})
}

func (s *Service) getAppProfile(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	id := r.PathValue("profile")
	var ap AppProfile
	found, err := s.db.Get(bucketAppProfiles, appProfileKey(project, instance, id), &ap)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "app profile not found: "+id)
		return
	}
	server.WriteJSON(w, 200, ap)
}

func (s *Service) patchAppProfile(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	id := r.PathValue("profile")
	var existing AppProfile
	found, err := s.db.Get(bucketAppProfiles, appProfileKey(project, instance, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "app profile not found: "+id)
		return
	}
	var body AppProfile
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Description != "" {
		existing.Description = body.Description
	}
	existing.Etag = fmt.Sprintf("etag-%d", s.nextSeq())
	if err := s.db.Put(bucketAppProfiles, appProfileKey(project, instance, id), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, existing)
}

func (s *Service) deleteAppProfile(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	id := r.PathValue("profile")
	var existing AppProfile
	found, _ := s.db.Get(bucketAppProfiles, appProfileKey(project, instance, id), &existing)
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "app profile not found: "+id)
		return
	}
	if err := s.db.Delete(bucketAppProfiles, appProfileKey(project, instance, id)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}
