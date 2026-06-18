package artifactregistry

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

// TestRepositoryLifecycle covers create (via both repositoryId and
// repository_id query params -- the exact Phase-1-round bug fixed for the
// Terraform provider) -> get -> list -> delete.
func TestRepositoryLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/locations/us-central1/repositories?repository_id=my-repo",
		map[string]string{"format": "DOCKER"}, &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create (repository_id): status=%d op=%+v", status, createOp)
	}

	var repo Repository
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/repositories/my-repo", nil, &repo)
	if status != 200 || repo.Format != "DOCKER" {
		t.Fatalf("get: status=%d repo=%+v", status, repo)
	}

	var list struct {
		Repositories []Repository `json:"repositories"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/repositories", nil, &list)
	if status != 200 || len(list.Repositories) != 1 {
		t.Fatalf("list: status=%d repos=%+v", status, list.Repositories)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/locations/us-central1/repositories/my-repo", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

// TestCreateRequiresFormat ensures the required-field validation still works.
func TestCreateRequiresFormat(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/locations/us-central1/repositories?repositoryId=bad-repo",
		map[string]string{}, nil)
	if status != 400 {
		t.Fatalf("create without format: want 400, got %d", status)
	}
}

// TestDuplicateCreateConflict asserts that creating a repository whose
// client-specified repositoryId already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/locations/us-central1/repositories?repositoryId=dup-repo",
		map[string]string{"format": "DOCKER"}, nil)

	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/locations/us-central1/repositories?repositoryId=dup-repo",
		map[string]string{"format": "DOCKER"}, nil)
	if status != 409 {
		t.Fatalf("duplicate repository: want 409, got %d", status)
	}
}
