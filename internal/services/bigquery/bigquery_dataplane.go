// Fase 17: BigQuery data plane.
// tabledata.insertAll, tabledata.list, jobs.insert, jobs.query (synchronous).
// El emulador no ejecuta SQL real — soporta "SELECT * FROM dataset.table
// [LIMIT N]" escaneando las filas almacenadas por insertAll.
package bigquery

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/sqlengine"
)

const (
	bucketTableData = "bigquery.tabledata"
	bucketJobs      = "bigquery.jobs"
)

// ── Job types ─────────────────────────────────────────────────────────────────

type Job struct {
	Kind          string       `json:"kind"`
	ID            string       `json:"id"`
	JobReference  JobReference `json:"jobReference"`
	Configuration JobConfig    `json:"configuration"`
	Status        JobStatus    `json:"status"`
	Statistics    JobStats     `json:"statistics"`
}

type JobReference struct {
	ProjectID string `json:"projectId"`
	JobID     string `json:"jobId"`
}

type JobConfig struct {
	JobType string       `json:"jobType"`
	Query   *QueryConfig `json:"query,omitempty"`
}

type QueryConfig struct {
	Query          string            `json:"query"`
	DefaultDataset *DatasetReference `json:"defaultDataset,omitempty"`
	UseLegacySQL   bool              `json:"useLegacySql"`
}

type JobStatus struct {
	State string `json:"state"` // PENDING | RUNNING | DONE
}

type JobStats struct {
	CreationTime string `json:"creationTime"`
	EndTime      string `json:"endTime"`
}

// QueryResultRow represents a BQ row in the wire format: { "f": [ {"v": ...} ] }
type QueryResultRow struct {
	F []QueryResultCell `json:"f"`
}

type QueryResultCell struct {
	V any `json:"v"`
}

// ── Route registration ─────────────────────────────────────────────────────────

func (s *Svc) registerDataPlane(mux *http.ServeMux) {
	// tabledata
	mux.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/insertAll", s.tabledataInsertAll)
	mux.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/data", s.tabledataList)

	// jobs — async insert + get
	mux.HandleFunc("POST /bigquery/v2/projects/{project}/jobs", s.jobsInsert)
	mux.HandleFunc("GET /bigquery/v2/projects/{project}/jobs/{job}", s.jobsGet)

	// queries — synchronous query + get results
	mux.HandleFunc("POST /bigquery/v2/projects/{project}/queries", s.jobsQuery)
	mux.HandleFunc("GET /bigquery/v2/projects/{project}/queries/{job}", s.jobsGetQueryResults)
}

// ── tabledata.insertAll ────────────────────────────────────────────────────────

func (s *Svc) tabledataInsertAll(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	dataset := r.PathValue("dataset")
	table := r.PathValue("table")

	// Verify table exists.
	var t Table
	if found, _ := s.db.Get(bucketTables, tableKey(project, dataset, table), &t); !found {
		server.WriteError(w, 404, "notFound", "tabla no encontrada")
		return
	}

	var body struct {
		Rows []struct {
			InsertID string          `json:"insertId"`
			JSON     json.RawMessage `json:"json"`
		} `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "invalid", "invalid JSON: "+err.Error())
		return
	}

	// Load existing rows.
	rows := s.loadRows(project, dataset, table)
	for _, row := range body.Rows {
		if len(row.JSON) > 0 {
			rows = append(rows, row.JSON)
		}
	}
	if err := s.saveRows(project, dataset, table, rows); err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}

	// Update NumRows on the table.
	t.NumRows = strconv.Itoa(len(rows))
	_ = s.db.Put(bucketTables, tableKey(project, dataset, table), t)

	server.WriteJSON(w, 200, map[string]any{
		"kind":         "bigquery#tableDataInsertAllResponse",
		"insertErrors": []any{},
	})
}

// ── tabledata.list ─────────────────────────────────────────────────────────────

func (s *Svc) tabledataList(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	dataset := r.PathValue("dataset")
	table := r.PathValue("table")

	// Verify table exists.
	var t Table
	if found, _ := s.db.Get(bucketTables, tableKey(project, dataset, table), &t); !found {
		server.WriteError(w, 404, "notFound", "tabla no encontrada")
		return
	}

	rows := s.loadRows(project, dataset, table)
	resultRows := rowsToWireFormat(rows, t.Schema)

	server.WriteJSON(w, 200, map[string]any{
		"kind":      "bigquery#tableDataList",
		"totalRows": strconv.Itoa(len(rows)),
		"rows":      resultRows,
	})
}

// ── jobs.insert (async query job) ─────────────────────────────────────────────

func (s *Svc) jobsInsert(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")

	var body struct {
		Configuration JobConfig    `json:"configuration"`
		JobReference  JobReference `json:"jobReference"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "invalid", "invalid JSON: "+err.Error())
		return
	}

	jobID := body.JobReference.JobID
	if jobID == "" {
		jobID = fmt.Sprintf("emulator-job-%d", time.Now().UnixNano())
	}

	now := fmt.Sprintf("%d", time.Now().UnixMilli())
	job := Job{
		Kind: "bigquery#job",
		ID:   fmt.Sprintf("%s:%s", project, jobID),
		JobReference: JobReference{
			ProjectID: project,
			JobID:     jobID,
		},
		Configuration: body.Configuration,
		Status:        JobStatus{State: "DONE"},
		Statistics:    JobStats{CreationTime: now, EndTime: now},
	}
	if job.Configuration.JobType == "" {
		job.Configuration.JobType = "QUERY"
	}

	// Execute the query if present and store results.
	if body.Configuration.Query != nil {
		qcfg := body.Configuration.Query
		defaultDS := ""
		if qcfg.DefaultDataset != nil {
			defaultDS = qcfg.DefaultDataset.DatasetID
		}
		rows, schema, _ := s.runSimpleQuery(project, qcfg.Query, defaultDS)
		_ = s.saveJobResults(project, jobID, rows, schema)
	}

	if err := s.db.Put(bucketJobs, fmt.Sprintf("%s/%s", project, jobID), job); err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	server.WriteJSON(w, 200, job)
}

// ── jobs.get ──────────────────────────────────────────────────────────────────

func (s *Svc) jobsGet(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	jobID := r.PathValue("job")

	var job Job
	found, err := s.db.Get(bucketJobs, fmt.Sprintf("%s/%s", project, jobID), &job)
	if err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "notFound", "job no encontrado")
		return
	}
	server.WriteJSON(w, 200, job)
}

// ── jobs.query (synchronous) ──────────────────────────────────────────────────

func (s *Svc) jobsQuery(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")

	var body struct {
		Query          string            `json:"query"`
		DefaultDataset *DatasetReference `json:"defaultDataset"`
		TimeoutMs      int               `json:"timeoutMs"`
		UseLegacySQL   bool              `json:"useLegacySql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "invalid", "invalid JSON: "+err.Error())
		return
	}
	if body.Query == "" {
		server.WriteError(w, 400, "invalid", "query is required")
		return
	}

	defaultDS := ""
	if body.DefaultDataset != nil {
		defaultDS = body.DefaultDataset.DatasetID
	}

	rows, schema, err := s.runSimpleQuery(project, body.Query, defaultDS)
	if err != nil {
		server.WriteError(w, 400, "invalid", err.Error())
		return
	}

	jobID := fmt.Sprintf("emulator-query-%d", time.Now().UnixNano())
	now := fmt.Sprintf("%d", time.Now().UnixMilli())

	resultRows := rowsToWireFormat(rows, schema)

	server.WriteJSON(w, 200, map[string]any{
		"kind":        "bigquery#queryResponse",
		"jobComplete": true,
		"jobReference": JobReference{
			ProjectID: project,
			JobID:     jobID,
		},
		"totalRows": strconv.Itoa(len(rows)),
		"schema":    schema,
		"rows":      resultRows,
		"cacheHit":  false,
		"creationTime": now,
	})
}

// ── jobs.getQueryResults ──────────────────────────────────────────────────────

func (s *Svc) jobsGetQueryResults(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	jobID := r.PathValue("job")

	rows, schema, found := s.loadJobResults(project, jobID)
	if !found {
		// Try to find the job itself.
		var job Job
		if jobFound, _ := s.db.Get(bucketJobs, fmt.Sprintf("%s/%s", project, jobID), &job); !jobFound {
			server.WriteError(w, 404, "notFound", "job no encontrado")
			return
		}
		// Job exists but no results (e.g., DDL job).
		rows = []json.RawMessage{}
		schema = TableSchema{}
	}

	resultRows := rowsToWireFormat(rows, schema)
	server.WriteJSON(w, 200, map[string]any{
		"kind":        "bigquery#getQueryResultsResponse",
		"jobComplete": true,
		"jobReference": JobReference{
			ProjectID: project,
			JobID:     jobID,
		},
		"totalRows": strconv.Itoa(len(rows)),
		"schema":    schema,
		"rows":      resultRows,
	})
}

// ── Storage helpers ───────────────────────────────────────────────────────────

type storedTableData struct {
	Rows []json.RawMessage `json:"rows"`
}

func (s *Svc) loadRows(project, dataset, table string) []json.RawMessage {
	var stored storedTableData
	s.db.Get(bucketTableData, tableKey(project, dataset, table), &stored) //nolint
	if stored.Rows == nil {
		return []json.RawMessage{}
	}
	return stored.Rows
}

func (s *Svc) saveRows(project, dataset, table string, rows []json.RawMessage) error {
	return s.db.Put(bucketTableData, tableKey(project, dataset, table), storedTableData{Rows: rows})
}

type storedJobResults struct {
	Rows   []json.RawMessage `json:"rows"`
	Schema TableSchema       `json:"schema"`
}

func (s *Svc) saveJobResults(project, jobID string, rows []json.RawMessage, schema TableSchema) error {
	return s.db.Put(bucketJobs+".results", fmt.Sprintf("%s/%s", project, jobID), storedJobResults{Rows: rows, Schema: schema})
}

func (s *Svc) loadJobResults(project, jobID string) ([]json.RawMessage, TableSchema, bool) {
	var stored storedJobResults
	found, _ := s.db.Get(bucketJobs+".results", fmt.Sprintf("%s/%s", project, jobID), &stored)
	return stored.Rows, stored.Schema, found
}

// ── Query execution ───────────────────────────────────────────────────────────

// selectAllRe matches: SELECT ... FROM [project:]dataset.table [LIMIT N] [;]
// Case-insensitive, whitespace-flexible.
var selectAllRe = regexp.MustCompile(
	`(?i)^\s*SELECT\s+[\s\S]+?\s+FROM\s+` +
		`(?:[a-zA-Z0-9_\-]+:)?` + // optional project:
		`([a-zA-Z0-9_\-]+)\.([a-zA-Z0-9_\-]+)` + // dataset.table
		`(?:\s+LIMIT\s+(\d+))?` +
		`\s*;?\s*$`,
)

func (s *Svc) runSimpleQuery(project, query, defaultDataset string) ([]json.RawMessage, TableSchema, error) {
	// 1. Attempt in-memory execution via sqlengine (Phase 25)
	if rows, schema, err := s.runEngineQuery(project, query, defaultDataset); err == nil {
		return rows, schema, nil
	}

	// 2. Fallback to basic SELECT * scan
	matches := selectAllRe.FindStringSubmatch(strings.TrimSpace(query))
	if matches == nil {
		return nil, TableSchema{}, fmt.Errorf("emulator query execution error: %s", query)
	}
	dataset := matches[1]
	tableName := matches[2]
	limitStr := matches[3]

	// If caller used table-only (no dataset.table) form, fall back to defaultDataset.
	if dataset == "" {
		dataset = defaultDataset
	}

	// Verify table exists.
	var t Table
	if found, _ := s.db.Get(bucketTables, tableKey(project, dataset, tableName), &t); !found {
		return nil, TableSchema{}, fmt.Errorf("table %s.%s not found", dataset, tableName)
	}

	rows := s.loadRows(project, dataset, tableName)

	if limitStr != "" {
		limit, _ := strconv.Atoi(limitStr)
		if limit < len(rows) {
			rows = rows[:limit]
		}
	}

	return rows, t.Schema, nil
}

func (s *Svc) runEngineQuery(project, query, defaultDataset string) ([]json.RawMessage, TableSchema, error) {
	parsed, err := sqlengine.Parse(query)
	if err != nil {
		return nil, TableSchema{}, err
	}
	stmt, ok := parsed.(*sqlengine.SelectStmt)
	if !ok {
		return nil, TableSchema{}, fmt.Errorf("not a SELECT query")
	}

	// Table name in BQ can be `[project:]dataset.table` or `dataset.table` or `table`
	tblRaw := stmt.Table
	tblRaw = strings.Trim(tblRaw, "`\"")
	if idx := strings.Index(tblRaw, ":"); idx >= 0 {
		tblRaw = tblRaw[idx+1:]
	}

	var dataset, tableName string
	if parts := strings.Split(tblRaw, "."); len(parts) >= 2 {
		dataset = parts[0]
		tableName = parts[1]
	} else {
		dataset = defaultDataset
		tableName = tblRaw
	}

	if dataset == "" || tableName == "" {
		return nil, TableSchema{}, fmt.Errorf("could not determine dataset and table")
	}

	var t Table
	if found, _ := s.db.Get(bucketTables, tableKey(project, dataset, tableName), &t); !found {
		return nil, TableSchema{}, fmt.Errorf("table %s.%s not found", dataset, tableName)
	}

	rawRows := s.loadRows(project, dataset, tableName)
	eng := sqlengine.New()

	var cols []sqlengine.Column
	for _, f := range t.Schema.Fields {
		cols = append(cols, sqlengine.Column{Name: f.Name, Type: f.Type})
	}
	_ = eng.CreateTable(stmt.Table, cols)

	var parsedRows []map[string]any
	for _, r := range rawRows {
		var m map[string]any
		if err := json.Unmarshal(r, &m); err == nil {
			parsedRows = append(parsedRows, m)
		}
	}
	_ = eng.InsertRows(stmt.Table, parsedRows)

	qr, err := eng.Query(query)
	if err != nil {
		return nil, TableSchema{}, err
	}

	// Build output schema
	var outSchema TableSchema
	for _, c := range qr.Columns {
		bType := "STRING"
		if c.Type != "" {
			bType = c.Type
		}
		outSchema.Fields = append(outSchema.Fields, TableFieldSchema{
			Name: c.Name,
			Type: bType,
		})
	}

	// Build output rows
	maps := qr.ToMaps()
	result := make([]json.RawMessage, len(maps))
	for i, m := range maps {
		b, _ := json.Marshal(m)
		result[i] = b
	}

	return result, outSchema, nil
}

// ── Wire-format helpers ───────────────────────────────────────────────────────

// rowsToWireFormat converts stored JSON rows to BQ wire format
// { "f": [ { "v": value } ... ] } using the schema field order.
// If schema has no fields, uses sorted key order from the first row.
func rowsToWireFormat(rows []json.RawMessage, schema TableSchema) []QueryResultRow {
	if len(rows) == 0 {
		return []QueryResultRow{}
	}

	// Determine field order.
	var fieldNames []string
	if len(schema.Fields) > 0 {
		for _, f := range schema.Fields {
			fieldNames = append(fieldNames, f.Name)
		}
	} else {
		// Infer from first row.
		var first map[string]json.RawMessage
		if err := json.Unmarshal(rows[0], &first); err == nil {
			for k := range first {
				fieldNames = append(fieldNames, k)
			}
			sort.Strings(fieldNames)
		}
	}

	result := make([]QueryResultRow, 0, len(rows))
	for _, raw := range rows {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			continue
		}
		row := QueryResultRow{F: make([]QueryResultCell, 0, len(fieldNames))}
		for _, name := range fieldNames {
			val := obj[name]
			var v any
			_ = json.Unmarshal(val, &v)
			row.F = append(row.F, QueryResultCell{V: v})
		}
		result = append(result, row)
	}
	return result
}
