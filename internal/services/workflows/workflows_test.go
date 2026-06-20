package workflows

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

func validWorkflowBody() map[string]any {
	return map[string]any{
		"sourceContents": "main:\n  steps:\n  - final:\n      return: done",
	}
}

// TestWorkflowLifecycle covers create -> get -> list -> patch (revision
// bump) -> delete.
func TestWorkflowLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=my-wf",
		validWorkflowBody(), &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Workflow
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/my-wf", nil, &got)
	if status != 200 || got.State != "ACTIVE" || got.RevisionID == "" {
		t.Fatalf("get: status=%d wf=%+v", status, got)
	}

	var list struct {
		Workflows []Workflow `json:"workflows"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows", nil, &list)
	if status != 200 || len(list.Workflows) != 1 {
		t.Fatalf("list: status=%d workflows=%+v", status, list.Workflows)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/my-wf",
		map[string]any{"sourceContents": "main:\n  steps:\n  - final:\n      return: updated"}, &patchOp)
	if status != 200 || !patchOp.Done {
		t.Fatalf("patch: status=%d op=%+v", status, patchOp)
	}
	var patched Workflow
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/my-wf", nil, &patched)
	if patched.RevisionID == got.RevisionID {
		t.Fatalf("expected revisionId to bump after source update, still %q", patched.RevisionID)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/my-wf", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

// TestExecutionLifecycle covers create -> get -> list for a workflow
// execution, asserting it resolves synchronously to SUCCEEDED.
func TestExecutionLifecycle(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=my-wf",
		validWorkflowBody(), nil)

	var exec Execution
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/my-wf/executions",
		map[string]any{"argument": `{"foo":"bar"}`}, &exec)
	if status != 200 || exec.State != "SUCCEEDED" || exec.Result != `{"foo":"bar"}` {
		t.Fatalf("create execution: status=%d exec=%+v", status, exec)
	}

	execID := exec.Name[len(exec.Name)-len("exec-1"):]
	var got Execution
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/my-wf/executions/"+execID, nil, &got)
	if status != 200 || got.State != "SUCCEEDED" {
		t.Fatalf("get execution: status=%d exec=%+v", status, got)
	}

	var list struct {
		Executions []Execution `json:"executions"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/my-wf/executions", nil, &list)
	if status != 200 || len(list.Executions) != 1 {
		t.Fatalf("list executions: status=%d executions=%+v", status, list.Executions)
	}
}

func TestCreateWorkflowRequiresSourceContents(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=bad",
		map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without sourceContents: want 400, got %d", status)
	}
}

func TestCreateExecutionRequiresExistingWorkflow(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/missing/executions",
		map[string]any{}, nil)
	if status != 404 {
		t.Fatalf("create execution for missing workflow: want 404, got %d", status)
	}
}

// TestDuplicateCreateConflict asserts that creating a workflow whose
// workflowId already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=my-wf",
		validWorkflowBody(), nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=my-wf",
		validWorkflowBody(), nil)
	if status != 409 {
		t.Fatalf("duplicate workflow: want 409, got %d", status)
	}
}
