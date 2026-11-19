package serviceusage

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

func TestServiceUsageLifecycle(t *testing.T) {
	srv := newTestServer(t)

	// 1. Get non-enabled service -> should return state DISABLED
	var getDisabled Service
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/services/compute.googleapis.com", nil, &getDisabled)
	if getDisabled.State != "DISABLED" {
		t.Fatalf("expected state DISABLED, got %s", getDisabled.State)
	}

	// 2. Enable single service
	var enableOp Operation
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/my-proj/services/compute.googleapis.com:enable", nil, &enableOp)
	if !enableOp.Done {
		t.Fatal("expected operation to be done")
	}

	// Verify get after enable
	var getEnabled Service
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/services/compute.googleapis.com", nil, &getEnabled)
	if getEnabled.State != "ENABLED" {
		t.Fatalf("expected state ENABLED, got %s", getEnabled.State)
	}

	// 3. BatchEnable
	var batchOp Operation
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/my-proj/services:batchEnable", map[string]any{
		"serviceIds": []string{"storage.googleapis.com", "pubsub.googleapis.com"},
	}, &batchOp)
	if !batchOp.Done {
		t.Fatal("expected batch operation to be done")
	}

	// 4. List services
	var listResp struct {
		Services []Service `json:"services"`
	}
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/services?filter=state:ENABLED", nil, &listResp)
	if len(listResp.Services) != 3 {
		t.Fatalf("expected 3 enabled services, got %d", len(listResp.Services))
	}

	// 5. Disable service
	var disableOp Operation
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/my-proj/services/compute.googleapis.com:disable", nil, &disableOp)
	if !disableOp.Done {
		t.Fatal("expected disable operation to be done")
	}

	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/services/compute.googleapis.com", nil, &getEnabled)
	if getEnabled.State != "DISABLED" {
		t.Fatalf("expected state DISABLED after disable, got %s", getEnabled.State)
	}
}
