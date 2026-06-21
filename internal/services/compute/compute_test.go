package compute

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cesar/gcp-emulator/internal/services/orgpolicy"
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
// endpoints return an empty result for a project with no MIGs. As of Phase
// 9 the zonal list is real (see TestInstanceGroupManagerAndAutoscalerLifecycle
// for the populated case); the aggregated/list variant is still a fixed
// empty-map response (real API shape: items is a scope->ScopedList map, not
// an array, so an empty map is the correct "no matches" response).
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
		Items map[string]any `json:"items"`
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

// TestInsertInstanceBlockedByVmExternalIpAccessPolicy covers the Phase 11
// org-policy enforcement added to insertInstance: registering compute and
// orgpolicy on the same mux/db (as cmd/server/main.go does), enabling
// constraints/compute.vmExternalIpAccess makes an instance request with an
// accessConfig (external IP) fail with FAILED_PRECONDITION, while an
// instance with no accessConfigs (no external IP requested) is unaffected.
func TestInsertInstanceBlockedByVmExternalIpAccessPolicy(t *testing.T) {
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	orgpolicy.New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	status := testutil.DoJSON(t, "POST", srv.URL+"/v2/projects/proj1/policies", map[string]any{
		"name": "projects/proj1/policies/compute.vmExternalIpAccess",
		"spec": map[string]any{"rules": []map[string]any{{"enforce": true}}},
	}, nil)
	if status != 200 {
		t.Fatalf("create org policy: want 200, got %d", status)
	}

	withExternalIP := map[string]any{
		"name": "external-instance",
		"networkInterfaces": []map[string]any{
			{"network": "default", "accessConfigs": []map[string]any{{}}},
		},
	}
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances", withExternalIP, nil)
	if status != 412 {
		t.Fatalf("insert with external IP + constraint enforced: want 412, got %d", status)
	}

	noExternalIP := map[string]any{
		"name": "internal-instance",
		"networkInterfaces": []map[string]any{
			{"network": "default"},
		},
	}
	var op Operation
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instances", noExternalIP, &op)
	if status != 200 || op.Status != "DONE" {
		t.Fatalf("insert without external IP: want 200/DONE, got status=%d op=%+v", status, op)
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

// TestNetworkPeering covers networks.addPeering/removePeering (Phase 10:
// google_compute_network_peering), asserting the peering shows up nested
// on the network resource and can be replaced/removed by name.
func TestNetworkPeering(t *testing.T) {
	srv := newTestServer(t)

	testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/networks", map[string]any{"name": "net-a"}, nil)
	testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/networks", map[string]any{"name": "net-b"}, nil)

	var addOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/networks/net-a/addPeering", map[string]any{
		"networkPeering": map[string]any{
			"name":                 "peer-a-b",
			"network":              "net-b",
			"exchangeSubnetRoutes": true,
		},
	}, &addOp)
	if status != 200 || addOp.Status != "DONE" || addOp.OperationType != "addPeering" {
		t.Fatalf("addPeering: status=%d op=%+v", status, addOp)
	}

	var net Network
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/networks/net-a", nil, &net)
	if status != 200 || len(net.Peerings) != 1 || net.Peerings[0].State != "ACTIVE" || !net.Peerings[0].ExchangeSubnetRoutes {
		t.Fatalf("get network after addPeering: status=%d net=%+v", status, net)
	}

	var removeOp Operation
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/networks/net-a/removePeering",
		map[string]any{"name": "peer-a-b"}, &removeOp)
	if status != 200 || removeOp.Status != "DONE" {
		t.Fatalf("removePeering: status=%d op=%+v", status, removeOp)
	}

	var netAfterRemove Network
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/networks/net-a", nil, &netAfterRemove)
	if status != 200 || len(netAfterRemove.Peerings) != 0 {
		t.Fatalf("get network after removePeering: status=%d net=%+v", status, netAfterRemove)
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
		{"instanceTemplate", "/compute/v1/projects/proj1/global/instanceTemplates", map[string]any{"name": "dup-tmpl"}},
		{"instanceGroupManager", "/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers", map[string]any{"name": "dup-mig", "instanceTemplate": "dup-tmpl"}},
		{"autoscaler", "/compute/v1/projects/proj1/zones/us-central1-a/autoscalers", map[string]any{"name": "dup-as", "target": "dup-mig"}},
	}

	for _, c := range cases {
		testutil.DoJSON(t, "POST", srv.URL+c.path, c.body, nil)
		status := testutil.DoJSON(t, "POST", srv.URL+c.path, c.body, nil)
		if status != 409 {
			t.Fatalf("duplicate %s: want 409, got %d", c.label, status)
		}
	}
}

// TestInstanceTemplateLifecycle covers create -> get -> list -> delete for
// the immutable instance template resource (no update endpoint, matching
// the real API).
func TestInstanceTemplateLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var op Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/instanceTemplates", map[string]any{
		"name": "my-template",
		"properties": map[string]any{
			"machineType": "e2-medium",
			"disks": []map[string]any{
				{"boot": true, "initializeParams": map[string]any{"sourceImage": "projects/debian-cloud/global/images/family/debian-12"}},
			},
		},
	}, &op)
	if status != 200 || op.Status != "DONE" || op.OperationType != "insert" {
		t.Fatalf("insert template: status=%d op=%+v", status, op)
	}

	var got InstanceTemplate
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/instanceTemplates/my-template", nil, &got)
	if status != 200 || got.Properties.MachineType != "e2-medium" {
		t.Fatalf("get template: status=%d tmpl=%+v", status, got)
	}

	var list struct {
		Items []InstanceTemplate `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/global/instanceTemplates", nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list templates: status=%d items=%+v", status, list.Items)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/global/instanceTemplates/my-template", nil, nil)
	if status != 200 {
		t.Fatalf("delete template: want 200, got %d", status)
	}
}

// TestInstanceGroupManagerAndAutoscalerLifecycle covers MIG create -> get ->
// list -> patch -> resize -> delete, plus a paired autoscaler's create ->
// get -> patch -> delete -- the standard Terraform fleet-management pairing
// (google_compute_instance_group_manager + google_compute_autoscaler) that
// was the single biggest Compute coverage gap before Phase 9.
func TestInstanceGroupManagerAndAutoscalerLifecycle(t *testing.T) {
	srv := newTestServer(t)

	testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/instanceTemplates", map[string]any{"name": "fleet-template"}, nil)

	var migOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers", map[string]any{
		"name":             "my-mig",
		"instanceTemplate": "fleet-template",
		"targetSize":       2,
	}, &migOp)
	if status != 200 || migOp.Status != "DONE" || migOp.OperationType != "insert" {
		t.Fatalf("insert mig: status=%d op=%+v", status, migOp)
	}

	var got InstanceGroupManager
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/my-mig", nil, &got)
	if status != 200 || got.TargetSize != 2 || got.InstanceTemplate == "" {
		t.Fatalf("get mig: status=%d mig=%+v", status, got)
	}

	var list struct {
		Items []InstanceGroupManager `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers", nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list migs: status=%d items=%+v", status, list.Items)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/my-mig", map[string]any{
		"targetSize": 3,
	}, &patchOp)
	if status != 200 || patchOp.Status != "DONE" {
		t.Fatalf("patch mig: status=%d op=%+v", status, patchOp)
	}

	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/my-mig/resize?size=5", nil, nil)
	if status != 200 {
		t.Fatalf("resize mig: want 200, got %d", status)
	}
	var resized InstanceGroupManager
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/my-mig", nil, &resized)
	if resized.TargetSize != 5 {
		t.Fatalf("expected targetSize=5 after resize, got %d", resized.TargetSize)
	}

	var asOp Operation
	status = testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/autoscalers", map[string]any{
		"name":   "my-as",
		"target": "my-mig",
		"autoscalingPolicy": map[string]any{
			"minNumReplicas": 1,
			"maxNumReplicas": 10,
		},
	}, &asOp)
	if status != 200 || asOp.Status != "DONE" {
		t.Fatalf("insert autoscaler: status=%d op=%+v", status, asOp)
	}

	var gotAs Autoscaler
	status = testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/autoscalers/my-as", nil, &gotAs)
	if status != 200 || gotAs.AutoscalingPolicy.MaxNumReplicas != 10 {
		t.Fatalf("get autoscaler: status=%d as=%+v", status, gotAs)
	}

	var patchAsOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/autoscalers/my-as", map[string]any{
		"autoscalingPolicy": map[string]any{"minNumReplicas": 2, "maxNumReplicas": 20},
	}, &patchAsOp)
	if status != 200 || patchAsOp.Status != "DONE" {
		t.Fatalf("patch autoscaler: status=%d op=%+v", status, patchAsOp)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/autoscalers/my-as", nil, nil)
	if status != 200 {
		t.Fatalf("delete autoscaler: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/my-mig", nil, nil)
	if status != 200 {
		t.Fatalf("delete mig: want 200, got %d", status)
	}
}

// TestAutoscalerClampsRealTargetSize covers the Phase 11 behavior added to
// instancegroups.go: an autoscaler's min/maxNumReplicas now has a real,
// immediate effect on its target MIG's targetSize instead of being stored
// as decorative metadata. It covers three angles: attaching an autoscaler
// to an already-oversized MIG shrinks it immediately; a later resize past
// the bounds gets clamped instead of accepted as-is; and tightening the
// policy via PATCH shrinks the group again without a manual resize.
func TestAutoscalerClampsRealTargetSize(t *testing.T) {
	srv := newTestServer(t)

	testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/global/instanceTemplates", map[string]any{"name": "tmpl"}, nil)
	testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers", map[string]any{
		"name":             "clamp-mig",
		"instanceTemplate": "tmpl",
		"targetSize":       8,
	}, nil)

	// Atar un autoscaler con max=5 a un MIG que ya está en 8 debe achicarlo
	// de verdad, ahí mismo, en vez de dejarlo como estaba hasta el próximo
	// resize manual.
	status := testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/autoscalers", map[string]any{
		"name":   "clamp-as",
		"target": "clamp-mig",
		"autoscalingPolicy": map[string]any{
			"minNumReplicas": 2,
			"maxNumReplicas": 5,
		},
	}, nil)
	if status != 200 {
		t.Fatalf("insert autoscaler: want 200, got %d", status)
	}
	var afterAttach InstanceGroupManager
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/clamp-mig", nil, &afterAttach)
	if afterAttach.TargetSize != 5 {
		t.Fatalf("expected targetSize clamped to 5 right after attaching autoscaler, got %d", afterAttach.TargetSize)
	}

	// Un resize pidiendo 20 (por encima de max=5) debe quedar clamped a 5.
	testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/clamp-mig/resize?size=20", nil, nil)
	var afterResize InstanceGroupManager
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/clamp-mig", nil, &afterResize)
	if afterResize.TargetSize != 5 {
		t.Fatalf("expected resize above max clamped to 5, got %d", afterResize.TargetSize)
	}

	// Un resize pidiendo 1 (por debajo de min=2) debe quedar clamped a 2.
	testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/clamp-mig/resize?size=1", nil, nil)
	var afterLowResize InstanceGroupManager
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/clamp-mig", nil, &afterLowResize)
	if afterLowResize.TargetSize != 2 {
		t.Fatalf("expected resize below min clamped to 2, got %d", afterLowResize.TargetSize)
	}

	// Subir max a 4 (por debajo del tamaño actual, 2, no cambia nada) y
	// luego a un escenario donde el tamaño actual queda fuera de rango:
	// bajar max por debajo del tamaño actual debe achicar el grupo de
	// verdad vía PATCH del autoscaler, sin un resize manual.
	testutil.DoJSON(t, "POST", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/clamp-mig/resize?size=4", nil, nil)
	testutil.DoJSON(t, "PATCH", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/autoscalers/clamp-as", map[string]any{
		"autoscalingPolicy": map[string]any{"minNumReplicas": 1, "maxNumReplicas": 3},
	}, nil)
	var afterPatch InstanceGroupManager
	testutil.DoJSON(t, "GET", srv.URL+"/compute/v1/projects/proj1/zones/us-central1-a/instanceGroupManagers/clamp-mig", nil, &afterPatch)
	if afterPatch.TargetSize != 3 {
		t.Fatalf("expected targetSize shrunk to 3 after lowering max via PATCH, got %d", afterPatch.TargetSize)
	}
}
