package spanner

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

// TestInstanceLifecycle covers create -> get -> list -> patch -> delete.
func TestInstanceLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances", map[string]any{
		"instanceId": "my-instance",
	}, &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Instance
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances/my-instance", nil, &got)
	if status != 200 || got.State != "READY" || got.Config != "regional-us-central1" || got.ProcessingUnits != 100 {
		t.Fatalf("get: status=%d inst=%+v", status, got)
	}

	var list struct {
		Instances []Instance `json:"instances"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances", nil, &list)
	if status != 200 || len(list.Instances) != 1 {
		t.Fatalf("list: status=%d instances=%+v", status, list.Instances)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v1/projects/proj1/instances/my-instance", map[string]any{
		"instance": map[string]any{"nodeCount": 3},
	}, &patchOp)
	if status != 200 || !patchOp.Done {
		t.Fatalf("patch: status=%d op=%+v", status, patchOp)
	}
	var patched Instance
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances/my-instance", nil, &patched)
	if patched.NodeCount != 3 {
		t.Fatalf("expected nodeCount updated to 3, got %d", patched.NodeCount)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/instances/my-instance", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

// TestDatabaseLifecycle covers create (via CREATE DATABASE DDL statement) ->
// get -> list -> updateDdl -> delete, asserting the database name is parsed
// correctly out of the createStatement.
func TestDatabaseLifecycle(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances", map[string]any{"instanceId": "inst1"}, nil)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances/inst1/databases", map[string]any{
		"createStatement": "CREATE DATABASE `mydb`",
	}, &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create database: status=%d op=%+v", status, createOp)
	}

	var got Database
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances/inst1/databases/mydb", nil, &got)
	if status != 200 || got.State != "READY" || got.DatabaseDialect != "GOOGLE_STANDARD_SQL" {
		t.Fatalf("get database: status=%d db=%+v", status, got)
	}

	var list struct {
		Databases []Database `json:"databases"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances/inst1/databases", nil, &list)
	if status != 200 || len(list.Databases) != 1 {
		t.Fatalf("list databases: status=%d dbs=%+v", status, list.Databases)
	}

	var ddlOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v1/projects/proj1/instances/inst1/databases/mydb/ddl", map[string]any{
		"statements": []string{"ALTER TABLE foo ADD COLUMN bar STRING(MAX)"},
	}, &ddlOp)
	if status != 200 || !ddlOp.Done {
		t.Fatalf("update ddl: status=%d op=%+v", status, ddlOp)
	}
	var afterDdl Database
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances/inst1/databases/mydb", nil, &afterDdl)
	if len(afterDdl.ExtraStatements) != 1 {
		t.Fatalf("expected extraStatements appended, got %+v", afterDdl.ExtraStatements)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/instances/inst1/databases/mydb", nil, nil)
	if status != 200 {
		t.Fatalf("delete database: want 200, got %d", status)
	}
}

func TestCreateDatabaseRequiresValidCreateStatement(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances", map[string]any{"instanceId": "inst1"}, nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances/inst1/databases", map[string]any{
		"createStatement": "not a valid statement",
	}, nil)
	if status != 400 {
		t.Fatalf("invalid createStatement: want 400, got %d", status)
	}
}
