// dispatch.go añade triggers/{trigger}:publishEvent, una extensión propia
// de este emulador. La API real de Eventarc no tiene ningún endpoint para
// publicar eventos a mano -- los CloudEvents llegan automáticamente desde
// la fuente real (Audit Logs, Pub/Sub, Cloud Storage, etc.), nunca por una
// llamada directa a eventarc.googleapis.com. Pero "shape-compatible, no
// behavior-complete" para Fase 11 significa que, dado que el trigger ya
// existe con sus eventFilters y su destination, conviene poder simular la
// llegada de un evento real y observar un efecto real: un POST HTTP
// genuino al destino, en formato CloudEvents (binary content mode), sólo
// si el evento matchea los eventFilters -- igual de real que la entrega
// push de Pub/Sub (ver pubsub.deliverPush), pero disparado a mano en vez
// de automáticamente por una fuente de eventos que este emulador no tiene.
package eventarc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/activity"
	"github.com/cesar/gcp-emulator/internal/server"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

// CloudEvent es el body aceptado por publishEvent: el subconjunto de
// atributos CloudEvents (https://cloudevents.io) sobre el que realmente
// filtran los eventFilters de Eventarc -- "type" y "source" son atributos
// CloudEvents estándar; cualquier otro atributo (p.ej. "bucket" en eventos
// de Cloud Storage) viaja en Attributes y también puede filtrarse.
type CloudEvent struct {
	ID              string            `json:"id"`
	Source          string            `json:"source"`
	Type            string            `json:"type"`
	DataContentType string            `json:"datacontenttype,omitempty"`
	Attributes      map[string]string `json:"attributes,omitempty"`
	Data            json.RawMessage   `json:"data,omitempty"`
}

// attr devuelve el valor del atributo CloudEvents con ese nombre -- "type"
// y "source" son campos top-level, todo lo demás vive en Attributes.
func (e CloudEvent) attr(name string) (string, bool) {
	switch name {
	case "type":
		return e.Type, e.Type != ""
	case "source":
		return e.Source, e.Source != ""
	default:
		v, ok := e.Attributes[name]
		return v, ok
	}
}

// matchesFilters replica el subconjunto real de eventarc#EventFilter: por
// default es igualdad exacta; operator="match-path-pattern" habilita el
// path pattern matching real de Eventarc.
func matchesFilters(e CloudEvent, filters []EventFilter) bool {
	for _, f := range filters {
		v, ok := e.attr(f.Attribute)
		if !ok {
			return false
		}
		if f.Operator == "match-path-pattern" {
			if !matchPathPattern(f.Value, v) {
				return false
			}
			continue
		}
		if v != f.Value {
			return false
		}
	}
	return true
}

// matchPathPattern implementa el subconjunto de la sintaxis real de
// Eventarc path patterns usado en la práctica: segmentos separados por
// "/", "*" matchea exactamente un segmento, y un "**" final matchea cero o
// más segmentos restantes.
func matchPathPattern(pattern, value string) bool {
	pSegs := strings.Split(pattern, "/")
	vSegs := strings.Split(value, "/")
	for i, p := range pSegs {
		if p == "**" {
			return true
		}
		if i >= len(vSegs) {
			return false
		}
		if p != "*" && p != vSegs[i] {
			return false
		}
	}
	return len(pSegs) == len(vSegs)
}

// dbGetter es el subconjunto de *storage.DB que resolveDestinationURL
// necesita -- declarado como interface en vez de importar storage
// directamente para mantener la firma mínima y testeable.
type dbGetter interface {
	Get(bucket, key string, v any) (bool, error)
}

// resolveDestinationURL devuelve a dónde despachar el CloudEvent: si el
// trigger usa destination.httpEndpoint (el mismo mecanismo real que Cloud
// Scheduler/Tasks: una URL arbitraria), se usa directo. Si usa
// destination.cloudRun, se resuelve el nombre de servicio contra
// cloudrun.services -- sin importar el paquete cloudrun, mismo patrón de
// "leer el bucket directo" que iamenforce/activity/billingbudgets/
// networkmanagement ya usan para evitar ciclos de import.
func resolveDestinationURL(db dbGetter, project string, dest Destination) (string, error) {
	if dest.HTTPEndpoint != nil && dest.HTTPEndpoint.URI != "" {
		return dest.HTTPEndpoint.URI, nil
	}
	if dest.CloudRun != nil && dest.CloudRun.Service != "" {
		region := dest.CloudRun.Region
		if region == "" {
			region = "us-central1"
		}
		name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, region, dest.CloudRun.Service)
		var svc struct {
			URI string `json:"uri"`
		}
		found, err := db.Get("cloudrun.services", name, &svc)
		if err != nil {
			return "", err
		}
		if !found || svc.URI == "" {
			return "", fmt.Errorf("no se pudo resolver el servicio de Cloud Run %s a una URL", name)
		}
		return svc.URI + dest.CloudRun.Path, nil
	}
	return "", fmt.Errorf("el trigger no tiene un destination resoluble (cloudRun ni httpEndpoint)")
}

// PublishResult es la forma de respuesta de publishEvent. No es mirror de
// ningún recurso real -- ver el comentario del paquete arriba.
type PublishResult struct {
	Matched   bool   `json:"matched"`
	Delivered bool   `json:"delivered"`
	Status    string `json:"status"`
}

func (s *Service) publishEvent(w http.ResponseWriter, r *http.Request, triggerID string) {
	project := r.PathValue("project")
	location := r.PathValue("location")

	var t Trigger
	found, err := s.db.Get(bucketTriggers, triggerKey(project, location, triggerID), &t)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "trigger no encontrado")
		return
	}

	var event CloudEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if event.ID == "" {
		event.ID = fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}

	if !matchesFilters(event, t.EventFilters) {
		server.WriteJSON(w, 200, PublishResult{Matched: false, Status: "event no matchea los eventFilters del trigger"})
		return
	}

	server.WriteJSON(w, 200, s.deliver(project, t, event))
}

// deliver hace el POST HTTP real al destino resuelto, en CloudEvents
// binary content mode (atributos como headers ce-*, body = el data crudo
// del evento) -- el mismo binding que usaría un receptor real de Eventarc.
func (s *Service) deliver(project string, t Trigger, event CloudEvent) PublishResult {
	url, err := resolveDestinationURL(s.db, project, t.Destination)
	if err != nil {
		s.logDelivery(project, t, "error: "+err.Error(), "ERROR")
		return PublishResult{Matched: true, Delivered: false, Status: err.Error()}
	}

	body := event.Data
	if len(body) == 0 {
		body = []byte("{}")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		s.logDelivery(project, t, "error: "+err.Error(), "ERROR")
		return PublishResult{Matched: true, Delivered: false, Status: err.Error()}
	}
	contentType := event.DataContentType
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("ce-id", event.ID)
	req.Header.Set("ce-specversion", "1.0")
	req.Header.Set("ce-source", event.Source)
	req.Header.Set("ce-type", event.Type)
	for k, v := range event.Attributes {
		req.Header.Set("ce-"+k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		s.logDelivery(project, t, "error: "+err.Error(), "ERROR")
		return PublishResult{Matched: true, Delivered: false, Status: err.Error()}
	}
	defer resp.Body.Close()
	status := fmt.Sprintf("http %d", resp.StatusCode)
	severity := "INFO"
	delivered := resp.StatusCode < 400
	if !delivered {
		severity = "ERROR"
	}
	s.logDelivery(project, t, status, severity)
	return PublishResult{Matched: true, Delivered: delivered, Status: status}
}

func (s *Service) logDelivery(project string, t Trigger, status, severity string) {
	activity.RecordLog(project, activity.LogEntry{
		LogName:     fmt.Sprintf("projects/%s/logs/eventarc.googleapis.com%%2Fdeliveries", project),
		Severity:    severity,
		TextPayload: fmt.Sprintf("event delivery to trigger %s: %s", t.Name, status),
		Resource:    map[string]any{"type": "eventarc_trigger", "labels": map[string]string{"trigger_name": t.Name}},
	})
	activity.IncrCounter(project, "eventarc.googleapis.com/trigger/event_count", map[string]string{"trigger_name": t.Name})
}
