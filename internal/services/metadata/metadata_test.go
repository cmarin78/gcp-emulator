package metadata

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestMetadataServerFlavorRequired(t *testing.T) {
	srv := newTestServer(t)

	// Missing header should yield 403 Forbidden
	resp, err := http.Get(srv.URL + "/computeMetadata/v1/project/project-id")
	if err != nil {
		t.Fatalf("failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d", resp.StatusCode)
	}
}

func TestMetadataServerEndpoints(t *testing.T) {
	srv := newTestServer(t)

	client := &http.Client{}
	req, _ := http.NewRequest("GET", srv.URL+"/computeMetadata/v1/project/project-id", nil)
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "demo-project" {
		t.Fatalf("expected demo-project, got %s", string(body))
	}

	// Token endpoint
	req, _ = http.NewRequest("GET", srv.URL+"/computeMetadata/v1/instance/service-accounts/default/token", nil)
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("failed to decode token response: %v", err)
	}
	if !strings.HasPrefix(tokenResp.AccessToken, "ya29.") {
		t.Fatalf("expected ya29. prefix, got %s", tokenResp.AccessToken)
	}
	if tokenResp.ExpiresIn != 3600 {
		t.Fatalf("expected expires_in 3600, got %d", tokenResp.ExpiresIn)
	}

	// Email endpoint
	req, _ = http.NewRequest("GET", srv.URL+"/computeMetadata/v1/instance/service-accounts/default/email", nil)
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	emailBytes, _ := io.ReadAll(resp.Body)
	if string(emailBytes) != "default@developer.gserviceaccount.com" {
		t.Fatalf("expected default email, got %s", string(emailBytes))
	}
}
