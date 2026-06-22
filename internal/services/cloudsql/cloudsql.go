// Package cloudsql emula un subconjunto de la API de Cloud SQL Admin
// (sqladmin.googleapis.com/sql/v1beta4): instancias, bases de datos y
// usuarios. Las mutaciones sobre instancias y bases de datos devuelven un
// recurso "Operation" (siempre resuelto, status=DONE), igual que la API
// real, para compatibilidad con clientes que hacen polling.
package cloudsql

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cesar/gcp-emulator/internal/activity"
	"github.com/cesar/gcp-emulator/internal/realbackend"
	"github.com/cesar/gcp-emulator/internal/realbackend/postgres"
	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketInstances = "cloudsql.instances"
	bucketDatabases = "cloudsql.databases"
	bucketUsers     = "cloudsql.users"
	bucketOps       = "cloudsql.operations"
)

// Settings replica el subconjunto relevante de sqladmin.Settings.
type Settings struct {
	Tier             string            `json:"tier"`
	DataDiskSizeGb   string            `json:"dataDiskSizeGb,omitempty"`
	DataDiskType     string            `json:"dataDiskType,omitempty"`
	ActivationPolicy string            `json:"activationPolicy,omitempty"`
	AvailabilityType string            `json:"availabilityType,omitempty"`
	UserLabels       map[string]string `json:"userLabels,omitempty"`
}

// RealConnection is an emulator-only extension — not part of the real
// sqladmin API. When the instance opted into real execution
// (internal/realbackend.WantsReal, checked against settings.userLabels)
// and a real embedded Postgres backend was actually admitted (Phase 13),
// this field is populated with everything needed to connect to it with a
// real Postgres driver and run real queries. It's omitted from JSON for
// every shape-only instance, which remains the default for every
// existing caller (gcloud, Terraform, the pre-Phase-13 test suites).
type RealConnection struct {
	Backend  string `json:"backend"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// IPMapping replica sqladmin.IpMapping.
type IPMapping struct {
	Type   string `json:"type"`
	IPAddr string `json:"ipAddress"`
}

// DatabaseInstance replica sqladmin.DatabaseInstance.
type DatabaseInstance struct {
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	Project         string          `json:"project"`
	Region          string          `json:"region"`
	DatabaseVersion string          `json:"databaseVersion"`
	Settings        Settings        `json:"settings"`
	State           string          `json:"state"`
	ConnectionName  string          `json:"connectionName"`
	IPAddresses     []IPMapping     `json:"ipAddresses"`
	SelfLink        string          `json:"selfLink"`
	InstanceType    string          `json:"instanceType,omitempty"`
	RealConnection  *RealConnection `json:"realConnection,omitempty"`
}

// Database replica sqladmin.Database.
type Database struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Project   string `json:"project"`
	Instance  string `json:"instance"`
	Charset   string `json:"charset,omitempty"`
	Collation string `json:"collation,omitempty"`
	SelfLink  string `json:"selfLink"`
}

// User replica sqladmin.User.
type User struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Project  string `json:"project"`
	Instance string `json:"instance"`
	Host     string `json:"host,omitempty"`
	Password string `json:"password,omitempty"`
}

// Operation replica sqladmin.Operation.
type Operation struct {
	Kind          string `json:"kind"`
	TargetLink    string `json:"targetLink,omitempty"`
	Status        string `json:"status"`
	User          string `json:"user,omitempty"`
	InsertTime    string `json:"insertTime,omitempty"`
	StartTime     string `json:"startTime,omitempty"`
	EndTime       string `json:"endTime,omitempty"`
	OperationType string `json:"operationType"`
	Name          string `json:"name"`
	TargetID      string `json:"targetId,omitempty"`
	SelfLink      string `json:"selfLink,omitempty"`
	TargetProject string `json:"targetProject,omitempty"`
}

type Svc struct {
	db  *storage.DB
	seq int64

	// gov/pgConns back Phase 13's real-backend opt-in. gov is the shared
	// realbackend.Governor (nil in tests that don't care about real
	// execution, in which case opt-in requests silently stay shape-only —
	// the same documented fallback every Phase 11/12 boundary uses).
	// pgConns maps a governor ID to the live engine handle so
	// createDatabase/createUser/deleteDatabase/deleteUser know which real
	// engine to route a statement to; it's kept in sync with the Governor
	// via SetOnEvict so an evicted/released backend is never used after
	// it was stopped.
	mu      sync.Mutex
	gov     *realbackend.Governor
	pgConns map[string]*postgres.Backend
}

// New creates a Cloud SQL service. gov may be nil (e.g. in tests that
// don't exercise Phase 13's real-execution opt-in); a nil Governor simply
// means every instance stays shape-only regardless of opt-in headers,
// the same zero-cost-by-default behavior as before Phase 13.
func New(db *storage.DB, gov *realbackend.Governor) *Svc {
	s := &Svc{db: db, gov: gov, pgConns: map[string]*postgres.Backend{}}
	if gov != nil {
		gov.SetOnEvict(s.forgetReal)
	}
	return s
}

func (s *Svc) forgetReal(id string) {
	s.mu.Lock()
	delete(s.pgConns, id)
	s.mu.Unlock()
}

func realBackendID(project, instance string) string {
	return "cloudsql:" + instanceKey(project, instance)
}

func (s *Svc) realBackendFor(project, instance string) *postgres.Backend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pgConns[realBackendID(project, instance)]
}

// tryStartReal attempts to back inst with a real, embedded Postgres
// engine when the caller opted in (realbackend.WantsReal, checked
// against settings.userLabels) and the instance is a Postgres flavor —
// the only engine this emulator can run without Docker (see ROADMAP.md's
// "Real-execution: committed scope"). On any failure — no Governor wired,
// a budget refusal, or the embedded engine failing to start (e.g. no
// network on its first run, since it needs to download a real postgres
// binary once) — this logs and leaves inst exactly as shape-only as it
// would have been before Phase 13. Same documented-fallback pattern as
// Phase 11's Workflows interpreter (non-JSON sourceContents) and Cloud
// Armor's CEL boundary.
func (s *Svc) tryStartReal(r *http.Request, project string, inst *DatabaseInstance) {
	if s.gov == nil {
		return
	}
	if !realbackend.WantsReal(r, inst.Settings.UserLabels) {
		return
	}
	if !strings.HasPrefix(strings.ToUpper(inst.DatabaseVersion), "POSTGRES") {
		log.Printf("cloudsql: %s/%s pidió backend real pero databaseVersion=%s no es Postgres, sigue shape-only", project, inst.Name, inst.DatabaseVersion)
		return
	}
	password := randomPassword()
	backend, err := postgres.Start(password)
	if err != nil {
		log.Printf("cloudsql: %s/%s pidió backend real pero no se pudo iniciar Postgres embebido, sigue shape-only: %v", project, inst.Name, err)
		return
	}
	id := realBackendID(project, inst.Name)
	admitted, evicted := s.gov.Admit(id, backend)
	if !admitted {
		log.Printf("cloudsql: %s/%s pidió backend real pero el Governor lo rechazó (budget), sigue shape-only", project, inst.Name)
		_ = backend.Stop()
		return
	}
	for _, evID := range evicted {
		log.Printf("cloudsql: backend real %q desalojado (LRU) para liberar espacio para %q", evID, id)
	}
	s.mu.Lock()
	s.pgConns[id] = backend
	s.mu.Unlock()
	inst.RealConnection = &RealConnection{
		Backend:  backend.Kind(),
		Host:     backend.Host(),
		Port:     backend.Port(),
		User:     "postgres",
		Password: password,
	}
	// Phase 15: a real backend coming up is real activity, surfaced
	// through Cloud Logging the same way every Phase 11 real dispatch
	// already is (RecordLog alongside the producer-side log.Printf
	// above).
	activity.RecordLog(project, activity.LogEntry{
		LogName:     fmt.Sprintf("projects/%s/logs/cloudsql.googleapis.com%%2Freal_backend", project),
		Severity:    "INFO",
		TextPayload: fmt.Sprintf("instancia %s: backend Postgres real iniciado en %s:%d", inst.Name, backend.Host(), backend.Port()),
		Resource:    map[string]any{"type": "cloudsql_database", "labels": map[string]string{"database_id": project + ":" + inst.Name}},
	})
}

func randomPassword() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// quoteIdent quotes a Postgres identifier (database/role name) for use in
// a DDL statement built by this package. This is a local, single-tenant
// dev engine reachable only from this same machine, not a multi-tenant
// server exposed to untrusted input — the bar here is correctness against
// the names gcloud/Terraform actually send, not hardening against a
// malicious caller.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteLiteral quotes a Postgres string literal (e.g. a password) for use
// in a DDL statement built by this package.
func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

// Register monta las rutas de Cloud SQL Admin, siguiendo los paths reales
// de sqladmin.googleapis.com/sql/v1beta4. A diferencia de Cloud Run/Functions,
// este base path (/sql/v1beta4) no es compartido con ningún otro servicio,
// así que las operaciones se registran directamente aquí.
func (s *Svc) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /sql/v1beta4/projects/{project}/instances", s.createInstance)
	mux.HandleFunc("GET /sql/v1beta4/projects/{project}/instances", s.listInstances)
	mux.HandleFunc("GET /sql/v1beta4/projects/{project}/instances/{instance}", s.getInstance)
	mux.HandleFunc("PATCH /sql/v1beta4/projects/{project}/instances/{instance}", s.patchInstance)
	mux.HandleFunc("DELETE /sql/v1beta4/projects/{project}/instances/{instance}", s.deleteInstance)

	mux.HandleFunc("POST /sql/v1beta4/projects/{project}/instances/{instance}/databases", s.createDatabase)
	mux.HandleFunc("GET /sql/v1beta4/projects/{project}/instances/{instance}/databases", s.listDatabases)
	mux.HandleFunc("GET /sql/v1beta4/projects/{project}/instances/{instance}/databases/{database}", s.getDatabase)
	mux.HandleFunc("PATCH /sql/v1beta4/projects/{project}/instances/{instance}/databases/{database}", s.patchDatabase)
	mux.HandleFunc("DELETE /sql/v1beta4/projects/{project}/instances/{instance}/databases/{database}", s.deleteDatabase)

	mux.HandleFunc("POST /sql/v1beta4/projects/{project}/instances/{instance}/users", s.createUser)
	mux.HandleFunc("GET /sql/v1beta4/projects/{project}/instances/{instance}/users", s.listUsers)
	mux.HandleFunc("DELETE /sql/v1beta4/projects/{project}/instances/{instance}/users", s.deleteUser)

	mux.HandleFunc("GET /sql/v1beta4/projects/{project}/operations/{operation}", s.getOperation)
}

func instanceKey(project, instance string) string {
	return fmt.Sprintf("%s/%s", project, instance)
}

func databaseKey(project, instance, database string) string {
	return fmt.Sprintf("%s/%s/%s", project, instance, database)
}

func userKey(project, instance, host, name string) string {
	return fmt.Sprintf("%s/%s/%s/%s", project, instance, host, name)
}

func (s *Svc) nextOpName() string {
	s.seq++
	return fmt.Sprintf("op-%d", s.seq)
}

func (s *Svc) writeOperation(w http.ResponseWriter, project, opType, targetID, targetLink string) {
	now := time.Now().UTC().Format(time.RFC3339)
	op := Operation{
		Kind:          "sql#operation",
		Status:        "DONE",
		OperationType: opType,
		Name:          s.nextOpName(),
		TargetID:      targetID,
		TargetLink:    targetLink,
		TargetProject: project,
		InsertTime:    now,
		StartTime:     now,
		EndTime:       now,
		SelfLink:      fmt.Sprintf("projects/%s/operations/%s", project, ""),
	}
	op.SelfLink = fmt.Sprintf("projects/%s/operations/%s", project, op.Name)
	_ = s.db.Put(bucketOps, fmt.Sprintf("%s/%s", project, op.Name), op)
	server.WriteJSON(w, 200, op)
}

func (s *Svc) getOperation(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	opName := r.PathValue("operation")
	var op Operation
	found, err := s.db.Get(bucketOps, fmt.Sprintf("%s/%s", project, opName), &op)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "operación no encontrada")
		return
	}
	server.WriteJSON(w, 200, op)
}

func (s *Svc) createInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name            string   `json:"name"`
		Region          string   `json:"region"`
		DatabaseVersion string   `json:"databaseVersion"`
		Settings        Settings `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	var existingInst DatabaseInstance
	found, err := s.db.Get(bucketInstances, instanceKey(project, body.Name), &existingInst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "instancia ya existe: "+body.Name)
		return
	}
	inst := DatabaseInstance{
		Kind:            "sql#instance",
		Name:            body.Name,
		Project:         project,
		Region:          orDefault(body.Region, "us-central1"),
		DatabaseVersion: orDefault(body.DatabaseVersion, "POSTGRES_15"),
		Settings:        body.Settings,
		State:           "RUNNABLE",
		ConnectionName:  fmt.Sprintf("%s:%s:%s", project, orDefault(body.Region, "us-central1"), body.Name),
		IPAddresses: []IPMapping{
			{Type: "PRIMARY", IPAddr: fmt.Sprintf("10.0.%d.10", len(body.Name)%255)},
		},
		SelfLink: fmt.Sprintf("projects/%s/instances/%s", project, body.Name),
	}
	if inst.Settings.Tier == "" {
		inst.Settings.Tier = "db-f1-micro"
	}
	s.tryStartReal(r, project, &inst)
	if err := s.db.Put(bucketInstances, instanceKey(project, inst.Name), inst); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, "CREATE", inst.Name, inst.SelfLink)
}

func (s *Svc) listInstances(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := project + "/"
	items := []DatabaseInstance{}
	_ = s.db.List(bucketInstances, prefix, func(key string, raw []byte) error {
		var inst DatabaseInstance
		if err := json.Unmarshal(raw, &inst); err != nil {
			return err
		}
		items = append(items, inst)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "sql#instancesList", "items": items})
}

func (s *Svc) getInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	var inst DatabaseInstance
	found, err := s.db.Get(bucketInstances, instanceKey(project, instance), &inst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instancia no encontrada")
		return
	}
	s.pollRealMetrics(r, project, instance)
	server.WriteJSON(w, 200, inst)
}

// pollRealMetrics is Phase 15's source for real Cloud SQL metrics: every
// time someone actually checks on a real-backed instance (the same
// "metrics get fresher when something looks at the resource" posture
// used throughout this opt-in feature set), it queries the live embedded
// Postgres engine's connection count and records it into internal/
// activity as a GAUGE, so Cloud Monitoring's timeSeries.list reflects an
// actual measurement instead of staying an empty stub. A failed poll
// (engine briefly unreachable, etc.) is logged and otherwise ignored —
// this must never break a plain GET of the instance.
func (s *Svc) pollRealMetrics(r *http.Request, project, instance string) {
	backend := s.realBackendFor(project, instance)
	if backend == nil {
		return
	}
	connections, err := backend.Stats(r.Context())
	if err != nil {
		log.Printf("cloudsql: no se pudieron leer métricas reales de %s/%s: %v", project, instance, err)
		return
	}
	activity.RecordGauge(project, "cloudsql.googleapis.com/database/postgresql/num_backends",
		map[string]string{"database_id": project + ":" + instance}, float64(connections))
}

func (s *Svc) patchInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	var existing DatabaseInstance
	found, err := s.db.Get(bucketInstances, instanceKey(project, instance), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instancia no encontrada")
		return
	}
	var body struct {
		Settings Settings `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Settings.Tier != "" {
		existing.Settings.Tier = body.Settings.Tier
	}
	if body.Settings.DataDiskSizeGb != "" {
		existing.Settings.DataDiskSizeGb = body.Settings.DataDiskSizeGb
	}
	if body.Settings.ActivationPolicy != "" {
		existing.Settings.ActivationPolicy = body.Settings.ActivationPolicy
	}
	if body.Settings.AvailabilityType != "" {
		existing.Settings.AvailabilityType = body.Settings.AvailabilityType
	}
	if err := s.db.Put(bucketInstances, instanceKey(project, instance), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, "UPDATE", instance, existing.SelfLink)
}

func (s *Svc) deleteInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	if s.gov != nil {
		if s.realBackendFor(project, instance) != nil {
			activity.RecordLog(project, activity.LogEntry{
				LogName:     fmt.Sprintf("projects/%s/logs/cloudsql.googleapis.com%%2Freal_backend", project),
				Severity:    "INFO",
				TextPayload: fmt.Sprintf("instancia %s: backend Postgres real detenido", instance),
				Resource:    map[string]any{"type": "cloudsql_database", "labels": map[string]string{"database_id": project + ":" + instance}},
			})
		}
		s.gov.Release(realBackendID(project, instance))
	}
	if err := s.db.Delete(bucketInstances, instanceKey(project, instance)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, "DELETE", instance, "")
}

func (s *Svc) createDatabase(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	var body struct {
		Name      string `json:"name"`
		Charset   string `json:"charset"`
		Collation string `json:"collation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	var existingDB Database
	found, err := s.db.Get(bucketDatabases, databaseKey(project, instance, body.Name), &existingDB)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "base de datos ya existe: "+body.Name)
		return
	}
	dbRes := Database{
		Kind:      "sql#database",
		Name:      body.Name,
		Project:   project,
		Instance:  instance,
		Charset:   orDefault(body.Charset, "UTF8"),
		Collation: orDefault(body.Collation, "en_US.UTF8"),
		SelfLink:  fmt.Sprintf("projects/%s/instances/%s/databases/%s", project, instance, body.Name),
	}
	if backend := s.realBackendFor(project, instance); backend != nil {
		if err := backend.Exec("CREATE DATABASE " + quoteIdent(body.Name)); err != nil {
			server.WriteError(w, 500, "INTERNAL", "no se pudo crear la base de datos real: "+err.Error())
			return
		}
	}
	if err := s.db.Put(bucketDatabases, databaseKey(project, instance, body.Name), dbRes); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, "CREATE_DATABASE", body.Name, dbRes.SelfLink)
}

func (s *Svc) listDatabases(w http.ResponseWriter, r *http.Request) {
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
	server.WriteJSON(w, 200, map[string]any{"kind": "sql#databasesList", "items": items})
}

func (s *Svc) getDatabase(w http.ResponseWriter, r *http.Request) {
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
		server.WriteError(w, 404, "NOT_FOUND", "base de datos no encontrada")
		return
	}
	server.WriteJSON(w, 200, d)
}

func (s *Svc) patchDatabase(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	database := r.PathValue("database")
	var existing Database
	found, err := s.db.Get(bucketDatabases, databaseKey(project, instance, database), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "base de datos no encontrada")
		return
	}
	var body struct {
		Charset   string `json:"charset"`
		Collation string `json:"collation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Charset != "" {
		existing.Charset = body.Charset
	}
	if body.Collation != "" {
		existing.Collation = body.Collation
	}
	if err := s.db.Put(bucketDatabases, databaseKey(project, instance, database), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, "UPDATE_DATABASE", database, existing.SelfLink)
}

func (s *Svc) deleteDatabase(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	database := r.PathValue("database")
	if backend := s.realBackendFor(project, instance); backend != nil {
		if err := backend.Exec("DROP DATABASE IF EXISTS " + quoteIdent(database)); err != nil {
			server.WriteError(w, 500, "INTERNAL", "no se pudo eliminar la base de datos real: "+err.Error())
			return
		}
	}
	if err := s.db.Delete(bucketDatabases, databaseKey(project, instance, database)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, "DELETE_DATABASE", database, "")
}

func (s *Svc) createUser(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	var body struct {
		Name     string `json:"name"`
		Host     string `json:"host"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	host := orDefault(body.Host, "%")
	var existingUser User
	found, err := s.db.Get(bucketUsers, userKey(project, instance, host, body.Name), &existingUser)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "usuario ya existe: "+body.Name)
		return
	}
	usr := User{
		Kind:     "sql#user",
		Name:     body.Name,
		Project:  project,
		Instance: instance,
		Host:     host,
		Password: body.Password,
	}
	if backend := s.realBackendFor(project, instance); backend != nil {
		stmt := "CREATE ROLE " + quoteIdent(body.Name) + " LOGIN PASSWORD " + quoteLiteral(body.Password)
		if err := backend.Exec(stmt); err != nil {
			server.WriteError(w, 500, "INTERNAL", "no se pudo crear el usuario real: "+err.Error())
			return
		}
	}
	if err := s.db.Put(bucketUsers, userKey(project, instance, host, body.Name), usr); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, "CREATE_USER", body.Name, "")
}

func (s *Svc) listUsers(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	prefix := fmt.Sprintf("%s/%s/", project, instance)
	items := []User{}
	_ = s.db.List(bucketUsers, prefix, func(key string, raw []byte) error {
		var u User
		if err := json.Unmarshal(raw, &u); err != nil {
			return err
		}
		u.Password = "" // la API real nunca devuelve el password en list/get
		items = append(items, u)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "sql#usersList", "items": items})
}

// deleteUser usa query params (?host=&name=), igual que la API real,
// porque el nombre de usuario no es único sin el host.
func (s *Svc) deleteUser(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	name := r.URL.Query().Get("name")
	host := orDefault(r.URL.Query().Get("host"), "%")
	if name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	if backend := s.realBackendFor(project, instance); backend != nil {
		if err := backend.Exec("DROP ROLE IF EXISTS " + quoteIdent(name)); err != nil {
			server.WriteError(w, 500, "INTERNAL", "no se pudo eliminar el usuario real: "+err.Error())
			return
		}
	}
	if err := s.db.Delete(bucketUsers, userKey(project, instance, host, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, "DELETE_USER", name, "")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
