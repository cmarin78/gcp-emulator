package filestore

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

func validInstanceBody() map[string]any {
	return map[string]any{
		"fileShares": []map[string]any{{"name": "share1", "capacityGb": 2560}},
		"networks":   []map[string]any{{"network": "default", "modes": []string{"MODE_IPV4"}}},
	}
}

// TestInstanceLifecycle covers create -> get -> list -> patch -> delete,
// asserting nested fileShares/networks are persisted and every mutation
// resolves synchronously (done:true), with state always READY.
func TestInstanceLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances?instanceId=my-nfs",
		validInstanceBody(), &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Instance
	status = testutil.DoJSON(t, "GET", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances/my-nfs", nil, &got)
	if status != 200 || got.State != "READY" || len(got.FileShares) != 1 || got.FileShares[0].CapacityGb != 2560 {
		t.Fatalf("get: status=%d inst=%+v", status, got)
	}
	if len(got.Networks) != 1 || len(got.Networks[0].IpAddresses) == 0 {
		t.Fatalf("expected network with assigned ip address, got %+v", got.Networks)
	}

	var list struct {
		Instances []Instance `json:"instances"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances", nil, &list)
	if status != 200 || len(list.Instances) != 1 {
		t.Fatalf("list: status=%d instances=%+v", status, list.Instances)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances/my-nfs",
		map[string]any{"description": "updated"}, &patchOp)
	if status != 200 || !patchOp.Done {
		t.Fatalf("patch: status=%d op=%+v", status, patchOp)
	}
	var patched Instance
	testutil.DoJSON(t, "GET", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances/my-nfs", nil, &patched)
	if patched.Description != "updated" {
		t.Fatalf("expected description updated, got %q", patched.Description)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances/my-nfs", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

func TestCreateRequiresInstanceId(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances", validInstanceBody(), nil)
	if status != 400 {
		t.Fatalf("create without instanceId: want 400, got %d", status)
	}
}

func TestCreateRequiresFileShare(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances?instanceId=bad",
		map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without fileShares: want 400, got %d", status)
	}
}

// TestDuplicateCreateConflict asserts that creating an instance whose
// instanceId already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances?instanceId=my-nfs",
		validInstanceBody(), nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances?instanceId=my-nfs",
		validInstanceBody(), nil)
	if status != 409 {
		t.Fatalf("duplicate instance: want 409, got %d", status)
	}
}

func TestInstanceNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", srv.URL+"/file/v1/projects/proj1/locations/us-central1/instances/missing", nil, nil)
	if status != 404 {
		t.Fatalf("get missing instance: want 404, got %d", status)
	}
}
