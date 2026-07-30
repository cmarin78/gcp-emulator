package dataproc

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

func base(srvURL string) string {
	return srvURL + "/dataproc/v1/projects/proj1/regions/us-central1/clusters"
}

func TestCreateClusterIsRunning(t *testing.T) {
	srv := newTestServer(t)

	var op Operation
	status := testutil.DoJSON(t, "POST", base(srv.URL), map[string]any{
		"clusterName": "my-cluster",
		"config": map[string]any{
			"workerConfig": map[string]any{"numInstances": 2},
		},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create cluster: status=%d op=%+v", status, op)
	}
	if op.Name == "" {
		t.Fatal("create cluster: missing operation name")
	}
}

func TestCreateClusterRequiresName(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", base(srv.URL), map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without name: want 400, got %d", status)
	}
}

func TestCreateClusterDuplicate(t *testing.T) {
	srv := newTestServer(t)
	body := map[string]any{"clusterName": "dup"}
	testutil.DoJSON(t, "POST", base(srv.URL), body, nil)
	status := testutil.DoJSON(t, "POST", base(srv.URL), body, nil)
	if status != 409 {
		t.Fatalf("duplicate cluster: want 409, got %d", status)
	}
}

func TestListAndGetCluster(t *testing.T) {
	srv := newTestServer(t)
	b := base(srv.URL)

	testutil.DoJSON(t, "POST", b, map[string]any{"clusterName": "c1"}, nil)
	testutil.DoJSON(t, "POST", b, map[string]any{"clusterName": "c2"}, nil)

	var list struct {
		Clusters []Cluster `json:"clusters"`
	}
	status := testutil.DoJSON(t, "GET", b, nil, &list)
	if status != 200 {
		t.Fatalf("list: want 200, got %d", status)
	}
	if len(list.Clusters) != 2 {
		t.Fatalf("list: want 2 clusters, got %d", len(list.Clusters))
	}

	var got Cluster
	status = testutil.DoJSON(t, "GET", b+"/c1", nil, &got)
	if status != 200 {
		t.Fatalf("get: want 200, got %d", status)
	}
	if got.ClusterName != "c1" {
		t.Fatalf("get: want clusterName=c1, got %q", got.ClusterName)
	}
	if got.Status.State != "RUNNING" {
		t.Fatalf("get: want state RUNNING, got %q", got.Status.State)
	}
	if got.ClusterUUID == "" {
		t.Fatal("get: missing clusterUuid")
	}
}

func TestGetClusterNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", base(srv.URL)+"/ghost", nil, nil)
	if status != 404 {
		t.Fatalf("get nonexistent: want 404, got %d", status)
	}
}

func TestPatchCluster(t *testing.T) {
	srv := newTestServer(t)
	b := base(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{"clusterName": "patch-me"}, nil)

	var op Operation
	status := testutil.DoJSON(t, "PATCH", b+"/patch-me", map[string]any{
		"labels": map[string]string{"env": "test"},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("patch: status=%d op=%+v", status, op)
	}

	var got Cluster
	testutil.DoJSON(t, "GET", b+"/patch-me", nil, &got)
	if got.Labels["env"] != "test" {
		t.Fatalf("patch: want label env=test, got %v", got.Labels)
	}
}

func TestDeleteCluster(t *testing.T) {
	srv := newTestServer(t)
	b := base(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{"clusterName": "del-me"}, nil)

	var op Operation
	status := testutil.DoJSON(t, "DELETE", b+"/del-me", nil, &op)
	if status != 200 || !op.Done {
		t.Fatalf("delete: status=%d op=%+v", status, op)
	}
	// Gone.
	status = testutil.DoJSON(t, "GET", b+"/del-me", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestDeleteClusterNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "DELETE", base(srv.URL)+"/ghost", nil, nil)
	if status != 404 {
		t.Fatalf("delete nonexistent: want 404, got %d", status)
	}
}

func TestListClustersIsolatedByRegion(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST",
		srv.URL+"/dataproc/v1/projects/p/regions/us-central1/clusters",
		map[string]any{"clusterName": "c-us"}, nil)
	testutil.DoJSON(t, "POST",
		srv.URL+"/dataproc/v1/projects/p/regions/europe-west1/clusters",
		map[string]any{"clusterName": "c-eu"}, nil)

	var list struct {
		Clusters []Cluster `json:"clusters"`
	}
	testutil.DoJSON(t, "GET",
		srv.URL+"/dataproc/v1/projects/p/regions/us-central1/clusters", nil, &list)
	if len(list.Clusters) != 1 {
		t.Fatalf("region isolation: want 1 cluster in us-central1, got %d", len(list.Clusters))
	}
}
