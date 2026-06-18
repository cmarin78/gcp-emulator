package secretmanager

import (
	"encoding/base64"
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

// TestSecretVersionLifecycle covers create secret -> addVersion ->
// access "latest" -> destroy, the flow Terraform's google_secret_manager_secret
// + _version resources rely on.
func TestSecretVersionLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var sec Secret
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/secrets?secretId=my-secret", map[string]any{
		"labels": map[string]string{"env": "test"},
	}, &sec)
	if status != 200 || sec.Name != "projects/proj1/secrets/my-secret" {
		t.Fatalf("create secret: status=%d sec=%+v", status, sec)
	}

	payload := base64.StdEncoding.EncodeToString([]byte("super-secret-value"))
	var ver SecretVersion
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/secrets/my-secret:addVersion", map[string]any{
		"payload": map[string]string{"data": payload},
	}, &ver)
	if status != 200 || ver.State != "ENABLED" {
		t.Fatalf("addVersion: status=%d ver=%+v", status, ver)
	}

	var accessResp struct {
		Name    string `json:"name"`
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/secrets/my-secret/versions/latest:access", nil, &accessResp)
	if status != 200 || accessResp.Payload.Data != payload {
		t.Fatalf("access latest: status=%d resp=%+v", status, accessResp)
	}

	var destroyed SecretVersion
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/secrets/my-secret/versions/latest:destroy", nil, &destroyed)
	if status != 200 || destroyed.State != "DESTROYED" {
		t.Fatalf("destroy: status=%d ver=%+v", status, destroyed)
	}
}

func TestSecretListAndDelete(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/secrets?secretId=s1", nil, nil)

	var list struct {
		Secrets []Secret `json:"secrets"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/secrets", nil, &list)
	if status != 200 || len(list.Secrets) != 1 {
		t.Fatalf("list: status=%d secrets=%+v", status, list.Secrets)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/secrets/s1", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/secrets/s1", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

// TestDuplicateCreateConflict asserts that creating a secret whose
// client-specified secretId already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflict(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/secrets?secretId=dup-secret", nil, nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/secrets?secretId=dup-secret", nil, nil)
	if status != 409 {
		t.Fatalf("duplicate secret: want 409, got %d", status)
	}
}
