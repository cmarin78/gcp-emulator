package eventarc

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

func validTriggerBody() map[string]any {
	return map[string]any{
		"eventFilters": []map[string]any{
			{"attribute": "type", "value": "google.cloud.storage.object.v1.finalized"},
		},
		"destination": map[string]any{
			"cloudRun": map[string]any{"service": "my-svc", "region": "us-central1"},
		},
	}
}

// TestTriggerLifecycle covers create -> get -> list -> patch -> delete,
// asserting a synthesized pubsub subscription name is always present
// (every trigger has an underlying transport in the real API).
func TestTriggerLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers?triggerId=my-trigger",
		validTriggerBody(), &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Trigger
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers/my-trigger", nil, &got)
	if status != 200 || len(got.EventFilters) != 1 || got.Transport.Pubsub == nil || got.Transport.Pubsub.Subscription == "" {
		t.Fatalf("get: status=%d trigger=%+v", status, got)
	}
	if got.Destination.CloudRun == nil || got.Destination.CloudRun.Service != "my-svc" {
		t.Fatalf("expected cloudRun destination, got %+v", got.Destination)
	}

	var list struct {
		Triggers []Trigger `json:"triggers"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers", nil, &list)
	if status != 200 || len(list.Triggers) != 1 {
		t.Fatalf("list: status=%d triggers=%+v", status, list.Triggers)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers/my-trigger",
		map[string]any{"labels": map[string]string{"env": "prod"}}, &patchOp)
	if status != 200 || !patchOp.Done {
		t.Fatalf("patch: status=%d op=%+v", status, patchOp)
	}
	var patched Trigger
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers/my-trigger", nil, &patched)
	if patched.Labels["env"] != "prod" {
		t.Fatalf("expected labels updated, got %+v", patched.Labels)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers/my-trigger", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

func TestCreateTriggerRequiresEventFilters(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers?triggerId=bad",
		map[string]any{"destination": map[string]any{"cloudRun": map[string]any{"service": "svc"}}}, nil)
	if status != 400 {
		t.Fatalf("create without eventFilters: want 400, got %d", status)
	}
}

func TestCreateTriggerRequiresDestination(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers?triggerId=bad",
		map[string]any{"eventFilters": []map[string]any{{"attribute": "type", "value": "x"}}}, nil)
	if status != 400 {
		t.Fatalf("create without destination: want 400, got %d", status)
	}
}

// TestDuplicateCreateConflict asserts that creating a trigger whose
// triggerId already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers?triggerId=my-trigger",
		validTriggerBody(), nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers?triggerId=my-trigger",
		validTriggerBody(), nil)
	if status != 409 {
		t.Fatalf("duplicate trigger: want 409, got %d", status)
	}
}

func TestTriggerNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers/missing", nil, nil)
	if status != 404 {
		t.Fatalf("get missing trigger: want 404, got %d", status)
	}
}
