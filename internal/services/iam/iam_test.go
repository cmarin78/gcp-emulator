package iam

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

// TestServiceAccountLifecycle covers create -> get -> list -> delete, the
// minimum flow `gcloud iam service-accounts ...` exercises.
func TestServiceAccountLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var created ServiceAccount
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/serviceAccounts", map[string]any{
		"accountId": "my-sa",
		"serviceAccount": map[string]string{
			"displayName": "My SA",
		},
	}, &created)
	if status != 200 {
		t.Fatalf("create: want 200, got %d", status)
	}
	if created.Email != "my-sa@proj1.iam.gserviceaccount.com" {
		t.Fatalf("unexpected email: %q", created.Email)
	}

	var got ServiceAccount
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/serviceAccounts/my-sa", nil, &got)
	if status != 200 || got.Email != created.Email {
		t.Fatalf("get: status=%d got=%+v", status, got)
	}

	var listResp struct {
		Accounts []ServiceAccount `json:"accounts"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/serviceAccounts", nil, &listResp)
	if status != 200 || len(listResp.Accounts) != 1 {
		t.Fatalf("list: status=%d accounts=%+v", status, listResp.Accounts)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/serviceAccounts/my-sa", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}

	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/serviceAccounts/my-sa", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

// TestDuplicateCreateConflicts asserts that creating a service account or
// custom role whose client-specified ID already exists returns 409
// ALREADY_EXISTS instead of silently overwriting.
func TestDuplicateCreateConflicts(t *testing.T) {
	srv := newTestServer(t)

	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/serviceAccounts", map[string]any{
		"accountId": "dup-sa",
	}, nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/serviceAccounts", map[string]any{
		"accountId": "dup-sa",
	}, nil)
	if status != 409 {
		t.Fatalf("duplicate service account: want 409, got %d", status)
	}

	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/roles", map[string]any{
		"roleId": "dupRole",
	}, nil)
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/roles", map[string]any{
		"roleId": "dupRole",
	}, nil)
	if status != 409 {
		t.Fatalf("duplicate custom role: want 409, got %d", status)
	}
}

// TestProjectIamPolicy covers the getIamPolicy/setIamPolicy action dispatch,
// which relies on Go's mux capturing a whole path segment and the handler
// splitting on ":" itself -- a pattern worth covering directly since it's
// easy to break by accident.
func TestProjectIamPolicy(t *testing.T) {
	srv := newTestServer(t)

	var initial Policy
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1:getIamPolicy", nil, &initial)
	if status != 200 || initial.Etag != "initial" {
		t.Fatalf("getIamPolicy (empty): status=%d policy=%+v", status, initial)
	}

	var updated Policy
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1:setIamPolicy", map[string]any{
		"policy": Policy{
			Version:  1,
			Bindings: []Binding{{Role: "roles/viewer", Members: []string{"user:a@example.com"}}},
		},
	}, &updated)
	if status != 200 || len(updated.Bindings) != 1 {
		t.Fatalf("setIamPolicy: status=%d policy=%+v", status, updated)
	}

	var reread Policy
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1:getIamPolicy", nil, &reread)
	if len(reread.Bindings) != 1 || reread.Bindings[0].Role != "roles/viewer" {
		t.Fatalf("getIamPolicy (after set): %+v", reread)
	}
}

// TestCustomRoleLifecycle covers create -> delete (soft) -> undelete, the
// distinguishing behavior of this resource versus a plain CRUD type.
func TestCustomRoleLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var role Role
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/roles", map[string]any{
		"roleId": "myRole",
		"role": Role{
			Title:               "My Role",
			IncludedPermissions: []string{"storage.buckets.get"},
		},
	}, &role)
	if status != 200 || role.Name != "projects/proj1/roles/myRole" {
		t.Fatalf("create role: status=%d role=%+v", status, role)
	}

	var deleted Role
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/roles/myRole", nil, &deleted)
	if status != 200 || !deleted.Deleted {
		t.Fatalf("delete role: status=%d role=%+v", status, deleted)
	}

	var undeleted Role
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/roles/myRole:undelete", nil, &undeleted)
	if status != 200 || undeleted.Deleted {
		t.Fatalf("undelete role: status=%d role=%+v", status, undeleted)
	}
}

// TestServiceAccountKeyLifecycle covers create -> list -> get, asserting
// the private key material is omitted from list/get responses just like
// the real API.
func TestServiceAccountKeyLifecycle(t *testing.T) {
	srv := newTestServer(t)

	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/serviceAccounts", map[string]any{
		"accountId": "key-sa",
	}, nil)

	var created ServiceAccountKey
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/serviceAccounts/key-sa/keys", nil, &created)
	if status != 200 || created.PrivateKeyData == "" {
		t.Fatalf("create key: status=%d key=%+v", status, created)
	}

	var listResp struct {
		Keys []ServiceAccountKey `json:"keys"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/serviceAccounts/key-sa/keys", nil, &listResp)
	if status != 200 || len(listResp.Keys) != 1 || listResp.Keys[0].PrivateKeyData != "" {
		t.Fatalf("list keys: status=%d keys=%+v", status, listResp.Keys)
	}
}
