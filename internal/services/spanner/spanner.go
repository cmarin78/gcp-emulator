// Package spanner emulates a subset of the Cloud Spanner API
// (spanner.googleapis.com/v1): instances and databases. Real instances and
// databases take time to provision; this emulator resolves every mutation
// synchronously and always reports state READY, following the same
// "shape-compatible, not behavior-complete" approach used elsewhere in this
// project. Mutations return a google.longrunning.Operation, same shape as
// cloudbuild.go and memorystore.go (the real Spanner API uses this too).
package spanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/sqlengine"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketInstances = "spanner.instances"
	bucketDatabases = "spanner.databases"
	bucketSessions  = "spanner.sessions"
)

// Instance mirrors the real spanner#Instance resource (project-scoped, not
// regional: the "region" is encoded in the config name, e.g.
// regional-us-central1).
type Instance struct {
	Name            string `json:"name"`
	Config          string `json:"config"`
	DisplayName     string `json:"displayName,omitempty"`
	NodeCount       int64  `json:"nodeCount,omitempty"`
	ProcessingUnits int64  `json:"processingUnits,omitempty"`
	State           string `json:"state,omitempty"`
}

// Database mirrors the real spanner#Database resource, scoped to a parent
// instance.
type Database struct {
	Name            string   `json:"name"`
	State           string   `json:"state,omitempty"`
	CreateTime      string   `json:"createTime,omitempty"`
	DatabaseDialect string   `json:"databaseDialect,omitempty"`
	ExtraStatements []string `json:"extraStatements,omitempty"`
}

// Operation mirrors google.longrunning.Operation, same shape as
// cloudbuild.go and memorystore.go's Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type operationMetadata struct {
	Target string `json:"target"`
	Verb   string `json:"verb"`
}

type Session struct {
	Name                   string `json:"name"`
	CreateTime             string `json:"createTime"`
	ApproximateLastUseTime string `json:"approximateLastUseTime"`
}

type Service struct {
	db      *storage.DB
	seq     int64
	engines map[string]*sqlengine.Engine
	engMu   sync.RWMutex
}

func New(db *storage.DB) *Service {
	return &Service{
		db:      db,
		engines: make(map[string]*sqlengine.Engine),
	}
}

func (s *Service) getEngine(dbKey string) *sqlengine.Engine {
	s.engMu.Lock()
	defer s.engMu.Unlock()
	eng, ok := s.engines[dbKey]
	if !ok {
		eng = sqlengine.New()
		s.engines[dbKey] = eng
	}
	return eng
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/instances", s.createInstance)
	mux.HandleFunc("GET /v1/projects/{project}/instances", s.listInstances)
	mux.HandleFunc("GET /v1/projects/{project}/instances/{instance}", s.getInstance)
	mux.HandleFunc("PATCH /v1/projects/{project}/instances/{instance}", s.patchInstance)
	mux.HandleFunc("DELETE /v1/projects/{project}/instances/{instance}", s.deleteInstance)

	mux.HandleFunc("POST /v1/projects/{project}/instances/{instance}/databases", s.createDatabase)
	mux.HandleFunc("GET /v1/projects/{project}/instances/{instance}/databases", s.listDatabases)
	mux.HandleFunc("GET /v1/projects/{project}/instances/{instance}/databases/{database}", s.getDatabase)
	mux.HandleFunc("DELETE /v1/projects/{project}/instances/{instance}/databases/{database}", s.deleteDatabase)
	mux.HandleFunc("PATCH /v1/projects/{project}/instances/{instance}/databases/{database}/ddl", s.updateDatabaseDdl)

	// Spanner Data Plane (Phase 25: In-Memory SQL Engine & Sessions)
	mux.HandleFunc("POST /v1/projects/{project}/instances/{instance}/databases/{database}/sessions", s.createSession)
	mux.HandleFunc("GET /v1/projects/{project}/instances/{instance}/databases/{database}/sessions/{session}", s.getSession)
	mux.HandleFunc("DELETE /v1/projects/{project}/instances/{instance}/databases/{database}/sessions/{session}", s.deleteSession)
	mux.HandleFunc("POST /v1/projects/{project}/instances/{instance}/databases/{database}/sessions/{sessionAction}", s.sessionAction)
	mux.HandleFunc("POST /v1/projects/{project}/instances/{instance}/databases/{databaseAction}", s.databaseAction)

	mux.HandleFunc("GET /v1/projects/{project}/instances/{instance}/operations/{operation}", s.getOperation)
}

func instanceKey(project, instance string) string { return project + "/" + instance }
func instanceName(project, instance string) string {
	return fmt.Sprintf("projects/%s/instances/%s", project, instance)
}

func databaseKey(project, instance, database string) string {
	return fmt.Sprintf("%s/%s/%s", project, instance, database)
}
func databaseName(project, instance, database string) string {
	return fmt.Sprintf("projects/%s/instances/%s/databases/%s", project, instance, database)
}

func (s *Service) nextID() int64 {
	return atomic.AddInt64(&s.seq, 1)
}

func (s *Service) writeOperation(w http.ResponseWriter, project, instance, verb, target string, payload any) {
	meta, _ := json.Marshal(operationMetadata{Target: target, Verb: verb})
	resp, _ := json.Marshal(payload)
	op := Operation{
		Name:     fmt.Sprintf("projects/%s/instances/%s/operations/op-%d", project, instance, s.nextID()),
		Done:     true,
		Metadata: meta,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		InstanceID string   `json:"instanceId"`
		Instance   Instance `json:"instance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.InstanceID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "instanceId is required")
		return
	}
	var existingInst Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, body.InstanceID), &existingInst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "instance already exists: "+body.InstanceID)
		return
	}
	inst := Instance{
		Name:            instanceName(project, body.InstanceID),
		Config:          orDefault(body.Instance.Config, "regional-us-central1"),
		DisplayName:     orDefault(body.Instance.DisplayName, body.InstanceID),
		NodeCount:       body.Instance.NodeCount,
		ProcessingUnits: orDefaultInt(body.Instance.ProcessingUnits, 100),
		State:           "READY",
	}
	if err := s.db.Put(bucketInstances, instanceKey(project, body.InstanceID), inst); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, body.InstanceID, "create", inst.Name, inst)
}

func (s *Service) listInstances(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := project + "/"
	items := []Instance{}
	_ = s.db.List(bucketInstances, prefix, func(key string, raw []byte) error {
		var inst Instance
		if err := json.Unmarshal(raw, &inst); err != nil {
			return err
		}
		items = append(items, inst)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"instances": items})
}

func (s *Service) getInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	var inst Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, instance), &inst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found")
		return
	}
	server.WriteJSON(w, 200, inst)
}

func (s *Service) patchInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	var existing Instance
	found, err := s.db.Get(bucketInstances, instanceKey(project, instance), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance not found")
		return
	}
	var body struct {
		Instance Instance `json:"instance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Instance.DisplayName != "" {
		existing.DisplayName = body.Instance.DisplayName
	}
	if body.Instance.NodeCount != 0 {
		existing.NodeCount = body.Instance.NodeCount
	}
	if body.Instance.ProcessingUnits != 0 {
		existing.ProcessingUnits = body.Instance.ProcessingUnits
	}
	if err := s.db.Put(bucketInstances, instanceKey(project, instance), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, instance, "update", existing.Name, existing)
}

func (s *Service) deleteInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	if err := s.db.Delete(bucketInstances, instanceKey(project, instance)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Service) createDatabase(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	var body struct {
		CreateStatement string   `json:"createStatement"`
		ExtraStatements []string `json:"extraStatements"`
		DatabaseDialect string   `json:"databaseDialect"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	name := parseDatabaseNameFromCreateStatement(body.CreateStatement)
	if name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "createStatement must be of the form 'CREATE DATABASE <name>'")
		return
	}
	var existingDB Database
	found, err := s.db.Get(bucketDatabases, databaseKey(project, instance, name), &existingDB)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "database already exists: "+name)
		return
	}
	dbRes := Database{
		Name:            databaseName(project, instance, name),
		State:           "READY",
		CreateTime:      time.Now().UTC().Format(time.RFC3339),
		DatabaseDialect: orDefault(body.DatabaseDialect, "GOOGLE_STANDARD_SQL"),
		ExtraStatements: body.ExtraStatements,
	}
	if err := s.db.Put(bucketDatabases, databaseKey(project, instance, name), dbRes); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	eng := s.getEngine(databaseKey(project, instance, name))
	for _, stmt := range body.ExtraStatements {
		_, _ = eng.Execute(stmt)
	}
	s.writeOperation(w, project, instance, "create_database", dbRes.Name, dbRes)
}

func (s *Service) listDatabases(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	prefix := fmt.Sprintf("%s/%s/", project, instance)
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

func (s *Service) getDatabase(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	database := r.PathValue("database")
	var d Database
	found, err := s.db.Get(bucketDatabases, databaseKey(project, instance, database), &d)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "database not found")
		return
	}
	server.WriteJSON(w, 200, d)
}

func (s *Service) deleteDatabase(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	database := r.PathValue("database")
	dbKey := databaseKey(project, instance, database)
	if err := s.db.Delete(bucketDatabases, dbKey); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.engMu.Lock()
	delete(s.engines, dbKey)
	s.engMu.Unlock()
	server.WriteJSON(w, 200, map[string]any{})
}

// updateDatabaseDdl handles UpdateDatabaseDdl (databaseadmin.projects.
// instances.databases.updateDdl).
func (s *Service) updateDatabaseDdl(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	database := r.PathValue("database")
	var d Database
	found, err := s.db.Get(bucketDatabases, databaseKey(project, instance, database), &d)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "database not found")
		return
	}
	var body struct {
		Statements []string `json:"statements"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	d.ExtraStatements = append(d.ExtraStatements, body.Statements...)
	if err := s.db.Put(bucketDatabases, databaseKey(project, instance, database), d); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	eng := s.getEngine(databaseKey(project, instance, database))
	for _, stmt := range body.Statements {
		_, _ = eng.Execute(stmt)
	}

	s.writeOperation(w, project, instance, "update_database_ddl", d.Name, map[string]any{})
}

// getOperation always reports the operation as done, since every mutation
// above already resolves synchronously and returns done:true. This handler
// exists only so gcloud/Terraform's polling code path gets a valid response
// if it chooses to poll anyway.
func (s *Service) getOperation(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	opName := r.PathValue("operation")
	op := Operation{
		Name: fmt.Sprintf("projects/%s/instances/%s/operations/%s", project, instance, opName),
		Done: true,
	}
	server.WriteJSON(w, 200, op)
}

// parseDatabaseNameFromCreateStatement extracts <name> from a real Spanner
// "CREATE DATABASE <name>" DDL statement, same syntax Terraform/gcloud send.
func parseDatabaseNameFromCreateStatement(stmt string) string {
	const prefix = "CREATE DATABASE "
	upper := strings.ToUpper(stmt)
	idx := strings.Index(upper, prefix)
	if idx == -1 {
		return ""
	}
	rest := strings.TrimSpace(stmt[idx+len(prefix):])
	return strings.Trim(rest, "`")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDefaultInt(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}

// ── Spanner Data Plane (Phase 25: Sessions & In-Memory SQL Execution) ──────────

func sessionKey(project, instance, database, session string) string {
	return fmt.Sprintf("%s/%s/%s/%s", project, instance, database, session)
}

func sessionName(project, instance, database, session string) string {
	return fmt.Sprintf("projects/%s/instances/%s/databases/%s/sessions/%s", project, instance, database, session)
}

func (s *Service) createSession(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	database := r.PathValue("database")

	var d Database
	found, err := s.db.Get(bucketDatabases, databaseKey(project, instance, database), &d)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "database not found")
		return
	}

	sessionID := fmt.Sprintf("session-%d", atomic.AddInt64(&s.seq, 1))
	now := time.Now().UTC().Format(time.RFC3339)
	sess := Session{
		Name:                   sessionName(project, instance, database, sessionID),
		CreateTime:             now,
		ApproximateLastUseTime: now,
	}
	_ = s.db.Put(bucketSessions, sessionKey(project, instance, database, sessionID), sess)
	server.WriteJSON(w, 200, sess)
}

func (s *Service) getSession(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	database := r.PathValue("database")
	sessionID := r.PathValue("session")

	var sess Session
	found, err := s.db.Get(bucketSessions, sessionKey(project, instance, database, sessionID), &sess)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "session not found")
		return
	}
	server.WriteJSON(w, 200, sess)
}

func (s *Service) deleteSession(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	database := r.PathValue("database")
	sessionID := r.PathValue("session")

	_ = s.db.Delete(bucketSessions, sessionKey(project, instance, database, sessionID))
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Service) databaseAction(w http.ResponseWriter, r *http.Request) {
	dbName, action, ok := strings.Cut(r.PathValue("databaseAction"), ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "not found")
		return
	}
	if action == "executeSql" {
		s.handleExecuteSql(w, r, dbName)
		return
	}
	server.WriteError(w, 404, "NOT_FOUND", "unknown action: "+action)
}

func (s *Service) sessionAction(w http.ResponseWriter, r *http.Request) {
	database := r.PathValue("database")
	_, action, ok := strings.Cut(r.PathValue("sessionAction"), ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "not found")
		return
	}
	switch action {
	case "executeSql":
		s.handleExecuteSql(w, r, database)
	case "executeBatchDml":
		s.handleExecuteBatchDml(w, r, database)
	case "beginTransaction":
		server.WriteJSON(w, 200, map[string]any{"id": fmt.Sprintf("tx-%d", time.Now().UnixNano())})
	case "commit":
		server.WriteJSON(w, 200, map[string]any{"commitTimestamp": time.Now().UTC().Format(time.RFC3339)})
	case "rollback":
		server.WriteJSON(w, 200, map[string]any{})
	default:
		server.WriteError(w, 404, "NOT_FOUND", "unknown action: "+action)
	}
}

func (s *Service) handleExecuteSql(w http.ResponseWriter, r *http.Request, database string) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	dbKey := databaseKey(project, instance, database)

	var d Database
	found, err := s.db.Get(bucketDatabases, dbKey, &d)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "database not found")
		return
	}

	var body struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if strings.TrimSpace(body.SQL) == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "sql is required")
		return
	}

	eng := s.getEngine(dbKey)
	trimmed := strings.TrimSpace(body.SQL)
	isSelect := strings.HasPrefix(strings.ToUpper(trimmed), "SELECT")

	if isSelect {
		qr, err := eng.Query(body.SQL)
		if err != nil {
			server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
			return
		}
		var fields []map[string]any
		for _, col := range qr.Columns {
			code := "STRING"
			if col.Type != "" {
				code = col.Type
			}
			fields = append(fields, map[string]any{
				"name": col.Name,
				"type": map[string]string{"code": code},
			})
		}
		wireRows := make([][]any, len(qr.Rows))
		for i, row := range qr.Rows {
			rowVals := make([]any, len(row))
			for j, v := range row {
				if v == nil {
					rowVals[j] = nil
				} else {
					rowVals[j] = fmt.Sprintf("%v", v)
				}
			}
			wireRows[i] = rowVals
		}

		server.WriteJSON(w, 200, map[string]any{
			"metadata": map[string]any{
				"rowType": map[string]any{
					"fields": fields,
				},
			},
			"rows": wireRows,
		})
		return
	}

	res, err := eng.Execute(body.SQL)
	if err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}

	server.WriteJSON(w, 200, map[string]any{
		"metadata": map[string]any{
			"rowType": map[string]any{
				"fields": []any{},
			},
		},
		"stats": map[string]any{
			"rowCountExact": fmt.Sprintf("%d", res.RowsAffected),
		},
	})
}

func (s *Service) handleExecuteBatchDml(w http.ResponseWriter, r *http.Request, database string) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	dbKey := databaseKey(project, instance, database)

	var body struct {
		Statements []struct {
			SQL string `json:"sql"`
		} `json:"statements"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}

	eng := s.getEngine(dbKey)
	var resultSets []map[string]any
	for _, stmt := range body.Statements {
		res, err := eng.Execute(stmt.SQL)
		if err != nil {
			server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
			return
		}
		resultSets = append(resultSets, map[string]any{
			"stats": map[string]any{
				"rowCountExact": fmt.Sprintf("%d", res.RowsAffected),
			},
		})
	}

	server.WriteJSON(w, 200, map[string]any{
		"resultSets": resultSets,
		"status":     map[string]any{"code": 0},
	})
}
