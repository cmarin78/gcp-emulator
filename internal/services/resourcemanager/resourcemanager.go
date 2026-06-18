// Package resourcemanager emula un subconjunto de la API de Cloud Resource
// Manager (cloudresourcemanager.googleapis.com/v3): projects. El "project"
// ya funcionaba como un string opaco aceptado por el resto de los
// servicios; este paquete solo agrega el recurso real para soportar
// `google_project` de Terraform y `gcloud projects create/describe/delete`.
package resourcemanager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketProjects = "resourcemanager.projects"

// Project replica el recurso "Project" de la API v3.
type Project struct {
	Name        string `json:"name"` // projects/{projectId}
	ProjectID   string `json:"projectId"`
	Parent      string `json:"parent,omitempty"` // "organizations/{id}" o "folders/{id}"
	State       string `json:"state"`
	DisplayName string `json:"displayName,omitempty"`
	CreateTime  string `json:"createTime,omitempty"`
	UpdateTime  string `json:"updateTime,omitempty"`
	Etag        string `json:"etag,omitempty"`
}

// Operation replica el shape genérico google.longrunning.Operation, igual
// que en Artifact Registry/Cloud Run: el emulador resuelve todo de forma
// sincrónica y devuelve siempre done=true.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Response json.RawMessage `json:"response,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service {
	return &Service{db: db}
}

// Register monta las rutas de Resource Manager, siguiendo los paths reales
// de cloudresourcemanager.googleapis.com/v3.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v3/projects", s.createProject)
	mux.HandleFunc("GET /v3/projects", s.listProjects)
	mux.HandleFunc("GET /v3/projects/{project}", s.getProject)
	mux.HandleFunc("DELETE /v3/projects/{project}", s.deleteProject)
	mux.HandleFunc("GET /v3/operations/{operation}", s.getOperation)
}

func projectName(projectID string) string {
	return fmt.Sprintf("projects/%s", projectID)
}

func operationName(op string) string {
	return fmt.Sprintf("operations/%s", op)
}

func (s *Service) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID   string `json:"projectId"`
		Parent      string `json:"parent"`
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.ProjectID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "projectId es requerido")
		return
	}
	name := projectName(body.ProjectID)
	var existing Project
	found, err := s.db.Get(bucketProjects, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "el project ya existe")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	proj := Project{
		Name:        name,
		ProjectID:   body.ProjectID,
		Parent:      body.Parent,
		State:       "ACTIVE",
		DisplayName: body.DisplayName,
		CreateTime:  now,
		UpdateTime:  now,
		Etag:        fmt.Sprintf("etag-%d", time.Now().UnixNano()),
	}
	if err := s.db.Put(bucketProjects, proj.Name, proj); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	respBytes, _ := json.Marshal(proj)
	s.seq++
	op := Operation{
		Name:     operationName(fmt.Sprintf("op-%d", s.seq)),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) listProjects(w http.ResponseWriter, r *http.Request) {
	projects := []Project{}
	_ = s.db.List(bucketProjects, "projects/", func(key string, raw []byte) error {
		var proj Project
		if err := json.Unmarshal(raw, &proj); err != nil {
			return err
		}
		projects = append(projects, proj)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"projects": projects})
}

func (s *Service) getProject(w http.ResponseWriter, r *http.Request) {
	name := projectName(r.PathValue("project"))
	var proj Project
	found, err := s.db.Get(bucketProjects, name, &proj)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "project no encontrado")
		return
	}
	server.WriteJSON(w, 200, proj)
}

func (s *Service) deleteProject(w http.ResponseWriter, r *http.Request) {
	name := projectName(r.PathValue("project"))
	var proj Project
	found, err := s.db.Get(bucketProjects, name, &proj)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "project no encontrado")
		return
	}
	// La API real hace un soft-delete (state -> DELETE_REQUESTED) en vez de
	// borrar el recurso; se modela igual para que un `get` posterior siga
	// encontrando el project, como espera algún código que verifica el
	// estado tras destruir.
	proj.State = "DELETE_REQUESTED"
	proj.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.Put(bucketProjects, name, proj); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	respBytes, _ := json.Marshal(proj)
	s.seq++
	op := Operation{
		Name:     operationName(fmt.Sprintf("op-%d", s.seq)),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

// getOperation siempre devuelve done=true: este emulador no modela
// operaciones realmente asíncronas para Resource Manager.
func (s *Service) getOperation(w http.ResponseWriter, r *http.Request) {
	server.WriteJSON(w, 200, Operation{Name: operationName(r.PathValue("operation")), Done: true})
}
