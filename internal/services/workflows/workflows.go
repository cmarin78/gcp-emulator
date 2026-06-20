// Package workflows emulates a subset of the Workflows API
// (workflows.googleapis.com/v1 for definitions, plus the nested executions
// resource that the real API actually serves from a separate
// workflowexecutions.googleapis.com host but under the same logical path —
// modeled here in one package since they're a single conceptual feature).
// Real workflow executions run a YAML/JSON-defined state machine; this
// emulator stores the source text opaquely and resolves every execution to
// SUCCEEDED synchronously, no real step interpretation, matching the
// "shape-compatible, not behavior-complete" approach used elsewhere.
package workflows

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketWorkflows  = "workflows.workflows"
	bucketExecutions = "workflows.executions"
)

// Workflow mirrors the relevant subset of workflows#Workflow.
type Workflow struct {
	Name               string            `json:"name"`
	Description        string            `json:"description,omitempty"`
	State              string            `json:"state,omitempty"`
	RevisionID         string            `json:"revisionId,omitempty"`
	SourceContents     string            `json:"sourceContents,omitempty"`
	ServiceAccount     string            `json:"serviceAccount,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	CreateTime         string            `json:"createTime,omitempty"`
	UpdateTime         string            `json:"updateTime,omitempty"`
	RevisionCreateTime string            `json:"revisionCreateTime,omitempty"`
}

// Execution mirrors the relevant subset of executions#Execution.
type Execution struct {
	Name               string `json:"name"`
	StartTime          string `json:"startTime,omitempty"`
	EndTime            string `json:"endTime,omitempty"`
	State              string `json:"state,omitempty"`
	Argument           string `json:"argument,omitempty"`
	Result             string `json:"result,omitempty"`
	WorkflowRevisionID string `json:"workflowRevisionId,omitempty"`
}

// Operation mirrors google.longrunning.Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/workflows", s.createWorkflow)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/workflows", s.listWorkflows)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/workflows/{workflow}", s.getWorkflow)
	mux.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/workflows/{workflow}", s.patchWorkflow)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/workflows/{workflow}", s.deleteWorkflow)

	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/workflows/{workflow}/executions", s.createExecution)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/workflows/{workflow}/executions", s.listExecutions)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/workflows/{workflow}/executions/{execution}", s.getExecution)
}

func workflowKey(project, location, workflow string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, workflow)
}

func workflowName(project, location, workflow string) string {
	return fmt.Sprintf("projects/%s/locations/%s/workflows/%s", project, location, workflow)
}

func executionKey(project, location, workflow, execution string) string {
	return fmt.Sprintf("%s/%s/%s/%s", project, location, workflow, execution)
}

func executionName(project, location, workflow, execution string) string {
	return fmt.Sprintf("projects/%s/locations/%s/workflows/%s/executions/%s", project, location, workflow, execution)
}

func (s *Service) nextID() int64 {
	s.seq++
	return s.seq
}

func (s *Service) writeOperation(w http.ResponseWriter, project, location, verb string, wf Workflow) {
	meta, _ := json.Marshal(map[string]string{"target": wf.Name, "verb": verb})
	resp, _ := json.Marshal(wf)
	op := Operation{
		Name:     fmt.Sprintf("projects/%s/locations/%s/operations/op-%d", project, location, s.nextID()),
		Done:     true,
		Metadata: meta,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createWorkflow(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	workflowID := r.URL.Query().Get("workflowId")
	if workflowID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "workflowId is required")
		return
	}
	var body struct {
		Description    string            `json:"description"`
		SourceContents string            `json:"sourceContents"`
		ServiceAccount string            `json:"serviceAccount"`
		Labels         map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.SourceContents == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "sourceContents is required")
		return
	}
	var existing Workflow
	found, err := s.db.Get(bucketWorkflows, workflowKey(project, location, workflowID), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "workflow already exists: "+workflowID)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	wf := Workflow{
		Name:               workflowName(project, location, workflowID),
		Description:        body.Description,
		State:              "ACTIVE",
		RevisionID:         "000001-aaa",
		SourceContents:     body.SourceContents,
		ServiceAccount:     body.ServiceAccount,
		Labels:             body.Labels,
		CreateTime:         now,
		UpdateTime:         now,
		RevisionCreateTime: now,
	}
	if err := s.db.Put(bucketWorkflows, workflowKey(project, location, workflowID), wf); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "create", wf)
}

func (s *Service) listWorkflows(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)
	items := []Workflow{}
	_ = s.db.List(bucketWorkflows, prefix, func(key string, raw []byte) error {
		var wf Workflow
		if err := json.Unmarshal(raw, &wf); err != nil {
			return err
		}
		items = append(items, wf)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"workflows": items})
}

func (s *Service) getWorkflow(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	workflow := r.PathValue("workflow")
	var wf Workflow
	found, err := s.db.Get(bucketWorkflows, workflowKey(project, location, workflow), &wf)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "workflow not found")
		return
	}
	server.WriteJSON(w, 200, wf)
}

func (s *Service) patchWorkflow(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	workflow := r.PathValue("workflow")
	var existing Workflow
	found, err := s.db.Get(bucketWorkflows, workflowKey(project, location, workflow), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "workflow not found")
		return
	}
	var body struct {
		Description    string            `json:"description"`
		SourceContents string            `json:"sourceContents"`
		Labels         map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Description != "" {
		existing.Description = body.Description
	}
	if body.SourceContents != "" {
		existing.SourceContents = body.SourceContents
		existing.RevisionID = bumpRevision(existing.RevisionID)
		existing.RevisionCreateTime = time.Now().UTC().Format(time.RFC3339)
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.Put(bucketWorkflows, workflowKey(project, location, workflow), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "update", existing)
}

func (s *Service) deleteWorkflow(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	workflow := r.PathValue("workflow")
	var existing Workflow
	_, _ = s.db.Get(bucketWorkflows, workflowKey(project, location, workflow), &existing)
	if err := s.db.Delete(bucketWorkflows, workflowKey(project, location, workflow)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if existing.Name == "" {
		existing.Name = workflowName(project, location, workflow)
	}
	s.writeOperation(w, project, location, "delete", existing)
}

// createExecution starts a new execution of an existing workflow. No real
// step interpretation happens: the execution resolves to SUCCEEDED
// immediately, with result echoing the input argument (a reasonable
// shape-compatible default for callers that just check execution.state).
func (s *Service) createExecution(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	workflow := r.PathValue("workflow")
	var wf Workflow
	found, err := s.db.Get(bucketWorkflows, workflowKey(project, location, workflow), &wf)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "workflow not found")
		return
	}
	var body struct {
		Argument string `json:"argument"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	executionID := fmt.Sprintf("exec-%d", s.nextID())
	now := time.Now().UTC().Format(time.RFC3339)
	exec := Execution{
		Name:               executionName(project, location, workflow, executionID),
		StartTime:          now,
		EndTime:            now,
		State:              "SUCCEEDED",
		Argument:           body.Argument,
		Result:             body.Argument,
		WorkflowRevisionID: wf.RevisionID,
	}
	if err := s.db.Put(bucketExecutions, executionKey(project, location, workflow, executionID), exec); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, exec)
}

func (s *Service) listExecutions(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	workflow := r.PathValue("workflow")
	prefix := fmt.Sprintf("%s/%s/%s/", project, location, workflow)
	items := []Execution{}
	_ = s.db.List(bucketExecutions, prefix, func(key string, raw []byte) error {
		var exec Execution
		if err := json.Unmarshal(raw, &exec); err != nil {
			return err
		}
		items = append(items, exec)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"executions": items})
}

func (s *Service) getExecution(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	workflow := r.PathValue("workflow")
	execution := r.PathValue("execution")
	var exec Execution
	found, err := s.db.Get(bucketExecutions, executionKey(project, location, workflow, execution), &exec)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "execution not found")
		return
	}
	server.WriteJSON(w, 200, exec)
}

func bumpRevision(rev string) string {
	if rev == "" {
		return "000001-aaa"
	}
	var n int
	fmt.Sscanf(rev, "%06d-", &n)
	return fmt.Sprintf("%06d-aaa", n+1)
}
