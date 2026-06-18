package gcs

import (
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

// TestBucketLifecycle covers create -> get -> list -> delete.
func TestBucketLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var created Bucket
	status := testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b", map[string]string{"name": "my-bucket"}, &created)
	if status != 200 || created.Name != "my-bucket" {
		t.Fatalf("create: status=%d bucket=%+v", status, created)
	}

	var got Bucket
	status = testutil.DoJSON(t, "GET", srv.URL+"/storage/v1/b/my-bucket", nil, &got)
	if status != 200 || got.Name != "my-bucket" {
		t.Fatalf("get: status=%d bucket=%+v", status, got)
	}

	var list struct {
		Items []Bucket `json:"items"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/storage/v1/b", nil, &list)
	if status != 200 || len(list.Items) != 1 {
		t.Fatalf("list: status=%d items=%+v", status, list.Items)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/storage/v1/b/my-bucket", nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", status)
	}
}

// TestObjectUploadDownloadDelete covers the simple-upload flow
// (uploadType=media) plus metadata get, ?alt=media download, and delete --
// the core of `gcloud storage cp`.
func TestObjectUploadDownloadDelete(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b", map[string]string{"name": "bkt"}, nil)

	uploadURL := srv.URL + "/upload/storage/v1/b/bkt/o?name=hello.txt"
	req, _ := http.NewRequest("POST", uploadURL, strings.NewReader("hello world"))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("upload: want 200, got %d", resp.StatusCode)
	}

	var meta Object
	status := testutil.DoJSON(t, "GET", srv.URL+"/storage/v1/b/bkt/o/hello.txt", nil, &meta)
	if status != 200 || meta.Size != "11" {
		t.Fatalf("get metadata: status=%d obj=%+v", status, meta)
	}

	dlResp, err := http.Get(srv.URL + "/storage/v1/b/bkt/o/hello.txt?alt=media")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer dlResp.Body.Close()
	data, _ := io.ReadAll(dlResp.Body)
	if string(data) != "hello world" {
		t.Fatalf("download content: got %q", string(data))
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/storage/v1/b/bkt/o/hello.txt", nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete object: want 204, got %d", status)
	}
}

// TestBucketIamPolicy covers the get/set resource-level IAM binding flow.
func TestBucketIamPolicy(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b", map[string]string{"name": "bkt"}, nil)

	var initial IAMPolicy
	status := testutil.DoJSON(t, "GET", srv.URL+"/storage/v1/b/bkt/iam", nil, &initial)
	if status != 200 || initial.Etag != "initial" {
		t.Fatalf("get iam (empty): status=%d policy=%+v", status, initial)
	}

	req, _ := http.NewRequest("PUT", srv.URL+"/storage/v1/b/bkt/iam", strings.NewReader(
		`{"bindings":[{"role":"roles/storage.objectViewer","members":["allUsers"]}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("set iam: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("set iam: want 200, got %d", resp.StatusCode)
	}

	var reread IAMPolicy
	testutil.DoJSON(t, "GET", srv.URL+"/storage/v1/b/bkt/iam", nil, &reread)
	if len(reread.Bindings) != 1 || reread.Bindings[0].Role != "roles/storage.objectViewer" {
		t.Fatalf("get iam (after set): %+v", reread)
	}
}
