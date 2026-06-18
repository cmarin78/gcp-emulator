// Phase 8 of the roadmap: extended networking — routers, routes, and Cloud
// NAT (modeled as a nested config on routers, same as the real API: NAT
// gateways are not a standalone resource, they live in
// router.nats[]). Rounds out the networking family already covered by
// network.go (networks/subnetworks/firewalls).
package compute

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

const (
	bucketRouters = "compute.routers"
	bucketRoutes  = "compute.routes"
)

// RouterNat mirrors the real API's nested NAT config on a router
// (router.nats[]) — Cloud NAT isn't a standalone resource.
type RouterNat struct {
	Name                          string   `json:"name"`
	NatIpAllocateOption           string   `json:"natIpAllocateOption,omitempty"`
	SourceSubnetworkIpRangesToNat string   `json:"sourceSubnetworkIpRangesToNat,omitempty"`
	NatIps                        []string `json:"natIps,omitempty"`
}

// Router mirrors the real compute#router resource (regional).
type Router struct {
	ID                string      `json:"id"`
	Name              string      `json:"name"`
	Network           string      `json:"network"`
	Region            string      `json:"region"`
	Nats              []RouterNat `json:"nats,omitempty"`
	CreationTimestamp string      `json:"creationTimestamp"`
	SelfLink          string      `json:"selfLink"`
}

// Route mirrors the real compute#route resource (global). Only the most
// commonly used next-hop variants are modeled.
type Route struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Network           string   `json:"network"`
	DestRange         string   `json:"destRange"`
	Priority          int64    `json:"priority,omitempty"`
	Tags              []string `json:"tags,omitempty"`
	NextHopGateway    string   `json:"nextHopGateway,omitempty"`
	NextHopIp         string   `json:"nextHopIp,omitempty"`
	NextHopInstance   string   `json:"nextHopInstance,omitempty"`
	CreationTimestamp string   `json:"creationTimestamp"`
	SelfLink          string   `json:"selfLink"`
}

func routerKey(region, name string) string { return region + "/" + name }
func routeKey(name string) string          { return name }

func (s *Service) insertRouter(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	var body struct {
		Name    string      `json:"name"`
		Network string      `json:"network"`
		Nats    []RouterNat `json:"nats"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" || body.Network == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name and network are required")
		return
	}
	rt := Router{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		Network:           normalizeGlobalRef(project, "networks", body.Network),
		Region:            regionPath(project, region),
		Nats:              body.Nats,
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/regions/%s/routers/%s", project, region, body.Name),
	}
	if err := s.db.Put(bucketRouters, routerKey(region, rt.Name), rt); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneRegional("insert", rt.SelfLink, fmt.Sprintf("%s/projects/%s/regions/%s/operations", opsBase(r), project, region), regionPath(project, region))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listRouters(w http.ResponseWriter, r *http.Request) {
	region := r.PathValue("region")
	var items []Router
	_ = s.db.List(bucketRouters, region+"/", func(key string, raw []byte) error {
		var rt Router
		if err := json.Unmarshal(raw, &rt); err != nil {
			return err
		}
		items = append(items, rt)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#routerList", "items": items})
}

func (s *Service) getRouter(w http.ResponseWriter, r *http.Request) {
	region := r.PathValue("region")
	var rt Router
	found, err := s.db.Get(bucketRouters, routerKey(region, r.PathValue("router")), &rt)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "router not found")
		return
	}
	server.WriteJSON(w, 200, rt)
}

// patchRouter replaces the router's nats[] (and other patchable fields),
// matching how Terraform's google_compute_router_nat manages NAT configs:
// it patches the parent router rather than creating a standalone resource.
func (s *Service) patchRouter(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	name := r.PathValue("router")
	var rt Router
	found, err := s.db.Get(bucketRouters, routerKey(region, name), &rt)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "router not found")
		return
	}
	var body struct {
		Nats *[]RouterNat `json:"nats"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Nats != nil {
		rt.Nats = *body.Nats
	}
	if err := s.db.Put(bucketRouters, routerKey(region, name), rt); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneRegional("patch", rt.SelfLink, fmt.Sprintf("%s/projects/%s/regions/%s/operations", opsBase(r), project, region), regionPath(project, region))
	server.WriteJSON(w, 200, op)
}

func (s *Service) deleteRouter(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	region := r.PathValue("region")
	name := r.PathValue("router")
	if err := s.db.Delete(bucketRouters, routerKey(region, name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.DoneRegional("delete", fmt.Sprintf("/compute/v1/projects/%s/regions/%s/routers/%s", project, region, name),
		fmt.Sprintf("%s/projects/%s/regions/%s/operations", opsBase(r), project, region), regionPath(project, region))
	server.WriteJSON(w, 200, op)
}

func (s *Service) insertRoute(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name            string   `json:"name"`
		Network         string   `json:"network"`
		DestRange       string   `json:"destRange"`
		Priority        int64    `json:"priority"`
		Tags            []string `json:"tags"`
		NextHopGateway  string   `json:"nextHopGateway"`
		NextHopIp       string   `json:"nextHopIp"`
		NextHopInstance string   `json:"nextHopInstance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" || body.Network == "" || body.DestRange == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name, network and destRange are required")
		return
	}
	rt := Route{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		Network:           normalizeGlobalRef(project, "networks", body.Network),
		DestRange:         body.DestRange,
		Priority:          body.Priority,
		Tags:              body.Tags,
		NextHopGateway:    normalizeGlobalRef(project, "gateways", body.NextHopGateway),
		NextHopIp:         body.NextHopIp,
		NextHopInstance:   body.NextHopInstance,
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          fmt.Sprintf("/compute/v1/projects/%s/global/routes/%s", project, body.Name),
	}
	if err := s.db.Put(bucketRoutes, routeKey(rt.Name), rt); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", rt.SelfLink, fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listRoutes(w http.ResponseWriter, r *http.Request) {
	var items []Route
	_ = s.db.List(bucketRoutes, "", func(key string, raw []byte) error {
		var rt Route
		if err := json.Unmarshal(raw, &rt); err != nil {
			return err
		}
		items = append(items, rt)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#routeList", "items": items})
}

func (s *Service) getRoute(w http.ResponseWriter, r *http.Request) {
	var rt Route
	found, err := s.db.Get(bucketRoutes, routeKey(r.PathValue("route")), &rt)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "route not found")
		return
	}
	server.WriteJSON(w, 200, rt)
}

func (s *Service) deleteRoute(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("route")
	if err := s.db.Delete(bucketRoutes, routeKey(name)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", fmt.Sprintf("/compute/v1/projects/%s/global/routes/%s", project, name),
		fmt.Sprintf("%s/projects/%s/global/operations", opsBase(r), project))
	server.WriteJSON(w, 200, op)
}
