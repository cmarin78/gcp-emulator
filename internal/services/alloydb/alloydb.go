// Package alloydb emulates a subset of the AlloyDB for PostgreSQL API
// (alloydb.googleapis.com/v1): clusters and instances (google_alloydb_cluster
// and google_alloydb_instance in Terraform).
package alloydb

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
	bucketClusters  = "alloydb.clusters"
	bucketInstances = "alloydb.instances"
)

// NetworkConfig specifies the VPC network for the AlloyDB cluster.
type NetworkConfig struct {
	Network           string `json:"network,omitempty"`
	AllocatedIPRange  string `json:"allocatedIpRange,omitempty"`
}

// Cluster represents an AlloyDB cluster resource.
type Cluster struct {
	Name          string         `json:"name"` // projects/{project}/locations/{location}/clusters/{cluster}
	DisplayName   string         `json:"displayName,omitempty"`
	ClusterType   string         `json:"clusterType,omitempty"` // PRIMARY | SECONDARY
	NetworkConfig *NetworkConfig `json:"networkConfig,omitempty"`
	State         string         `json:"state,omitempty"` // READY
	Labels        map[string]string `json:"labels,omitempty"`
	CreateTime    string         `json:"createTime,omitempty"`
	UID           string         `json:"uid,omitempty"`
}

// Instance represents an AlloyDB instance within a cluster.
type Instance struct {
	Name         string            `json:"name"` // projects/{project}/locations/{location}/clusters/{cluster}/instances/{instance}
	DisplayName  string            `json:"displayName,omitempty"`
	InstanceType string            `json:"instanceType,omitempty"` // PRIMARY | READ_POOL
	State        string            `json:"state,omitempty"`        // READY
	IPAddress    string            `json:"ipAddress,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	CreateTime   string            `json:"createTime,omitempty"`
	UID          string            `json:"uid,omitempty"`
}

// Operation mirrors standard google.longrunning.Operation.
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

func New(db *storage.DB) *Service {
	return &Service{db: db}
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /alloydb/v1/projects/{project}/locations/{location}/clusters", s.createCluster)
	mux.HandleFunc("GET /alloydb/v1/projects/{project}/locations/{location}/clusters", s.listClusters)
	mux.HandleFunc("GET /alloydb/v1/projects/{project}/locations/{location}/clusters/{cluster}", s.getCluster)
	mux.HandleFunc("PATCH /alloydb/v1/projects/{project}/locations/{location}/clusters/{cluster}", s.patchCluster)
	mux.HandleFunc("DELETE /alloydb/v1/projects/{project}/locations/{location}/clusters/{cluster}", s.deleteCluster)

	mux.HandleFunc("POST /alloydb/v1/projects/{project}/locations/{location}/clusters/{cluster}/instances", s.createInstance)
	mux.HandleFunc("GET /alloydb/v1/projects/{project}/locations/{location}/clusters/{cluster}/instances", s.listInstances)
	mux.HandleFunc("GET /alloydb/v1/projects/{project}/locations/{location}/clusters/{cluster}/instances/{instance}", s.getInstance)
	mux.HandleFunc("PATCH /alloydb/v1/projects/{project}/locations/{location}/clusters/{cluster}/instances/{instance}", s.patchInstance)
	mux.HandleFunc("DELETE /alloydb/v1/projects/{project}/locations/{location}/clusters/{cluster}/instances/{instance}", s.deleteInstance)
}

func clusterKey(project, location, cluster string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, cluster)
}

func clusterName(project, location, cluster string) string {
	return fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, cluster)
}

func instanceKey(project, location, cluster, instance string) string {
	return fmt.Sprintf("%s/%s/%s/%s", project, location, cluster, instance)
}

func instanceName(project, location, cluster, instance string) string {
	return fmt.Sprintf("projects/%s/locations/%s/clusters/%s/instances/%s", project, location, cluster, instance)
}

func (s *Service) nextID() int64 {
	return atomic.AddInt64(&s.seq, 1)
}

func (s *Service) writeOp(w http.ResponseWriter, project, location string, res any) {
	respBytes, _ := json.Marshal(res)
	op := Operation{
		Name:     fmt.Sprintf("projects/%s/locations/%s/operations/op-%d", project, location, s.nextID()),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	clusterID := r.URL.Query().Get("clusterId")

	var c Cluster
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if clusterID == "" {
		if c.Name != "" {
			parts := strings.Split(c.Name, "/")
			clusterID = parts[len(parts)-1]
		}
	}
	if clusterID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "clusterId is required")
		return
	}

	key := clusterKey(project, location, clusterID)
	var existing Cluster
	found, err := s.db.Get(bucketClusters, key, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "cluster already exists: "+clusterID)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	c.Name = clusterName(project, location, clusterID)
	c.State = "READY"
	if c.ClusterType == "" {
		c.ClusterType = "PRIMARY"
	}
	c.CreateTime = now
	c.UID = fmt.Sprintf("alloydb-%d", s.nextID())

	if err := s.db.Put(bucketClusters, key, c); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, c)
}

func (s *Service) getCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	clusterID := r.PathValue("cluster")

	var c Cluster
	found, err := s.db.Get(bucketClusters, clusterKey(project, location, clusterID), &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cluster not found: "+clusterID)
		return
	}
	server.WriteJSON(w, 200, c)
}

func (s *Service) listClusters(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)

	var list []Cluster
	err := s.db.List(bucketClusters, prefix, func(key string, raw []byte) error {
		var c Cluster
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		list = append(list, c)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if list == nil {
		list = []Cluster{}
	}
	server.WriteJSON(w, 200, map[string]any{"clusters": list})
}

func (s *Service) patchCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	clusterID := r.PathValue("cluster")

	key := clusterKey(project, location, clusterID)
	var c Cluster
	found, err := s.db.Get(bucketClusters, key, &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cluster not found: "+clusterID)
		return
	}

	var patch Cluster
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if patch.DisplayName != "" {
		c.DisplayName = patch.DisplayName
	}
	if patch.Labels != nil {
		c.Labels = patch.Labels
	}
	if err := s.db.Put(bucketClusters, key, c); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, c)
}

func (s *Service) deleteCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	clusterID := r.PathValue("cluster")

	key := clusterKey(project, location, clusterID)
	var c Cluster
	found, err := s.db.Get(bucketClusters, key, &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cluster not found: "+clusterID)
		return
	}

	// Cascading delete to child instances: buffer keys first to avoid BoltDB view deadlock
	instPrefix := fmt.Sprintf("%s/%s/%s/", project, location, clusterID)
	var instKeys []string
	_ = s.db.List(bucketInstances, instPrefix, func(k string, raw []byte) error {
		instKeys = append(instKeys, k)
		return nil
	})
	for _, ik := range instKeys {
		_ = s.db.Delete(bucketInstances, ik)
	}

	if err := s.db.Delete(bucketClusters, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, map[string]any{})
}

func (s *Service) createInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	clusterID := r.PathValue("cluster")
	instanceID := r.URL.Query().Get("instanceId")

	// Cluster must exist
	var c Cluster
	found, err := s.db.Get(bucketClusters, clusterKey(project, location, clusterID), &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cluster not found: "+clusterID)
		return
	}

	var inst Instance
	if err := json.NewDecoder(r.Body).Decode(&inst); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if instanceID == "" {
		if inst.Name != "" {
			parts := strings.Split(inst.Name, "/")
			instanceID = parts[len(parts)-1]
		}
	}
	if instanceID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "instanceId is required")
		return
	}

	key := instanceKey(project, location, clusterID, instanceID)
	var existing Instance
	found, err = s.db.Get(bucketInstances, key, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "instance already exists: "+instanceID)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	inst.Name = instanceName(project, location, clusterID, instanceID)
	inst.State = "READY"
	if inst.InstanceType == "" {
		inst.InstanceType = "PRIMARY"
	}
	if inst.IPAddress == "" {
		inst.IPAddress = fmt.Sprintf("10.0.%d.%d", (s.nextID()%250)+1, (s.nextID()%250)+1)
	}
	inst.CreateTime = now
	inst.UID = fmt.Sprintf("alloydb-inst-%d", s.nextID())

	if err := s.db.Put(bucketInstances, key, inst); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, inst)
}

func (s *Service) getInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	clusterID := r.PathValue("cluster")
	instanceID := r.PathValue("instance")

	var inst Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, location, clusterID, instanceID), &inst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found: "+instanceID)
		return
	}
	server.WriteJSON(w, 200, inst)
}

func (s *Service) listInstances(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	clusterID := r.PathValue("cluster")
	prefix := fmt.Sprintf("%s/%s/%s/", project, location, clusterID)

	var list []Instance
	err := s.db.List(bucketInstances, prefix, func(key string, raw []byte) error {
		var inst Instance
		if err := json.Unmarshal(raw, &inst); err != nil {
			return err
		}
		list = append(list, inst)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if list == nil {
		list = []Instance{}
	}
	server.WriteJSON(w, 200, map[string]any{"instances": list})
}

func (s *Service) patchInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	clusterID := r.PathValue("cluster")
	instanceID := r.PathValue("instance")

	key := instanceKey(project, location, clusterID, instanceID)
	var inst Instance
	found, err := s.db.Get(bucketInstances, key, &inst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found: "+instanceID)
		return
	}

	var patch Instance
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if patch.DisplayName != "" {
		inst.DisplayName = patch.DisplayName
	}
	if patch.Labels != nil {
		inst.Labels = patch.Labels
	}
	if err := s.db.Put(bucketInstances, key, inst); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, inst)
}

func (s *Service) deleteInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	clusterID := r.PathValue("cluster")
	instanceID := r.PathValue("instance")

	key := instanceKey(project, location, clusterID, instanceID)
	var inst Instance
	found, err := s.db.Get(bucketInstances, key, &inst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found: "+instanceID)
		return
	}
	if err := s.db.Delete(bucketInstances, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, map[string]any{})
}
