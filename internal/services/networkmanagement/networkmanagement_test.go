package networkmanagement

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

// seedNetwork/seedFirewall write directly into the storage buckets
// internal/services/compute uses, instead of importing that package (see
// the comment on bucketComputeNetworks/bucketComputeFirewalls in
// reachability.go) -- the same technique billingbudgets_test.go uses to
// seed a compute instance.
func seedNetwork(t *testing.T, srv *httptest.Server, db dbPutter, name string, peerings []computePeering) {
	t.Helper()
	if err := db.Put(bucketComputeNetworks, name, computeNetwork{Name: name, Peerings: peerings}); err != nil {
		t.Fatalf("seed network %s: %v", name, err)
	}
}

func seedFirewall(t *testing.T, db dbPutter, name, network, direction string, priority int64, sourceRanges []string, allowed, denied []computeFirewallRule) {
	t.Helper()
	fw := computeFirewall{
		Name: name, Network: network, Direction: direction, Priority: priority,
		SourceRanges: sourceRanges, Allowed: allowed, Denied: denied,
	}
	if err := db.Put(bucketComputeFirewalls, name, fw); err != nil {
		t.Fatalf("seed firewall %s: %v", name, err)
	}
}

// dbPutter is the minimal subset of *storage.DB used by the seed helpers
// above, declared so this file doesn't need its own storage import beyond
// what newTestServer/testutil already bring in transitively. We use
// *storage.DB's concrete Put method via testutil.NewDB(t) directly in
// tests, so this interface only exists to keep the helper signatures
// readable.
type dbPutter interface {
	Put(bucket, key string, value any) error
}

func TestReachableWithinSameNetworkAndAllowFirewall(t *testing.T) {
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	seedNetwork(t, srv, db, "default", nil)
	seedFirewall(t, db, "allow-http", "default", "INGRESS", 1000, []string{"0.0.0.0/0"},
		[]computeFirewallRule{{IPProtocol: "tcp", Ports: []string{"80"}}}, nil)

	var ct ConnectivityTest
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/connectivityTests", map[string]any{
		"name":     "ct-1",
		"protocol": "TCP",
		"source":   map[string]any{"ipAddress": "203.0.113.5", "network": "default"},
		"destination": map[string]any{
			"ipAddress": "10.0.0.5", "network": "default", "port": 80,
		},
	}, &ct)
	if status != 200 {
		t.Fatalf("create: want 200, got %d", status)
	}
	if ct.ReachabilityDetails == nil || ct.ReachabilityDetails.Result != "REACHABLE" {
		t.Fatalf("want REACHABLE, got %+v", ct.ReachabilityDetails)
	}
}

func TestUnreachableWithNoMatchingIngressRule(t *testing.T) {
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	seedNetwork(t, srv, db, "default", nil)
	// No firewall at all: real GCP denies ingress by default.

	var ct ConnectivityTest
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/connectivityTests", map[string]any{
		"name":        "ct-2",
		"protocol":    "TCP",
		"source":      map[string]any{"ipAddress": "203.0.113.5", "network": "default"},
		"destination": map[string]any{"ipAddress": "10.0.0.5", "network": "default", "port": 22},
	}, &ct)
	if status != 200 {
		t.Fatalf("create: want 200, got %d", status)
	}
	if ct.ReachabilityDetails == nil || ct.ReachabilityDetails.Result != "UNREACHABLE" {
		t.Fatalf("want UNREACHABLE (implied ingress deny), got %+v", ct.ReachabilityDetails)
	}
}

func TestDenyRuleAtLowerPriorityWinsOverAllow(t *testing.T) {
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	seedNetwork(t, srv, db, "default", nil)
	// priority 100 (evaluated first) denies the 10.0.0.0/8 source; priority
	// 1000 allows everyone else. A source inside 10.0.0.0/8 must be denied
	// even though a later, less specific allow rule also matches.
	seedFirewall(t, db, "deny-internal", "default", "INGRESS", 100, []string{"10.0.0.0/8"},
		nil, []computeFirewallRule{{IPProtocol: "tcp", Ports: []string{"80"}}})
	seedFirewall(t, db, "allow-all", "default", "INGRESS", 1000, []string{"0.0.0.0/0"},
		[]computeFirewallRule{{IPProtocol: "tcp", Ports: []string{"80"}}}, nil)

	var blocked ConnectivityTest
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/connectivityTests", map[string]any{
		"name":        "ct-blocked",
		"protocol":    "TCP",
		"source":      map[string]any{"ipAddress": "10.1.2.3", "network": "default"},
		"destination": map[string]any{"ipAddress": "10.0.0.5", "network": "default", "port": 80},
	}, &blocked)
	if status != 200 || blocked.ReachabilityDetails.Result != "UNREACHABLE" {
		t.Fatalf("source in 10.0.0.0/8: want UNREACHABLE, got status=%d details=%+v", status, blocked.ReachabilityDetails)
	}

	var allowed ConnectivityTest
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/connectivityTests", map[string]any{
		"name":        "ct-allowed",
		"protocol":    "TCP",
		"source":      map[string]any{"ipAddress": "203.0.113.9", "network": "default"},
		"destination": map[string]any{"ipAddress": "10.0.0.5", "network": "default", "port": 80},
	}, &allowed)
	if status != 200 || allowed.ReachabilityDetails.Result != "REACHABLE" {
		t.Fatalf("source outside 10.0.0.0/8: want REACHABLE, got status=%d details=%+v", status, allowed.ReachabilityDetails)
	}
}

func TestUnreachableAcrossUnpeeredNetworks(t *testing.T) {
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	seedNetwork(t, srv, db, "net-a", nil)
	seedNetwork(t, srv, db, "net-b", nil)
	seedFirewall(t, db, "allow-all-b", "net-b", "INGRESS", 1000, []string{"0.0.0.0/0"},
		[]computeFirewallRule{{IPProtocol: "all"}}, nil)

	var ct ConnectivityTest
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/connectivityTests", map[string]any{
		"name":        "ct-cross",
		"protocol":    "TCP",
		"source":      map[string]any{"ipAddress": "10.1.0.5", "network": "net-a"},
		"destination": map[string]any{"ipAddress": "10.2.0.5", "network": "net-b", "port": 443},
	}, &ct)
	if status != 200 {
		t.Fatalf("create: want 200, got %d", status)
	}
	if ct.ReachabilityDetails.Result != "UNREACHABLE" {
		t.Fatalf("unpeered networks: want UNREACHABLE, got %+v", ct.ReachabilityDetails)
	}
}

func TestReachableAcrossActivePeering(t *testing.T) {
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	seedNetwork(t, srv, db, "net-a", []computePeering{{Network: "net-b", State: "ACTIVE"}})
	seedNetwork(t, srv, db, "net-b", nil)
	seedFirewall(t, db, "allow-all-b", "net-b", "INGRESS", 1000, []string{"0.0.0.0/0"},
		[]computeFirewallRule{{IPProtocol: "all"}}, nil)

	var ct ConnectivityTest
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/connectivityTests", map[string]any{
		"name":        "ct-peered",
		"protocol":    "TCP",
		"source":      map[string]any{"ipAddress": "10.1.0.5", "network": "net-a"},
		"destination": map[string]any{"ipAddress": "10.2.0.5", "network": "net-b", "port": 443},
	}, &ct)
	if status != 200 {
		t.Fatalf("create: want 200, got %d", status)
	}
	if ct.ReachabilityDetails.Result != "REACHABLE" {
		t.Fatalf("peered networks: want REACHABLE, got %+v", ct.ReachabilityDetails)
	}
}

// TestRerunReflectsFirewallChange covers the actual point of this
// resource per Phase 11: a firewall added *after* the test was created
// must have a real effect the next time the test is rerun.
func TestRerunReflectsFirewallChange(t *testing.T) {
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	seedNetwork(t, srv, db, "default", nil)

	var ct ConnectivityTest
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/connectivityTests", map[string]any{
		"name":        "ct-rerun",
		"protocol":    "TCP",
		"source":      map[string]any{"ipAddress": "203.0.113.5", "network": "default"},
		"destination": map[string]any{"ipAddress": "10.0.0.5", "network": "default", "port": 80},
	}, &ct)
	if status != 200 || ct.ReachabilityDetails.Result != "UNREACHABLE" {
		t.Fatalf("initial create: want UNREACHABLE, got status=%d details=%+v", status, ct.ReachabilityDetails)
	}

	seedFirewall(t, db, "allow-http", "default", "INGRESS", 1000, []string{"0.0.0.0/0"},
		[]computeFirewallRule{{IPProtocol: "tcp", Ports: []string{"80"}}}, nil)

	var reran ConnectivityTest
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/connectivityTests/ct-rerun:rerun", nil, &reran)
	if status != 200 || reran.ReachabilityDetails.Result != "REACHABLE" {
		t.Fatalf("rerun after adding allow rule: want REACHABLE, got status=%d details=%+v", status, reran.ReachabilityDetails)
	}
}
