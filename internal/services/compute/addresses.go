// Fase 17: compute.addresses (regional) y compute.globalAddresses.
// Ambos tipos de IP estática aparecen en prácticamente cualquier config de
// Terraform que use un Load Balancer:
//   google_compute_address        → regional, /regions/{region}/addresses
//   google_compute_global_address → global,   /global/addresses
//
// Se modelan juntos aquí porque comparten la misma forma de Address y solo
// difieren en la ruta de registro y el bucket de almacenamiento.
package compute

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

const (
	bucketAddresses       = "compute.addresses"
	bucketGlobalAddresses = "compute.globalAddresses"
)

// Address refleja el subconjunto relevante de compute#address.
type Address struct {
	Kind              string `json:"kind"`
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	Region            string `json:"region,omitempty"`
	Address           string `json:"address,omitempty"`
	AddressType       string `json:"addressType,omitempty"`
	IPVersion         string `json:"ipVersion,omitempty"`
	NetworkTier       string `json:"networkTier,omitempty"`
	Purpose           string `json:"purpose,omitempty"`
	Network           string `json:"network,omitempty"`
	Subnetwork        string `json:"subnetwork,omitempty"`
	Status            string `json:"status"`
	SelfLink          string `json:"selfLink"`
	CreationTimestamp string `json:"creationTimestamp"`
}

func (s *Service) registerAddresses(mux *http.ServeMux) {
	// Regional addresses
	mux.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/addresses", s.insertAddress)
	mux.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/addresses", s.listAddresses)
	mux.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/addresses/{address}", s.getAddress)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/regions/{region}/addresses/{address}", s.deleteAddress)

	// Global addresses
	mux.HandleFunc("POST /compute/v1/projects/{project}/global/addresses", s.insertGlobalAddress)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/addresses", s.listGlobalAddresses)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/addresses/{address}", s.getGlobalAddress)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/addresses/{address}", s.deleteGlobalAddress)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func addressKey(project, scope, name string) string {
	return project + "/" + scope + "/" + name
}

func fakeIP(seed string) string {
	// Genera una IP determinista de aspecto razonable basada en el nombre.
	h := 0
	for _, c := range seed {
		h = h*31 + int(c)
		if h < 0 {
			h = -h
		}
	}
	return fmt.Sprintf("34.%d.%d.1", h%256, (h/256)%256)
}

// ── regional addresses ────────────────────────────────────────────────────────

func (s *Service) insertAddress(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")

	var body Address
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name is required")
		return
	}

	key := addressKey(project, region, body.Name)
	var existing Address
	if found, _ := s.db.Get(bucketAddresses, key, &existing); found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "address "+body.Name+" already exists")
		return
	}

	body.Kind = "compute#address"
	body.Region = "regions/" + region
	body.Status = "RESERVED"
	body.SelfLink = "https://www.googleapis.com/compute/v1/projects/" + project + "/regions/" + region + "/addresses/" + body.Name
	body.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
	if body.Address == "" {
		body.Address = fakeIP(project + region + body.Name)
	}
	if body.AddressType == "" {
		body.AddressType = "EXTERNAL"
	}
	if body.NetworkTier == "" {
		body.NetworkTier = "PREMIUM"
	}
	if err := s.db.Put(bucketAddresses, key, body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, body)
}

func (s *Service) listAddresses(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	prefix := project + "/" + region + "/"
	var items []Address
	_ = s.db.List(bucketAddresses, prefix, func(_ string, raw []byte) error {
		var a Address
		if err := json.Unmarshal(raw, &a); err == nil {
			items = append(items, a)
		}
		return nil
	})
	if items == nil {
		items = []Address{}
	}
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#addressList", "items": items})
}

func (s *Service) getAddress(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	name := r.PathValue("address")
	var a Address
	found, err := s.db.Get(bucketAddresses, addressKey(project, region, name), &a)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "address "+name+" not found")
		return
	}
	server.WriteJSON(w, 200, a)
}

func (s *Service) deleteAddress(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	name := r.PathValue("address")
	key := addressKey(project, region, name)
	var existing Address
	if found, _ := s.db.Get(bucketAddresses, key, &existing); !found {
		server.WriteError(w, 404, "NOT_FOUND", "address "+name+" not found")
		return
	}
	if err := s.db.Delete(bucketAddresses, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

// ── global addresses ──────────────────────────────────────────────────────────

func (s *Service) insertGlobalAddress(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")

	var body Address
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "invalid JSON: "+err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name is required")
		return
	}

	key := addressKey(project, "global", body.Name)
	var existing Address
	if found, _ := s.db.Get(bucketGlobalAddresses, key, &existing); found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "global address "+body.Name+" already exists")
		return
	}

	body.Kind = "compute#address"
	body.Status = "RESERVED"
	body.SelfLink = "https://www.googleapis.com/compute/v1/projects/" + project + "/global/addresses/" + body.Name
	body.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
	if body.Address == "" {
		body.Address = fakeIP(project + "global" + body.Name)
	}
	if body.AddressType == "" {
		body.AddressType = "EXTERNAL"
	}
	if body.NetworkTier == "" {
		body.NetworkTier = "PREMIUM"
	}
	if body.IPVersion == "" {
		body.IPVersion = "IPV4"
	}
	if err := s.db.Put(bucketGlobalAddresses, key, body); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, body)
}

func (s *Service) listGlobalAddresses(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := project + "/global/"
	var items []Address
	_ = s.db.List(bucketGlobalAddresses, prefix, func(_ string, raw []byte) error {
		var a Address
		if err := json.Unmarshal(raw, &a); err == nil {
			items = append(items, a)
		}
		return nil
	})
	if items == nil {
		items = []Address{}
	}
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#addressList", "items": items})
}

func (s *Service) getGlobalAddress(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("address")
	var a Address
	found, err := s.db.Get(bucketGlobalAddresses, addressKey(project, "global", name), &a)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "global address "+name+" not found")
		return
	}
	server.WriteJSON(w, 200, a)
}

func (s *Service) deleteGlobalAddress(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("address")
	key := addressKey(project, "global", name)
	var existing Address
	if found, _ := s.db.Get(bucketGlobalAddresses, key, &existing); !found {
		server.WriteError(w, 404, "NOT_FOUND", "global address "+name+" not found")
		return
	}
	if err := s.db.Delete(bucketGlobalAddresses, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}
