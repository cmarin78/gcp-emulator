package resourcemanager

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

// TestProjectLifecycle covers create -> get -> list -> delete, asserting
// delete performs a soft-delete (state -> DELETE_REQUESTED) rather than
// actually removing the record, matching the real API's behavior.
func TestProjectLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v3/projects", map[string]string{
		"projectId":   "my-project",
		"displayName": "My Project",
	}, &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Project
	status = testutil.DoJSON(t, "GET", srv.URL+"/v3/projects/my-project", nil, &got)
	if status != 200 || got.State != "ACTIVE" {
		t.Fatalf("get: status=%d proj=%+v", status, got)
	}

	var list struct {
		Projects []Project `json:"projects"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v3/projects", nil, &list)
	if status != 200 || len(list.Projects) != 1 {
		t.Fatalf("list: status=%d projects=%+v", status, list.Projects)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v3/projects/my-project", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}

	var afterDelete Project
	status = testutil.DoJSON(t, "GET", srv.URL+"/v3/projects/my-project", nil, &afterDelete)
	if status != 200 || afterDelete.State != "DELETE_REQUESTED" {
		t.Fatalf("expected soft-delete state DELETE_REQUESTED, got status=%d proj=%+v", status, afterDelete)
	}
}

func TestCreateDuplicateProjectFails(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v3/projects", map[string]string{"projectId": "dup"}, nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v3/projects", map[string]string{"projectId": "dup"}, nil)
	if status != 409 {
		t.Fatalf("duplicate create: want 409, got %d", status)
	}
}

// TestGetOperation asserts the operations polling endpoint always reports
// done=true -- this emulator never models real async Resource Manager ops.
func TestGetOperation(t *testing.T) {
	srv := newTestServer(t)
	var op Operation
	status := testutil.DoJSON(t, "GET", srv.URL+"/v3/operations/op-123", nil, &op)
	if status != 200 || !op.Done {
		t.Fatalf("getOperation: status=%d op=%+v", status, op)
	}
}
