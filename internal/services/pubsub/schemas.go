// Fase 17: Pub/Sub schema registry (pubsub.schemas).
// google_pubsub_schema + schema_settings en google_pubsub_topic son parte
// del stack moderno de Pub/Sub. Los schemas se validan en forma y tipo
// pero no se compila ni ejecuta Avro/Protobuf real — el lab solo necesita
// que los recursos existan y los topics los referencien.
package pubsub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cesar/gcp-emulator/internal/server"
)

const bucketSchemas = "pubsub.schemas"

// Schema replica pubsub#Schema.
type Schema struct {
	Name       string `json:"name"`       // projects/{project}/schemas/{schema}
	Type       string `json:"type"`       // AVRO | PROTOCOL_BUFFER
	Definition string `json:"definition"` // schema source (opaque for the emulator)
}

func schemaName(project, id string) string {
	return fmt.Sprintf("projects/%s/schemas/%s", project, id)
}

func (s *Service) registerSchemas(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/schemas", s.createSchema)
	mux.HandleFunc("GET /v1/projects/{project}/schemas", s.listSchemas)
	mux.HandleFunc("GET /v1/projects/{project}/schemas/{schema}", s.getSchema)
	mux.HandleFunc("DELETE /v1/projects/{project}/schemas/{schema}", s.deleteSchema)
	// :validate — accepts any schema body and always returns valid.
	mux.HandleFunc("POST /v1/projects/{project}/schemas/{schemaAction}", s.schemaAction)
}

func (s *Service) createSchema(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	schemaID := r.URL.Query().Get("schemaId")

	var body Schema
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if schemaID == "" {
		// Some clients pass the name inside the body.
		if body.Name != "" {
			parts := strings.Split(body.Name, "/")
			schemaID = parts[len(parts)-1]
		}
	}
	if schemaID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "schemaId is required (query param or body.name)")
		return
	}
	if body.Type == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "type is required")
		return
	}

	name := schemaName(project, schemaID)
	var existing Schema
	if found, _ := s.db.Get(bucketSchemas, name, &existing); found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "schema "+schemaID+" already exists")
		return
	}

	body.Name = name
	if err := s.db.Put(bucketSchemas, name, body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, body)
}

func (s *Service) listSchemas(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := fmt.Sprintf("projects/%s/schemas/", project)
	var items []Schema
	_ = s.db.List(bucketSchemas, prefix, func(_ string, raw []byte) error {
		var sc Schema
		if err := json.Unmarshal(raw, &sc); err == nil {
			items = append(items, sc)
		}
		return nil
	})
	if items == nil {
		items = []Schema{}
	}
	server.WriteJSON(w, 200, map[string]any{"schemas": items})
}

func (s *Service) getSchema(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("schema")
	var sc Schema
	found, err := s.db.Get(bucketSchemas, schemaName(project, id), &sc)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "schema "+id+" not found")
		return
	}
	server.WriteJSON(w, 200, sc)
}

func (s *Service) deleteSchema(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("schema")
	key := schemaName(project, id)
	var existing Schema
	if found, _ := s.db.Get(bucketSchemas, key, &existing); !found {
		server.WriteError(w, 404, "NOT_FOUND", "schema "+id+" not found")
		return
	}
	if err := s.db.Delete(bucketSchemas, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

// schemaAction dispatches schema verb actions (:validate).
func (s *Service) schemaAction(w http.ResponseWriter, r *http.Request) {
	_, action, ok := strings.Cut(r.PathValue("schemaAction"), ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	if action != "validate" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "acción no soportada: "+action)
		return
	}
	// Always valid in the emulator.
	server.WriteJSON(w, 200, map[string]any{})
}
