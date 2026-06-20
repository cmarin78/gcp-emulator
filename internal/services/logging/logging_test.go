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

// TestEntriesWriteAndList covers the round trip: a client writes entries
// (one with a top-level default severity, one overriding it), then lists
// them back filtered by resourceNames, asserting both are present with the
// expected fields. Uses a project name unique to this test ("proj-entries")
// since internal/activity is process-global state shared across tests in
// this binary.
func TestEntriesWriteAndList(t *testing.T) {
	srv := newTestServer(t)

	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/entries:write", map[string]any{
		"logName":  "projects/proj-entries/logs/my-log",
		"severity": "INFO",
		"resource": map[string]any{"type": "global"},
		"entries": []map[string]any{
			{"textPayload": "first entry"},
			{"textPayload": "second entry", "severity": "ERROR"},
		},
	}, nil)
	if status != 200 {
		t.Fatalf("entries:write: want 200, got %d", status)
	}

	var list struct {
		Entries []map[string]any `json:"entries"`
	}
	status = testutil.DoJSON(t, "POST", srv.URL+"/v2/entries:list", map[string]any{
		"resourceNames": []string{"projects/proj-entries"},
	}, &list)
	if status != 200 || len(list.Entries) != 2 {
		t.Fatalf("entries:list: status=%d entries=%+v", status, list.Entries)
	}
	if list.Entries[0]["severity"] != "INFO" || list.Entries[0]["textPayload"] != "first entry" {
		t.Fatalf("unexpected first entry: %+v", list.Entries[0])
	}
	if list.Entries[1]["severity"] != "ERROR" || list.Entries[1]["textPayload"] != "second entry" {
		t.Fatalf("unexpected second entry: %+v", list.Entries[1])
	}
}

// TestEntriesListFilterBySeverity covers the simple substring filter
// support: filtering by "ERROR" should only return the matching entry.
func TestEntriesListFilterBySeverity(t *testing.T) {
	srv := newTestServer(t)

	testutil.DoJSON(t, "POST", srv.URL+"/v2/entries:write", map[string]any{
		"logName": "projects/proj-entries-filter/logs/my-log",
		"entries": []map[string]any{
			{"textPayload": "ok one", "severity": "INFO"},
			{"textPayload": "bad one", "severity": "ERROR"},
		},
	}, nil)

	var list struct {
		Entries []map[string]any `json:"entries"`
	}
	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/entries:list", map[string]any{
		"resourceNames": []string{"projects/proj-entries-filter"},
		"filter":        "ERROR",
	}, &list)
	if status != 200 || len(list.Entries) != 1 || list.Entries[0]["textPayload"] != "bad one" {
		t.Fatalf("filtered list: status=%d entries=%+v", status, list.Entries)
	}
}
