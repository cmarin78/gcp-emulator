// Package logging emula un subconjunto de Cloud Logging
// (logging.googleapis.com/v2): únicamente sinks a nivel de proyecto
// (projects/{project}/sinks). No hay pipeline de logs real (no se
// pueden escribir/consultar entradas); es un stub suficiente para que
// `google_logging_project_sink` de Terraform funcione end-to-end.
package logging

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketSinks = "logging.sinks"

// LogSink replica el subconjunto relevante de
// google.logging.v2.LogSink.
type LogSink struct {
	Name            string   `json:"name"`
	Destination     string   `json:"destination"`
	Filter          string   `json:"filter,omitempty"`
	Description     string   `json:"description,omitempty"`
	Disabled        bool     `json:"disabled,omitempty"`
	WriterIdentity  string   `json:"writerIdentity"`
	IncludeChildren bool     `json:"includeChildren,omitempty"`
	Exclusions      []string `json:"exclusions,omitempty"`
	CreateTime      string   `json:"createTime"`
	UpdateTime      string   `json:"updateTime"`
}

type Svc struct {
	db *storage.DB
}

func New(db *storage.DB) *Svc {
	return &Svc{db: db}
}

func (s *Svc) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v2/projects/{project}/sinks", s.createSink)
	mux.HandleFunc("GET /v2/projects/{project}/sinks", s.listSinks)
	mux.HandleFunc("GET /v2/projects/{project}/sinks/{sink}", s.getSink)
	mux.HandleFunc("PUT /v2/projects/{project}/sinks/{sink}", s.updateSink)
	mux.HandleFunc("PATCH /v2/projects/{project}/sinks/{sink}", s.updateSink)
	mux.HandleFunc("DELETE /v2/projects/{project}/sinks/{sink}", s.deleteSink)
}

func sinkKey(project, sink string) string {
	return fmt.Sprintf("projects/%s/sinks/%s", project, sink)
}

func (s *Svc) createSink(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name            string   `json:"name"`
		Destination     string   `json:"destination"`
		Filter          string   `json:"filter"`
		Description     string   `json:"description"`
		Disabled        bool     `json:"disabled"`
		IncludeChildren bool     `json:"includeChildren"`
		Exclusions      []string `json:"exclusions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es obligatorio")
		return
	}
	key := sinkKey(project, body.Name)
	if found, _ := s.db.Get(bucketSinks, key, new(LogSink)); found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "el sink ya existe")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	sink := LogSink{
		Name:            key,
		Destination:     body.Destination,
		Filter:          body.Filter,
		Description:     body.Description,
		Disabled:        body.Disabled,
		IncludeChildren: body.IncludeChildren,
		Exclusions:      body.Exclusions,
		WriterIdentity:  fmt.Sprintf("serviceAccount:emulator-logging@%s.iam.gserviceaccount.com", project),
		CreateTime:      now,
		UpdateTime:      now,
	}
	if err := s.db.Put(bucketSinks, key, sink); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, sink)
}

func (s *Svc) listSinks(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/sinks/", r.PathValue("project"))
	items := []LogSink{}
	_ = s.db.List(bucketSinks, prefix, func(key string, raw []byte) error {
		var sink LogSink
		if err := json.Unmarshal(raw, &sink); err != nil {
			return err
		}
		items = append(items, sink)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"sinks": items})
}

func (s *Svc) getSink(w http.ResponseWriter, r *http.Request) {
	key := sinkKey(r.PathValue("project"), r.PathValue("sink"))
	var sink LogSink
	found, err := s.db.Get(bucketSinks, key, &sink)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "sink no encontrado")
		return
	}
	server.WriteJSON(w, 200, sink)
}

func (s *Svc) updateSink(w http.ResponseWriter, r *http.Request) {
	key := sinkKey(r.PathValue("project"), r.PathValue("sink"))
	var existing LogSink
	found, err := s.db.Get(bucketSinks, key, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "sink no encontrado")
		return
	}
	var body struct {
		Destination     string   `json:"destination"`
		Filter          string   `json:"filter"`
		Description     string   `json:"description"`
		Disabled        bool     `json:"disabled"`
		IncludeChildren bool     `json:"includeChildren"`
		Exclusions      []string `json:"exclusions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Destination != "" {
		existing.Destination = body.Destination
	}
	existing.Filter = body.Filter
	existing.Description = body.Description
	existing.Disabled = body.Disabled
	existing.IncludeChildren = body.IncludeChildren
	existing.Exclusions = body.Exclusions
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.Put(bucketSinks, key, existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Svc) deleteSink(w http.ResponseWriter, r *http.Request) {
	key := sinkKey(r.PathValue("project"), r.PathValue("sink"))
	if err := s.db.Delete(bucketSinks, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}
