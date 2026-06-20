package iap

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

func TestBrandAndClientLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var brand Brand
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/brands", map[string]any{
		"supportEmail":     "support@example.com",
		"applicationTitle": "My App",
	}, &brand)
	if status != 200 || brand.Name == "" {
		t.Fatalf("create brand: status=%d brand=%+v", status, brand)
	}

	var list struct {
		Brands []Brand `json:"brands"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/brands", nil, &list)
	if status != 200 || len(list.Brands) != 1 {
		t.Fatalf("list brands: status=%d brands=%+v", status, list.Brands)
	}

	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/brands", map[string]any{
		"supportEmail":     "support@example.com",
		"applicationTitle": "My App",
	}, nil)
	if status != 409 {
		t.Fatalf("duplicate brand: want 409, got %d", status)
	}

	var client Client
	status = testutil.DoJSON(t, "POST", srv.URL+brandPathFor(brand.Name)+"/identityAwareProxyClients",
		map[string]any{"displayName": "my-client"}, &client)
	if status != 200 || client.Secret == "" || client.Name == "" {
		t.Fatalf("create client: status=%d client=%+v", status, client)
	}

	var clientList struct {
		IdentityAwareProxyClients []Client `json:"identityAwareProxyClients"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+brandPathFor(brand.Name)+"/identityAwareProxyClients", nil, &clientList)
	if status != 200 || len(clientList.IdentityAwareProxyClients) != 1 {
		t.Fatalf("list clients: status=%d clients=%+v", status, clientList.IdentityAwareProxyClients)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/"+client.Name, nil, nil)
	if status != 200 {
		t.Fatalf("delete client: want 200, got %d", status)
	}
}

// brandPathFor turns a brand's resource name ("projects/p/brands/1") into
// its REST path ("/v1/projects/p/brands/1").
func brandPathFor(name string) string {
	return "/v1/" + name
}
