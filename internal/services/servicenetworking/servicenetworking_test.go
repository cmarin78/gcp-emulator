package servicenetworking

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

func validConnectionBody() map[string]any {
	return map[string]any{
		"network":               "projects/proj1/global/networks/default",
		"reservedPeeringRanges": []string{"google-managed-services-default"},
	}
}

// TestConnectionLifecycle covers create -> list -> patch -> delete,
// asserting every mutation resolves synchronously (done:true).
func TestConnectionLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/services/servicenetworking.googleapis.com/connections",
		validConnectionBody(), &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var list struct {
		Connections []Connection `json:"connections"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/services/servicenetworking.googleapis.com/connections", nil, &list)
	if status != 200 || len(list.Connections) != 1 || list.Connections[0].Network != "projects/proj1/global/networks/default" {
		t.Fatalf("list: status=%d connections=%+v", status, list.Connections)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v1/services/servicenetworking.googleapis.com/connections/main",
		map[string]any{"reservedPeeringRanges": []string{"range-a", "range-b"}}, &patchOp)
	if status != 200 || !patchOp.Done {
		t.Fatalf("patch: status=%d op=%+v", status, patchOp)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/services/servicenetworking.googleapis.com/connections/main:deleteConnection",
		nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}

	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/services/servicenetworking.googleapis.com/connections", nil, &list)
	if status != 200 || len(list.Connections) != 0 {
		t.Fatalf("list after delete: status=%d connections=%+v", status, list.Connections)
	}
}

func TestCreateConnectionRequiresFields(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/services/servicenetworking.googleapis.com/connections",
		map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without network/ranges: want 400, got %d", status)
	}
}

func TestDuplicateConnectionConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/services/servicenetworking.googleapis.com/connections", validConnectionBody(), nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/services/servicenetworking.googleapis.com/connections", validConnectionBody(), nil)
	if status != 409 {
		t.Fatalf("duplicate connection: want 409, got %d", status)
	}
}

func TestPatchConnectionNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "PATCH", srv.URL+"/v1/services/servicenetworking.googleapis.com/connections/main",
		map[string]any{}, nil)
	if status != 404 {
		t.Fatalf("patch missing connection: want 404, got %d", status)
	}
}
