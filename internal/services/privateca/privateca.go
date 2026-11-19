// Package privateca emulates a subset of the Certificate Authority Service API
// (privateca.googleapis.com/v1): CA pools and Certificate Authorities
// (google_privateca_ca_pool and google_privateca_certificate_authority in Terraform).
package privateca

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketCaPools = "privateca.caPools"
	bucketCAs     = "privateca.certificateAuthorities"
)

// CaPool represents a Certificate Authority Pool.
type CaPool struct {
	Name            string            `json:"name"` // projects/{project}/locations/{location}/caPools/{caPool}
	Tier            string            `json:"tier,omitempty"` // ENTERPRISE | DEVOPS
	IssuancePolicy  json.RawMessage   `json:"issuancePolicy,omitempty"`
	PublishingOptions json.RawMessage `json:"publishingOptions,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

// KeySpec specifies the cryptographic key algorithm.
type KeySpec struct {
	CloudKmsKeyVersion string `json:"cloudKmsKeyVersion,omitempty"`
	Algorithm          string `json:"algorithm,omitempty"` // RSA_PSS_2048_SHA256, etc.
}

// CertificateAuthority represents an individual CA within a pool.
type CertificateAuthority struct {
	Name       string            `json:"name"` // projects/{project}/locations/{location}/caPools/{caPool}/certificateAuthorities/{ca}
	Type       string            `json:"type,omitempty"` // SELF_SIGNED | SUBORDINATE
	Config     json.RawMessage   `json:"config,omitempty"`
	Lifetime   string            `json:"lifetime,omitempty"`
	KeySpec    *KeySpec          `json:"keySpec,omitempty"`
	State      string            `json:"state,omitempty"` // ENABLED | DISABLED | STAGED
	Labels     map[string]string `json:"labels,omitempty"`
	CreateTime string            `json:"createTime,omitempty"`
	Tier       string            `json:"tier,omitempty"`
}

// Operation mirrors standard google.longrunning.Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service {
	return &Service{db: db}
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/caPools", s.createCaPool)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/caPools", s.listCaPools)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/caPools/{caPool}", s.getCaPool)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/caPools/{caPool}", s.deleteCaPool)

	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/caPools/{caPool}/certificateAuthorities", s.createCA)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/caPools/{caPool}/certificateAuthorities", s.listCAs)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/caPools/{caPool}/certificateAuthorities/{ca}", s.getCA)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/caPools/{caPool}/certificateAuthorities/{ca}", s.deleteCA)
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/caPools/{caPool}/certificateAuthorities/{caAction}", s.caAction)
}

func caPoolKey(project, location, pool string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, pool)
}

func caPoolName(project, location, pool string) string {
	return fmt.Sprintf("projects/%s/locations/%s/caPools/%s", project, location, pool)
}

func caKey(project, location, pool, ca string) string {
	return fmt.Sprintf("%s/%s/%s/%s", project, location, pool, ca)
}

func caName(project, location, pool, ca string) string {
	return fmt.Sprintf("projects/%s/locations/%s/caPools/%s/certificateAuthorities/%s", project, location, pool, ca)
}

func (s *Service) nextID() int64 {
	return atomic.AddInt64(&s.seq, 1)
}

func (s *Service) writeOp(w http.ResponseWriter, project, location string, res any) {
	respBytes, _ := json.Marshal(res)
	op := Operation{
		Name:     fmt.Sprintf("projects/%s/locations/%s/operations/op-privateca-%d", project, location, s.nextID()),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createCaPool(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	poolID := r.URL.Query().Get("caPoolId")

	var pool CaPool
	if err := json.NewDecoder(r.Body).Decode(&pool); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if poolID == "" {
		if pool.Name != "" {
			parts := strings.Split(pool.Name, "/")
			poolID = parts[len(parts)-1]
		}
	}
	if poolID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "caPoolId is required")
		return
	}

	key := caPoolKey(project, location, poolID)
	var existing CaPool
	found, err := s.db.Get(bucketCaPools, key, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "caPool already exists: "+poolID)
		return
	}

	pool.Name = caPoolName(project, location, poolID)
	if pool.Tier == "" {
		pool.Tier = "ENTERPRISE"
	}
	if err := s.db.Put(bucketCaPools, key, pool); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, pool)
}

func (s *Service) getCaPool(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	poolID := r.PathValue("caPool")

	var pool CaPool
	found, err := s.db.Get(bucketCaPools, caPoolKey(project, location, poolID), &pool)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "caPool not found: "+poolID)
		return
	}
	server.WriteJSON(w, 200, pool)
}

func (s *Service) listCaPools(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)

	var list []CaPool
	err := s.db.List(bucketCaPools, prefix, func(key string, raw []byte) error {
		var p CaPool
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		list = append(list, p)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if list == nil {
		list = []CaPool{}
	}
	server.WriteJSON(w, 200, map[string]any{"caPools": list})
}

func (s *Service) deleteCaPool(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	poolID := r.PathValue("caPool")

	key := caPoolKey(project, location, poolID)
	var pool CaPool
	found, err := s.db.Get(bucketCaPools, key, &pool)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "caPool not found: "+poolID)
		return
	}

	// Cascading delete to child CAs: buffer keys first to prevent BoltDB view deadlock
	caPrefix := fmt.Sprintf("%s/%s/%s/", project, location, poolID)
	var caKeys []string
	_ = s.db.List(bucketCAs, caPrefix, func(k string, raw []byte) error {
		caKeys = append(caKeys, k)
		return nil
	})
	for _, ck := range caKeys {
		_ = s.db.Delete(bucketCAs, ck)
	}

	if err := s.db.Delete(bucketCaPools, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, map[string]any{})
}

func (s *Service) createCA(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	poolID := r.PathValue("caPool")
	caID := r.URL.Query().Get("certificateAuthorityId")

	// CaPool must exist
	var pool CaPool
	found, err := s.db.Get(bucketCaPools, caPoolKey(project, location, poolID), &pool)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "caPool not found: "+poolID)
		return
	}

	var ca CertificateAuthority
	if err := json.NewDecoder(r.Body).Decode(&ca); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if caID == "" {
		if ca.Name != "" {
			parts := strings.Split(ca.Name, "/")
			caID = parts[len(parts)-1]
		}
	}
	if caID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "certificateAuthorityId is required")
		return
	}

	key := caKey(project, location, poolID, caID)
	var existing CertificateAuthority
	found, err = s.db.Get(bucketCAs, key, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "certificateAuthority already exists: "+caID)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	ca.Name = caName(project, location, poolID, caID)
	ca.Tier = pool.Tier
	if ca.Type == "" {
		ca.Type = "SELF_SIGNED"
	}
	if ca.State == "" {
		ca.State = "ENABLED"
	}
	ca.CreateTime = now

	if err := s.db.Put(bucketCAs, key, ca); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, ca)
}

func (s *Service) getCA(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	poolID := r.PathValue("caPool")
	caID := r.PathValue("ca")

	var ca CertificateAuthority
	found, err := s.db.Get(bucketCAs, caKey(project, location, poolID, caID), &ca)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "certificateAuthority not found: "+caID)
		return
	}
	server.WriteJSON(w, 200, ca)
}

func (s *Service) listCAs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	poolID := r.PathValue("caPool")
	prefix := fmt.Sprintf("%s/%s/%s/", project, location, poolID)

	var list []CertificateAuthority
	err := s.db.List(bucketCAs, prefix, func(key string, raw []byte) error {
		var ca CertificateAuthority
		if err := json.Unmarshal(raw, &ca); err != nil {
			return err
		}
		list = append(list, ca)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if list == nil {
		list = []CertificateAuthority{}
	}
	server.WriteJSON(w, 200, map[string]any{"certificateAuthorities": list})
}

func (s *Service) deleteCA(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	poolID := r.PathValue("caPool")
	caID := r.PathValue("ca")

	key := caKey(project, location, poolID, caID)
	var ca CertificateAuthority
	found, err := s.db.Get(bucketCAs, key, &ca)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "certificateAuthority not found: "+caID)
		return
	}
	if err := s.db.Delete(bucketCAs, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, map[string]any{})
}

func (s *Service) caAction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	poolID := r.PathValue("caPool")
	caID, action, ok := strings.Cut(r.PathValue("caAction"), ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "invalid action route")
		return
	}

	key := caKey(project, location, poolID, caID)
	var ca CertificateAuthority
	found, err := s.db.Get(bucketCAs, key, &ca)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "certificateAuthority not found: "+caID)
		return
	}

	switch action {
	case "enable", "activate":
		ca.State = "ENABLED"
	case "disable":
		ca.State = "DISABLED"
	default:
		server.WriteError(w, 404, "NOT_FOUND", "unknown action: "+action)
		return
	}

	if err := s.db.Put(bucketCAs, key, ca); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOp(w, project, location, ca)
}
