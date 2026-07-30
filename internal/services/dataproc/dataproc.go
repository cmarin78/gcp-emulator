// Package dataproc emulates a subset of the Cloud Dataproc API
// (dataproc.googleapis.com/v1): cluster CRUD per region.
// Real clusters take several minutes to provision; this emulator resolves
// every mutation synchronously and always reports state RUNNING, following
// the same "shape-compatible, not behavior-complete" approach used elsewhere.
// Mutations return a google.longrunning.Operation with done:true.
package dataproc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketClusters = "dataproc.clusters"

// ClusterStatus mirrors the real Dataproc ClusterStatus shape.
type ClusterStatus struct {
	State          string `json:"state,omitempty"`
	StateStartTime string `json:"stateStartTime,omitempty"`
}

// GceClusterConfig holds a minimal subset of GceClusterConfig fields.
type GceClusterConfig struct {
	ZoneURI       string `json:"zoneUri,omitempty"`
	NetworkURI    string `json:"networkUri,omitempty"`
	SubnetworkURI string `json:"subnetworkUri,omitempty"`
}

// InstanceGroupConfig holds the master / worker node count + machine type.
type InstanceGroupConfig struct {
	NumInstances   int64  `json:"numInstances,omitempty"`
	MachineTypeURI string `json:"machineTypeUri,omitempty"`
}

// ClusterConfig mirrors the real Dataproc ClusterConfig shape (subset).
type ClusterConfig struct {
	GceClusterConfig    *GceClusterConfig    `json:"gceClusterConfig,omitempty"`
	MasterConfig        *InstanceGroupConfig `json:"masterConfig,omitempty"`
	WorkerConfig        *InstanceGroupConfig `json:"workerConfig,omitempty"`
	SecondaryWorkerConfig *InstanceGroupConfig `json:"secondaryWorkerConfig,omitempty"`
}

// Cluster mirrors the real dataproc#Cluster resource.
type Cluster struct {
	ProjectID   string            `json:"projectId,omitempty"`
	ClusterName string            `json:"clusterName,omitempty"`
	ClusterUUID string            `json:"clusterUuid,omitempty"`
	Config      *ClusterConfig    `json:"config,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Status      ClusterStatus     `json:"status,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	UpdateTime  string            `json:"updateTime,omitempty"`
}

// Operation mirrors google.longrunning.Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type operationMetadata struct {
	ClusterName string `json:"clusterName"`
	ClusterUUID string `json:"clusterUuid"`
	Status      string `json:"status"`
}

// Service is the Dataproc emulator service.
type Service struct {
	db  *storage.DB
	seq int64
}

// New creates a new Dataproc service backed by db.
func New(db *storage.DB) *Service { return &Service{db: db} }

// Register wires Dataproc routes into mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /dataproc/v1/projects/{project}/regions/{region}/clusters", s.createCluster)
	mux.HandleFunc("GET /dataproc/v1/projects/{project}/regions/{region}/clusters", s.listClusters)
	mux.HandleFunc("GET /dataproc/v1/projects/{project}/regions/{region}/clusters/{cluster}", s.getCluster)
	mux.HandleFunc("PATCH /dataproc/v1/projects/{project}/regions/{region}/clusters/{cluster}", s.patchCluster)
	mux.HandleFunc("DELETE /dataproc/v1/projects/{project}/regions/{region}/clusters/{cluster}", s.deleteCluster)
}

func clusterKey(project, region, name string) string {
	return fmt.Sprintf("%s/%s/%s", project, region, name)
}

func (s *Service) nextSeq() int64 {
	s.seq++
	return s.seq
}

func opName(project, region string, id int64) string {
	return fmt.Sprintf("projects/%s/regions/%s/operations/operation-%016d", project, region, id)
}

func (s *Service) writeOperation(w http.ResponseWriter, project, region string, cluster Cluster) {
	meta, _ := json.Marshal(operationMetadata{
		ClusterName: cluster.ClusterName,
		ClusterUUID: cluster.ClusterUUID,
		Status:      "DONE",
	})
	resp, _ := json.Marshal(cluster)
	op := Operation{
		Name:     opName(project, region, s.nextSeq()),
		Done:     true,
		Metadata: meta,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")

	var body Cluster
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.ClusterName == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "clusterName is required")
		return
	}

	var existing Cluster
	found, err := s.db.Get(bucketClusters, clusterKey(project, region, body.ClusterName), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "cluster already exists: "+body.ClusterName)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	body.ProjectID = project
	body.ClusterUUID = fmt.Sprintf("uuid-%016d", s.nextSeq())
	body.Status = ClusterStatus{State: "RUNNING", StateStartTime: now}
	body.CreateTime = now
	body.UpdateTime = now

	if err := s.db.Put(bucketClusters, clusterKey(project, region, body.ClusterName), body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, region, body)
}

func (s *Service) listClusters(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	prefix := fmt.Sprintf("%s/%s/", project, region)

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
	region := r.PathValue("region")
	name := r.PathValue("cluster")

	var c Cluster
	found, err := s.db.Get(bucketClusters, clusterKey(project, region, name), &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cluster not found: "+name)
		return
	}
	server.WriteJSON(w, 200, c)
}

func (s *Service) patchCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	name := r.PathValue("cluster")

	var existing Cluster
	found, err := s.db.Get(bucketClusters, clusterKey(project, region, name), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cluster not found: "+name)
		return
	}

	var body struct {
		Labels  map[string]string `json:"labels"`
		Config  *ClusterConfig    `json:"config"`
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

	if err := s.db.Put(bucketClusters, clusterKey(project, region, name), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, region, existing)
}

func (s *Service) deleteCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	name := r.PathValue("cluster")

	var existing Cluster
	found, _ := s.db.Get(bucketClusters, clusterKey(project, region, name), &existing)
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cluster not found: "+name)
		return
	}

	if err := s.db.Delete(bucketClusters, clusterKey(project, region, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	existing.Status = ClusterStatus{State: "DELETING"}
	s.writeOperation(w, project, region, existing)
}
