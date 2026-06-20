package cloudscheduler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cesar/gcp-emulator/internal/activity"
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

// TestJobRunDispatchesRealHTTP asserts that ":run" on a job with a real
// httpTarget performs an actual HTTP call — the Phase 11 behavioral
// upgrade — rather than only touching timestamps.
func TestJobRunDispatchesRealHTTP(t *testing.T) {
	hit := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		hit <- string(body)
	}))
	t.Cleanup(target.Close)

	srv := newTestServer(t)
	// Schedule far in the future so the background firing goroutine doesn't
	// also hit the target during the test, keeping the assertion below
	// (single message on a buffered channel of size 1) deterministic.
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs", map[string]any{
		"name":     "projects/proj1/locations/us-central1/jobs/run-job",
		"schedule": "0 0 1 1 *",
		"httpTarget": map[string]any{
			"uri":        target.URL,
			"httpMethod": "POST",
			"body":       "aGVsbG8=", // base64("hello")
		},
	}, nil)

	var ran Job
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/jobs/run-job:run", nil, &ran)
	if status != 200 || ran.LastAttemptTime == "" {
		t.Fatalf("run: status=%d job=%+v", status, ran)
	}

	select {
	case body := <-hit:
		if body != "hello" {
			t.Fatalf("dispatched body = %q, want %q", body, "hello")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for real HTTP dispatch")
	}

	// activity.RecordLog/IncrCounter run just after the HTTP call completes
	// (Fase 11 Logging/Monitoring wiring), slightly after the target server
	// already received the request -- poll briefly instead of asserting
	// immediately.
	deadline := time.Now().Add(2 * time.Second)
	for {
		logs := activity.ListLogs("proj1")
		if len(logs) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for activity log entry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	series := activity.ListTimeSeries("proj1", "cloudscheduler.googleapis.com/job/execution_count")
	if len(series) != 1 || len(series[0].Points) == 0 {
		t.Fatalf("want a recorded execution_count series, got %+v", series)
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
