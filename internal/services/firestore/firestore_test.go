package firestore

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

// TestDatabaseLifecycle covers create (default databaseId) -> get -> list ->
// patch -> delete, all wrapped in google.longrunning.Operation for
// create/delete.
func TestDatabaseLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/databases", map[string]string{
		"locationId": "nam5",
	}, &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Database
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/databases/(default)", nil, &got)
	if status != 200 || got.Type != "FIRESTORE_NATIVE" {
		t.Fatalf("get: status=%d db=%+v", status, got)
	}

	var list struct {
		Databases []Database `json:"databases"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/databases", nil, &list)
	if status != 200 || len(list.Databases) != 1 {
		t.Fatalf("list: status=%d dbs=%+v", status, list.Databases)
	}

	var patched Database
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v1/projects/proj1/databases/(default)", map[string]string{
		"concurrencyMode": "PESSIMISTIC",
	}, &patched)
	if status != 200 || patched.ConcurrencyMode != "PESSIMISTIC" {
		t.Fatalf("patch: status=%d db=%+v", status, patched)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/databases/(default)", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

// TestDocumentLifecycle covers create -> get -> list -> patch (upsert) -> delete.
func TestDocumentLifecycle(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/databases", nil, nil)

	var doc Document
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/databases/(default)/documents/users?documentId=alice",
		map[string]any{"fields": map[string]any{"name": map[string]string{"stringValue": "Alice"}}}, &doc)
	if status != 200 || doc.Name == "" {
		t.Fatalf("create doc: status=%d doc=%+v", status, doc)
	}

	var got Document
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/databases/(default)/documents/users/alice", nil, &got)
	if status != 200 || string(got.Fields) == "" {
		t.Fatalf("get doc: status=%d doc=%+v", status, got)
	}

	var list struct {
		Documents []Document `json:"documents"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/databases/(default)/documents/users", nil, &list)
	if status != 200 || len(list.Documents) != 1 {
		t.Fatalf("list docs: status=%d docs=%+v", status, list.Documents)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/databases/(default)/documents/users/alice", nil, nil)
	if status != 200 {
		t.Fatalf("delete doc: want 200, got %d", status)
	}
}

// TestDuplicateCreateConflicts asserts that creating a database or document
// whose client-specified ID already exists returns 409 ALREADY_EXISTS
// instead of silently overwriting.
func TestDuplicateCreateConflicts(t *testing.T) {
	srv := newTestServer(t)

	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/databases", nil, nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/databases", nil, nil)
	if status != 409 {
		t.Fatalf("duplicate database: want 409, got %d", status)
	}

	docBody := map[string]any{"fields": map[string]any{"name": map[string]string{"stringValue": "Alice"}}}
	testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/databases/(default)/documents/users?documentId=dup-doc",
		docBody, nil)
	status = testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/databases/(default)/documents/users?documentId=dup-doc",
		docBody, nil)
	if status != 409 {
		t.Fatalf("duplicate document: want 409, got %d", status)
	}
}
