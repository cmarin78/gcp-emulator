package gcs

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// newTestServer is defined in gcs_test.go (same package).

func uploadRaw(t *testing.T, baseURL, bucket, name, content string) {
	t.Helper()
	url := baseURL + "/upload/storage/v1/b/" + bucket + "/o?name=" + name
	req, _ := http.NewRequest("POST", url, strings.NewReader(content))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("uploadRaw %s: err=%v status=%v", name, err, resp.StatusCode)
	}
}

func TestPatchObjectMetadata(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b?project=proj1", map[string]any{"name": "bkt"}, nil)
	uploadRaw(t, srv.URL, "bkt", "obj.txt", "hello")

	var patched Object
	status := testutil.DoJSON(t, "PATCH", srv.URL+"/storage/v1/b/bkt/o/obj.txt", map[string]any{
		"contentType": "text/plain",
		"metadata":    map[string]string{"env": "lab"},
	}, &patched)
	if status != 200 || patched.ContentType != "text/plain" {
		t.Fatalf("patch contentType: status=%d obj=%+v", status, patched)
	}
	if patched.Metadata["env"] != "lab" {
		t.Fatalf("patch custom metadata: %+v", patched.Metadata)
	}
}

func TestComposeObjects(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b?project=proj1", map[string]any{"name": "bkt"}, nil)
	uploadRaw(t, srv.URL, "bkt", "part1", "hello ")
	uploadRaw(t, srv.URL, "bkt", "part2", "world")

	var composed Object
	status := testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b/bkt/o/result.txt/compose", map[string]any{
		"sourceObjects": []map[string]string{{"name": "part1"}, {"name": "part2"}},
		"destination":   map[string]any{"contentType": "text/plain"},
	}, &composed)
	if status != 200 || composed.Name != "result.txt" {
		t.Fatalf("compose: status=%d obj=%+v", status, composed)
	}
	// Size should equal len("hello ") + len("world") = 11.
	if composed.Size != "11" {
		t.Fatalf("compose: expected size=11, got %q", composed.Size)
	}
}

func TestComposeMissingSourceReturns404(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b?project=proj1", map[string]any{"name": "bkt"}, nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b/bkt/o/result/compose", map[string]any{
		"sourceObjects": []map[string]string{{"name": "nonexistent"}},
	}, nil)
	if status != 404 {
		t.Fatalf("compose missing source: want 404, got %d", status)
	}
}
