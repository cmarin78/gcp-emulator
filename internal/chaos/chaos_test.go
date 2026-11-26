package chaos

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func TestChaosRuleMatchingAndIntercept(t *testing.T) {
	m := New()

	// 1. Add rule for storage path with 503
	rule, err := m.AddRule(Rule{
		PathPrefix: "/storage/v1/b/faulty-bucket",
		HTTPStatus: 503,
	})
	if err != nil {
		t.Fatalf("add rule failed: %v", err)
	}
	if rule.ID == "" || rule.ErrorCode != "UNAVAILABLE" {
		t.Fatalf("unexpected defaults: %+v", rule)
	}

	// 2. Request to unaffected bucket
	reqOK, _ := http.NewRequest("GET", "/storage/v1/b/ok-bucket", nil)
	delay, shouldFail, status, _, _ := m.Intercept(reqOK)
	if shouldFail || delay > 0 || status != 0 {
		t.Fatalf("expected no chaos on unaffected bucket: fail=%v delay=%v status=%d", shouldFail, delay, status)
	}

	// 3. Request to faulty bucket
	reqFaulty, _ := http.NewRequest("GET", "/storage/v1/b/faulty-bucket/o", nil)
	delay, shouldFail, status, code, _ := m.Intercept(reqFaulty)
	if !shouldFail || status != 503 || code != "UNAVAILABLE" {
		t.Fatalf("expected 503 UNAVAILABLE on faulty bucket: fail=%v status=%d code=%s", shouldFail, status, code)
	}

	// 4. Admin and Health bypass
	reqAdmin, _ := http.NewRequest("GET", "/admin/chaos/rules", nil)
	_, shouldFail, _, _, _ = m.Intercept(reqAdmin)
	if shouldFail {
		t.Fatal("expected admin endpoints to be exempt from chaos")
	}

	reqHealth, _ := http.NewRequest("GET", "/healthz", nil)
	_, shouldFail, _, _, _ = m.Intercept(reqHealth)
	if shouldFail {
		t.Fatal("expected /healthz to be exempt from chaos")
	}

	// Check Stats
	stats := m.GetStats()
	if stats.InjectedFailures != 1 || stats.TotalRequestsEvaluated < 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	// Delete Rule
	if !m.DeleteRule(rule.ID) {
		t.Fatal("expected successful delete")
	}
	_, shouldFail, _, _, _ = m.Intercept(reqFaulty)
	if shouldFail {
		t.Fatal("expected no failure after rule deletion")
	}
}

func TestChaosArtificialLatency(t *testing.T) {
	m := New()
	_, _ = m.AddRule(Rule{
		Service: "pubsub",
		DelayMs: 20,
	})

	req, _ := http.NewRequest("POST", "/v1/projects/p1/topics/t1:publish", nil)
	delay, shouldFail, _, _, _ := m.Intercept(req)
	if shouldFail {
		t.Fatal("expected latency only, no failure")
	}
	if delay < 20*time.Millisecond {
		t.Fatalf("want at least 20ms delay, got %v", delay)
	}

	stats := m.GetStats()
	if stats.InjectedDelays != 1 || stats.TotalDelayMs < 20 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestChaosHTTPAdminEndpoints(t *testing.T) {
	m := New()
	mux := http.NewServeMux()
	m.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. Create Rule
	createBody := map[string]any{
		"pathPrefix": "/compute/v1/projects/my-proj",
		"httpStatus": 500,
		"delayMs":    10,
	}
	var created Rule
	status := testutil.DoJSON(t, "POST", srv.URL+"/admin/chaos/rules", createBody, &created)
	if status != 201 || created.ID == "" {
		t.Fatalf("create chaos rule: status=%d rule=%+v", status, created)
	}

	// 2. List Rules
	var listResp struct {
		Rules []Rule `json:"rules"`
		Count int    `json:"count"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/admin/chaos/rules", nil, &listResp)
	if status != 200 || listResp.Count != 1 {
		t.Fatalf("list chaos rules: status=%d resp=%+v", status, listResp)
	}

	// 3. Get Rule
	var fetched Rule
	status = testutil.DoJSON(t, "GET", srv.URL+"/admin/chaos/rules/"+created.ID, nil, &fetched)
	if status != 200 || fetched.ID != created.ID {
		t.Fatalf("get chaos rule: status=%d rule=%+v", status, fetched)
	}

	// 4. Get Stats
	var stats Stats
	status = testutil.DoJSON(t, "GET", srv.URL+"/admin/chaos/stats", nil, &stats)
	if status != 200 {
		t.Fatalf("get chaos stats: status=%d", status)
	}

	// 5. Delete Rule
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/admin/chaos/rules/"+created.ID, nil, nil)
	if status != 200 {
		t.Fatalf("delete chaos rule: status=%d", status)
	}

	// 6. Clear Rules
	testutil.DoJSON(t, "POST", srv.URL+"/admin/chaos/rules", createBody, nil)
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/admin/chaos/rules", nil, nil)
	if status != 200 {
		t.Fatalf("clear chaos rules: status=%d", status)
	}
}
