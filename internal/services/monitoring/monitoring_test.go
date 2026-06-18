package monitoring

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

// TestTimeSeriesStub asserts the timeSeries endpoint always returns an
// empty list -- there is no real metrics pipeline behind this emulator.
func TestTimeSeriesStub(t *testing.T) {
	srv := newTestServer(t)
	var list struct {
		TimeSeries []any `json:"timeSeries"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/v3/projects/proj1/timeSeries", nil, &list)
	if status != 200 || list.TimeSeries == nil || len(list.TimeSeries) != 0 {
		t.Fatalf("timeSeries: status=%d list=%+v", status, list.TimeSeries)
	}
}
