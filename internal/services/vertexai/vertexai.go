// Package vertexai emulates a subset of the Vertex AI API
// (aiplatform.googleapis.com/v1): Model Registry and Endpoints.
//
// Scope:
//   - Models: upload (POST .../models:upload), get, list, patch, delete.
//   - Endpoints: create, get, list, patch, delete, deployModel, undeployModel.
//
// All mutations return synchronous Operations (done=true), following the
// "shape-compatible, not behavior-complete" approach used throughout this
// project. No real inference or model execution happens.
//
// Routes are mounted under /aiplatform/v1/... so Terraform's
// vertex_ai_custom_endpoint should point at <emulator>/aiplatform/.
package vertexai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketModels    = "vertexai.models"
	bucketEndpoints = "vertexai.endpoints"
)

// ── Resource shapes ────────────────────────────────────────────────────────────

// Model mirrors google.cloud.aiplatform.v1.Model.
type Model struct {
	Name              string            `json:"name,omitempty"`
	DisplayName       string            `json:"displayName,omitempty"`
	Description       string            `json:"description,omitempty"`
	MetadataSchemaUri string            `json:"metadataSchemaUri,omitempty"`
	Metadata          json.RawMessage   `json:"metadata,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	CreateTime        string            `json:"createTime,omitempty"`
	UpdateTime        string            `json:"updateTime,omitempty"`
	VersionID         string            `json:"versionId,omitempty"`
	Etag              string            `json:"etag,omitempty"`
}

// Endpoint mirrors google.cloud.aiplatform.v1.Endpoint.
type Endpoint struct {
	Name           string            `json:"name,omitempty"`
	DisplayName    string            `json:"displayName,omitempty"`
	Description    string            `json:"description,omitempty"`
	DeployedModels []DeployedModel   `json:"deployedModels,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	CreateTime     string            `json:"createTime,omitempty"`
	UpdateTime     string            `json:"updateTime,omitempty"`
	Etag           string            `json:"etag,omitempty"`
}

// DeployedModel is a model that has been deployed to an endpoint.
type DeployedModel struct {
	ID          string `json:"id,omitempty"`
	Model       string `json:"model,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	CreateTime  string `json:"createTime,omitempty"`
}

// Operation mirrors google.longrunning.Operation — always done=true.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Response json.RawMessage `json:"response,omitempty"`
}

// Service holds the BoltDB handle and a monotonic ID counter.
type Service struct {
	db  *storage.DB
	seq int64
}

// New constructs a Vertex AI emulator service.
func New(db *storage.DB) *Service {
	return &Service{db: db}
}

// Register mounts the Vertex AI routes onto mux.
func (s *Service) Register(mux *http.ServeMux) {
	base := "/aiplatform/v1/projects/{project}/locations/{location}"

	// Models.
	// POST .../models:upload — the standard way to add a model.
	mux.HandleFunc("POST "+base+"/models:upload", s.uploadModel)
	mux.HandleFunc("GET "+base+"/models", s.listModels)
	mux.HandleFunc("GET "+base+"/models/{model}", s.getModel)
	mux.HandleFunc("PATCH "+base+"/models/{model}", s.patchModel)
	mux.HandleFunc("DELETE "+base+"/models/{model}", s.deleteModel)

	// Endpoints.
	mux.HandleFunc("POST "+base+"/endpoints", s.createEndpoint)
	mux.HandleFunc("GET "+base+"/endpoints", s.listEndpoints)
	mux.HandleFunc("GET "+base+"/endpoints/{endpoint}", s.getEndpoint)
	mux.HandleFunc("PATCH "+base+"/endpoints/{endpoint}", s.patchEndpoint)
	mux.HandleFunc("DELETE "+base+"/endpoints/{endpoint}", s.deleteEndpoint)
	// Verb-suffix actions: deployModel, undeployModel.
	// "{endpointAction}" captures e.g. "abc123:deployModel".
	mux.HandleFunc("POST "+base+"/endpoints/{endpointAction}", s.endpointAction)

	// Operations — clients poll after create/delete; we always return done=true.
	mux.HandleFunc("GET "+base+"/operations/{operation}", s.getOperation)
}

// ── Key helpers ───────────────────────────────────────────────────────────────

func modelKey(project, location, id string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, id)
}

func endpointKey(project, location, id string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, id)
}

func (s *Service) nextID() string {
	return fmt.Sprintf("%d", atomic.AddInt64(&s.seq, 1))
}

func operationName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/operations/op-%s", project, location, id)
}

func nowStr() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func etag() string {
	return fmt.Sprintf("etag-%d", time.Now().UnixNano())
}

// ── Model handlers ────────────────────────────────────────────────────────────

// uploadModel handles POST .../models:upload.
// The real API wraps the model in {"model": {...}} — we accept that shape
// and also a bare model body for convenience.
func (s *Service) uploadModel(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")

	// The request body is UploadModelRequest: {"model": {...}}.
	var body struct {
		Model Model `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}

	id := s.nextID()
	now := nowStr()
	m := body.Model
	m.Name = fmt.Sprintf("projects/%s/locations/%s/models/%s", project, location, id)
	m.VersionID = "1"
	m.CreateTime = now
	m.UpdateTime = now
	m.Etag = etag()

	if err := s.db.Put(bucketModels, modelKey(project, location, id), m); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	respBytes, _ := json.Marshal(m)
	op := Operation{
		Name:     operationName(project, location, id),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) listModels(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := project + "/" + location + "/"
	models := []Model{}
	_ = s.db.List(bucketModels, prefix, func(_ string, raw []byte) error {
		var m Model
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		models = append(models, m)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"models": models})
}

func (s *Service) getModel(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("model")
	var m Model
	found, err := s.db.Get(bucketModels, modelKey(project, location, id), &m)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "model not found: "+id)
		return
	}
	server.WriteJSON(w, 200, m)
}

func (s *Service) patchModel(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("model")
	var existing Model
	found, err := s.db.Get(bucketModels, modelKey(project, location, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "model not found: "+id)
		return
	}

	var patch struct {
		DisplayName string            `json:"displayName"`
		Description string            `json:"description"`
		Labels      map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if patch.DisplayName != "" {
		existing.DisplayName = patch.DisplayName
	}
	if patch.Description != "" {
		existing.Description = patch.Description
	}
	if patch.Labels != nil {
		existing.Labels = patch.Labels
	}
	existing.UpdateTime = nowStr()
	existing.Etag = etag()

	if err := s.db.Put(bucketModels, modelKey(project, location, id), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Service) deleteModel(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("model")
	var m Model
	found, err := s.db.Get(bucketModels, modelKey(project, location, id), &m)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "model not found: "+id)
		return
	}
	if err := s.db.Delete(bucketModels, modelKey(project, location, id)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := Operation{
		Name: operationName(project, location, id),
		Done: true,
	}
	server.WriteJSON(w, 200, op)
}

// ── Endpoint handlers ─────────────────────────────────────────────────────────

func (s *Service) createEndpoint(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")

	var body Endpoint
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if body.DisplayName == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "displayName is required")
		return
	}

	id := s.nextID()
	now := nowStr()
	ep := Endpoint{
		Name:        fmt.Sprintf("projects/%s/locations/%s/endpoints/%s", project, location, id),
		DisplayName: body.DisplayName,
		Description: body.Description,
		Labels:      body.Labels,
		CreateTime:  now,
		UpdateTime:  now,
		Etag:        etag(),
	}
	if err := s.db.Put(bucketEndpoints, endpointKey(project, location, id), ep); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	respBytes, _ := json.Marshal(ep)
	op := Operation{
		Name:     operationName(project, location, id),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) listEndpoints(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := project + "/" + location + "/"
	eps := []Endpoint{}
	_ = s.db.List(bucketEndpoints, prefix, func(_ string, raw []byte) error {
		var ep Endpoint
		if err := json.Unmarshal(raw, &ep); err != nil {
			return err
		}
		eps = append(eps, ep)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"endpoints": eps})
}

func (s *Service) getEndpoint(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("endpoint")
	var ep Endpoint
	found, err := s.db.Get(bucketEndpoints, endpointKey(project, location, id), &ep)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "endpoint not found: "+id)
		return
	}
	server.WriteJSON(w, 200, ep)
}

func (s *Service) patchEndpoint(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("endpoint")
	var existing Endpoint
	found, err := s.db.Get(bucketEndpoints, endpointKey(project, location, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "endpoint not found: "+id)
		return
	}

	var patch struct {
		DisplayName string            `json:"displayName"`
		Description string            `json:"description"`
		Labels      map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if patch.DisplayName != "" {
		existing.DisplayName = patch.DisplayName
	}
	if patch.Description != "" {
		existing.Description = patch.Description
	}
	if patch.Labels != nil {
		existing.Labels = patch.Labels
	}
	existing.UpdateTime = nowStr()
	existing.Etag = etag()

	if err := s.db.Put(bucketEndpoints, endpointKey(project, location, id), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Service) deleteEndpoint(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("endpoint")
	var ep Endpoint
	found, err := s.db.Get(bucketEndpoints, endpointKey(project, location, id), &ep)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "endpoint not found: "+id)
		return
	}
	if err := s.db.Delete(bucketEndpoints, endpointKey(project, location, id)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := Operation{
		Name: operationName(project, location, id),
		Done: true,
	}
	server.WriteJSON(w, 200, op)
}

// endpointAction handles POST .../endpoints/{endpointAction} where the path
// segment is "{endpointID}:{action}" (e.g. "42:deployModel").
func (s *Service) endpointAction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	raw := r.PathValue("endpointAction")
	id, action, ok := strings.Cut(raw, ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "route not found")
		return
	}

	switch action {
	case "deployModel":
		s.deployModel(w, r, project, location, id)
	case "undeployModel":
		s.undeployModel(w, r, project, location, id)
	default:
		server.WriteError(w, 400, "INVALID_ARGUMENT", "unsupported action: "+action)
	}
}

// deployModel attaches a model reference to an endpoint's deployedModels list.
func (s *Service) deployModel(w http.ResponseWriter, r *http.Request, project, location, id string) {
	var existing Endpoint
	found, err := s.db.Get(bucketEndpoints, endpointKey(project, location, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "endpoint not found: "+id)
		return
	}

	var body struct {
		DeployedModel DeployedModel `json:"deployedModel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if body.DeployedModel.Model == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "deployedModel.model is required")
		return
	}

	dm := body.DeployedModel
	dm.ID = s.nextID()
	dm.CreateTime = nowStr()
	existing.DeployedModels = append(existing.DeployedModels, dm)
	existing.UpdateTime = nowStr()
	existing.Etag = etag()

	if err := s.db.Put(bucketEndpoints, endpointKey(project, location, id), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	respBytes, _ := json.Marshal(map[string]any{"deployedModel": dm, "endpoint": existing.Name})
	op := Operation{
		Name:     operationName(project, location, id),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

// undeployModel removes a model from an endpoint's deployedModels list.
func (s *Service) undeployModel(w http.ResponseWriter, r *http.Request, project, location, id string) {
	var existing Endpoint
	found, err := s.db.Get(bucketEndpoints, endpointKey(project, location, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "endpoint not found: "+id)
		return
	}

	var body struct {
		DeployedModelID string `json:"deployedModelId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}

	var updated []DeployedModel
	for _, dm := range existing.DeployedModels {
		if dm.ID != body.DeployedModelID {
			updated = append(updated, dm)
		}
	}
	existing.DeployedModels = updated // nil when empty → omitted by omitempty
	existing.UpdateTime = nowStr()
	existing.Etag = etag()

	if err := s.db.Put(bucketEndpoints, endpointKey(project, location, id), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	op := Operation{
		Name: operationName(project, location, id),
		Done: true,
	}
	server.WriteJSON(w, 200, op)
}

// getOperation always returns done=true: all Vertex AI operations in this
// emulator are synchronous.
func (s *Service) getOperation(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	op := r.PathValue("operation")
	server.WriteJSON(w, 200, Operation{
		Name: operationName(project, location, op),
		Done: true,
	})
}
