// Package certificatemanager emulates a subset of the Certificate Manager
// API (certificatemanager.googleapis.com/v1): certificates and certificate
// maps used by load balancers for TLS termination (google_certificate_manager_
// certificate / _certificate_map in Terraform). Real managed certificates
// go through DNS/HTTP authorization and CA issuance before becoming
// ACTIVE; this emulator always reports ACTIVE immediately and never
// issues a real certificate, same "shape-compatible, not
// behavior-complete" approach used elsewhere (e.g. Filestore/Cloud SQL
// instances reporting READY immediately).
package certificatemanager

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketCertificates = "certificatemanager.certificates"
	bucketMaps         = "certificatemanager.maps"
)

// Operation mirrors google.longrunning.Operation, same shape used by
// vpcaccess/workflows/eventarc/filestore elsewhere in this codebase.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

// SelfManagedCertificate mirrors certificatemanager#SelfManagedCertificate.
type SelfManagedCertificate struct {
	PemCertificate string `json:"pemCertificate,omitempty"`
	PemPrivateKey  string `json:"pemPrivateKey,omitempty"`
}

// ManagedCertificate mirrors certificatemanager#ManagedCertificate (subset).
type ManagedCertificate struct {
	Domains []string `json:"domains,omitempty"`
	State   string   `json:"state,omitempty"`
}

// Certificate mirrors the relevant subset of certificatemanager#Certificate.
type Certificate struct {
	Name        string                  `json:"name"` // projects/{p}/locations/{l}/certificates/{id}
	Description string                  `json:"description,omitempty"`
	SelfManaged *SelfManagedCertificate `json:"selfManaged,omitempty"`
	Managed     *ManagedCertificate     `json:"managed,omitempty"`
	SanDnsnames []string                `json:"sanDnsnames,omitempty"`
	CreateTime  string                  `json:"createTime,omitempty"`
	Labels      map[string]string       `json:"labels,omitempty"`
}

// CertificateMap mirrors the relevant subset of certificatemanager#CertificateMap.
type CertificateMap struct {
	Name        string            `json:"name"` // projects/{p}/locations/{l}/certificateMaps/{id}
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/certificates", s.createCertificate)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/certificates", s.listCertificates)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/certificates/{certificate}", s.getCertificate)
	mux.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/certificates/{certificate}", s.updateCertificate)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/certificates/{certificate}", s.deleteCertificate)

	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/certificateMaps", s.createMap)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/certificateMaps", s.listMaps)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/certificateMaps/{map}", s.getMap)
	mux.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/certificateMaps/{map}", s.updateMap)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/certificateMaps/{map}", s.deleteMap)
}

func certKey(project, location, id string) string { return project + "/" + location + "/" + id }

func certName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/certificates/%s", project, location, id)
}

func mapName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/certificateMaps/%s", project, location, id)
}

func (s *Service) nextID() int64 {
	s.seq++
	return s.seq
}

func (s *Service) writeOperation(w http.ResponseWriter, target string, resource any) {
	meta, _ := json.Marshal(map[string]string{"target": target})
	resp, _ := json.Marshal(resource)
	op := Operation{
		Name:     fmt.Sprintf("operations/op-%d", s.nextID()),
		Done:     true,
		Metadata: meta,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createCertificate(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.URL.Query().Get("certificateId")
	if id == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "certificateId query parameter is required")
		return
	}
	var existing Certificate
	found, err := s.db.Get(bucketCertificates, certKey(project, location, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "certificate already exists: "+id)
		return
	}
	var body Certificate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.SelfManaged == nil && body.Managed == nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "selfManaged or managed must be set")
		return
	}
	if body.Managed != nil {
		body.Managed.State = "ACTIVE"
	}
	body.Name = certName(project, location, id)
	if err := s.db.Put(bucketCertificates, certKey(project, location, id), body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, body.Name, body)
}

func (s *Service) listCertificates(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	items := []Certificate{}
	err := s.db.List(bucketCertificates, project+"/"+location+"/", func(key string, raw []byte) error {
		var c Certificate
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		items = append(items, c)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{"certificates": items})
}

func (s *Service) getCertificate(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("certificate")
	var c Certificate
	found, err := s.db.Get(bucketCertificates, certKey(project, location, id), &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "certificate not found")
		return
	}
	server.WriteJSON(w, 200, c)
}

func (s *Service) updateCertificate(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("certificate")
	var existing Certificate
	found, err := s.db.Get(bucketCertificates, certKey(project, location, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "certificate not found")
		return
	}
	var body Certificate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Description != "" {
		existing.Description = body.Description
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	if err := s.db.Put(bucketCertificates, certKey(project, location, id), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, existing.Name, existing)
}

func (s *Service) deleteCertificate(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("certificate")
	if err := s.db.Delete(bucketCertificates, certKey(project, location, id)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, certName(project, location, id), map[string]string{})
}

func (s *Service) createMap(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.URL.Query().Get("certificateMapId")
	if id == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "certificateMapId query parameter is required")
		return
	}
	var existing CertificateMap
	found, err := s.db.Get(bucketMaps, certKey(project, location, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "certificate map already exists: "+id)
		return
	}
	var body CertificateMap
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	body.Name = mapName(project, location, id)
	if err := s.db.Put(bucketMaps, certKey(project, location, id), body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, body.Name, body)
}

func (s *Service) listMaps(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	items := []CertificateMap{}
	err := s.db.List(bucketMaps, project+"/"+location+"/", func(key string, raw []byte) error {
		var m CertificateMap
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		items = append(items, m)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{"certificateMaps": items})
}

func (s *Service) getMap(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("map")
	var m CertificateMap
	found, err := s.db.Get(bucketMaps, certKey(project, location, id), &m)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "certificate map not found")
		return
	}
	server.WriteJSON(w, 200, m)
}

func (s *Service) updateMap(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("map")
	var existing CertificateMap
	found, err := s.db.Get(bucketMaps, certKey(project, location, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "certificate map not found")
		return
	}
	var body CertificateMap
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Description != "" {
		existing.Description = body.Description
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	if err := s.db.Put(bucketMaps, certKey(project, location, id), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, existing.Name, existing)
}

func (s *Service) deleteMap(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	id := r.PathValue("map")
	if err := s.db.Delete(bucketMaps, certKey(project, location, id)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, mapName(project, location, id), map[string]string{})
}
