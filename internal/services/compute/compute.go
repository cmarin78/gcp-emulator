// Package compute emula un subconjunto de la API de Compute Engine
// (compute.googleapis.com/compute/v1): listar zonas/machine types y
// CRUD básico de instancias, devolviendo recursos "Operation" como lo
// hace la API real para que gcloud compute funcione sin parches.
package compute

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

// fakeFingerprint genera un fingerprint con forma válida para los clientes
// reales: en la API real estos campos son bytes (base64 en JSON). Las
// librerías generadas (apitools, usada por gcloud) intentan decodificar
// base64 ese campo y fallan con "Incorrect padding" si no lo es.
func fakeFingerprint(seed string) string {
	return base64.StdEncoding.EncodeToString([]byte(seed))
}

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
	Disks             []AttachedDisk    `json:"disks,omitempty"`
	Metadata          Metadata          `json:"metadata"`
	Tags              Tags              `json:"tags"`
	Scheduling        Scheduling        `json:"scheduling"`
	CPUPlatform       string            `json:"cpuPlatform,omitempty"`
	LabelFingerprint  string            `json:"labelFingerprint,omitempty"`
}

// Tags y Scheduling, igual que Metadata: la API real siempre los incluye
// como objetos no nulos en la respuesta de una instancia, y algunos
// clientes (Terraform) los leen sin chequear nil.
type Tags struct {
	Fingerprint string   `json:"fingerprint,omitempty"`
	Items       []string `json:"items,omitempty"`
}

type Scheduling struct {
	OnHostMaintenance string `json:"onHostMaintenance,omitempty"`
	AutomaticRestart  *bool  `json:"automaticRestart,omitempty"`
	Preemptible       bool   `json:"preemptible,omitempty"`
}

// Metadata siempre va presente (no puntero) en la respuesta: algunos
// clientes (p. ej. el provider de Terraform) asumen que el objeto
// "metadata" de una instancia nunca es nulo y no chequean antes de leer
// sus campos.
type Metadata struct {
	Kind        string         `json:"kind,omitempty"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	Items       []MetadataItem `json:"items,omitempty"`
}

type MetadataItem struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

type NetworkIface struct {
	Network       string         `json:"network,omitempty"`
	Subnetwork    string         `json:"subnetwork,omitempty"`
	NetworkIP     string         `json:"networkIP,omitempty"`
	AccessConfigs []AccessConfig `json:"accessConfigs,omitempty"`
}

type AccessConfig struct {
	Name  string `json:"name,omitempty"`
	Type  string `json:"type,omitempty"`
	NatIP string `json:"natIP,omitempty"`
}

// AttachedDisk es el disco tal como aparece embebido en una instancia
// (Instance.Disks). Cuando viene con InitializeParams en el request, el
// emulador crea un recurso Disk real (ver disks.go) y lo reemplaza por
// una referencia "source", igual que la API real.
type AttachedDisk struct {
	Boot             bool              `json:"boot,omitempty"`
	AutoDelete       bool              `json:"autoDelete,omitempty"`
	DeviceName       string            `json:"deviceName,omitempty"`
	Source           string            `json:"source,omitempty"`
	Mode             string            `json:"mode,omitempty"`
	Type             string            `json:"type,omitempty"`
	InitializeParams *InitializeParams `json:"initializeParams,omitempty"`
}

type InitializeParams struct {
	SourceImage string `json:"sourceImage,omitempty"`
	DiskSizeGb  string `json:"diskSizeGb,omitempty"`
	DiskType    string `json:"diskType,omitempty"`
}

var staticZones = []string{
	"us-central1-a", "us-central1-b", "us-east1-b", "europe-west1-b", "southamerica-east1-a",
}

var staticMachineTypes = []string{
	"e2-micro", "e2-small", "e2-medium", "n1-standard-1", "n1-standard-2",
}

type Service struct {
	db    *storage.DB
	ops   *server.Operations
	seq   int64
	ipSeq int64
}

func New(db *storage.DB) *Service {
	return &Service{db: db, ops: server.NewOperations()}
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones", s.listZones)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}", s.getZone)
	mux.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}", s.getRegion)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/machineTypes", s.listMachineTypes)

	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances", s.insertInstance)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instances", s.listInstances)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instances/{instance}", s.getInstance)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/instances/{instance}", s.deleteInstance)
	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{instance}/stop", s.stopInstance)
	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{instance}/start", s.startInstance)

	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/operations/{operation}", s.getOperation)
	mux.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/operations/{operation}", s.getOperation)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/operations/{operation}", s.getOperation)
	// El método "wait" (POST .../operations/{operation}/wait) es lo que usa
	// gcloud para esperar a que termine una operación (p. ej. tras
	// instances.stop/start); como el emulador resuelve todo de forma
	// síncrona, basta con devolver la misma operación ya DONE.
	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/operations/{operation}/wait", s.getOperation)
	mux.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/operations/{operation}/wait", s.getOperation)
	mux.HandleFunc("POST /compute/v1/projects/{project}/global/operations/{operation}/wait", s.getOperation)

	// Fase 1: networks, subnetworks, firewalls, images, disks.
	mux.HandleFunc("POST /compute/v1/projects/{project}/global/networks", s.insertNetwork)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/networks", s.listNetworks)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/networks/{network}", s.getNetwork)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/networks/{network}", s.deleteNetwork)

	mux.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/subnetworks", s.insertSubnetwork)
	mux.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/subnetworks", s.listSubnetworks)
	mux.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/subnetworks/{subnetwork}", s.getSubnetwork)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/regions/{region}/subnetworks/{subnetwork}", s.deleteSubnetwork)

	mux.HandleFunc("POST /compute/v1/projects/{project}/global/firewalls", s.insertFirewall)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/firewalls", s.listFirewalls)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/firewalls/{firewall}", s.getFirewall)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/firewalls/{firewall}", s.deleteFirewall)

	mux.HandleFunc("GET /compute/v1/projects/{project}/global/images", s.listImages)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/images/family/{family}", s.getImageByFamily)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/images/{image}", s.getImage)

	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/disks", s.insertDisk)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/disks", s.listDisks)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/disks/{disk}", s.getDisk)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/disks/{disk}", s.deleteDisk)

	mux.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/routers", s.insertRouter)
	mux.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/routers", s.listRouters)
	mux.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/routers/{router}", s.getRouter)
	mux.HandleFunc("PATCH /compute/v1/projects/{project}/regions/{region}/routers/{router}", s.patchRouter)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/regions/{region}/routers/{router}", s.deleteRouter)

	mux.HandleFunc("POST /compute/v1/projects/{project}/global/routes", s.insertRoute)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/routes", s.listRoutes)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/routes/{route}", s.getRoute)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/routes/{route}", s.deleteRoute)
}

func (s *Service) listZones(w http.ResponseWriter, r *http.Request) {
	items := make([]map[string]string, 0, len(staticZones))
	for _, z := range staticZones {
		items = append(items, map[string]string{"name": z, "status": "UP"})
	}
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#zoneList", "items": items})
}

func (s *Service) getZone(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	for _, z := range staticZones {
		if z == zone {
			server.WriteJSON(w, 200, map[string]string{
				"name":     z,
				"status":   "UP",
				"selfLink": fmt.Sprintf("/compute/v1/projects/%s/zones/%s", project, z),
			})
			return
		}
	}
	server.WriteError(w, 404, "NOT_FOUND", "zona no encontrada")
}

func (s *Service) getRegion(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	server.WriteJSON(w, 200, map[string]string{
		"name":     region,
		"status":   "UP",
		"selfLink": fmt.Sprintf("/compute/v1/projects/%s/regions/%s", project, region),
	})
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
		Name              string            `json:"name"`
		MachineType       string            `json:"machineType"`
		Labels            map[string]string `json:"labels"`
		Disks             []AttachedDisk    `json:"disks"`
		NetworkInterfaces []NetworkIface    `json:"networkInterfaces"`
		Metadata          *Metadata         `json:"metadata"`
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
	now := time.Now().UTC().Format(time.RFC3339)

	disks, err := s.resolveAttachedDisks(project, zone, body.Name, body.Disks)
	if err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	ifaces := s.resolveNetworkInterfaces(project, body.NetworkInterfaces)

	meta := Metadata{Kind: "compute#metadata", Fingerprint: fakeFingerprint(fmt.Sprintf("meta-%d", s.seq))}
	if body.Metadata != nil {
		meta.Items = body.Metadata.Items
	}
	autoRestart := true

	inst := Instance{
		ID:                fmt.Sprintf("%d", s.seq),
		Name:              body.Name,
		Zone:              zone,
		MachineType:       orDefault(body.MachineType, "e2-medium"),
		Status:            "RUNNING",
		CreationTimestamp: now,
		SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/zones/%s/instances/%s", project, zone, body.Name),
		Labels:            body.Labels,
		Disks:             disks,
		NetworkInterfaces: ifaces,
		Metadata:          meta,
		Tags:              Tags{Fingerprint: fakeFingerprint(fmt.Sprintf("tags-%d", s.seq))},
		Scheduling:        Scheduling{OnHostMaintenance: "MIGRATE", AutomaticRestart: &autoRestart},
		CPUPlatform:       "Intel Broadwell",
		LabelFingerprint:  fakeFingerprint(fmt.Sprintf("labels-%d", s.seq)),
	}
	if err := s.db.Put(bucketInstances, instanceKey(zone, inst.Name), inst); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneZonal("insert", inst.SelfLink, fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

// resolveAttachedDisks procesa los discos pedidos al crear una instancia:
// si vienen con initializeParams (boot disk implícito, el caso típico de
// google_compute_instance), crea un recurso Disk real respaldándolo y
// deja en la instancia solo la referencia "source", igual que la API real.
func (s *Service) resolveAttachedDisks(project, zone, instanceName string, in []AttachedDisk) ([]AttachedDisk, error) {
	out := make([]AttachedDisk, 0, len(in))
	for i, d := range in {
		if d.InitializeParams != nil {
			diskName := d.DeviceName
			if diskName == "" {
				if d.Boot {
					diskName = instanceName
				} else {
					diskName = fmt.Sprintf("%s-disk-%d", instanceName, i+1)
				}
			}
			disk := Disk{
				ID:                fmt.Sprintf("%d", s.nextSeq()),
				Name:              diskName,
				Zone:              zonePath(project, zone),
				SizeGb:            orDefault(d.InitializeParams.DiskSizeGb, "10"),
				SourceImage:       d.InitializeParams.SourceImage,
				Type:              orDefault(d.InitializeParams.DiskType, "pd-standard"),
				Status:            "READY",
				CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
				SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/zones/%s/disks/%s", project, zone, diskName),
			}
			if err := s.db.Put(bucketDisks, diskKey(zone, disk.Name), disk); err != nil {
				return nil, err
			}
			d.Source = disk.SelfLink
			d.InitializeParams = nil
		}
		if d.DeviceName == "" {
			d.DeviceName = instanceName
		}
		if d.Type == "" {
			d.Type = "PERSISTENT"
		}
		if d.Mode == "" {
			d.Mode = "READ_WRITE"
		}
		out = append(out, d)
	}
	return out, nil
}

// resolveNetworkInterfaces normaliza referencias a network/subnetwork (acepta
// nombre corto o selfLink) y asigna una IP interna fake si no vino una.
func (s *Service) resolveNetworkInterfaces(project string, in []NetworkIface) []NetworkIface {
	out := make([]NetworkIface, 0, len(in))
	for _, ni := range in {
		ni.Network = normalizeGlobalRef(project, "networks", ni.Network)
		ni.Subnetwork = normalizeRef(ni.Subnetwork)
		if ni.NetworkIP == "" {
			ni.NetworkIP = fmt.Sprintf("10.128.0.%d", s.nextIP())
		}
		for i := range ni.AccessConfigs {
			if ni.AccessConfigs[i].Name == "" {
				ni.AccessConfigs[i].Name = "External NAT"
			}
			if ni.AccessConfigs[i].Type == "" {
				ni.AccessConfigs[i].Type = "ONE_TO_ONE_NAT"
			}
			if ni.AccessConfigs[i].NatIP == "" {
				ni.AccessConfigs[i].NatIP = fmt.Sprintf("34.10.0.%d", s.nextIP())
			}
		}
		out = append(out, ni)
	}
	return out
}

func (s *Service) nextSeq() int64 {
	s.seq++
	return s.seq
}

func (s *Service) nextIP() int64 {
	s.ipSeq++
	if s.ipSeq > 254 {
		s.ipSeq = 1
	}
	return s.ipSeq
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
	op := s.ops.DoneZonal("delete", fmt.Sprintf("/compute/v1/projects/%s/zones/%s/instances/%s", project, zone, name),
		fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) setStatus(w http.ResponseWriter, r *http.Request, status, opType string) {
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
	// gcloud usa el operationType ("start"/"stop") para resolver el poller de
	// la operación; un valor genérico como "update" no es el que la API real
	// devuelve para estas acciones y hace que gcloud falle con
	// "unknown collection" al intentar resolver la operación.
	op := s.ops.DoneZonal(opType, inst.SelfLink, fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) stopInstance(w http.ResponseWriter, r *http.Request) {
	s.setStatus(w, r, "TERMINATED", "stop")
}
func (s *Service) startInstance(w http.ResponseWriter, r *http.Request) {
	s.setStatus(w, r, "RUNNING", "start")
}

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

func zonePath(project, zone string) string {
	return fmt.Sprintf("projects/%s/zones/%s", project, zone)
}
