// Package iam emula un subconjunto de la API de Google Cloud IAM
// (iam.googleapis.com/v1) y de Resource Manager IAM policies: cuentas de
// servicio y políticas IAM a nivel de proyecto. Suficiente para que
// `gcloud iam service-accounts ...` funcione contra el emulador.
package iam

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketServiceAccounts = "iam.service_accounts"
const bucketPolicies = "iam.policies"

type ServiceAccount struct {
	Name        string `json:"name"`
	ProjectID   string `json:"projectId"`
	UniqueID    string `json:"uniqueId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Disabled    bool   `json:"disabled"`
}

type Policy struct {
	Version  int       `json:"version"`
	Bindings []Binding `json:"bindings"`
	Etag     string    `json:"etag"`
}

type Binding struct {
	Role    string   `json:"role"`
	Members []string `json:"members"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service {
	return &Service{db: db}
}

// Register monta las rutas IAM en el mux, siguiendo los paths reales de
// iam.googleapis.com y cloudresourcemanager.googleapis.com.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/serviceAccounts", s.createServiceAccount)
	mux.HandleFunc("GET /v1/projects/{project}/serviceAccounts", s.listServiceAccounts)
	mux.HandleFunc("GET /v1/projects/{project}/serviceAccounts/{account}", s.getServiceAccount)
	mux.HandleFunc("DELETE /v1/projects/{project}/serviceAccounts/{account}", s.deleteServiceAccount)

	// IAM policy a nivel de proyecto (compatible con resourcemanager v1/v3).
	mux.HandleFunc("POST /v1/projects/{project}:getIamPolicy", s.getProjectPolicy)
	mux.HandleFunc("POST /v1/projects/{project}:setIamPolicy", s.setProjectPolicy)

	// Roles predefinidos básicos, sólo lectura (lista estática).
	mux.HandleFunc("GET /v1/roles", s.listPredefinedRoles)
}

func (s *Service) createServiceAccount(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		AccountID      string `json:"accountId"`
		ServiceAccount struct {
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
		} `json:"serviceAccount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "cuerpo inválido: "+err.Error())
		return
	}
	if body.AccountID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "accountId es requerido")
		return
	}
	s.seq++
	sa := ServiceAccount{
		Name:        fmt.Sprintf("projects/%s/serviceAccounts/%s@%s.iam.gserviceaccount.com", project, body.AccountID, project),
		ProjectID:   project,
		UniqueID:    fmt.Sprintf("%d%d", time.Now().Unix(), s.seq),
		Email:       fmt.Sprintf("%s@%s.iam.gserviceaccount.com", body.AccountID, project),
		DisplayName: body.ServiceAccount.DisplayName,
		Description: body.ServiceAccount.Description,
	}
	if err := s.db.Put(bucketServiceAccounts, sa.Email, sa); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, sa)
}

func (s *Service) listServiceAccounts(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var accounts []ServiceAccount
	_ = s.db.List(bucketServiceAccounts, "", func(key string, raw []byte) error {
		var sa ServiceAccount
		if err := json.Unmarshal(raw, &sa); err != nil {
			return err
		}
		if sa.ProjectID == project {
			accounts = append(accounts, sa)
		}
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"accounts": accounts})
}

func (s *Service) getServiceAccount(w http.ResponseWriter, r *http.Request) {
	email := emailFromPathValue(r.PathValue("account"), r.PathValue("project"))
	var sa ServiceAccount
	found, err := s.db.Get(bucketServiceAccounts, email, &sa)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "service account no encontrada")
		return
	}
	server.WriteJSON(w, 200, sa)
}

func (s *Service) deleteServiceAccount(w http.ResponseWriter, r *http.Request) {
	email := emailFromPathValue(r.PathValue("account"), r.PathValue("project"))
	if err := s.db.Delete(bucketServiceAccounts, email); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

// emailFromPathValue acepta tanto el email completo como "accountId" en el
// path, igual que la API real (acepta {account} o {account}@project.iam...).
func emailFromPathValue(account, project string) string {
	if strings.Contains(account, "@") {
		return account
	}
	return fmt.Sprintf("%s@%s.iam.gserviceaccount.com", account, project)
}

func (s *Service) getProjectPolicy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var policy Policy
	found, err := s.db.Get(bucketPolicies, project, &policy)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		policy = Policy{Version: 1, Bindings: []Binding{}, Etag: "initial"}
	}
	server.WriteJSON(w, 200, policy)
}

func (s *Service) setProjectPolicy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Policy Policy `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	body.Policy.Etag = fmt.Sprintf("etag-%d", time.Now().UnixNano())
	if err := s.db.Put(bucketPolicies, project, body.Policy); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, body.Policy)
}

func (s *Service) listPredefinedRoles(w http.ResponseWriter, r *http.Request) {
	roles := []map[string]string{
		{"name": "roles/owner", "title": "Owner", "description": "Acceso total a todos los recursos."},
		{"name": "roles/editor", "title": "Editor", "description": "Editar todos los recursos."},
		{"name": "roles/viewer", "title": "Viewer", "description": "Acceso de solo lectura."},
		{"name": "roles/storage.admin", "title": "Storage Admin", "description": "Control total sobre buckets y objetos."},
		{"name": "roles/compute.admin", "title": "Compute Admin", "description": "Control total sobre recursos de Compute Engine."},
	}
	server.WriteJSON(w, 200, map[string]any{"roles": roles})
}
