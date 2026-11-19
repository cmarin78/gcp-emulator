package alloydb

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

func TestAlloyDBLifecycleAndCascadingDelete(t *testing.T) {
	srv := newTestServer(t)

	// 1. Create cluster
	var op Operation
	testutil.DoJSON(t, "POST", srv.URL+"/alloydb/v1/projects/proj1/locations/us-central1/clusters?clusterId=my-cluster", map[string]any{
		"displayName": "My Test Cluster",
		"clusterType": "PRIMARY",
		"networkConfig": map[string]any{
			"network": "projects/proj1/global/networks/default",
		},
	}, &op)
	if !op.Done {
		t.Fatal("expected create cluster op to be done")
	}

	// 2. Get cluster
	var cluster Cluster
	testutil.DoJSON(t, "GET", srv.URL+"/alloydb/v1/projects/proj1/locations/us-central1/clusters/my-cluster", nil, &cluster)
	if cluster.State != "READY" || cluster.DisplayName != "My Test Cluster" {
		t.Fatalf("unexpected cluster state or name: %+v", cluster)
	}

	// 3. Create instance under cluster
	var instOp Operation
	testutil.DoJSON(t, "POST", srv.URL+"/alloydb/v1/projects/proj1/locations/us-central1/clusters/my-cluster/instances?instanceId=inst-1", map[string]any{
		"displayName":  "Instance 1",
		"instanceType": "PRIMARY",
	}, &instOp)
	if !instOp.Done {
		t.Fatal("expected create instance op to be done")
	}

	// 4. List instances
	var instListResp struct {
		Instances []Instance `json:"instances"`
	}
	testutil.DoJSON(t, "GET", srv.URL+"/alloydb/v1/projects/proj1/locations/us-central1/clusters/my-cluster/instances", nil, &instListResp)
	if len(instListResp.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(instListResp.Instances))
	}

	// 5. Delete cluster -> should cascade delete inst-1
	testutil.DoJSON(t, "DELETE", srv.URL+"/alloydb/v1/projects/proj1/locations/us-central1/clusters/my-cluster", nil, &op)
	if !op.Done {
		t.Fatal("expected delete cluster op to be done")
	}

	// Verify cluster is gone
	status := testutil.DoJSON(t, "GET", srv.URL+"/alloydb/v1/projects/proj1/locations/us-central1/clusters/my-cluster", nil, nil)
	if status != 404 {
		t.Fatalf("expected 404 for deleted cluster, got %d", status)
	}

	// Verify instance is gone
	status = testutil.DoJSON(t, "GET", srv.URL+"/alloydb/v1/projects/proj1/locations/us-central1/clusters/my-cluster/instances/inst-1", nil, nil)
	if status != 404 {
		t.Fatalf("expected 404 for cascaded instance, got %d", status)
	}
}
