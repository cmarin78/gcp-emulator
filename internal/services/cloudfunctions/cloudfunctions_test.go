package cloudfunctions

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
	New(db, nil).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func validBody() map[string]any {
	return map[string]any{
		"buildConfig": map[string]string{
			"runtime":    "go121",
			"entryPoint": "HelloWorld",
		},
	}
}

// TestFunctionLifecycle covers create -> get -> list -> update -> delete.
func TestFunctionLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/functions?functionId=my-fn",
		validBody(), &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var fn Function
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/functions/my-fn", nil, &fn)
	if status != 200 || fn.State != "ACTIVE" || fn.ServiceConfig.URI == "" {
		t.Fatalf("get: status=%d fn=%+v", status, fn)
	}

	var list struct {
		Functions []Function `json:"functions"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/functions", nil, &list)
	if status != 200 || len(list.Functions) != 1 {
		t.Fatalf("list: status=%d functions=%+v", status, list.Functions)
	}

	var updateOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v2/projects/proj1/locations/us-central1/functions/my-fn",
		map[string]string{"description": "updated"}, &updateOp)
	if status != 200 || !updateOp.Done {
		t.Fatalf("update: status=%d op=%+v", status, updateOp)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v2/projects/proj1/locations/us-central1/functions/my-fn", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

// TestCreateRequiresRuntimeAndEntryPoint covers required-field validation.
func TestCreateRequiresRuntimeAndEntryPoint(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/functions?functionId=bad-fn",
		map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without runtime/entryPoint: want 400, got %d", status)
	}
}

// TestDuplicateCreateConflict asserts that creating a function whose
// functionId already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/functions?functionId=my-fn",
		validBody(), nil)

	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/functions?functionId=my-fn",
		validBody(), nil)
	if status != 409 {
		t.Fatalf("duplicate function: want 409, got %d", status)
	}
}
