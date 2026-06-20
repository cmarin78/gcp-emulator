// Package logging emula un subconjunto de Cloud Logging
// (logging.googleapis.com/v2): sinks a nivel de proyecto
// (projects/{project}/sinks), igual que antes, más entries:write/
// entries:list real (Fase 11): las entradas que el cliente escribe, y las
// que los demás servicios del emulador generan al actuar de verdad
// (Cloud Scheduler/Tasks/Pub/Sub vía internal/activity), quedan
// consultables — ya no es un stub vacío.
package logging

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/activity"
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

	mux.HandleFunc("POST /v2/entries:write", s.writeEntries)
	mux.HandleFunc("POST /v2/entries:list", s.listEntries)
}

func sinkKey(project, sink string) string {
	return fmt.Sprintf("projects/%s/sinks/%s", project, sink)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
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

// entryFields is the relevant subset of logging.v2.LogEntry accepted from
// the client, shared between the per-entry overrides and the top-level
// defaults of a write request — matching the real API's "entries[] inherit
// from the top-level fields unless overridden" behavior.
type entryFields struct {
	LogName     string            `json:"logName"`
	Severity    string            `json:"severity"`
	TextPayload string            `json:"textPayload"`
	Resource    map[string]any    `json:"resource"`
	Labels      map[string]string `json:"labels"`
	Timestamp   string            `json:"timestamp"`
}

// writeEntries implementa entries.write: persiste cada entrada (vía
// internal/activity, la misma store que alimentan los disparos reales de
// Cloud Scheduler/Tasks/Pub/Sub) bajo el proyecto extraído de su logName.
func (s *Svc) writeEntries(w http.ResponseWriter, r *http.Request) {
	var body struct {
		entryFields
		Entries []entryFields `json:"entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	entries := body.Entries
	if len(entries) == 0 {
		entries = []entryFields{body.entryFields}
	}
	for _, e := range entries {
		logName := orDefault(e.LogName, body.LogName)
		project := activity.ProjectOf(logName)
		if project == "" {
			// entries.write también acepta logName solo a nivel de
			// proyecto sin segmento /logs/ explícito en algunos clientes;
			// si no se puede resolver el proyecto, la entrada no tiene
			// dónde guardarse (igual de inútil que en la API real sin
			// parent válido).
			continue
		}
		resource := e.Resource
		if resource == nil {
			resource = body.Resource
		}
		labels := e.Labels
		if labels == nil {
			labels = body.Labels
		}
		activity.RecordLog(project, activity.LogEntry{
			LogName:     logName,
			Severity:    orDefault(orDefault(e.Severity, body.Severity), "DEFAULT"),
			TextPayload: e.TextPayload,
			Resource:    resource,
			Labels:      labels,
			Timestamp:   e.Timestamp,
		})
	}
	server.WriteJSON(w, 200, map[string]any{})
}

// listEntries implementa entries.list: junta las entradas de todos los
// proyectos en resourceNames (formato "projects/{project}") y aplica un
// filtro simple por substring sobre severity/textPayload/logName — no es
// el lenguaje de filtros completo de la API real, pero alcanza para
// verificar que algo se registró.
func (s *Svc) listEntries(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ResourceNames []string `json:"resourceNames"`
		Filter        string   `json:"filter"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	seen := map[string]bool{}
	out := []activity.LogEntry{}
	for _, rn := range body.ResourceNames {
		project := activity.ProjectOf(rn)
		if project == "" || seen[project] {
			continue
		}
		seen[project] = true
		out = append(out, activity.ListLogs(project)...)
	}
	if body.Filter != "" {
		filtered := make([]activity.LogEntry, 0, len(out))
		for _, e := range out {
			if strings.Contains(e.Severity, body.Filter) ||
				strings.Contains(e.TextPayload, body.Filter) ||
				strings.Contains(e.LogName, body.Filter) {
				filtered = append(filtered, e)
			}
		}
		out = filtered
	}
	server.WriteJSON(w, 200, map[string]any{"entries": out})
}
