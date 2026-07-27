// Fase 17: Firestore runQuery + subcollections.
//
// runQuery implementa filtros simples (fieldFilter con EQUAL) y limit sobre
// una colección. Los documentos se almacenan con fields en formato Firestore
// wire ({"stringValue":"x"}, {"integerValue":"5"}, etc.) — el emulador no
// valida el formato, pero sí sabe leer los tipos básicos para comparar.
//
// Subcollecciones: Se soportan paths arbitrariamente anidados mediante la
// ruta wildcard {path...}; el ServeMux de Go da precedencia a las rutas con
// más literales, así que las rutas existentes de {collection} y
// {collection}/{docID} siguen manejando los casos de 1 y 2 segmentos.
package firestore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

// ── StructuredQuery types ─────────────────────────────────────────────────────

type RunQueryRequest struct {
	StructuredQuery *StructuredQuery `json:"structuredQuery,omitempty"`
	Parent          string           `json:"parent,omitempty"`
}

type StructuredQuery struct {
	From   []CollectionSelector `json:"from,omitempty"`
	Where  *Filter              `json:"where,omitempty"`
	Limit  *int                 `json:"limit,omitempty"`
	Offset int                  `json:"offset,omitempty"`
}

type CollectionSelector struct {
	CollectionID   string `json:"collectionId"`
	AllDescendants bool   `json:"allDescendants,omitempty"`
}

type Filter struct {
	FieldFilter      *FieldFilter      `json:"fieldFilter,omitempty"`
	CompositeFilter  *CompositeFilter  `json:"compositeFilter,omitempty"`
}

type FieldFilter struct {
	Field FieldReference `json:"field"`
	Op    string         `json:"op"` // EQUAL, LESS_THAN, GREATER_THAN, etc.
	Value FieldValue     `json:"value"`
}

type FieldReference struct {
	FieldPath string `json:"fieldPath"`
}

// FieldValue holds typed Firestore values (wire format).
type FieldValue struct {
	StringValue  *string          `json:"stringValue,omitempty"`
	IntegerValue *string          `json:"integerValue,omitempty"`
	BooleanValue *bool            `json:"booleanValue,omitempty"`
	DoubleValue  *float64         `json:"doubleValue,omitempty"`
	MapValue     *MapValue        `json:"mapValue,omitempty"`
	NullValue    *string          `json:"nullValue,omitempty"`
}

type MapValue struct {
	Fields map[string]FieldValue `json:"fields,omitempty"`
}

type CompositeFilter struct {
	Op      string   `json:"op"` // AND
	Filters []Filter `json:"filters,omitempty"`
}

// QueryResult is one entry in the runQuery response array.
type QueryResult struct {
	Document *Document `json:"document,omitempty"`
	ReadTime string    `json:"readTime"`
}

// ── Route registration ─────────────────────────────────────────────────────────

func (s *Svc) registerQueryAndSubcollections(mux *http.ServeMux) {
	// runQuery at root documents level.
	// "documents:runQuery" is a literal path segment with colon — valid for ServeMux.
	mux.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:runQuery", s.runQuery)

	// Subcollection support via multi-segment wildcard.
	// Go ServeMux prefers the more-specific existing routes for 1- and 2-segment paths,
	// so {path...} only handles 3+ segment paths in practice.
	mux.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents/{path...}", s.subDocDispatch)
	mux.HandleFunc("GET /v1/projects/{project}/databases/{database}/documents/{path...}", s.subDocDispatch)
	mux.HandleFunc("PATCH /v1/projects/{project}/databases/{database}/documents/{path...}", s.subDocDispatch)
	mux.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/documents/{path...}", s.subDocDispatch)
}

// ── runQuery ─────────────────────────────────────────────────────────────────

func (s *Svc) runQuery(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	database := r.PathValue("database")

	var req RunQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if req.StructuredQuery == nil || len(req.StructuredQuery.From) == 0 {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "structuredQuery.from is required")
		return
	}

	collectionID := req.StructuredQuery.From[0].CollectionID
	prefix := fmt.Sprintf("projects/%s/databases/%s/documents/%s/", project, database, collectionID)

	allDescendants := req.StructuredQuery.From[0].AllDescendants

	// Collect docs in the collection.
	var docs []Document
	_ = s.db.List(bucketDocuments, prefix, func(key string, raw []byte) error {
		// Skip subcollection documents unless allDescendants is set.
		if !allDescendants {
			suffix := strings.TrimPrefix(key, prefix)
			if strings.Contains(suffix, "/") {
				return nil
			}
		}
		var d Document
		if err := json.Unmarshal(raw, &d); err == nil {
			docs = append(docs, d)
		}
		return nil
	})

	// Apply where filter.
	if req.StructuredQuery.Where != nil {
		docs = applyFilter(docs, req.StructuredQuery.Where)
	}

	// Apply offset.
	if req.StructuredQuery.Offset > 0 && req.StructuredQuery.Offset < len(docs) {
		docs = docs[req.StructuredQuery.Offset:]
	}

	// Apply limit.
	if req.StructuredQuery.Limit != nil && *req.StructuredQuery.Limit >= 0 {
		limit := *req.StructuredQuery.Limit
		if limit < len(docs) {
			docs = docs[:limit]
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	results := make([]QueryResult, 0, len(docs))
	for i := range docs {
		results = append(results, QueryResult{Document: &docs[i], ReadTime: now})
	}
	// The real REST runQuery returns newline-delimited JSON objects.
	// For emulator simplicity we return a JSON array — the test client handles either.
	server.WriteJSON(w, 200, results)
}

// applyFilter filters docs matching the given Filter (recursive for AND).
func applyFilter(docs []Document, f *Filter) []Document {
	if f == nil {
		return docs
	}
	if f.FieldFilter != nil {
		var out []Document
		for _, d := range docs {
			if matchFieldFilter(d, f.FieldFilter) {
				out = append(out, d)
			}
		}
		return out
	}
	if f.CompositeFilter != nil && strings.EqualFold(f.CompositeFilter.Op, "AND") {
		result := docs
		for _, sub := range f.CompositeFilter.Filters {
			sub := sub // capture
			result = applyFilter(result, &sub)
		}
		return result
	}
	return docs
}

// matchFieldFilter compares a document field value against the filter value.
// Supports op: EQUAL (and alias EQUAL for simplicity).
func matchFieldFilter(doc Document, ff *FieldFilter) bool {
	if doc.Fields == nil {
		return false
	}
	// Parse the fields JSON.
	var fields map[string]FieldValue
	if err := json.Unmarshal(doc.Fields, &fields); err != nil {
		return false
	}
	docVal, ok := fields[ff.Field.FieldPath]
	if !ok {
		return false
	}
	switch strings.ToUpper(ff.Op) {
	case "EQUAL", "OPERATOR_EQUAL":
		return fieldValuesEqual(docVal, ff.Value)
	}
	// Unsupported op — pass through (include all).
	return true
}

// fieldValuesEqual compares two FieldValues by content.
func fieldValuesEqual(a, b FieldValue) bool {
	if a.StringValue != nil && b.StringValue != nil {
		return *a.StringValue == *b.StringValue
	}
	if a.IntegerValue != nil && b.IntegerValue != nil {
		return *a.IntegerValue == *b.IntegerValue
	}
	if a.BooleanValue != nil && b.BooleanValue != nil {
		return *a.BooleanValue == *b.BooleanValue
	}
	if a.DoubleValue != nil && b.DoubleValue != nil {
		return *a.DoubleValue == *b.DoubleValue
	}
	return false
}

// ── Subcollection dispatcher ──────────────────────────────────────────────────

// subDocDispatch handles paths with 1+ segments captured by {path...}.
// For 1-segment (collection) and 2-segment (collection/docID) paths the
// existing specific routes take priority, so we only see 3+ segment paths.
func (s *Svc) subDocDispatch(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	database := r.PathValue("database")
	path := r.PathValue("path") // e.g., "orders/123/items" or "orders/123/items/456"

	segments := strings.Split(strings.Trim(path, "/"), "/")
	// Filter empty segments.
	nonEmpty := segments[:0]
	for _, seg := range segments {
		if seg != "" {
			nonEmpty = append(nonEmpty, seg)
		}
	}
	segments = nonEmpty

	if len(segments) < 1 {
		server.WriteError(w, 404, "NOT_FOUND", "invalid path")
		return
	}

	// Even number of segments = document path; odd = collection path.
	isDocument := len(segments)%2 == 0

	fullDocPath := strings.Join(segments, "/")
	fullName := fmt.Sprintf("projects/%s/databases/%s/documents/%s", project, database, fullDocPath)

	now := time.Now().UTC().Format(time.RFC3339)

	switch r.Method {
	case "GET":
		if isDocument {
			var doc Document
			found, err := s.db.Get(bucketDocuments, fullName, &doc)
			if err != nil {
				server.WriteError(w, 500, "INTERNAL", err.Error())
				return
			}
			if !found {
				server.WriteError(w, 404, "NOT_FOUND", "documento no encontrado: "+path)
				return
			}
			server.WriteJSON(w, 200, doc)
		} else {
			// List direct children of this collection only.
			listPrefix := fullName + "/"
			items := []Document{}
			_ = s.db.List(bucketDocuments, listPrefix, func(key string, raw []byte) error {
				suffix := strings.TrimPrefix(key, listPrefix)
				if strings.Contains(suffix, "/") {
					return nil // skip deeper nesting
				}
				var d Document
				if err := json.Unmarshal(raw, &d); err == nil {
					items = append(items, d)
				}
				return nil
			})
			server.WriteJSON(w, 200, map[string]any{"documents": items})
		}

	case "POST":
		if !isDocument {
			// Create in this collection.
			docID := r.URL.Query().Get("documentId")
			if docID == "" {
				s.seq++
				docID = fmt.Sprintf("auto-%d", s.seq)
			}
			var body struct {
				Fields json.RawMessage `json:"fields"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
				return
			}
			newName := fullName + "/" + docID
			var existing Document
			if found, _ := s.db.Get(bucketDocuments, newName, &existing); found {
				server.WriteError(w, 409, "ALREADY_EXISTS", "el documento ya existe")
				return
			}
			doc := Document{Name: newName, Fields: body.Fields, CreateTime: now, UpdateTime: now}
			if err := s.db.Put(bucketDocuments, newName, doc); err != nil {
				server.WriteError(w, 500, "INTERNAL", err.Error())
				return
			}
			server.WriteJSON(w, 200, doc)
		} else {
			server.WriteError(w, 405, "METHOD_NOT_ALLOWED", "use PATCH to update a document")
		}

	case "PATCH":
		if isDocument {
			var existing Document
			_, _ = s.db.Get(bucketDocuments, fullName, &existing)
			var body struct {
				Fields json.RawMessage `json:"fields"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
				return
			}
			if existing.CreateTime == "" {
				existing = Document{Name: fullName, CreateTime: now}
			}
			existing.Fields = body.Fields
			existing.UpdateTime = now
			if err := s.db.Put(bucketDocuments, fullName, existing); err != nil {
				server.WriteError(w, 500, "INTERNAL", err.Error())
				return
			}
			server.WriteJSON(w, 200, existing)
		} else {
			server.WriteError(w, 405, "METHOD_NOT_ALLOWED", "cannot PATCH a collection")
		}

	case "DELETE":
		if isDocument {
			if err := s.db.Delete(bucketDocuments, fullName); err != nil {
				server.WriteError(w, 500, "INTERNAL", err.Error())
				return
			}
			server.WriteJSON(w, 200, map[string]any{})
		} else {
			server.WriteError(w, 405, "METHOD_NOT_ALLOWED", "cannot DELETE a collection")
		}

	default:
		server.WriteError(w, 405, "METHOD_NOT_ALLOWED", r.Method+" not allowed")
	}
}
