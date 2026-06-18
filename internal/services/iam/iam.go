// Package iam emula un subconjunto de la API de Google Cloud IAM
// (iam.googleapis.com/v1) y de Resource Manager IAM policies: cuentas de
// servicio y políticas IAM a nivel de proyecto. Suficiente para que
// `gcloud iam service-accounts ...` funcione contra el emulador.
package iam

import (
	"encoding/base64"
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
const bucketCustomRoles = "iam.custom_roles"
const bucketSAKeys = "iam.sa_keys"
const bucketSAPolicies = "iam.sa_policies"

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

// Role replica el recurso "Role" de iam.googleapis.com (custom roles a
// nivel de proyecto, "projects/{project}/roles/{role}").
type Role struct {
	Name                string   `json:"name"`
	Title               string   `json:"title,omitempty"`
	Description         string   `json:"description,omitempty"`
	IncludedPermissions []string `json:"includedPermissions,omitempty"`
	Stage               string   `json:"stage,omitempty"`
	Deleted             bool     `json:"deleted,omitempty"`
	Etag                string   `json:"etag,omitempty"`
}

// ServiceAccountKey replica el recurso "ServiceAccountKey". privateKeyData
// sólo se devuelve en la respuesta de creación (igual que la API real); en
// list/get se omite. Las claves son ficticias (no son material criptográfico
// real), suficientes para que el flujo de gcloud/Terraform funcione.
type ServiceAccountKey struct {
	Name            string `json:"name"`
	PrivateKeyType  string `json:"privateKeyType,omitempty"`
	KeyAlgorithm    string `json:"keyAlgorithm,omitempty"`
	PrivateKeyData  string `json:"privateKeyData,omitempty"`
	PublicKeyData   string `json:"publicKeyData,omitempty"`
	ValidAfterTime  string `json:"validAfterTime,omitempty"`
	ValidBeforeTime string `json:"validBeforeTime,omitempty"`
	KeyOrigin       string `json:"keyOrigin,omitempty"`
	KeyType         string `json:"keyType,omitempty"`
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
	// Go's net/http mux no permite mezclar texto literal con un wildcard
	// dentro del mismo segmento (p. ej. "{project}:getIamPolicy"), así que
	// capturamos el segmento completo y separamos la acción nosotros mismos.
	mux.HandleFunc("POST /v1/projects/{projectAction}", s.projectPolicyAction)

	// Roles predefinidos básicos, sólo lectura (lista estática).
	mux.HandleFunc("GET /v1/roles", s.listPredefinedRoles)

	// Custom roles a nivel de proyecto.
	mux.HandleFunc("POST /v1/projects/{project}/roles", s.createCustomRole)
	mux.HandleFunc("GET /v1/projects/{project}/roles", s.listCustomRoles)
	mux.HandleFunc("GET /v1/projects/{project}/roles/{role}", s.getCustomRole)
	mux.HandleFunc("PATCH /v1/projects/{project}/roles/{role}", s.updateCustomRole)
	mux.HandleFunc("DELETE /v1/projects/{project}/roles/{role}", s.deleteCustomRole)
	// "{role}:undelete" -- mismo patrón de segmento completo + strings.Cut.
	mux.HandleFunc("POST /v1/projects/{project}/roles/{roleAction}", s.roleAction)

	// Service account keys.
	mux.HandleFunc("POST /v1/projects/{project}/serviceAccounts/{account}/keys", s.createServiceAccountKey)
	mux.HandleFunc("GET /v1/projects/{project}/serviceAccounts/{account}/keys", s.listServiceAccountKeys)
	mux.HandleFunc("GET /v1/projects/{project}/serviceAccounts/{account}/keys/{key}", s.getServiceAccountKey)
	mux.HandleFunc("DELETE /v1/projects/{project}/serviceAccounts/{account}/keys/{key}", s.deleteServiceAccountKey)

	// IAM policy a nivel de service account (resource-level binding).
	mux.HandleFunc("POST /v1/projects/{project}/serviceAccounts/{accountAction}", s.serviceAccountPolicyAction)
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
	email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", body.AccountID, project)
	var existing ServiceAccount
	found, err := s.db.Get(bucketServiceAccounts, email, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "service account ya existe: "+email)
		return
	}
	s.seq++
	sa := ServiceAccount{
		Name:        fmt.Sprintf("projects/%s/serviceAccounts/%s@%s.iam.gserviceaccount.com", project, body.AccountID, project),
		ProjectID:   project,
		UniqueID:    fmt.Sprintf("%d%d", time.Now().Unix(), s.seq),
		Email:       email,
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

// projectPolicyAction despacha POST /v1/projects/{project}:getIamPolicy y
// :setIamPolicy, ya que ambos comparten el mismo segmento de ruta.
func (s *Service) projectPolicyAction(w http.ResponseWriter, r *http.Request) {
	projectAction := r.PathValue("projectAction")
	project, action, ok := strings.Cut(projectAction, ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	switch action {
	case "getIamPolicy":
		s.getProjectPolicy(w, r, project)
	case "setIamPolicy":
		s.setProjectPolicy(w, r, project)
	default:
		server.WriteError(w, 404, "NOT_FOUND", "acción no soportada: "+action)
	}
}

func (s *Service) getProjectPolicy(w http.ResponseWriter, r *http.Request, project string) {
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

func (s *Service) setProjectPolicy(w http.ResponseWriter, r *http.Request, project string) {
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

func customRoleName(project, roleID string) string {
	return fmt.Sprintf("projects/%s/roles/%s", project, roleID)
}

// createCustomRole replica POST .../roles, cuyo body real es
// {"roleId": "...", "role": {...}} (CreateRoleRequest).
func (s *Service) createCustomRole(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		RoleID string `json:"roleId"`
		Role   Role   `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.RoleID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "roleId es requerido")
		return
	}
	role := body.Role
	role.Name = customRoleName(project, body.RoleID)
	var existingRole Role
	found, err := s.db.Get(bucketCustomRoles, role.Name, &existingRole)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "custom role ya existe: "+role.Name)
		return
	}
	if role.Stage == "" {
		role.Stage = "GA"
	}
	role.Etag = fmt.Sprintf("etag-%d", time.Now().UnixNano())
	if err := s.db.Put(bucketCustomRoles, role.Name, role); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, role)
}

func (s *Service) listCustomRoles(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := fmt.Sprintf("projects/%s/roles/", project)
	roles := []Role{}
	_ = s.db.List(bucketCustomRoles, prefix, func(key string, raw []byte) error {
		var role Role
		if err := json.Unmarshal(raw, &role); err != nil {
			return err
		}
		roles = append(roles, role)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"roles": roles})
}

func (s *Service) getCustomRole(w http.ResponseWriter, r *http.Request) {
	name := customRoleName(r.PathValue("project"), r.PathValue("role"))
	var role Role
	found, err := s.db.Get(bucketCustomRoles, name, &role)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "role no encontrado")
		return
	}
	server.WriteJSON(w, 200, role)
}

// updateCustomRole replica PATCH .../roles/{role}: el body es el recurso
// Role con los campos a actualizar (se aplica como reemplazo simple de
// title/description/includedPermissions/stage, sin soportar updateMask
// parcial, suficiente para gcloud/Terraform).
func (s *Service) updateCustomRole(w http.ResponseWriter, r *http.Request) {
	name := customRoleName(r.PathValue("project"), r.PathValue("role"))
	var existing Role
	found, err := s.db.Get(bucketCustomRoles, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "role no encontrado")
		return
	}
	var body Role
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	body.Name = name
	body.Deleted = existing.Deleted
	if body.Stage == "" {
		body.Stage = existing.Stage
	}
	body.Etag = fmt.Sprintf("etag-%d", time.Now().UnixNano())
	if err := s.db.Put(bucketCustomRoles, name, body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, body)
}

// deleteCustomRole hace soft-delete (igual que la API real, que conserva
// el rol marcado como deleted durante un periodo de gracia antes de
// purgarlo); aquí no se purga nunca, pero :undelete funciona.
func (s *Service) deleteCustomRole(w http.ResponseWriter, r *http.Request) {
	name := customRoleName(r.PathValue("project"), r.PathValue("role"))
	var role Role
	found, err := s.db.Get(bucketCustomRoles, name, &role)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "role no encontrado")
		return
	}
	role.Deleted = true
	role.Etag = fmt.Sprintf("etag-%d", time.Now().UnixNano())
	if err := s.db.Put(bucketCustomRoles, name, role); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, role)
}

// roleAction despacha POST /v1/projects/{project}/roles/{role}:undelete.
func (s *Service) roleAction(w http.ResponseWriter, r *http.Request) {
	roleID, action, ok := strings.Cut(r.PathValue("roleAction"), ":")
	if !ok || action != "undelete" {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	name := customRoleName(r.PathValue("project"), roleID)
	var role Role
	found, err := s.db.Get(bucketCustomRoles, name, &role)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "role no encontrado")
		return
	}
	role.Deleted = false
	role.Etag = fmt.Sprintf("etag-%d", time.Now().UnixNano())
	if err := s.db.Put(bucketCustomRoles, name, role); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, role)
}

func saKeyName(project, email, keyID string) string {
	return fmt.Sprintf("projects/%s/serviceAccounts/%s/keys/%s", project, email, keyID)
}

// fakeKeyData genera un blob base64 con forma de archivo de credenciales
// JSON real, pero con material criptográfico ficticio: suficiente para que
// las herramientas que sólo parsean el JSON (gcloud, Terraform) funcionen,
// sin pretender ser una clave válida para firmar tokens reales.
func fakeKeyData(project, email, keyID string) string {
	cred := map[string]string{
		"type":           "service_account",
		"project_id":     project,
		"private_key_id": keyID,
		"private_key":    "-----BEGIN PRIVATE KEY-----\nFAKE-EMULATOR-KEY-NOT-REAL\n-----END PRIVATE KEY-----\n",
		"client_email":   email,
		"client_id":      keyID,
		"token_uri":      "https://oauth2.googleapis.com/token",
	}
	raw, _ := json.Marshal(cred)
	return base64.StdEncoding.EncodeToString(raw)
}

func (s *Service) createServiceAccountKey(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	email := emailFromPathValue(r.PathValue("account"), project)

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

	s.seq++
	keyID := fmt.Sprintf("%d%d", time.Now().Unix(), s.seq)
	now := time.Now().UTC()
	key := ServiceAccountKey{
		Name:            saKeyName(project, email, keyID),
		PrivateKeyType:  "TYPE_GOOGLE_CREDENTIALS_FILE",
		KeyAlgorithm:    "KEY_ALG_RSA_2048",
		PrivateKeyData:  fakeKeyData(project, email, keyID),
		PublicKeyData:   base64.StdEncoding.EncodeToString([]byte("-----BEGIN PUBLIC KEY-----\nFAKE-EMULATOR-KEY-NOT-REAL\n-----END PUBLIC KEY-----\n")),
		ValidAfterTime:  now.Format(time.RFC3339),
		ValidBeforeTime: now.AddDate(10, 0, 0).Format(time.RFC3339),
		KeyOrigin:       "GOOGLE_PROVIDED",
		KeyType:         "USER_MANAGED",
	}
	if err := s.db.Put(bucketSAKeys, key.Name, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, key)
}

func (s *Service) listServiceAccountKeys(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	email := emailFromPathValue(r.PathValue("account"), project)
	prefix := fmt.Sprintf("projects/%s/serviceAccounts/%s/keys/", project, email)
	keys := []ServiceAccountKey{}
	_ = s.db.List(bucketSAKeys, prefix, func(key string, raw []byte) error {
		var k ServiceAccountKey
		if err := json.Unmarshal(raw, &k); err != nil {
			return err
		}
		k.PrivateKeyData = "" // la API real tampoco la devuelve en list
		keys = append(keys, k)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"keys": keys})
}

func (s *Service) getServiceAccountKey(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	email := emailFromPathValue(r.PathValue("account"), project)
	name := saKeyName(project, email, r.PathValue("key"))
	var key ServiceAccountKey
	found, err := s.db.Get(bucketSAKeys, name, &key)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "key no encontrada")
		return
	}
	key.PrivateKeyData = "" // la API real tampoco la devuelve en get
	server.WriteJSON(w, 200, key)
}

func (s *Service) deleteServiceAccountKey(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	email := emailFromPathValue(r.PathValue("account"), project)
	name := saKeyName(project, email, r.PathValue("key"))
	if err := s.db.Delete(bucketSAKeys, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

// serviceAccountPolicyAction despacha
// POST /v1/projects/{project}/serviceAccounts/{account}:getIamPolicy y
// :setIamPolicy (IAM binding a nivel de recurso, sobre la propia SA).
func (s *Service) serviceAccountPolicyAction(w http.ResponseWriter, r *http.Request) {
	accountAction := r.PathValue("accountAction")
	account, action, ok := strings.Cut(accountAction, ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	project := r.PathValue("project")
	email := emailFromPathValue(account, project)
	switch action {
	case "getIamPolicy":
		s.getServiceAccountPolicy(w, email)
	case "setIamPolicy":
		s.setServiceAccountPolicy(w, r, email)
	default:
		server.WriteError(w, 404, "NOT_FOUND", "acción no soportada: "+action)
	}
}

func (s *Service) getServiceAccountPolicy(w http.ResponseWriter, email string) {
	var policy Policy
	found, err := s.db.Get(bucketSAPolicies, email, &policy)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		policy = Policy{Version: 1, Bindings: []Binding{}, Etag: "initial"}
	}
	server.WriteJSON(w, 200, policy)
}

func (s *Service) setServiceAccountPolicy(w http.ResponseWriter, r *http.Request, email string) {
	var body struct {
		Policy Policy `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	body.Policy.Etag = fmt.Sprintf("etag-%d", time.Now().UnixNano())
	if err := s.db.Put(bucketSAPolicies, email, body.Policy); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, body.Policy)
}
