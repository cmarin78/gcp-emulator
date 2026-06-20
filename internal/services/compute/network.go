// Fase 1 del roadmap: recursos de red (networks, subnetworks, firewalls),
// catálogo de imágenes y discos persistentes, necesarios para que
// google_compute_instance (boot_disk + network_interface) funcione sin
// parches vía Terraform/gcloud.
package compute

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

const (
	bucketNetworks    = "compute.networks"
	bucketSubnetworks = "compute.subnetworks"
	bucketFirewalls   = "compute.firewalls"
	bucketDisks       = "compute.disks"
)

type Network struct {
	ID                    string           `json:"id"`
	Name                  string           `json:"name"`
	Description           string           `json:"description,omitempty"`
	AutoCreateSubnetworks bool             `json:"autoCreateSubnetworks"`
	CreationTimestamp     string           `json:"creationTimestamp"`
	SelfLink              string           `json:"selfLink"`
	Peerings              []NetworkPeering `json:"peerings,omitempty"`
}

// NetworkPeering mirrors compute#NetworkPeering, the nested resource
// created/removed via the network's addPeering/removePeering custom
// methods (google_compute_network_peering in Terraform). Real peerings
// negotiate asynchronously and can land in INACTIVE if the peer network
// hasn't reciprocated; this emulator always reports ACTIVE, matching the
// "shape-compatible, not behavior-complete" approach used elsewhere.
type NetworkPeering struct {
	Name                 string `json:"name"`
	Network              string `json:"network"`
	State                string `json:"state"`
	StateDetails         string `json:"stateDetails,omitempty"`
	ExchangeSubnetRoutes bool   `json:"exchangeSubnetRoutes"`
}

type Subnetwork struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Network           string `json:"network"`
	Region            string `json:"region"`
	IpCidrRange       string `json:"ipCidrRange"`
	CreationTimestamp string `json:"creationTimestamp"`
	SelfLink          string `json:"selfLink"`
}

type Firewall struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Network           string            `json:"network"`
	Direction         string            `json:"direction"`
	Priority          int64             `json:"priority,omitempty"`
	SourceRanges      []string          `json:"sourceRanges,omitempty"`
	Allowed           []FirewallAllowed `json:"allowed,omitempty"`
	Denied            []FirewallAllowed `json:"denied,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp"`
	SelfLink          string            `json:"selfLink"`
}

type FirewallAllowed struct {
	IPProtocol string   `json:"IPProtocol"`
	Ports      []string `json:"ports,omitempty"`
}

// Image representa una entrada del catálogo público de imágenes. El
// catálogo es estático y de solo lectura (no hace falta crear imágenes
// custom para que boot_disk.initialize_params.image funcione).
type Image struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Family            string `json:"family,omitempty"`
	DiskSizeGb        string `json:"diskSizeGb,omitempty"`
	Status            string `json:"status"`
	CreationTimestamp string `json:"creationTimestamp"`
	SelfLink          string `json:"selfLink"`
}

type Disk struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Zone              string `json:"zone"`
	SizeGb            string `json:"sizeGb,omitempty"`
	SourceImage       string `json:"sourceImage,omitempty"`
	Type              string `json:"type,omitempty"`
	Status            string `json:"status"`
	CreationTimestamp string `json:"creationTimestamp"`
	SelfLink          string `json:"selfLink"`
}

// staticImages es el catálogo estático que se sirve para cualquier
// {project} en la ruta (igual que en GCP real, donde las imágenes
// públicas viven en proyectos especiales como debian-cloud).
var staticImages = []Image{
	{Name: "debian-12-bookworm-v20240910", Family: "debian-12", DiskSizeGb: "10"},
	{Name: "debian-11-bullseye-v20240910", Family: "debian-11", DiskSizeGb: "10"},
	{Name: "ubuntu-2204-jammy-v20240910", Family: "ubuntu-2204-lts", DiskSizeGb: "10"},
	{Name: "cos-109-17800-66-15", Family: "cos-stable", DiskSizeGb: "10"},
}

func networkKey(name string) string            { return name }
func subnetworkKey(region, name string) string { return region + "/" + name }
func firewallKey(name string) string           { return name }
func diskKey(zone, name string) string         { return zone + "/" + name }

func regionPath(project, region string) string {
	return fmt.Sprintf("projects/%s/regions/%s", project, region)
}

// opsBase devuelve la base absoluta (esquema+host+"/compute/v1") a partir
// del propio request, para construir el selfLink de una Operation. gcloud
// resuelve el selfLink de una Operation con
// resources.Parse(selfLink) SIN especificar collection (lo hace así p. ej.
// en compute/instances/stop.py y start.py), lo que requiere que la URL sea
// absoluta para poder matchear contra la API registrada; un selfLink
// relativo (sin esquema/host) hace que ese parseo falle con
// "unknown collection for [...]". Los selfLink de "insert" no se vuelven a
// parsear de esa forma, así que el bug solo se manifestaba en stop/start.
func opsBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/compute/v1", scheme, r.Host)
}

// normalizeGlobalRef acepta tanto un nombre corto ("default") como un
// selfLink/URL ya completo, y devuelve siempre una referencia completa
// relativa al recurso global indicado (p. ej. "networks", "images").
func normalizeGlobalRef(project, kind, ref string) string {
	if ref == "" {
		return ""
	}
	if strings.Contains(ref, "/") {
		return ref
	}
	return fmt.Sprintf("/compute/v1/projects/%s/global/%s/%s", project, kind, ref)
}

// normalizeRef deja pasar tal cual cualquier referencia no vacía (ya sea
// nombre corto o selfLink); existe para dejar explícito en el código de
// llamada que el campo es opcional, sin imponer un formato a subnetwork.
func normalizeRef(ref string) string { return ref }

func (s *Service) insertNetwork(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name                  string `json:"name"`
		Description           string `json:"description"`
		AutoCreateSubnetworks *bool  `json:"autoCreateSubnetworks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	var existing Network
	found, err := s.db.Get(bucketNetworks, networkKey(body.Name), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "red ya existe: "+body.Name)
		return
	}
	auto := true
	if body.AutoCreateSubnetworks != nil {
		auto = *body.AutoCreateSubnetworks
	}
	n := Network{
		ID:                    fmt.Sprintf("%d", s.nextSeq()),
		Name:                  body.Name,
		Description:           body.Description,
		AutoCreateSubnetworks: auto,
		CreationTimestamp:     time.Now().UTC().Format(time.RFC3339),
		SelfLink:              fmt.Sprintf("/compute/v1/projects/%s/global/networks/%s", project, body.Name),
	}
	if err := s.db.Put(bucketNetworks, networkKey(n.Name), n); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", n.SelfLink, fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listNetworks(w http.ResponseWriter, r *http.Request) {
	var items []Network
	_ = s.db.List(bucketNetworks, "", func(key string, raw []byte) error {
		var n Network
		if err := json.Unmarshal(raw, &n); err != nil {
			return err
		}
		items = append(items, n)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#networkList", "items": items})
}

func (s *Service) getNetwork(w http.ResponseWriter, r *http.Request) {
	var n Network
	found, err := s.db.Get(bucketNetworks, networkKey(r.PathValue("network")), &n)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "red no encontrada")
		return
	}
	server.WriteJSON(w, 200, n)
}

func (s *Service) deleteNetwork(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("network")
	if err := s.db.Delete(bucketNetworks, networkKey(name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", fmt.Sprintf("/compute/v1/projects/%s/global/networks/%s", project, name),
		fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project))
	server.WriteJSON(w, 200, op)
}

// addPeering implements networks.addPeering (POST .../networks/{network}/
// addPeering), creating or replacing a named peering entry on the network.
func (s *Service) addPeering(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("network")
	var n Network
	found, err := s.db.Get(bucketNetworks, networkKey(name), &n)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "red no encontrada")
		return
	}
	var body struct {
		NetworkPeering struct {
			Name                 string `json:"name"`
			Network              string `json:"network"`
			ExchangeSubnetRoutes bool   `json:"exchangeSubnetRoutes"`
		} `json:"networkPeering"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.NetworkPeering.Name == "" || body.NetworkPeering.Network == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "networkPeering.name y networkPeering.network son requeridos")
		return
	}
	peering := NetworkPeering{
		Name:                 body.NetworkPeering.Name,
		Network:              normalizeGlobalRef(project, "networks", body.NetworkPeering.Network),
		State:                "ACTIVE",
		StateDetails:         "[2/2] Connected.",
		ExchangeSubnetRoutes: body.NetworkPeering.ExchangeSubnetRoutes,
	}
	replaced := false
	for i := range n.Peerings {
		if n.Peerings[i].Name == peering.Name {
			n.Peerings[i] = peering
			replaced = true
			break
		}
	}
	if !replaced {
		n.Peerings = append(n.Peerings, peering)
	}
	if err := s.db.Put(bucketNetworks, networkKey(n.Name), n); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("addPeering", n.SelfLink, fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project))
	server.WriteJSON(w, 200, op)
}

// removePeering implements networks.removePeering (POST .../networks/
// {network}/removePeering), dropping a named peering entry by name.
func (s *Service) removePeering(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("network")
	var n Network
	found, err := s.db.Get(bucketNetworks, networkKey(name), &n)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "red no encontrada")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	kept := make([]NetworkPeering, 0, len(n.Peerings))
	for _, p := range n.Peerings {
		if p.Name != body.Name {
			kept = append(kept, p)
		}
	}
	n.Peerings = kept
	if err := s.db.Put(bucketNetworks, networkKey(n.Name), n); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("removePeering", n.SelfLink, fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) insertSubnetwork(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	var body struct {
		Name        string `json:"name"`
		Network     string `json:"network"`
		IpCidrRange string `json:"ipCidrRange"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" || body.Network == "" || body.IpCidrRange == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name, network e ipCidrRange son requeridos")
		return
	}
	var existingSn Subnetwork
	found, err := s.db.Get(bucketSubnetworks, subnetworkKey(region, body.Name), &existingSn)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "subred ya existe: "+body.Name)
		return
	}
	sn := Subnetwork{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		Network:           normalizeGlobalRef(project, "networks", body.Network),
		Region:            regionPath(project, region),
		IpCidrRange:       body.IpCidrRange,
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/regions/%s/subnetworks/%s", project, region, body.Name),
	}
	if err := s.db.Put(bucketSubnetworks, subnetworkKey(region, sn.Name), sn); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneRegional("insert", sn.SelfLink, fmt.Sprintf("%s/projects/%s/regions/%s/operations", opsBase(r), project, region), regionPath(project, region))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listSubnetworks(w http.ResponseWriter, r *http.Request) {
	region := r.PathValue("region")
	var items []Subnetwork
	_ = s.db.List(bucketSubnetworks, region+"/", func(key string, raw []byte) error {
		var sn Subnetwork
		if err := json.Unmarshal(raw, &sn); err != nil {
			return err
		}
		items = append(items, sn)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#subnetworkList", "items": items})
}

func (s *Service) getSubnetwork(w http.ResponseWriter, r *http.Request) {
	region := r.PathValue("region")
	var sn Subnetwork
	found, err := s.db.Get(bucketSubnetworks, subnetworkKey(region, r.PathValue("subnetwork")), &sn)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "subred no encontrada")
		return
	}
	server.WriteJSON(w, 200, sn)
}

func (s *Service) deleteSubnetwork(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	name := r.PathValue("subnetwork")
	if err := s.db.Delete(bucketSubnetworks, subnetworkKey(region, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneRegional("delete", fmt.Sprintf("/compute/v1/projects/%s/regions/%s/subnetworks/%s", project, region, name),
		fmt.Sprintf("%s/projects/%s/regions/%s/operations", opsBase(r), project, region), regionPath(project, region))
	server.WriteJSON(w, 200, op)
}

func (s *Service) insertFirewall(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name         string            `json:"name"`
		Network      string            `json:"network"`
		Direction    string            `json:"direction"`
		Priority     int64             `json:"priority"`
		SourceRanges []string          `json:"sourceRanges"`
		Allowed      []FirewallAllowed `json:"allowed"`
		Denied       []FirewallAllowed `json:"denied"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	var existingFw Firewall
	found, err := s.db.Get(bucketFirewalls, firewallKey(body.Name), &existingFw)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "firewall ya existe: "+body.Name)
		return
	}
	fw := Firewall{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		Network:           normalizeGlobalRef(project, "networks", orDefault(body.Network, "default")),
		Direction:         orDefault(body.Direction, "INGRESS"),
		Priority:          body.Priority,
		SourceRanges:      body.SourceRanges,
		Allowed:           body.Allowed,
		Denied:            body.Denied,
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/global/firewalls/%s", project, body.Name),
	}
	if err := s.db.Put(bucketFirewalls, firewallKey(fw.Name), fw); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", fw.SelfLink, fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listFirewalls(w http.ResponseWriter, r *http.Request) {
	var items []Firewall
	_ = s.db.List(bucketFirewalls, "", func(key string, raw []byte) error {
		var fw Firewall
		if err := json.Unmarshal(raw, &fw); err != nil {
			return err
		}
		items = append(items, fw)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#firewallList", "items": items})
}

func (s *Service) getFirewall(w http.ResponseWriter, r *http.Request) {
	var fw Firewall
	found, err := s.db.Get(bucketFirewalls, firewallKey(r.PathValue("firewall")), &fw)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "firewall no encontrado")
		return
	}
	server.WriteJSON(w, 200, fw)
}

func (s *Service) deleteFirewall(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("firewall")
	if err := s.db.Delete(bucketFirewalls, firewallKey(name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", fmt.Sprintf("/compute/v1/projects/%s/global/firewalls/%s", project, name),
		fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listImages(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	items := make([]Image, 0, len(staticImages))
	for i, img := range staticImages {
		items = append(items, fillImage(project, i, img))
	}
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#imageList", "items": items})
}

func (s *Service) getImage(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("image")
	for i, img := range staticImages {
		if img.Name == name {
			server.WriteJSON(w, 200, fillImage(project, i, img))
			return
		}
	}
	server.WriteError(w, 404, "NOT_FOUND", "imagen no encontrada")
}

func (s *Service) getImageByFamily(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	family := r.PathValue("family")
	for i, img := range staticImages {
		if img.Family == family {
			server.WriteJSON(w, 200, fillImage(project, i, img))
			return
		}
	}
	server.WriteError(w, 404, "NOT_FOUND", "no hay imagen para esa familia")
}

// fillImage completa los campos calculados de una imagen estática. El
// campo "id" en la API real de Compute es un uint64 serializado como
// string (json:",string"), así que tiene que ser numérico: no podemos
// usar el nombre de la imagen como en otros recursos.
func fillImage(project string, index int, img Image) Image {
	img.ID = fmt.Sprintf("%d", 100000+index)
	img.Status = "READY"
	img.CreationTimestamp = "2024-01-01T00:00:00Z"
	img.SelfLink = fmt.Sprintf("/compute/v1/projects/%s/global/images/%s", project, img.Name)
	return img
}

func (s *Service) insertDisk(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	var body struct {
		Name        string `json:"name"`
		SizeGb      string `json:"sizeGb"`
		SourceImage string `json:"sourceImage"`
		Type        string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	var existingDisk Disk
	found, err := s.db.Get(bucketDisks, diskKey(zone, body.Name), &existingDisk)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "disco ya existe: "+body.Name)
		return
	}
	d := Disk{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		Zone:              zonePath(project, zone),
		SizeGb:            orDefault(body.SizeGb, "10"),
		SourceImage:       normalizeGlobalRef(project, "images", body.SourceImage),
		Type:              orDefault(body.Type, "pd-standard"),
		Status:            "READY",
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/zones/%s/disks/%s", project, zone, body.Name),
	}
	if err := s.db.Put(bucketDisks, diskKey(zone, d.Name), d); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneZonal("insert", d.SelfLink, fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listDisks(w http.ResponseWriter, r *http.Request) {
	zone := r.PathValue("zone")
	var items []Disk
	_ = s.db.List(bucketDisks, zone+"/", func(key string, raw []byte) error {
		var d Disk
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		items = append(items, d)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#diskList", "items": items})
}

func (s *Service) getDisk(w http.ResponseWriter, r *http.Request) {
	zone := r.PathValue("zone")
	var d Disk
	found, err := s.db.Get(bucketDisks, diskKey(zone, r.PathValue("disk")), &d)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "disco no encontrado")
		return
	}
	server.WriteJSON(w, 200, d)
}

func (s *Service) deleteDisk(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	name := r.PathValue("disk")
	if err := s.db.Delete(bucketDisks, diskKey(zone, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneZonal("delete", fmt.Sprintf("/compute/v1/projects/%s/zones/%s/disks/%s", project, zone, name),
		fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}
