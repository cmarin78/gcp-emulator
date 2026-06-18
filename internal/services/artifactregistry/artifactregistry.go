// Package artifactregistry emula un subconjunto de la API de Artifact
// Registry (artifactregistry.googleapis.com/v1): repositories. Las
// operaciones de creación se devuelven como recursos
// "google.longrunning.Operation" ya resueltos (done=true), porque el
// emulador no necesita un flujo asíncrono real.
package artifactregistry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketRepositories = "artifactregistry.repositories"

// Repository replica el recurso "Repository".
type Repository struct {
	Name        string            `json:"name"` // projects/{p}/locations/{l}/repositories/{r}
	Format      string            `json:"format"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	UpdateTime  string            `json:"updateTime,omitempty"`
}

// Operation replica el shape genérico google.longrunning.Operation, que
// es distinto del Operation síncrono usado por Compute
// (internal/server.Operation). Artifact Registry siempre devuelve
// operaciones ya resueltas (done=true).
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

// Register monta las rutas de Artifact Registry, siguiendo los paths
// reales de artifactregistry.googleapis.com.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories", s.createRepository)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories", s.listRepositories)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repository}", s.getRepository)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repository}", s.deleteRepository)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/operations/{operation}", s.getOperation)
}

func repositoryName(project, location, repo string) string {
	return fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repo)
}

func operationName(project, location, op string) string {
	return fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, op)
}

func (s *Service) createRepository(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	repoID := r.URL.Query().Get("repositoryId")
	if repoID == "" {
		repoID = r.URL.Query().Get("repository_id")
	}
	if repoID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "repositoryId es requerido")
		return
	}
	var body struct {
		Format      string            `json:"format"`
		Description string            `json:"description"`
		Labels      map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Format == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "format es requerido (ej. DOCKER, NPM, MAVEN)")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	repo := Repository{
		Name:        repositoryName(project, location, repoID),
		Format:      body.Format,
		Description: body.Description,
		Labels:      body.Labels,
		CreateTime:  now,
		UpdateTime:  now,
	}
	if err := s.db.Put(bucketRepositories, repo.Name, repo); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	respBytes, _ := json.Marshal(repo)
	s.seq++
	op := Operation{
		Name:     operationName(project, location, fmt.Sprintf("op-%d", s.seq)),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) listRepositories(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/repositories/", project, location)
	repos := []Repository{}
	_ = s.db.List(bucketRepositories, prefix, func(key string, raw []byte) error {
		var repo Repository
		if err := json.Unmarshal(raw, &repo); err != nil {
			return err
		}
		repos = append(repos, repo)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"repositories": repos})
}

func (s *Service) getRepository(w http.ResponseWriter, r *http.Request) {
	name := repositoryName(r.PathValue("project"), r.PathValue("location"), r.PathValue("repository"))
	var repo Repository
	found, err := s.db.Get(bucketRepositories, name, &repo)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "repository no encontrado")
		return
	}
	server.WriteJSON(w, 200, repo)
}

func (s *Service) deleteRepository(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	name := repositoryName(project, location, r.PathValue("repository"))
	if err := s.db.Delete(bucketRepositories, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.seq++
	op := Operation{
		Name: operationName(project, location, fmt.Sprintf("op-%d", s.seq)),
		Done: true,
	}
	server.WriteJSON(w, 200, op)
}

// getOperation siempre devuelve done=true: este emulador no modela
// operaciones realmente asíncronas para Artifact Registry.
func (s *Service) getOperation(w http.ResponseWriter, r *http.Request) {
	name := operationName(r.PathValue("project"), r.PathValue("location"), r.PathValue("operation"))
	server.WriteJSON(w, 200, Operation{Name: name, Done: true})
}
