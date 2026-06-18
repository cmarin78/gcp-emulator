package gke

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestClusterLifecycle covers create -> get -> list -> delete, asserting the
// auto-created "default-pool" node pool is always present on the cluster
// even though the client never explicitly creates one.
func TestClusterLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/clusters", map[string]any{
		"cluster": map[string]any{"name": "my-cluster", "initialNodeCount": 2},
	}, &createOp)
	if status != 200 || createOp.Status != "DONE" || createOp.OperationType != "CREATE_CLUSTER" {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Cluster
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/clusters/my-cluster", nil, &got)
	if status != 200 || got.Status != "RUNNING" {
		t.Fatalf("get: status=%d cluster=%+v", status, got)
	}
	if len(got.NodePools) != 1 || got.NodePools[0].Name != "default-pool" {
		t.Fatalf("expected auto-created default-pool, got %+v", got.NodePools)
	}
	if got.MasterAuth == nil || got.MasterAuth.ClusterCaCertificate == "" {
		t.Fatalf("expected masterAuth to be populated, got %+v", got.MasterAuth)
	}

	var list struct {
		Clusters []Cluster `json:"clusters"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/clusters", nil, &list)
	if status != 200 || len(list.Clusters) != 1 {
		t.Fatalf("list: status=%d clusters=%+v", status, list.Clusters)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/locations/us-central1/clusters/my-cluster", nil, &deleteOp)
	if status != 200 || deleteOp.Status != "DONE" || deleteOp.OperationType != "DELETE_CLUSTER" {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

// TestNodePoolLifecycle covers create -> get -> list -> delete of an
// additional node pool, asserting the cluster's NodePools list (read fresh
// from storage rather than a cached copy) reflects the addition.
func TestNodePoolLifecycle(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/clusters", map[string]any{
		"cluster": map[string]any{"name": "my-cluster"},
	}, nil)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/clusters/my-cluster/nodePools", map[string]any{
		"nodePool": map[string]any{"name": "pool-2", "initialNodeCount": 5},
	}, &createOp)
	if status != 200 || createOp.OperationType != "CREATE_NODE_POOL" {
		t.Fatalf("create node pool: status=%d op=%+v", status, createOp)
	}

	var got NodePool
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/clusters/my-cluster/nodePools/pool-2", nil, &got)
	if status != 200 || got.Status != "RUNNING" || got.InitialNodeCount != 5 {
		t.Fatalf("get node pool: status=%d np=%+v", status, got)
	}

	var list struct {
		NodePools []NodePool `json:"nodePools"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/clusters/my-cluster/nodePools", nil, &list)
	if status != 200 || len(list.NodePools) != 2 {
		t.Fatalf("list node pools: status=%d pools=%+v", status, list.NodePools)
	}

	var cluster Cluster
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/clusters/my-cluster", nil, &cluster)
	if len(cluster.NodePools) != 2 {
		t.Fatalf("expected cluster.NodePools to reflect added pool, got %+v", cluster.NodePools)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/locations/us-central1/clusters/my-cluster/nodePools/pool-2", nil, &deleteOp)
	if status != 200 || deleteOp.OperationType != "DELETE_NODE_POOL" {
		t.Fatalf("delete node pool: status=%d op=%+v", status, deleteOp)
	}
}
