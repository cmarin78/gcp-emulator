package iap

import (
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// newTestServer is defined in iap_test.go (same package).

func TestBrandRequiresSupportEmailAndTitle(t *testing.T) {
	srv := newTestServer(t)

	// Missing both fields.
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/brands",
		map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("brand no email/title: want 400, got %d", status)
	}

	// Missing applicationTitle.
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/brands",
		map[string]any{"supportEmail": "a@b.com"}, nil)
	if status != 400 {
		t.Fatalf("brand no title: want 400, got %d", status)
	}

	// Missing supportEmail.
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/brands",
		map[string]any{"applicationTitle": "App"}, nil)
	if status != 400 {
		t.Fatalf("brand no email: want 400, got %d", status)
	}
}

func TestGetBrandNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/brands/99999", nil, nil)
	if status != 404 {
		t.Fatalf("get nonexistent brand: want 404, got %d", status)
	}
}

func TestGetClientNotFound(t *testing.T) {
	srv := newTestServer(t)

	// Create brand first.
	var brand Brand
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/brands",
		map[string]any{"supportEmail": "a@b.com", "applicationTitle": "App"}, &brand)

	status := testutil.DoJSON(t, "GET",
		srv.URL+brandPathFor(brand.Name)+"/identityAwareProxyClients/nonexistent.apps.googleusercontent.com",
		nil, nil)
	if status != 404 {
		t.Fatalf("get nonexistent client: want 404, got %d", status)
	}
}

func TestClientDeleteNotFound(t *testing.T) {
	srv := newTestServer(t)

	var brand Brand
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/brands",
		map[string]any{"supportEmail": "a@b.com", "applicationTitle": "App"}, &brand)

	// Create and delete a client, then try to get/delete again.
	var client Client
	testutil.DoJSON(t, "POST", srv.URL+brandPathFor(brand.Name)+"/identityAwareProxyClients",
		map[string]any{"displayName": "tmp"}, &client)
	testutil.DoJSON(t, "DELETE", srv.URL+"/v1/"+client.Name, nil, nil)

	// After delete, the client should be gone from list.
	var list struct {
		IdentityAwareProxyClients []Client `json:"identityAwareProxyClients"`
	}
	testutil.DoJSON(t, "GET", srv.URL+brandPathFor(brand.Name)+"/identityAwareProxyClients", nil, &list)
	if len(list.IdentityAwareProxyClients) != 0 {
		t.Fatalf("list after delete: expected 0 clients, got %d", len(list.IdentityAwareProxyClients))
	}
}

func TestMultipleClientsPerBrand(t *testing.T) {
	srv := newTestServer(t)

	var brand Brand
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/brands",
		map[string]any{"supportEmail": "a@b.com", "applicationTitle": "App"}, &brand)

	// Create 3 clients.
	for _, name := range []string{"client-a", "client-b", "client-c"} {
		var c Client
		status := testutil.DoJSON(t, "POST",
			srv.URL+brandPathFor(brand.Name)+"/identityAwareProxyClients",
			map[string]any{"displayName": name}, &c)
		if status != 200 || c.Secret == "" {
			t.Fatalf("create client %s: status=%d client=%+v", name, status, c)
		}
	}

	var list struct {
		IdentityAwareProxyClients []Client `json:"identityAwareProxyClients"`
	}
	testutil.DoJSON(t, "GET", srv.URL+brandPathFor(brand.Name)+"/identityAwareProxyClients", nil, &list)
	if len(list.IdentityAwareProxyClients) != 3 {
		t.Fatalf("list clients: want 3, got %d", len(list.IdentityAwareProxyClients))
	}

	// Verify each client has a unique secret.
	seen := map[string]bool{}
	for _, c := range list.IdentityAwareProxyClients {
		if seen[c.Secret] {
			t.Fatalf("duplicate client secret: %q", c.Secret)
		}
		seen[c.Secret] = true
	}
}
