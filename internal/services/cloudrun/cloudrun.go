// Package cloudrun emula un subconjunto de la API de Cloud Run Admin
// (run.googleapis.com/v2): servicios y revisiones implícitas. Las
// operaciones de mutación (create/update/delete) se devuelven como
// recursos "google.longrunning.Operation" ya resueltos (done=true),
// igual que en Artifact Registry, porque el emulador no modela un
// despliegue real ni revisiones independientes.
package cloudrun

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

const bucketServices = "cloudrun.services"

// Container replica el subconjunto relevante de
// google.cloud.run.v2.Container.
type Container struct {
	Name  string          `json:"name,omitempty"`
	Image string          `json:"image"`
	Ports []ContainerPort `json:"ports,omitempty"`
	Env   []EnvVar        `json:"env,omitempty"`
}

type ContainerPort struct {
	Name          string `json:"name,omitempty"`
	ContainerPort int    `json:"containerPort,omitempty"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// RevisionTemplate replica google.cloud.run.v2.RevisionTemplate.
type RevisionTemplate struct {
	Revision                      string            `json:"revision,omitempty"`
	Labels                        map[string]string `json:"labels,omitempty"`
	Annotations                   map[string]string `json:"annotations,omitempty"`
	Containers                    []Container       `json:"containers"`
	ServiceAccount                string            `json:"serviceAccount,omitempty"`
	Timeout                       string            `json:"timeout,omitempty"`
	MaxInstanceRequestConcurrency int               `json:"maxInstanceRequestConcurrency,omitempty"`
}

// Condition replica el shape mínimo usado por terminalCondition/conditions.
type Condition struct {
	Type  string `json:"type"`
	State string `json:"state"`
}

// Service replica google.cloud.run.v2.Service.
type Service struct {
	Name                  string            `json:"name"` // projects/{p}/locations/{l}/services/{s}
	UID                   string            `json:"uid"`
	Generation            string            `json:"generation"`
	Labels                map[string]string `json:"labels,omitempty"`
	Annotations           map[string]string `json:"annotations,omitempty"`
	Ingress               string            `json:"ingress,omitempty"`
	LaunchStage           string            `json:"launchStage,omitempty"`
	Template              RevisionTemplate  `json:"template"`
	URI                   string            `json:"uri"`
	CreateTime            string            `json:"createTime"`
	UpdateTime            string            `json:"updateTime"`
	LatestReadyRevision   string            `json:"latestReadyRevision"`
	LatestCreatedRevision string            `json:"latestCreatedRevision"`
	ObservedGeneration    string            `json:"observedGeneration"`
	Reconciling           bool              `json:"reconciling"`
	TerminalCondition     Condition         `json:"terminalCondition"`

	// RealEndpoint is an emulator-only extension — not part of the real
	// Cloud Run API. When the service opted into real execution
	// (internal/realbackend.WantsReal, checked against Labels) and a real
	// Docker container running template.containers[0].image was actually
	// admitted (Phase 14), this points at the real, locally reachable URL
	// fronting that container. The existing URI field above stays exactly
	// as cosmetic as it always was (never backed by a real listener) for
	// backward compatibility; RealEndpoint is additive and omitted from
	// JSON for every shape-only service, which remains the default for
	// every existing caller (gcloud, Terraform, the pre-existing test
	// suite).
	RealEndpoint *RealEndpoint `json:"realEndpoint,omitempty"`
}

// RealEndpoint is documented on Service.RealEndpoint above.
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
	// Phase 13's cloudsql.Svc exactly: gov is the shared
	// realbackend.Governor (nil in tests that don't care about real
	// execution, in which case opt-in requests silently stay shape-only).
	// containers maps a governor ID to the live *dockerrun.Backend so
	// updateService/deleteService/jobAction know which container to stop;
	// it's kept in sync with the Governor via SetOnEvict so an evicted/
	// released backend is never used after it was stopped.
	mu         sync.Mutex
	gov        *realbackend.Governor
	containers map[string]*dockerrun.Backend
}

// New creates a Cloud Run service. gov may be nil (e.g. in tests that
// don't exercise Phase 14's real-execution opt-in); a nil Governor simply
// means every service/job stays shape-only regardless of opt-in headers,
// the same zero-cost-by-default behavior as before Phase 14.
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

func serviceBackendID(name string) string { return "cloudrun:service:" + name }

func (s *Svc) realBackendFor(name string) *dockerrun.Backend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.containers[serviceBackendID(name)]
}

// tryStartReal attempts to back svc with a real Docker container running
// its template's image, when the caller opted in (realbackend.WantsReal,
// checked against svc.Labels) and a Docker engine is actually reachable
// (realbackend.DetectDocker). On any failure — no Governor wired, Docker
// unavailable, a budget refusal, or the container failing to start/become
// reachable — this logs and leaves svc exactly as shape-only as it would
// have been before Phase 14. Same documented-fallback pattern as Cloud
// SQL's tryStartReal (Phase 13).
func (s *Svc) tryStartReal(r *http.Request, svc *Service) {
	if s.gov == nil {
		return
	}
	if !realbackend.WantsReal(r, svc.Labels) {
		return
	}
	if len(svc.Template.Containers) == 0 || svc.Template.Containers[0].Image == "" {
		return
	}
	avail := realbackend.DetectDocker(r.Context())
	if !avail.Available {
		log.Printf("cloudrun: %s pidió backend real pero Docker no está disponible (%s), sigue shape-only", svc.Name, avail.Detail)
		return
	}
	container := svc.Template.Containers[0]
	backend, err := dockerrun.Start(container.Image, envMapOf(container.Env), containerPortOf(container), 0)
	if err != nil {
		log.Printf("cloudrun: %s pidió backend real pero no se pudo iniciar el contenedor, sigue shape-only: %v", svc.Name, err)
		return
	}
	id := serviceBackendID(svc.Name)
	admitted, evicted := s.gov.Admit(id, backend)
	if !admitted {
		log.Printf("cloudrun: %s pidió backend real pero el Governor lo rechazó (budget), sigue shape-only", svc.Name)
		_ = backend.Stop()
		return
	}
	for _, evID := range evicted {
		log.Printf("cloudrun: backend real %q desalojado (LRU) para liberar espacio para %q", evID, id)
	}
	s.mu.Lock()
	s.containers[id] = backend
	s.mu.Unlock()
	svc.RealEndpoint = &RealEndpoint{Backend: backend.Kind(), URL: backend.URL()}
}

// stopReal releases (and stops) any real container backing the named
// service. Safe to call even when none is registered (Governor.Release is
// a no-op for an absent id).
func (s *Svc) stopReal(name string) {
	if s.gov == nil {
		return
	}
	s.gov.Release(serviceBackendID(name))
}

// containerPortOf returns the container port a real backend should
// publish: the template's explicit port if set, else 8080 — the real
// Cloud Run convention (the container is expected to listen on $PORT,
// default 8080).
func containerPortOf(c Container) int {
	if len(c.Ports) > 0 && c.Ports[0].ContainerPort > 0 {
		return c.Ports[0].ContainerPort
	}
	return 8080
}

func envMapOf(vars []EnvVar) map[string]string {
	m := map[string]string{}
	for _, v := range vars {
		m[v.Name] = v.Value
	}
	return m
}

// Register monta las rutas de Cloud Run, siguiendo los paths reales de
// run.googleapis.com/v2. El endpoint de operaciones
// (/v2/.../operations/{operation}) se registra una sola vez de forma
// centralizada (ver internal/server.RegisterV2Operations), porque Cloud
// Functions usa exactamente el mismo path real y registrarlo dos veces
// en el mismo mux causaría un panic de ruta duplicada.
func (s *Svc) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/services", s.createService)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/services", s.listServices)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/services/{service}", s.getService)
	mux.HandleFunc("PATCH /v2/projects/{project}/locations/{location}/services/{service}", s.updateService)
	mux.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/services/{service}", s.deleteService)

	// Fase 9: Cloud Run Jobs (recurso distinto de los servicios de arriba).
	s.registerJobs(mux)
}

func serviceName(project, location, service string) string {
	return fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, service)
}

func operationName(project, location, op string) string {
	return fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, op)
}

func fakeURI(service, location string) string {
	return fmt.Sprintf("https://%s-emulator-%s.a.run.app", service, location)
}

func (s *Svc) createService(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	serviceID := r.URL.Query().Get("serviceId")
	if serviceID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "serviceId es requerido")
		return
	}
	var body struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		Ingress     string            `json:"ingress"`
		LaunchStage string            `json:"launchStage"`
		Template    RevisionTemplate  `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if len(body.Template.Containers) == 0 || body.Template.Containers[0].Image == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "template.containers[0].image es requerido")
		return
	}
	name := serviceName(project, location, serviceID)
	var existingSvc Service
	found, err := s.db.Get(bucketServices, name, &existingSvc)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "servicio ya existe: "+name)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	svc := Service{
		Name:                  name,
		UID:                   fmt.Sprintf("uid-%d", time.Now().UnixNano()),
		Generation:            "1",
		Labels:                body.Labels,
		Annotations:           body.Annotations,
		Ingress:               orDefault(body.Ingress, "INGRESS_TRAFFIC_ALL"),
		LaunchStage:           orDefault(body.LaunchStage, "GA"),
		Template:              body.Template,
		URI:                   fakeURI(serviceID, location),
		CreateTime:            now,
		UpdateTime:            now,
		LatestReadyRevision:   name + "-00001-aaa",
		LatestCreatedRevision: name + "-00001-aaa",
		ObservedGeneration:    "1",
		Reconciling:           false,
		TerminalCondition:     Condition{Type: "Ready", State: "CONDITION_SUCCEEDED"},
	}
	s.tryStartReal(r, &svc)
	if err := s.db.Put(bucketServices, svc.Name, svc); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, svc)
}

func (s *Svc) listServices(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/services/", project, location)
	items := []Service{}
	_ = s.db.List(bucketServices, prefix, func(key string, raw []byte) error {
		var svc Service
		if err := json.Unmarshal(raw, &svc); err != nil {
			return err
		}
		items = append(items, svc)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"services": items})
}

func (s *Svc) getService(w http.ResponseWriter, r *http.Request) {
	name := serviceName(r.PathValue("project"), r.PathValue("location"), r.PathValue("service"))
	var svc Service
	found, err := s.db.Get(bucketServices, name, &svc)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "servicio no encontrado")
		return
	}
	server.WriteJSON(w, 200, svc)
}

func (s *Svc) updateService(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	name := serviceName(project, location, r.PathValue("service"))
	var existing Service
	found, err := s.db.Get(bucketServices, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "servicio no encontrado")
		return
	}
	var body struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		Ingress     string            `json:"ingress"`
		Template    RevisionTemplate  `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	if body.Annotations != nil {
		existing.Annotations = body.Annotations
	}
	if body.Ingress != "" {
		existing.Ingress = body.Ingress
	}
	if len(body.Template.Containers) > 0 {
		// A new template means any currently-running real container is
		// stale (it's running the old image) — stop it before possibly
		// starting a fresh one below. stopReal is a safe no-op if no real
		// backend is currently registered for this service.
		s.stopReal(existing.Name)
		existing.RealEndpoint = nil
		existing.Template = body.Template
		s.tryStartReal(r, &existing)
	}
	existing.Generation = bumpGeneration(existing.Generation)
	existing.ObservedGeneration = existing.Generation
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.Put(bucketServices, existing.Name, existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, existing)
}

func (s *Svc) deleteService(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	name := serviceName(project, location, r.PathValue("service"))
	s.stopReal(name)
	if err := s.db.Delete(bucketServices, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.seq++
	op := Operation{Name: operationName(project, location, fmt.Sprintf("op-%d", s.seq)), Done: true}
	server.WriteJSON(w, 200, op)
}

func (s *Svc) writeOperation(w http.ResponseWriter, project, location string, svc Service) {
	respBytes, _ := json.Marshal(svc)
	s.seq++
	op := Operation{
		Name:     operationName(project, location, fmt.Sprintf("op-%d", s.seq)),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

func bumpGeneration(g string) string {
	switch g {
	case "":
		return "1"
	default:
		var n int
		fmt.Sscanf(g, "%d", &n)
		return fmt.Sprintf("%d", n+1)
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
