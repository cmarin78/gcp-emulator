// Package clouddeploy emulates a subset of the Cloud Deploy API
// (clouddeploy.googleapis.com/v1): delivery pipelines and targets
// (google_clouddeploy_delivery_pipeline and google_clouddeploy_target in Terraform).
package clouddeploy

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
	bucketPipelines = "clouddeploy.deliveryPipelines"
	bucketTargets   = "clouddeploy.targets"
)

// Stage represents a deployment stage in a delivery pipeline.
type Stage struct {
	TargetID string   `json:"targetId"`
	Profiles []string `json:"profiles,omitempty"`
}

// SerialPipeline contains a sequence of stages.
type SerialPipeline struct {
	Stages []Stage `json:"stages,omitempty"`
}

// DeliveryPipeline represents a Cloud Deploy delivery pipeline resource.
type DeliveryPipeline struct {
	Name           string            `json:"name"` // projects/{project}/locations/{location}/deliveryPipelines/{pipeline}
	Description    string            `json:"description,omitempty"`
	SerialPipeline *SerialPipeline   `json:"serialPipeline,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	UID            string            `json:"uid,omitempty"`
	CreateTime     string            `json:"createTime,omitempty"`
}

// GkeTarget configures deployment to a GKE cluster.
type GkeTarget struct {
	Cluster string `json:"cluster"`
}

// CloudRunTarget configures deployment to Cloud Run.
type CloudRunTarget struct {
	Location string `json:"location"`
}

// Target represents a Cloud Deploy deployment target.
type Target struct {
	Name        string            `json:"name"` // projects/{project}/locations/{location}/targets/{target}
	Description string            `json:"description,omitempty"`
	Gke         *GkeTarget        `json:"gke,omitempty"`
	Run         *CloudRunTarget   `json:"run,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	UID         string            `json:"uid,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
}

// Operation mirrors standard google.longrunning.Operation.
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

func New(db *storage.DB) *Service {
	return &Service{db: db}
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/deliveryPipelines", s.createPipeline)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/deliveryPipelines", s.listPipelines)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/deliveryPipelines/{pipeline}", s.getPipeline)
	mux.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/deliveryPipelines/{pipeline}", s.patchPipeline)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/deliveryPipelines/{pipeline}", s.deletePipeline)

	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/targets", s.createTarget)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/targets", s.listTargets)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/targets/{target}", s.getTarget)
	mux.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/targets/{target}", s.patchTarget)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/targets/{target}", s.deleteTarget)
}

func pipelineKey(project, location, pipeline string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, pipeline)
}

func pipelineName(project, location, pipeline string) string {
	return fmt.Sprintf("projects/%s/locations/%s/deliveryPipelines/%s", project, location, pipeline)
}

func targetKey(project, location, target string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, target)
}

func targetName(project, location, target string) string {
	return fmt.Sprintf("projects/%s/locations/%s/targets/%s", project, location, target)
}

func (s *Service) nextID() int64 {
	return atomic.AddInt64(&s.seq, 1)
}

func (s *Service) writeOp(w http.ResponseWriter, project, location string, res any) {
	respBytes, _ := json.Marshal(res)
	op := Operation{
		Name:     fmt.Sprintf("projects/%s/locations/%s/operations/op-clouddeploy-%d", project, location, s.nextID()),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createPipeline(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	pipelineID := r.URL.Query().Get("deliveryPipelineId")

	var p DeliveryPipeline
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if pipelineID == "" {
		if p.Name != "" {
			parts := strings.Split(p.Name, "/")
			pipelineID = parts[len(parts)-1]
		}
	}
	if pipelineID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "deliveryPipelineId is required")
		return
	}

	key := pipelineKey(project, location, pipelineID)
	var existing DeliveryPipeline
	found, err := s.db.Get(bucketPipelines, key, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "deliveryPipeline already exists: "+pipelineID)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	p.Name = pipelineName(project, location, pipelineID)
	p.UID = fmt.Sprintf("deploy-pipe-%d", s.nextID())
	p.CreateTime = now

	if err := s.db.Put(bucketPipelines, key, p); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, p)
}

func (s *Service) getPipeline(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	pipelineID := r.PathValue("pipeline")

	var p DeliveryPipeline
	found, err := s.db.Get(bucketPipelines, pipelineKey(project, location, pipelineID), &p)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "deliveryPipeline not found: "+pipelineID)
		return
	}
	server.WriteJSON(w, 200, p)
}

func (s *Service) listPipelines(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)

	var list []DeliveryPipeline
	err := s.db.List(bucketPipelines, prefix, func(key string, raw []byte) error {
		var p DeliveryPipeline
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		list = append(list, p)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if list == nil {
		list = []DeliveryPipeline{}
	}
	server.WriteJSON(w, 200, map[string]any{"deliveryPipelines": list})
}

func (s *Service) patchPipeline(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	pipelineID := r.PathValue("pipeline")

	key := pipelineKey(project, location, pipelineID)
	var p DeliveryPipeline
	found, err := s.db.Get(bucketPipelines, key, &p)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "deliveryPipeline not found: "+pipelineID)
		return
	}

	var patch DeliveryPipeline
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if patch.Description != "" {
		p.Description = patch.Description
	}
	if patch.SerialPipeline != nil {
		p.SerialPipeline = patch.SerialPipeline
	}
	if patch.Labels != nil {
		p.Labels = patch.Labels
	}
	if err := s.db.Put(bucketPipelines, key, p); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, p)
}

func (s *Service) deletePipeline(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	pipelineID := r.PathValue("pipeline")

	key := pipelineKey(project, location, pipelineID)
	var p DeliveryPipeline
	found, err := s.db.Get(bucketPipelines, key, &p)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "deliveryPipeline not found: "+pipelineID)
		return
	}
	if err := s.db.Delete(bucketPipelines, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, map[string]any{})
}

func (s *Service) createTarget(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	targetID := r.URL.Query().Get("targetId")

	var t Target
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if targetID == "" {
		if t.Name != "" {
			parts := strings.Split(t.Name, "/")
			targetID = parts[len(parts)-1]
		}
	}
	if targetID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "targetId is required")
		return
	}

	key := targetKey(project, location, targetID)
	var existing Target
	found, err := s.db.Get(bucketTargets, key, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "target already exists: "+targetID)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	t.Name = targetName(project, location, targetID)
	t.UID = fmt.Sprintf("deploy-tgt-%d", s.nextID())
	t.CreateTime = now

	if err := s.db.Put(bucketTargets, key, t); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, t)
}

func (s *Service) getTarget(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	targetID := r.PathValue("target")

	var t Target
	found, err := s.db.Get(bucketTargets, targetKey(project, location, targetID), &t)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "target not found: "+targetID)
		return
	}
	server.WriteJSON(w, 200, t)
}

func (s *Service) listTargets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)

	var list []Target
	err := s.db.List(bucketTargets, prefix, func(key string, raw []byte) error {
		var t Target
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		list = append(list, t)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if list == nil {
		list = []Target{}
	}
	server.WriteJSON(w, 200, map[string]any{"targets": list})
}

func (s *Service) patchTarget(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	targetID := r.PathValue("target")

	key := targetKey(project, location, targetID)
	var t Target
	found, err := s.db.Get(bucketTargets, key, &t)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "target not found: "+targetID)
		return
	}

	var patch Target
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if patch.Description != "" {
		t.Description = patch.Description
	}
	if patch.Gke != nil {
		t.Gke = patch.Gke
	}
	if patch.Run != nil {
		t.Run = patch.Run
	}
	if patch.Labels != nil {
		t.Labels = patch.Labels
	}
	if err := s.db.Put(bucketTargets, key, t); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, t)
}

func (s *Service) deleteTarget(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	targetID := r.PathValue("target")

	key := targetKey(project, location, targetID)
	var t Target
	found, err := s.db.Get(bucketTargets, key, &t)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "target not found: "+targetID)
		return
	}
	if err := s.db.Delete(bucketTargets, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, map[string]any{})
}
