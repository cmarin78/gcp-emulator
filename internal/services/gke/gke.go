// Package gke emulates a subset of the Google Kubernetes Engine API
// (container.googleapis.com/v1): clusters and nodePools. There is no real
// Kubernetes control plane behind this emulator — clusters and node pools
// are just shape-compatible records that always report status RUNNING,
// following the same "shape-compatible, not behavior-complete" approach
// used by the other CRUD-with-Operation services in this project. Mutations
// return a container#operation, the real GKE API's own Operation shape
// (distinct from both sqladmin#operation and google.longrunning.Operation).
package gke

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketClusters  = "gke.clusters"
	bucketNodePools = "gke.nodePools"
)

// NodeConfig mirrors the real container#NodeConfig subset most commonly set.
type NodeConfig struct {
	MachineType string   `json:"machineType,omitempty"`
	DiskSizeGb  int64    `json:"diskSizeGb,omitempty"`
	OauthScopes []string `json:"oauthScopes,omitempty"`
}

// NodePool mirrors the real container#NodePool resource, scoped to a parent
// cluster.
type NodePool struct {
	Name             string     `json:"name"`
	Config           NodeConfig `json:"config,omitempty"`
	InitialNodeCount int64      `json:"initialNodeCount,omitempty"`
	Status           string     `json:"status,omitempty"`
	SelfLink         string     `json:"selfLink,omitempty"`
}

// Cluster mirrors the real container#Cluster resource (location-scoped:
// location may be a zone or a region).
type Cluster struct {
	Name                 string     `json:"name"`
	Location             string     `json:"location,omitempty"`
	InitialNodeCount     int64      `json:"initialNodeCount,omitempty"`
	NodeConfig           NodeConfig `json:"nodeConfig,omitempty"`
	Network              string     `json:"network,omitempty"`
	Subnetwork           string     `json:"subnetwork,omitempty"`
	Status               string     `json:"status,omitempty"`
	Endpoint             string     `json:"endpoint,omitempty"`
	CurrentMasterVersion string     `json:"currentMasterVersion,omitempty"`
	SelfLink             string     `json:"selfLink,omitempty"`
	CreateTime           string     `json:"createTime,omitempty"`
}

// Operation mirrors container#operation, the real GKE API's own Operation
// shape (different from both sqladmin#operation and
// google.longrunning.Operation, which is why it's modeled separately here
// rather than reusing cloudsql.go's or cloudbuild.go's Operation type).
type Operation struct {
	Name          string `json:"name"`
	Zone          string `json:"zone,omitempty"`
	OperationType string `json:"operationType"`
	Status        string `json:"status"`
	SelfLink      string `json:"selfLink,omitempty"`
	TargetLink    string `json:"targetLink,omitempty"`
	StartTime     string `json:"startTime,omitempty"`
	EndTime       string `json:"endTime,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/clusters", s.createCluster)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/clusters", s.listClusters)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/clusters/{cluster}", s.getCluster)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/clusters/{cluster}", s.deleteCluster)

	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/clusters/{cluster}/nodePools", s.createNodePool)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/clusters/{cluster}/nodePools", s.listNodePools)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/clusters/{cluster}/nodePools/{nodePool}", s.getNodePool)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/clusters/{cluster}/nodePools/{nodePool}", s.deleteNodePool)

	// Note: no dedicated GET .../operations/{operation} route here — that
	// exact path pattern is already registered by artifactregistry.go on
	// the shared /v1/* mux, and http.ServeMux panics on duplicate patterns.
	// Not a problem in practice: every mutation above already resolves
	// synchronously and returns status DONE in its own response, so
	// clients have no real reason to poll.
}

func clusterKey(project, location, cluster string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, cluster)
}

func nodePoolKey(project, location, cluster, nodePool string) string {
	return fmt.Sprintf("%s/%s/%s/%s", project, location, cluster, nodePool)
}

func (s *Service) nextID() int64 {
	s.seq++
	return s.seq
}

func (s *Service) writeOperation(w http.ResponseWriter, project, location, opType, targetLink string) {
	now := time.Now().UTC().Format(time.RFC3339)
	name := fmt.Sprintf("op-%d", s.nextID())
	op := Operation{
		Name:          name,
		Zone:          location,
		OperationType: opType,
		Status:        "DONE",
		SelfLink:      fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, name),
		TargetLink:    targetLink,
		StartTime:     now,
		EndTime:       now,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	var body struct {
		Cluster Cluster `json:"cluster"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Cluster.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "cluster.name is required")
		return
	}
	c := body.Cluster
	c.Location = location
	c.Status = "RUNNING"
	c.Endpoint = fmt.Sprintf("10.%d.%d.1", len(c.Name)%255, len(project)%255)
	c.CurrentMasterVersion = orDefault(c.CurrentMasterVersion, "1.30.0-gke.0")
	c.SelfLink = fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, c.Name)
	c.CreateTime = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.Put(bucketClusters, clusterKey(project, location, c.Name), c); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "CREATE_CLUSTER", c.SelfLink)
}

func (s *Service) listClusters(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)
	items := []Cluster{}
	_ = s.db.List(bucketClusters, prefix, func(key string, raw []byte) error {
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
	location := r.PathValue("location")
	cluster := r.PathValue("cluster")
	var c Cluster
	found, err := s.db.Get(bucketClusters, clusterKey(project, location, cluster), &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cluster not found")
		return
	}
	server.WriteJSON(w, 200, c)
}

func (s *Service) deleteCluster(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	cluster := r.PathValue("cluster")
	if err := s.db.Delete(bucketClusters, clusterKey(project, location, cluster)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "DELETE_CLUSTER", fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, cluster))
}

func (s *Service) createNodePool(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	cluster := r.PathValue("cluster")
	var body struct {
		NodePool NodePool `json:"nodePool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.NodePool.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "nodePool.name is required")
		return
	}
	np := body.NodePool
	np.Status = "RUNNING"
	if np.InitialNodeCount == 0 {
		np.InitialNodeCount = 3
	}
	np.SelfLink = fmt.Sprintf("projects/%s/locations/%s/clusters/%s/nodePools/%s", project, location, cluster, np.Name)
	if err := s.db.Put(bucketNodePools, nodePoolKey(project, location, cluster, np.Name), np); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "CREATE_NODE_POOL", np.SelfLink)
}

func (s *Service) listNodePools(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	cluster := r.PathValue("cluster")
	prefix := fmt.Sprintf("%s/%s/%s/", project, location, cluster)
	items := []NodePool{}
	_ = s.db.List(bucketNodePools, prefix, func(key string, raw []byte) error {
		var np NodePool
		if err := json.Unmarshal(raw, &np); err != nil {
			return err
		}
		items = append(items, np)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"nodePools": items})
}

func (s *Service) getNodePool(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	cluster := r.PathValue("cluster")
	nodePool := r.PathValue("nodePool")
	var np NodePool
	found, err := s.db.Get(bucketNodePools, nodePoolKey(project, location, cluster, nodePool), &np)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "node pool not found")
		return
	}
	server.WriteJSON(w, 200, np)
}

func (s *Service) deleteNodePool(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	cluster := r.PathValue("cluster")
	nodePool := r.PathValue("nodePool")
	if err := s.db.Delete(bucketNodePools, nodePoolKey(project, location, cluster, nodePool)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "DELETE_NODE_POOL", fmt.Sprintf("projects/%s/locations/%s/clusters/%s/nodePools/%s", project, location, cluster, nodePool))
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
