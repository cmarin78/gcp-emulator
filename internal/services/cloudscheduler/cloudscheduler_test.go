package cloudscheduler

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

// TestJobLifecycle covers create -> get -> list -> delete.
func TestJobLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var job Job
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs", map[string]any{
		"name":     "projects/proj1/locations/us-central1/jobs/my-job",
		"schedule": "* * * * *",
	}, &job)
	if status != 200 || job.State != "ENABLED" {
		t.Fatalf("create: status=%d job=%+v", status, job)
	}

	var got Job
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs/my-job", nil, &got)
	if status != 200 || got.Schedule != "* * * * *" {
		t.Fatalf("get: status=%d job=%+v", status, got)
	}

	var list struct {
		Jobs []Job `json:"jobs"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs", nil, &list)
	if status != 200 || len(list.Jobs) != 1 {
		t.Fatalf("list: status=%d jobs=%+v", status, list.Jobs)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs/my-job", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

// TestJobActionsRunPauseResume covers the ":run"/":pause"/":resume" verb
// dispatch, which relies on capturing the full path segment and splitting
// on ":" -- the same fragile pattern used across several services.
func TestJobActionsRunPauseResume(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs", map[string]any{
		"name": "projects/proj1/locations/us-central1/jobs/my-job",
	}, nil)

	var ran Job
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs/my-job:run", nil, &ran)
	if status != 200 || ran.LastAttemptTime == "" {
		t.Fatalf("run: status=%d job=%+v", status, ran)
	}

	var paused Job
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs/my-job:pause", nil, &paused)
	if status != 200 || paused.State != "PAUSED" {
		t.Fatalf("pause: status=%d job=%+v", status, paused)
	}

	var resumed Job
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs/my-job:resume", nil, &resumed)
	if status != 200 || resumed.State != "ENABLED" {
		t.Fatalf("resume: status=%d job=%+v", status, resumed)
	}
}

// TestDuplicateCreateConflict asserts that creating a job whose
// client-specified name already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	body := map[string]any{"name": "projects/proj1/locations/us-central1/jobs/dup-job"}
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs", body, nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs", body, nil)
	if status != 409 {
		t.Fatalf("duplicate job: want 409, got %d", status)
	}
}
