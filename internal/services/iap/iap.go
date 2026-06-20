// Package iap emulates a subset of the Identity-Aware Proxy API
// (iap.googleapis.com/v1): brands and their nested OAuth clients. Real IAP
// brands represent the OAuth consent screen configuration for a project and
// are permanent once created (no update/delete in the real API, which this
// emulator mirrors); clients are the nested OAuth client IDs/secrets used
// by `google_iap_client`. This emulator just persists the resource shape
// and never actually enforces IAP on any backend, same
// "shape-compatible, not behavior-complete" approach used elsewhere.
package iap

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketBrands  = "iap.brands"
	bucketClients = "iap.clients"
)

// Brand mirrors the relevant subset of iap#Brand.
type Brand struct {
	Name             string `json:"name"` // projects/{project}/brands/{brand}
	SupportEmail     string `json:"supportEmail"`
	ApplicationTitle string `json:"applicationTitle"`
	OrgInternalOnly  bool   `json:"orgInternalOnly,omitempty"`
}

// Client mirrors the relevant subset of iap#IdentityAwareProxyClient.
type Client struct {
	Name        string `json:"name"` // projects/{project}/brands/{brand}/identityAwareProxyClients/{client}
	Secret      string `json:"secret,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/brands", s.createBrand)
	mux.HandleFunc("GET /v1/projects/{project}/brands", s.listBrands)
	mux.HandleFunc("GET /v1/projects/{project}/brands/{brand}", s.getBrand)

	mux.HandleFunc("POST /v1/projects/{project}/brands/{brand}/identityAwareProxyClients", s.createClient)
	mux.HandleFunc("GET /v1/projects/{project}/brands/{brand}/identityAwareProxyClients", s.listClients)
	mux.HandleFunc("GET /v1/projects/{project}/brands/{brand}/identityAwareProxyClients/{client}", s.getClient)
	mux.HandleFunc("DELETE /v1/projects/{project}/brands/{brand}/identityAwareProxyClients/{client}", s.deleteClient)
}

func brandKey(project string) string { return project }

func brandName(project, brand string) string {
	return fmt.Sprintf("projects/%s/brands/%s", project, brand)
}

func clientKey(project, brand, client string) string {
	return fmt.Sprintf("%s/%s/%s", project, brand, client)
}

func clientName(project, brand, client string) string {
	return fmt.Sprintf("projects/%s/brands/%s/identityAwareProxyClients/%s", project, brand, client)
}

func (s *Service) nextID() int64 {
	s.seq++
	return s.seq
}

func randomSecret() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// createBrand creates the (singleton) brand for a project. The real API
// allows exactly one brand per project; this emulator enforces the same.
func (s *Service) createBrand(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var existing Brand
	found, err := s.db.Get(bucketBrands, brandKey(project), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "brand already exists for project: "+project)
		return
	}
	var body struct {
		SupportEmail     string `json:"supportEmail"`
		ApplicationTitle string `json:"applicationTitle"`
		OrgInternalOnly  bool   `json:"orgInternalOnly"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.SupportEmail == "" || body.ApplicationTitle == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "supportEmail and applicationTitle are required")
		return
	}
	brandID := fmt.Sprintf("%d", s.nextID())
	brand := Brand{
		Name:             brandName(project, brandID),
		SupportEmail:     body.SupportEmail,
		ApplicationTitle: body.ApplicationTitle,
		OrgInternalOnly:  body.OrgInternalOnly,
	}
	if err := s.db.Put(bucketBrands, brandKey(project), brand); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, brand)
}

func (s *Service) listBrands(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	items := []Brand{}
	var existing Brand
	found, err := s.db.Get(bucketBrands, brandKey(project), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		items = append(items, existing)
	}
	server.WriteJSON(w, 200, map[string]any{"brands": items})
}

func (s *Service) getBrand(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var brand Brand
	found, err := s.db.Get(bucketBrands, brandKey(project), &brand)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found || brand.Name != brandName(project, r.PathValue("brand")) {
		server.WriteError(w, 404, "NOT_FOUND", "brand not found")
		return
	}
	server.WriteJSON(w, 200, brand)
}

func (s *Service) createClient(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	brand := r.PathValue("brand")
	var body struct {
		DisplayName string `json:"displayName"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	clientID := fmt.Sprintf("client-%d.apps.googleusercontent.com", s.nextID())
	c := Client{
		Name:        clientName(project, brand, clientID),
		Secret:      randomSecret(),
		DisplayName: body.DisplayName,
	}
	if err := s.db.Put(bucketClients, clientKey(project, brand, clientID), c); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, c)
}

func (s *Service) listClients(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	brand := r.PathValue("brand")
	prefix := fmt.Sprintf("%s/%s/", project, brand)
	items := []Client{}
	_ = s.db.List(bucketClients, prefix, func(key string, raw []byte) error {
		var c Client
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		items = append(items, c)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"identityAwareProxyClients": items})
}

func (s *Service) getClient(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	brand := r.PathValue("brand")
	client := r.PathValue("client")
	var c Client
	found, err := s.db.Get(bucketClients, clientKey(project, brand, client), &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "client not found")
		return
	}
	server.WriteJSON(w, 200, c)
}

func (s *Service) deleteClient(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	brand := r.PathValue("brand")
	client := r.PathValue("client")
	if err := s.db.Delete(bucketClients, clientKey(project, brand, client)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}
