// Package firestore emula un subconjunto de la API de Firestore
// (firestore.googleapis.com/v1): bases de datos (admin) y documentos
// (CRUD simple). El campo "fields" de un documento se almacena y
// devuelve tal cual lo envía el cliente (passthrough JSON), sin
// modelar el wire format completo de Firestore Value
// (stringValue/mapValue/arrayValue/...): es una simplificación
// deliberada, suficiente para create/get/update/delete/list, pero no
// para queries estructuradas (runQuery) ni transacciones.
package firestore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketDatabases = "firestore.databases"
	bucketDocuments = "firestore.documents"
)

// Database replica el subconjunto relevante de
// google.firestore.admin.v1.Database.
type Database struct {
	Name            string `json:"name"` // projects/{p}/databases/{database}
	Type            string `json:"type"`
	LocationID      string `json:"locationId"`
	ConcurrencyMode string `json:"concurrencyMode,omitempty"`
	CreateTime      string `json:"createTime"`
	UpdateTime      string `json:"updateTime"`
	Etag            string `json:"etag"`
}

// Document replica el subconjunto relevante de
// google.firestore.v1.Document. Fields se mantiene como JSON crudo
// (ver comentario del paquete).
type Document struct {
	Name       string          `json:"name"` // projects/{p}/databases/{d}/documents/{collection}/{docId}
	Fields     json.RawMessage `json:"fields,omitempty"`
	CreateTime string          `json:"createTime"`
	UpdateTime string          `json:"updateTime"`
}

// Operation replica el shape genérico google.longrunning.Operation,
// usado por create/delete de bases de datos (admin API).
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Response json.RawMessage `json:"response,omitempty"`
}

type Svc struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Svc {
	return &Svc{db: db}
}

// Register monta las rutas de Firestore. Las bases de datos (admin)
// y los documentos comparten el prefijo /v1/projects/{project}/databases,
// pero con sufijos distintos, así que no hay colisión con otros
// servicios que también usan /v1/projects/{project}/... (Pub/Sub,
// Secret Manager, IAM) porque el literal "databases" los distingue.
func (s *Svc) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/databases", s.createDatabase)
	mux.HandleFunc("GET /v1/projects/{project}/databases", s.listDatabases)
	mux.HandleFunc("GET /v1/projects/{project}/databases/{database}", s.getDatabase)
	mux.HandleFunc("PATCH /v1/projects/{project}/databases/{database}", s.patchDatabase)
	mux.HandleFunc("DELETE /v1/projects/{project}/databases/{database}", s.deleteDatabase)
	mux.HandleFunc("GET /v1/projects/{project}/databases/{database}/operations/{operation}", s.getOperation)

	mux.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents/{collection}", s.createDocument)
	mux.HandleFunc("GET /v1/projects/{project}/databases/{database}/documents/{collection}", s.listDocuments)
	mux.HandleFunc("GET /v1/projects/{project}/databases/{database}/documents/{collection}/{docID}", s.getDocument)
	mux.HandleFunc("PATCH /v1/projects/{project}/databases/{database}/documents/{collection}/{docID}", s.patchDocument)
	mux.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/documents/{collection}/{docID}", s.deleteDocument)
}

func dbName(project, database string) string {
	return fmt.Sprintf("projects/%s/databases/%s", project, database)
}

func docName(project, database, collection, docID string) string {
	return fmt.Sprintf("projects/%s/databases/%s/documents/%s/%s", project, database, collection, docID)
}

func (s *Svc) nextOpName() string {
	s.seq++
	return fmt.Sprintf("op-%d", s.seq)
}

func (s *Svc) createDatabase(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	databaseID := r.URL.Query().Get("databaseId")
	if databaseID == "" {
		databaseID = "(default)"
	}
	var body struct {
		Type            string `json:"type"`
		LocationID      string `json:"locationId"`
		ConcurrencyMode string `json:"concurrencyMode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	name := dbName(project, databaseID)
	var existingDB Database
	found, err := s.db.Get(bucketDatabases, name, &existingDB)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "la base de datos ya existe: "+name)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	d := Database{
		Name:            name,
		Type:            orDefault(body.Type, "FIRESTORE_NATIVE"),
		LocationID:      orDefault(body.LocationID, "nam5"),
		ConcurrencyMode: orDefault(body.ConcurrencyMode, "OPTIMISTIC"),
		CreateTime:      now,
		UpdateTime:      now,
		Etag:            fmt.Sprintf("etag-%d", time.Now().UnixNano()),
	}
	if err := s.db.Put(bucketDatabases, d.Name, d); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	respBytes, _ := json.Marshal(d)
	op := Operation{Name: fmt.Sprintf("%s/operations/%s", d.Name, s.nextOpName()), Done: true, Response: respBytes}
	server.WriteJSON(w, 200, op)
}

func (s *Svc) listDatabases(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := fmt.Sprintf("projects/%s/databases/", project)
	items := []Database{}
	_ = s.db.List(bucketDatabases, prefix, func(key string, raw []byte) error {
		var d Database
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		items = append(items, d)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"databases": items})
}

func (s *Svc) getDatabase(w http.ResponseWriter, r *http.Request) {
	name := dbName(r.PathValue("project"), r.PathValue("database"))
	var d Database
	found, err := s.db.Get(bucketDatabases, name, &d)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "base de datos no encontrada")
		return
	}
	server.WriteJSON(w, 200, d)
}

func (s *Svc) patchDatabase(w http.ResponseWriter, r *http.Request) {
	name := dbName(r.PathValue("project"), r.PathValue("database"))
	var existing Database
	found, err := s.db.Get(bucketDatabases, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "base de datos no encontrada")
		return
	}
	var body struct {
		ConcurrencyMode string `json:"concurrencyMode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.ConcurrencyMode != "" {
		existing.ConcurrencyMode = body.ConcurrencyMode
	}
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	if err := s.db.Put(bucketDatabases, name, existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Svc) deleteDatabase(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	database := r.PathValue("database")
	name := dbName(project, database)
	var existing Database
	_, _ = s.db.Get(bucketDatabases, name, &existing)
	if err := s.db.Delete(bucketDatabases, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	respBytes, _ := json.Marshal(existing)
	op := Operation{Name: fmt.Sprintf("%s/operations/%s", name, s.nextOpName()), Done: true, Response: respBytes}
	server.WriteJSON(w, 200, op)
}

func (s *Svc) getOperation(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("%s/operations/%s",
		dbName(r.PathValue("project"), r.PathValue("database")), r.PathValue("operation"))
	server.WriteJSON(w, 200, Operation{Name: name, Done: true})
}

func (s *Svc) createDocument(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	database := r.PathValue("database")
	collection := r.PathValue("collection")
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
	name := docName(project, database, collection, docID)
	var existing Document
	if found, _ := s.db.Get(bucketDocuments, name, &existing); found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "el documento ya existe")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	doc := Document{Name: name, Fields: body.Fields, CreateTime: now, UpdateTime: now}
	if err := s.db.Put(bucketDocuments, name, doc); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, doc)
}

func (s *Svc) listDocuments(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	database := r.PathValue("database")
	collection := r.PathValue("collection")
	prefix := fmt.Sprintf("projects/%s/databases/%s/documents/%s/", project, database, collection)
	items := []Document{}
	_ = s.db.List(bucketDocuments, prefix, func(key string, raw []byte) error {
		var d Document
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		items = append(items, d)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"documents": items})
}

func (s *Svc) getDocument(w http.ResponseWriter, r *http.Request) {
	name := docName(r.PathValue("project"), r.PathValue("database"), r.PathValue("collection"), r.PathValue("docID"))
	var doc Document
	found, err := s.db.Get(bucketDocuments, name, &doc)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "documento no encontrado")
		return
	}
	server.WriteJSON(w, 200, doc)
}

func (s *Svc) patchDocument(w http.ResponseWriter, r *http.Request) {
	name := docName(r.PathValue("project"), r.PathValue("database"), r.PathValue("collection"), r.PathValue("docID"))
	var existing Document
	found, err := s.db.Get(bucketDocuments, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	var body struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if !found {
		// La API real de Firestore hace upsert en patch (updateDocument).
		existing = Document{Name: name, CreateTime: now}
	}
	existing.Fields = body.Fields
	existing.UpdateTime = now
	if err := s.db.Put(bucketDocuments, name, existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Svc) deleteDocument(w http.ResponseWriter, r *http.Request) {
	name := docName(r.PathValue("project"), r.PathValue("database"), r.PathValue("collection"), r.PathValue("docID"))
	if err := s.db.Delete(bucketDocuments, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
