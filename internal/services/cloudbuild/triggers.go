// Fase 17: Cloud Build triggers (cloudbuild.projects.triggers).
// google_cloudbuild_trigger es uno de los recursos Cloud Build más usados
// en IaC, pero solo existían builds. Los triggers usan la ruta legacy
// /v1/projects/{project}/triggers (sin location), que es la que tanto
// Terraform como gcloud cloudbuild triggers usan.
package cloudbuild

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

const bucketTriggers = "cloudbuild.triggers"

// TriggerGitHub mirrors the real GitHubEventsConfig.
type TriggerGitHub struct {
	Owner  string              `json:"owner,omitempty"`
	Name   string              `json:"name,omitempty"`
	Push   *TriggerGitHubPush `json:"push,omitempty"`
	PullRequest *TriggerGitHubPR `json:"pullRequest,omitempty"`
}

type TriggerGitHubPush struct {
	Branch string `json:"branch,omitempty"`
	Tag    string `json:"tag,omitempty"`
}

type TriggerGitHubPR struct {
	Branch string `json:"branch,omitempty"`
}

// BuildTrigger mirrors the real BuildTrigger resource.
type BuildTrigger struct {
	ID               string        `json:"id"`
	Name             string        `json:"name,omitempty"`
	Description      string        `json:"description,omitempty"`
	Tags             []string      `json:"tags,omitempty"`
	Disabled         bool          `json:"disabled,omitempty"`
	Filename         string        `json:"filename,omitempty"`
	GitHub           *TriggerGitHub `json:"github,omitempty"`
	IncludedFiles    []string      `json:"includedFiles,omitempty"`
	IgnoredFiles     []string      `json:"ignoredFiles,omitempty"`
	CreateTime       string        `json:"createTime,omitempty"`
	ResourceName     string        `json:"resourceName,omitempty"`
}

func triggerKey(project, id string) string {
	return fmt.Sprintf("%s/%s", project, id)
}

func (s *Service) registerTriggers(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/triggers", s.createTrigger)
	mux.HandleFunc("GET /v1/projects/{project}/triggers", s.listTriggers)
	mux.HandleFunc("GET /v1/projects/{project}/triggers/{trigger}", s.getTrigger)
	mux.HandleFunc("PATCH /v1/projects/{project}/triggers/{trigger}", s.patchTrigger)
	mux.HandleFunc("DELETE /v1/projects/{project}/triggers/{trigger}", s.deleteTrigger)
	// :run dispatches a trigger manually; returns a Build operation.
	mux.HandleFunc("POST /v1/projects/{project}/triggers/{triggerAction}", s.triggerAction)
}

func (s *Service) createTrigger(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body BuildTrigger
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}

	id := s.nextID()
	body.ID = id
	body.ResourceName = fmt.Sprintf("projects/%s/triggers/%s", project, id)
	if body.Name == "" {
		body.Name = "trigger-" + id
	}
	body.CreateTime = time.Now().UTC().Format(time.RFC3339)

	if err := s.db.Put(bucketTriggers, triggerKey(project, id), body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, body)
}

func (s *Service) listTriggers(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var items []BuildTrigger
	_ = s.db.List(bucketTriggers, project+"/", func(_ string, raw []byte) error {
		var t BuildTrigger
		if err := json.Unmarshal(raw, &t); err == nil {
			items = append(items, t)
		}
		return nil
	})
	if items == nil {
		items = []BuildTrigger{}
	}
	server.WriteJSON(w, 200, map[string]any{"triggers": items})
}

func (s *Service) getTrigger(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("trigger")
	var trigger BuildTrigger
	found, err := s.db.Get(bucketTriggers, triggerKey(project, id), &trigger)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "trigger "+id+" not found")
		return
	}
	server.WriteJSON(w, 200, trigger)
}

func (s *Service) patchTrigger(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("trigger")
	key := triggerKey(project, id)
	var existing BuildTrigger
	found, err := s.db.Get(bucketTriggers, key, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "trigger "+id+" not found")
		return
	}
	var patch BuildTrigger
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	// Preserve immutable fields.
	patch.ID = existing.ID
	patch.ResourceName = existing.ResourceName
	patch.CreateTime = existing.CreateTime
	if patch.Name == "" {
		patch.Name = existing.Name
	}
	if err := s.db.Put(bucketTriggers, key, patch); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, patch)
}

func (s *Service) deleteTrigger(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("trigger")
	key := triggerKey(project, id)
	var existing BuildTrigger
	if found, _ := s.db.Get(bucketTriggers, key, &existing); !found {
		server.WriteError(w, 404, "NOT_FOUND", "trigger "+id+" not found")
		return
	}
	if err := s.db.Delete(bucketTriggers, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

// triggerAction handles POST .../triggers/{id}:run — dispatches the trigger
// manually and returns a build operation (always SUCCESS, shape-only).
func (s *Service) triggerAction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	raw := r.PathValue("triggerAction")
	id, action, ok := strings.Cut(raw, ":")
	if !ok || action != "run" {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	var existing BuildTrigger
	if found, _ := s.db.Get(bucketTriggers, triggerKey(project, id), &existing); !found {
		server.WriteError(w, 404, "NOT_FOUND", "trigger "+id+" not found")
		return
	}

	buildID := s.nextID()
	b := Build{
		ID:         buildID,
		ProjectID:  project,
		Status:     "SUCCESS",
		CreateTime: time.Now().UTC().Format(time.RFC3339),
		StartTime:  time.Now().UTC().Format(time.RFC3339),
		FinishTime: time.Now().UTC().Format(time.RFC3339),
	}
	_ = s.db.Put(bucketBuilds, buildKey(project, buildID), b)

	metaBytes, _ := json.Marshal(buildOperationMetadata{Build: b})
	respBytes, _ := json.Marshal(b)
	server.WriteJSON(w, 200, Operation{
		Name:     operationName(buildID),
		Done:     true,
		Metadata: metaBytes,
		Response: respBytes,
	})
}
