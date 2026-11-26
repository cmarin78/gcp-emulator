package gcs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

const bucketNotifications = "gcs.notificationConfigs"

// NotificationConfig represents a Cloud Storage notification configuration for Pub/Sub.
type NotificationConfig struct {
	Kind             string   `json:"kind,omitempty"`
	ID               string   `json:"id"`
	Topic            string   `json:"topic"` // //pubsub.googleapis.com/projects/{project}/topics/{topic} or projects/{project}/topics/{topic}
	EventTypes       []string `json:"event_types,omitempty"` // OBJECT_FINALIZE, OBJECT_DELETE, etc.
	PayloadFormat    string   `json:"payload_format,omitempty"` // JSON_API_V1 or NONE
	ObjectNamePrefix string   `json:"object_name_prefix,omitempty"`
	Etag             string   `json:"etag,omitempty"`
	SelfLink         string   `json:"selfLink,omitempty"`
}

// CloudEvent mirrors the basic CloudEvents envelope dispatched for Eventarc.
type CloudEvent struct {
	ID              string            `json:"id"`
	Source          string            `json:"source"`
	Type            string            `json:"type"`
	DataContentType string            `json:"datacontenttype,omitempty"`
	Attributes      map[string]string `json:"attributes,omitempty"`
	Data            json.RawMessage   `json:"data,omitempty"`
}

type PubSubPublisher func(project, topic string, data []byte, attributes map[string]string) error
type CloudEventPublisher func(e CloudEvent) error

func notificationKey(bucket, id string) string {
	return bucket + "/" + id
}

func (s *Service) nextNotificationSeq() int64 {
	return atomic.AddInt64(&s.notificationSeq, 1)
}

func (s *Service) SetPubSubPublisher(fn PubSubPublisher) {
	s.pubsubPublisher = fn
}

func (s *Service) SetCloudEventPublisher(fn CloudEventPublisher) {
	s.eventarcPublisher = fn
}

func (s *Service) registerNotificationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /storage/v1/b/{bucket}/notificationConfigs", s.createNotificationConfig)
	mux.HandleFunc("GET /storage/v1/b/{bucket}/notificationConfigs", s.listNotificationConfigs)
	mux.HandleFunc("GET /storage/v1/b/{bucket}/notificationConfigs/{notification}", s.getNotificationConfig)
	mux.HandleFunc("DELETE /storage/v1/b/{bucket}/notificationConfigs/{notification}", s.deleteNotificationConfig)
}

func (s *Service) createNotificationConfig(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")

	var b Bucket
	found, err := s.db.Get(bucketBuckets, bucket, &b)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "bucket no encontrado")
		return
	}

	var body NotificationConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Topic == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "topic es requerido")
		return
	}

	id := fmt.Sprintf("%d", s.nextNotificationSeq())
	cfg := NotificationConfig{
		Kind:             "storage#notificationConfig",
		ID:               id,
		Topic:            body.Topic,
		EventTypes:       body.EventTypes,
		PayloadFormat:    orDefault(body.PayloadFormat, "JSON_API_V1"),
		ObjectNamePrefix: body.ObjectNamePrefix,
		Etag:             fmt.Sprintf("etag-notif-%s", id),
		SelfLink:         fmt.Sprintf("/storage/v1/b/%s/notificationConfigs/%s", bucket, id),
	}

	if err := s.db.Put(bucketNotifications, notificationKey(bucket, id), cfg); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, cfg)
}

func (s *Service) getNotificationConfig(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	id := r.PathValue("notification")

	var cfg NotificationConfig
	found, err := s.db.Get(bucketNotifications, notificationKey(bucket, id), &cfg)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "notificationConfig no encontrada")
		return
	}
	server.WriteJSON(w, 200, cfg)
}

func (s *Service) listNotificationConfigs(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	prefix := bucket + "/"

	var items []NotificationConfig
	err := s.db.List(bucketNotifications, prefix, func(key string, raw []byte) error {
		var cfg NotificationConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		items = append(items, cfg)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if items == nil {
		items = []NotificationConfig{}
	}
	server.WriteJSON(w, 200, map[string]any{
		"kind":  "storage#notificationConfigs",
		"items": items,
	})
}

func (s *Service) deleteNotificationConfig(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	id := r.PathValue("notification")

	key := notificationKey(bucket, id)
	var cfg NotificationConfig
	found, err := s.db.Get(bucketNotifications, key, &cfg)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "notificationConfig no encontrada")
		return
	}

	if err := s.db.Delete(bucketNotifications, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// notifyObjectEvent triggers Pub/Sub and Eventarc events for an object lifecycle action.
func (s *Service) notifyObjectEvent(bucket string, obj Object, eventType string) {
	// 1. Pub/Sub Notification Configs
	if s.pubsubPublisher != nil {
		prefix := bucket + "/"
		_ = s.db.List(bucketNotifications, prefix, func(key string, raw []byte) error {
			var cfg NotificationConfig
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return nil
			}

			// Filter by objectNamePrefix if specified
			if cfg.ObjectNamePrefix != "" && !strings.HasPrefix(obj.Name, cfg.ObjectNamePrefix) {
				return nil
			}

			// Filter by event_types if specified
			if len(cfg.EventTypes) > 0 {
				matched := false
				for _, et := range cfg.EventTypes {
					if et == eventType {
						matched = true
						break
					}
				}
				if !matched {
					return nil
				}
			}

			// Parse project and topic from format "//pubsub.googleapis.com/projects/{project}/topics/{topic}" or "projects/{project}/topics/{topic}"
			topicStr := strings.TrimPrefix(cfg.Topic, "//pubsub.googleapis.com/")
			parts := strings.Split(topicStr, "/")
			if len(parts) >= 4 && parts[0] == "projects" && parts[2] == "topics" {
				project := parts[1]
				topic := parts[3]

				attrs := map[string]string{
					"eventType":        eventType,
					"bucketId":         bucket,
					"objectId":         obj.Name,
					"objectGeneration": obj.Updated,
					"payloadFormat":    cfg.PayloadFormat,
				}

				var data []byte
				if cfg.PayloadFormat != "NONE" {
					data, _ = json.Marshal(obj)
				}

				_ = s.pubsubPublisher(project, topic, data, attrs)
			}
			return nil
		})
	}

	// 2. Eventarc CloudEvent dispatch
	if s.eventarcPublisher != nil {
		cloudEventType := "google.cloud.storage.object.v1.finalized"
		if eventType == "OBJECT_DELETE" {
			cloudEventType = "google.cloud.storage.object.v1.deleted"
		}
		data, _ := json.Marshal(obj)
		ce := CloudEvent{
			ID:              fmt.Sprintf("gcs-event-%s-%d", bucket, time.Now().UnixNano()),
			Source:          fmt.Sprintf("//storage.googleapis.com/projects/_/buckets/%s", bucket),
			Type:            cloudEventType,
			DataContentType: "application/json",
			Attributes: map[string]string{
				"bucket": bucket,
				"object": obj.Name,
			},
			Data: data,
		}
		_ = s.eventarcPublisher(ce)
	}
}
