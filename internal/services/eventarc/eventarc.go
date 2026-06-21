// Package eventarc emulates a subset of the Eventarc API
// (eventarc.googleapis.com/v1): triggers. Real triggers wire up an
// underlying Pub/Sub subscription and route matching events to a
// destination (most commonly a Cloud Run service); this emulator just
// persists the resource shape and a synthesized transport.pubsub.subscription
// name, with no real event delivery, matching the
// "shape-compatible, not behavior-complete" approach used elsewhere.
// Mutations return a google.longrunning.Operation, matching the real API.
package eventarc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketTriggers = "eventarc.triggers"

// EventFilter mirrors eventarc#EventFilter.
type EventFilter struct {
	Attribute string `json:"attribute"`
	Value     string `json:"value"`
	Operator  string `json:"operator,omitempty"`
}

// CloudRunDestination mirrors eventarc#CloudRun.
type CloudRunDestination struct {
	Service string `json:"service"`
	Region  string `json:"region,omitempty"`
	Path    string `json:"path,omitempty"`
}

// HTTPEndpoint mirrors eventarc#HttpEndpoint -- a real, supported
// Destination variant (destination.http_endpoint in
// google_eventarc_trigger) that points at an arbitrary URL instead of
// resolving a GCP resource. Used here as the reliably-dispatchable
// destination for real CloudEvent delivery (see dispatch.go): a Cloud Run
// destination requires resolving the named service to its URI, which only
// succeeds if that service actually exists in this emulator.
type HTTPEndpoint struct {
	URI string `json:"uri"`
}

// Destination mirrors eventarc#Destination. Only CloudRun and HTTPEndpoint
// are modeled -- the real API also has GKE and Workflow variants, but
// these two cover the common Terraform cases and, combined, give Phase 11
// dispatch (dispatch.go) both a resource-resolved path and an
// always-reachable path for tests.
type Destination struct {
	CloudRun     *CloudRunDestination `json:"cloudRun,omitempty"`
	HTTPEndpoint *HTTPEndpoint        `json:"httpEndpoint,omitempty"`
}

// PubsubTransport mirrors eventarc#Pubsub.
type PubsubTransport struct {
	Topic        string `json:"topic,omitempty"`
	Subscription string `json:"subscription,omitempty"`
}

// Transport mirrors eventarc#Transport.
type Transport struct {
	Pubsub *PubsubTransport `json:"pubsub,omitempty"`
}

// Trigger mirrors the relevant subset of eventarc#Trigger.
type Trigger struct {
	Name           string            `json:"name"`
	UID            string            `json:"uid"`
	EventFilters   []EventFilter     `json:"eventFilters"`
	ServiceAccount string            `json:"serviceAccount,omitempty"`
	Destination    Destination       `json:"destination"`
	Transport      Transport         `json:"transport"`
	Labels         map[string]string `json:"labels,omitempty"`
	CreateTime     string            `json:"createTime,omitempty"`
	UpdateTime     string            `json:"updateTime,omitempty"`
	Etag           string            `json:"etag,omitempty"`
}

// Operation mirrors google.longrunning.Operation.
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

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/triggers", s.createTrigger)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/triggers", s.listTriggers)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/triggers/{trigger}", s.getTrigger)
	mux.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/triggers/{trigger}", s.patchTrigger)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/triggers/{trigger}", s.deleteTrigger)
	// "{trigger}:publishEvent" no se puede expresar como un patrón mixto en
	// el mux de Go (un wildcard debe ocupar el segmento completo); se
	// captura el segmento entero y se separa con strings.Cut, mismo patrón
	// que Cloud Scheduler usa para "{job}:run"/":pause"/":resume" y que
	// networkmanagement usa para "{test}:rerun".
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/triggers/{triggerAction}", s.triggerAction)
}

// triggerAction despacha el único custom method de este recurso
// ("{trigger}:publishEvent", ver dispatch.go).
func (s *Service) triggerAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := strings.Cut(r.PathValue("triggerAction"), ":")
	if !ok || action != "publishEvent" {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	s.publishEvent(w, r, id)
}

func triggerKey(project, location, trigger string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, trigger)
}

func triggerName(project, location, trigger string) string {
	return fmt.Sprintf("projects/%s/locations/%s/triggers/%s", project, location, trigger)
}

func (s *Service) nextID() int64 {
	s.seq++
	return s.seq
}

func (s *Service) writeOperation(w http.ResponseWriter, project, location, verb string, t Trigger) {
	meta, _ := json.Marshal(map[string]string{"target": t.Name, "verb": verb})
	resp, _ := json.Marshal(t)
	op := Operation{
		Name:     fmt.Sprintf("projects/%s/locations/%s/operations/op-%d", project, location, s.nextID()),
		Done:     true,
		Metadata: meta,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createTrigger(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	triggerID := r.URL.Query().Get("triggerId")
	if triggerID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "triggerId is required")
		return
	}
	var body struct {
		EventFilters   []EventFilter     `json:"eventFilters"`
		ServiceAccount string            `json:"serviceAccount"`
		Destination    Destination       `json:"destination"`
		Transport      Transport         `json:"transport"`
		Labels         map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if len(body.EventFilters) == 0 {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "eventFilters is required")
		return
	}
	hasCloudRun := body.Destination.CloudRun != nil && body.Destination.CloudRun.Service != ""
	hasHTTPEndpoint := body.Destination.HTTPEndpoint != nil && body.Destination.HTTPEndpoint.URI != ""
	if !hasCloudRun && !hasHTTPEndpoint {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "destination.cloudRun.service or destination.httpEndpoint.uri is required")
		return
	}
	var existing Trigger
	found, err := s.db.Get(bucketTriggers, triggerKey(project, location, triggerID), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "trigger already exists: "+triggerID)
		return
	}
	transport := body.Transport
	if transport.Pubsub == nil {
		transport.Pubsub = &PubsubTransport{}
	}
	if transport.Pubsub.Subscription == "" {
		transport.Pubsub.Subscription = fmt.Sprintf("projects/%s/subscriptions/eventarc-%s-sub-%d", project, triggerID, s.nextID())
	}
	now := time.Now().UTC().Format(time.RFC3339)
	t := Trigger{
		Name:           triggerName(project, location, triggerID),
		UID:            fmt.Sprintf("uid-%d", time.Now().UnixNano()),
		EventFilters:   body.EventFilters,
		ServiceAccount: body.ServiceAccount,
		Destination:    body.Destination,
		Transport:      transport,
		Labels:         body.Labels,
		CreateTime:     now,
		UpdateTime:     now,
		Etag:           fmt.Sprintf("etag-%d", s.nextID()),
	}
	if err := s.db.Put(bucketTriggers, triggerKey(project, location, triggerID), t); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "create", t)
}

func (s *Service) listTriggers(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)
	items := []Trigger{}
	_ = s.db.List(bucketTriggers, prefix, func(key string, raw []byte) error {
		var t Trigger
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		items = append(items, t)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"triggers": items})
}

func (s *Service) getTrigger(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	trigger := r.PathValue("trigger")
	var t Trigger
	found, err := s.db.Get(bucketTriggers, triggerKey(project, location, trigger), &t)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "trigger not found")
		return
	}
	server.WriteJSON(w, 200, t)
}

func (s *Service) patchTrigger(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	trigger := r.PathValue("trigger")
	var existing Trigger
	found, err := s.db.Get(bucketTriggers, triggerKey(project, location, trigger), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "trigger not found")
		return
	}
	var body struct {
		EventFilters []EventFilter     `json:"eventFilters"`
		Destination  *Destination      `json:"destination"`
		Labels       map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if len(body.EventFilters) > 0 {
		existing.EventFilters = body.EventFilters
	}
	if body.Destination != nil {
		existing.Destination = *body.Destination
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	existing.Etag = fmt.Sprintf("etag-%d", s.nextID())
	if err := s.db.Put(bucketTriggers, triggerKey(project, location, trigger), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "update", existing)
}

func (s *Service) deleteTrigger(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	trigger := r.PathValue("trigger")
	var existing Trigger
	_, _ = s.db.Get(bucketTriggers, triggerKey(project, location, trigger), &existing)
	if err := s.db.Delete(bucketTriggers, triggerKey(project, location, trigger)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if existing.Name == "" {
		existing.Name = triggerName(project, location, trigger)
	}
	s.writeOperation(w, project, location, "delete", existing)
}
