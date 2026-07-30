package memcache

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
	return srvURL + "/memcache/v1beta2/projects/proj1/locations/us-central1/instances"
}

func TestCreateInstanceIsReady(t *testing.T) {
	srv := newTestServer(t)

	var op Operation
	status := testutil.DoJSON(t, "POST", base(srv.URL)+"?instanceId=mc1", map[string]any{
		"displayName": "my-memcache",
		"nodeCount":   3,
		"nodeConfig":  map[string]any{"cpuCount": 1, "memorySizeMb": 1024},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create: status=%d op=%+v", status, op)
	}
}

func TestCreateInstanceRequiresID(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", base(srv.URL), map[string]any{"displayName": "x"}, nil)
	if status != 400 {
		t.Fatalf("no instanceId: want 400, got %d", status)
	}
}

func TestCreateInstanceDuplicate(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", base(srv.URL)+"?instanceId=dup", map[string]any{}, nil)
	status := testutil.DoJSON(t, "POST", base(srv.URL)+"?instanceId=dup", map[string]any{}, nil)
	if status != 409 {
		t.Fatalf("duplicate: want 409, got %d", status)
	}
}

func TestListAndGetInstance(t *testing.T) {
	srv := newTestServer(t)
	b := base(srv.URL)

	testutil.DoJSON(t, "POST", b+"?instanceId=mc1", map[string]any{"displayName": "a"}, nil)
	testutil.DoJSON(t, "POST", b+"?instanceId=mc2", map[string]any{"displayName": "b"}, nil)

	var list struct {
		Instances []Instance `json:"instances"`
	}
	status := testutil.DoJSON(t, "GET", b, nil, &list)
	if status != 200 || len(list.Instances) != 2 {
		t.Fatalf("list: want 2, got %d (status=%d)", len(list.Instances), status)
	}

	var got Instance
	status = testutil.DoJSON(t, "GET", b+"/mc1", nil, &got)
	if status != 200 {
		t.Fatalf("get: want 200, got %d", status)
	}
	if got.State != "READY" {
		t.Fatalf("get: state=%q, want READY", got.State)
	}
	if got.MemcacheVersion != "MEMCACHE_1_6" {
		t.Fatalf("get: version=%q", got.MemcacheVersion)
	}
}

func TestGetInstanceNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", base(srv.URL)+"/ghost", nil, nil)
	if status != 404 {
		t.Fatalf("want 404, got %d", status)
	}
}

func TestPatchInstance(t *testing.T) {
	srv := newTestServer(t)
	b := base(srv.URL)
	testutil.DoJSON(t, "POST", b+"?instanceId=mc1", map[string]any{"displayName": "old", "nodeCount": 1}, nil)

	var op Operation
	status := testutil.DoJSON(t, "PATCH", b+"/mc1", map[string]any{
		"displayName": "new",
		"nodeCount":   5,
		"labels":      map[string]string{"env": "test"},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("patch: status=%d", status)
	}

	var got Instance
	testutil.DoJSON(t, "GET", b+"/mc1", nil, &got)
	if got.DisplayName != "new" || got.NodeCount != 5 {
		t.Fatalf("patch: displayName=%q nodeCount=%d", got.DisplayName, got.NodeCount)
	}
	if got.Labels["env"] != "test" {
		t.Fatalf("patch: labels=%v", got.Labels)
	}
}

func TestDeleteInstance(t *testing.T) {
	srv := newTestServer(t)
	b := base(srv.URL)
	testutil.DoJSON(t, "POST", b+"?instanceId=del", map[string]any{}, nil)

	status := testutil.DoJSON(t, "DELETE", b+"/del", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", b+"/del", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestDeleteInstanceNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "DELETE", base(srv.URL)+"/ghost", nil, nil)
	if status != 404 {
		t.Fatalf("want 404, got %d", status)
	}
}

func TestListInstancesIsolatedByLocation(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST",
		srv.URL+"/memcache/v1beta2/projects/p/locations/us-central1/instances?instanceId=mc1",
		map[string]any{}, nil)
	testutil.DoJSON(t, "POST",
		srv.URL+"/memcache/v1beta2/projects/p/locations/europe-west1/instances?instanceId=mc2",
		map[string]any{}, nil)

	var list struct {
		Instances []Instance `json:"instances"`
	}
	testutil.DoJSON(t, "GET",
		srv.URL+"/memcache/v1beta2/projects/p/locations/us-central1/instances", nil, &list)
	if len(list.Instances) != 1 {
		t.Fatalf("location isolation: want 1, got %d", len(list.Instances))
	}
}
