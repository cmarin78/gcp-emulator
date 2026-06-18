package logging

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

// TestSinkLifecycle covers create -> get -> list -> update -> delete,
// asserting the synthesized writerIdentity service account and that the
// sink's full resource name is keyed as "projects/{project}/sinks/{name}".
func TestSinkLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var sink LogSink
	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/sinks", map[string]any{
		"name":        "my-sink",
		"destination": "storage.googleapis.com/my-bucket",
		"filter":      "severity >= WARNING",
	}, &sink)
	if status != 200 || sink.Name != "projects/proj1/sinks/my-sink" {
		t.Fatalf("create: status=%d sink=%+v", status, sink)
	}
	if sink.WriterIdentity != "serviceAccount:emulator-logging@proj1.iam.gserviceaccount.com" {
		t.Fatalf("unexpected writerIdentity: %q", sink.WriterIdentity)
	}

	var got LogSink
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/sinks/my-sink", nil, &got)
	if status != 200 || got.Destination != "storage.googleapis.com/my-bucket" {
		t.Fatalf("get: status=%d sink=%+v", status, got)
	}

	var list struct {
		Sinks []LogSink `json:"sinks"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/sinks", nil, &list)
	if status != 200 || len(list.Sinks) != 1 {
		t.Fatalf("list: status=%d sinks=%+v", status, list.Sinks)
	}

	var updated LogSink
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v2/projects/proj1/sinks/my-sink", map[string]any{
		"destination": "storage.googleapis.com/my-bucket",
		"filter":      "severity >= ERROR",
		"disabled":    true,
	}, &updated)
	if status != 200 || updated.Filter != "severity >= ERROR" || !updated.Disabled {
		t.Fatalf("update: status=%d sink=%+v", status, updated)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v2/projects/proj1/sinks/my-sink", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

func TestCreateSinkRequiresName(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/sinks", map[string]any{
		"destination": "storage.googleapis.com/my-bucket",
	}, nil)
	if status != 400 {
		t.Fatalf("create without name: want 400, got %d", status)
	}
}

func TestCreateDuplicateSinkFails(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/sinks", map[string]any{
		"name": "dup", "destination": "storage.googleapis.com/b",
	}, nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/sinks", map[string]any{
		"name": "dup", "destination": "storage.googleapis.com/b",
	}, nil)
	if status != 409 {
		t.Fatalf("duplicate create: want 409, got %d", status)
	}
}

func TestGetSinkNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/sinks/missing", nil, nil)
	if status != 404 {
		t.Fatalf("get missing sink: want 404, got %d", status)
	}
}
