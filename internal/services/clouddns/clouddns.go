// Package clouddns emula un subconjunto de la API de Cloud DNS
// (dns.googleapis.com/v1): managedZones y resourceRecordSets. Como hace el
// provider hashicorp/google (`google_dns_record_set`), los rrsets no se
// crean directamente: se aplican vía el recurso "Change"
// (additions/deletions), que aquí se resuelve de forma sincrónica
// (status=done) y persiste el resultado en el bucket de rrsets. No hay
// resolución DNS real, solo el grafo de recursos que Terraform/gcloud
// esperan — mismo enfoque "shape-compatible" usado en el resto del emulador.
package clouddns

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const (
	bucketZones   = "clouddns.managedZones"
	bucketRRSets  = "clouddns.rrsets"
	bucketChanges = "clouddns.changes"
)

// ManagedZone replica el recurso "ManagedZone".
type ManagedZone struct {
	Name         string   `json:"name"`
	DNSName      string   `json:"dnsName"`
	Description  string   `json:"description,omitempty"`
	Id           string   `json:"id"`
	CreationTime string   `json:"creationTime,omitempty"`
	NameServers  []string `json:"nameServers,omitempty"`
	Visibility   string   `json:"visibility,omitempty"`
}

// ResourceRecordSet replica el recurso "ResourceRecordSet". La clave real
// del recurso es (name, type), no solo name.
type ResourceRecordSet struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	TTL     int64    `json:"ttl,omitempty"`
	Rrdatas []string `json:"rrdatas,omitempty"`
}

// Change replica el recurso "Change": additions/deletions aplicadas a los
// rrsets de la zona. Siempre se resuelve sincrónicamente (status=done).
type Change struct {
	Id        string              `json:"id"`
	Status    string              `json:"status"`
	StartTime string              `json:"startTime,omitempty"`
	Additions []ResourceRecordSet `json:"additions,omitempty"`
	Deletions []ResourceRecordSet `json:"deletions,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service {
	return &Service{db: db}
}

// Register monta las rutas de Cloud DNS, siguiendo los paths reales de
// dns.googleapis.com.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /dns/v1/projects/{project}/managedZones", s.createZone)
	mux.HandleFunc("GET /dns/v1/projects/{project}/managedZones", s.listZones)
	mux.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{managedZone}", s.getZone)
	mux.HandleFunc("DELETE /dns/v1/projects/{project}/managedZones/{managedZone}", s.deleteZone)

	mux.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{managedZone}/rrsets", s.listRRSets)
	// rrsets:resolve es una extensión propia del emulador (no existe en la
	// API real de Cloud DNS, que no resuelve nombres -- ver resolve.go) para
	// poder validar el grafo de rrsets sin levantar un resolver DNS real.
	mux.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{managedZone}/rrsets:resolve", s.resolveRRSet)

	mux.HandleFunc("POST /dns/v1/projects/{project}/managedZones/{managedZone}/changes", s.createChange)
	mux.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{managedZone}/changes", s.listChanges)
	mux.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{managedZone}/changes/{change}", s.getChange)
}

func zoneName(project, zone string) string {
	return fmt.Sprintf("projects/%s/managedZones/%s", project, zone)
}

func rrsetKey(project, zone, name, typ string) string {
	return fmt.Sprintf("projects/%s/managedZones/%s/rrsets/%s/%s", project, zone, name, typ)
}

func rrsetPrefix(project, zone string) string {
	return fmt.Sprintf("projects/%s/managedZones/%s/rrsets/", project, zone)
}

func changeName(project, zone, id string) string {
	return fmt.Sprintf("projects/%s/managedZones/%s/changes/%s", project, zone, id)
}

func (s *Service) createZone(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")

	var body struct {
		Name        string `json:"name"`
		DNSName     string `json:"dnsName"`
		Description string `json:"description"`
		Visibility  string `json:"visibility"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" || body.DNSName == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name y dnsName son requeridos")
		return
	}

	name := zoneName(project, body.Name)
	var existing ManagedZone
	found, err := s.db.Get(bucketZones, name, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "la managed zone ya existe")
		return
	}

	s.seq++
	zone := ManagedZone{
		Name:         body.Name,
		DNSName:      body.DNSName,
		Description:  body.Description,
		Id:           fmt.Sprintf("%d", s.seq),
		CreationTime: time.Now().UTC().Format(time.RFC3339),
		NameServers: []string{
			fmt.Sprintf("ns-emulator-1.%s", body.DNSName),
			fmt.Sprintf("ns-emulator-2.%s", body.DNSName),
		},
		Visibility: body.Visibility,
	}
	if zone.Visibility == "" {
		zone.Visibility = "public"
	}
	if err := s.db.Put(bucketZones, name, zone); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, zone)
}

func (s *Service) listZones(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	prefix := fmt.Sprintf("projects/%s/managedZones/", project)
	zones := []ManagedZone{}
	_ = s.db.List(bucketZones, prefix, func(key string, raw []byte) error {
		var z ManagedZone
		if err := json.Unmarshal(raw, &z); err != nil {
			return err
		}
		zones = append(zones, z)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"managedZones": zones})
}

func (s *Service) getZone(w http.ResponseWriter, r *http.Request) {
	name := zoneName(r.PathValue("project"), r.PathValue("managedZone"))
	var z ManagedZone
	found, err := s.db.Get(bucketZones, name, &z)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "managed zone no encontrada")
		return
	}
	server.WriteJSON(w, 200, z)
}

func (s *Service) deleteZone(w http.ResponseWriter, r *http.Request) {
	name := zoneName(r.PathValue("project"), r.PathValue("managedZone"))
	if err := s.db.Delete(bucketZones, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

func (s *Service) listRRSets(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("managedZone")
	prefix := rrsetPrefix(project, zone)
	rrsets := []ResourceRecordSet{}
	_ = s.db.List(bucketRRSets, prefix, func(key string, raw []byte) error {
		var rr ResourceRecordSet
		if err := json.Unmarshal(raw, &rr); err != nil {
			return err
		}
		rrsets = append(rrsets, rr)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"rrsets": rrsets})
}

func (s *Service) createChange(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("managedZone")

	zName := zoneName(project, zone)
	var z ManagedZone
	found, err := s.db.Get(bucketZones, zName, &z)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "managed zone no encontrada")
		return
	}

	var body struct {
		Additions []ResourceRecordSet `json:"additions"`
		Deletions []ResourceRecordSet `json:"deletions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}

	// Las deletions se aplican antes que las additions, igual que la API
	// real (permite reemplazar un rrset existente en un solo Change).
	for _, del := range body.Deletions {
		key := rrsetKey(project, zone, del.Name, del.Type)
		if err := s.db.Delete(bucketRRSets, key); err != nil {
			server.WriteError(w, 500, "INTERNAL", err.Error())
			return
		}
	}
	for _, add := range body.Additions {
		key := rrsetKey(project, zone, add.Name, add.Type)
		if err := s.db.Put(bucketRRSets, key, add); err != nil {
			server.WriteError(w, 500, "INTERNAL", err.Error())
			return
		}
	}

	s.seq++
	change := Change{
		Id:        fmt.Sprintf("%d", s.seq),
		Status:    "done",
		StartTime: time.Now().UTC().Format(time.RFC3339),
		Additions: body.Additions,
		Deletions: body.Deletions,
	}
	if err := s.db.Put(bucketChanges, changeName(project, zone, change.Id), change); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, change)
}

func (s *Service) listChanges(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("managedZone")
	prefix := fmt.Sprintf("projects/%s/managedZones/%s/changes/", project, zone)
	changes := []Change{}
	_ = s.db.List(bucketChanges, prefix, func(key string, raw []byte) error {
		var c Change
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		changes = append(changes, c)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"changes": changes})
}

func (s *Service) getChange(w http.ResponseWriter, r *http.Request) {
	name := changeName(r.PathValue("project"), r.PathValue("managedZone"), r.PathValue("change"))
	var c Change
	found, err := s.db.Get(bucketChanges, name, &c)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "change no encontrado")
		return
	}
	server.WriteJSON(w, 200, c)
}
