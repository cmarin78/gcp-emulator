package privateca

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

func TestPrivateCALifecycleAndCascade(t *testing.T) {
	srv := newTestServer(t)

	// 1. Create CaPool
	var op Operation
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/my-proj/locations/us-central1/caPools?caPoolId=my-pool", map[string]any{
		"tier": "ENTERPRISE",
	}, &op)
	if !op.Done {
		t.Fatal("expected create ca pool op to be done")
	}

	// 2. Get CaPool
	var pool CaPool
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/locations/us-central1/caPools/my-pool", nil, &pool)
	if pool.Tier != "ENTERPRISE" {
		t.Fatalf("expected tier ENTERPRISE, got %s", pool.Tier)
	}

	// 3. Create CertificateAuthority under pool
	var caOp Operation
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/my-proj/locations/us-central1/caPools/my-pool/certificateAuthorities?certificateAuthorityId=root-ca", map[string]any{
		"type": "SELF_SIGNED",
		"keySpec": map[string]any{
			"algorithm": "RSA_PSS_2048_SHA256",
		},
	}, &caOp)
	if !caOp.Done {
		t.Fatal("expected create ca op to be done")
	}

	// 4. Disable CA via custom action
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/my-proj/locations/us-central1/caPools/my-pool/certificateAuthorities/root-ca:disable", nil, &caOp)
	var ca CertificateAuthority
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/locations/us-central1/caPools/my-pool/certificateAuthorities/root-ca", nil, &ca)
	if ca.State != "DISABLED" {
		t.Fatalf("expected state DISABLED, got %s", ca.State)
	}

	// 5. Delete CaPool -> should cascade delete root-ca
	testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/my-proj/locations/us-central1/caPools/my-pool", nil, &op)

	status := testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/locations/us-central1/caPools/my-pool", nil, nil)
	if status != 404 {
		t.Fatalf("expected 404 for deleted pool, got %d", status)
	}

	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/locations/us-central1/caPools/my-pool/certificateAuthorities/root-ca", nil, nil)
	if status != 404 {
		t.Fatalf("expected 404 for cascaded ca, got %d", status)
	}
}
