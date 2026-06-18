package memorystore

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

// TestInstanceLifecycle covers create -> get -> list -> patch -> delete,
// asserting every mutation resolves synchronously (done:true) and the
// instance always reports state READY (no real provisioning happens).
func TestInstanceLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/instances?instanceId=my-cache",
		map[string]any{"memorySizeGb": 4}, &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Instance
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/instances/my-cache", nil, &got)
	if status != 200 || got.State != "READY" || got.MemorySizeGb != 4 || got.Tier != "BASIC" {
		t.Fatalf("get: status=%d inst=%+v", status, got)
	}

	var list struct {
		Instances []Instance `json:"instances"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/instances", nil, &list)
	if status != 200 || len(list.Instances) != 1 {
		t.Fatalf("list: status=%d instances=%+v", status, list.Instances)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v1/projects/proj1/locations/us-central1/instances/my-cache",
		map[string]any{"memorySizeGb": 8}, &patchOp)
	if status != 200 || !patchOp.Done {
		t.Fatalf("patch: status=%d op=%+v", status, patchOp)
	}
	var patched Instance
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/instances/my-cache", nil, &patched)
	if patched.MemorySizeGb != 8 {
		t.Fatalf("expected memorySizeGb updated to 8, got %d", patched.MemorySizeGb)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/locations/us-central1/instances/my-cache", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

func TestCreateRequiresInstanceId(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/instances", map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without instanceId: want 400, got %d", status)
	}
}

// TestDuplicateCreateConflict asserts that creating an instance whose
// instanceId already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/instances?instanceId=my-cache",
		map[string]any{"memorySizeGb": 4}, nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/instances?instanceId=my-cache",
		map[string]any{"memorySizeGb": 4}, nil)
	if status != 409 {
		t.Fatalf("duplicate instance: want 409, got %d", status)
	}
}
