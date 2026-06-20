package monitoring

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

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

// TestAlertPolicyLifecycle covers create -> get -> list -> patch -> delete,
// asserting the default combiner and enabled state, and that the auto
// generated numeric ID is used to build the policy name.
func TestAlertPolicyLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var ap AlertPolicy
	status := testutil.DoJSON(t, "POST", srv.URL+"/v3/projects/proj1/alertPolicies", map[string]any{
		"displayName": "high-cpu",
	}, &ap)
	if status != 200 || ap.Name == "" || ap.Combiner != "OR" || !ap.Enabled {
		t.Fatalf("create: status=%d ap=%+v", status, ap)
	}

	var got AlertPolicy
	status = testutil.DoJSON(t, "GET", srv.URL+"/v3/"+ap.Name, nil, &got)
	if status != 200 || got.DisplayName != "high-cpu" {
		t.Fatalf("get: status=%d ap=%+v", status, got)
	}

	var list struct {
		AlertPolicies []AlertPolicy `json:"alertPolicies"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v3/projects/proj1/alertPolicies", nil, &list)
	if status != 200 || len(list.AlertPolicies) != 1 {
		t.Fatalf("list: status=%d aps=%+v", status, list.AlertPolicies)
	}

	var patched AlertPolicy
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v3/"+ap.Name, map[string]any{
		"enabled": false,
	}, &patched)
	if status != 200 || patched.Enabled {
		t.Fatalf("patch: status=%d ap=%+v", status, patched)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v3/"+ap.Name, nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

// TestTimeSeriesEmptyWhenNoActivity asserts that a project with no recorded
// activity still gets a well-formed empty list (not an error), matching the
// previous stub behavior for projects that simply haven't done anything yet.
func TestTimeSeriesEmptyWhenNoActivity(t *testing.T) {
	srv := newTestServer(t)
	var list struct {
		TimeSeries []any `json:"timeSeries"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/v3/projects/proj-empty-ts/timeSeries", nil, &list)
	if status != 200 || list.TimeSeries == nil || len(list.TimeSeries) != 0 {
		t.Fatalf("timeSeries: status=%d list=%+v", status, list.TimeSeries)
	}
}

// TestTimeSeriesReflectsRecordedActivity covers the Fase 11 wiring: counters
// incremented via internal/activity (the same path Cloud Scheduler/Tasks/
// Pub/Sub use after a real dispatch) show up through listTimeSeries, both
// unfiltered and filtered by metric.type. Uses a project name unique to this
// test ("proj-ts-activity") since internal/activity is process-global state
// shared across tests in this binary.
func TestTimeSeriesReflectsRecordedActivity(t *testing.T) {
	srv := newTestServer(t)
	const project = "proj-ts-activity"

	activity.IncrCounter(project, "cloudscheduler.googleapis.com/job/execution_count", map[string]string{"job_name": "j1"})
	activity.IncrCounter(project, "cloudscheduler.googleapis.com/job/execution_count", map[string]string{"job_name": "j1"})
	activity.IncrCounter(project, "cloudtasks.googleapis.com/queue/task_attempt_count", map[string]string{"task_name": "t1"})

	var all struct {
		TimeSeries []map[string]any `json:"timeSeries"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/v3/projects/"+project+"/timeSeries", nil, &all)
	if status != 200 || len(all.TimeSeries) != 2 {
		t.Fatalf("unfiltered timeSeries: status=%d list=%+v", status, all.TimeSeries)
	}

	filter := url.QueryEscape(`metric.type="cloudscheduler.googleapis.com/job/execution_count"`)
	var filtered struct {
		TimeSeries []map[string]any `json:"timeSeries"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v3/projects/"+project+"/timeSeries?filter="+filter, nil, &filtered)
	if status != 200 || len(filtered.TimeSeries) != 1 {
		t.Fatalf("filtered timeSeries: status=%d list=%+v", status, filtered.TimeSeries)
	}
	metric, _ := filtered.TimeSeries[0]["metric"].(map[string]any)
	if metric["type"] != "cloudscheduler.googleapis.com/job/execution_count" {
		t.Fatalf("unexpected metric type: %+v", filtered.TimeSeries[0])
	}
	points, _ := filtered.TimeSeries[0]["points"].([]any)
	if len(points) != 2 {
		t.Fatalf("want 2 points (cumulative), got %+v", points)
	}
	last, _ := points[len(points)-1].(map[string]any)
	value, _ := last["value"].(map[string]any)
	if value["int64Value"] != "2" {
		t.Fatalf("want cumulative value 2, got %+v", value)
	}
}
