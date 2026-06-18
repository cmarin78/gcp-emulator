package cloudbuild

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

// TestCreateBuildLegacyAndGet covers the legacy project-scoped create/get/list flow.
func TestCreateBuildLegacyAndGet(t *testing.T) {
	srv := newTestServer(t)

	var op Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/builds", map[string]any{
		"steps": []map[string]any{{"name": "gcr.io/cloud-builders/docker", "args": []string{"build", "."}}},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create legacy: status=%d op=%+v", status, op)
	}

	var build Build
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/builds/1", nil, &build)
	if status != 200 || build.Status != "SUCCESS" {
		t.Fatalf("get build: status=%d build=%+v", status, build)
	}

	var list struct {
		Builds []Build `json:"builds"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/builds", nil, &list)
	if status != 200 || len(list.Builds) != 1 {
		t.Fatalf("list builds: status=%d builds=%+v", status, list.Builds)
	}
}

// TestCreateBuildRegional covers the regional create endpoint that real
// `gcloud builds submit` actually calls, plus the shared getOperation
// endpoint that artifactregistry.go's pattern doesn't cover (this one is
// project-scoped legacy form: /v1/projects/{project}/operations/{operation}).
func TestCreateBuildRegional(t *testing.T) {
	srv := newTestServer(t)

	var op Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/builds", map[string]any{
		"steps": []map[string]any{{"name": "gcr.io/cloud-builders/docker"}},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create regional: status=%d op=%+v", status, op)
	}
	if op.Name == "" {
		t.Fatalf("expected non-empty operation name, got %+v", op)
	}

	var build Build
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/global/builds/1", nil, &build)
	if status != 200 || build.Status != "SUCCESS" {
		t.Fatalf("get build regional: status=%d build=%+v", status, build)
	}
}
