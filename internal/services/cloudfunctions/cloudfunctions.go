// Package cloudfunctions emula un subconjunto de la API de Cloud
// Functions Gen2 (cloudfunctions.googleapis.com/v2): functions. Gen2 se
// implementa en GCP real sobre Cloud Run; aquí el emulador simplemente
// sintetiza una URL de invocación con el mismo formato, sin desplegar
// nada de verdad. Las operaciones de mutación se devuelven como
// "google.longrunning.Operation" ya resueltas (done=true).
//
// Phase 14 adds an opt-in (internal/realbackend.WantsReal, checked
// against the function's labels) for real Docker execution, mirroring
// Cloud Run Services' tryStartReal — but Gen2's real API has no field at
// all for specifying a container image (a real Gen2 deploy builds one
// from source via Cloud Build, which this emulator doesn't implement), so
// real execution here only ever applies when the caller also sets a new,
// clearly-documented emulator-only field, RealExecution.Image, naming a
// pre-built image to run directly instead.
package cloudfunctions

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/cesar/gcp-emulator/internal/realbackend"
	"github.com/cesar/gcp-emulator/internal/realbackend/dockerrun"
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

	// RealExecution is an emulator-only extension, accepted on
	// create/update — not part of the real Cloud Functions API, which has
	// no way to specify a container image directly. Set Image to opt a
	// function into Phase 14's real Docker execution (also requires
	// internal/realbackend.WantsReal via Labels); the emulator then runs
	// that image directly instead of attempting (and failing) to build
	// the function's actual source.
	RealExecution *RealExecution `json:"realExecution,omitempty"`

	// RealEndpoint is an emulator-only extension — not part of the real
	// Cloud Functions API. Populated only when RealExecution.Image was
	// set, the opt-in was requested, and a real container was actually
	// admitted; points at the real, locally reachable URL fronting it.
	// ServiceConfig.URI above stays exactly as cosmetic as it always was.
	RealEndpoint *RealEndpoint `json:"realEndpoint,omitempty"`
}

// RealExecution is documented on Function.RealExecution above.
type RealExecution struct {
	Image string `json:"image"`
}

// RealEndpoint is documented on Function.RealEndpoint above. Duplicated
// (rather than imported) from internal/services/cloudrun's identical
// shape, per this project's "duplicate small helpers, avoid cross-package
// coupling" convention.
type RealEndpoint struct {
	Backend string `json:"backend"`
	URL     string `json:"url"`
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

	// gov/containers back Phase 14's real-backend opt-in, mirroring
	// Cloud Run's identical fields (internal/services/cloudrun.Svc): gov
	// is the shared realbackend.Governor (nil in tests that don't
	// exercise real execution, in which case opt-in requests silently
	// stay shape-only). containers maps a governor ID to the live
	// *dockerrun.Backend, kept in sync via SetOnEvict.
	mu         sync.Mutex
	gov        *realbackend.Governor
	containers map[string]*dockerrun.Backend
}

// New creates a Cloud Functions service. gov may be nil (e.g. in tests
// that don't exercise Phase 14's real-execution opt-in); a nil Governor
// simply means every function stays shape-only regardless of opt-in
// headers, the same zero-cost-by-default behavior as before Phase 14.
func New(db *storage.DB, gov *realbackend.Governor) *Svc {
	s := &Svc{db: db, gov: gov, containers: map[string]*dockerrun.Backend{}}
	if gov != nil {
		gov.SetOnEvict(s.forgetReal)
	}
	return s
}

func (s *Svc) forgetReal(id string) {
	s.mu.Lock()
	delete(s.containers, id)
	s.mu.Unlock()
}

func functionBackendID(name string) string { return "cloudfunctions:" + name }

// tryStartReal attempts to back fn with a real Docker container running
// fn.RealExecution.Image, when the caller opted in
// (realbackend.WantsReal, checked against fn.Labels), RealExecution.Image
// is actually set (Gen2 has no real image field of its own — see the
// package doc comment), and Docker is reachable. On any failure this logs
// and leaves fn exactly as shape-only as it would have been before
// Phase 14.
func (s *Svc) tryStartReal(r *http.Request, fn *Function) {
	if s.gov == nil {
		return
	}
	if !realbackend.WantsReal(r, fn.Labels) {
		return
	}
	if fn.RealExecution == nil || fn.RealExecution.Image == "" {
		log.Printf("cloudfunctions: %s pidió backend real pero no se especificó realExecution.image (Gen2 no expone un campo de imagen en la API real; es una extensión propia del emulador), sigue shape-only", fn.Name)
		return
	}
	avail := realbackend.DetectDocker(r.Context())
	if !avail.Available {
		log.Printf("cloudfunctions: %s pidió backend real pero Docker no está disponible (%s), sigue shape-only", fn.Name, avail.Detail)
		return
	}
	backend, err := dockerrun.Start(fn.RealExecution.Image, fn.ServiceConfig.EnvironmentVariables, 8080, 0)
	if err != nil {
		log.Printf("cloudfunctions: %s pidió backend real pero no se pudo iniciar el contenedor, sigue shape-only: %v", fn.Name, err)
		return
	}
	id := functionBackendID(fn.Name)
	admitted, evicted := s.gov.Admit(id, backend)
	if !admitted {
		log.Printf("cloudfunctions: %s pidió backend real pero el Governor lo rechazó (budget), sigue shape-only", fn.Name)
		_ = backend.Stop()
		return
	}
	for _, evID := range evicted {
		log.Printf("cloudfunctions: backend real %q desalojado (LRU) para liberar espacio para %q", evID, id)
	}
	s.mu.Lock()
	s.containers[id] = backend
	s.mu.Unlock()
	fn.RealEndpoint = &RealEndpoint{Backend: backend.Kind(), URL: backend.URL()}
}

// stopReal releases (and stops) any real container backing the named
// function. Safe to call even when none is registered.
func (s *Svc) stopReal(name string) {
	if s.gov == nil {
		return
	}
	s.gov.Release(functionBackendID(name))
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
		RealExecution *RealExecution    `json:"realExecution"`
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
		RealExecution: body.RealExecution,
	}
	s.tryStartReal(r, &fn)
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
		RealExecution *RealExecution    `json:"realExecution"`
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
	if body.RealExecution != nil {
		// A new image means any currently-running real container is
		// stale — stop it before possibly starting a fresh one below.
		// stopReal is a safe no-op if no real backend is registered yet.
		s.stopReal(existing.Name)
		existing.RealEndpoint = nil
		existing.RealExecution = body.RealExecution
		s.tryStartReal(r, &existing)
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
	s.stopReal(name)
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
