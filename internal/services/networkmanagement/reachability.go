// reachability.go contiene la evaluación real de "puede A llegar a B" que
// hace que connectivityTests sea más que un CRUD: lee directamente los
// buckets de Compute para networks y firewalls (en vez de importar ese
// paquete, la misma técnica que internal/iamenforce e
// internal/services/billingbudgets ya usan en esta fase, para evitar
// cualquier riesgo de ciclo de imports) y aplica las reglas reales de GCP:
// egress es ALLOW por defecto, ingress es DENY por defecto, y dentro de
// cada dirección la regla de menor "priority" gana.
package networkmanagement

import (
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/cesar/gcp-emulator/internal/storage"
)

// bucketComputeNetworks/bucketComputeFirewalls deben coincidir exactamente
// con los nombres de bucket que internal/services/compute usa (bucketNetworks
// y bucketFirewalls en network.go).
const (
	bucketComputeNetworks  = "compute.networks"
	bucketComputeFirewalls = "compute.firewalls"
)

// computeNetwork es una copia local mínima de la forma JSON de
// compute.Network: solo lo necesario para resolver peerings.
type computeNetwork struct {
	Name     string           `json:"name"`
	Peerings []computePeering `json:"peerings,omitempty"`
}

type computePeering struct {
	Network string `json:"network"`
	State   string `json:"state"`
}

// computeFirewall es una copia local mínima de la forma JSON de
// compute.Firewall.
type computeFirewall struct {
	Name         string                `json:"name"`
	Network      string                `json:"network"`
	Direction    string                `json:"direction"`
	Priority     int64                 `json:"priority,omitempty"`
	SourceRanges []string              `json:"sourceRanges,omitempty"`
	Allowed      []computeFirewallRule `json:"allowed,omitempty"`
	Denied       []computeFirewallRule `json:"denied,omitempty"`
}

type computeFirewallRule struct {
	IPProtocol string   `json:"IPProtocol"`
	Ports      []string `json:"ports,omitempty"`
}

// networkNameOf extrae el nombre corto de una referencia de red (nombre
// corto o selfLink ".../networks/{name}"), igual que compute.normalizeRef
// deja pasar ambas formas sin imponer un formato.
func networkNameOf(ref string) string {
	if ref == "" {
		return "default"
	}
	return lastSegment(ref)
}

func loadComputeNetwork(db *storage.DB, name string) (computeNetwork, bool) {
	var n computeNetwork
	found, err := db.Get(bucketComputeNetworks, name, &n)
	if err != nil || !found {
		return computeNetwork{}, false
	}
	return n, true
}

func loadFirewallsForNetwork(db *storage.DB, networkName, direction string) []computeFirewall {
	var matched []computeFirewall
	_ = db.List(bucketComputeFirewalls, "", func(_ string, raw []byte) error {
		var fw computeFirewall
		if err := json.Unmarshal(raw, &fw); err != nil {
			return nil
		}
		if networkNameOf(fw.Network) != networkName {
			return nil
		}
		if strings.EqualFold(fw.Direction, direction) {
			matched = append(matched, fw)
		}
		return nil
	})
	sort.Slice(matched, func(i, j int) bool { return matched[i].Priority < matched[j].Priority })
	return matched
}

// networksConnected reporta si dos redes (mismo nombre, o unidas por un
// peering ACTIVE en cualquiera de los dos sentidos) permiten que el
// tráfico llegue de una a la otra a nivel de red, antes de mirar
// firewalls.
func networksConnected(db *storage.DB, srcNetwork, dstNetwork string) bool {
	if srcNetwork == dstNetwork {
		return true
	}
	if n, ok := loadComputeNetwork(db, srcNetwork); ok {
		for _, p := range n.Peerings {
			if networkNameOf(p.Network) == dstNetwork && strings.EqualFold(p.State, "ACTIVE") {
				return true
			}
		}
	}
	if n, ok := loadComputeNetwork(db, dstNetwork); ok {
		for _, p := range n.Peerings {
			if networkNameOf(p.Network) == srcNetwork && strings.EqualFold(p.State, "ACTIVE") {
				return true
			}
		}
	}
	return false
}

// cidrContains reporta si ip cae dentro de cidr. Una entrada sin "/" (IP
// suelta) se trata como /32, igual que gcloud acepta en sourceRanges.
func cidrContains(cidr, ip string) bool {
	if !strings.Contains(cidr, "/") {
		cidr += "/32"
	}
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return network.Contains(parsed)
}

// portInRanges reporta si port cae dentro de alguno de los rangos de ports
// (forma real de la API: "80", o "8000-9000"). Una lista vacía significa
// "todos los puertos" para ese protocolo, igual que la API real.
func portInRanges(port int64, ranges []string) bool {
	if len(ranges) == 0 {
		return true
	}
	for _, r := range ranges {
		if lo, hi, ok := parsePortRange(r); ok && port >= lo && port <= hi {
			return true
		}
	}
	return false
}

func parsePortRange(r string) (lo, hi int64, ok bool) {
	if idx := strings.IndexByte(r, '-'); idx >= 0 {
		a, err1 := strconv.ParseInt(r[:idx], 10, 64)
		b, err2 := strconv.ParseInt(r[idx+1:], 10, 64)
		if err1 != nil || err2 != nil {
			return 0, 0, false
		}
		return a, b, true
	}
	v, err := strconv.ParseInt(r, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return v, v, true
}

// ruleMatches reporta si una regla de firewall (Allowed o Denied) cubre el
// protocolo/puerto del test. "all" es el wildcard real de IPProtocol.
func ruleMatches(rule computeFirewallRule, protocol string, port int64) bool {
	if !strings.EqualFold(rule.IPProtocol, protocol) && !strings.EqualFold(rule.IPProtocol, "all") {
		return false
	}
	return portInRanges(port, rule.Ports)
}

// evaluateLeg evalúa, para una dirección (INGRESS o EGRESS) sobre una red,
// si el tráfico pasa: recorre las reglas de esa dirección ordenadas por
// priority ascendente (la API real evalúa así: menor número = mayor
// precedencia) y se queda con la primera que matchea el CIDR relevante y
// el protocolo/puerto. Si ninguna regla matchea, aplica el default real de
// GCP: INGRESS deniega por defecto, EGRESS permite por defecto.
func evaluateLeg(db *storage.DB, networkName, direction, relevantIP, protocol string, port int64) (allowed bool, steps []Step) {
	rules := loadFirewallsForNetwork(db, networkName, direction)
	for _, fw := range rules {
		ranges := fw.SourceRanges
		if len(ranges) == 0 {
			ranges = []string{"0.0.0.0/0"}
		}
		ipMatches := false
		for _, cidr := range ranges {
			if cidrContains(cidr, relevantIP) {
				ipMatches = true
				break
			}
		}
		if !ipMatches {
			continue
		}
		for _, rule := range fw.Allowed {
			if ruleMatches(rule, protocol, port) {
				return true, []Step{{
					State:       "FIREWALL_RULE",
					Description: direction + " allowed by firewall '" + fw.Name + "' (priority " + strconv.FormatInt(fw.Priority, 10) + ")",
				}}
			}
		}
		for _, rule := range fw.Denied {
			if ruleMatches(rule, protocol, port) {
				return false, []Step{{
					State:       "FIREWALL_RULE",
					Description: direction + " denied by firewall '" + fw.Name + "' (priority " + strconv.FormatInt(fw.Priority, 10) + ")",
				}}
			}
		}
	}
	if strings.EqualFold(direction, "EGRESS") {
		return true, []Step{{State: "DEFAULT", Description: "EGRESS allowed by implied default (no matching rule)"}}
	}
	return false, []Step{{State: "DEFAULT", Description: "INGRESS denied by implied default (no matching rule)"}}
}

// evaluateReachability es el punto de entrada usado por createTest/
// updateTest/rerunTest: decide REACHABLE/UNREACHABLE para un par
// source/destination y construye un único Trace describiendo por qué.
func evaluateReachability(db *storage.DB, source, destination Endpoint, protocol string) *ReachabilityDetails {
	srcNetwork := networkNameOf(source.Network)
	dstNetwork := networkNameOf(destination.Network)

	if !networksConnected(db, srcNetwork, dstNetwork) {
		return &ReachabilityDetails{
			Result: "UNREACHABLE",
			Traces: []Trace{{Steps: []Step{{
				State:       "NETWORK_PEERING",
				Description: "networks '" + srcNetwork + "' and '" + dstNetwork + "' are not the same network and have no ACTIVE peering between them",
			}}}},
		}
	}

	var steps []Step
	steps = append(steps, Step{State: "START", Description: "source network '" + srcNetwork + "' can reach destination network '" + dstNetwork + "'"})

	egressOK, egressSteps := evaluateLeg(db, srcNetwork, "EGRESS", destination.IPAddress, protocol, destination.Port)
	steps = append(steps, egressSteps...)
	if !egressOK {
		return &ReachabilityDetails{Result: "UNREACHABLE", Traces: []Trace{{Steps: steps}}}
	}

	ingressOK, ingressSteps := evaluateLeg(db, dstNetwork, "INGRESS", source.IPAddress, protocol, destination.Port)
	steps = append(steps, ingressSteps...)
	if !ingressOK {
		return &ReachabilityDetails{Result: "UNREACHABLE", Traces: []Trace{{Steps: steps}}}
	}

	steps = append(steps, Step{State: "FINAL", Description: "delivered"})
	return &ReachabilityDetails{Result: "REACHABLE", Traces: []Trace{{Steps: steps}}}
}
