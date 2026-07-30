// Package appengine emulates a subset of the App Engine Admin API
// (appengine.googleapis.com/v1): applications, services, and versions.
// No real App Engine runtime is involved — this is shape-compatible only.
// Mutations that would be long-running in production (create version, migrate
// traffic) return synchronous Operation with done:true, following the same
// approach used elsewhere in this project.
package appengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketApps     = "appengine.applications"
	bucketServices = "appengine.services"
	bucketVersions = "appengine.versions"
)

// Application mirrors the real appengine#Application resource.
type Application struct {
	Name         string `json:"name,omitempty"`
	ID           string `json:"id,omitempty"`
	LocationID   string `json:"locationId,omitempty"`
	DatabaseType string `json:"databaseType,omitempty"`
	ServingStatus string `json:"servingStatus,omitempty"`
	CreateTime   string `json:"createTime,omitempty"`
}

// TrafficSplit holds service traffic allocation.
type TrafficSplit struct {
	ShardBy     string             `json:"shardBy,omitempty"`
	Allocations map[string]float64 `json:"allocations,omitempty"`
}

// Service mirrors the real appengine#Service resource.
type Service struct {
	Name   string       `json:"name,omitempty"`
	ID     string       `json:"id,omitempty"`
	Split  *TrafficSplit `json:"split,omitempty"`
}

// Deployment holds version deployment info (shape only).
type Deployment struct {
	Files map[string]json.RawMessage `json:"files,omitempty"`
}

// Version mirrors the real appengine#Version resource.
type Version struct {
	Name           string            `json:"name,omitempty"`
	ID             string            `json:"id,omitempty"`
	Runtime        string            `json:"runtime,omitempty"`
	Env            string            `json:"env,omitempty"`
	ServingStatus  string            `json:"servingStatus,omitempty"`
	Deployment     *Deployment       `json:"deployment,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	CreateTime     string            `json:"createTime,omitempty"`
	CreatedBy      string            `json:"createdBy,omitempty"`
	EnvVariables   map[string]string `json:"envVariables,omitempty"`
}

// Operation mirrors google.longrunning.Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Response json.RawMessage `json:"response,omitempty"`
}

// Emulator is the App Engine emulator service.
type Emulator struct {
	db  *storage.DB
	seq int64
}

// New creates a new App Engine emulator backed by db.
func New(db *storage.DB) *Emulator { return &Emulator{db: db} }

// Register wires App Engine routes into mux.
func (s *Emulator) Register(mux *http.ServeMux) {
	// Applications.
	mux.HandleFunc("POST /appengine/v1/apps", s.createApp)
	mux.HandleFunc("GET /appengine/v1/apps/{app}", s.getApp)
	mux.HandleFunc("PATCH /appengine/v1/apps/{app}", s.patchApp)

	// Services.
	mux.HandleFunc("GET /appengine/v1/apps/{app}/services", s.listServices)
	mux.HandleFunc("GET /appengine/v1/apps/{app}/services/{service}", s.getService)
	mux.HandleFunc("PATCH /appengine/v1/apps/{app}/services/{service}", s.patchService)
	mux.HandleFunc("DELETE /appengine/v1/apps/{app}/services/{service}", s.deleteService)

	// Versions.
	mux.HandleFunc("POST /appengine/v1/apps/{app}/services/{service}/versions", s.createVersion)
	mux.HandleFunc("GET /appengine/v1/apps/{app}/services/{service}/versions", s.listVersions)
	mux.HandleFunc("GET /appengine/v1/apps/{app}/services/{service}/versions/{version}", s.getVersion)
	mux.HandleFunc("PATCH /appengine/v1/apps/{app}/services/{service}/versions/{version}", s.patchVersion)
	mux.HandleFunc("DELETE /appengine/v1/apps/{app}/services/{service}/versions/{version}", s.deleteVersion)

	// Traffic migration (verb-suffix on version).
	mux.HandleFunc("POST /appengine/v1/apps/{app}/services/{service}/versions/{versionAction}", s.versionAction)
}

func (s *Emulator) nextSeq() int64 {
	s.seq++
	return s.seq
}

func opName(app string, id int64) string {
	return fmt.Sprintf("apps/%s/operations/op-%016d", app, id)
}

func (s *Emulator) writeOperation(w http.ResponseWriter, app string, v any) {
	resp, _ := json.Marshal(v)
	op := Operation{Name: opName(app, s.nextSeq()), Done: true, Response: resp}
	server.WriteJSON(w, 200, op)
}

// ── Applications ──────────────────────────────────────────────────────────────

func (s *Emulator) createApp(w http.ResponseWriter, r *http.Request) {
	var body Application
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.ID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "id is required")
		return
	}
	var existing Application
	found, err := s.db.Get(bucketApps, body.ID, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "application already exists: "+body.ID)
		return
	}
	body.Name = "apps/" + body.ID
	body.ServingStatus = "SERVING"
	if body.LocationID == "" {
		body.LocationID = "us-central"
	}
	if body.DatabaseType == "" {
		body.DatabaseType = "CLOUD_DATASTORE_COMPATIBILITY"
	}
	body.CreateTime = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.Put(bucketApps, body.ID, body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, body.ID, body)
}

func (s *Emulator) getApp(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	var a Application
	found, err := s.db.Get(bucketApps, app, &a)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "application not found: "+app)
		return
	}
	server.WriteJSON(w, 200, a)
}

func (s *Emulator) patchApp(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	var existing Application
	found, err := s.db.Get(bucketApps, app, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "application not found: "+app)
		return
	}
	var body struct {
		ServingStatus string `json:"servingStatus"`
		LocationID    string `json:"locationId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.ServingStatus != "" {
		existing.ServingStatus = body.ServingStatus
	}
	if body.LocationID != "" {
		existing.LocationID = body.LocationID
	}
	if err := s.db.Put(bucketApps, app, existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, app, existing)
}

// ── Services ──────────────────────────────────────────────────────────────────

func serviceKey(app, svc string) string { return app + "/" + svc }

func serviceName(app, svc string) string { return fmt.Sprintf("apps/%s/services/%s", app, svc) }

func (s *Emulator) listServices(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	prefix := app + "/"
	items := []Service{}
	_ = s.db.List(bucketServices, prefix, func(_ string, raw []byte) error {
		var svc Service
		if err := json.Unmarshal(raw, &svc); err != nil {
			return err
		}
		items = append(items, svc)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"services": items})
}

func (s *Emulator) getService(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	svcID := r.PathValue("service")
	var svc Service
	found, err := s.db.Get(bucketServices, serviceKey(app, svcID), &svc)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "service not found: "+svcID)
		return
	}
	server.WriteJSON(w, 200, svc)
}

func (s *Emulator) patchService(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	svcID := r.PathValue("service")
	var existing Service
	// Auto-create service if it doesn't exist yet (App Engine does this on
	// first version deploy; here we allow explicit PATCH to upsert).
	s.db.Get(bucketServices, serviceKey(app, svcID), &existing) //nolint:errcheck
	if existing.ID == "" {
		existing.ID = svcID
		existing.Name = serviceName(app, svcID)
	}
	var body struct {
		Split *TrafficSplit `json:"split"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Split != nil {
		existing.Split = body.Split
	}
	if err := s.db.Put(bucketServices, serviceKey(app, svcID), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, app, existing)
}

func (s *Emulator) deleteService(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	svcID := r.PathValue("service")
	var existing Service
	found, _ := s.db.Get(bucketServices, serviceKey(app, svcID), &existing)
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "service not found: "+svcID)
		return
	}
	if err := s.db.Delete(bucketServices, serviceKey(app, svcID)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, app, map[string]any{})
}

// ── Versions ──────────────────────────────────────────────────────────────────

func versionKey(app, svc, ver string) string {
	return fmt.Sprintf("%s/%s/%s", app, svc, ver)
}

func versionName(app, svc, ver string) string {
	return fmt.Sprintf("apps/%s/services/%s/versions/%s", app, svc, ver)
}

func (s *Emulator) createVersion(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	svcID := r.PathValue("service")

	var body Version
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.ID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "id is required")
		return
	}

	body.Name = versionName(app, svcID, body.ID)
	body.ServingStatus = "SERVING"
	body.CreateTime = time.Now().UTC().Format(time.RFC3339)

	if err := s.db.Put(bucketVersions, versionKey(app, svcID, body.ID), body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	// Ensure parent service exists.
	var svc Service
	s.db.Get(bucketServices, serviceKey(app, svcID), &svc) //nolint:errcheck
	if svc.ID == "" {
		svc = Service{
			Name:  serviceName(app, svcID),
			ID:    svcID,
			Split: &TrafficSplit{ShardBy: "IP", Allocations: map[string]float64{body.ID: 1.0}},
		}
		s.db.Put(bucketServices, serviceKey(app, svcID), svc) //nolint:errcheck
	}

	s.writeOperation(w, app, body)
}

func (s *Emulator) listVersions(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	svcID := r.PathValue("service")
	prefix := fmt.Sprintf("%s/%s/", app, svcID)
	items := []Version{}
	_ = s.db.List(bucketVersions, prefix, func(_ string, raw []byte) error {
		var v Version
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		items = append(items, v)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"versions": items})
}

func (s *Emulator) getVersion(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	svcID := r.PathValue("service")
	verID := r.PathValue("version")
	var ver Version
	found, err := s.db.Get(bucketVersions, versionKey(app, svcID, verID), &ver)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "version not found: "+verID)
		return
	}
	server.WriteJSON(w, 200, ver)
}

func (s *Emulator) patchVersion(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	svcID := r.PathValue("service")
	verID := r.PathValue("version")
	var existing Version
	found, err := s.db.Get(bucketVersions, versionKey(app, svcID, verID), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "version not found: "+verID)
		return
	}
	var body struct {
		ServingStatus string            `json:"servingStatus"`
		EnvVariables  map[string]string `json:"envVariables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.ServingStatus != "" {
		existing.ServingStatus = body.ServingStatus
	}
	if body.EnvVariables != nil {
		existing.EnvVariables = body.EnvVariables
	}
	if err := s.db.Put(bucketVersions, versionKey(app, svcID, verID), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, app, existing)
}

func (s *Emulator) deleteVersion(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	svcID := r.PathValue("service")
	verID := r.PathValue("version")
	var existing Version
	found, _ := s.db.Get(bucketVersions, versionKey(app, svcID, verID), &existing)
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "version not found: "+verID)
		return
	}
	if err := s.db.Delete(bucketVersions, versionKey(app, svcID, verID)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, app, map[string]any{})
}

// versionAction handles POST .../versions/{id}:migrate (and future verb-suffix
// actions). Pattern: "{versionAction}" captures e.g. "v1:migrate"; we split on
// ":" to separate the version ID from the action verb.
func (s *Emulator) versionAction(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	svcID := r.PathValue("service")
	raw := r.PathValue("versionAction")
	verID, action, ok := strings.Cut(raw, ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "unknown action: "+raw)
		return
	}
	switch action {
	case "migrate":
		s.migrateVersion(w, r, app, svcID, verID)
	default:
		server.WriteError(w, 404, "NOT_FOUND", "unknown version action: "+action)
	}
}

func (s *Emulator) migrateVersion(w http.ResponseWriter, r *http.Request, app, svcID, verID string) {
	var ver Version
	found, _ := s.db.Get(bucketVersions, versionKey(app, svcID, verID), &ver)
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "version not found: "+verID)
		return
	}
	// Update service split to send all traffic to this version.
	var svc Service
	s.db.Get(bucketServices, serviceKey(app, svcID), &svc) //nolint:errcheck
	svc.Split = &TrafficSplit{ShardBy: "IP", Allocations: map[string]float64{verID: 1.0}}
	s.db.Put(bucketServices, serviceKey(app, svcID), svc) //nolint:errcheck
	s.writeOperation(w, app, svc)
}
