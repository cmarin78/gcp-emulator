// Package compute emula un subconjunto de la API de Compute Engine
// (compute.googleapis.com/compute/v1): listar zonas/machine types y
// CRUD básico de instancias, devolviendo recursos "Operation" como lo
// hace la API real para que gcloud compute funcione sin parches.
package compute

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketInstances = "compute.instances"

type Instance struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Zone              string            `json:"zone"`
	MachineType       string            `json:"machineType"`
	Status            string            `json:"status"`
	CreationTimestamp string            `json:"creationTimestamp"`
	SelfLink          string            `json:"selfLink"`
	Labels            map[string]string `json:"labels,omitempty"`
	NetworkInterfaces []NetworkIface    `json:"networkInterfaces,omitempty"`
}

type NetworkIface struct {
	Network   string `json:"network"`
	NetworkIP string `json:"networkIP"`
}

var staticZones = []string{
	"us-central1-a", "us-central1-b", "us-east1-b", "europe-west1-b", "southamerica-east1-a",
}

var staticMachineTypes = []string{
	"e2-micro", "e2-small", "e2-medium", "n1-standard-1", "n1-standard-2",
}

type Service struct {
	db   *storage.DB
	ops  *server.Operations
	seq  int64
}

func New(db *storage.DB) *Service {
	return &Service{db: db, ops: server.NewOperations()}
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones", s.listZones)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/machineTypes", s.listMachineTypes)

	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances", s.insertInstance)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instances", s.listInstances)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instances/{instance}", s.getInstance)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/instances/{instance}", s.deleteInstance)
	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{instance}/stop", s.stopInstance)
	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{instance}/start", s.startInstance)

	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/operations/{operation}", s.getOperation)
}

func (s *Service) listZones(w http.ResponseWriter, r *http.Request) {
	items := make([]map[string]string, 0, len(staticZones))
	for _, z := range staticZones {
		items = append(items, map[string]string{"name": z, "status": "UP"})
	}
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#zoneList", "items": items})
}

func (s *Service) listMachineTypes(w http.ResponseWriter, r *http.Request) {
	zone := r.PathValue("zone")
	items := make([]map[string]string, 0, len(staticMachineTypes))
	for _, mt := range staticMachineTypes {
		items = append(items, map[string]string{"name": mt, "zone": zone})
	}
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#machineTypeList", "items": items})
}

func instanceKey(zone, name string) string { return zone + "/" + name }

func (s *Service) insertInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	var body struct {
		Name        string            `json:"name"`
		MachineType string            `json:"machineType"`
		Labels      map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	s.seq++
	inst := Instance{
		ID:                fmt.Sprintf("%d", s.seq),
		Name:              body.Name,
		Zone:              zone,
		MachineType:       orDefault(body.MachineType, "e2-medium"),
		Status:            "RUNNING",
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/zones/%s/instances/%s", project, zone, body.Name),
		Labels:            body.Labels,
	}
	if err := s.db.Put(bucketInstances, instanceKey(zone, inst.Name), inst); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", inst.SelfLink, fmt.Sprintf("/compute/v1/projects/%s/zones/%s/operations", project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listInstances(w http.ResponseWriter, r *http.Request) {
	zone := r.PathValue("zone")
	var items []Instance
	_ = s.db.List(bucketInstances, zone+"/", func(key string, raw []byte) error {
		var inst Instance
		if err := json.Unmarshal(raw, &inst); err != nil {
			return err
		}
		items = append(items, inst)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#instanceList", "items": items})
}

func (s *Service) getInstance(w http.ResponseWriter, r *http.Request) {
	zone := r.PathValue("zone")
	name := r.PathValue("instance")
	var inst Instance
	found, err := s.db.Get(bucketInstances, instanceKey(zone, name), &inst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instancia no encontrada")
		return
	}
	server.WriteJSON(w, 200, inst)
}

func (s *Service) deleteInstance(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	name := r.PathValue("instance")
	if err := s.db.Delete(bucketInstances, instanceKey(zone, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", fmt.Sprintf("/compute/v1/projects/%s/zones/%s/instances/%s", project, zone, name),
		fmt.Sprintf("/compute/v1/projects/%s/zones/%s/operations", project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) setStatus(w http.ResponseWriter, r *http.Request, status string) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	name := r.PathValue("instance")
	var inst Instance
	found, err := s.db.Get(bucketInstances, instanceKey(zone, name), &inst)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instancia no encontrada")
		return
	}
	inst.Status = status
	if err := s.db.Put(bucketInstances, instanceKey(zone, name), inst); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("update", inst.SelfLink, fmt.Sprintf("/compute/v1/projects/%s/zones/%s/operations", project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) stopInstance(w http.ResponseWriter, r *http.Request)  { s.setStatus(w, r, "TERMINATED") }
func (s *Service) startInstance(w http.ResponseWriter, r *http.Request) { s.setStatus(w, r, "RUNNING") }

func (s *Service) getOperation(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("operation")
	op, ok := s.ops.Get(name)
	if !ok {
		server.WriteError(w, 404, "NOT_FOUND", "operación no encontrada")
		return
	}
	server.WriteJSON(w, 200, op)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
