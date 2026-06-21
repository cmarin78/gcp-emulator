// resolve.go añade rrsets:resolve, una extensión propia de este emulador.
// La API real de Cloud DNS no tiene ningún endpoint de resolución: el
// nombre se resuelve vía el resolver interno de la VPC, no vía
// dns.googleapis.com (ver el comentario del paquete). Pero "shape-compatible,
// no behavior-complete" para Fase 11 significa que cuando el grafo de
// rrsets sí existe (porque Terraform/gcloud lo creó), conviene poder
// recorrerlo de verdad -- sigue cadenas de CNAME dentro de la misma zona,
// en vez de limitarse a devolver el rrset con match exacto de (name, type).
// Esto se documenta explícitamente como una extensión, no como un mirror
// de un endpoint real.
package clouddns

import (
	"net/http"
	"strings"

	"github.com/cesar/gcp-emulator/internal/server"
)

// maxCNAMEChain limita la cantidad de saltos de CNAME que se siguen, para
// no entrar en un loop infinito si alguien crea un ciclo (A -> B -> A).
const maxCNAMEChain = 10

// ResolveResult es la forma de respuesta de rrsets:resolve. No mirror de
// ningún recurso real de la API.
type ResolveResult struct {
	Name    string              `json:"name"`
	Type    string              `json:"type"`
	Rcode   string              `json:"rcode"` // NOERROR | NXDOMAIN | CNAME_EXTERNAL
	Chain   []ResourceRecordSet `json:"chain,omitempty"`
	Answer  *ResourceRecordSet  `json:"answer,omitempty"`
	Comment string              `json:"comment,omitempty"`
}

func (s *Service) resolveRRSet(w http.ResponseWriter, r *http.Request) {
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

	name := r.URL.Query().Get("name")
	typ := r.URL.Query().Get("type")
	if name == "" || typ == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name y type son requeridos")
		return
	}
	typ = strings.ToUpper(typ)

	result := s.resolveInZone(project, zone, z.DNSName, name, typ)
	server.WriteJSON(w, 200, result)
}

// resolveInZone busca el rrset (name, type) exacto; si no existe y type no
// es CNAME, sigue una posible cadena de CNAME para ese name (igual que
// haría un resolver real) hasta encontrar el type pedido, salir de la
// zona, o tocar maxCNAMEChain.
func (s *Service) resolveInZone(project, zone, dnsName, name, typ string) ResolveResult {
	visited := map[string]bool{}
	var chain []ResourceRecordSet
	current := name

	for hop := 0; hop <= maxCNAMEChain; hop++ {
		if visited[current] {
			return ResolveResult{
				Name: name, Type: typ, Rcode: "NXDOMAIN", Chain: chain,
				Comment: "CNAME loop detectado en " + current,
			}
		}
		visited[current] = true

		var direct ResourceRecordSet
		found, err := s.db.Get(bucketRRSets, rrsetKey(project, zone, current, typ), &direct)
		if err == nil && found {
			return ResolveResult{Name: name, Type: typ, Rcode: "NOERROR", Chain: chain, Answer: &direct}
		}

		if typ == "CNAME" {
			break
		}
		var cname ResourceRecordSet
		found, err = s.db.Get(bucketRRSets, rrsetKey(project, zone, current, "CNAME"), &cname)
		if err != nil || !found || len(cname.Rrdatas) == 0 {
			break
		}
		chain = append(chain, cname)
		// target conserva el punto final tal cual viene en el rrdata (igual
		// que lo guardó el Change original), porque rrsetKey usa el name tal
		// cual fue creado -- mezclar formas con/sin punto rompería el lookup
		// del siguiente salto.
		target := cname.Rrdatas[0]
		if !strings.HasSuffix(target, dnsName) {
			return ResolveResult{
				Name: name, Type: typ, Rcode: "CNAME_EXTERNAL", Chain: chain,
				Comment: "CNAME apunta fuera de la zona (" + target + "): el emulador no resuelve entre zonas/recursivamente",
			}
		}
		current = target
	}

	return ResolveResult{Name: name, Type: typ, Rcode: "NXDOMAIN", Chain: chain}
}
