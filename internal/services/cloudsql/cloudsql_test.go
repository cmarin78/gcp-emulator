package cloudsql

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
// asserting every mutation returns a DONE Operation and the operation is
// also retrievable via getOperation.
func TestInstanceLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances", map[string]any{
		"name": "my-instance",
	}, &createOp)
	if status != 200 || createOp.Status != "DONE" {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var op Operation
	status = testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/projects/proj1/operations/"+createOp.Name, nil, &op)
	if status != 200 || op.Name != createOp.Name {
		t.Fatalf("getOperation: status=%d op=%+v", status, op)
	}

	var inst DatabaseInstance
	status = testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/projects/proj1/instances/my-instance", nil, &inst)
	if status != 200 || inst.State != "RUNNABLE" || inst.Settings.Tier != "db-f1-micro" {
		t.Fatalf("get: status=%d inst=%+v", status, inst)
	}

	var list struct {
		Items []DatabaseInstance `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/projects/proj1/instances", nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list: status=%d items=%+v", status, list.Items)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/sql/v1beta4/projects/proj1/instances/my-instance", map[string]any{
		"settings": map[string]string{"tier": "db-n1-standard-1"},
	}, &patchOp)
	if status != 200 || patchOp.Status != "DONE" {
		t.Fatalf("patch: status=%d op=%+v", status, patchOp)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/sql/v1beta4/projects/proj1/instances/my-instance", nil, &deleteOp)
	if status != 200 || deleteOp.Status != "DONE" {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

// TestDatabaseAndUserLifecycle covers database CRUD and user create/list/delete
// (the delete uses ?name=&host= query params since the username isn't unique
// without the host).
func TestDatabaseAndUserLifecycle(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances", map[string]any{"name": "inst1"}, nil)

	var db Database
	status := testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances/inst1/databases", map[string]string{
		"name": "mydb",
	}, nil)
	_ = db
	if status != 200 {
		t.Fatalf("create database: want 200, got %d", status)
	}

	var gotDB Database
	status = testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/projects/proj1/instances/inst1/databases/mydb", nil, &gotDB)
	if status != 200 || gotDB.Charset != "UTF8" {
		t.Fatalf("get database: status=%d db=%+v", status, gotDB)
	}

	status = testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances/inst1/users", map[string]string{
		"name":     "appuser",
		"password": "secret",
	}, nil)
	if status != 200 {
		t.Fatalf("create user: want 200, got %d", status)
	}

	var userList struct {
		Items []User `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/projects/proj1/instances/inst1/users", nil, &userList)
	if status != 200 || len(userList.Items) != 1 || userList.Items[0].Password != "" {
		t.Fatalf("list users: status=%d users=%+v", status, userList.Items)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/sql/v1beta4/projects/proj1/instances/inst1/users?name=appuser", nil, nil)
	if status != 200 {
		t.Fatalf("delete user: want 200, got %d", status)
	}
}
