package orgpolicy

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

func TestPolicyLifecycle(t *testing.T) {
	srv := newTestServer(t)

	body := map[string]any{
		"name": "projects/proj1/policies/compute.disableSerialPortAccess",
		"spec": map[string]any{
			"rules": []map[string]any{{"enforce": true}},
		},
	}
	var created Policy
	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/policies", body, &created)
	if status != 200 || created.Name != "projects/proj1/policies/compute.disableSerialPortAccess" || created.Spec == nil {
		t.Fatalf("create: status=%d policy=%+v", status, created)
	}

	status = testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/policies", body, nil)
	if status != 409 {
		t.Fatalf("duplicate create: want 409, got %d", status)
	}

	var got Policy
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/policies/compute.disableSerialPortAccess", nil, &got)
	if status != 200 || got.Name != created.Name {
		t.Fatalf("get: status=%d policy=%+v", status, got)
	}

	var list struct {
		Policies []Policy `json:"policies"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/policies", nil, &list)
	if status != 200 || len(list.Policies) != 1 {
		t.Fatalf("list: status=%d policies=%+v", status, list.Policies)
	}

	var updated Policy
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v2/projects/proj1/policies/compute.disableSerialPortAccess",
		map[string]any{"spec": map[string]any{"rules": []map[string]any{{"denyAll": true}}}}, &updated)
	if status != 200 || len(updated.Spec.Rules) != 1 || !updated.Spec.Rules[0].DenyAll {
		t.Fatalf("update: status=%d policy=%+v", status, updated)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v2/projects/proj1/policies/compute.disableSerialPortAccess", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}

	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/policies/compute.disableSerialPortAccess", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestCreatePolicyRequiresName(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/policies", map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without name: want 400, got %d", status)
	}
}

func TestUpdatePolicyNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "PATCH", srv.URL+"/v2/projects/proj1/policies/missing", map[string]any{}, nil)
	if status != 404 {
		t.Fatalf("update missing policy: want 404, got %d", status)
	}
}
