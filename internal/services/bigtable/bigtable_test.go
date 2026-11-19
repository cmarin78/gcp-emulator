package bigtable

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

func instanceBase(srvURL string) string {
	return srvURL + "/bigtable/v2/projects/proj1/instances"
}

// ── Instance tests ────────────────────────────────────────────────────────────

func TestCreateAndGetInstance(t *testing.T) {
	srv := newTestServer(t)
	b := instanceBase(srv.URL)

	var op Operation
	status := testutil.DoJSON(t, "POST", b, map[string]any{
		"instanceId": "inst1",
		"instance":   map[string]any{"displayName": "My Instance", "type": "PRODUCTION"},
		"clusters": map[string]any{
			"cluster1": map[string]any{"location": "projects/proj1/locations/us-central1-a", "serveNodes": 3},
		},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create instance: status=%d op=%+v", status, op)
	}

	var got Instance
	status = testutil.DoJSON(t, "GET", b+"/inst1", nil, &got)
	if status != 200 {
		t.Fatalf("get instance: want 200, got %d", status)
	}
	if got.DisplayName != "My Instance" {
		t.Fatalf("get instance: displayName=%q", got.DisplayName)
	}
	if got.State != "READY" {
		t.Fatalf("get instance: state=%q, want READY", got.State)
	}
}

func TestCreateInstanceRequiresID(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", instanceBase(srv.URL), map[string]any{
		"instance": map[string]any{"displayName": "x"},
	}, nil)
	if status != 400 {
		t.Fatalf("no instanceId: want 400, got %d", status)
	}
}

func TestCreateInstanceDuplicate(t *testing.T) {
	srv := newTestServer(t)
	b := instanceBase(srv.URL)
	body := map[string]any{"instanceId": "dup", "instance": map[string]any{"displayName": "d"}}
	testutil.DoJSON(t, "POST", b, body, nil)
	status := testutil.DoJSON(t, "POST", b, body, nil)
	if status != 409 {
		t.Fatalf("duplicate: want 409, got %d", status)
	}
}

func TestListInstances(t *testing.T) {
	srv := newTestServer(t)
	b := instanceBase(srv.URL)
	for _, id := range []string{"a", "b", "c"} {
		testutil.DoJSON(t, "POST", b, map[string]any{"instanceId": id, "instance": map[string]any{"displayName": id}}, nil)
	}
	var list struct {
		Instances []Instance `json:"instances"`
	}
	status := testutil.DoJSON(t, "GET", b, nil, &list)
	if status != 200 || len(list.Instances) != 3 {
		t.Fatalf("list: want 3, got %d (status=%d)", len(list.Instances), status)
	}
}

func TestPatchInstance(t *testing.T) {
	srv := newTestServer(t)
	b := instanceBase(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{"instanceId": "patch-me", "instance": map[string]any{"displayName": "old"}}, nil)

	var got Instance
	status := testutil.DoJSON(t, "PATCH", b+"/patch-me", map[string]any{
		"displayName": "new",
		"labels":      map[string]string{"env": "prod"},
	}, &got)
	if status != 200 || got.DisplayName != "new" {
		t.Fatalf("patch: status=%d displayName=%q", status, got.DisplayName)
	}
	if got.Labels["env"] != "prod" {
		t.Fatalf("patch: labels=%v", got.Labels)
	}
}

func TestDeleteInstance(t *testing.T) {
	srv := newTestServer(t)
	b := instanceBase(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{"instanceId": "del", "instance": map[string]any{"displayName": "d"}}, nil)

	status := testutil.DoJSON(t, "DELETE", b+"/del", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", b+"/del", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestGetInstanceNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", instanceBase(srv.URL)+"/ghost", nil, nil)
	if status != 404 {
		t.Fatalf("want 404, got %d", status)
	}
}

// ── Cluster tests ─────────────────────────────────────────────────────────────

func TestCreateAndListClusters(t *testing.T) {
	srv := newTestServer(t)
	b := instanceBase(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{"instanceId": "inst1", "instance": map[string]any{"displayName": "i"}}, nil)

	cBase := b + "/inst1/clusters"
	var op Operation
	status := testutil.DoJSON(t, "POST", cBase+"?clusterId=cl1", map[string]any{
		"serveNodes": 3,
		"defaultStorageType": "SSD",
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create cluster: status=%d op=%+v", status, op)
	}

	var list struct {
		Clusters []Cluster `json:"clusters"`
	}
	testutil.DoJSON(t, "GET", cBase, nil, &list)
	if len(list.Clusters) != 1 {
		t.Fatalf("list clusters: want 1, got %d", len(list.Clusters))
	}
	if list.Clusters[0].State != "READY" {
		t.Fatalf("cluster state: want READY, got %q", list.Clusters[0].State)
	}
}

func TestDeleteCluster(t *testing.T) {
	srv := newTestServer(t)
	b := instanceBase(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{"instanceId": "i", "instance": map[string]any{"displayName": "i"}}, nil)
	cBase := b + "/i/clusters"
	testutil.DoJSON(t, "POST", cBase+"?clusterId=cl", map[string]any{"serveNodes": 1}, nil)

	status := testutil.DoJSON(t, "DELETE", cBase+"/cl", nil, nil)
	if status != 200 {
		t.Fatalf("delete cluster: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", cBase+"/cl", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete cluster: want 404, got %d", status)
	}
}

// ── App Profile tests ─────────────────────────────────────────────────────────

func TestAppProfileLifecycle(t *testing.T) {
	srv := newTestServer(t)
	b := instanceBase(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{"instanceId": "inst1", "instance": map[string]any{"displayName": "i"}}, nil)
	apBase := b + "/inst1/appProfiles"

	// Create.
	var ap AppProfile
	status := testutil.DoJSON(t, "POST", apBase+"?appProfileId=ap1", map[string]any{
		"description": "default routing",
	}, &ap)
	if status != 200 {
		t.Fatalf("create app profile: want 200, got %d", status)
	}
	if ap.Description != "default routing" {
		t.Fatalf("create app profile: description=%q", ap.Description)
	}
	if ap.Etag == "" {
		t.Fatal("create app profile: missing etag")
	}

	// Get.
	var got AppProfile
	status = testutil.DoJSON(t, "GET", apBase+"/ap1", nil, &got)
	if status != 200 || got.Description != "default routing" {
		t.Fatalf("get app profile: status=%d %+v", status, got)
	}

	// List.
	testutil.DoJSON(t, "POST", apBase+"?appProfileId=ap2", map[string]any{"description": "second"}, nil)
	var list struct {
		AppProfiles []AppProfile `json:"appProfiles"`
	}
	testutil.DoJSON(t, "GET", apBase, nil, &list)
	if len(list.AppProfiles) != 2 {
		t.Fatalf("list app profiles: want 2, got %d", len(list.AppProfiles))
	}

	// Patch.
	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", apBase+"/ap1", map[string]any{
		"description": "updated",
	}, &patchOp)
	if status != 200 || !patchOp.Done {
		t.Fatalf("patch app profile: status=%d", status)
	}

	// Delete.
	status = testutil.DoJSON(t, "DELETE", apBase+"/ap1", nil, nil)
	if status != 200 {
		t.Fatalf("delete app profile: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", apBase+"/ap1", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestAppProfileRequiresID(t *testing.T) {
	srv := newTestServer(t)
	b := instanceBase(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{"instanceId": "inst1", "instance": map[string]any{"displayName": "i"}}, nil)
	status := testutil.DoJSON(t, "POST", b+"/inst1/appProfiles", map[string]any{"description": "x"}, nil)
	if status != 400 {
		t.Fatalf("no appProfileId: want 400, got %d", status)
	}
}

func TestDeleteInstanceCascadesChildren(t *testing.T) {
	srv := newTestServer(t)
	b := instanceBase(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{
		"instanceId": "cascade-inst",
		"instance":   map[string]any{"displayName": "Cascade Test"},
		"clusters": map[string]any{
			"c1": map[string]any{"location": "us-central1-b", "serveNodes": 1},
		},
	}, nil)

	testutil.DoJSON(t, "POST", b+"/cascade-inst/appProfiles?appProfileId=p1", map[string]any{
		"description": "profile 1",
	}, nil)

	// Verify child cluster and profile exist
	var c Cluster
	if status := testutil.DoJSON(t, "GET", b+"/cascade-inst/clusters/c1", nil, &c); status != 200 {
		t.Fatalf("cluster get: want 200, got %d", status)
	}
	var p AppProfile
	if status := testutil.DoJSON(t, "GET", b+"/cascade-inst/appProfiles/p1", nil, &p); status != 200 {
		t.Fatalf("profile get: want 200, got %d", status)
	}

	// Delete instance
	if status := testutil.DoJSON(t, "DELETE", b+"/cascade-inst", nil, nil); status != 200 {
		t.Fatalf("delete instance: want 200, got %d", status)
	}

	// Verify children are deleted
	if status := testutil.DoJSON(t, "GET", b+"/cascade-inst/clusters/c1", nil, nil); status != 404 {
		t.Errorf("cluster after parent delete: want 404, got %d", status)
	}
	if status := testutil.DoJSON(t, "GET", b+"/cascade-inst/appProfiles/p1", nil, nil); status != 404 {
		t.Errorf("profile after parent delete: want 404, got %d", status)
	}
}
