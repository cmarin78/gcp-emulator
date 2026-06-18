package kms

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

// TestKeyRingLifecycle covers create -> get -> list. KeyRings have no
// delete in the real API (KMS never deletes keyrings/keys), so this
// emulator deliberately registers no DELETE route -- nothing to test there.
func TestKeyRingLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var kr KeyRing
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings?keyRingId=my-ring", nil, &kr)
	if status != 200 || kr.Name == "" {
		t.Fatalf("create: status=%d kr=%+v", status, kr)
	}

	var got KeyRing
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings/my-ring", nil, &got)
	if status != 200 || got.Name != kr.Name {
		t.Fatalf("get: status=%d kr=%+v", status, got)
	}

	var list struct {
		KeyRings []KeyRing `json:"keyRings"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings", nil, &list)
	if status != 200 || len(list.KeyRings) != 1 {
		t.Fatalf("list: status=%d keyRings=%+v", status, list.KeyRings)
	}

	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings?keyRingId=my-ring", nil, nil)
	if status != 409 {
		t.Fatalf("duplicate create: want 409, got %d", status)
	}
}

// TestCryptoKeyLifecycle covers create -> get -> list -> patch, asserting
// the synthesized "primary" version 1 is always ENABLED on creation, and
// that the cryptoKeyVersions:destroy verb-dispatch route (a path segment
// captured whole and split on ":") works correctly.
func TestCryptoKeyLifecycle(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings?keyRingId=my-ring", nil, nil)

	var ck CryptoKey
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings/my-ring/cryptoKeys?cryptoKeyId=my-key",
		map[string]string{"purpose": "ENCRYPT_DECRYPT"}, &ck)
	if status != 200 || ck.Primary == nil || ck.Primary.State != "ENABLED" {
		t.Fatalf("create: status=%d ck=%+v", status, ck)
	}

	var got CryptoKey
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings/my-ring/cryptoKeys/my-key", nil, &got)
	if status != 200 || got.Name != ck.Name {
		t.Fatalf("get: status=%d ck=%+v", status, got)
	}

	var list struct {
		CryptoKeys []CryptoKey `json:"cryptoKeys"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings/my-ring/cryptoKeys", nil, &list)
	if status != 200 || len(list.CryptoKeys) != 1 {
		t.Fatalf("list: status=%d cks=%+v", status, list.CryptoKeys)
	}

	var patched CryptoKey
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings/my-ring/cryptoKeys/my-key",
		map[string]any{"labels": map[string]string{"env": "prod"}}, &patched)
	if status != 200 || patched.Labels["env"] != "prod" {
		t.Fatalf("patch: status=%d ck=%+v", status, patched)
	}

	var destroyed CryptoKeyVersion
	status = testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings/my-ring/cryptoKeys/my-key/cryptoKeyVersions/1:destroy",
		nil, &destroyed)
	if status != 200 || destroyed.State != "DESTROY_SCHEDULED" {
		t.Fatalf("destroy version: status=%d v=%+v", status, destroyed)
	}

	var version CryptoKeyVersion
	status = testutil.DoJSON(t, "GET",
		srv.URL+"/v1/projects/proj1/locations/us-central1/keyRings/my-ring/cryptoKeys/my-key/cryptoKeyVersions/1",
		nil, &version)
	if status != 200 || version.State != "DESTROY_SCHEDULED" {
		t.Fatalf("get version: status=%d v=%+v", status, version)
	}
}
