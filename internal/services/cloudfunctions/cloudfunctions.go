// Package cloudfunctions emula un subconjunto de la API de Cloud
// Functions Gen2 (cloudfunctions.googleapis.com/v2): functions. Gen2 se
// implementa en GCP real sobre Cloud Run; aquí el emulador simplemente
// sintetiza una URL de invocación con el mismo formato, sin desplegar
// nada de verdad. Las operaciones de mutación se devuelven como
// "google.longrunning.Operation" ya resueltas (done=true).
package cloudfunctions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketFunctions = "cloudfunctions.functions"

// StorageSource replica google.cloud.functions.v2.StorageSource.
type StorageSource struct {
	Bucket string `json:"bucket,omitempty"`
	Object string `json:"object,omitempty"`
}

// Source replica google.cloud.functions.v2.Source.
type Source struct {
	StorageSource *StorageSource `json:"storageSource,omitempty"`
}

// BuildConfig replica el subconjunto relevante de
// google.cloud.functions.v2.BuildConfig.
type BuildConfig struct {
	Runtime              string            `json:"runtime,omitempty"`
	EntryPoint           string            `json:"entryPoint,omitempty"`
	Source               *Source           `json:"source,omitempty"`
	EnvironmentVariables map[string]string `json:"environmentVariables,omitempty"`
}

// ServiceConfig replica el subconjunto relevante de
// google.cloud.functions.v2.ServiceConfig.
type ServiceConfig struct {
	Service              string            `json:"service,omitempty"` // Cloud Run service que la respalda
	URI                  string            `json:"uri,omitempty"`
	TimeoutSeconds       int               `json:"timeoutSeconds,omitempty"`
	AvailableMemory      string            `json:"availableMemory,omitempty"`
	MaxInstanceCount     int               `json:"maxInstanceCount,omitempty"`
	MinInstanceCount     int               `json:"minInstanceCount,omitempty"`
	EnvironmentVariables map[string]string `json:"environmentVariables,omitempty"`
	ServiceAccountEmail  string            `json:"serviceAccountEmail,omitempty"`
	IngressSettings      string            `json:"ingressSettings,omitempty"`
}

// Function replica google.cloud.functions.v2.Function.
type Function struct {
	Name          string            `json:"name"` // projects/{p}/locations/{l}/functions/{f}
	Description   string            `json:"description,omitempty"`
	BuildConfig   BuildConfig       `json:"buildConfig"`
	ServiceConfig ServiceConfig     `json:"serviceConfig"`
	State         string            `json:"state"`
	Environment   string            `json:"environment"`
	Labels        map[string]string `json:"labels,omitempty"`
	CreateTime    string            `json:"createTime"`
	UpdateTime    string            `json:"updateTime"`
}

// Operation replica el shape genérico google.longrunning.Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Response json.RawMessage `json:"response,omitempty"`
}

type Svc struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Svc {
	return &Svc{db: db}
}

// Register monta las rutas de Cloud Functions, siguiendo los paths
// reales de cloudfunctions.googleapis.com/v2. No registra su propio
// endpoint de operaciones: ver el comentario en
// internal/services/cloudrun.Register sobre por qué se centraliza en
// internal/server.RegisterV2Operations.
func (s *Svc) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/functions", s.createFunction)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/functions", s.listFunctions)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/functions/{function}", s.getFunction)
	mux.HandleFunc("PATCH /v2/projects/{project}/locations/{location}/functions/{function}", s.updateFunction)
	mux.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/functions/{function}", s.deleteFunction)
}

func functionName(project, location, function string) string {
	return fmt.Sprintf("projects/%s/locations/%s/functions/%s", project, location, function)
}

func operationName(project, location, op string) string {
	return fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, op)
}

func fakeURI(function, location string) string {
	return fmt.Sprintf("https://%s-%s.a.run.app", function, location)
}

func (s *Svc) createFunction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	functionID := r.URL.Query().Get("functionId")
	if functionID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "functionId es requerido")
		return
	}
	var body struct {
		Description   string            `json:"description"`
		BuildConfig   BuildConfig       `json:"buildConfig"`
		ServiceConfig ServiceConfig     `json:"serviceConfig"`
		Labels        map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.BuildConfig.Runtime == "" || body.BuildConfig.EntryPoint == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "buildConfig.runtime y buildConfig.entryPoint son requeridos")
		return
	}
	name := functionName(project, location, functionID)
	var existingFn Function
	found, err := s.db.Get(bucketFunctions, name, &existingFn)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "función ya existe: "+name)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	svcConfig := body.ServiceConfig
	svcConfig.Service = fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, functionID)
	svcConfig.URI = fakeURI(functionID, location)
	fn := Function{
		Name:          name,
		Description:   body.Description,
		BuildConfig:   body.BuildConfig,
		ServiceConfig: svcConfig,
		State:         "ACTIVE",
		Environment:   "GEN_2",
		Labels:        body.Labels,
		CreateTime:    now,
		UpdateTime:    now,
	}
	if err := s.db.Put(bucketFunctions, fn.Name, fn); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, fn)
}

func (s *Svc) listFunctions(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/functions/", project, location)
	items := []Function{}
	_ = s.db.List(bucketFunctions, prefix, func(key string, raw []byte) error {
		var fn Function
		if err := json.Unmarshal(raw, &fn); err != nil {
			return err
		}
		items = append(items, fn)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"functions": items})
}

func (s *Svc) getFunction(w http.ResponseWriter, r *http.Request) {
	name := functionName(r.PathValue("project"), r.PathValue("location"), r.PathValue("function"))
	var fn Function
	found, err := s.db.Get(bucketFunctions, name, &fn)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "función no encontrada")
		return
	}
	server.WriteJSON(w, 200, fn)
}

func (s *Svc) updateFunction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	name := functionName(project, location, r.PathValue("function"))
	var existing Function
	found, err := s.db.Get(bucketFunctions, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "función no encontrada")
		return
	}
	var body struct {
		Description   string            `json:"description"`
		BuildConfig   *BuildConfig      `json:"buildConfig"`
		ServiceConfig *ServiceConfig    `json:"serviceConfig"`
		Labels        map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Description != "" {
		existing.Description = body.Description
	}
	if body.BuildConfig != nil {
		existing.BuildConfig = *body.BuildConfig
	}
	if body.ServiceConfig != nil {
		body.ServiceConfig.Service = existing.ServiceConfig.Service
		body.ServiceConfig.URI = existing.ServiceConfig.URI
		existing.ServiceConfig = *body.ServiceConfig
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.Put(bucketFunctions, existing.Name, existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, existing)
}

func (s *Svc) deleteFunction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	name := functionName(project, location, r.PathValue("function"))
	if err := s.db.Delete(bucketFunctions, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.seq++
	op := Operation{Name: operationName(project, location, fmt.Sprintf("op-%d", s.seq)), Done: true}
	server.WriteJSON(w, 200, op)
}

func (s *Svc) writeOperation(w http.ResponseWriter, project, location string, fn Function) {
	respBytes, _ := json.Marshal(fn)
	s.seq++
	op := Operation{
		Name:     operationName(project, location, fmt.Sprintf("op-%d", s.seq)),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}
