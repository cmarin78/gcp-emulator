package dataflow

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

func TestCreateBatchJobIsDone(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1b3/projects/proj1/locations/us-central1/jobs"

	var job Job
	status := testutil.DoJSON(t, "POST", base, map[string]any{
		"name": "my-batch-job",
		"type": "JOB_TYPE_BATCH",
	}, &job)
	if status != 200 {
		t.Fatalf("create batch job: want 200, got %d", status)
	}
	if job.ID == "" {
		t.Fatal("create batch job: missing id")
	}
	if job.CurrentState != "JOB_STATE_DONE" {
		t.Fatalf("batch job state: want JOB_STATE_DONE, got %q", job.CurrentState)
	}
	if job.Type != "JOB_TYPE_BATCH" {
		t.Fatalf("batch job type: want JOB_TYPE_BATCH, got %q", job.Type)
	}
	if job.Location != "us-central1" {
		t.Fatalf("batch job location: want us-central1, got %q", job.Location)
	}
}

func TestCreateStreamingJobIsRunning(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1b3/projects/proj1/locations/us-east1/jobs"

	var job Job
	status := testutil.DoJSON(t, "POST", base, map[string]any{
		"name": "my-streaming-job",
		"type": "JOB_TYPE_STREAMING",
	}, &job)
	if status != 200 {
		t.Fatalf("create streaming job: want 200, got %d", status)
	}
	if job.CurrentState != "JOB_STATE_RUNNING" {
		t.Fatalf("streaming job state: want JOB_STATE_RUNNING, got %q", job.CurrentState)
	}
	if job.Type != "JOB_TYPE_STREAMING" {
		t.Fatalf("streaming job type: want JOB_TYPE_STREAMING, got %q", job.Type)
	}
}

func TestDefaultTypeIsBatch(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1b3/projects/proj1/locations/us-central1/jobs"

	var job Job
	testutil.DoJSON(t, "POST", base, map[string]any{"name": "no-type-job"}, &job)
	if job.Type != "JOB_TYPE_BATCH" {
		t.Fatalf("default type: want JOB_TYPE_BATCH, got %q", job.Type)
	}
	if job.CurrentState != "JOB_STATE_DONE" {
		t.Fatalf("default type state: want JOB_STATE_DONE, got %q", job.CurrentState)
	}
}

func TestListAndGetJob(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1b3/projects/proj1/locations/us-central1/jobs"

	// Create two jobs.
	var j1, j2 Job
	testutil.DoJSON(t, "POST", base, map[string]any{"name": "job-a", "type": "JOB_TYPE_BATCH"}, &j1)
	testutil.DoJSON(t, "POST", base, map[string]any{"name": "job-b", "type": "JOB_TYPE_STREAMING"}, &j2)

	// List.
	var list struct {
		Jobs []Job `json:"jobs"`
	}
	status := testutil.DoJSON(t, "GET", base, nil, &list)
	if status != 200 {
		t.Fatalf("list jobs: want 200, got %d", status)
	}
	if len(list.Jobs) != 2 {
		t.Fatalf("list jobs: want 2, got %d", len(list.Jobs))
	}

	// Get by ID.
	var got Job
	status = testutil.DoJSON(t, "GET", base+"/"+j1.ID, nil, &got)
	if status != 200 {
		t.Fatalf("get job: want 200, got %d", status)
	}
	if got.ID != j1.ID || got.Name != "job-a" {
		t.Fatalf("get job: unexpected job %+v", got)
	}
}

func TestGetJobNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", srv.URL+"/v1b3/projects/proj1/locations/us-central1/jobs/nonexistent", nil, nil)
	if status != 404 {
		t.Fatalf("get nonexistent job: want 404, got %d", status)
	}
}

func TestCancelStreamingJob(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1b3/projects/proj1/locations/us-central1/jobs"

	var job Job
	testutil.DoJSON(t, "POST", base, map[string]any{"name": "stream", "type": "JOB_TYPE_STREAMING"}, &job)
	if job.CurrentState != "JOB_STATE_RUNNING" {
		t.Fatalf("pre-cancel: want JOB_STATE_RUNNING, got %q", job.CurrentState)
	}

	// Cancel.
	var updated Job
	status := testutil.DoJSON(t, "PUT", base+"/"+job.ID, map[string]any{
		"requestedState": "JOB_STATE_CANCELLED",
	}, &updated)
	if status != 200 {
		t.Fatalf("cancel: want 200, got %d", status)
	}
	if updated.CurrentState != "JOB_STATE_CANCELLED" {
		t.Fatalf("after cancel: want JOB_STATE_CANCELLED, got %q", updated.CurrentState)
	}

	// Confirm via GET.
	testutil.DoJSON(t, "GET", base+"/"+job.ID, nil, &updated)
	if updated.CurrentState != "JOB_STATE_CANCELLED" {
		t.Fatalf("get after cancel: want JOB_STATE_CANCELLED, got %q", updated.CurrentState)
	}
}

func TestUpdateJobNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "PUT",
		srv.URL+"/v1b3/projects/proj1/locations/us-central1/jobs/ghost",
		map[string]any{"requestedState": "JOB_STATE_CANCELLED"}, nil)
	if status != 404 {
		t.Fatalf("update nonexistent: want 404, got %d", status)
	}
}

func TestLegacyJobPaths(t *testing.T) {
	srv := newTestServer(t)
	legacyBase := srv.URL + "/v1b3/projects/proj1/jobs"

	// Create via legacy path → location defaults to us-central1.
	var job Job
	status := testutil.DoJSON(t, "POST", legacyBase, map[string]any{
		"name": "legacy-batch",
		"type": "JOB_TYPE_BATCH",
	}, &job)
	if status != 200 {
		t.Fatalf("legacy create: want 200, got %d", status)
	}
	if job.Location != defaultLegacyLocation {
		t.Fatalf("legacy location: want %q, got %q", defaultLegacyLocation, job.Location)
	}

	// Get via legacy path.
	var got Job
	status = testutil.DoJSON(t, "GET", legacyBase+"/"+job.ID, nil, &got)
	if status != 200 {
		t.Fatalf("legacy get: want 200, got %d", status)
	}
	if got.ID != job.ID {
		t.Fatalf("legacy get: wrong job %+v", got)
	}

	// List via legacy path spans all locations.
	var list struct {
		Jobs []Job `json:"jobs"`
	}
	testutil.DoJSON(t, "GET", legacyBase, nil, &list)
	if len(list.Jobs) < 1 {
		t.Fatalf("legacy list: want ≥1 job, got %d", len(list.Jobs))
	}
}

func TestListJobsIsolatedByLocation(t *testing.T) {
	srv := newTestServer(t)

	testutil.DoJSON(t, "POST", srv.URL+"/v1b3/projects/p/locations/us-central1/jobs",
		map[string]any{"name": "j1"}, nil)
	testutil.DoJSON(t, "POST", srv.URL+"/v1b3/projects/p/locations/europe-west1/jobs",
		map[string]any{"name": "j2"}, nil)

	var list struct {
		Jobs []Job `json:"jobs"`
	}
	// Regional list: only us-central1.
	testutil.DoJSON(t, "GET", srv.URL+"/v1b3/projects/p/locations/us-central1/jobs", nil, &list)
	if len(list.Jobs) != 1 {
		t.Fatalf("regional list us-central1: want 1, got %d", len(list.Jobs))
	}
	// Legacy list: all locations.
	testutil.DoJSON(t, "GET", srv.URL+"/v1b3/projects/p/jobs", nil, &list)
	if len(list.Jobs) != 2 {
		t.Fatalf("legacy list all: want 2, got %d", len(list.Jobs))
	}
}
