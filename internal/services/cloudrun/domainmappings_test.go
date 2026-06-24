package cloudrun

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func newDomainMappingTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db, nil).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func validDomainMappingBody() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "www.example.com"},
		"spec":     map[string]any{"routeName": "checkout"},
	}
}

func TestDomainMappingLifecycle(t *testing.T) {
	srv := newDomainMappingTestServer(t)
	base := srv.URL + "/apis/domains.cloudrun.com/v1/namespaces/proj1/domainmappings"

	var created DomainMapping
	status := testutil.DoJSON(t, "POST", base, validDomainMappingBody(), &created)
	if status != 200 || created.Metadata.Name != "www.example.com" || created.Status == nil {
		t.Fatalf("create: status=%d dm=%+v", status, created)
	}
	if created.Status.MappedRouteName != "checkout" || len(created.Status.Conditions) == 0 || created.Status.Conditions[0].Status != "True" {
		t.Fatalf("create: expected an already-ready status, got %+v", created.Status)
	}

	var list struct {
		Items []DomainMapping `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", base, nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list: status=%d items=%+v", status, list.Items)
	}

	itemPath := base + "/www.example.com"
	var got DomainMapping
	status = testutil.DoJSON(t, "GET", itemPath, nil, &got)
	if status != 200 || got.Metadata.Name != "www.example.com" {
		t.Fatalf("get: status=%d dm=%+v", status, got)
	}

	status = testutil.DoJSON(t, "DELETE", itemPath, nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", itemPath, nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestCreateDomainMappingRequiresRouteName(t *testing.T) {
	srv := newDomainMappingTestServer(t)
	base := srv.URL + "/apis/domains.cloudrun.com/v1/namespaces/proj1/domainmappings"
	status := testutil.DoJSON(t, "POST", base, map[string]any{
		"metadata": map[string]any{"name": "www.example.com"},
	}, nil)
	if status != 400 {
		t.Fatalf("create without spec.routeName: want 400, got %d", status)
	}
}

// TestCreateDomainMappingDuplicateConflicts follows this codebase's
// duplicate-create-conflict convention (see CONTRIBUTING.md): creating a
// domain mapping under an already-existing domain must 409, the same as
// every other create endpoint in this project.
func TestCreateDomainMappingDuplicateConflicts(t *testing.T) {
	srv := newDomainMappingTestServer(t)
	base := srv.URL + "/apis/domains.cloudrun.com/v1/namespaces/proj1/domainmappings"
	status := testutil.DoJSON(t, "POST", base, validDomainMappingBody(), nil)
	if status != 200 {
		t.Fatalf("first create: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "POST", base, validDomainMappingBody(), nil)
	if status != 409 {
		t.Fatalf("duplicate create: want 409, got %d", status)
	}
}
