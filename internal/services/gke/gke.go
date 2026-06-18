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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

// fakeFingerprint produces a syntactically valid base64 fingerprint, same
// approach as loadbalancing/securitypolicy.go's helper of the same name.
// Real clients (gcloud, Terraform) decode fingerprint-like fields as base64
// and error out on anything else.
func fakeFingerprint(seed string) string {
	return base64.StdEncoding.EncodeToString([]byte(seed))
}

const (
	bucketClusters  = "gke.clusters"
	bucketNodePools = "gke.nodePools"
)

// WorkloadMetadataConfig mirrors container#WorkloadMetadataConfig. Every
// node pool on a modern real GKE cluster has one (metadata concealment via
// GKE_METADATA has been the default since GKE 1.24), and the Terraform
// provider's node pool flattener reads its Mode unconditionally — omitting
// this struct entirely is one of the causes of the provider's nil-pointer
// panic against this emulator.
type WorkloadMetadataConfig struct {
	Mode string `json:"mode"`
}

// ShieldedInstanceConfig mirrors container#ShieldedInstanceConfig, also
// always present on real node pools (Shielded GKE Nodes is the default).
type ShieldedInstanceConfig struct {
	EnableSecureBoot          bool `json:"enableSecureBoot"`
	EnableIntegrityMonitoring bool `json:"enableIntegrityMonitoring"`
}

// NodeConfig mirrors the real container#NodeConfig subset most commonly set.
type NodeConfig struct {
	MachineType            string                  `json:"machineType,omitempty"`
	DiskSizeGb              int64                   `json:"diskSizeGb,omitempty"`
	OauthScopes             []string                `json:"oauthScopes,omitempty"`
	WorkloadMetadataConfig  *WorkloadMetadataConfig `json:"workloadMetadataConfig,omitempty"`
	ShieldedInstanceConfig  *ShieldedInstanceConfig `json:"shieldedInstanceConfig,omitempty"`
}

// NodePool mirrors the real container#NodePool resource, scoped to a parent
// cluster.
type NodePool struct {
	Name              string     `json:"name"`
	Config            NodeConfig `json:"config,omitempty"`
	InitialNodeCount  int64      `json:"initialNodeCount,omitempty"`
	Status            string     `json:"status,omitempty"`
	SelfLink          string     `json:"selfLink,omitempty"`
	Version           string     `json:"version,omitempty"`
	InstanceGroupUrls []string   `json:"instanceGroupUrls"`
}

// MasterAuth mirrors container#MasterAuth. Real GKE always issues a cluster
// CA certificate, so this is never absent on a real cluster response.
type MasterAuth struct {
	ClusterCaCertificate string `json:"clusterCaCertificate"`
}

// AddonsConfigItem-style sub-blocks: real GKE always reports the on/off
// state of every built-in addon, never omits the parent blocks.
type httpLoadBalancing struct {
	Disabled bool `json:"disabled"`
}
type horizontalPodAutoscaling struct {
	Disabled bool `json:"disabled"`
}
type networkPolicyConfig struct {
	Disabled bool `json:"disabled"`
}

// AddonsConfig mirrors container#AddonsConfig.
type AddonsConfig struct {
	HttpLoadBalancing        httpLoadBalancing        `json:"httpLoadBalancing"`
	HorizontalPodAutoscaling horizontalPodAutoscaling `json:"horizontalPodAutoscaling"`
	NetworkPolicyConfig      networkPolicyConfig      `json:"networkPolicyConfig"`
}

// IPAllocationPolicy mirrors container#IPAllocationPolicy. VPC-native
// (alias IP) clusters are the modern default and always report a
// non-empty clusterIpv4Cidr/servicesIpv4Cidr pair.
type IPAllocationPolicy struct {
	UseIpAliases    bool   `json:"useIpAliases"`
	ClusterIpv4Cidr string `json:"clusterIpv4Cidr"`
	ServicesIpv4Cidr string `json:"servicesIpv4Cidr"`
}

// LegacyAbac mirrors container#LegacyAbac.
type LegacyAbac struct {
	Enabled bool `json:"enabled"`
}

// ReleaseChannel mirrors container#ReleaseChannel.
type ReleaseChannel struct {
	Channel string `json:"channel"`
}

// ShieldedNodes mirrors container#ShieldedNodes.
type ShieldedNodes struct {
	Enabled bool `json:"enabled"`
}

// WorkloadIdentityConfig mirrors container#WorkloadIdentityConfig.
type WorkloadIdentityConfig struct {
	WorkloadPool string `json:"workloadPool"`
}

// NetworkConfigDefaultSnatStatus mirrors the nested defaultSnatStatus block
// on container#NetworkConfig.
type NetworkConfigDefaultSnatStatus struct {
	Disabled bool `json:"disabled"`
}

// NetworkConfig mirrors container#NetworkConfig.
type NetworkConfig struct {
	Network           string                         `json:"network,omitempty"`
	Subnetwork        string                         `json:"subnetwork,omitempty"`
	DefaultSnatStatus NetworkConfigDefaultSnatStatus `json:"defaultSnatStatus"`
}

// Cluster mirrors the real container#Cluster resource (location-scoped:
// location may be a zone or a region). It always populates the substructs
// below because the Terraform provider's flatten/read code is written
// against real GKE, which never omits them — see the comment on NodePools.
type Cluster struct {
	Name                   string                  `json:"name"`
	Location               string                  `json:"location,omitempty"`
	InitialNodeCount       int64                   `json:"initialNodeCount,omitempty"`
	NodeConfig             NodeConfig              `json:"nodeConfig,omitempty"`
	Network                string                  `json:"network,omitempty"`
	Subnetwork             string                  `json:"subnetwork,omitempty"`
	Status                 string                  `json:"status,omitempty"`
	Endpoint               string                  `json:"endpoint,omitempty"`
	CurrentMasterVersion   string                  `json:"currentMasterVersion,omitempty"`
	CurrentNodeVersion     string                  `json:"currentNodeVersion,omitempty"`
	InitialClusterVersion  string                  `json:"initialClusterVersion,omitempty"`
	SelfLink               string                  `json:"selfLink,omitempty"`
	CreateTime             string                  `json:"createTime,omitempty"`
	LoggingService         string                  `json:"loggingService,omitempty"`
	MonitoringService      string                  `json:"monitoringService,omitempty"`
	ClusterIpv4Cidr        string                  `json:"clusterIpv4Cidr,omitempty"`
	ServicesIpv4Cidr       string                  `json:"servicesIpv4Cidr,omitempty"`
	NodeIpv4CidrSize       int64                   `json:"nodeIpv4CidrSize,omitempty"`
	CurrentNodeCount       int64                   `json:"currentNodeCount,omitempty"`
	LabelFingerprint       string                  `json:"labelFingerprint,omitempty"`
	MasterAuth             *MasterAuth             `json:"masterAuth,omitempty"`
	AddonsConfig           *AddonsConfig           `json:"addonsConfig,omitempty"`
	IPAllocationPolicy     *IPAllocationPolicy     `json:"ipAllocationPolicy,omitempty"`
	LegacyAbac             *LegacyAbac             `json:"legacyAbac,omitempty"`
	ReleaseChannel         *ReleaseChannel         `json:"releaseChannel,omitempty"`
	ShieldedNodes          *ShieldedNodes          `json:"shieldedNodes,omitempty"`
	WorkloadIdentityConfig *WorkloadIdentityConfig `json:"workloadIdentityConfig,omitempty"`
	NetworkConfig          *NetworkConfig          `json:"networkConfig,omitempty"`
	// NodePools is always populated, matching the real API: GKE auto-creates
	// a "default-pool" node pool from initialNodeCount/nodeConfig at cluster
	// creation time even if the client never explicitly creates one.
	// Terraform's provider (resourceContainerClusterRead) dereferences this
	// array unconditionally when flattening the cluster — leaving it nil
	// crashes the plugin with a nil pointer panic.
	NodePools []NodePool `json:"nodePools"`
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

// nodePoolsForCluster lists every node pool currently stored for a cluster,
// read fresh from bucketNodePools rather than from any cached copy on the
// Cluster record. Used to keep Cluster.NodePools (always populated, see the
// comment on that field) in sync with pools added later via createNodePool.
func (s *Service) nodePoolsForCluster(project, location, cluster string) []NodePool {
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
	return items
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
	var existingCluster Cluster
	found, err := s.db.Get(bucketClusters, clusterKey(project, location, body.Cluster.Name), &existingCluster)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "cluster already exists: "+body.Cluster.Name)
		return
	}
	c := body.Cluster
	c.Location = location
	c.Status = "RUNNING"
	c.Endpoint = fmt.Sprintf("10.%d.%d.1", len(c.Name)%255, len(project)%255)
	c.CurrentMasterVersion = orDefault(c.CurrentMasterVersion, "1.30.0-gke.0")
	c.CurrentNodeVersion = c.CurrentMasterVersion
	c.InitialClusterVersion = c.CurrentMasterVersion
	c.SelfLink = fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, c.Name)
	c.CreateTime = time.Now().UTC().Format(time.RFC3339)
	c.LoggingService = orDefault(c.LoggingService, "logging.googleapis.com/kubernetes")
	c.MonitoringService = orDefault(c.MonitoringService, "monitoring.googleapis.com/kubernetes")
	c.ClusterIpv4Cidr = orDefault(c.ClusterIpv4Cidr, "10.0.0.0/14")
	c.ServicesIpv4Cidr = orDefault(c.ServicesIpv4Cidr, "10.4.0.0/20")
	if c.NodeIpv4CidrSize == 0 {
		c.NodeIpv4CidrSize = 24
	}
	c.CurrentNodeCount = c.InitialNodeCount
	if c.CurrentNodeCount == 0 {
		c.CurrentNodeCount = 1
	}
	c.LabelFingerprint = fakeFingerprint(fmt.Sprintf("%s-%d", c.Name, time.Now().UnixNano()))
	c.MasterAuth = &MasterAuth{ClusterCaCertificate: base64.StdEncoding.EncodeToString([]byte("fake-ca-cert-" + c.Name))}
	c.AddonsConfig = &AddonsConfig{
		HttpLoadBalancing:        httpLoadBalancing{Disabled: false},
		HorizontalPodAutoscaling: horizontalPodAutoscaling{Disabled: false},
		NetworkPolicyConfig:      networkPolicyConfig{Disabled: true},
	}
	c.IPAllocationPolicy = &IPAllocationPolicy{
		UseIpAliases:      true,
		ClusterIpv4Cidr:   c.ClusterIpv4Cidr,
		ServicesIpv4Cidr:  c.ServicesIpv4Cidr,
	}
	c.LegacyAbac = &LegacyAbac{Enabled: false}
	c.ReleaseChannel = &ReleaseChannel{Channel: "UNSPECIFIED"}
	c.ShieldedNodes = &ShieldedNodes{Enabled: true}
	c.WorkloadIdentityConfig = &WorkloadIdentityConfig{WorkloadPool: project + ".svc.id.goog"}
	c.NetworkConfig = &NetworkConfig{
		Network:           c.Network,
		Subnetwork:        c.Subnetwork,
		DefaultSnatStatus: NetworkConfigDefaultSnatStatus{Disabled: false},
	}
	c.NodeConfig.WorkloadMetadataConfig = &WorkloadMetadataConfig{Mode: "GKE_METADATA"}
	c.NodeConfig.ShieldedInstanceConfig = &ShieldedInstanceConfig{EnableSecureBoot: false, EnableIntegrityMonitoring: true}

	// Real GKE always auto-creates a "default-pool" node pool from the
	// cluster's initialNodeCount/nodeConfig, even when the client never
	// explicitly calls createNodePool. Mirror that here so Cluster.NodePools
	// is never empty — see the comment on that field for why this matters.
	defaultPool := NodePool{
		Name:              "default-pool",
		Config:            c.NodeConfig,
		InitialNodeCount:  c.InitialNodeCount,
		Status:            "RUNNING",
		SelfLink:          fmt.Sprintf("projects/%s/locations/%s/clusters/%s/nodePools/default-pool", project, location, c.Name),
		Version:           c.CurrentMasterVersion,
		InstanceGroupUrls: []string{fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/zones/%s/instanceGroupManagers/gke-%s-default-pool", project, location, c.Name)},
	}
	if defaultPool.InitialNodeCount == 0 {
		defaultPool.InitialNodeCount = 3
	}
	if err := s.db.Put(bucketNodePools, nodePoolKey(project, location, c.Name, defaultPool.Name), defaultPool); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	c.NodePools = s.nodePoolsForCluster(project, location, c.Name)

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
		c.NodePools = s.nodePoolsForCluster(project, location, c.Name)
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
	c.NodePools = s.nodePoolsForCluster(project, location, cluster)
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
	var existingNP NodePool
	found, err := s.db.Get(bucketNodePools, nodePoolKey(project, location, cluster, body.NodePool.Name), &existingNP)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "node pool already exists: "+body.NodePool.Name)
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
