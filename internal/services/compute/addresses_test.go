package compute

import (
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func TestRegionalAddressLifecycle(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/compute/v1/projects/proj1/regions/us-central1/addresses"

	var created Address
	status := testutil.DoJSON(t, "POST", base, map[string]any{"name": "my-ip"}, &created)
	if status != 200 || created.Name != "my-ip" || created.Status != "RESERVED" || created.Address == "" {
		t.Fatalf("create: status=%d addr=%+v", status, created)
	}
	if created.AddressType != "EXTERNAL" {
		t.Fatalf("default AddressType should be EXTERNAL, got %q", created.AddressType)
	}

	var list struct {
		Items []Address `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", base, nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list: status=%d items=%+v", status, list.Items)
	}

	var got Address
	status = testutil.DoJSON(t, "GET", base+"/my-ip", nil, &got)
	if status != 200 || got.Name != "my-ip" {
		t.Fatalf("get: status=%d addr=%+v", status, got)
	}

	status = testutil.DoJSON(t, "DELETE", base+"/my-ip", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", base+"/my-ip", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestGlobalAddressLifecycle(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/compute/v1/projects/proj1/global/addresses"

	var created Address
	status := testutil.DoJSON(t, "POST", base, map[string]any{"name": "global-ip"}, &created)
	if status != 200 || created.Name != "global-ip" || created.IPVersion != "IPV4" {
		t.Fatalf("create: status=%d addr=%+v", status, created)
	}

	var list struct {
		Items []Address `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", base, nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list: status=%d items=%+v", status, list.Items)
	}

	status = testutil.DoJSON(t, "DELETE", base+"/global-ip", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

func TestAddressDuplicateConflicts(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/compute/v1/projects/proj1/regions/us-central1/addresses"
	body := map[string]any{"name": "dup"}
	testutil.DoJSON(t, "POST", base, body, nil)
	status := testutil.DoJSON(t, "POST", base, body, nil)
	if status != 409 {
		t.Fatalf("duplicate regional address: want 409, got %d", status)
	}

	gbase := srv.URL + "/compute/v1/projects/proj1/global/addresses"
	testutil.DoJSON(t, "POST", gbase, body, nil)
	status = testutil.DoJSON(t, "POST", gbase, body, nil)
	if status != 409 {
		t.Fatalf("duplicate global address: want 409, got %d", status)
	}
}

func TestAddressCustomIPPreserved(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/compute/v1/projects/proj1/regions/europe-west1/addresses"
	var got Address
	testutil.DoJSON(t, "POST", base, map[string]any{"name": "custom-ip", "address": "10.0.0.5"}, &got)
	if got.Address != "10.0.0.5" {
		t.Fatalf("custom address not preserved: got %q", got.Address)
	}
}
