// Package gcs emula un subconjunto de la API JSON de Cloud Storage
// (storage.googleapis.com/storage/v1), suficiente para crear/listar/borrar
// buckets y subir/descargar/listar/borrar objetos vía gcloud storage,
// gsutil o el SDK oficial.
package gcs

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketBuckets = "gcs.buckets"
const bucketObjects = "gcs.objects"
const bucketIAMPolicies = "gcs.bucket_iam"

type Bucket struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	ProjectID    string `json:"projectNumber,omitempty"`
	Location     string `json:"location,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
	TimeCreated  string `json:"timeCreated"`
	SelfLink     string `json:"selfLink"`
}

// IAMPolicy replica el shape de la política IAM de un bucket
// (GET/PUT /storage/v1/b/{bucket}/iam). Es un tipo propio, independiente
// del paquete iam, siguiendo la convención de servicios autocontenidos.
type IAMPolicy struct {
	Kind     string       `json:"kind"`
	Bindings []IAMBinding `json:"bindings"`
	Etag     string       `json:"etag"`
	Version  int          `json:"version,omitempty"`
}

type IAMBinding struct {
	Role    string   `json:"role"`
	Members []string `json:"members"`
}

type Object struct {
	Name             string            `json:"name"`
	Bucket           string            `json:"bucket"`
	ID               string            `json:"id"`
	ContentType      string            `json:"contentType,omitempty"`
	ContentLanguage  string            `json:"contentLanguage,omitempty"`
	ContentEncoding  string            `json:"contentEncoding,omitempty"`
	CacheControl     string            `json:"cacheControl,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Size             string            `json:"size"`
	TimeCreated      string            `json:"timeCreated"`
	Updated          string            `json:"updated"`
	SelfLink         string            `json:"selfLink"`
	MediaLink        string            `json:"mediaLink"`
	ContentB64       string            `json:"-"` // contenido real, no se serializa en metadata responses
}

type Service struct {
	db                *storage.DB
	notificationSeq   int64
	pubsubPublisher   PubSubPublisher
	eventarcPublisher CloudEventPublisher
}

func New(db *storage.DB) *Service {
	return &Service{db: db}
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /storage/v1/b", s.createBucket)
	mux.HandleFunc("GET /storage/v1/b", s.listBuckets)
	mux.HandleFunc("GET /storage/v1/b/{bucket}", s.getBucket)
	mux.HandleFunc("DELETE /storage/v1/b/{bucket}", s.deleteBucket)

	mux.HandleFunc("GET /storage/v1/b/{bucket}/o", s.listObjects)
	mux.HandleFunc("GET /storage/v1/b/{bucket}/o/{object...}", s.getObjectOrDownload)
	mux.HandleFunc("DELETE /storage/v1/b/{bucket}/o/{object...}", s.deleteObject)

	// Endpoint de "simple upload" (uploadType=media), como en
	// https://storage.googleapis.com/upload/storage/v1/b/{bucket}/o
	mux.HandleFunc("POST /upload/storage/v1/b/{bucket}/o", s.uploadObject)

	// IAM binding a nivel de bucket (resource-level IAM), igual que la
	// API real: GET/PUT /storage/v1/b/{bucket}/iam.
	mux.HandleFunc("GET /storage/v1/b/{bucket}/iam", s.getBucketIamPolicy)
	mux.HandleFunc("PUT /storage/v1/b/{bucket}/iam", s.setBucketIamPolicy)

	// Fase 17: PATCH objeto (actualizar metadatos), compose.
	mux.HandleFunc("PATCH /storage/v1/b/{bucket}/o/{object...}", s.patchObject)
	mux.HandleFunc("POST /storage/v1/b/{bucket}/o/{destinationObject...}", s.composeObjects)

	// Fase 24: NotificationConfigs para eventos reactivos a Pub/Sub
	s.registerNotificationRoutes(mux)
}

func (s *Service) createBucket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string `json:"name"`
		Location     string `json:"location"`
		StorageClass string `json:"storageClass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	var existing Bucket
	found, err := s.db.Get(bucketBuckets, body.Name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "bucket ya existe: "+body.Name)
		return
	}
	b := Bucket{
		Name:         body.Name,
		ID:           body.Name,
		Location:     orDefault(body.Location, "US"),
		StorageClass: orDefault(body.StorageClass, "STANDARD"),
		TimeCreated:  time.Now().UTC().Format(time.RFC3339),
		SelfLink:     fmt.Sprintf("/storage/v1/b/%s", body.Name),
	}
	if err := s.db.Put(bucketBuckets, b.Name, b); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, b)
}

func (s *Service) listBuckets(w http.ResponseWriter, r *http.Request) {
	var items []Bucket
	_ = s.db.List(bucketBuckets, "", func(key string, raw []byte) error {
		var b Bucket
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		items = append(items, b)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "storage#buckets", "items": items})
}

func (s *Service) getBucket(w http.ResponseWriter, r *http.Request) {
	var b Bucket
	found, err := s.db.Get(bucketBuckets, r.PathValue("bucket"), &b)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "bucket no encontrado")
		return
	}
	server.WriteJSON(w, 200, b)
}

func (s *Service) deleteBucket(w http.ResponseWriter, r *http.Request) {
	if err := s.db.Delete(bucketBuckets, r.PathValue("bucket")); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func objectKey(bucket, name string) string {
	return bucket + "/" + name
}

func (s *Service) uploadObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	name := r.URL.Query().Get("name")
	if name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "el parámetro 'name' es requerido (uploadType=media)")
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	obj := Object{
		Name:        name,
		Bucket:      bucket,
		ID:          fmt.Sprintf("%s/%s", bucket, name),
		ContentType: contentType,
		Size:        fmt.Sprintf("%d", len(data)),
		TimeCreated: now,
		Updated:     now,
		SelfLink:    fmt.Sprintf("/storage/v1/b/%s/o/%s", bucket, name),
		MediaLink:   fmt.Sprintf("/storage/v1/b/%s/o/%s?alt=media", bucket, name),
		ContentB64:  base64.StdEncoding.EncodeToString(data),
	}
	if err := s.putObject(obj); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.notifyObjectEvent(bucket, obj, "OBJECT_FINALIZE")
	server.WriteJSON(w, 200, obj)
}

// putObject persiste metadata + contenido. Usamos un wrapper JSON propio
// porque Object.ContentB64 tiene json:"-" para no filtrarlo en respuestas
// normales de metadata.
type storedObject struct {
	Object
	ContentB64 string `json:"contentB64"`
}

func (s *Service) putObject(obj Object) error {
	rec := storedObject{Object: obj, ContentB64: obj.ContentB64}
	return s.db.Put(bucketObjects, objectKey(obj.Bucket, obj.Name), rec)
}

func (s *Service) loadObject(bucket, name string) (storedObject, bool, error) {
	var rec storedObject
	found, err := s.db.Get(bucketObjects, objectKey(bucket, name), &rec)
	return rec, found, err
}

func (s *Service) listObjects(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	prefix := r.URL.Query().Get("prefix")
	var items []Object
	_ = s.db.List(bucketObjects, bucket+"/"+prefix, func(key string, raw []byte) error {
		var rec storedObject
		if err := json.Unmarshal(raw, &rec); err != nil {
			return err
		}
		items = append(items, rec.Object)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "storage#objects", "items": items})
}

func (s *Service) getObjectOrDownload(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	name := r.PathValue("object")
	rec, found, err := s.loadObject(bucket, name)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "objeto no encontrado")
		return
	}
	if r.URL.Query().Get("alt") == "media" {
		data, decErr := base64.StdEncoding.DecodeString(rec.ContentB64)
		if decErr != nil {
			server.WriteError(w, 500, "INTERNAL", decErr.Error())
			return
		}
		w.Header().Set("Content-Type", rec.Object.ContentType)
		w.Write(data)
		return
	}
	server.WriteJSON(w, 200, rec.Object)
}

func (s *Service) deleteObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	name := r.PathValue("object")
	rec, found, err := s.loadObject(bucket, name)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "objeto no encontrado")
		return
	}
	if err := s.db.Delete(bucketObjects, objectKey(bucket, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.notifyObjectEvent(bucket, rec.Object, "OBJECT_DELETE")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) getBucketIamPolicy(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	var policy IAMPolicy
	found, err := s.db.Get(bucketIAMPolicies, bucket, &policy)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		policy = IAMPolicy{Kind: "storage#policy", Bindings: []IAMBinding{}, Etag: "initial", Version: 1}
	}
	server.WriteJSON(w, 200, policy)
}

func (s *Service) setBucketIamPolicy(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	var policy IAMPolicy
	if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	policy.Kind = "storage#policy"
	policy.Etag = fmt.Sprintf("etag-%d", time.Now().UnixNano())
	if err := s.db.Put(bucketIAMPolicies, bucket, policy); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, policy)
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// ── Fase 17: PATCH object (metadata update) ───────────────────────────────────

func (s *Service) patchObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	name := r.PathValue("object")

	rec, found, err := s.loadObject(bucket, name)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "object not found")
		return
	}

	var patch Object
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}

	// Apply patchable fields only — name/bucket/size are immutable.
	if patch.ContentType != "" {
		rec.Object.ContentType = patch.ContentType
	}
	if patch.ContentLanguage != "" {
		rec.Object.ContentLanguage = patch.ContentLanguage
	}
	if patch.ContentEncoding != "" {
		rec.Object.ContentEncoding = patch.ContentEncoding
	}
	if patch.CacheControl != "" {
		rec.Object.CacheControl = patch.CacheControl
	}
	if patch.Metadata != nil {
		if rec.Object.Metadata == nil {
			rec.Object.Metadata = map[string]string{}
		}
		for k, v := range patch.Metadata {
			rec.Object.Metadata[k] = v
		}
	}
	rec.Object.Updated = time.Now().UTC().Format(time.RFC3339)
	rec.Object.ContentB64 = rec.ContentB64 // preserve stored content

	if err := s.putObject(rec.Object); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, rec.Object)
}

// ── Fase 17: compose ──────────────────────────────────────────────────────────

// composeObjects handles POST /storage/v1/b/{bucket}/o/{destination}/compose.
// The {destinationObject...} wildcard captures e.g. "my-obj/compose"; we
// strip the trailing "/compose" suffix to get the destination object name.
func (s *Service) composeObjects(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	raw := r.PathValue("destinationObject")

	// Only handle paths ending with /compose; everything else is unknown.
	dst, hasCompose := strings.CutSuffix(raw, "/compose")
	if !hasCompose {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}

	var body struct {
		SourceObjects []struct {
			Name string `json:"name"`
		} `json:"sourceObjects"`
		Destination Object `json:"destination"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if len(body.SourceObjects) == 0 {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "sourceObjects is required")
		return
	}

	// Concatenate source objects' content.
	var composed []byte
	for _, src := range body.SourceObjects {
		rec, found, _ := s.loadObject(bucket, src.Name)
		if !found {
			server.WriteError(w, 404, "NOT_FOUND", "source object not found: "+src.Name)
			return
		}
		data, _ := base64.StdEncoding.DecodeString(rec.ContentB64)
		composed = append(composed, data...)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	obj := Object{
		Name:        dst,
		Bucket:      bucket,
		ID:          bucket + "/" + dst,
		ContentType: orDefault(body.Destination.ContentType, "application/octet-stream"),
		Size:        fmt.Sprintf("%d", len(composed)),
		TimeCreated: now,
		Updated:     now,
		SelfLink:    "https://storage.googleapis.com/storage/v1/b/" + bucket + "/o/" + dst,
		MediaLink:   "https://storage.googleapis.com/download/storage/v1/b/" + bucket + "/o/" + dst + "?alt=media",
		ContentB64:  base64.StdEncoding.EncodeToString(composed),
	}
	if err := s.putObject(obj); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.notifyObjectEvent(bucket, obj, "OBJECT_FINALIZE")
	server.WriteJSON(w, 200, obj)
}
