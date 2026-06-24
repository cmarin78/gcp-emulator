// domainmappings.go adds Cloud Run Domain Mappings, the one Cloud Run
// resource that the real API still exposes through the older Knative-style
// surface (domains.cloudrun.com/v1) instead of run.googleapis.com/v2 used
// by the rest of this package — there is no v2 equivalent yet, so
// Terraform's google_cloud_run_domain_mapping and `gcloud run domain-
// mappings` both talk to this exact path. Like the rest of this package,
// mutations resolve synchronously: the emulator doesn't model real DNS
// verification, so status.resourceRecords/conditions are populated
// immediately as if Google had already verified the domain.
package cloudrun

import (
	"encoding/json"
	"net/http"

	"github.com/cesar/gcp-emulator/internal/server"
)

const bucketDomainMappings = "cloudrun.domainmappings"

// DomainMappingMetadata mirrors the relevant subset of the Knative
// ObjectMeta used by DomainMapping.
type DomainMappingMetadata struct {
	Name      string `json:"name"`      // the custom domain itself, e.g. "www.example.com"
	Namespace string `json:"namespace"` // project ID
}

// DomainMappingSpec mirrors DomainMapping#DomainMappingSpec.
type DomainMappingSpec struct {
	RouteName       string `json:"routeName"`
	CertificateMode string `json:"certificateMode,omitempty"`
	ForceOverride   bool   `json:"forceOverride,omitempty"`
}

// ResourceRecord mirrors DomainMapping#ResourceRecord.
type ResourceRecord struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Rrdata string `json:"rrdata"`
}

// DomainMappingCondition mirrors the minimal Knative condition shape used
// elsewhere in this codebase (cloudrun.Condition, but DomainMapping's JSON
// uses lowercase "status" instead of "state").
type DomainMappingCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// DomainMappingStatus mirrors DomainMapping#DomainMappingStatus.
type DomainMappingStatus struct {
	ResourceRecords    []ResourceRecord          `json:"resourceRecords,omitempty"`
	MappedRouteName    string                    `json:"mappedRouteName,omitempty"`
	Conditions         []DomainMappingCondition  `json:"conditions,omitempty"`
	ObservedGeneration int                       `json:"observedGeneration,omitempty"`
}

// DomainMapping mirrors google.cloud.run.v1.DomainMapping (the Knative-
// shaped resource the real API still uses for this one feature).
type DomainMapping struct {
	APIVersion string                `json:"apiVersion"`
	Kind       string                `json:"kind"`
	Metadata   DomainMappingMetadata `json:"metadata"`
	Spec       DomainMappingSpec     `json:"spec"`
	Status     *DomainMappingStatus  `json:"status,omitempty"`
}

func domainMappingKey(namespace, name string) string { return namespace + "/" + name }

// registerDomainMappings mounts the domains.cloudrun.com/v1 routes. Called
// from cloudrun.Register alongside the v2 service/job routes.
func (s *Svc) registerDomainMappings(mux *http.ServeMux) {
	mux.HandleFunc("POST /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings", s.createDomainMapping)
	mux.HandleFunc("GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings", s.listDomainMappings)
	mux.HandleFunc("GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}", s.getDomainMapping)
	mux.HandleFunc("DELETE /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}", s.deleteDomainMapping)
}

func (s *Svc) createDomainMapping(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	var body struct {
		Metadata DomainMappingMetadata `json:"metadata"`
		Spec     DomainMappingSpec     `json:"spec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Metadata.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "metadata.name (el dominio) es requerido")
		return
	}
	if body.Spec.RouteName == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "spec.routeName es requerido")
		return
	}
	key := domainMappingKey(namespace, body.Metadata.Name)
	var existing DomainMapping
	found, err := s.db.Get(bucketDomainMappings, key, &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "el domain mapping ya existe: "+body.Metadata.Name)
		return
	}

	dm := DomainMapping{
		APIVersion: "domains.cloudrun.com/v1",
		Kind:       "DomainMapping",
		Metadata:   DomainMappingMetadata{Name: body.Metadata.Name, Namespace: namespace},
		Spec:       body.Spec,
		// The real API only fills Status in once Google's domain
		// verification + cert provisioning finishes; this emulator has no
		// real DNS to verify against, so -- consistent with the rest of
		// the project's "shape-compatible, not behavior-complete" default
		// -- it resolves immediately as already-ready, the same documented
		// shortcut cloudrun.Service.TerminalCondition already takes.
		Status: &DomainMappingStatus{
			ResourceRecords: []ResourceRecord{
				{Name: body.Metadata.Name, Type: "CNAME", Rrdata: "ghs.googlehosted.com."},
			},
			MappedRouteName:    body.Spec.RouteName,
			Conditions:         []DomainMappingCondition{{Type: "Ready", Status: "True"}},
			ObservedGeneration: 1,
		},
	}
	if err := s.db.Put(bucketDomainMappings, key, dm); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, dm)
}

func (s *Svc) listDomainMappings(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	items := []DomainMapping{}
	_ = s.db.List(bucketDomainMappings, namespace+"/", func(_ string, raw []byte) error {
		var dm DomainMapping
		if err := json.Unmarshal(raw, &dm); err != nil {
			return err
		}
		items = append(items, dm)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"items": items})
}

func (s *Svc) getDomainMapping(w http.ResponseWriter, r *http.Request) {
	key := domainMappingKey(r.PathValue("namespace"), r.PathValue("name"))
	var dm DomainMapping
	found, err := s.db.Get(bucketDomainMappings, key, &dm)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "domain mapping no encontrado")
		return
	}
	server.WriteJSON(w, 200, dm)
}

func (s *Svc) deleteDomainMapping(w http.ResponseWriter, r *http.Request) {
	key := domainMappingKey(r.PathValue("namespace"), r.PathValue("name"))
	found, err := s.db.Get(bucketDomainMappings, key, &DomainMapping{})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "domain mapping no encontrado")
		return
	}
	if err := s.db.Delete(bucketDomainMappings, key); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}
