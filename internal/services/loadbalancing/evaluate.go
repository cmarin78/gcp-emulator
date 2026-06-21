// Phase 11 of the roadmap: real rule evaluation for Cloud Armor
// securityPolicies against simulated request attributes. Real Cloud Armor
// has no public "evaluate this request" endpoint at all — enforcement
// happens invisibly in front of the load balancer, never via a direct call
// to compute.googleapis.com. So, like Cloud DNS's rrsets:resolve and
// Eventarc's triggers:publishEvent before it, this ships as an explicitly
// documented **emulator-only extension**: a custom method,
// securityPolicies/{name}:evaluate, following the same whole-segment
// ":action" routing technique already used by networkmanagement's
// connectivityTests:rerun (net/http's ServeMux can't express a literal ":"
// inside a path pattern, so the segment is matched as a wildcard and split
// with strings.Cut).
//
// Evaluation is real, not a fixed answer: rules are walked in ascending
// priority order (lower number = higher precedence, matching the real
// API), and the first rule whose match condition matches the simulated
// source IP decides the outcome. A policy with no matching rule falls back
// to ALLOW, mirroring the implicit default rule (priority 2147483647,
// action "allow") every real Cloud Armor policy carries automatically.
// Only config.srcIpRanges matching is evaluated (CIDR containment, "*"
// meaning match-all) — CEL-expression rules (match.expr) are accepted and
// stored (see SecurityPolicyRuleMatch's doc comment in securitypolicy.go)
// but never match here, a documented narrowing, not an oversight.
package loadbalancing

import (
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/cesar/gcp-emulator/internal/server"
)

// cidrContains reports whether ip falls inside cidr. An entry without "/"
// (a bare IP) is treated as /32, same convention duplicated across this
// project (see networkmanagement/reachability.go's helper of the same
// name) rather than introducing a cross-package import.
func cidrContains(cidr, ip string) bool {
	if cidr == "*" {
		return true
	}
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

// ruleMatchesIP reports whether rule's match condition matches sourceIP.
// Only config.srcIpRanges is evaluated; a rule with no Match (or a
// CEL-only Match with no Config) never matches here — see the package doc
// comment above for why.
func ruleMatchesIP(rule SecurityPolicyRule, sourceIP string) bool {
	if rule.Match == nil || rule.Match.Config == nil {
		return false
	}
	for _, cidr := range rule.Match.Config.SrcIpRanges {
		if cidrContains(cidr, sourceIP) {
			return true
		}
	}
	return false
}

// EvaluatedRule is the matched-rule subset returned by evaluatePolicy,
// enough for a caller to see which rule decided the outcome.
type EvaluatedRule struct {
	Priority    int64  `json:"priority"`
	Action      string `json:"action"`
	Description string `json:"description,omitempty"`
}

// evaluatePolicy walks sp's rules in ascending priority order and returns
// the action of the first rule matching sourceIP, defaulting to "allow"
// (the real API's implicit default rule) when nothing matches.
func evaluatePolicy(sp SecurityPolicy, sourceIP string) (action string, matched *EvaluatedRule) {
	rules := make([]SecurityPolicyRule, len(sp.Rules))
	copy(rules, sp.Rules)
	sort.Slice(rules, func(i, j int) bool { return rules[i].Priority < rules[j].Priority })

	for _, rule := range rules {
		if ruleMatchesIP(rule, sourceIP) {
			return rule.Action, &EvaluatedRule{Priority: rule.Priority, Action: rule.Action, Description: rule.Description}
		}
	}
	return "allow", nil
}

// securityPolicyAction dispatches the single custom method registered on
// the wildcard "{securityPolicyAction}" segment: "{name}:evaluate". Any
// other suffix (or none) is INVALID_ARGUMENT, the same convention
// networkmanagement.testAction uses for its own single custom method.
func (s *Service) securityPolicyAction(w http.ResponseWriter, r *http.Request) {
	name, action, ok := strings.Cut(r.PathValue("securityPolicyAction"), ":")
	if !ok || action != "evaluate" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "unsupported securityPolicies custom method")
		return
	}

	var sp SecurityPolicy
	found, err := s.db.Get(bucketSecurityPolicies, name, &sp)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "security policy not found")
		return
	}

	var body struct {
		SourceIP string `json:"sourceIp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if net.ParseIP(body.SourceIP) == nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "sourceIp must be a valid IP address")
		return
	}

	resultAction, matchedRule := evaluatePolicy(sp, body.SourceIP)
	server.WriteJSON(w, 200, map[string]any{
		"sourceIp":    body.SourceIP,
		"action":      resultAction,
		"matchedRule": matchedRule,
	})
}
