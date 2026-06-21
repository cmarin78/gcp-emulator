package loadbalancing

import (
	"net/http/httptest"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func createPolicyWithRules(t *testing.T, srv *httptest.Server, name string, rules []map[string]any) {
	t.Helper()
	var op Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies", map[string]any{
		"name":  name,
		"rules": rules,
	}, &op)
	if status != 200 {
		t.Fatalf("create policy %q: status=%d op=%+v", name, status, op)
	}
}

// TestSecurityPolicyEvaluateDefaultAllow covers the implicit default rule:
// a policy with no rule matching the simulated source IP falls back to
// "allow", same as the real API's automatic priority-2147483647 rule.
func TestSecurityPolicyEvaluateDefaultAllow(t *testing.T) {
	srv := newTestServer(t)
	createPolicyWithRules(t, srv, "policy-default", []map[string]any{
		{
			"priority": 1000,
			"action":   "deny(403)",
			"match": map[string]any{
				"versionedExpr": "SRC_IPS_V1",
				"config":        map[string]any{"srcIpRanges": []string{"10.0.0.0/8"}},
			},
		},
	})

	var result struct {
		Action      string         `json:"action"`
		MatchedRule *EvaluatedRule `json:"matchedRule"`
	}
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/policy-default:evaluate",
		map[string]any{"sourceIp": "8.8.8.8"}, &result)
	if status != 200 || result.Action != "allow" || result.MatchedRule != nil {
		t.Fatalf("evaluate: status=%d result=%+v", status, result)
	}
}

// TestSecurityPolicyEvaluateDenyMatch covers a matching deny rule by CIDR.
func TestSecurityPolicyEvaluateDenyMatch(t *testing.T) {
	srv := newTestServer(t)
	createPolicyWithRules(t, srv, "policy-deny", []map[string]any{
		{
			"priority": 1000,
			"action":   "deny(403)",
			"match": map[string]any{
				"versionedExpr": "SRC_IPS_V1",
				"config":        map[string]any{"srcIpRanges": []string{"203.0.113.0/24"}},
			},
		},
	})

	var result struct {
		Action      string         `json:"action"`
		MatchedRule *EvaluatedRule `json:"matchedRule"`
	}
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/policy-deny:evaluate",
		map[string]any{"sourceIp": "203.0.113.42"}, &result)
	if status != 200 || result.Action != "deny(403)" || result.MatchedRule == nil || result.MatchedRule.Priority != 1000 {
		t.Fatalf("evaluate: status=%d result=%+v", status, result)
	}
}

// TestSecurityPolicyEvaluatePriorityOrder covers two overlapping rules:
// the lower-priority-number rule must win regardless of declaration order.
func TestSecurityPolicyEvaluatePriorityOrder(t *testing.T) {
	srv := newTestServer(t)
	createPolicyWithRules(t, srv, "policy-priority", []map[string]any{
		{
			"priority": 2000,
			"action":   "deny(403)",
			"match": map[string]any{
				"config": map[string]any{"srcIpRanges": []string{"0.0.0.0/0"}},
			},
		},
		{
			"priority": 500,
			"action":   "allow",
			"match": map[string]any{
				"config": map[string]any{"srcIpRanges": []string{"1.2.3.4/32"}},
			},
		},
	})

	var result struct {
		Action      string         `json:"action"`
		MatchedRule *EvaluatedRule `json:"matchedRule"`
	}
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/policy-priority:evaluate",
		map[string]any{"sourceIp": "1.2.3.4"}, &result)
	if status != 200 || result.Action != "allow" || result.MatchedRule == nil || result.MatchedRule.Priority != 500 {
		t.Fatalf("evaluate: status=%d result=%+v", status, result)
	}
}

// TestSecurityPolicyEvaluateWildcard covers the "*" srcIpRanges catch-all,
// the shape real Cloud Armor uses for its own default-deny-everything rule.
func TestSecurityPolicyEvaluateWildcard(t *testing.T) {
	srv := newTestServer(t)
	createPolicyWithRules(t, srv, "policy-wildcard", []map[string]any{
		{
			"priority": 2147483647,
			"action":   "deny(404)",
			"match": map[string]any{
				"config": map[string]any{"srcIpRanges": []string{"*"}},
			},
		},
	})

	var result struct {
		Action      string         `json:"action"`
		MatchedRule *EvaluatedRule `json:"matchedRule"`
	}
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/policy-wildcard:evaluate",
		map[string]any{"sourceIp": "192.168.1.1"}, &result)
	if status != 200 || result.Action != "deny(404)" || result.MatchedRule == nil {
		t.Fatalf("evaluate: status=%d result=%+v", status, result)
	}
}

// TestSecurityPolicyEvaluateCELNotEvaluated covers the documented boundary:
// a CEL-only match (no config.srcIpRanges) never matches, falling back to
// the implicit allow default instead of erroring or panicking.
func TestSecurityPolicyEvaluateCELNotEvaluated(t *testing.T) {
	srv := newTestServer(t)
	createPolicyWithRules(t, srv, "policy-cel", []map[string]any{
		{
			"priority": 1000,
			"action":   "deny(403)",
			"match": map[string]any{
				"expr": map[string]any{"expression": "origin.region_code == \"CU\""},
			},
		},
	})

	var result struct {
		Action      string         `json:"action"`
		MatchedRule *EvaluatedRule `json:"matchedRule"`
	}
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/policy-cel:evaluate",
		map[string]any{"sourceIp": "1.1.1.1"}, &result)
	if status != 200 || result.Action != "allow" || result.MatchedRule != nil {
		t.Fatalf("evaluate: status=%d result=%+v", status, result)
	}
}

// TestSecurityPolicyEvaluateInvalidIP covers input validation: a malformed
// sourceIp is rejected instead of silently evaluating against garbage.
func TestSecurityPolicyEvaluateInvalidIP(t *testing.T) {
	srv := newTestServer(t)
	createPolicyWithRules(t, srv, "policy-badip", nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/policy-badip:evaluate",
		map[string]any{"sourceIp": "not-an-ip"}, nil)
	if status != 400 {
		t.Fatalf("evaluate with invalid sourceIp: want 400, got %d", status)
	}
}

// TestSecurityPolicyEvaluateNotFound covers evaluating a policy that
// doesn't exist.
func TestSecurityPolicyEvaluateNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/missing:evaluate",
		map[string]any{"sourceIp": "1.1.1.1"}, nil)
	if status != 404 {
		t.Fatalf("evaluate missing policy: want 404, got %d", status)
	}
}
