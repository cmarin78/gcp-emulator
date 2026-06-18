package compute

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

// Operation mirrors internal/server.Operation's relevant fields for
// decoding responses in this package's tests (compute uses
// server.Operations, whose Operation type has a Status string field --
// "PENDING" | "RUNNING" | "DONE" -- not a Done bool like most other
// packages' google.longrunning-shaped operations).
type Operation struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	OperationType string `json:"operationType"`
}

// TestListZonesAndMachineTypes covers the static catalogs served for any
// {project}/{zone} combination.
func TestListZonesAndMachineTypes(t *testing.T) {
	srv := newTestServer(t)

	var zones struct {
		Items []struct{ Name string } `json:"items"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones", nil, &zones)
	if status != 200 || len(zones.Items) == 0 {
		t.Fatalf("list zones: status=%d zones=%+v", status, zones)
	}

	var zone struct{ Name string }
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a", nil, &zone)
	if status != 200 || zone.Name != "us-central1-a" {
		t.Fatalf("get zone: status=%d zone=%+v", status, zone)
	}

	var mts struct {
		Items []struct{ Name string } `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/machineTypes", nil, &mts)
	if status != 200 || len(mts.Items) == 0 {
		t.Fatalf("list machineTypes: status=%d mts=%+v", status, mts)
	}
}

// TestInstanceGroupManagersStub asserts both the zonal and aggregated list
// endpoints always return an empty list -- needed because Terraform's GKE
// node-pool reader queries this, but this emulator models no real IGMs.
func TestInstanceGroupManagersStub(t *testing.T) {
	srv := newTestServer(t)

	var zonal struct {
		Items []any `json:"items"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers", nil, &zonal)
	if status != 200 || zonal.Items == nil || len(zonal.Items) != 0 {
		t.Fatalf("zonal igm stub: status=%d items=%+v", status, zonal.Items)
	}

	var agg struct {
		Items []any `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/aggregated/instanceGroupManagers", nil, &agg)
	if status != 200 || agg.Items == nil || len(agg.Items) != 0 {
		t.Fatalf("aggregated igm stub: status=%d items=%+v", status, agg.Items)
	}
}

// TestInstanceLifecycle covers insert (with a boot disk via initializeParams
// and a network interface) -> get -> list -> stop -> start -> delete,
// asserting the operation shape uses Status=="DONE" (compute's Operation type
// has no Done bool, unlike most other packages) and that stop/start return
// the exact opType gcloud needs to resolve its operation poller.
func TestInstanceLifecycle(t *testing.T) {
	srv := newTestServer(t)

	body := map[string]any{
		"name":        "my-instance",
		"machineType": "zones/us-central1-a/machineTypes/e2-medium",
		"disks": []map[string]any{
			{
				"boot": true,
				"initializeParams": map[string]any{
					"sourceImage": "projects/debian-cloud/global/images/family/debian-12",
					"diskSizeGb":  "10",
				},
			},
		},
		"networkInterfaces": []map[string]any{
			{"network": "default", "accessConfigs": []map[string]any{{}}},
		},
	}
	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances", body, &createOp)
	if status != 200 || createOp.Status != "DONE" || createOp.OperationType != "insert" {
		t.Fatalf("insert: status=%d op=%+v", status, createOp)
	}

	var got Instance
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances/my-instance", nil, &got)
	if status != 200 || got.Status != "RUNNING" {
		t.Fatalf("get: status=%d inst=%+v", status, got)
	}
	if len(got.Disks) != 1 || got.Disks[0].Source == "" {
		t.Fatalf("expected boot disk resolved to a Source selfLink, got %+v", got.Disks)
	}
	if len(got.NetworkInterfaces) != 1 || len(got.NetworkInterfaces[0].AccessConfigs) == 0 {
		t.Fatalf("expected network interface w/ access config, got %+v", got.NetworkInterfaces)
	}

	var list struct {
		Items []Instance `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances", nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list: status=%d items=%+v", status, list.Items)
	}

	var stopOp Operation
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances/my-instance/stop", nil, &stopOp)
	if status != 200 || stopOp.Status != "DONE" || stopOp.OperationType != "stop" {
		t.Fatalf("stop: status=%d op=%+v", status, stopOp)
	}
	var stopped Instance
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances/my-instance", nil, &stopped)
	if stopped.Status != "TERMINATED" {
		t.Fatalf("expected TERMINATED after stop, got %q", stopped.Status)
	}

	var startOp Operation
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances/my-instance/start", nil, &startOp)
	if status != 200 || startOp.Status != "DONE" || startOp.OperationType != "start" {
		t.Fatalf("start: status=%d op=%+v", status, startOp)
	}
	var started Instance
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances/my-instance", nil, &started)
	if started.Status != "RUNNING" {
		t.Fatalf("expected RUNNING after start, got %q", started.Status)
	}

	// Operation polling: a previously returned operation name must still be
	// retrievable and reflect Status=="DONE".
	var polled Operation
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/operations/"+startOp.Name, nil, &polled)
	if status != 200 || polled.Status != "DONE" {
		t.Fatalf("poll operation: status=%d op=%+v", status, polled)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances/my-instance", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

func TestInsertInstanceRequiresName(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances", map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("insert without name: want 400, got %d", status)
	}
}

// TestNetworksSubnetworksFirewalls covers basic CRUD across the three
// networking resource types in network.go.
func TestNetworksSubnetworksFirewalls(t *testing.T) {
	srv := newTestServer(t)

	var netOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/networks", map[string]any{
		"name": "my-net",
	}, &netOp)
	if status != 200 || netOp.Status != "DONE" || netOp.OperationType != "insert" {
		t.Fatalf("insert network: status=%d op=%+v", status, netOp)
	}

	var subOp Operation
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/regions/us-central1/subnetworks", map[string]any{
		"name":        "my-subnet",
		"network":     "my-net",
		"ipCidrRange": "10.0.0.0/24",
	}, &subOp)
	if status != 200 || subOp.Status != "DONE" {
		t.Fatalf("insert subnetwork: status=%d op=%+v", status, subOp)
	}

	var fwOp Operation
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/firewalls", map[string]any{
		"name": "my-fw",
	}, &fwOp)
	if status != 200 || fwOp.Status != "DONE" {
		t.Fatalf("insert firewall: status=%d op=%+v", status, fwOp)
	}
	var fw Firewall
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/firewalls/my-fw", nil, &fw)
	if status != 200 || fw.Network == "" || fw.Direction != "INGRESS" {
		t.Fatalf("get firewall: status=%d fw=%+v", status, fw)
	}

	var gotNet Network
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/networks/my-net", nil, &gotNet)
	if status != 200 || gotNet.Name != "my-net" || !gotNet.AutoCreateSubnetworks {
		t.Fatalf("get network: status=%d net=%+v", status, gotNet)
	}

	var subList struct {
		Items []Subnetwork `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/regions/us-central1/subnetworks", nil, &subList)
	if status != 200 || len(subList.Items) != 1 {
		t.Fatalf("list subnetworks: status=%d items=%+v", status, subList.Items)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/global/firewalls/my-fw", nil, nil)
	if status != 200 {
		t.Fatalf("delete firewall: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/regions/us-central1/subnetworks/my-subnet", nil, nil)
	if status != 200 {
		t.Fatalf("delete subnetwork: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/global/networks/my-net", nil, nil)
	if status != 200 {
		t.Fatalf("delete network: want 200, got %d", status)
	}
}

// TestImagesCatalog covers the static, read-only images catalog (list,
// get-by-name, get-by-family) -- there is no create route for images.
func TestImagesCatalog(t *testing.T) {
	srv := newTestServer(t)

	var list struct {
		Items []Image `json:"items"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/debian-cloud/global/images", nil, &list)
	if status != 200 || len(list.Items) == 0 {
		t.Fatalf("list images: status=%d items=%+v", status, list.Items)
	}

	var byFamily Image
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/debian-cloud/global/images/family/debian-12", nil, &byFamily)
	if status != 200 || byFamily.Family != "debian-12" {
		t.Fatalf("get image by family: status=%d img=%+v", status, byFamily)
	}

	var byName Image
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/debian-cloud/global/images/"+byFamily.Name, nil, &byName)
	if status != 200 || byName.Name != byFamily.Name {
		t.Fatalf("get image by name: status=%d img=%+v", status, byName)
	}
}

// TestDisksLifecycle covers standalone disk CRUD.
func TestDisksLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var diskOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/disks", map[string]any{
		"name": "my-disk",
	}, &diskOp)
	if status != 200 || diskOp.Status != "DONE" {
		t.Fatalf("insert disk: status=%d op=%+v", status, diskOp)
	}
	var disk Disk
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/disks/my-disk", nil, &disk)
	if status != 200 || disk.SizeGb != "10" || disk.Type != "pd-standard" {
		t.Fatalf("get disk: status=%d disk=%+v", status, disk)
	}

	var got Disk
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/disks/my-disk", nil, &got)
	if status != 200 || got.Name != "my-disk" {
		t.Fatalf("get disk: status=%d disk=%+v", status, got)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/disks/my-disk", nil, nil)
	if status != 200 {
		t.Fatalf("delete disk: want 200, got %d", status)
	}
}

// TestRoutersAndNat covers router CRUD plus the nested Nats patch behavior
// (Cloud NAT is modeled as nested config on the parent Router, not its own
// resource, matching how Terraform's google_compute_router_nat works).
func TestRoutersAndNat(t *testing.T) {
	srv := newTestServer(t)

	var routerOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/regions/us-central1/routers", map[string]any{
		"name":    "my-router",
		"network": "default",
	}, &routerOp)
	if status != 200 || routerOp.Status != "DONE" {
		t.Fatalf("insert router: status=%d op=%+v", status, routerOp)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/compute/v1/projects/proj1/regions/us-central1/routers/my-router", map[string]any{
		"nats": []map[string]any{
			{"name": "my-nat", "natIpAllocateOption": "AUTO_ONLY", "sourceSubnetworkIpRangesToNat": "ALL_SUBNETWORKS_ALL_IP_RANGES"},
		},
	}, &patchOp)
	if status != 200 || patchOp.Status != "DONE" {
		t.Fatalf("patch router nats: status=%d op=%+v", status, patchOp)
	}

	var got Router
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/regions/us-central1/routers/my-router", nil, &got)
	if status != 200 || len(got.Nats) != 1 || got.Nats[0].Name != "my-nat" {
		t.Fatalf("get router after nat patch: status=%d router=%+v", status, got)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/regions/us-central1/routers/my-router", nil, nil)
	if status != 200 {
		t.Fatalf("delete router: want 200, got %d", status)
	}
}

// TestRoutesLifecycle covers route CRUD.
func TestRoutesLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var routeOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/routes", map[string]any{
		"name":      "my-route",
		"network":   "default",
		"destRange": "0.0.0.0/0",
	}, &routeOp)
	if status != 200 || routeOp.Status != "DONE" {
		t.Fatalf("insert route: status=%d op=%+v", status, routeOp)
	}

	var list struct {
		Items []Route `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/routes", nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list routes: status=%d items=%+v", status, list.Items)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/global/routes/my-route", nil, nil)
	if status != 200 {
		t.Fatalf("delete route: want 200, got %d", status)
	}
}

// TestDuplicateCreateConflicts asserts that inserting any client-named
// resource in this package (instance, network, subnetwork, firewall, disk,
// router, route) twice with the same name returns 409 ALREADY_EXISTS instead
// of silently overwriting.
func TestDuplicateCreateConflicts(t *testing.T) {
	srv := newTestServer(t)

	cases := []struct {
		label string
		path  string
		body  map[string]any
	}{
		{"instance", "/compute/v1/projects/proj1/zones/us-central1-a/instances", map[string]any{"name": "dup-instance"}},
		{"network", "/compute/v1/projects/proj1/global/networks", map[string]any{"name": "dup-net"}},
		{"subnetwork", "/compute/v1/projects/proj1/regions/us-central1/subnetworks", map[string]any{"name": "dup-subnet", "network": "default", "ipCidrRange": "10.0.0.0/24"}},
		{"firewall", "/compute/v1/projects/proj1/global/firewalls", map[string]any{"name": "dup-fw"}},
		{"disk", "/compute/v1/projects/proj1/zones/us-central1-a/disks", map[string]any{"name": "dup-disk"}},
		{"router", "/compute/v1/projects/proj1/regions/us-central1/routers", map[string]any{"name": "dup-router", "network": "default"}},
		{"route", "/compute/v1/projects/proj1/global/routes", map[string]any{"name": "dup-route", "network": "default", "destRange": "0.0.0.0/0"}},
	}

	for _, c := range cases {
		testutil.DoJSON(t, "POST", srv.URL+c.path, c.body, nil)
		status := testutil.DoJSON(t, "POST", srv.URL+c.path, c.body, nil)
		if status != 409 {
			t.Fatalf("duplicate %s: want 409, got %d", c.label, status)
		}
	}
}
