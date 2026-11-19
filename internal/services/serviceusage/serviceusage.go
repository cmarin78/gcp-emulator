// Package serviceusage emulates a subset of the Service Usage API
// (serviceusage.googleapis.com/v1): enabling, disabling, getting, and listing
// services on a project (google_project_service and google_project_services in Terraform).
package serviceusage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketServices = "serviceusage.services"

// ServiceConfig holds metadata about the service.
type ServiceConfig struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
}

// Service represents an enabled/available service on a project.
type Service struct {
	Name   string        `json:"name"`   // projects/{project}/services/{service}
	State  string        `json:"state"`  // ENABLED | DISABLED
	Config ServiceConfig `json:"config"` // service definition
}

// Operation mirrors a standard google.longrunning.Operation for Service Usage.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Response json.RawMessage `json:"response,omitempty"`
}

type Svc struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Svc {
	return &Svc{db: db}
}

func (s *Svc) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/services:batchEnable", s.batchEnable)
	mux.HandleFunc("GET /v1/projects/{project}/services", s.listServices)
	mux.HandleFunc("GET /v1/projects/{project}/services/{service}", s.getService)
	mux.HandleFunc("POST /v1/projects/{project}/services/{serviceAction}", s.serviceAction)
}

func serviceKey(project, service string) string {
	return fmt.Sprintf("%s/%s", project, service)
}

func serviceResourceName(project, service string) string {
	return fmt.Sprintf("projects/%s/services/%s", project, service)
}

func (s *Svc) nextID() int64 {
	return atomic.AddInt64(&s.seq, 1)
}

func (s *Svc) batchEnable(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		ServiceIDs []string `json:"serviceIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}

	var enabledList []Service
	for _, id := range body.ServiceIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		svc := Service{
			Name:  serviceResourceName(project, id),
			State: "ENABLED",
			Config: ServiceConfig{
				Name:  id,
				Title: id,
			},
		}
		if err := s.db.Put(bucketServices, serviceKey(project, id), svc); err != nil {
			server.WriteError(w, 500, "INTERNAL", err.Error())
			return
		}
		enabledList = append(enabledList, svc)
	}

	respPayload, _ := json.Marshal(map[string]any{
		"@type":    "type.googleapis.com/google.api.serviceusage.v1.BatchEnableServicesResponse",
		"services": enabledList,
	})

	op := Operation{
		Name:     fmt.Sprintf("operations/op-serviceusage-%d", s.nextID()),
		Done:     true,
		Response: respPayload,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Svc) getService(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	serviceID := r.PathValue("service")

	var svc Service
	found, err := s.db.Get(bucketServices, serviceKey(project, serviceID), &svc)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		// By default in GCP, non-enabled services return state DISABLED rather than 404
		svc = Service{
			Name:  serviceResourceName(project, serviceID),
			State: "DISABLED",
			Config: ServiceConfig{
				Name:  serviceID,
				Title: serviceID,
			},
		}
	}
	server.WriteJSON(w, 200, svc)
}

func (s *Svc) listServices(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	filter := r.URL.Query().Get("filter")

	prefix := project + "/"
	var out []Service
	err := s.db.List(bucketServices, prefix, func(key string, raw []byte) error {
		var svc Service
		if err := json.Unmarshal(raw, &svc); err != nil {
			return err
		}
		if strings.Contains(filter, "state:ENABLED") && svc.State != "ENABLED" {
			return nil
		}
		out = append(out, svc)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if out == nil {
		out = []Service{}
	}
	server.WriteJSON(w, 200, map[string]any{
		"services": out,
	})
}

func (s *Svc) serviceAction(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	serviceID, action, ok := strings.Cut(r.PathValue("serviceAction"), ":")
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "unsupported route")
		return
	}

	switch action {
	case "enable":
		svc := Service{
			Name:  serviceResourceName(project, serviceID),
			State: "ENABLED",
			Config: ServiceConfig{
				Name:  serviceID,
				Title: serviceID,
			},
		}
		if err := s.db.Put(bucketServices, serviceKey(project, serviceID), svc); err != nil {
			server.WriteError(w, 500, "INTERNAL", err.Error())
			return
		}
		respPayload, _ := json.Marshal(map[string]any{
			"@type":   "type.googleapis.com/google.api.serviceusage.v1.EnableServiceResponse",
			"service": svc,
		})
		op := Operation{
			Name:     fmt.Sprintf("operations/op-serviceusage-%d", s.nextID()),
			Done:     true,
			Response: respPayload,
		}
		server.WriteJSON(w, 200, op)

	case "disable":
		svc := Service{
			Name:  serviceResourceName(project, serviceID),
			State: "DISABLED",
			Config: ServiceConfig{
				Name:  serviceID,
				Title: serviceID,
			},
		}
		if err := s.db.Put(bucketServices, serviceKey(project, serviceID), svc); err != nil {
			server.WriteError(w, 500, "INTERNAL", err.Error())
			return
		}
		respPayload, _ := json.Marshal(map[string]any{
			"@type":   "type.googleapis.com/google.api.serviceusage.v1.DisableServiceResponse",
			"service": svc,
		})
		op := Operation{
			Name:     fmt.Sprintf("operations/op-serviceusage-%d", s.nextID()),
			Done:     true,
			Response: respPayload,
		}
		server.WriteJSON(w, 200, op)

	default:
		server.WriteError(w, 404, "NOT_FOUND", "unknown action: "+action)
	}
}
