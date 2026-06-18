package cloudtasks

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

// TestQueueLifecycle covers create -> get -> list -> pause/resume -> delete.
func TestQueueLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var q Queue
	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues", map[string]string{
		"name": "projects/proj1/locations/us-central1/queues/my-queue",
	}, &q)
	if status != 200 || q.State != "RUNNING" {
		t.Fatalf("create: status=%d q=%+v", status, q)
	}

	var got Queue
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/my-queue", nil, &got)
	if status != 200 || got.Name != q.Name {
		t.Fatalf("get: status=%d q=%+v", status, got)
	}

	var list struct {
		Queues []Queue `json:"queues"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/queues", nil, &list)
	if status != 200 || len(list.Queues) != 1 {
		t.Fatalf("list: status=%d queues=%+v", status, list.Queues)
	}

	var paused Queue
	status = testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/my-queue:pause", nil, &paused)
	if status != 200 || paused.State != "PAUSED" {
		t.Fatalf("pause: status=%d q=%+v", status, paused)
	}

	var resumed Queue
	status = testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/my-queue:resume", nil, &resumed)
	if status != 200 || resumed.State != "RUNNING" {
		t.Fatalf("resume: status=%d q=%+v", status, resumed)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/my-queue", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

// TestTaskLifecycle covers create -> get -> list -> delete under a queue,
// and specifically asserts the /queues/{queue}/tasks route doesn't collide
// with the /queues/{queueAction} verb-dispatch route registered on the same
// mux for the same base path.
func TestTaskLifecycle(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues", map[string]string{
		"name": "projects/proj1/locations/us-central1/queues/q1",
	}, nil)

	var task Task
	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/q1/tasks", map[string]any{
		"task": map[string]any{},
	}, &task)
	if status != 200 || task.Name == "" {
		t.Fatalf("create task: status=%d task=%+v", status, task)
	}

	taskID := task.Name[len(task.Name)-len("task-1"):]
	_ = taskID

	var list struct {
		Tasks []Task `json:"tasks"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/q1/tasks", nil, &list)
	if status != 200 || len(list.Tasks) != 1 {
		t.Fatalf("list tasks: status=%d tasks=%+v", status, list.Tasks)
	}

	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/q1/tasks/task-1", nil, nil)
	if status != 200 {
		t.Fatalf("get task: want 200, got %d", status)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/q1/tasks/task-1", nil, nil)
	if status != 200 {
		t.Fatalf("delete task: want 200, got %d", status)
	}
}

// TestDuplicateCreateConflicts asserts that creating a queue whose name
// already exists, or a task with an explicit client-specified name that
// already exists, returns 409 ALREADY_EXISTS instead of silently overwriting.
func TestDuplicateCreateConflicts(t *testing.T) {
	srv := newTestServer(t)

	queueBody := map[string]string{"name": "projects/proj1/locations/us-central1/queues/dup-queue"}
	testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues", queueBody, nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues", queueBody, nil)
	if status != 409 {
		t.Fatalf("duplicate queue: want 409, got %d", status)
	}

	taskBody := map[string]any{
		"task": map[string]any{
			"name": "projects/proj1/locations/us-central1/queues/dup-queue/tasks/dup-task",
		},
	}
	testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/dup-queue/tasks", taskBody, nil)
	status = testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/locations/us-central1/queues/dup-queue/tasks", taskBody, nil)
	if status != 409 {
		t.Fatalf("duplicate task: want 409, got %d", status)
	}
}
