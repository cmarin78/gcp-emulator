// Package kms emula un subconjunto de Cloud KMS (cloudkms.googleapis.com/v1):
// keyrings y cryptokeys. Replica una particularidad real de la API: ni los
// keyrings ni las cryptokeys se pueden borrar (KMS no tiene delete para
// estos recursos, solo destrucción de versiones de clave o deshabilitar);
// por eso aquí no se registra ningún DELETE para estos recursos, igual que
// en la API real.
package kms

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketKeyRings  = "kms.keyrings"
	bucketCryptoKey = "kms.cryptokeys"
)

// KeyRing replica el subconjunto relevante de
// google.cloud.kms.v1.KeyRing.
type KeyRing struct {
	Name       string `json:"name"` // projects/{p}/locations/{l}/keyRings/{keyRing}
	CreateTime string `json:"createTime"`
}

// CryptoKey replica el subconjunto relevante de
// google.cloud.kms.v1.CryptoKey.
type CryptoKey struct {
	Name            string            `json:"name"` // .../keyRings/{keyRing}/cryptoKeys/{cryptoKey}
	Purpose         string            `json:"purpose"`
	CreateTime      string            `json:"createTime"`
	Labels          map[string]string `json:"labels,omitempty"`
	VersionTemplate map[string]string `json:"versionTemplate,omitempty"`
	Primary         *CryptoKeyVersion `json:"primary,omitempty"`
}

// CryptoKeyVersion replica el subconjunto relevante de
// google.cloud.kms.v1.CryptoKeyVersion, usado aquí solo como el campo
// "primary" embebido en CryptoKey (versión 1, siempre ENABLED).
type CryptoKeyVersion struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type Svc struct {
	db *storage.DB
}

func New(db *storage.DB) *Svc {
	return &Svc{db: db}
}

func (s *Svc) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings", s.createKeyRing)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings", s.listKeyRings)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}", s.getKeyRing)

	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys", s.createCryptoKey)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys", s.listCryptoKeys)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}", s.getCryptoKey)
	mux.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}", s.patchCryptoKey)

	// cryptoKeyVersions: tampoco hay delete real; "destroy" programa la
	// destrucción (state -> DESTROY_SCHEDULED). Terraform usa esto para
	// implementar el destroy de google_kms_crypto_key (la key en sí
	// nunca se borra, solo sus versiones).
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions", s.listCryptoKeyVersions)
	// "{version}:destroy" no se puede expresar como patrón mixto en el mux
	// de Go; se captura el segmento completo y se separa con strings.Cut
	// (mismo patrón usado en IAM/Pub-Sub/Secret Manager).
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{versionAction}", s.getCryptoKeyVersion)
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{versionAction}", s.versionAction)
}

func keyRingName(project, location, keyRing string) string {
	return fmt.Sprintf("projects/%s/locations/%s/keyRings/%s", project, location, keyRing)
}

func cryptoKeyName(project, location, keyRing, cryptoKey string) string {
	return fmt.Sprintf("%s/cryptoKeys/%s", keyRingName(project, location, keyRing), cryptoKey)
}

func (s *Svc) createKeyRing(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	keyRingID := r.URL.Query().Get("keyRingId")
	if keyRingID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "keyRingId es obligatorio")
		return
	}
	name := keyRingName(project, location, keyRingID)
	var existing KeyRing
	if found, _ := s.db.Get(bucketKeyRings, name, &existing); found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "el keyRing ya existe")
		return
	}
	kr := KeyRing{Name: name, CreateTime: time.Now().UTC().Format(time.RFC3339)}
	if err := s.db.Put(bucketKeyRings, name, kr); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, kr)
}

func (s *Svc) listKeyRings(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/%s/keyRings/", r.PathValue("project"), r.PathValue("location"))
	items := []KeyRing{}
	_ = s.db.List(bucketKeyRings, prefix, func(key string, raw []byte) error {
		var kr KeyRing
		if err := json.Unmarshal(raw, &kr); err != nil {
			return err
		}
		items = append(items, kr)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"keyRings": items})
}

func (s *Svc) getKeyRing(w http.ResponseWriter, r *http.Request) {
	name := keyRingName(r.PathValue("project"), r.PathValue("location"), r.PathValue("keyRing"))
	var kr KeyRing
	found, err := s.db.Get(bucketKeyRings, name, &kr)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "keyRing no encontrado")
		return
	}
	server.WriteJSON(w, 200, kr)
}

func (s *Svc) createCryptoKey(w http.ResponseWriter, r *http.Request) {
	project, location, keyRing := r.PathValue("project"), r.PathValue("location"), r.PathValue("keyRing")
	if found, _ := s.db.Get(bucketKeyRings, keyRingName(project, location, keyRing), new(KeyRing)); !found {
		server.WriteError(w, 404, "NOT_FOUND", "keyRing no encontrado")
		return
	}
	cryptoKeyID := r.URL.Query().Get("cryptoKeyId")
	if cryptoKeyID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "cryptoKeyId es obligatorio")
		return
	}
	var body struct {
		Purpose         string            `json:"purpose"`
		Labels          map[string]string `json:"labels"`
		VersionTemplate map[string]string `json:"versionTemplate"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := cryptoKeyName(project, location, keyRing, cryptoKeyID)
	var existing CryptoKey
	if found, _ := s.db.Get(bucketCryptoKey, name, &existing); found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "la cryptoKey ya existe")
		return
	}
	ck := CryptoKey{
		Name:            name,
		Purpose:         orDefault(body.Purpose, "ENCRYPT_DECRYPT"),
		CreateTime:      time.Now().UTC().Format(time.RFC3339),
		Labels:          body.Labels,
		VersionTemplate: body.VersionTemplate,
		Primary:         &CryptoKeyVersion{Name: name + "/cryptoKeyVersions/1", State: "ENABLED"},
	}
	if err := s.db.Put(bucketCryptoKey, name, ck); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, ck)
}

func (s *Svc) listCryptoKeys(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("%s/cryptoKeys/", keyRingName(r.PathValue("project"), r.PathValue("location"), r.PathValue("keyRing")))
	items := []CryptoKey{}
	_ = s.db.List(bucketCryptoKey, prefix, func(key string, raw []byte) error {
		var ck CryptoKey
		if err := json.Unmarshal(raw, &ck); err != nil {
			return err
		}
		items = append(items, ck)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"cryptoKeys": items})
}

func (s *Svc) getCryptoKey(w http.ResponseWriter, r *http.Request) {
	name := cryptoKeyName(r.PathValue("project"), r.PathValue("location"), r.PathValue("keyRing"), r.PathValue("cryptoKey"))
	var ck CryptoKey
	found, err := s.db.Get(bucketCryptoKey, name, &ck)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cryptoKey no encontrada")
		return
	}
	server.WriteJSON(w, 200, ck)
}

func (s *Svc) patchCryptoKey(w http.ResponseWriter, r *http.Request) {
	name := cryptoKeyName(r.PathValue("project"), r.PathValue("location"), r.PathValue("keyRing"), r.PathValue("cryptoKey"))
	var existing CryptoKey
	found, err := s.db.Get(bucketCryptoKey, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cryptoKey no encontrada")
		return
	}
	var body struct {
		Labels map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Labels != nil {
		existing.Labels = body.Labels
	}
	if err := s.db.Put(bucketCryptoKey, name, existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Svc) listCryptoKeyVersions(w http.ResponseWriter, r *http.Request) {
	name := cryptoKeyName(r.PathValue("project"), r.PathValue("location"), r.PathValue("keyRing"), r.PathValue("cryptoKey"))
	var ck CryptoKey
	found, err := s.db.Get(bucketCryptoKey, name, &ck)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "cryptoKey no encontrada")
		return
	}
	versions := []CryptoKeyVersion{}
	if ck.Primary != nil {
		versions = append(versions, *ck.Primary)
	}
	server.WriteJSON(w, 200, map[string]any{"cryptoKeyVersions": versions})
}

func (s *Svc) getCryptoKeyVersion(w http.ResponseWriter, r *http.Request) {
	version, _, _ := strings.Cut(r.PathValue("versionAction"), ":")
	name := cryptoKeyName(r.PathValue("project"), r.PathValue("location"), r.PathValue("keyRing"), r.PathValue("cryptoKey"))
	var ck CryptoKey
	found, err := s.db.Get(bucketCryptoKey, name, &ck)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found || ck.Primary == nil || !strings.HasSuffix(ck.Primary.Name, "/"+version) {
		server.WriteError(w, 404, "NOT_FOUND", "cryptoKeyVersion no encontrada")
		return
	}
	server.WriteJSON(w, 200, *ck.Primary)
}

// versionAction despacha acciones sobre cryptoKeyVersions invocadas vía
// POST .../cryptoKeyVersions/{version}:accion. Por ahora solo se modela
// ":destroy" (programa la destrucción, state -> DESTROY_SCHEDULED), que
// es lo que usa Terraform al hacer destroy de google_kms_crypto_key.
func (s *Svc) versionAction(w http.ResponseWriter, r *http.Request) {
	_, action, _ := strings.Cut(r.PathValue("versionAction"), ":")
	if action != "destroy" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "acción no soportada: "+action)
		return
	}
	name := cryptoKeyName(r.PathValue("project"), r.PathValue("location"), r.PathValue("keyRing"), r.PathValue("cryptoKey"))
	var ck CryptoKey
	found, err := s.db.Get(bucketCryptoKey, name, &ck)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found || ck.Primary == nil {
		server.WriteError(w, 404, "NOT_FOUND", "cryptoKeyVersion no encontrada")
		return
	}
	ck.Primary.State = "DESTROY_SCHEDULED"
	if err := s.db.Put(bucketCryptoKey, name, ck); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, *ck.Primary)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
