package certificatemanager

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

func TestCertificateLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/locations/global/certificates?certificateId=my-cert",
		map[string]any{"managed": map[string]any{"domains": []string{"example.com"}}}, &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Certificate
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/global/certificates/my-cert", nil, &got)
	if status != 200 || got.Managed == nil || got.Managed.State != "ACTIVE" {
		t.Fatalf("get: status=%d cert=%+v", status, got)
	}

	var list struct {
		Certificates []Certificate `json:"certificates"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/global/certificates", nil, &list)
	if status != 200 || len(list.Certificates) != 1 {
		t.Fatalf("list: status=%d certs=%+v", status, list.Certificates)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/locations/global/certificates/my-cert", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete: status=%d op=%+v", status, deleteOp)
	}
}

func TestCertificateRequiresCertificateId(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/certificates",
		map[string]any{"managed": map[string]any{"domains": []string{"example.com"}}}, nil)
	if status != 400 {
		t.Fatalf("create without certificateId: want 400, got %d", status)
	}
}

func TestCertificateMapLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/locations/global/certificateMaps?certificateMapId=my-map",
		map[string]any{"description": "test map"}, &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create map: status=%d op=%+v", status, createOp)
	}

	var got CertificateMap
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/locations/global/certificateMaps/my-map", nil, &got)
	if status != 200 || got.Description != "test map" {
		t.Fatalf("get map: status=%d map=%+v", status, got)
	}

	var deleteOp Operation
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/locations/global/certificateMaps/my-map", nil, &deleteOp)
	if status != 200 || !deleteOp.Done {
		t.Fatalf("delete map: status=%d op=%+v", status, deleteOp)
	}
}

func TestDuplicateCertificateConflict(t *testing.T) {
	srv := newTestServer(t)
	body := map[string]any{"managed": map[string]any{"domains": []string{"example.com"}}}
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/certificates?certificateId=dup", body, nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/certificates?certificateId=dup", body, nil)
	if status != 409 {
		t.Fatalf("duplicate certificate: want 409, got %d", status)
	}
}
