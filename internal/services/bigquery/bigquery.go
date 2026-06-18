// Package bigquery emula un subconjunto de la API de BigQuery
// (bigquery.googleapis.com/bigquery/v2): datasets y tables. A
// diferencia de Cloud Run/Functions/SQL/Firestore, las mutaciones de
// BigQuery son síncronas en la API real (no hay Operation): el
// recurso creado/actualizado se devuelve directamente.
package bigquery

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketDatasets = "bigquery.datasets"
	bucketTables   = "bigquery.tables"
)

// DatasetReference replica bigquery.DatasetReference.
type DatasetReference struct {
	DatasetID string `json:"datasetId"`
	ProjectID string `json:"projectId"`
}

// Dataset replica el subconjunto relevante de bigquery.Dataset.
type Dataset struct {
	Kind             string            `json:"kind"`
	ID               string            `json:"id"`
	DatasetReference DatasetReference  `json:"datasetReference"`
	Location         string            `json:"location,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	FriendlyName     string            `json:"friendlyName,omitempty"`
	Description      string            `json:"description,omitempty"`
	CreationTime     string            `json:"creationTime"`
	LastModifiedTime string            `json:"lastModifiedTime"`
	SelfLink         string            `json:"selfLink"`
	Etag             string            `json:"etag"`
}

// TableReference replica bigquery.TableReference.
type TableReference struct {
	ProjectID string `json:"projectId"`
	DatasetID string `json:"datasetId"`
	TableID   string `json:"tableId"`
}

// TableFieldSchema replica el subconjunto relevante de
// bigquery.TableFieldSchema.
type TableFieldSchema struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Mode string `json:"mode,omitempty"`
}

// TableSchema replica bigquery.TableSchema.
type TableSchema struct {
	Fields []TableFieldSchema `json:"fields,omitempty"`
}

// Table replica el subconjunto relevante de bigquery.Table.
type Table struct {
	Kind             string         `json:"kind"`
	ID               string         `json:"id"`
	TableReference   TableReference `json:"tableReference"`
	Schema           TableSchema    `json:"schema,omitempty"`
	Type             string         `json:"type"`
	CreationTime     string         `json:"creationTime"`
	LastModifiedTime string         `json:"lastModifiedTime"`
	SelfLink         string         `json:"selfLink"`
	Etag             string         `json:"etag"`
	NumRows          string         `json:"numRows"`
}

type Svc struct {
	db *storage.DB
}

func New(db *storage.DB) *Svc {
	return &Svc{db: db}
}

// Register monta las rutas de BigQuery, siguiendo los paths reales de
// bigquery.googleapis.com/bigquery/v2. El prefijo /bigquery/v2 no es
// compartido con ningún otro servicio.
func (s *Svc) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /bigquery/v2/projects/{project}/datasets", s.createDataset)
	mux.HandleFunc("GET /bigquery/v2/projects/{project}/datasets", s.listDatasets)
	mux.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}", s.getDataset)
	mux.HandleFunc("PATCH /bigquery/v2/projects/{project}/datasets/{dataset}", s.patchDataset)
	mux.HandleFunc("PUT /bigquery/v2/projects/{project}/datasets/{dataset}", s.patchDataset)
	mux.HandleFunc("DELETE /bigquery/v2/projects/{project}/datasets/{dataset}", s.deleteDataset)

	mux.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables", s.createTable)
	mux.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables", s.listTables)
	mux.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}", s.getTable)
	mux.HandleFunc("PATCH /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}", s.patchTable)
	mux.HandleFunc("PUT /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}", s.patchTable)
	mux.HandleFunc("DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}", s.deleteTable)
}

func datasetKey(project, dataset string) string {
	return fmt.Sprintf("%s/%s", project, dataset)
}

func tableKey(project, dataset, table string) string {
	return fmt.Sprintf("%s/%s/%s", project, dataset, table)
}

func (s *Svc) createDataset(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		DatasetReference DatasetReference  `json:"datasetReference"`
		Location         string            `json:"location"`
		Labels           map[string]string `json:"labels"`
		FriendlyName     string            `json:"friendlyName"`
		Description      string            `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "invalid", err.Error())
		return
	}
	if body.DatasetReference.DatasetID == "" {
		server.WriteError(w, 400, "invalid", "datasetReference.datasetId es requerido")
		return
	}
	var existingDS Dataset
	found, err := s.db.Get(bucketDatasets, datasetKey(project, body.DatasetReference.DatasetID), &existingDS)
	if err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "duplicate", "dataset ya existe: "+body.DatasetReference.DatasetID)
		return
	}
	now := fmt.Sprintf("%d", time.Now().UnixMilli())
	ds := Dataset{
		Kind: "bigquery#dataset",
		ID:   fmt.Sprintf("%s:%s", project, body.DatasetReference.DatasetID),
		DatasetReference: DatasetReference{
			DatasetID: body.DatasetReference.DatasetID,
			ProjectID: project,
		},
		Location:         orDefault(body.Location, "US"),
		Labels:           body.Labels,
		FriendlyName:     body.FriendlyName,
		Description:      body.Description,
		CreationTime:     now,
		LastModifiedTime: now,
		SelfLink:         fmt.Sprintf("projects/%s/datasets/%s", project, body.DatasetReference.DatasetID),
		Etag:             fmt.Sprintf("etag-%d", time.Now().UnixNano()),
	}
	if err := s.db.Put(bucketDatasets, datasetKey(project, ds.DatasetReference.DatasetID), ds); err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	server.WriteJSON(w, 200, ds)
}

func (s *Svc) listDatasets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := project + "/"
	items := []Dataset{}
	_ = s.db.List(bucketDatasets, prefix, func(key string, raw []byte) error {
		var d Dataset
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		items = append(items, d)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "bigquery#datasetList", "datasets": items})
}

func (s *Svc) getDataset(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	dataset := r.PathValue("dataset")
	var d Dataset
	found, err := s.db.Get(bucketDatasets, datasetKey(project, dataset), &d)
	if err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "notFound", "dataset no encontrado")
		return
	}
	server.WriteJSON(w, 200, d)
}

func (s *Svc) patchDataset(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	dataset := r.PathValue("dataset")
	var existing Dataset
	found, err := s.db.Get(bucketDatasets, datasetKey(project, dataset), &existing)
	if err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "notFound", "dataset no encontrado")
		return
	}
	var body struct {
		Labels       map[string]string `json:"labels"`
		FriendlyName string            `json:"friendlyName"`
		Description  string            `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "invalid", err.Error())
		return
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	if body.FriendlyName != "" {
		existing.FriendlyName = body.FriendlyName
	}
	if body.Description != "" {
		existing.Description = body.Description
	}
	existing.LastModifiedTime = fmt.Sprintf("%d", time.Now().UnixMilli())
	if err := s.db.Put(bucketDatasets, datasetKey(project, dataset), existing); err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Svc) deleteDataset(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	dataset := r.PathValue("dataset")
	if err := s.db.Delete(bucketDatasets, datasetKey(project, dataset)); err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Svc) createTable(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	dataset := r.PathValue("dataset")
	var body struct {
		TableReference TableReference `json:"tableReference"`
		Schema         TableSchema    `json:"schema"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "invalid", err.Error())
		return
	}
	if body.TableReference.TableID == "" {
		server.WriteError(w, 400, "invalid", "tableReference.tableId es requerido")
		return
	}
	var existingTable Table
	found, err := s.db.Get(bucketTables, tableKey(project, dataset, body.TableReference.TableID), &existingTable)
	if err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "duplicate", "tabla ya existe: "+body.TableReference.TableID)
		return
	}
	now := fmt.Sprintf("%d", time.Now().UnixMilli())
	t := Table{
		Kind: "bigquery#table",
		ID:   fmt.Sprintf("%s:%s.%s", project, dataset, body.TableReference.TableID),
		TableReference: TableReference{
			ProjectID: project,
			DatasetID: dataset,
			TableID:   body.TableReference.TableID,
		},
		Schema:           body.Schema,
		Type:             "TABLE",
		CreationTime:     now,
		LastModifiedTime: now,
		SelfLink:         fmt.Sprintf("projects/%s/datasets/%s/tables/%s", project, dataset, body.TableReference.TableID),
		Etag:             fmt.Sprintf("etag-%d", time.Now().UnixNano()),
		NumRows:          "0",
	}
	if err := s.db.Put(bucketTables, tableKey(project, dataset, t.TableReference.TableID), t); err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	server.WriteJSON(w, 200, t)
}

func (s *Svc) listTables(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	dataset := r.PathValue("dataset")
	prefix := fmt.Sprintf("%s/%s/", project, dataset)
	items := []Table{}
	_ = s.db.List(bucketTables, prefix, func(key string, raw []byte) error {
		var t Table
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		items = append(items, t)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "bigquery#tableList", "tables": items})
}

func (s *Svc) getTable(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	dataset := r.PathValue("dataset")
	table := r.PathValue("table")
	var t Table
	found, err := s.db.Get(bucketTables, tableKey(project, dataset, table), &t)
	if err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "notFound", "tabla no encontrada")
		return
	}
	server.WriteJSON(w, 200, t)
}

func (s *Svc) patchTable(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	dataset := r.PathValue("dataset")
	table := r.PathValue("table")
	var existing Table
	found, err := s.db.Get(bucketTables, tableKey(project, dataset, table), &existing)
	if err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "notFound", "tabla no encontrada")
		return
	}
	var body struct {
		Schema TableSchema `json:"schema"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "invalid", err.Error())
		return
	}
	if len(body.Schema.Fields) > 0 {
		existing.Schema = body.Schema
	}
	existing.LastModifiedTime = fmt.Sprintf("%d", time.Now().UnixMilli())
	if err := s.db.Put(bucketTables, tableKey(project, dataset, table), existing); err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Svc) deleteTable(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	dataset := r.PathValue("dataset")
	table := r.PathValue("table")
	if err := s.db.Delete(bucketTables, tableKey(project, dataset, table)); err != nil {
		server.WriteError(w, 500, "internal", err.Error())
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
