package clouddns

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

// TestZoneLifecycle covers create -> get -> list -> delete.
func TestZoneLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var zone ManagedZone
	status := testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones", map[string]string{
		"name":    "my-zone",
		"dnsName": "example.com.",
	}, &zone)
	if status != 200 || zone.Name != "my-zone" || len(zone.NameServers) != 2 {
		t.Fatalf("create zone: status=%d zone=%+v", status, zone)
	}

	var got ManagedZone
	status = testutil.DoJSON(t, "GET", srv.URL+"/dns/v1/projects/proj1/managedZones/my-zone", nil, &got)
	if status != 200 || got.Name != "my-zone" {
		t.Fatalf("get zone: status=%d zone=%+v", status, got)
	}

	var list struct {
		ManagedZones []ManagedZone `json:"managedZones"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/dns/v1/projects/proj1/managedZones", nil, &list)
	if status != 200 || len(list.ManagedZones) != 1 {
		t.Fatalf("list zones: status=%d zones=%+v", status, list.ManagedZones)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/dns/v1/projects/proj1/managedZones/my-zone", nil, nil)
	if status != 200 {
		t.Fatalf("delete zone: want 200, got %d", status)
	}
}

// TestDuplicateCreateConflict asserts that creating a managed zone whose
// name already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	body := map[string]string{"name": "dup-zone", "dnsName": "example.com."}
	testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones", body, nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones", body, nil)
	if status != 409 {
		t.Fatalf("duplicate zone: want 409, got %d", status)
	}
}

// TestChangeAppliesAdditionsAndDeletions covers the Change-based rrset
// mutation flow used by the `google_dns_record_set` Terraform resource:
// additions are applied, and a later change deleting + re-adding the same
// (name, type) acts as an update.
func TestChangeAppliesAdditionsAndDeletions(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones", map[string]string{
		"name":    "z1",
		"dnsName": "example.com.",
	}, nil)

	var change Change
	status := testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones/z1/changes", map[string]any{
		"additions": []ResourceRecordSet{
			{Name: "www.example.com.", Type: "A", TTL: 300, Rrdatas: []string{"1.2.3.4"}},
		},
	}, &change)
	if status != 200 || change.Status != "done" || len(change.Additions) != 1 {
		t.Fatalf("create change: status=%d change=%+v", status, change)
	}

	var list struct {
		Rrsets []ResourceRecordSet `json:"rrsets"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/dns/v1/projects/proj1/managedZones/z1/rrsets", nil, &list)
	if status != 200 || len(list.Rrsets) != 1 || list.Rrsets[0].Rrdatas[0] != "1.2.3.4" {
		t.Fatalf("list rrsets: status=%d rrsets=%+v", status, list.Rrsets)
	}

	// Update: delete the old rrset and add a replacement in one Change.
	status = testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones/z1/changes", map[string]any{
		"deletions": []ResourceRecordSet{{Name: "www.example.com.", Type: "A"}},
		"additions": []ResourceRecordSet{{Name: "www.example.com.", Type: "A", TTL: 300, Rrdatas: []string{"5.6.7.8"}}},
	}, nil)
	if status != 200 {
		t.Fatalf("update change: want 200, got %d", status)
	}

	var list2 struct {
		Rrsets []ResourceRecordSet `json:"rrsets"`
	}
	testutil.DoJSON(t, "GET", srv.URL+"/dns/v1/projects/proj1/managedZones/z1/rrsets", nil, &list2)
	if len(list2.Rrsets) != 1 || list2.Rrsets[0].Rrdatas[0] != "5.6.7.8" {
		t.Fatalf("rrsets after update: %+v", list2.Rrsets)
	}
}
