// Package monitoring emula un subconjunto de Cloud Monitoring
// (monitoring.googleapis.com/v3): únicamente alertPolicies a nivel de
// proyecto. Es un stub de métricas: no hay series temporales reales ni
// evaluación de condiciones, solo persistencia CRUD de la política, lo
// necesario para que `google_monitoring_alert_policy` de Terraform
// funcione end-to-end.
package monitoring

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketAlertPolicies = "monitoring.alertPolicies"

// AlertPolicy replica el subconjunto relevante de
// google.monitoring.v3.AlertPolicy. Conditions/combiner/notificationChannels
// se mantienen como JSON crudo porque su estructura interna no es
// relevante para el emulador (solo se persiste y se devuelve tal cual).
type AlertPolicy struct {
	Name                 string          `json:"name"`
	DisplayName          string          `json:"displayName"`
	Combiner             string          `json:"combiner,omitempty"`
	Conditions           json.RawMessage `json:"conditions,omitempty"`
	NotificationChannels []string        `json:"notificationChannels,omitempty"`
	Documentation        json.RawMessage `json:"documentation,omitempty"`
	Enabled              bool            `json:"enabled"`
	CreationRecord       map[string]any  `json:"creationRecord,omitempty"`
}

type Svc struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Svc {
	return &Svc{db: db}
}

func (s *Svc) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v3/projects/{project}/alertPolicies", s.createAlertPolicy)
	mux.HandleFunc("GET /v3/projects/{project}/alertPolicies", s.listAlertPolicies)
	mux.HandleFunc("GET /v3/projects/{project}/alertPolicies/{policy}", s.getAlertPolicy)
	mux.HandleFunc("PATCH /v3/projects/{project}/alertPolicies/{policy}", s.patchAlertPolicy)
	mux.HandleFunc("DELETE /v3/projects/{project}/alertPolicies/{policy}", s.deleteAlertPolicy)

	// Stub minimo de timeSeries: list siempre devuelve vacio (no hay
	// pipeline real de metricas), pero el endpoint existe para clientes
	// que lo consulten.
	mux.HandleFunc("GET /v3/projects/{project}/timeSeries", s.listTimeSeries)
}

func (s *Svc) nextID() string {
	s.seq++
	return fmt.Sprintf("%d", s.seq)
}

func policyKey(project, policy string) string {
	return fmt.Sprintf("projects/%s/alertPolicies/%s", project, policy)
}

func (s *Svc) createAlertPolicy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		DisplayName          string          `json:"displayName"`
		Combiner             string          `json:"combiner"`
		Conditions           json.RawMessage `json:"conditions"`
		NotificationChannels []string        `json:"notificationChannels"`
		Documentation        json.RawMessage `json:"documentation"`
		Enabled              *bool           `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	id := s.nextID()
	name := policyKey(project, id)
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	ap := AlertPolicy{
		Name:                 name,
		DisplayName:          body.DisplayName,
		Combiner:             orDefault(body.Combiner, "OR"),
		Conditions:           body.Conditions,
		NotificationChannels: body.NotificationChannels,
		Documentation:        body.Documentation,
		Enabled:              enabled,
		CreationRecord: map[string]any{
			"mutateTime": time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := s.db.Put(bucketAlertPolicies, name, ap); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, ap)
}

func (s *Svc) listAlertPolicies(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/alertPolicies/", r.PathValue("project"))
	items := []AlertPolicy{}
	_ = s.db.List(bucketAlertPolicies, prefix, func(key string, raw []byte) error {
		var ap AlertPolicy
		if err := json.Unmarshal(raw, &ap); err != nil {
			return err
		}
		items = append(items, ap)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"alertPolicies": items})
}

func (s *Svc) getAlertPolicy(w http.ResponseWriter, r *http.Request) {
	name := policyKey(r.PathValue("project"), r.PathValue("policy"))
	var ap AlertPolicy
	found, err := s.db.Get(bucketAlertPolicies, name, &ap)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "alertPolicy no encontrada")
		return
	}
	server.WriteJSON(w, 200, ap)
}

func (s *Svc) patchAlertPolicy(w http.ResponseWriter, r *http.Request) {
	name := policyKey(r.PathValue("project"), r.PathValue("policy"))
	var existing AlertPolicy
	found, err := s.db.Get(bucketAlertPolicies, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "alertPolicy no encontrada")
		return
	}
	var body struct {
		DisplayName          string          `json:"displayName"`
		Combiner             string          `json:"combiner"`
		Conditions           json.RawMessage `json:"conditions"`
		NotificationChannels []string        `json:"notificationChannels"`
		Documentation        json.RawMessage `json:"documentation"`
		Enabled              *bool           `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.DisplayName != "" {
		existing.DisplayName = body.DisplayName
	}
	if body.Combiner != "" {
		existing.Combiner = body.Combiner
	}
	if body.Conditions != nil {
		existing.Conditions = body.Conditions
	}
	if body.NotificationChannels != nil {
		existing.NotificationChannels = body.NotificationChannels
	}
	if body.Documentation != nil {
		existing.Documentation = body.Documentation
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if err := s.db.Put(bucketAlertPolicies, name, existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Svc) deleteAlertPolicy(w http.ResponseWriter, r *http.Request) {
	name := policyKey(r.PathValue("project"), r.PathValue("policy"))
	if err := s.db.Delete(bucketAlertPolicies, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Svc) listTimeSeries(w http.ResponseWriter, r *http.Request) {
	server.WriteJSON(w, 200, map[string]any{"timeSeries": []any{}})
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
