// Phase 9 of the roadmap: managed instance groups (zonal) + autoscalers.
// The single biggest remaining gap in Compute coverage before this: instance
// templates alone aren't enough to satisfy real Terraform configs, which
// almost always pair google_compute_instance_template with
// google_compute_instance_group_manager (+ optionally
// google_compute_autoscaler). Shape-only, like the rest of this emulator:
// no real fleet management, scaling, or health-check-driven recreation.
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
	bucketInstanceGroupManagers = "compute.instanceGroupManagers"
	bucketAutoscalers           = "compute.autoscalers"
)

// InstanceGroupManager mirrors compute#instanceGroupManager (zonal).
type InstanceGroupManager struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	BaseInstanceName  string `json:"baseInstanceName"`
	InstanceTemplate  string `json:"instanceTemplate"`
	TargetSize        int64  `json:"targetSize"`
	Zone              string `json:"zone"`
	InstanceGroup     string `json:"instanceGroup"`
	CreationTimestamp string `json:"creationTimestamp"`
	SelfLink          string `json:"selfLink"`
}

// AutoscalerPolicy mirrors the relevant subset of compute#autoscalerPolicy.
type AutoscalerPolicy struct {
	MaxNumReplicas    int64 `json:"maxNumReplicas,omitempty"`
	MinNumReplicas    int64 `json:"minNumReplicas,omitempty"`
	CoolDownPeriodSec int64 `json:"coolDownPeriodSec,omitempty"`
}

// Autoscaler mirrors compute#autoscaler (zonal).
type Autoscaler struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Target            string           `json:"target"`
	AutoscalingPolicy AutoscalerPolicy `json:"autoscalingPolicy"`
	Zone              string           `json:"zone"`
	CreationTimestamp string           `json:"creationTimestamp"`
	SelfLink          string           `json:"selfLink"`
}

func migKey(zone, name string) string        { return zone + "/" + name }
func autoscalerKey(zone, name string) string { return zone + "/" + name }

// migNameFromTarget extrae el nombre del instance group manager de un
// autoscaler.target (".../instanceGroupManagers/{name}", absoluto o
// relativo) -- usado para encontrar el MIG real al que un autoscaler está
// atado, en vez de tratar autoscalingPolicy como metadata decorativa.
func migNameFromTarget(target string) string {
	const marker = "/instanceGroupManagers/"
	if idx := strings.Index(target, marker); idx >= 0 {
		return target[idx+len(marker):]
	}
	// target sin el marcador "/instanceGroupManagers/" -- algunos clientes
	// (y el test de este paquete) mandan solo el nombre del MIG en vez de
	// una referencia completa; tratamos el último segmento de path (o el
	// string entero si no tiene barras) como el nombre.
	if idx := strings.LastIndexByte(target, '/'); idx >= 0 {
		return target[idx+1:]
	}
	return target
}

// clampToAutoscalerPolicy aplica los límites min/maxNumReplicas de una
// autoscaling policy sobre un tamaño propuesto. Un límite en cero (su valor
// por defecto si el cliente no lo seteó) se ignora, igual que la API real
// solo aplica los límites que el cliente configuró explícitamente.
func clampToAutoscalerPolicy(size int64, p AutoscalerPolicy) int64 {
	if p.MinNumReplicas > 0 && size < p.MinNumReplicas {
		size = p.MinNumReplicas
	}
	if p.MaxNumReplicas > 0 && size > p.MaxNumReplicas {
		size = p.MaxNumReplicas
	}
	return size
}

// findAutoscalerForTarget busca, entre los autoscalers de una zona, el que
// apunta al instance group manager dado. Esto es lo que cierra la brecha de
// Fase 11 para este recurso: antes, autoscalingPolicy.min/maxNumReplicas se
// guardaba pero nunca afectaba targetSize del MIG; ahora cualquier resize o
// patch real queda sujeto a esos límites.
func (s *Service) findAutoscalerForTarget(zone, migName string) (Autoscaler, bool) {
	var found Autoscaler
	ok := false
	_ = s.db.List(bucketAutoscalers, zone+"/", func(key string, raw []byte) error {
		var as Autoscaler
		if err := json.Unmarshal(raw, &as); err != nil {
			return nil
		}
		if migNameFromTarget(as.Target) == migName {
			found, ok = as, true
		}
		return nil
	})
	return found, ok
}

// reconcileMIGSize aplica (si existe) el clamp del autoscaler atado a mig
// sobre un targetSize propuesto por el cliente (vía PATCH o resize), y deja
// el resultado en mig.TargetSize para que el caller lo persista.
func (s *Service) reconcileMIGSize(zone string, mig *InstanceGroupManager, proposed int64) {
	size := proposed
	if as, ok := s.findAutoscalerForTarget(zone, mig.Name); ok {
		size = clampToAutoscalerPolicy(size, as.AutoscalingPolicy)
	}
	mig.TargetSize = size
}

// reconcileAttachedMIG vuelve a aplicar los límites min/maxNumReplicas de un
// autoscaler sobre el targetSize actual de su MIG, persistiendo el cambio si
// corresponde. Se llama al crear o modificar un autoscaler, para que tenga
// un efecto real e inmediato sobre el grupo en vez de esperar al próximo
// resize manual (p.ej. bajar maxNumReplicas por debajo del tamaño actual
// debe achicar el grupo de verdad).
func (s *Service) reconcileAttachedMIG(zone string, as Autoscaler) {
	migName := migNameFromTarget(as.Target)
	if migName == "" {
		return
	}
	var mig InstanceGroupManager
	found, err := s.db.Get(bucketInstanceGroupManagers, migKey(zone, migName), &mig)
	if err != nil || !found {
		return
	}
	clamped := clampToAutoscalerPolicy(mig.TargetSize, as.AutoscalingPolicy)
	if clamped != mig.TargetSize {
		mig.TargetSize = clamped
		_ = s.db.Put(bucketInstanceGroupManagers, migKey(zone, migName), mig)
	}
}

func (s *Service) registerInstanceGroups(mux *http.ServeMux) {
	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroupManagers", s.insertInstanceGroupManager)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instanceGroupManagers", s.listInstanceGroupManagers)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instanceGroupManagers/{instanceGroupManager}", s.getInstanceGroupManager)
	mux.HandleFunc("PATCH /compute/v1/projects/{project}/zones/{zone}/instanceGroupManagers/{instanceGroupManager}", s.patchInstanceGroupManager)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/instanceGroupManagers/{instanceGroupManager}", s.deleteInstanceGroupManager)
	// resize is the dedicated verb the real API uses to change targetSize
	// (instead of a generic PATCH), used by gcloud compute instance-groups
	// managed resize.
	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroupManagers/{instanceGroupManager}/resize", s.resizeInstanceGroupManager)

	mux.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/autoscalers", s.insertAutoscaler)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/autoscalers", s.listAutoscalers)
	mux.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/autoscalers/{autoscaler}", s.getAutoscaler)
	mux.HandleFunc("PATCH /compute/v1/projects/{project}/zones/{zone}/autoscalers/{autoscaler}", s.patchAutoscaler)
	mux.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/autoscalers/{autoscaler}", s.deleteAutoscaler)
}

func (s *Service) insertInstanceGroupManager(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	var body struct {
		Name             string `json:"name"`
		BaseInstanceName string `json:"baseInstanceName"`
		InstanceTemplate string `json:"instanceTemplate"`
		TargetSize       int64  `json:"targetSize"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" || body.InstanceTemplate == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name e instanceTemplate son requeridos")
		return
	}
	var existing InstanceGroupManager
	found, err := s.db.Get(bucketInstanceGroupManagers, migKey(zone, body.Name), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "instance group manager ya existe: "+body.Name)
		return
	}
	mig := InstanceGroupManager{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		BaseInstanceName:  orDefault(body.BaseInstanceName, body.Name),
		InstanceTemplate:  normalizeGlobalRef(project, "instanceTemplates", body.InstanceTemplate),
		TargetSize:        body.TargetSize,
		Zone:              zonePath(project, zone),
		InstanceGroup:     fmt.Sprintf("/compute/v1/projects/%s/zones/%s/instanceGroups/%s", project, zone, body.Name),
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/zones/%s/instanceGroupManagers/%s", project, zone, body.Name),
	}
	if err := s.db.Put(bucketInstanceGroupManagers, migKey(zone, mig.Name), mig); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneZonal("insert", mig.SelfLink, fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listInstanceGroupManagers(w http.ResponseWriter, r *http.Request) {
	zone := r.PathValue("zone")
	items := []InstanceGroupManager{}
	_ = s.db.List(bucketInstanceGroupManagers, zone+"/", func(key string, raw []byte) error {
		var mig InstanceGroupManager
		if err := json.Unmarshal(raw, &mig); err != nil {
			return err
		}
		items = append(items, mig)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#instanceGroupManagerList", "items": items})
}

func (s *Service) getInstanceGroupManager(w http.ResponseWriter, r *http.Request) {
	zone := r.PathValue("zone")
	var mig InstanceGroupManager
	found, err := s.db.Get(bucketInstanceGroupManagers, migKey(zone, r.PathValue("instanceGroupManager")), &mig)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance group manager no encontrado")
		return
	}
	server.WriteJSON(w, 200, mig)
}

func (s *Service) patchInstanceGroupManager(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	name := r.PathValue("instanceGroupManager")
	var mig InstanceGroupManager
	found, err := s.db.Get(bucketInstanceGroupManagers, migKey(zone, name), &mig)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance group manager no encontrado")
		return
	}
	var body struct {
		InstanceTemplate string `json:"instanceTemplate"`
		TargetSize       *int64 `json:"targetSize"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.InstanceTemplate != "" {
		mig.InstanceTemplate = normalizeGlobalRef(project, "instanceTemplates", body.InstanceTemplate)
	}
	if body.TargetSize != nil {
		s.reconcileMIGSize(zone, &mig, *body.TargetSize)
	}
	if err := s.db.Put(bucketInstanceGroupManagers, migKey(zone, name), mig); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneZonal("patch", mig.SelfLink, fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) resizeInstanceGroupManager(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	name := r.PathValue("instanceGroupManager")
	var mig InstanceGroupManager
	found, err := s.db.Get(bucketInstanceGroupManagers, migKey(zone, name), &mig)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "instance group manager no encontrado")
		return
	}
	size, err := parseQueryInt(r, "size")
	if err == nil {
		s.reconcileMIGSize(zone, &mig, size)
	}
	if err := s.db.Put(bucketInstanceGroupManagers, migKey(zone, name), mig); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneZonal("resize", mig.SelfLink, fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) deleteInstanceGroupManager(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	name := r.PathValue("instanceGroupManager")
	if err := s.db.Delete(bucketInstanceGroupManagers, migKey(zone, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneZonal("delete", fmt.Sprintf("/compute/v1/projects/%s/zones/%s/instanceGroupManagers/%s", project, zone, name),
		fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) insertAutoscaler(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	var body struct {
		Name              string           `json:"name"`
		Target            string           `json:"target"`
		AutoscalingPolicy AutoscalerPolicy `json:"autoscalingPolicy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" || body.Target == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name y target son requeridos")
		return
	}
	var existing Autoscaler
	found, err := s.db.Get(bucketAutoscalers, autoscalerKey(zone, body.Name), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "autoscaler ya existe: "+body.Name)
		return
	}
	as := Autoscaler{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		Target:            normalizeRef(body.Target),
		AutoscalingPolicy: body.AutoscalingPolicy,
		Zone:              zonePath(project, zone),
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/zones/%s/autoscalers/%s", project, zone, body.Name),
	}
	if err := s.db.Put(bucketAutoscalers, autoscalerKey(zone, as.Name), as); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.reconcileAttachedMIG(zone, as)
	op := s.ops.DoneZonal("insert", as.SelfLink, fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listAutoscalers(w http.ResponseWriter, r *http.Request) {
	zone := r.PathValue("zone")
	items := []Autoscaler{}
	_ = s.db.List(bucketAutoscalers, zone+"/", func(key string, raw []byte) error {
		var as Autoscaler
		if err := json.Unmarshal(raw, &as); err != nil {
			return err
		}
		items = append(items, as)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#autoscalerList", "items": items})
}

func (s *Service) getAutoscaler(w http.ResponseWriter, r *http.Request) {
	zone := r.PathValue("zone")
	var as Autoscaler
	found, err := s.db.Get(bucketAutoscalers, autoscalerKey(zone, r.PathValue("autoscaler")), &as)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "autoscaler no encontrado")
		return
	}
	server.WriteJSON(w, 200, as)
}

func (s *Service) patchAutoscaler(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	name := r.PathValue("autoscaler")
	var as Autoscaler
	found, err := s.db.Get(bucketAutoscalers, autoscalerKey(zone, name), &as)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "autoscaler no encontrado")
		return
	}
	var body struct {
		AutoscalingPolicy *AutoscalerPolicy `json:"autoscalingPolicy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.AutoscalingPolicy != nil {
		as.AutoscalingPolicy = *body.AutoscalingPolicy
	}
	if err := s.db.Put(bucketAutoscalers, autoscalerKey(zone, name), as); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	s.reconcileAttachedMIG(zone, as)
	op := s.ops.DoneZonal("patch", as.SelfLink, fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

func (s *Service) deleteAutoscaler(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	zone := r.PathValue("zone")
	name := r.PathValue("autoscaler")
	if err := s.db.Delete(bucketAutoscalers, autoscalerKey(zone, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneZonal("delete", fmt.Sprintf("/compute/v1/projects/%s/zones/%s/autoscalers/%s", project, zone, name),
		fmt.Sprintf("%s/projects/%s/zones/%s/operations", opsBase(r), project, zone), zonePath(project, zone))
	server.WriteJSON(w, 200, op)
}

func parseQueryInt(r *http.Request, key string) (int64, error) {
	v := r.URL.Query().Get(key)
	var n int64
	_, err := fmt.Sscanf(v, "%d", &n)
	return n, err
}
