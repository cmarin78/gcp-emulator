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
	"net/http"
	"time"

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
		existing.Template = body.Template
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
