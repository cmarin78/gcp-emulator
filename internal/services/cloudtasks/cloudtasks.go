// Package cloudtasks emula un subconjunto de la API de Cloud Tasks
// (cloudtasks.googleapis.com/v2): queues y tasks.
//
// Fase 11 (capa de comportamiento): createTask, si trae un httpRequest real,
// efectivamente lo despacha — un HTTP request de verdad al url configurado,
// respetando scheduleTime si está en el futuro, sin depender de Docker ni de
// ninguna dependencia nueva. appEngineHttpRequest sigue siendo solo shape
// (App Engine no está emulado).
package cloudtasks

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketQueues = "cloudtasks.queues"
	bucketTasks  = "cloudtasks.tasks"
)

// Queue replica el recurso "Queue".
type Queue struct {
	Name  string `json:"name"` // projects/{p}/locations/{l}/queues/{q}
	State string `json:"state"`
}

// Task replica el recurso "Task" (subset: appEngineHttpRequest se acepta
// como passthrough JSON sin entregarse a ningún destino real, ya que App
// Engine no está emulado; httpRequest sí se despacha de verdad).
type Task struct {
	Name          string          `json:"name"` // .../queues/{q}/tasks/{t}
	ScheduleTime  string          `json:"scheduleTime,omitempty"`
	CreateTime    string          `json:"createTime,omitempty"`
	DispatchCount int             `json:"dispatchCount"`
	HTTPRequest   json.RawMessage `json:"httpRequest,omitempty"`
	AppEngineHTTP json.RawMessage `json:"appEngineHttpRequest,omitempty"`
}

// httpRequestSpec mirrors the relevant subset of cloudtasks#HttpRequest
// needed to actually dispatch the task.
type httpRequestSpec struct {
	URL        string            `json:"url"`
	HTTPMethod string            `json:"httpMethod"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"` // base64, same wire format as the real API
}

type Service struct {
	db         *storage.DB
	seq        int64
	httpClient *http.Client
}

func New(db *storage.DB) *Service {
	return &Service{db: db, httpClient: &http.Client{Timeout: 10 * time.Second}}
}

// dispatchTask performs the real HTTP call for a task's httpRequest and
// records the attempt (dispatchCount/scheduleTime) back onto the stored
// task. Runs in its own goroutine, scheduled to respect task.ScheduleTime
// when it's in the future.
func (s *Service) dispatchTask(t Task) {
	if len(t.HTTPRequest) == 0 {
		return
	}
	var hr httpRequestSpec
	if err := json.Unmarshal(t.HTTPRequest, &hr); err != nil || hr.URL == "" {
		return
	}

	if t.ScheduleTime != "" {
		if when, err := time.Parse(time.RFC3339, t.ScheduleTime); err == nil {
			if d := time.Until(when); d > 0 {
				time.Sleep(d)
			}
		}
	}

	method := hr.HTTPMethod
	if method == "" {
		method = "POST"
	}
	var body []byte
	if hr.Body != "" {
		body, _ = base64.StdEncoding.DecodeString(hr.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, hr.URL, bytes.NewReader(body))
	if err == nil {
		for k, v := range hr.Headers {
			req.Header.Set(k, v)
		}
		if resp, err := s.httpClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}

	var current Task
	found, gerr := s.db.Get(bucketTasks, t.Name, &current)
	if gerr == nil && found {
		current.DispatchCount++
		_ = s.db.Put(bucketTasks, t.Name, current)
	}
}

// Register monta las rutas de Cloud Tasks, siguiendo los paths reales de
// cloudtasks.googleapis.com.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues", s.createQueue)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues", s.listQueues)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}", s.getQueue)
	mux.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/queues/{queue}", s.deleteQueue)
	// "{queue}:pause"/"{queue}:resume" via strings.Cut, mismo patrón que
	// Secret Manager/Cloud Scheduler.
	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queueAction}", s.queueAction)

	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks", s.createTask)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}/tasks", s.listTasks)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}", s.getTask)
	mux.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}", s.deleteTask)
}

func queueName(project, location, queue string) string {
	return fmt.Sprintf("projects/%s/locations/%s/queues/%s", project, location, queue)
}

func taskName(project, location, queue, task string) string {
	return fmt.Sprintf("projects/%s/locations/%s/queues/%s/tasks/%s", project, location, queue, task)
}

func (s *Service) createQueue(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	queueID := body.Name
	if idx := strings.LastIndex(queueID, "/"); idx >= 0 {
		queueID = queueID[idx+1:]
	}
	if queueID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name (con el queueId) es requerido")
		return
	}

	name := queueName(project, location, queueID)
	var existing Queue
	found, err := s.db.Get(bucketQueues, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "la queue ya existe")
		return
	}

	q := Queue{Name: name, State: "RUNNING"}
	if err := s.db.Put(bucketQueues, q.Name, q); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, q)
}

func (s *Service) listQueues(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/queues/", project, location)
	queues := []Queue{}
	_ = s.db.List(bucketQueues, prefix, func(key string, raw []byte) error {
		var q Queue
		if err := json.Unmarshal(raw, &q); err != nil {
			return err
		}
		queues = append(queues, q)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"queues": queues})
}

func (s *Service) getQueue(w http.ResponseWriter, r *http.Request) {
	name := queueName(r.PathValue("project"), r.PathValue("location"), r.PathValue("queue"))
	var q Queue
	found, err := s.db.Get(bucketQueues, name, &q)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "queue no encontrada")
		return
	}
	server.WriteJSON(w, 200, q)
}

func (s *Service) deleteQueue(w http.ResponseWriter, r *http.Request) {
	name := queueName(r.PathValue("project"), r.PathValue("location"), r.PathValue("queue"))
	if err := s.db.Delete(bucketQueues, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Service) queueAction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	queueID, action, ok := strings.Cut(r.PathValue("queueAction"), ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}

	name := queueName(project, location, queueID)
	var q Queue
	found, err := s.db.Get(bucketQueues, name, &q)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "queue no encontrada")
		return
	}

	switch action {
	case "pause":
		q.State = "PAUSED"
	case "resume":
		q.State = "RUNNING"
	default:
		server.WriteError(w, 404, "NOT_FOUND", "acción no soportada: "+action)
		return
	}

	if err := s.db.Put(bucketQueues, name, q); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, q)
}

func (s *Service) createTask(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	queueID := r.PathValue("queue")

	qName := queueName(project, location, queueID)
	var q Queue
	found, err := s.db.Get(bucketQueues, qName, &q)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "queue no encontrada")
		return
	}

	var body struct {
		Task struct {
			Name          string          `json:"name"`
			ScheduleTime  string          `json:"scheduleTime"`
			HTTPRequest   json.RawMessage `json:"httpRequest"`
			AppEngineHTTP json.RawMessage `json:"appEngineHttpRequest"`
		} `json:"task"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	taskID := body.Task.Name
	if idx := strings.LastIndex(taskID, "/"); idx >= 0 {
		taskID = taskID[idx+1:]
	}
	if taskID == "" {
		s.seq++
		taskID = fmt.Sprintf("task-%d", s.seq)
	} else {
		var existing Task
		found, err := s.db.Get(bucketTasks, taskName(project, location, queueID, taskID), &existing)
		if err != nil {
			server.WriteError(w, 500, "INTERNAL", err.Error())
			return
		}
		if found {
			server.WriteError(w, 409, "ALREADY_EXISTS", "la task ya existe")
			return
		}
	}

	t := Task{
		Name:          taskName(project, location, queueID, taskID),
		ScheduleTime:  body.Task.ScheduleTime,
		CreateTime:    time.Now().UTC().Format(time.RFC3339),
		HTTPRequest:   body.Task.HTTPRequest,
		AppEngineHTTP: body.Task.AppEngineHTTP,
	}
	if err := s.db.Put(bucketTasks, t.Name, t); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if q.State != "PAUSED" {
		go s.dispatchTask(t)
	}
	server.WriteJSON(w, 200, t)
}

func (s *Service) listTasks(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	queueID := r.PathValue("queue")
	prefix := fmt.Sprintf("projects/%s/locations/%s/queues/%s/tasks/", project, location, queueID)
	tasks := []Task{}
	_ = s.db.List(bucketTasks, prefix, func(key string, raw []byte) error {
		var t Task
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		tasks = append(tasks, t)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"tasks": tasks})
}

func (s *Service) getTask(w http.ResponseWriter, r *http.Request) {
	name := taskName(r.PathValue("project"), r.PathValue("location"), r.PathValue("queue"), r.PathValue("task"))
	var t Task
	found, err := s.db.Get(bucketTasks, name, &t)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "task no encontrada")
		return
	}
	server.WriteJSON(w, 200, t)
}

func (s *Service) deleteTask(w http.ResponseWriter, r *http.Request) {
	name := taskName(r.PathValue("project"), r.PathValue("location"), r.PathValue("queue"), r.PathValue("task"))
	if err := s.db.Delete(bucketTasks, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}
