package kms

import (
	"net/http/httptest"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// setupKeyRingAndKey seeds a keyring and a ENCRYPT_DECRYPT crypto key for
// data-plane tests. It reuses newTestServer which is defined in kms_test.go.
func setupKeyRingAndKey(t *testing.T, srv *httptest.Server, ring, key string) {
	t.Helper()
	base := srv.URL
	testutil.DoJSON(t, "POST", base+"/v1/projects/proj1/locations/global/keyRings?keyRingId="+ring, nil, nil)
	testutil.DoJSON(t, "POST", base+"/v1/projects/proj1/locations/global/keyRings/"+ring+"/cryptoKeys?cryptoKeyId="+key,
		map[string]string{"purpose": "ENCRYPT_DECRYPT"}, nil)
}

func TestKMSEncryptDecrypt(t *testing.T) {
	srv := newTestServer(t)
	setupKeyRingAndKey(t, srv, "ring1", "key1")
	keyPath := srv.URL + "/v1/projects/proj1/locations/global/keyRings/ring1/cryptoKeys/key1"

	var enc map[string]any
	status := testutil.DoJSON(t, "POST", keyPath+":encrypt",
		map[string]any{"plaintext": "aGVsbG8="}, // base64("hello")
		&enc)
	if status != 200 || enc["ciphertext"] == "" {
		t.Fatalf("encrypt: status=%d resp=%+v", status, enc)
	}
	ciphertext := enc["ciphertext"].(string)
	if ciphertext == "" {
		t.Fatal("ciphertext is empty")
	}

	var dec map[string]any
	status = testutil.DoJSON(t, "POST", keyPath+":decrypt",
		map[string]any{"ciphertext": ciphertext}, &dec)
	if status != 200 || dec["plaintext"] != "aGVsbG8=" {
		t.Fatalf("decrypt: status=%d resp=%+v", status, dec)
	}
}

func TestKMSEncryptRequiresPlaintext(t *testing.T) {
	srv := newTestServer(t)
	setupKeyRingAndKey(t, srv, "ring2", "key2")
	keyPath := srv.URL + "/v1/projects/proj1/locations/global/keyRings/ring2/cryptoKeys/key2"
	status := testutil.DoJSON(t, "POST", keyPath+":encrypt", map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("encrypt without plaintext: want 400, got %d", status)
	}
}

func TestKMSEncryptNonExistentKeyReturns404(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/global/keyRings?keyRingId=ring3", nil, nil)
	keyPath := srv.URL + "/v1/projects/proj1/locations/global/keyRings/ring3/cryptoKeys/nonexistent"
	status := testutil.DoJSON(t, "POST", keyPath+":encrypt", map[string]any{"plaintext": "x"}, nil)
	if status != 404 {
		t.Fatalf("encrypt nonexistent key: want 404, got %d", status)
	}
}

func TestKMSAsymmetricSign(t *testing.T) {
	srv := newTestServer(t)
	setupKeyRingAndKey(t, srv, "ring4", "key4")
	keyPath := srv.URL + "/v1/projects/proj1/locations/global/keyRings/ring4/cryptoKeys/key4"
	var resp map[string]any
	status := testutil.DoJSON(t, "POST", keyPath+":asymmetricSign",
		map[string]any{"digest": map[string]any{"sha256": "abc"}}, &resp)
	if status != 200 || resp["signature"] == "" {
		t.Fatalf("asymmetricSign: status=%d resp=%+v", status, resp)
	}
}
