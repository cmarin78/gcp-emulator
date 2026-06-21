package clouddns

import (
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// TestResolveDirectMatch covers the simple case: an rrset exists for
// exactly (name, type).
func TestResolveDirectMatch(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones", map[string]string{
		"name":    "z1",
		"dnsName": "example.com.",
	}, nil)
	testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones/z1/changes", map[string]any{
		"additions": []ResourceRecordSet{
			{Name: "www.example.com.", Type: "A", TTL: 300, Rrdatas: []string{"1.2.3.4"}},
		},
	}, nil)

	var result ResolveResult
	status := testutil.DoJSON(t, "GET",
		srv.URL+"/dns/v1/projects/proj1/managedZones/z1/rrsets:resolve?name=www.example.com.&type=A",
		nil, &result)
	if status != 200 {
		t.Fatalf("resolve: want 200, got %d", status)
	}
	if result.Rcode != "NOERROR" || result.Answer == nil || result.Answer.Rrdatas[0] != "1.2.3.4" {
		t.Fatalf("resolve direct match: got %+v", result)
	}
	if len(result.Chain) != 0 {
		t.Fatalf("direct match should have no CNAME chain, got %+v", result.Chain)
	}
}

// TestResolveFollowsCNAMEChain covers the case the real Cloud DNS API has
// no endpoint for at all: alias.example.com CNAME -> target.example.com,
// target.example.com A 5.6.7.8 -- resolving alias.example.com/A should
// follow the CNAME and return target's A record, with the CNAME hop
// recorded in the chain.
func TestResolveFollowsCNAMEChain(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones", map[string]string{
		"name":    "z1",
		"dnsName": "example.com.",
	}, nil)
	testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones/z1/changes", map[string]any{
		"additions": []ResourceRecordSet{
			{Name: "alias.example.com.", Type: "CNAME", TTL: 300, Rrdatas: []string{"target.example.com."}},
			{Name: "target.example.com.", Type: "A", TTL: 300, Rrdatas: []string{"5.6.7.8"}},
		},
	}, nil)

	var result ResolveResult
	status := testutil.DoJSON(t, "GET",
		srv.URL+"/dns/v1/projects/proj1/managedZones/z1/rrsets:resolve?name=alias.example.com.&type=A",
		nil, &result)
	if status != 200 {
		t.Fatalf("resolve: want 200, got %d", status)
	}
	if result.Rcode != "NOERROR" || result.Answer == nil || result.Answer.Rrdatas[0] != "5.6.7.8" {
		t.Fatalf("resolve via CNAME: got %+v", result)
	}
	if len(result.Chain) != 1 || result.Chain[0].Type != "CNAME" {
		t.Fatalf("expected one CNAME hop in chain, got %+v", result.Chain)
	}
}

// TestResolveNXDOMAIN covers a name with no matching rrset and no CNAME to
// follow.
func TestResolveNXDOMAIN(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones", map[string]string{
		"name":    "z1",
		"dnsName": "example.com.",
	}, nil)

	var result ResolveResult
	status := testutil.DoJSON(t, "GET",
		srv.URL+"/dns/v1/projects/proj1/managedZones/z1/rrsets:resolve?name=nope.example.com.&type=A",
		nil, &result)
	if status != 200 {
		t.Fatalf("resolve: want 200, got %d", status)
	}
	if result.Rcode != "NXDOMAIN" {
		t.Fatalf("expected NXDOMAIN, got %+v", result)
	}
}

// TestResolveCNAMEExternalStopsAtZoneBoundary covers a CNAME pointing
// outside the zone's dnsName: the emulator doesn't resolve across zones,
// so it must stop and report CNAME_EXTERNAL instead of silently returning
// NXDOMAIN.
func TestResolveCNAMEExternalStopsAtZoneBoundary(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones", map[string]string{
		"name":    "z1",
		"dnsName": "example.com.",
	}, nil)
	testutil.DoJSON(t, "POST", srv.URL+"/dns/v1/projects/proj1/managedZones/z1/changes", map[string]any{
		"additions": []ResourceRecordSet{
			{Name: "alias.example.com.", Type: "CNAME", TTL: 300, Rrdatas: []string{"other.org."}},
		},
	}, nil)

	var result ResolveResult
	status := testutil.DoJSON(t, "GET",
		srv.URL+"/dns/v1/projects/proj1/managedZones/z1/rrsets:resolve?name=alias.example.com.&type=A",
		nil, &result)
	if status != 200 {
		t.Fatalf("resolve: want 200, got %d", status)
	}
	if result.Rcode != "CNAME_EXTERNAL" {
		t.Fatalf("expected CNAME_EXTERNAL, got %+v", result)
	}
}
