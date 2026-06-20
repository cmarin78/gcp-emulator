// Phase 9 of the roadmap: instance templates (global, immutable resource —
// the real API has no update endpoint, only insert/get/list/delete, which
// this emulator mirrors). Modeled as a thin wrapper: the template's
// "properties" sub-object reuses the same Disks/NetworkInterfaces/Metadata
// shapes already defined for Instance in compute.go, since
// google_compute_instance_template's schema is effectively an Instance
// minus the zone-specific fields (name/zone/status).
package compute

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

const bucketInstanceTemplates = "compute.instanceTemplates"

// InstanceTemplateProperties mirrors the relevant subset of
// compute#instanceProperties embedded in an instance template.
type InstanceTemplateProperties struct {
	MachineType       string            `json:"machineType,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Disks             []AttachedDisk    `json:"disks,omitempty"`
	NetworkInterfaces []NetworkIface    `json:"networkInterfaces,omitempty"`
	Metadata          Metadata          `json:"metadata"`
	Tags              Tags              `json:"tags"`
	Scheduling        Scheduling        `json:"scheduling"`
}

// InstanceTemplate mirrors compute#instanceTemplate (global, immutable).
type InstanceTemplate struct {
	ID                string                     `json:"id"`
	Name              string                     `json:"name"`
	Description       string                     `json:"description,omitempty"`
	Properties        InstanceTemplateProperties `json:"properties"`
	CreationTimestamp string                     `json:"creationTimestamp"`
	SelfLink          string                     `json:"selfLink"`
}

func instanceTemplateKey(name string) string { return name }

func (s *Service) registerInstanceTemplates(mux *http.ServeMux) {
	mux.HandleFunc("POST /compute/v1/projects/{project}/global/instanceTemplates", s.insertInstanceTemplate)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/instanceTemplates", s.listInstanceTemplates)
	mux.HandleFunc("GET /compute/v1/projects/{project}/global/instanceTemplates/{instanceTemplate}", s.getInstanceTemplate)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/global/instanceTemplates/{instanceTemplate}", s.deleteInstanceTemplate)
}

func (s *Service) insertInstanceTemplate(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name        string                     `json:"name"`
		Description string                     `json:"description"`
		Properties  InstanceTemplateProperties `json:"properties"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name es requerido")
		return
	}
	var existing InstanceTemplate
	found, err := s.db.Get(bucketInstanceTemplates, instanceTemplateKey(body.Name), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "instance template ya existe: "+body.Name)
		return
	}

	props := body.Properties
	props.NetworkInterfaces = s.resolveNetworkInterfaces(project, props.NetworkInterfaces)
	if props.MachineType == "" {
		props.MachineType = "e2-medium"
	}
	props.Metadata.Kind = orDefault(props.Metadata.Kind, "compute#metadata")
	if props.Metadata.Fingerprint == "" {
		props.Metadata.Fingerprint = fakeFingerprint(fmt.Sprintf("tmpl-meta-%d", s.nextSeq()))
	}
	if props.Tags.Fingerprint == "" {
		props.Tags.Fingerprint = fakeFingerprint(fmt.Sprintf("tmpl-tags-%d", s.nextSeq()))
	}

	tpl := InstanceTemplate{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		Description:       body.Description,
		Properties:        props,
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/global/instanceTemplates/%s", project, body.Name),
	}
	if err := s.db.Put(bucketInstanceTemplates, instanceTemplateKey(tpl.Name), tpl); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", tpl.SelfLink, fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listInstanceTemplates(w http.ResponseWriter, r *http.Request) {
	var items []InstanceTemplate
	_ = s.db.List(bucketInstanceTemplates, "", func(key string, raw []byte) error {
		var tpl InstanceTemplate
		if err := json.Unmarshal(raw, &tpl); err != nil {
			return err
		}
		items = append(items, tpl)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#instanceTemplateList", "items": items})
}

func (s *Service) getInstanceTemplate(w http.ResponseWriter, r *http.Request) {
	var tpl InstanceTemplate
	found, err := s.db.Get(bucketInstanceTemplates, instanceTemplateKey(r.PathValue("instanceTemplate")), &tpl)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance template no encontrado")
		return
	}
	server.WriteJSON(w, 200, tpl)
}

func (s *Service) deleteInstanceTemplate(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("instanceTemplate")
	if err := s.db.Delete(bucketInstanceTemplates, instanceTemplateKey(name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", fmt.Sprintf("/compute/v1/projects/%s/global/instanceTemplates/%s", project, name),
		fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project))
	server.WriteJSON(w, 200, op)
}
