// Fase 17: Cloud Resource Manager folders.
// google_folder + google_folder_iam_policy CRUD.
// Folders follow the same pattern as projects but with a numeric ID
// (auto-assigned) and support IAM policy binding at the folder level.
package resourcemanager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

const (
	bucketFolders    = "resourcemanager.folders"
	bucketFolderIAM  = "resourcemanager.folder_iam"
)

// Folder replica google.cloud.resourcemanager.v3.Folder.
type Folder struct {
	Name        string `json:"name"`        // folders/{folder_id}
	Parent      string `json:"parent"`      // "organizations/{n}" or "folders/{n}"
	DisplayName string `json:"displayName,omitempty"`
	State       string `json:"state"`       // ACTIVE | DELETE_REQUESTED
	CreateTime  string `json:"createTime,omitempty"`
	UpdateTime  string `json:"updateTime,omitempty"`
	Etag        string `json:"etag,omitempty"`
}

// FolderIAMPolicy holds IAM bindings for a folder.
type FolderIAMPolicy struct {
	Bindings []FolderIAMBinding `json:"bindings,omitempty"`
	Etag     string             `json:"etag"`
	Version  int                `json:"version,omitempty"`
}

// FolderIAMBinding is one role→members mapping.
type FolderIAMBinding struct {
	Role    string   `json:"role"`
	Members []string `json:"members"`
}

// ── Route registration ─────────────────────────────────────────────────────────

func (s *Service) registerFolders(mux *http.ServeMux) {
	mux.HandleFunc("POST /v3/folders", s.createFolder)
	mux.HandleFunc("GET /v3/folders", s.listFolders)
	mux.HandleFunc("GET /v3/folders/{folder}", s.getFolder)
	mux.HandleFunc("PATCH /v3/folders/{folder}", s.patchFolder)
	mux.HandleFunc("DELETE /v3/folders/{folder}", s.deleteFolder)

	// IAM policy — verb suffix on the folder resource.
	// "POST /v3/folders/{folderAction}" captures "123:getIamPolicy" etc.
	mux.HandleFunc("POST /v3/folders/{folderAction}", s.folderIAMAction)
}

// ── CRUD ──────────────────────────────────────────────────────────────────────

func (s *Service) createFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Parent      string `json:"parent"`
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if body.Parent == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "parent is required (organizations/{n} or folders/{n})")
		return
	}
	if body.DisplayName == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "displayName is required")
		return
	}

	s.seq++
	folderID := fmt.Sprintf("%d", s.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	folder := Folder{
		Name:        "folders/" + folderID,
		Parent:      body.Parent,
		DisplayName: body.DisplayName,
		State:       "ACTIVE",
		CreateTime:  now,
		UpdateTime:  now,
		Etag:        fmt.Sprintf("etag-%d", time.Now().UnixNano()),
	}
	if err := s.db.Put(bucketFolders, folder.Name, folder); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}

	respBytes, _ := json.Marshal(folder)
	s.seq++
	op := Operation{
		Name:     operationName(fmt.Sprintf("op-%d", s.seq)),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) listFolders(w http.ResponseWriter, r *http.Request) {
	parent := r.URL.Query().Get("parent")
	var folders []Folder
	_ = s.db.List(bucketFolders, "folders/", func(_ string, raw []byte) error {
		var f Folder
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		// Filter by parent if provided.
		if parent == "" || f.Parent == parent {
			folders = append(folders, f)
		}
		return nil
	})
	if folders == nil {
		folders = []Folder{}
	}
	server.WriteJSON(w, 200, map[string]any{"folders": folders})
}

func (s *Service) getFolder(w http.ResponseWriter, r *http.Request) {
	name := "folders/" + r.PathValue("folder")
	var folder Folder
	found, err := s.db.Get(bucketFolders, name, &folder)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "folder not found: "+name)
		return
	}
	server.WriteJSON(w, 200, folder)
}

func (s *Service) patchFolder(w http.ResponseWriter, r *http.Request) {
	name := "folders/" + r.PathValue("folder")
	var existing Folder
	found, err := s.db.Get(bucketFolders, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "folder not found: "+name)
		return
	}
	var body struct {
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if body.DisplayName != "" {
		existing.DisplayName = body.DisplayName
	}
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	existing.Etag = fmt.Sprintf("etag-%d", time.Now().UnixNano())
	if err := s.db.Put(bucketFolders, name, existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Service) deleteFolder(w http.ResponseWriter, r *http.Request) {
	name := "folders/" + r.PathValue("folder")
	var existing Folder
	found, err := s.db.Get(bucketFolders, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "folder not found: "+name)
		return
	}
	existing.State = "DELETE_REQUESTED"
	existing.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	_ = s.db.Put(bucketFolders, name, existing)

	respBytes, _ := json.Marshal(existing)
	s.seq++
	op := Operation{
		Name:     operationName(fmt.Sprintf("op-%d", s.seq)),
		Done:     true,
		Response: respBytes,
	}
	server.WriteJSON(w, 200, op)
}

// ── IAM policy ────────────────────────────────────────────────────────────────

// folderIAMAction handles POST /v3/folders/{folderAction} where the path
// segment contains "{folderID}:{action}" (e.g. "123:getIamPolicy").
func (s *Service) folderIAMAction(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("folderAction")
	folderID, action, ok := strings.Cut(raw, ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	name := "folders/" + folderID

	switch action {
	case "getIamPolicy":
		s.getFolderIAMPolicy(w, name)
	case "setIamPolicy":
		s.setFolderIAMPolicy(w, r, name)
	default:
		server.WriteError(w, 400, "INVALID_ARGUMENT", "acción no soportada: "+action)
	}
}

func (s *Service) getFolderIAMPolicy(w http.ResponseWriter, name string) {
	var policy FolderIAMPolicy
	found, err := s.db.Get(bucketFolderIAM, name, &policy)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		policy = FolderIAMPolicy{Bindings: []FolderIAMBinding{}, Etag: "initial", Version: 1}
	}
	server.WriteJSON(w, 200, policy)
}

func (s *Service) setFolderIAMPolicy(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Policy FolderIAMPolicy `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	body.Policy.Etag = fmt.Sprintf("etag-%d", time.Now().UnixNano())
	if body.Policy.Version == 0 {
		body.Policy.Version = 1
	}
	if err := s.db.Put(bucketFolderIAM, name, body.Policy); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, body.Policy)
}
