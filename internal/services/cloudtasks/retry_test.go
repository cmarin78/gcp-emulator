package cloudtasks

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cesar/gcp-emulator/internal/activity"
	"github.com/cesar/gcp-emulator/internal/testutil"
)

// TestTaskDispatchRetriesUntilSuccess covers Fase 16's retry/backoff
// addition: a target that fails its first two requests and succeeds on the
// third must end up with dispatchCount == 3 and a single terminal INFO log
// (not an ERROR), given a queue retryConfig with maxAttempts=5 and a small
// backoff so the test doesn't have to wait long.
func TestTaskDispatchRetriesUntilSuccess(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(target.Close)

	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues", map[string]any{
		"name": "projects/proj1/locations/us-central1/queues/q-retry",
		"retryConfig": map[string]any{
			"maxAttempts": 5,
			"minBackoff":  "10ms",
			"maxBackoff":  "20ms",
		},
	}, nil)

	var task Task
	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/q-retry/tasks", map[string]any{
		"task": map[string]any{
			"httpRequest": map[string]any{"url": target.URL, "httpMethod": "POST"},
		},
	}, &task)
	if status != 200 || task.Name == "" {
		t.Fatalf("create task: status=%d task=%+v", status, task)
	}

	deadline := time.Now().Add(3 * time.Second)
	var got Task
	for time.Now().Before(deadline) {
		testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/q-retry/tasks/task-1", nil, &got)
		if got.DispatchCount >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.DispatchCount != 3 {
		t.Fatalf("dispatchCount = %d, want 3 (2 failures + 1 success)", got.DispatchCount)
	}
	if hits.Load() != 3 {
		t.Fatalf("target received %d requests, want 3", hits.Load())
	}

	// Poll for the terminal log entry; it's written after the loop
	// finishes, slightly after dispatchCount's last update above.
	deadline = time.Now().Add(2 * time.Second)
	for {
		logs := activity.ListLogs("proj1")
		if len(logs) > 0 {
			if logs[len(logs)-1].Severity != "INFO" {
				t.Fatalf("expected a terminal INFO log on eventual success, got %+v", logs[len(logs)-1])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the terminal log entry")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTaskDispatchExhaustsRetriesAndLogsError covers the always-failing
// case: dispatchCount must stop exactly at maxAttempts (not retry forever),
// and the terminal log must be ERROR.
func TestTaskDispatchExhaustsRetriesAndLogsError(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(target.Close)

	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj2/locations/us-central1/queues", map[string]any{
		"name": "projects/proj2/locations/us-central1/queues/q-fail",
		"retryConfig": map[string]any{
			"maxAttempts": 2,
			"minBackoff":  "5ms",
			"maxBackoff":  "10ms",
		},
	}, nil)

	testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj2/locations/us-central1/queues/q-fail/tasks", map[string]any{
		"task": map[string]any{
			"httpRequest": map[string]any{"url": target.URL, "httpMethod": "POST"},
		},
	}, nil)

	deadline := time.Now().Add(3 * time.Second)
	var got Task
	for time.Now().Before(deadline) {
		testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj2/locations/us-central1/queues/q-fail/tasks/task-1", nil, &got)
		if got.DispatchCount >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.DispatchCount != 2 {
		t.Fatalf("dispatchCount = %d, want exactly 2 (capped at maxAttempts)", got.DispatchCount)
	}

	deadline = time.Now().Add(2 * time.Second)
	for {
		logs := activity.ListLogs("proj2")
		if len(logs) > 0 {
			if logs[len(logs)-1].Severity != "ERROR" {
				t.Fatalf("expected a terminal ERROR log after exhausting retries, got %+v", logs[len(logs)-1])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the terminal log entry")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
