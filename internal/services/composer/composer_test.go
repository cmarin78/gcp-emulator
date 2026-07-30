package composer

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

func base(srvURL string) string {
	return srvURL + "/composer/v1/projects/proj1/locations/us-central1/environments"
}

func TestCreateEnvironmentIsRunning(t *testing.T) {
	srv := newTestServer(t)

	var op Operation
	status := testutil.DoJSON(t, "POST", base(srv.URL), map[string]any{
		"name": "my-env",
		"config": map[string]any{
			"nodeCount": 3,
			"softwareConfig": map[string]any{
				"imageVersion": "composer-2.6.3-airflow-2.7.3",
			},
		},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create env: status=%d op=%+v", status, op)
	}
	if op.Name == "" {
		t.Fatal("create env: missing operation name")
	}
}

func TestCreateEnvironmentRequiresName(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", base(srv.URL), map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("no name: want 400, got %d", status)
	}
}

func TestCreateEnvironmentDuplicate(t *testing.T) {
	srv := newTestServer(t)
	body := map[string]any{"name": "dup"}
	testutil.DoJSON(t, "POST", base(srv.URL), body, nil)
	status := testutil.DoJSON(t, "POST", base(srv.URL), body, nil)
	if status != 409 {
		t.Fatalf("duplicate: want 409, got %d", status)
	}
}

func TestListAndGetEnvironment(t *testing.T) {
	srv := newTestServer(t)
	b := base(srv.URL)

	testutil.DoJSON(t, "POST", b, map[string]any{"name": "env1"}, nil)
	testutil.DoJSON(t, "POST", b, map[string]any{"name": "env2"}, nil)

	var list struct {
		Environments []Environment `json:"environments"`
	}
	status := testutil.DoJSON(t, "GET", b, nil, &list)
	if status != 200 || len(list.Environments) != 2 {
		t.Fatalf("list: want 2, got %d (status=%d)", len(list.Environments), status)
	}

	var got Environment
	status = testutil.DoJSON(t, "GET", b+"/env1", nil, &got)
	if status != 200 {
		t.Fatalf("get: want 200, got %d", status)
	}
	if got.State != "RUNNING" {
		t.Fatalf("get: state=%q, want RUNNING", got.State)
	}
	if got.CreateTime == "" {
		t.Fatal("get: missing createTime")
	}
}

func TestGetEnvironmentNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", base(srv.URL)+"/ghost", nil, nil)
	if status != 404 {
		t.Fatalf("want 404, got %d", status)
	}
}

func TestPatchEnvironment(t *testing.T) {
	srv := newTestServer(t)
	b := base(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{"name": "env1"}, nil)

	var op Operation
	status := testutil.DoJSON(t, "PATCH", b+"/env1", map[string]any{
		"labels": map[string]string{"team": "data"},
		"config": map[string]any{"nodeCount": 6},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("patch: status=%d", status)
	}

	var got Environment
	testutil.DoJSON(t, "GET", b+"/env1", nil, &got)
	if got.Labels["team"] != "data" {
		t.Fatalf("patch: labels=%v", got.Labels)
	}
	if got.Config == nil || got.Config.NodeCount != 6 {
		t.Fatalf("patch: nodeCount=%v", got.Config)
	}
}

func TestDeleteEnvironment(t *testing.T) {
	srv := newTestServer(t)
	b := base(srv.URL)
	testutil.DoJSON(t, "POST", b, map[string]any{"name": "del"}, nil)

	status := testutil.DoJSON(t, "DELETE", b+"/del", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", b+"/del", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestDeleteEnvironmentNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "DELETE", base(srv.URL)+"/ghost", nil, nil)
	if status != 404 {
		t.Fatalf("want 404, got %d", status)
	}
}

func TestListEnvironmentsIsolatedByLocation(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST",
		srv.URL+"/composer/v1/projects/p/locations/us-central1/environments",
		map[string]any{"name": "e1"}, nil)
	testutil.DoJSON(t, "POST",
		srv.URL+"/composer/v1/projects/p/locations/europe-west1/environments",
		map[string]any{"name": "e2"}, nil)

	var list struct {
		Environments []Environment `json:"environments"`
	}
	testutil.DoJSON(t, "GET",
		srv.URL+"/composer/v1/projects/p/locations/us-central1/environments", nil, &list)
	if len(list.Environments) != 1 {
		t.Fatalf("location isolation: want 1, got %d", len(list.Environments))
	}
}
