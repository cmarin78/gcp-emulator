// Package networkmanagement emulates a subset of the Network Management API
// (networkmanagement.googleapis.com/v1): Connectivity Tests
// (google_network_management_connectivity_test in Terraform). This is the
// Phase 11 "Networking" item from ROADMAP.md: a real "can A reach B" trace
// across firewalls and network peerings, instead of either not existing at
// all or returning a fixed status.
//
// Real connectivityTests run continuously and creation is a long-running
// Operation; here the analysis runs synchronously on create/rerun and the
// resource is returned directly, without an Operation wrapper -- the same
// simplification billingbudgets/orgpolicy already use elsewhere in this
// project for non-Compute APIs. The analysis itself is also intentionally
// narrower than the real Reachability Analyzer: it evaluates firewalls
// (ingress on the destination network, egress on the source network) and
// network peerings, but not routes/routers -- routes determine *how*
// traffic gets somewhere, while firewalls/peerings determine *whether*
// it's allowed to, which is the higher-value signal for "did I lock myself
// out" style checks Terraform users actually hit.
package networkmanagement

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketTests = "networkmanagement.connectivityTests"

// Endpoint mirrors the relevant subset of networkmanagement#Endpoint.
type Endpoint struct {
	IPAddress string `json:"ipAddress,omitempty"`
	Network   string `json:"network,omitempty"`
	Port      int64  `json:"port,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
}

// Step mirrors a single hop of networkmanagement#Trace.Step (subset).
type Step struct {
	Description string `json:"description"`
	State       string `json:"state"`
}

// Trace mirrors networkmanagement#Trace (subset: a single end-to-end trace,
// since this emulator's analysis isn't probabilistic/multi-path like the
// real Reachability Analyzer).
type Trace struct {
	Steps []Step `json:"steps"`
}

// ReachabilityDetails mirrors networkmanagement#ReachabilityDetails (subset).
type ReachabilityDetails struct {
	Result string  `json:"result"` // REACHABLE | UNREACHABLE
	Traces []Trace `json:"traces,omitempty"`
}

// ConnectivityTest mirrors networkmanagement#ConnectivityTest (subset
// sufficient for google_network_management_connectivity_test).
type ConnectivityTest struct {
	Name                string               `json:"name"` // projects/{p}/locations/global/connectivityTests/{id}
	DisplayName         string               `json:"displayName,omitempty"`
	Protocol            string               `json:"protocol,omitempty"`
	Source              Endpoint             `json:"source"`
	Destination         Endpoint             `json:"destination"`
	ReachabilityDetails *ReachabilityDetails `json:"reachabilityDetails,omitempty"`
	CreateTime          string               `json:"createTime,omitempty"`
	UpdateTime          string               `json:"updateTime,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64
}

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/projects/{project}/locations/global/connectivityTests", s.createTest)
	mux.HandleFunc("GET /v1/projects/{project}/locations/global/connectivityTests", s.listTests)
	mux.HandleFunc("GET /v1/projects/{project}/locations/global/connectivityTests/{test}", s.getTest)
	mux.HandleFunc("PATCH /v1/projects/{project}/locations/global/connectivityTests/{test}", s.updateTest)
	mux.HandleFunc("DELETE /v1/projects/{project}/locations/global/connectivityTests/{test}", s.deleteTest)
	// "{test}:rerun" no se puede expresar como un patrón mixto en el mux de
	// Go (un wildcard debe ocupar el segmento completo); se captura el
	// segmento entero y se separa con strings.Cut, mismo patrón que
	// Cloud Scheduler usa para "{job}:run"/":pause"/":resume".
	mux.HandleFunc("POST /v1/projects/{project}/locations/global/connectivityTests/{testAction}", s.testAction)
}

func testKey(project, id string) string { return project + "/" + id }

func testName(project, id string) string {
	return fmt.Sprintf("projects/%s/locations/global/connectivityTests/%s", project, id)
}

func (s *Service) nextSeq() int64 {
	s.seq++
	return s.seq
}

func (s *Service) createTest(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name        string   `json:"name"`
		DisplayName string   `json:"displayName"`
		Protocol    string   `json:"protocol"`
		Source      Endpoint `json:"source"`
		Destination Endpoint `json:"destination"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	id := lastSegment(body.Name)
	var existing ConnectivityTest
	found, err := s.db.Get(bucketTests, testKey(project, id), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "connectivity test ya existe: "+id)
		return
	}
	ct := ConnectivityTest{
		Name:        testName(project, id),
		DisplayName: orDefault(body.DisplayName, id),
		Protocol:    orDefault(body.Protocol, "TCP"),
		Source:      body.Source,
		Destination: body.Destination,
		CreateTime:  time.Now().UTC().Format(time.RFC3339),
	}
	ct.UpdateTime = ct.CreateTime
	ct.ReachabilityDetails = evaluateReachability(s.db, ct.Source, ct.Destination, ct.Protocol)
	if err := s.db.Put(bucketTests, testKey(project, id), ct); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, ct)
}

func (s *Service) listTests(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	items := []ConnectivityTest{}
	_ = s.db.List(bucketTests, project+"/", func(_ string, raw []byte) error {
		var ct ConnectivityTest
		if err := json.Unmarshal(raw, &ct); err != nil {
			return err
		}
		items = append(items, ct)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"resources": items})
}

func (s *Service) getTest(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("test")
	var ct ConnectivityTest
	found, err := s.db.Get(bucketTests, testKey(project, id), &ct)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "connectivity test no encontrado")
		return
	}
	server.WriteJSON(w, 200, ct)
}

func (s *Service) updateTest(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("test")
	var ct ConnectivityTest
	found, err := s.db.Get(bucketTests, testKey(project, id), &ct)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "connectivity test no encontrado")
		return
	}
	var body struct {
		DisplayName string    `json:"displayName"`
		Protocol    string    `json:"protocol"`
		Source      *Endpoint `json:"source"`
		Destination *Endpoint `json:"destination"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.DisplayName != "" {
		ct.DisplayName = body.DisplayName
	}
	if body.Protocol != "" {
		ct.Protocol = body.Protocol
	}
	if body.Source != nil {
		ct.Source = *body.Source
	}
	if body.Destination != nil {
		ct.Destination = *body.Destination
	}
	ct.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	ct.ReachabilityDetails = evaluateReachability(s.db, ct.Source, ct.Destination, ct.Protocol)
	if err := s.db.Put(bucketTests, testKey(project, id), ct); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, ct)
}

func (s *Service) deleteTest(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	id := r.PathValue("test")
	if err := s.db.Delete(bucketTests, testKey(project, id)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

// testAction despacha el único custom method de este recurso ("{test}:rerun").
// rerun re-evalúa reachability para un test existente contra el estado
// actual de firewalls/peerings -- ese es el punto de Fase 11 para este
// recurso: un cambio de firewall hecho *después* de crear el test tiene un
// efecto real y observable la próxima vez que se rerun-ea, en vez de que
// reachabilityDetails quede congelado en el momento de creación para
// siempre.
func (s *Service) testAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := strings.Cut(r.PathValue("testAction"), ":")
	if !ok || action != "rerun" {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}

	project := r.PathValue("project")
	var ct ConnectivityTest
	found, err := s.db.Get(bucketTests, testKey(project, id), &ct)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "connectivity test no encontrado")
		return
	}
	ct.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	ct.ReachabilityDetails = evaluateReachability(s.db, ct.Source, ct.Destination, ct.Protocol)
	if err := s.db.Put(bucketTests, testKey(project, id), ct); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, ct)
}

func lastSegment(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return s[i+1:]
		}
	}
	return s
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
