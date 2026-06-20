package cloudrun

import (
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func validJobBody() map[string]any {
	return map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"containers": []map[string]string{{"image": "gcr.io/proj1/my-job-image:latest"}},
			},
		},
	}
}

// TestJobLifecycle covers create -> get -> list -> update (generation bump)
// -> run (manual ":run" verb, execution count bump) -> delete.
func TestJobLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/jobs?jobId=my-job",
		validJobBody(), &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var job Job
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/jobs/my-job", nil, &job)
	if status != 200 || job.Generation != "1" || len(job.Template.Template.Containers) != 1 {
		t.Fatalf("get: status=%d job=%+v", status, job)
	}

	var list struct {
		Jobs []Job `json:"jobs"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/jobs", nil, &list)
	if status != 200 || len(list.Jobs) != 1 {
		t.Fatalf("list: status=%d jobs=%+v", status, list.Jobs)
	}

	var updateOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v2/projects/proj1/locations/us-central1/jobs/my-job",
		validJobBody(), &updateOp)
	if status != 200 || !updateOp.Done {
		t.Fatalf("update: status=%d op=%+v", status, updateOp)
	}
	var updated Job
	testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/jobs/my-job", nil, &updated)
	if updated.Generation != "2" {
		t.Fatalf("expected generation bump to 2, got %q", updated.Generation)
	}

	var runOp Operation
	status = testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/jobs/my-job:run", nil, &runOp)
	if status != 200 || !runOp.Done {
		t.Fatalf("run: status=%d op=%+v", status, runOp)
	}
	var ran Job
	testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/jobs/my-job", nil, &ran)
	if ran.ExecutionCount != 1 || ran.LatestCreatedExecution == "" {
		t.Fatalf("expected executionCount=1 after run, got %+v", ran)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v2/projects/proj1/locations/us-central1/jobs/my-job", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

func TestCreateJobRequiresContainerImage(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/jobs?jobId=bad-job",
		map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without container image: want 400, got %d", status)
	}
}

func TestCreateJobRequiresJobID(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/jobs",
		validJobBody(), nil)
	if status != 400 {
		t.Fatalf("create without jobId: want 400, got %d", status)
	}
}

// TestDuplicateCreateJobConflict asserts that creating a job whose jobId
// already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateJobConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/jobs?jobId=my-job",
		validJobBody(), nil)

	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/jobs?jobId=my-job",
		validJobBody(), nil)
	if status != 409 {
		t.Fatalf("duplicate job: want 409, got %d", status)
	}
}

func TestJobNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/jobs/missing", nil, nil)
	if status != 404 {
		t.Fatalf("get missing job: want 404, got %d", status)
	}
}
