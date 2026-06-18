package cloudrun

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

func validBody() map[string]any {
	return map[string]any{
		"template": map[string]any{
			"containers": []map[string]string{{"image": "gcr.io/proj1/my-image:latest"}},
		},
	}
}

// TestServiceLifecycle covers create -> get -> list -> update (generation
// bump) -> delete.
func TestServiceLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/services?serviceId=my-svc",
		validBody(), &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var svc Service
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/services/my-svc", nil, &svc)
	if status != 200 || svc.Generation != "1" || svc.URI == "" {
		t.Fatalf("get: status=%d svc=%+v", status, svc)
	}

	var list struct {
		Services []Service `json:"services"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/services", nil, &list)
	if status != 200 || len(list.Services) != 1 {
		t.Fatalf("list: status=%d services=%+v", status, list.Services)
	}

	var updateOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v2/projects/proj1/locations/us-central1/services/my-svc",
		validBody(), &updateOp)
	if status != 200 || !updateOp.Done {
		t.Fatalf("update: status=%d op=%+v", status, updateOp)
	}
	var updated Service
	testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/services/my-svc", nil, &updated)
	if updated.Generation != "2" {
		t.Fatalf("expected generation bump to 2, got %q", updated.Generation)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v2/projects/proj1/locations/us-central1/services/my-svc", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

func TestCreateRequiresContainerImage(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/services?serviceId=bad-svc",
		map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without container image: want 400, got %d", status)
	}
}

// TestDuplicateCreateConflict asserts that creating a service whose
// serviceId already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/services?serviceId=my-svc",
		validBody(), nil)

	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/services?serviceId=my-svc",
		validBody(), nil)
	if status != 409 {
		t.Fatalf("duplicate service: want 409, got %d", status)
	}
}
