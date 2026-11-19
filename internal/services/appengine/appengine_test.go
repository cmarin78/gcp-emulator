package appengine

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

// ── Application tests ─────────────────────────────────────────────────────────

func TestCreateAndGetApp(t *testing.T) {
	srv := newTestServer(t)

	var op Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/appengine/v1/apps", map[string]any{
		"id":         "my-project",
		"locationId": "us-central",
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create app: status=%d op=%+v", status, op)
	}

	var got Application
	status = testutil.DoJSON(t, "GET", srv.URL+"/appengine/v1/apps/my-project", nil, &got)
	if status != 200 {
		t.Fatalf("get app: want 200, got %d", status)
	}
	if got.LocationID != "us-central" {
		t.Fatalf("get app: locationId=%q", got.LocationID)
	}
	if got.ServingStatus != "SERVING" {
		t.Fatalf("get app: servingStatus=%q", got.ServingStatus)
	}
}

func TestCreateAppRequiresID(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/appengine/v1/apps", map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("no id: want 400, got %d", status)
	}
}

func TestCreateAppDuplicate(t *testing.T) {
	srv := newTestServer(t)
	body := map[string]any{"id": "dup"}
	testutil.DoJSON(t, "POST", srv.URL+"/appengine/v1/apps", body, nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/appengine/v1/apps", body, nil)
	if status != 409 {
		t.Fatalf("duplicate: want 409, got %d", status)
	}
}

func TestGetAppNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", srv.URL+"/appengine/v1/apps/ghost", nil, nil)
	if status != 404 {
		t.Fatalf("want 404, got %d", status)
	}
}

func TestPatchApp(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/appengine/v1/apps", map[string]any{"id": "app1"}, nil)

	var op Operation
	status := testutil.DoJSON(t, "PATCH", srv.URL+"/appengine/v1/apps/app1", map[string]any{
		"servingStatus": "USER_DISABLED",
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("patch app: status=%d", status)
	}

	var got Application
	testutil.DoJSON(t, "GET", srv.URL+"/appengine/v1/apps/app1", nil, &got)
	if got.ServingStatus != "USER_DISABLED" {
		t.Fatalf("patch app: servingStatus=%q", got.ServingStatus)
	}
}

// ── Service tests ─────────────────────────────────────────────────────────────

func TestServicesListAndGet(t *testing.T) {
	srv := newTestServer(t)
	b := srv.URL + "/appengine/v1/apps/app1"

	// Create two versions in two services → services auto-created.
	testutil.DoJSON(t, "POST", b+"/services/default/versions", map[string]any{
		"id": "v1", "runtime": "go122",
	}, nil)
	testutil.DoJSON(t, "POST", b+"/services/api/versions", map[string]any{
		"id": "v1", "runtime": "go122",
	}, nil)

	var list struct {
		Services []Service `json:"services"`
	}
	status := testutil.DoJSON(t, "GET", b+"/services", nil, &list)
	if status != 200 || len(list.Services) != 2 {
		t.Fatalf("list services: want 2, got %d (status=%d)", len(list.Services), status)
	}

	var got Service
	status = testutil.DoJSON(t, "GET", b+"/services/default", nil, &got)
	if status != 200 || got.ID != "default" {
		t.Fatalf("get service: status=%d id=%q", status, got.ID)
	}
}

func TestServicePatchTrafficSplit(t *testing.T) {
	srv := newTestServer(t)
	b := srv.URL + "/appengine/v1/apps/app1"

	var op Operation
	status := testutil.DoJSON(t, "PATCH", b+"/services/default", map[string]any{
		"split": map[string]any{
			"shardBy":     "IP",
			"allocations": map[string]float64{"v1": 0.8, "v2": 0.2},
		},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("patch service: status=%d", status)
	}

	var got Service
	testutil.DoJSON(t, "GET", b+"/services/default", nil, &got)
	if got.Split == nil || got.Split.Allocations["v1"] != 0.8 {
		t.Fatalf("patch service: split=%+v", got.Split)
	}
}

func TestDeleteService(t *testing.T) {
	srv := newTestServer(t)
	b := srv.URL + "/appengine/v1/apps/app1"
	testutil.DoJSON(t, "POST", b+"/services/svc/versions", map[string]any{"id": "v1"}, nil)

	status := testutil.DoJSON(t, "DELETE", b+"/services/svc", nil, nil)
	if status != 200 {
		t.Fatalf("delete service: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", b+"/services/svc", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete service: want 404, got %d", status)
	}
}

// ── Version tests ─────────────────────────────────────────────────────────────

func TestVersionLifecycle(t *testing.T) {
	srv := newTestServer(t)
	b := srv.URL + "/appengine/v1/apps/app1/services/default/versions"

	// Create.
	var op Operation
	status := testutil.DoJSON(t, "POST", b, map[string]any{
		"id":      "v1",
		"runtime": "go122",
		"env":     "standard",
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create version: status=%d op=%+v", status, op)
	}

	// Get.
	var got Version
	status = testutil.DoJSON(t, "GET", b+"/v1", nil, &got)
	if status != 200 {
		t.Fatalf("get version: want 200, got %d", status)
	}
	if got.Runtime != "go122" || got.ServingStatus != "SERVING" {
		t.Fatalf("get version: runtime=%q status=%q", got.Runtime, got.ServingStatus)
	}

	// List.
	testutil.DoJSON(t, "POST", b, map[string]any{"id": "v2", "runtime": "go122"}, nil)
	var list struct {
		Versions []Version `json:"versions"`
	}
	testutil.DoJSON(t, "GET", b, nil, &list)
	if len(list.Versions) != 2 {
		t.Fatalf("list versions: want 2, got %d", len(list.Versions))
	}

	// Patch.
	status = testutil.DoJSON(t, "PATCH", b+"/v1", map[string]any{
		"servingStatus": "STOPPED",
	}, &op)
	if status != 200 {
		t.Fatalf("patch version: want 200, got %d", status)
	}

	// Delete.
	status = testutil.DoJSON(t, "DELETE", b+"/v1", nil, nil)
	if status != 200 {
		t.Fatalf("delete version: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", b+"/v1", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete version: want 404, got %d", status)
	}
}

func TestCreateVersionRequiresID(t *testing.T) {
	srv := newTestServer(t)
	b := srv.URL + "/appengine/v1/apps/app1/services/default/versions"
	status := testutil.DoJSON(t, "POST", b, map[string]any{"runtime": "go122"}, nil)
	if status != 400 {
		t.Fatalf("no id: want 400, got %d", status)
	}
}

func TestMigrateVersion(t *testing.T) {
	srv := newTestServer(t)
	b := srv.URL + "/appengine/v1/apps/app1/services/default"

	testutil.DoJSON(t, "POST", b+"/versions", map[string]any{"id": "v1"}, nil)
	testutil.DoJSON(t, "POST", b+"/versions", map[string]any{"id": "v2"}, nil)

	var op Operation
	status := testutil.DoJSON(t, "POST", b+"/versions/v2:migrate", map[string]any{}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("migrate: status=%d op=%+v", status, op)
	}

	// Service split should now point 100% to v2.
	var svc Service
	testutil.DoJSON(t, "GET", b, nil, &svc)
	if svc.Split == nil || svc.Split.Allocations["v2"] != 1.0 {
		t.Fatalf("migrate: split=%+v", svc.Split)
	}
}

func TestDeleteServiceCascadesVersions(t *testing.T) {
	srv := newTestServer(t)
	b := srv.URL + "/appengine/v1/apps/app1/services/svc1"

	testutil.DoJSON(t, "POST", b+"/versions", map[string]any{"id": "v1", "runtime": "go122"}, nil)
	testutil.DoJSON(t, "POST", b+"/versions", map[string]any{"id": "v2", "runtime": "go122"}, nil)

	var v1, v2 Version
	if status := testutil.DoJSON(t, "GET", b+"/versions/v1", nil, &v1); status != 200 {
		t.Fatalf("get v1: want 200, got %d", status)
	}
	if status := testutil.DoJSON(t, "GET", b+"/versions/v2", nil, &v2); status != 200 {
		t.Fatalf("get v2: want 200, got %d", status)
	}

	// Delete service
	if status := testutil.DoJSON(t, "DELETE", b, nil, nil); status != 200 {
		t.Fatalf("delete service: want 200, got %d", status)
	}

	// Verify versions are cascade deleted
	if status := testutil.DoJSON(t, "GET", b+"/versions/v1", nil, nil); status != 404 {
		t.Errorf("v1 after service delete: want 404, got %d", status)
	}
	if status := testutil.DoJSON(t, "GET", b+"/versions/v2", nil, nil); status != 404 {
		t.Errorf("v2 after service delete: want 404, got %d", status)
	}
}
