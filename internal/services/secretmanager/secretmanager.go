// Package secretmanager emula un subconjunto de la API de Secret Manager
// (secretmanager.googleapis.com/v1): secrets y versions. Los payloads de
// las versiones se guardan en base64 (igual que la API real) en BoltDB.
package secretmanager

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

const (
	bucketSecrets  = "secretmanager.secrets"
	bucketVersions = "secretmanager.versions"
)

// Secret replica el recurso "Secret".
type Secret struct {
	Name       string            `json:"name"` // projects/{project}/secrets/{secret}
	Labels     map[string]string `json:"labels,omitempty"`
	CreateTime string            `json:"createTime,omitempty"`
	LatestVer  int64             `json:"-"` // último número de versión asignado (interno)
}

// SecretVersion replica el recurso "SecretVersion" (sin el payload).
type SecretVersion struct {
	Name       string `json:"name"` // projects/{project}/secrets/{secret}/versions/{version}
	State      string `json:"state"`
	CreateTime string `json:"createTime,omitempty"`
}

// secretVersionRecord es lo que se persiste: la versión más su payload.
type secretVersionRecord struct {
	SecretVersion
	DataB64 string `json:"dataB64"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service {
	return &Service{db: db}
}

// Register monta las rutas de Secret Manager, siguiendo los paths reales
// de secretmanager.googleapis.com.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/secrets", s.createSecret)
	mux.HandleFunc("GET /v1/projects/{project}/secrets", s.listSecrets)
	mux.HandleFunc("GET /v1/projects/{project}/secrets/{secret}", s.getSecret)
	mux.HandleFunc("DELETE /v1/projects/{project}/secrets/{secret}", s.deleteSecret)
	// "{secret}:addVersion" no se puede expresar como un patrón mixto en
	// el mux de Go; se captura el segmento completo y se separa con
	// strings.Cut (mismo patrón usado en IAM y Pub/Sub).
	mux.HandleFunc("POST /v1/projects/{project}/secrets/{secretAction}", s.secretAction)

	mux.HandleFunc("GET /v1/projects/{project}/secrets/{secret}/versions", s.listVersions)
	// "{version}" puede ser un número o "latest"; ":access" se separa igual
	// que arriba, capturando el segmento completo.
	mux.HandleFunc("GET /v1/projects/{project}/secrets/{secret}/versions/{versionAction}", s.versionGetOrAccess)
	mux.HandleFunc("POST /v1/projects/{project}/secrets/{secret}/versions/{versionAction}", s.versionDestroy)
}

func secretName(project, secret string) string {
	return fmt.Sprintf("projects/%s/secrets/%s", project, secret)
}

func versionName(project, secret, version string) string {
	return fmt.Sprintf("projects/%s/secrets/%s/versions/%s", project, secret, version)
}

func (s *Service) createSecret(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	secretID := r.URL.Query().Get("secretId")
	if secretID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "secretId es requerido")
		return
	}
	var body struct {
		Labels map[string]string `json:"labels"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	sec := Secret{
		Name:       secretName(project, secretID),
		Labels:     body.Labels,
		CreateTime: time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.db.Put(bucketSecrets, sec.Name, sec); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, sec)
}

func (s *Service) listSecrets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := fmt.Sprintf("projects/%s/secrets/", project)
	secrets := []Secret{}
	_ = s.db.List(bucketSecrets, prefix, func(key string, raw []byte) error {
		var sec Secret
		if err := json.Unmarshal(raw, &sec); err != nil {
			return err
		}
		secrets = append(secrets, sec)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"secrets": secrets})
}

func (s *Service) getSecret(w http.ResponseWriter, r *http.Request) {
	name := secretName(r.PathValue("project"), r.PathValue("secret"))
	var sec Secret
	found, err := s.db.Get(bucketSecrets, name, &sec)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "secret no encontrado")
		return
	}
	server.WriteJSON(w, 200, sec)
}

func (s *Service) deleteSecret(w http.ResponseWriter, r *http.Request) {
	name := secretName(r.PathValue("project"), r.PathValue("secret"))
	if err := s.db.Delete(bucketSecrets, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Service) secretAction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	secretID, action, ok := strings.Cut(r.PathValue("secretAction"), ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	switch action {
	case "addVersion":
		s.addVersion(w, r, project, secretID)
	default:
		server.WriteError(w, 404, "NOT_FOUND", "acción no soportada: "+action)
	}
}

func (s *Service) addVersion(w http.ResponseWriter, r *http.Request, project, secretID string) {
	sName := secretName(project, secretID)
	var sec Secret
	found, err := s.db.Get(bucketSecrets, sName, &sec)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "secret no encontrado")
		return
	}

	var body struct {
		Payload struct {
			Data string `json:"data"` // base64
		} `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if _, err := base64.StdEncoding.DecodeString(body.Payload.Data); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "payload.data debe ser base64 válido")
		return
	}

	s.seq++
	verNum := s.seq
	rec := secretVersionRecord{
		SecretVersion: SecretVersion{
			Name:       versionName(project, secretID, fmt.Sprintf("%d", verNum)),
			State:      "ENABLED",
			CreateTime: time.Now().UTC().Format(time.RFC3339),
		},
		DataB64: body.Payload.Data,
	}
	if err := s.db.Put(bucketVersions, rec.Name, rec); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, rec.SecretVersion)
}

func (s *Service) listVersions(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	secretID := r.PathValue("secret")
	prefix := fmt.Sprintf("projects/%s/secrets/%s/versions/", project, secretID)
	versions := []SecretVersion{}
	_ = s.db.List(bucketVersions, prefix, func(key string, raw []byte) error {
		var rec secretVersionRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return err
		}
		versions = append(versions, rec.SecretVersion)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"versions": versions})
}

// resolveVersion convierte "latest" en el número de versión más alto
// existente para ese secret; si ya es numérico, lo devuelve tal cual.
func (s *Service) resolveVersion(project, secretID, version string) (string, error) {
	if version != "latest" {
		return version, nil
	}
	prefix := fmt.Sprintf("projects/%s/secrets/%s/versions/", project, secretID)
	var latest int64
	err := s.db.List(bucketVersions, prefix, func(key string, raw []byte) error {
		var rec secretVersionRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return err
		}
		numStr := strings.TrimPrefix(rec.Name, prefix)
		var n int64
		fmt.Sscanf(numStr, "%d", &n)
		if n > latest {
			latest = n
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if latest == 0 {
		return "", fmt.Errorf("no hay versiones")
	}
	return fmt.Sprintf("%d", latest), nil
}

func (s *Service) versionGetOrAccess(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	secretID := r.PathValue("secret")
	versionSeg, action, hasAction := strings.Cut(r.PathValue("versionAction"), ":")

	version := versionSeg
	resolved, err := s.resolveVersion(project, secretID, version)
	if err != nil {
		server.WriteError(w, 404, "NOT_FOUND", "no hay versiones para este secret")
		return
	}
	version = resolved

	name := versionName(project, secretID, version)
	var rec secretVersionRecord
	found, err := s.db.Get(bucketVersions, name, &rec)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "version no encontrada")
		return
	}

	if hasAction && action == "access" {
		server.WriteJSON(w, 200, map[string]any{
			"name": rec.Name,
			"payload": map[string]any{
				"data": rec.DataB64,
			},
		})
		return
	}
	server.WriteJSON(w, 200, rec.SecretVersion)
}

func (s *Service) versionDestroy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	secretID := r.PathValue("secret")
	versionSeg, action, ok := strings.Cut(r.PathValue("versionAction"), ":")
	if !ok || action != "destroy" {
		server.WriteError(w, 404, "NOT_FOUND", "acción no soportada")
		return
	}
	version, err := s.resolveVersion(project, secretID, versionSeg)
	if err != nil {
		server.WriteError(w, 404, "NOT_FOUND", "no hay versiones para este secret")
		return
	}
	name := versionName(project, secretID, version)
	var rec secretVersionRecord
	found, err := s.db.Get(bucketVersions, name, &rec)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "version no encontrada")
		return
	}
	rec.State = "DESTROYED"
	rec.DataB64 = ""
	if err := s.db.Put(bucketVersions, name, rec); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, rec.SecretVersion)
}
