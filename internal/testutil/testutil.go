// Package testutil provides small helpers shared by every service's test
// suite: an isolated, throwaway BoltDB-backed *storage.DB per test (so tests
// never share state or fight over a file), and a couple of JSON request/
// response helpers for hitting an httptest.Server the same way a real
// gcloud/Terraform client would.
package testutil

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/cesar/gcp-emulator/internal/storage"
)

// NewDB opens a fresh BoltDB file inside t.TempDir() and registers a
// cleanup hook to close it. Each call gets its own file, so tests can run
// in parallel without sharing storage.
func NewDB(t *testing.T) *storage.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("testutil.NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// DoJSON issues an HTTP request against srvURL+path, optionally encoding
// body as the JSON request payload (pass nil for no body), and decodes the
// JSON response into out (pass nil to ignore the body). It fails the test
// on any transport error, but NOT on non-2xx status codes — callers that
// care about the status should check the returned int themselves.
func DoJSON(t *testing.T, method, url string, body any, out any) int {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("testutil.DoJSON: marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(data)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		t.Fatalf("testutil.DoJSON: new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("testutil.DoJSON: %s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("testutil.DoJSON: %s %s: decode response: %v", method, url, err)
		}
	}
	return resp.StatusCode
}
