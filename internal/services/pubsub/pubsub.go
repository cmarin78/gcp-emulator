// Package pubsub emula un subconjunto de la API de Cloud Pub/Sub
// (pubsub.googleapis.com/v1): topics, subscriptions, publish/pull/ack.
// Topics y subscriptions se persisten en BoltDB; la cola de mensajes
// pendientes vive en memoria (alcanza para flujos típicos de
// gcloud/Terraform, pero no sobrevive a un restart del emulador).
//
// Fase 11 (capa de comportamiento): una subscription con pushConfig.
// pushEndpoint ahora entrega de verdad — publish() hace un HTTP POST real
// al endpoint configurado con el wire format estándar de Pub/Sub push, en
// vez de solo encolar el mensaje para pull. Las subscriptions sin
// pushConfig siguen funcionando exactamente igual que antes (pull/ack).
package pubsub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketTopics        = "pubsub.topics"
	bucketSubscriptions = "pubsub.subscriptions"
)

// Topic replica el recurso "Topic" de la API real.
type Topic struct {
	Name   string            `json:"name"` // projects/{project}/topics/{topic}
	Labels map[string]string `json:"labels,omitempty"`
}

// PushConfig replica pubsub#PushConfig (subset).
type PushConfig struct {
	PushEndpoint string            `json:"pushEndpoint"`
	Attributes   map[string]string `json:"attributes,omitempty"`
}

// Subscription replica el recurso "Subscription".
type Subscription struct {
	Name               string            `json:"name"` // projects/{project}/subscriptions/{subscription}
	Topic              string            `json:"topic"`
	AckDeadlineSeconds int               `json:"ackDeadlineSeconds,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	PushConfig         *PushConfig       `json:"pushConfig,omitempty"`
}

// Message es el mensaje publicado, tal como lo devuelve la API (data en base64).
type Message struct {
	Data        string            `json:"data,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	MessageID   string            `json:"messageId"`
	PublishTime string            `json:"publishTime"`
}

// ReceivedMessage es lo que devuelve subscriptions.pull: el mensaje más
// un ackId que el cliente usa para confirmar la recepción.
type ReceivedMessage struct {
	AckID   string  `json:"ackId"`
	Message Message `json:"message"`
}

type Service struct {
	db         *storage.DB
	seq        int64
	httpClient *http.Client

	mu      sync.Mutex
	pending map[string][]ReceivedMessage // subscription name -> mensajes sin entregar
}

func New(db *storage.DB) *Service {
	return &Service{
		db:         db,
		pending:    make(map[string][]ReceivedMessage),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Register monta las rutas de Pub/Sub en el mux, siguiendo los paths
// reales de pubsub.googleapis.com.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("PUT /v1/projects/{project}/topics/{topic}", s.createTopic)
	mux.HandleFunc("GET /v1/projects/{project}/topics", s.listTopics)
	mux.HandleFunc("GET /v1/projects/{project}/topics/{topic}", s.getTopic)
	mux.HandleFunc("DELETE /v1/projects/{project}/topics/{topic}", s.deleteTopic)
	// Go's mux no permite mezclar texto literal con wildcard en el mismo
	// segmento (p. ej. "{topic}:publish"), así que capturamos el segmento
	// completo y separamos la acción nosotros mismos (igual que IAM).
	mux.HandleFunc("POST /v1/projects/{project}/topics/{topicAction}", s.topicAction)

	mux.HandleFunc("PUT /v1/projects/{project}/subscriptions/{subscription}", s.createSubscription)
	mux.HandleFunc("GET /v1/projects/{project}/subscriptions", s.listSubscriptions)
	mux.HandleFunc("GET /v1/projects/{project}/subscriptions/{subscription}", s.getSubscription)
	mux.HandleFunc("DELETE /v1/projects/{project}/subscriptions/{subscription}", s.deleteSubscription)
	mux.HandleFunc("POST /v1/projects/{project}/subscriptions/{subscriptionAction}", s.subscriptionAction)
}

func topicName(project, topic string) string {
	return fmt.Sprintf("projects/%s/topics/%s", project, topic)
}

func subscriptionName(project, sub string) string {
	return fmt.Sprintf("projects/%s/subscriptions/%s", project, sub)
}

func (s *Service) createTopic(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	topic := r.PathValue("topic")
	var body struct {
		Labels map[string]string `json:"labels"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := topicName(project, topic)
	var existing Topic
	found, err := s.db.Get(bucketTopics, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "topic ya existe: "+name)
		return
	}
	t := Topic{Name: name, Labels: body.Labels}
	if err := s.db.Put(bucketTopics, t.Name, t); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, t)
}

func (s *Service) listTopics(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := fmt.Sprintf("projects/%s/topics/", project)
	topics := []Topic{}
	_ = s.db.List(bucketTopics, prefix, func(key string, raw []byte) error {
		var t Topic
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		topics = append(topics, t)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"topics": topics})
}

func (s *Service) getTopic(w http.ResponseWriter, r *http.Request) {
	name := topicName(r.PathValue("project"), r.PathValue("topic"))
	var t Topic
	found, err := s.db.Get(bucketTopics, name, &t)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "topic no encontrado")
		return
	}
	server.WriteJSON(w, 200, t)
}

func (s *Service) deleteTopic(w http.ResponseWriter, r *http.Request) {
	name := topicName(r.PathValue("project"), r.PathValue("topic"))
	if err := s.db.Delete(bucketTopics, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Service) topicAction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	topic, action, ok := strings.Cut(r.PathValue("topicAction"), ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	switch action {
	case "publish":
		s.publish(w, r, project, topic)
	default:
		server.WriteError(w, 404, "NOT_FOUND", "acción no soportada: "+action)
	}
}

func (s *Service) publish(w http.ResponseWriter, r *http.Request, project, topic string) {
	var body struct {
		Messages []struct {
			Data       string            `json:"data"`
			Attributes map[string]string `json:"attributes"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	tName := topicName(project, topic)

	var t Topic
	found, err := s.db.Get(bucketTopics, tName, &t)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "topic no encontrado")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var subs []Subscription
	_ = s.db.List(bucketSubscriptions, fmt.Sprintf("projects/%s/subscriptions/", project), func(key string, raw []byte) error {
		var sub Subscription
		if err := json.Unmarshal(raw, &sub); err != nil {
			return err
		}
		if sub.Topic == tName {
			subs = append(subs, sub)
		}
		return nil
	})

	now := time.Now().UTC().Format(time.RFC3339)
	ids := make([]string, 0, len(body.Messages))
	for _, m := range body.Messages {
		s.seq++
		msg := Message{
			Data:        m.Data,
			Attributes:  m.Attributes,
			MessageID:   fmt.Sprintf("%d", s.seq),
			PublishTime: now,
		}
		ids = append(ids, msg.MessageID)
		for _, sub := range subs {
			if sub.PushConfig != nil && sub.PushConfig.PushEndpoint != "" {
				// Subscription de push: entrega real por HTTP, sin pasar
				// por la cola de pull (igual que la API real).
				go s.deliverPush(sub, msg)
				continue
			}
			s.seq++
			s.pending[sub.Name] = append(s.pending[sub.Name], ReceivedMessage{
				AckID:   fmt.Sprintf("ack-%d", s.seq),
				Message: msg,
			})
		}
	}
	server.WriteJSON(w, 200, map[string]any{"messageIds": ids})
}

// deliverPush hace un HTTP POST real al pushEndpoint de la subscription,
// con el wire format estándar de Pub/Sub push:
// {"message": {...}, "subscription": "projects/.../subscriptions/..."}.
func (s *Service) deliverPush(sub Subscription, msg Message) {
	payload, err := json.Marshal(map[string]any{
		"message":      msg,
		"subscription": sub.Name,
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", sub.PushConfig.PushEndpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range sub.PushConfig.Attributes {
		req.Header.Set(k, v)
	}
	if resp, err := s.httpClient.Do(req); err == nil {
		resp.Body.Close()
	}
	// Sin reintentos ni dead-lettering: si el endpoint falla, el mensaje se
	// pierde (a diferencia de la API real). Documentado como límite en el
	// ROADMAP — agregar retry/backoff queda para una próxima iteración.
}

func (s *Service) createSubscription(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	subID := r.PathValue("subscription")
	var body struct {
		Topic              string            `json:"topic"`
		AckDeadlineSeconds int               `json:"ackDeadlineSeconds"`
		Labels             map[string]string `json:"labels"`
		PushConfig         *PushConfig       `json:"pushConfig"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Topic == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "topic es requerido")
		return
	}
	name := subscriptionName(project, subID)
	var existingSub Subscription
	found, err := s.db.Get(bucketSubscriptions, name, &existingSub)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "subscription ya existe: "+name)
		return
	}
	var topicExists Topic
	found, err = s.db.Get(bucketTopics, body.Topic, &topicExists)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "topic no encontrado: "+body.Topic)
		return
	}
	ackDeadline := body.AckDeadlineSeconds
	if ackDeadline == 0 {
		ackDeadline = 10
	}
	sub := Subscription{
		Name:               name,
		Topic:              body.Topic,
		AckDeadlineSeconds: ackDeadline,
		Labels:             body.Labels,
		PushConfig:         body.PushConfig,
	}
	if err := s.db.Put(bucketSubscriptions, sub.Name, sub); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, sub)
}

func (s *Service) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := fmt.Sprintf("projects/%s/subscriptions/", project)
	subs := []Subscription{}
	_ = s.db.List(bucketSubscriptions, prefix, func(key string, raw []byte) error {
		var sub Subscription
		if err := json.Unmarshal(raw, &sub); err != nil {
			return err
		}
		subs = append(subs, sub)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"subscriptions": subs})
}

func (s *Service) getSubscription(w http.ResponseWriter, r *http.Request) {
	name := subscriptionName(r.PathValue("project"), r.PathValue("subscription"))
	var sub Subscription
	found, err := s.db.Get(bucketSubscriptions, name, &sub)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "subscription no encontrada")
		return
	}
	server.WriteJSON(w, 200, sub)
}

func (s *Service) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	name := subscriptionName(r.PathValue("project"), r.PathValue("subscription"))
	if err := s.db.Delete(bucketSubscriptions, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.mu.Lock()
	delete(s.pending, name)
	s.mu.Unlock()
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Service) subscriptionAction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	subID, action, ok := strings.Cut(r.PathValue("subscriptionAction"), ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	name := subscriptionName(project, subID)
	switch action {
	case "pull":
		s.pull(w, r, name)
	case "acknowledge":
		s.acknowledge(w, r, name)
	case "modifyPushConfig":
		s.modifyPushConfig(w, r, name)
	default:
		server.WriteError(w, 404, "NOT_FOUND", "acción no soportada: "+action)
	}
}

func (s *Service) modifyPushConfig(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		PushConfig *PushConfig `json:"pushConfig"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	var sub Subscription
	found, err := s.db.Get(bucketSubscriptions, name, &sub)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "subscription no encontrada")
		return
	}
	// pushConfig vacío ({}) vuelve la subscription a modo pull, igual que la API real.
	if body.PushConfig == nil || body.PushConfig.PushEndpoint == "" {
		sub.PushConfig = nil
	} else {
		sub.PushConfig = body.PushConfig
	}
	if err := s.db.Put(bucketSubscriptions, name, sub); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Service) pull(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		MaxMessages int `json:"maxMessages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	max := body.MaxMessages
	if max <= 0 {
		max = 10
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	queue := s.pending[name]
	n := max
	if n > len(queue) {
		n = len(queue)
	}
	received := queue[:n]
	s.pending[name] = queue[n:]

	server.WriteJSON(w, 200, map[string]any{"receivedMessages": received})
}

func (s *Service) acknowledge(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		AckIDs []string `json:"ackIds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// Los mensajes ya se removieron de la cola en pull(); acknowledge solo
	// confirma la recepción (no hay redelivery por ackDeadline en este emulador).
	server.WriteJSON(w, 200, map[string]any{})
}
