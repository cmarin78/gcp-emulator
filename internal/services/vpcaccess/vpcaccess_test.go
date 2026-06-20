package vpcaccess

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

func validConnectorBody() map[string]any {
	return map[string]any{
		"network":     "default",
		"ipCidrRange": "10.8.0.0/28",
	}
}

// TestConnectorLifecycle covers create -> get -> list -> delete, asserting
// every mutation resolves synchronously (done:true) and the connector
// always reports state READY (no real VM provisioning happens).
func TestConnectorLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/connectors?connectorId=my-connector",
		validConnectorBody(), &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Connector
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/connectors/my-connector", nil, &got)
	if status != 200 || got.State != "READY" || got.Network != "default" || got.MinThroughput == 0 {
		t.Fatalf("get: status=%d conn=%+v", status, got)
	}

	var list struct {
		Connectors []Connector `json:"connectors"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/connectors", nil, &list)
	if status != 200 || len(list.Connectors) != 1 {
		t.Fatalf("list: status=%d connectors=%+v", status, list.Connectors)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/locations/us-central1/connectors/my-connector", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

func TestCreateConnectorRequiresConnectorId(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/connectors", validConnectorBody(), nil)
	if status != 400 {
		t.Fatalf("create without connectorId: want 400, got %d", status)
	}
}

func TestCreateConnectorRequiresNetworkAndCidr(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/connectors?connectorId=bad",
		map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without network/ipCidrRange: want 400, got %d", status)
	}
}

// TestDuplicateCreateConflict asserts that creating a connector whose
// connectorId already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/connectors?connectorId=my-connector",
		validConnectorBody(), nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/connectors?connectorId=my-connector",
		validConnectorBody(), nil)
	if status != 409 {
		t.Fatalf("duplicate connector: want 409, got %d", status)
	}
}

func TestConnectorNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/us-central1/connectors/missing", nil, nil)
	if status != 404 {
		t.Fatalf("get missing connector: want 404, got %d", status)
	}
}
