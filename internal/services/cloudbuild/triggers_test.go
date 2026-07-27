package cloudbuild

import (
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// newTestServer is defined in cloudbuild_test.go (same package).

func TestTriggerLifecycle(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1/projects/proj1/triggers"

	var created BuildTrigger
	status := testutil.DoJSON(t, "POST", base, map[string]any{
		"name":     "my-trigger",
		"filename": "cloudbuild.yaml",
		"github": map[string]any{
			"owner": "myorg",
			"name":  "myrepo",
			"push":  map[string]any{"branch": "^main$"},
		},
	}, &created)
	if status != 200 || created.ID == "" || created.Name != "my-trigger" {
		t.Fatalf("create: status=%d trigger=%+v", status, created)
	}

	var list struct {
		Triggers []BuildTrigger `json:"triggers"`
	}
	status = testutil.DoJSON(t, "GET", base, nil, &list)
	if status != 200 || len(list.Triggers) != 1 {
		t.Fatalf("list: status=%d triggers=%+v", status, list.Triggers)
	}

	var got BuildTrigger
	status = testutil.DoJSON(t, "GET", base+"/"+created.ID, nil, &got)
	if status != 200 || got.ID != created.ID {
		t.Fatalf("get: status=%d trigger=%+v", status, got)
	}

	var patched BuildTrigger
	status = testutil.DoJSON(t, "PATCH", base+"/"+created.ID, map[string]any{
		"description": "updated",
		"filename":    "cloudbuild.yaml",
	}, &patched)
	if status != 200 || patched.Description != "updated" || patched.ID != created.ID {
		t.Fatalf("patch: status=%d trigger=%+v", status, patched)
	}

	status = testutil.DoJSON(t, "DELETE", base+"/"+created.ID, nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", base+"/"+created.ID, nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestTriggerRun(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1/projects/proj1/triggers"

	var created BuildTrigger
	testutil.DoJSON(t, "POST", base, map[string]any{"name": "run-me", "filename": "cloudbuild.yaml"}, &created)

	var op Operation
	status := testutil.DoJSON(t, "POST", base+"/"+created.ID+":run", map[string]any{}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("trigger:run: status=%d op=%+v", status, op)
	}
}

func TestTriggerGetNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/triggers/nonexistent", nil, nil)
	if status != 404 {
		t.Fatalf("get nonexistent: want 404, got %d", status)
	}
}

func TestTriggerDeleteNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/triggers/nonexistent", nil, nil)
	if status != 404 {
		t.Fatalf("delete nonexistent: want 404, got %d", status)
	}
}
