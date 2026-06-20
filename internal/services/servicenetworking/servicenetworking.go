// Package servicenetworking emulates a subset of the Service Networking API
// (servicenetworking.googleapis.com/v1): the "connection" resource used to
// peer a customer VPC with a Google-managed producer network for private
// IP access to Cloud SQL/Memorystore/Filestore. Real connections trigger an
// asynchronous VPC peering negotiation; this emulator resolves every
// mutation synchronously, same "shape-compatible, not behavior-complete"
// approach used elsewhere. There is exactly one connection per
// (project, service) pair in the real API (the connection name is always
// "main"), which this emulator mirrors.
//
// Mutations return a google.longrunning.Operation, matching the real API
// (same shape as vpcaccess.go/workflows.go). Delete uses the real API's
// custom method shape (POST .../connections/main:deleteConnection) rather
// than a plain DELETE, since the real API doesn't expose a DELETE verb
// here -- modeled the same way Cloud Run Jobs models ":run" elsewhere in
// this codebase (capture the trailing segment, split on ":").
package servicenetworking

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketConnections = "servicenetworking.connections"

// Connection mirrors the relevant subset of servicenetworking#Connection.
type Connection struct {
	Network               string   `json:"network"`
	ReservedPeeringRanges []string `json:"reservedPeeringRanges,omitempty"`
	Service               string   `json:"service,omitempty"`
	Peering               string   `json:"peering,omitempty"`
}

// Operation mirrors google.longrunning.Operation.
type Operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/services/{service}/connections", s.createConnection)
	mux.HandleFunc("GET /v1/services/{service}/connections", s.listConnections)
	mux.HandleFunc("PATCH /v1/services/{service}/connections/main", s.patchConnection)
	// "connections/main:deleteConnection" can't be expressed as a mixed Go
	// mux pattern; capture the full segment and split on ":", same pattern
	// used by Cloud Run Jobs/Cloud Scheduler/Secret Manager elsewhere.
	mux.HandleFunc("POST /v1/services/{service}/connections/{connectionAction}", s.connectionAction)
}

func connectionKey(service string) string { return service }

func (s *Service) nextID() int64 {
	s.seq++
	return s.seq
}

func (s *Service) writeOperation(w http.ResponseWriter, verb string, conn Connection) {
	meta, _ := json.Marshal(map[string]string{"target": conn.Service, "verb": verb})
	resp, _ := json.Marshal(conn)
	op := Operation{
		Name:     fmt.Sprintf("operations/op-%d", s.nextID()),
		Done:     true,
		Metadata: meta,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createConnection(w http.ResponseWriter, r *http.Request) {
	service := r.PathValue("service")
	var body struct {
		Network               string   `json:"network"`
		ReservedPeeringRanges []string `json:"reservedPeeringRanges"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Network == "" || len(body.ReservedPeeringRanges) == 0 {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "network and reservedPeeringRanges are required")
		return
	}
	var existing Connection
	found, err := s.db.Get(bucketConnections, connectionKey(service), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "connection already exists for service: "+service)
		return
	}
	conn := Connection{
		Network:               body.Network,
		ReservedPeeringRanges: body.ReservedPeeringRanges,
		Service:               service,
		Peering:               "servicenetworking-googleapis-com",
	}
	if err := s.db.Put(bucketConnections, connectionKey(service), conn); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, "create", conn)
}

func (s *Service) listConnections(w http.ResponseWriter, r *http.Request) {
	service := r.PathValue("service")
	items := []Connection{}
	var existing Connection
	found, err := s.db.Get(bucketConnections, connectionKey(service), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		items = append(items, existing)
	}
	server.WriteJSON(w, 200, map[string]any{"connections": items})
}

func (s *Service) patchConnection(w http.ResponseWriter, r *http.Request) {
	service := r.PathValue("service")
	var existing Connection
	found, err := s.db.Get(bucketConnections, connectionKey(service), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "connection not found for service: "+service)
		return
	}
	var body struct {
		ReservedPeeringRanges []string `json:"reservedPeeringRanges"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if len(body.ReservedPeeringRanges) > 0 {
		existing.ReservedPeeringRanges = body.ReservedPeeringRanges
	}
	if err := s.db.Put(bucketConnections, connectionKey(service), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, "update", existing)
}

// connectionAction dispatches the manual "main:deleteConnection" verb. No
// other custom verbs are modeled here.
func (s *Service) connectionAction(w http.ResponseWriter, r *http.Request) {
	service := r.PathValue("service")
	name, action, ok := strings.Cut(r.PathValue("connectionAction"), ":")
	if !ok || name != "main" || action != "deleteConnection" {
		server.WriteError(w, 404, "NOT_FOUND", "unsupported action")
		return
	}
	var existing Connection
	_, _ = s.db.Get(bucketConnections, connectionKey(service), &existing)
	if err := s.db.Delete(bucketConnections, connectionKey(service)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if existing.Service == "" {
		existing.Service = service
	}
	s.writeOperation(w, "delete", existing)
}
