// Fase 17: Cloud SQL ssl certs + database flags catalog.
//
// sslCerts: CRUD + createEphemeral (returns a fake PEM cert).
// flags:    GET /sql/v1beta4/flags — static catalog of common flags.
package cloudsql

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

const bucketSSLCerts = "cloudsql.sslcerts"

// SslCert replica sqladmin.SslCert.
type SslCert struct {
	Kind             string `json:"kind"`
	CertSerialNumber string `json:"certSerialNumber"`
	Cert             string `json:"cert"` // fake PEM content
	CreateTime       string `json:"createTime"`
	ExpirationTime   string `json:"expirationTime"`
	Sha1Fingerprint  string `json:"sha1Fingerprint"`
	Instance         string `json:"instance"`
	SelfLink         string `json:"selfLink"`
	CommonName       string `json:"commonName,omitempty"`
}

// Flag replica sqladmin.Flag.
type Flag struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	AppliesTo    []string `json:"appliesTo"`
	MinValue     *string  `json:"minValue,omitempty"`
	MaxValue     *string  `json:"maxValue,omitempty"`
	AllowedValues []string `json:"allowedValues,omitempty"`
	RequiresRestart bool   `json:"requiresRestart,omitempty"`
}

// ── Route registration ─────────────────────────────────────────────────────────

func (s *Svc) registerExtras(mux *http.ServeMux) {
	// Flags (static catalog, no instance).
	mux.HandleFunc("GET /sql/v1beta4/flags", s.listFlags)

	// SSL certs CRUD.
	mux.HandleFunc("POST /sql/v1beta4/projects/{project}/instances/{instance}/sslCerts", s.insertSslCert)
	mux.HandleFunc("GET /sql/v1beta4/projects/{project}/instances/{instance}/sslCerts", s.listSslCerts)
	mux.HandleFunc("GET /sql/v1beta4/projects/{project}/instances/{instance}/sslCerts/{fingerprint}", s.getSslCert)
	mux.HandleFunc("DELETE /sql/v1beta4/projects/{project}/instances/{instance}/sslCerts/{fingerprint}", s.deleteSslCert)

	// createEphemeral — verb suffix on sslCerts collection.
	mux.HandleFunc("POST /sql/v1beta4/projects/{project}/instances/{instance}/sslCerts:createEphemeral", s.createEphemeralCert)
}

// ── flags ─────────────────────────────────────────────────────────────────────

func (s *Svc) listFlags(w http.ResponseWriter, r *http.Request) {
	dv := r.URL.Query().Get("databaseVersion")
	flags := staticFlags()
	if dv != "" {
		filtered := flags[:0]
		upper := strings.ToUpper(dv)
		for _, f := range flags {
			for _, a := range f.AppliesTo {
				if strings.HasPrefix(upper, strings.ToUpper(a)) {
					filtered = append(filtered, f)
					break
				}
			}
		}
		flags = filtered
	}
	server.WriteJSON(w, 200, map[string]any{"kind": "sql#flagsList", "items": flags})
}

func staticFlags() []Flag {
	pg := []string{"POSTGRES_9_6", "POSTGRES_10", "POSTGRES_11", "POSTGRES_12", "POSTGRES_13", "POSTGRES_14", "POSTGRES_15", "POSTGRES_16"}
	my := []string{"MYSQL_5_7", "MYSQL_8_0"}
	both := append(pg, my...)
	minZero := "0"
	max := "2147483647"
	return []Flag{
		{Name: "max_connections", Type: "INTEGER", AppliesTo: pg, MinValue: &minZero, MaxValue: &max, RequiresRestart: true},
		{Name: "shared_buffers", Type: "INTEGER", AppliesTo: pg, MinValue: &minZero, MaxValue: &max, RequiresRestart: true},
		{Name: "work_mem", Type: "INTEGER", AppliesTo: pg, MinValue: &minZero, MaxValue: &max},
		{Name: "maintenance_work_mem", Type: "INTEGER", AppliesTo: pg, MinValue: &minZero, MaxValue: &max},
		{Name: "effective_cache_size", Type: "INTEGER", AppliesTo: pg, MinValue: &minZero, MaxValue: &max},
		{Name: "log_min_duration_statement", Type: "INTEGER", AppliesTo: pg, MinValue: &minZero, MaxValue: &max},
		{Name: "max_allowed_packet", Type: "INTEGER", AppliesTo: my, MinValue: &minZero, MaxValue: &max},
		{Name: "innodb_buffer_pool_size", Type: "INTEGER", AppliesTo: my, MinValue: &minZero, MaxValue: &max, RequiresRestart: true},
		{Name: "slow_query_log", Type: "STRING", AppliesTo: my, AllowedValues: []string{"on", "off"}},
		{Name: "long_query_time", Type: "FLOAT", AppliesTo: my},
		{Name: "character_set_server", Type: "STRING", AppliesTo: my, AllowedValues: []string{"utf8mb4", "utf8", "latin1"}},
		{Name: "sql_mode", Type: "STRING", AppliesTo: my},
		{Name: "log_output", Type: "STRING", AppliesTo: both, AllowedValues: []string{"FILE", "TABLE", "NONE"}},
	}
}

// ── SSL certs ─────────────────────────────────────────────────────────────────

func (s *Svc) insertSslCert(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")

	var body struct {
		CommonName string `json:"commonName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "invalid", "invalid JSON: "+err.Error())
		return
	}
	if body.CommonName == "" {
		server.WriteError(w, 400, "invalid", "commonName es requerido")
		return
	}

	cert := newFakeCert(project, instance, body.CommonName)
	if err := s.db.Put(bucketSSLCerts, sslCertKey(project, instance, cert.Sha1Fingerprint), cert); err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{
		"kind":       "sql#sslCertsInsertResponse",
		"operation":  makeOp(project, instance, "CREATE_SSL_CERT"),
		"serverCaCert": fakeCACert(project, instance),
		"clientCert": map[string]any{
			"certInfo": cert,
			"certPrivateKey": map[string]any{
				"kind": "sql#sslCertDetail",
			},
		},
	})
}

func (s *Svc) listSslCerts(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	prefix := sslCertPrefix(project, instance)
	var items []SslCert
	_ = s.db.List(bucketSSLCerts, prefix, func(_ string, raw []byte) error {
		var c SslCert
		if err := json.Unmarshal(raw, &c); err == nil {
			items = append(items, c)
		}
		return nil
	})
	if items == nil {
		items = []SslCert{}
	}
	server.WriteJSON(w, 200, map[string]any{"kind": "sql#sslCertsList", "items": items})
}

func (s *Svc) getSslCert(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	fingerprint := r.PathValue("fingerprint")
	var cert SslCert
	found, err := s.db.Get(bucketSSLCerts, sslCertKey(project, instance, fingerprint), &cert)
	if err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "notFound", "ssl cert not found")
		return
	}
	server.WriteJSON(w, 200, cert)
}

func (s *Svc) deleteSslCert(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")
	fingerprint := r.PathValue("fingerprint")
	key := sslCertKey(project, instance, fingerprint)
	var existing SslCert
	if found, _ := s.db.Get(bucketSSLCerts, key, &existing); !found {
		server.WriteError(w, 404, "notFound", "ssl cert not found")
		return
	}
	if err := s.db.Delete(bucketSSLCerts, key); err != nil {
		server.WriteError(w, 500, "internal", err.Error())
		return
	}
	server.WriteJSON(w, 200, makeOp(project, instance, "DELETE_SSL_CERT"))
}

func (s *Svc) createEphemeralCert(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	instance := r.PathValue("instance")

	// body may contain a public_key for real signing; we ignore it and return a fake cert.
	cert := newFakeCert(project, instance, "ephemeral-client")
	// Ephemeral certs are not stored.
	server.WriteJSON(w, 200, cert)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func sslCertPrefix(project, instance string) string {
	return fmt.Sprintf("%s/%s/", project, instance)
}

func sslCertKey(project, instance, fingerprint string) string {
	return fmt.Sprintf("%s/%s/%s", project, instance, fingerprint)
}

func newFakeCert(project, instance, commonName string) SslCert {
	buf := make([]byte, 20)
	_, _ = rand.Read(buf)
	fingerprint := hex.EncodeToString(buf)
	serial := hex.EncodeToString(buf[:8])
	now := time.Now().UTC()
	return SslCert{
		Kind:             "sql#sslCert",
		CertSerialNumber: serial,
		Cert:             fakePEM("CERTIFICATE", fingerprint),
		CreateTime:       now.Format(time.RFC3339),
		ExpirationTime:   now.AddDate(1, 0, 0).Format(time.RFC3339),
		Sha1Fingerprint:  fingerprint,
		Instance:         instance,
		SelfLink:         fmt.Sprintf("/sql/v1beta4/projects/%s/instances/%s/sslCerts/%s", project, instance, fingerprint),
		CommonName:       commonName,
	}
}

func fakeCACert(project, instance string) SslCert {
	return newFakeCert(project, instance, "GoogleCloudSQL-InstanceCACert")
}

func fakePEM(pemType, seed string) string {
	return fmt.Sprintf("-----BEGIN %s-----\nEMULATOR_%s\n-----END %s-----\n", pemType, seed, pemType)
}

func makeOp(project, instance, opType string) Operation {
	now := time.Now().UTC().Format(time.RFC3339)
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	opName := hex.EncodeToString(buf)
	return Operation{
		Kind:          "sql#operation",
		Status:        "DONE",
		OperationType: opType,
		Name:          opName,
		TargetProject: project,
		TargetID:      instance,
		TargetLink:    fmt.Sprintf("/sql/v1beta4/projects/%s/instances/%s", project, instance),
		InsertTime:    now,
		StartTime:     now,
		EndTime:       now,
	}
}
