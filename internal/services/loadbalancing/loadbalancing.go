// Package loadbalancing emula un subconjunto de los recursos globales de
// HTTP(S) Load Balancing dentro de compute.googleapis.com/v1: healthChecks,
// backendServices, urlMaps, targetHttpProxies/targetHttpsProxies y
// forwardingRules (todos en su variante global, sin regional/SSL todavía).
// Como con el resto del emulador, esto es "shape-compatible, no
// behavior-complete": no hay proxy real de tráfico ni health checking
// activo, solo el grafo de recursos y referencias que Terraform/gcloud
// esperan. Las mutaciones reutilizan internal/server.Operations, el mismo
// helper de Operation síncrono que ya usa el paquete compute, para que
// gcloud (que hace polling con selfLink absoluto) funcione sin parches.
package loadbalancing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketHealthChecks    = "loadbalancing.healthChecks"
	bucketBackendServices = "loadbalancing.backendServices"
	bucketURLMaps         = "loadbalancing.urlMaps"
	bucketTargetHTTP      = "loadbalancing.targetHttpProxies"
	bucketTargetHTTPS     = "loadbalancing.targetHttpsProxies"
	bucketForwardingRules = "loadbalancing.forwardingRules"
)

type HealthCheck struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Type              string          `json:"type,omitempty"`
	CheckIntervalSec  int64           `json:"checkIntervalSec,omitempty"`
	TimeoutSec        int64           `json:"timeoutSec,omitempty"`
	HTTPHealthCheck   json.RawMessage `json:"httpHealthCheck,omitempty"`
	CreationTimestamp string          `json:"creationTimestamp"`
	SelfLink          string          `json:"selfLink"`
}

type BackendService struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Protocol          string          `json:"protocol,omitempty"`
	PortName          string          `json:"portName,omitempty"`
	TimeoutSec        int64           `json:"timeoutSec,omitempty"`
	HealthChecks      []string        `json:"healthChecks,omitempty"`
	Backends          json.RawMessage `json:"backends,omitempty"`
	LoadBalancingScheme string        `json:"loadBalancingScheme,omitempty"`
	CreationTimestamp string          `json:"creationTimestamp"`
	SelfLink          string          `json:"selfLink"`
}

type URLMap struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	DefaultService    string          `json:"defaultService,omitempty"`
	HostRules         json.RawMessage `json:"hostRules,omitempty"`
	PathMatchers      json.RawMessage `json:"pathMatchers,omitempty"`
	CreationTimestamp string          `json:"creationTimestamp"`
	SelfLink          string          `json:"selfLink"`
}

type TargetHTTPProxy struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	URLMap            string `json:"urlMap,omitempty"`
	CreationTimestamp string `json:"creationTimestamp"`
	SelfLink          string `json:"selfLink"`
}

type TargetHTTPSProxy struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	URLMap            string   `json:"urlMap,omitempty"`
	SSLCertificates   []string `json:"sslCertificates,omitempty"`
	CreationTimestamp string   `json:"creationTimestamp"`
	SelfLink          string   `json:"selfLink"`
}

type ForwardingRule struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	IPAddress         string `json:"IPAddress,omitempty"`
	IPProtocol        string `json:"IPProtocol,omitempty"`
	PortRange         string `json:"portRange,omitempty"`
	Target            string `json:"target,omitempty"`
	LoadBalancingScheme string `json:"loadBalancingScheme,omitempty"`
	CreationTimestamp string `json:"creationTimestamp"`
	SelfLink          string `json:"selfLink"`
}

type Service struct {
	db  *storage.DB
	ops *server.Operations
	seq int64
}

func New(db *storage.DB) *Service {
	return &Service{db: db, ops: server.NewOperations()}
}

func (s *Service) nextSeq() int64 {
	s.seq++
	return s.seq
}

// opsBase replica el mismo helper que internal/services/compute/network.go:
// gcloud necesita un selfLink de Operation absoluto para poder resolverlo
// vía resources.Parse sin "collection=" explícito.
func opsBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/compute/v1", scheme, r.Host)
}

func opsCollection(r *http.Request, project string) string {
	return fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project)
}

// Register monta las rutas de Load Balancing bajo el mismo prefijo real de
// Compute (compute.googleapis.com/v1), en sus variantes globales.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /compute/v1/projects/{project}/global/healthChecks", s.insertHealthCheck)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/healthChecks", s.listHealthChecks)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/healthChecks/{healthCheck}", s.getHealthCheck)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/healthChecks/{healthCheck}", s.deleteHealthCheck)

	mux.HandleFunc("POST /compute/v1/projects/{project}/global/backendServices", s.insertBackendService)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/backendServices", s.listBackendServices)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/backendServices/{backendService}", s.getBackendService)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/backendServices/{backendService}", s.deleteBackendService)

	mux.HandleFunc("POST /compute/v1/projects/{project}/global/urlMaps", s.insertURLMap)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/urlMaps", s.listURLMaps)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/urlMaps/{urlMap}", s.getURLMap)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/urlMaps/{urlMap}", s.deleteURLMap)

	mux.HandleFunc("POST /compute/v1/projects/{project}/global/targetHttpProxies", s.insertTargetHTTPProxy)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/targetHttpProxies", s.listTargetHTTPProxies)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/targetHttpProxies/{targetHttpProxy}", s.getTargetHTTPProxy)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/targetHttpProxies/{targetHttpProxy}", s.deleteTargetHTTPProxy)

	mux.HandleFunc("POST /compute/v1/projects/{project}/global/targetHttpsProxies", s.insertTargetHTTPSProxy)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/targetHttpsProxies", s.listTargetHTTPSProxies)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/targetHttpsProxies/{targetHttpsProxy}", s.getTargetHTTPSProxy)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/targetHttpsProxies/{targetHttpsProxy}", s.deleteTargetHTTPSProxy)

	mux.HandleFunc("POST /compute/v1/projects/{project}/global/forwardingRules", s.insertForwardingRule)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/forwardingRules", s.listForwardingRules)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/forwardingRules/{forwardingRule}", s.getForwardingRule)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/forwardingRules/{forwardingRule}", s.deleteForwardingRule)
}

func selfLink(project, kind, name string) string {
	return fmt.Sprintf("/compute/v1/projects/%s/global/%s/%s", project, kind, name)
}

// --- healthChecks ---

func (s *Service) insertHealthCheck(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name             string          `json:"name"`
		Type             string          `json:"type"`
		CheckIntervalSec int64           `json:"checkIntervalSec"`
		TimeoutSec       int64           `json:"timeoutSec"`
		HTTPHealthCheck  json.RawMessage `json:"httpHealthCheck"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	hc := HealthCheck{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		Type:              body.Type,
		CheckIntervalSec:  body.CheckIntervalSec,
		TimeoutSec:        body.TimeoutSec,
		HTTPHealthCheck:   body.HTTPHealthCheck,
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          selfLink(project, "healthChecks", body.Name),
	}
	if err := s.db.Put(bucketHealthChecks, hc.Name, hc); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", hc.SelfLink, opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listHealthChecks(w http.ResponseWriter, r *http.Request) {
	items := []HealthCheck{}
	_ = s.db.List(bucketHealthChecks, "", func(key string, raw []byte) error {
		var hc HealthCheck
		if err := json.Unmarshal(raw, &hc); err != nil {
			return err
		}
		items = append(items, hc)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#healthCheckList", "items": items})
}

func (s *Service) getHealthCheck(w http.ResponseWriter, r *http.Request) {
	var hc HealthCheck
	found, err := s.db.Get(bucketHealthChecks, r.PathValue("healthCheck"), &hc)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "health check no encontrado")
		return
	}
	server.WriteJSON(w, 200, hc)
}

func (s *Service) deleteHealthCheck(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("healthCheck")
	if err := s.db.Delete(bucketHealthChecks, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", selfLink(project, "healthChecks", name), opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

// --- backendServices ---

func (s *Service) insertBackendService(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name                string          `json:"name"`
		Protocol            string          `json:"protocol"`
		PortName            string          `json:"portName"`
		TimeoutSec          int64           `json:"timeoutSec"`
		HealthChecks        []string        `json:"healthChecks"`
		Backends            json.RawMessage `json:"backends"`
		LoadBalancingScheme string          `json:"loadBalancingScheme"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	bs := BackendService{
		ID:                  fmt.Sprintf("%d", s.nextSeq()),
		Name:                body.Name,
		Protocol:            body.Protocol,
		PortName:            body.PortName,
		TimeoutSec:          body.TimeoutSec,
		HealthChecks:        body.HealthChecks,
		Backends:            body.Backends,
		LoadBalancingScheme: body.LoadBalancingScheme,
		CreationTimestamp:   time.Now().UTC().Format(time.RFC3339),
		SelfLink:            selfLink(project, "backendServices", body.Name),
	}
	if err := s.db.Put(bucketBackendServices, bs.Name, bs); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", bs.SelfLink, opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listBackendServices(w http.ResponseWriter, r *http.Request) {
	items := []BackendService{}
	_ = s.db.List(bucketBackendServices, "", func(key string, raw []byte) error {
		var bs BackendService
		if err := json.Unmarshal(raw, &bs); err != nil {
			return err
		}
		items = append(items, bs)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#backendServiceList", "items": items})
}

func (s *Service) getBackendService(w http.ResponseWriter, r *http.Request) {
	var bs BackendService
	found, err := s.db.Get(bucketBackendServices, r.PathValue("backendService"), &bs)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "backend service no encontrado")
		return
	}
	server.WriteJSON(w, 200, bs)
}

func (s *Service) deleteBackendService(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("backendService")
	if err := s.db.Delete(bucketBackendServices, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", selfLink(project, "backendServices", name), opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

// --- urlMaps ---

func (s *Service) insertURLMap(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name           string          `json:"name"`
		DefaultService string          `json:"defaultService"`
		HostRules      json.RawMessage `json:"hostRules"`
		PathMatchers   json.RawMessage `json:"pathMatchers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	um := URLMap{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		DefaultService:    body.DefaultService,
		HostRules:         body.HostRules,
		PathMatchers:      body.PathMatchers,
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          selfLink(project, "urlMaps", body.Name),
	}
	if err := s.db.Put(bucketURLMaps, um.Name, um); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", um.SelfLink, opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listURLMaps(w http.ResponseWriter, r *http.Request) {
	items := []URLMap{}
	_ = s.db.List(bucketURLMaps, "", func(key string, raw []byte) error {
		var um URLMap
		if err := json.Unmarshal(raw, &um); err != nil {
			return err
		}
		items = append(items, um)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#urlMapList", "items": items})
}

func (s *Service) getURLMap(w http.ResponseWriter, r *http.Request) {
	var um URLMap
	found, err := s.db.Get(bucketURLMaps, r.PathValue("urlMap"), &um)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "url map no encontrado")
		return
	}
	server.WriteJSON(w, 200, um)
}

func (s *Service) deleteURLMap(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("urlMap")
	if err := s.db.Delete(bucketURLMaps, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", selfLink(project, "urlMaps", name), opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

// --- targetHttpProxies ---

func (s *Service) insertTargetHTTPProxy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name   string `json:"name"`
		URLMap string `json:"urlMap"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	tp := TargetHTTPProxy{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		URLMap:            body.URLMap,
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          selfLink(project, "targetHttpProxies", body.Name),
	}
	if err := s.db.Put(bucketTargetHTTP, tp.Name, tp); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", tp.SelfLink, opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listTargetHTTPProxies(w http.ResponseWriter, r *http.Request) {
	items := []TargetHTTPProxy{}
	_ = s.db.List(bucketTargetHTTP, "", func(key string, raw []byte) error {
		var tp TargetHTTPProxy
		if err := json.Unmarshal(raw, &tp); err != nil {
			return err
		}
		items = append(items, tp)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#targetHttpProxyList", "items": items})
}

func (s *Service) getTargetHTTPProxy(w http.ResponseWriter, r *http.Request) {
	var tp TargetHTTPProxy
	found, err := s.db.Get(bucketTargetHTTP, r.PathValue("targetHttpProxy"), &tp)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "target http proxy no encontrado")
		return
	}
	server.WriteJSON(w, 200, tp)
}

func (s *Service) deleteTargetHTTPProxy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("targetHttpProxy")
	if err := s.db.Delete(bucketTargetHTTP, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", selfLink(project, "targetHttpProxies", name), opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

// --- targetHttpsProxies ---

func (s *Service) insertTargetHTTPSProxy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name            string   `json:"name"`
		URLMap          string   `json:"urlMap"`
		SSLCertificates []string `json:"sslCertificates"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	tp := TargetHTTPSProxy{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		URLMap:            body.URLMap,
		SSLCertificates:   body.SSLCertificates,
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          selfLink(project, "targetHttpsProxies", body.Name),
	}
	if err := s.db.Put(bucketTargetHTTPS, tp.Name, tp); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", tp.SelfLink, opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listTargetHTTPSProxies(w http.ResponseWriter, r *http.Request) {
	items := []TargetHTTPSProxy{}
	_ = s.db.List(bucketTargetHTTPS, "", func(key string, raw []byte) error {
		var tp TargetHTTPSProxy
		if err := json.Unmarshal(raw, &tp); err != nil {
			return err
		}
		items = append(items, tp)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#targetHttpsProxyList", "items": items})
}

func (s *Service) getTargetHTTPSProxy(w http.ResponseWriter, r *http.Request) {
	var tp TargetHTTPSProxy
	found, err := s.db.Get(bucketTargetHTTPS, r.PathValue("targetHttpsProxy"), &tp)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "target https proxy no encontrado")
		return
	}
	server.WriteJSON(w, 200, tp)
}

func (s *Service) deleteTargetHTTPSProxy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("targetHttpsProxy")
	if err := s.db.Delete(bucketTargetHTTPS, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", selfLink(project, "targetHttpsProxies", name), opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

// --- forwardingRules ---

func (s *Service) insertForwardingRule(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name                string `json:"name"`
		IPAddress           string `json:"IPAddress"`
		IPProtocol          string `json:"IPProtocol"`
		PortRange           string `json:"portRange"`
		Target              string `json:"target"`
		LoadBalancingScheme string `json:"loadBalancingScheme"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	ip := body.IPAddress
	if ip == "" {
		// IP fake determinística, suficiente para que Terraform/gcloud
		// tengan un valor no vacío que leer; no hay red real detrás.
		ip = fmt.Sprintf("10.10.%d.%d", (s.seq+1)/255, (s.seq+1)%255)
	}
	fr := ForwardingRule{
		ID:                  fmt.Sprintf("%d", s.nextSeq()),
		Name:                body.Name,
		IPAddress:           ip,
		IPProtocol:          body.IPProtocol,
		PortRange:           body.PortRange,
		Target:              body.Target,
		LoadBalancingScheme: body.LoadBalancingScheme,
		CreationTimestamp:   time.Now().UTC().Format(time.RFC3339),
		SelfLink:            selfLink(project, "forwardingRules", body.Name),
	}
	if err := s.db.Put(bucketForwardingRules, fr.Name, fr); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", fr.SelfLink, opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listForwardingRules(w http.ResponseWriter, r *http.Request) {
	items := []ForwardingRule{}
	_ = s.db.List(bucketForwardingRules, "", func(key string, raw []byte) error {
		var fr ForwardingRule
		if err := json.Unmarshal(raw, &fr); err != nil {
			return err
		}
		items = append(items, fr)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#forwardingRuleList", "items": items})
}

func (s *Service) getForwardingRule(w http.ResponseWriter, r *http.Request) {
	var fr ForwardingRule
	found, err := s.db.Get(bucketForwardingRules, r.PathValue("forwardingRule"), &fr)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "forwarding rule no encontrada")
		return
	}
	server.WriteJSON(w, 200, fr)
}

func (s *Service) deleteForwardingRule(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("forwardingRule")
	if err := s.db.Delete(bucketForwardingRules, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", selfLink(project, "forwardingRules", name), opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}
