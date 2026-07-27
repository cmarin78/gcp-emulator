package cloudsql

import (
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// newTestServer is defined in cloudsql_test.go (same package).

// ── ssl certs ─────────────────────────────────────────────────────────────────

func TestSslCertLifecycle(t *testing.T) {
	srv := newTestServer(t)

	// Create a Cloud SQL instance first.
	testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances",
		map[string]any{"name": "inst1", "databaseVersion": "POSTGRES_14"}, nil)

	base := srv.URL + "/sql/v1beta4/projects/proj1/instances/inst1/sslCerts"

	// Insert a cert.
	var resp map[string]any
	status := testutil.DoJSON(t, "POST", base, map[string]any{"commonName": "my-client"}, &resp)
	if status != 200 {
		t.Fatalf("insert sslCert: want 200, got %d", status)
	}
	clientCert, ok := resp["clientCert"].(map[string]any)
	if !ok {
		t.Fatalf("insert sslCert: missing clientCert in response: %+v", resp)
	}
	certInfo, ok := clientCert["certInfo"].(map[string]any)
	if !ok {
		t.Fatalf("insert sslCert: missing certInfo: %+v", clientCert)
	}
	fingerprint, ok := certInfo["sha1Fingerprint"].(string)
	if !ok || fingerprint == "" {
		t.Fatalf("insert sslCert: no fingerprint: %+v", certInfo)
	}

	// List certs.
	var list struct {
		Items []SslCert `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", base, nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list sslCerts: status=%d items=%+v", status, list.Items)
	}

	// Get cert by fingerprint.
	var got SslCert
	status = testutil.DoJSON(t, "GET", base+"/"+fingerprint, nil, &got)
	if status != 200 || got.Sha1Fingerprint != fingerprint {
		t.Fatalf("get sslCert: status=%d cert=%+v", status, got)
	}
	if got.Cert == "" {
		t.Fatalf("get sslCert: Cert field is empty")
	}
	if got.CommonName != "my-client" {
		t.Fatalf("get sslCert: want CommonName=my-client, got %q", got.CommonName)
	}

	// Delete cert.
	var op Operation
	status = testutil.DoJSON(t, "DELETE", base+"/"+fingerprint, nil, &op)
	if status != 200 || op.Status != "DONE" {
		t.Fatalf("delete sslCert: status=%d op=%+v", status, op)
	}

	// Verify it's gone.
	status = testutil.DoJSON(t, "GET", base+"/"+fingerprint, nil, nil)
	if status != 404 {
		t.Fatalf("get deleted sslCert: want 404, got %d", status)
	}
}

func TestSslCertInsertRequiresCommonName(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances",
		map[string]any{"name": "inst2"}, nil)
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/sql/v1beta4/projects/proj1/instances/inst2/sslCerts",
		map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("insert sslCert no commonName: want 400, got %d", status)
	}
}

func TestSslCertDeleteNotFound(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances",
		map[string]any{"name": "inst3"}, nil)
	status := testutil.DoJSON(t, "DELETE",
		srv.URL+"/sql/v1beta4/projects/proj1/instances/inst3/sslCerts/badfingerprint",
		nil, nil)
	if status != 404 {
		t.Fatalf("delete nonexistent sslCert: want 404, got %d", status)
	}
}

func TestCreateEphemeralCert(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances",
		map[string]any{"name": "inst4"}, nil)

	var cert SslCert
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/sql/v1beta4/projects/proj1/instances/inst4/sslCerts:createEphemeral",
		map[string]any{"publicKey": "-----BEGIN PUBLIC KEY-----\nfake\n-----END PUBLIC KEY-----"},
		&cert)
	if status != 200 || cert.Sha1Fingerprint == "" {
		t.Fatalf("createEphemeral: status=%d cert=%+v", status, cert)
	}
	// Ephemeral certs should NOT appear in list.
	var list struct {
		Items []SslCert `json:"items"`
	}
	testutil.DoJSON(t, "GET",
		srv.URL+"/sql/v1beta4/projects/proj1/instances/inst4/sslCerts", nil, &list)
	if len(list.Items) != 0 {
		t.Fatalf("ephemeral cert appeared in list: %+v", list.Items)
	}
}

// ── flags ─────────────────────────────────────────────────────────────────────

func TestListFlagsReturnsAll(t *testing.T) {
	srv := newTestServer(t)
	var resp struct {
		Items []Flag `json:"items"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/flags", nil, &resp)
	if status != 200 {
		t.Fatalf("list flags: want 200, got %d", status)
	}
	if len(resp.Items) == 0 {
		t.Fatalf("list flags: expected at least 1 flag, got 0")
	}
}

func TestListFlagsFiltersByDatabaseVersion(t *testing.T) {
	srv := newTestServer(t)
	var pgResp struct {
		Items []Flag `json:"items"`
	}
	var myResp struct {
		Items []Flag `json:"items"`
	}
	testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/flags?databaseVersion=POSTGRES_14", nil, &pgResp)
	testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/flags?databaseVersion=MYSQL_8_0", nil, &myResp)

	// max_connections is Postgres-only.
	hasPgFlag := false
	for _, f := range pgResp.Items {
		if f.Name == "max_connections" {
			hasPgFlag = true
		}
	}
	if !hasPgFlag {
		t.Fatalf("POSTGRES_14 flags: expected max_connections, got %v", pgResp.Items)
	}

	// max_connections should NOT appear in MySQL flags.
	for _, f := range myResp.Items {
		if f.Name == "max_connections" {
			t.Fatalf("MYSQL_8_0 flags: unexpected max_connections")
		}
	}

	// innodb_buffer_pool_size should be in MySQL.
	hasMyFlag := false
	for _, f := range myResp.Items {
		if f.Name == "innodb_buffer_pool_size" {
			hasMyFlag = true
		}
	}
	if !hasMyFlag {
		t.Fatalf("MYSQL_8_0 flags: expected innodb_buffer_pool_size, got %v", myResp.Items)
	}
}
