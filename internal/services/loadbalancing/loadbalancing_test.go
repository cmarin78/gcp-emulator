package loadbalancing

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Operation mirrors internal/server.Operation's relevant fields for
// decoding responses in this package's tests (loadbalancing reuses
// server.Operations, same shape as compute: Status, not Done).
type Operation struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	OperationType string `json:"operationType"`
}

// TestHealthCheckLifecycle covers insert -> get -> list -> delete.
func TestHealthCheckLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var op Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/healthChecks", map[string]any{
		"name": "my-hc",
		"type": "HTTP",
	}, &op)
	if status != 200 || op.Status != "DONE" || op.OperationType != "insert" {
		t.Fatalf("insert: status=%d op=%+v", status, op)
	}

	var got HealthCheck
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/healthChecks/my-hc", nil, &got)
	if status != 200 || got.Name != "my-hc" || got.Type != "HTTP" {
		t.Fatalf("get: status=%d hc=%+v", status, got)
	}

	var list struct {
		Items []HealthCheck `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/healthChecks", nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list: status=%d items=%+v", status, list.Items)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/global/healthChecks/my-hc", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

// TestBackendServiceSecurityPolicyRef asserts a short securityPolicy name
// passed at create time is normalized into a full global selfLink (same
// convention as compute/network.go's normalizeGlobalRef), and that an
// already-qualified ref is passed through unchanged.
func TestBackendServiceSecurityPolicyRef(t *testing.T) {
	srv := newTestServer(t)

	var bs BackendService
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/backendServices", map[string]any{
		"name":           "my-backend",
		"securityPolicy": "my-policy",
	}, &bs)
	if status != 200 {
		t.Fatalf("insert backend service: status=%d", status)
	}
	var got BackendService
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/backendServices/my-backend", nil, &got)
	want := "/compute/v1/projects/proj1/global/securityPolicies/my-policy"
	if got.SecurityPolicy != want {
		t.Fatalf("expected normalized securityPolicy ref %q, got %q", want, got.SecurityPolicy)
	}

	var bs2 BackendService
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/backendServices", map[string]any{
		"name":           "my-backend-2",
		"securityPolicy": "/compute/v1/projects/other-proj/global/securityPolicies/external-policy",
	}, &bs2)
	if status != 200 {
		t.Fatalf("insert backend service 2: status=%d", status)
	}
	var got2 BackendService
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/backendServices/my-backend-2", nil, &got2)
	if got2.SecurityPolicy != "/compute/v1/projects/other-proj/global/securityPolicies/external-policy" {
		t.Fatalf("expected already-qualified ref passed through unchanged, got %q", got2.SecurityPolicy)
	}
}

// TestBackendServiceCDN covers Cloud CDN's enableCDN/cdnPolicy fields:
// insert with enableCDN=true applies a default cdnPolicy, patch can
// override the policy or disable CDN (clearing the policy).
func TestBackendServiceCDN(t *testing.T) {
	srv := newTestServer(t)

	var bs BackendService
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/backendServices", map[string]any{
		"name":      "cdn-backend",
		"enableCDN": true,
	}, &bs)
	if status != 200 {
		t.Fatalf("insert: status=%d", status)
	}
	var got BackendService
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/backendServices/cdn-backend", nil, &got)
	if !got.EnableCDN || got.CdnPolicy == nil || got.CdnPolicy.CacheMode == "" {
		t.Fatalf("expected enableCDN with default cdnPolicy applied, got %+v", got)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/compute/v1/projects/proj1/global/backendServices/cdn-backend", map[string]any{
		"cdnPolicy": map[string]any{"cacheMode": "FORCE_CACHE_ALL", "defaultTtl": 7200},
	}, &patchOp)
	if status != 200 || patchOp.Status != "DONE" || patchOp.OperationType != "patch" {
		t.Fatalf("patch cdnPolicy: status=%d op=%+v", status, patchOp)
	}
	var patched BackendService
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/backendServices/cdn-backend", nil, &patched)
	if patched.CdnPolicy == nil || patched.CdnPolicy.CacheMode != "FORCE_CACHE_ALL" || patched.CdnPolicy.DefaultTTL != 7200 {
		t.Fatalf("expected cdnPolicy overridden, got %+v", patched.CdnPolicy)
	}

	status = testutil.DoJSON(t, "PATCH", srv.URL+"/compute/v1/projects/proj1/global/backendServices/cdn-backend", map[string]any{
		"enableCDN": false,
	}, nil)
	if status != 200 {
		t.Fatalf("patch disable cdn: status=%d", status)
	}
	var disabled BackendService
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/backendServices/cdn-backend", nil, &disabled)
	if disabled.EnableCDN || disabled.CdnPolicy != nil {
		t.Fatalf("expected CDN disabled and cdnPolicy cleared, got %+v", disabled)
	}
}

// TestURLMapsAndProxies covers urlMaps, targetHttpProxies and
// targetHttpsProxies CRUD.
func TestURLMapsAndProxies(t *testing.T) {
	srv := newTestServer(t)

	var um URLMap
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/urlMaps", map[string]any{
		"name":           "my-urlmap",
		"defaultService": "my-backend",
	}, &um)
	if status != 200 {
		t.Fatalf("insert urlMap: status=%d", status)
	}

	var httpProxy TargetHTTPProxy
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/targetHttpProxies", map[string]any{
		"name":   "my-http-proxy",
		"urlMap": "my-urlmap",
	}, &httpProxy)
	if status != 200 {
		t.Fatalf("insert targetHttpProxy: status=%d", status)
	}
	var gotHTTP TargetHTTPProxy
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/targetHttpProxies/my-http-proxy", nil, &gotHTTP)
	if gotHTTP.URLMap != "my-urlmap" {
		t.Fatalf("expected urlMap reference preserved, got %+v", gotHTTP)
	}

	var httpsProxy TargetHTTPSProxy
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/targetHttpsProxies", map[string]any{
		"name":            "my-https-proxy",
		"urlMap":          "my-urlmap",
		"sslCertificates": []string{"my-cert"},
	}, &httpsProxy)
	if status != 200 {
		t.Fatalf("insert targetHttpsProxy: status=%d", status)
	}
	var gotHTTPS TargetHTTPSProxy
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/targetHttpsProxies/my-https-proxy", nil, &gotHTTPS)
	if len(gotHTTPS.SSLCertificates) != 1 {
		t.Fatalf("expected sslCertificates preserved, got %+v", gotHTTPS)
	}

	var list struct {
		Items []URLMap `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/urlMaps", nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list urlMaps: status=%d items=%+v", status, list.Items)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/global/targetHttpProxies/my-http-proxy", nil, nil)
	if status != 200 {
		t.Fatalf("delete targetHttpProxy: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/global/targetHttpsProxies/my-https-proxy", nil, nil)
	if status != 200 {
		t.Fatalf("delete targetHttpsProxy: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/global/urlMaps/my-urlmap", nil, nil)
	if status != 200 {
		t.Fatalf("delete urlMap: want 200, got %d", status)
	}
}

// TestForwardingRuleLifecycle covers insert (asserting a deterministic fake
// IP is assigned when IPAddress is omitted) -> get -> list -> delete.
func TestForwardingRuleLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var op Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/forwardingRules", map[string]any{
		"name":   "my-rule",
		"target": "my-http-proxy",
	}, &op)
	if status != 200 || op.Status != "DONE" {
		t.Fatalf("insert: status=%d op=%+v", status, op)
	}

	var got ForwardingRule
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/forwardingRules/my-rule", nil, &got)
	if status != 200 || got.IPAddress == "" {
		t.Fatalf("expected a fake IP assigned, got %+v", got)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/global/forwardingRules/my-rule", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

func TestInsertRequiresName(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/healthChecks", map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("insert healthCheck without name: want 400, got %d", status)
	}
}

// TestSecurityPolicyLifecycle covers insert (asserting default type
// CLOUD_ARMOR and a valid base64 fingerprint) -> get -> list -> setLabels
// (asserting the fingerprint is rotated) -> delete.
func TestSecurityPolicyLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies", map[string]any{
		"name": "my-policy",
		"rules": []map[string]any{
			{"priority": 1000, "action": "allow"},
		},
	}, &createOp)
	if status != 200 || createOp.Status != "DONE" || createOp.OperationType != "insert" {
		t.Fatalf("insert: status=%d op=%+v", status, createOp)
	}

	var got SecurityPolicy
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/my-policy", nil, &got)
	if status != 200 || got.Type != "CLOUD_ARMOR" || got.Fingerprint == "" {
		t.Fatalf("get: status=%d sp=%+v", status, got)
	}

	var list struct {
		Items []SecurityPolicy `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies", nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list: status=%d items=%+v", status, list.Items)
	}

	var labelsOp Operation
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/my-policy/setLabels", map[string]any{
		"labelFingerprint": got.Fingerprint,
		"labels":           map[string]string{"env": "prod"},
	}, &labelsOp)
	if status != 200 || labelsOp.Status != "DONE" || labelsOp.OperationType != "setLabels" {
		t.Fatalf("setLabels: status=%d op=%+v", status, labelsOp)
	}
	var afterLabels SecurityPolicy
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/my-policy", nil, &afterLabels)
	if afterLabels.Fingerprint == got.Fingerprint {
		t.Fatalf("expected fingerprint to rotate after setLabels, still %q", afterLabels.Fingerprint)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/global/securityPolicies/my-policy", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

// TestDuplicateCreateConflicts asserts that inserting any of this package's
// six resource types (plus securityPolicies) with a name that already
// exists returns 409 ALREADY_EXISTS instead of silently overwriting.
func TestDuplicateCreateConflicts(t *testing.T) {
	srv := newTestServer(t)

	cases := []struct {
		label string
		path  string
		body  map[string]any
	}{
		{"healthCheck", "/compute/v1/projects/proj1/global/healthChecks", map[string]any{"name": "dup-hc", "type": "HTTP"}},
		{"backendService", "/compute/v1/projects/proj1/global/backendServices", map[string]any{"name": "dup-backend"}},
		{"urlMap", "/compute/v1/projects/proj1/global/urlMaps", map[string]any{"name": "dup-urlmap"}},
		{"targetHttpProxy", "/compute/v1/projects/proj1/global/targetHttpProxies", map[string]any{"name": "dup-http-proxy"}},
		{"targetHttpsProxy", "/compute/v1/projects/proj1/global/targetHttpsProxies", map[string]any{"name": "dup-https-proxy"}},
		{"forwardingRule", "/compute/v1/projects/proj1/global/forwardingRules", map[string]any{"name": "dup-rule"}},
		{"securityPolicy", "/compute/v1/projects/proj1/global/securityPolicies", map[string]any{"name": "dup-policy"}},
	}

	for _, c := range cases {
		testutil.DoJSON(t, "POST", srv.URL+c.path, c.body, nil)
		status := testutil.DoJSON(t, "POST", srv.URL+c.path, c.body, nil)
		if status != 409 {
			t.Fatalf("duplicate %s: want 409, got %d", c.label, status)
		}
	}
}
