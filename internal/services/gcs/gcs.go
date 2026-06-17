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
	Name        string `json:"name"`
	Bucket      string `json:"bucket"`
	ID          string `json:"id"`
	ContentType string `json:"contentType,omitempty"`
	Size        string `json:"size"`
	TimeCreated string `json:"timeCreated"`
	Updated     string `json:"updated"`
	SelfLink    string `json:"selfLink"`
	MediaLink   string `json:"mediaLink"`
	ContentB64  string `json:"-"` // contenido real, no se serializa en metadata responses
}

type Service struct {
	db *storage.DB
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
	if err := s.db.Delete(bucketObjects, objectKey(bucket, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
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
