// Package vpcaccess emulates a subset of the Serverless VPC Access API
// (vpcaccess.googleapis.com/v1): connectors. Real connectors provision a
// managed set of VMs that bridge serverless products (Cloud Run, Cloud
// Functions, App Engine) into a VPC; this emulator just persists the
// resource shape and always reports state READY, same
// "shape-compatible, not behavior-complete" approach used elsewhere.
// Mutations return a google.longrunning.Operation, matching the real API
// (same shape as memorystore.go/cloudbuild.go). Connectors have no update
// method in the real API (only create/get/list/delete), which this
// emulator mirrors.
package vpcaccess

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketConnectors = "vpcaccess.connectors"

// Connector mirrors the relevant subset of vpcaccess#Connector.
type Connector struct {
	Name              string   `json:"name"`
	Network           string   `json:"network,omitempty"`
	IpCidrRange       string   `json:"ipCidrRange,omitempty"`
	MinThroughput     int64    `json:"minThroughput,omitempty"`
	MaxThroughput     int64    `json:"maxThroughput,omitempty"`
	MachineType       string   `json:"machineType,omitempty"`
	MinInstances      int64    `json:"minInstances,omitempty"`
	MaxInstances      int64    `json:"maxInstances,omitempty"`
	State             string   `json:"state,omitempty"`
	ConnectedProjects []string `json:"connectedProjects,omitempty"`
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
	mux.HandleFunc("POST /v1/projects/{project}/locations/{location}/connectors", s.createConnector)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/connectors", s.listConnectors)
	mux.HandleFunc("GET /v1/projects/{project}/locations/{location}/connectors/{connector}", s.getConnector)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/connectors/{connector}", s.deleteConnector)
}

func connectorKey(project, location, connector string) string {
	return fmt.Sprintf("%s/%s/%s", project, location, connector)
}

func connectorName(project, location, connector string) string {
	return fmt.Sprintf("projects/%s/locations/%s/connectors/%s", project, location, connector)
}

func (s *Service) nextID() int64 {
	s.seq++
	return s.seq
}

func operationName(project, location string, id int64) string {
	return fmt.Sprintf("projects/%s/locations/%s/operations/op-%d", project, location, id)
}

func (s *Service) writeOperation(w http.ResponseWriter, project, location, verb string, c Connector) {
	meta, _ := json.Marshal(map[string]string{"target": c.Name, "verb": verb})
	resp, _ := json.Marshal(c)
	op := Operation{
		Name:     operationName(project, location, s.nextID()),
		Done:     true,
		Metadata: meta,
		Response: resp,
	}
	server.WriteJSON(w, 200, op)
}

func (s *Service) createConnector(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	connectorID := r.URL.Query().Get("connectorId")
	if connectorID == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "connectorId is required")
		return
	}
	var body struct {
		Network       string `json:"network"`
		IpCidrRange   string `json:"ipCidrRange"`
		MinThroughput int64  `json:"minThroughput"`
		MaxThroughput int64  `json:"maxThroughput"`
		MachineType   string `json:"machineType"`
		MinInstances  int64  `json:"minInstances"`
		MaxInstances  int64  `json:"maxInstances"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Network == "" || body.IpCidrRange == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "network e ipCidrRange son requeridos")
		return
	}
	var existing Connector
	found, err := s.db.Get(bucketConnectors, connectorKey(project, location, connectorID), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "connector already exists: "+connectorID)
		return
	}
	c := Connector{
		Name:          connectorName(project, location, connectorID),
		Network:       body.Network,
		IpCidrRange:   body.IpCidrRange,
		MinThroughput: orDefaultInt(body.MinThroughput, 200),
		MaxThroughput: orDefaultInt(body.MaxThroughput, 300),
		MachineType:   orDefault(body.MachineType, "e2-micro"),
		MinInstances:  orDefaultInt(body.MinInstances, 2),
		MaxInstances:  orDefaultInt(body.MaxInstances, 10),
		State:         "READY",
	}
	if err := s.db.Put(bucketConnectors, connectorKey(project, location, connectorID), c); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.writeOperation(w, project, location, "create", c)
}

func (s *Service) listConnectors(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	prefix := fmt.Sprintf("%s/%s/", project, location)
	items := []Connector{}
	_ = s.db.List(bucketConnectors, prefix, func(key string, raw []byte) error {
		var c Connector
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		items = append(items, c)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"connectors": items})
}

func (s *Service) getConnector(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	connector := r.PathValue("connector")
	var c Connector
	found, err := s.db.Get(bucketConnectors, connectorKey(project, location, connector), &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "connector not found")
		return
	}
	server.WriteJSON(w, 200, c)
}

func (s *Service) deleteConnector(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	connector := r.PathValue("connector")
	var existing Connector
	_, _ = s.db.Get(bucketConnectors, connectorKey(project, location, connector), &existing)
	if err := s.db.Delete(bucketConnectors, connectorKey(project, location, connector)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if existing.Name == "" {
		existing.Name = connectorName(project, location, connector)
	}
	s.writeOperation(w, project, location, "delete", existing)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDefaultInt(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}
