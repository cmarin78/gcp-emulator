// Package cloudbuild emulates a subset of the Cloud Build API
// (cloudbuild.googleapis.com/v1): builds. Real builds run a sequence of
// container steps and can take minutes; this emulator resolves every build
// synchronously and always reports status SUCCESS, following the same
// "shape-compatible, not behavior-complete" approach used by the other
// async-style services in this project (Cloud Run, Cloud Functions, etc.).
package cloudbuild

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketBuilds = "cloudbuild.builds"

// BuildStep mirrors the real API's BuildStep (only the fields clients
// typically set/read are modeled).
type BuildStep struct {
	Name string   `json:"name"`
	Args []string `json:"args,omitempty"`
	Env  []string `json:"env,omitempty"`
	ID   string   `json:"id,omitempty"`
}

// Build mirrors the real Build resource
// (projects/{p}/locations/{l}/builds/{id} or the legacy projects/{p}/builds/{id}).
type Build struct {
	ID         string      `json:"id"`
	ProjectID  string      `json:"projectId"`
	Status     string      `json:"status"`
	Steps      []BuildStep `json:"steps,omitempty"`
	Images     []string    `json:"images,omitempty"`
	Tags       []string    `json:"tags,omitempty"`
	LogsBucket string      `json:"logsBucket,omitempty"`
	LogURL     string      `json:"logUrl,omitempty"`
	CreateTime string      `json:"createTime,omitempty"`
	StartTime  string      `json:"startTime,omitempty"`
	FinishTime string      `json:"finishTime,omitempty"`
	SelfLink   string      `json:"selfLink,omitempty"`
}

// Operation mirrors the generic google.longrunning.Operation shape, same as
// Resource Manager/Artifact Registry/Cloud Run: always resolved (done=true)
// since the emulator runs everything synchronously.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

// buildOperationMetadata mirrors google.devtools.cloudbuild.v1.BuildOperationMetadata,
// which embeds the build itself — some clients (e.g. gcloud builds submit)
// read the build status from the operation's metadata rather than waiting
// for the response.
type buildOperationMetadata struct {
	Build Build `json:"build"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service {
	return &Service{db: db}
}

// Register mounts the Cloud Build routes, following the real
// cloudbuild.googleapis.com/v1 paths (legacy, project-scoped — no location
// segment, matching how most existing Terraform/gcloud usage calls it).
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/builds", s.createBuild)
	mux.HandleFunc("GET /v1/projects/{project}/builds", s.listBuilds)
	mux.HandleFunc("GET /v1/projects/{project}/builds/{build}", s.getBuild)
	mux.HandleFunc("GET /v1/projects/{project}/operations/{operation}", s.getOperation)
}

func buildKey(project, id string) string {
	return fmt.Sprintf("%s/%s", project, id)
}

func operationName(id string) string {
	return fmt.Sprintf("operations/build-%s", id)
}

func (s *Service) nextID() string {
	s.seq++
	return fmt.Sprintf("%d", s.seq)
}

func (s *Service) createBuild(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Steps      []BuildStep `json:"steps"`
		Images     []string    `json:"images"`
		Tags       []string    `json:"tags"`
		LogsBucket string      `json:"logsBucket"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := s.nextID()
	b := Build{
		ID:         id,
		ProjectID:  project,
		Status:     "SUCCESS",
		Steps:      body.Steps,
		Images:     body.Images,
		Tags:       body.Tags,
		LogsBucket: body.LogsBucket,
		LogURL:     fmt.Sprintf("https://console.cloud.google.com/cloud-build/builds/%s?project=%s", id, project),
		CreateTime: now,
		StartTime:  now,
		FinishTime: now,
		SelfLink:   fmt.Sprintf("/v1/projects/%s/builds/%s", project, id),
	}
	if err := s.db.Put(bucketBuilds, buildKey(project, id), b); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	respBytes, _ := json.Marshal(b)
	metaBytes, _ := json.Marshal(buildOperationMetadata{Build: b})
	op := Operation{
		Name:     operationName(id),
		Done:     true,
		Metadata: metaBytes,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) listBuilds(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	builds := []Build{}
	_ = s.db.List(bucketBuilds, project+"/", func(key string, raw []byte) error {
		var b Build
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		builds = append(builds, b)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"builds": builds})
}

func (s *Service) getBuild(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("build")
	var b Build
	found, err := s.db.Get(bucketBuilds, buildKey(project, id), &b)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "build not found")
		return
	}
	server.WriteJSON(w, 200, b)
}

// getOperation always reports done=true: this emulator doesn't model
// builds that take real time to run.
func (s *Service) getOperation(w http.ResponseWriter, r *http.Request) {
	server.WriteJSON(w, 200, Operation{Name: operationName(r.PathValue("operation")), Done: true})
}
