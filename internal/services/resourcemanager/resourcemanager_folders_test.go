package resourcemanager

import (
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// newTestServer is defined in resourcemanager_test.go (same package).

func TestFolderLifecycle(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v3/folders"

	// Create.
	var op Operation
	status := testutil.DoJSON(t, "POST", base, map[string]any{
		"parent":      "organizations/123456",
		"displayName": "Engineering",
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create folder: status=%d op=%+v", status, op)
	}

	// Decode folder from operation response.
	var folder Folder
	if err := op.Response.UnmarshalJSON(op.Response); err == nil {
	}
	testutil.DoJSON(t, "POST", base, map[string]any{
		"parent":      "organizations/123456",
		"displayName": "Engineering",
	}, &op)
	// Use list to get the folder name.
	var list struct {
		Folders []Folder `json:"folders"`
	}
	testutil.DoJSON(t, "GET", base, nil, &list)
	if len(list.Folders) == 0 {
		t.Fatalf("list folders: expected at least 1, got 0")
	}
	folder = list.Folders[0]
	if folder.Name == "" || folder.State != "ACTIVE" {
		t.Fatalf("folder state: %+v", folder)
	}

	// Get by ID (strip "folders/" prefix).
	folderID := folder.Name[len("folders/"):]
	var got Folder
	status = testutil.DoJSON(t, "GET", base+"/"+folderID, nil, &got)
	if status != 200 || got.DisplayName == "" {
		t.Fatalf("get folder: status=%d folder=%+v", status, got)
	}

	// Patch.
	status = testutil.DoJSON(t, "PATCH", base+"/"+folderID, map[string]any{
		"displayName": "Engineering-Updated",
	}, &got)
	if status != 200 || got.DisplayName != "Engineering-Updated" {
		t.Fatalf("patch folder: status=%d folder=%+v", status, got)
	}

	// Delete (soft delete).
	status = testutil.DoJSON(t, "DELETE", base+"/"+folderID, nil, &op)
	if status != 200 || !op.Done {
		t.Fatalf("delete folder: status=%d op=%+v", status, op)
	}
	// After delete, state should be DELETE_REQUESTED.
	testutil.DoJSON(t, "GET", base+"/"+folderID, nil, &got)
	if got.State != "DELETE_REQUESTED" {
		t.Fatalf("folder state after delete: want DELETE_REQUESTED, got %q", got.State)
	}
}

func TestFolderRequiresParentAndDisplayName(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v3/folders"

	// Missing parent.
	status := testutil.DoJSON(t, "POST", base, map[string]any{"displayName": "Ops"}, nil)
	if status != 400 {
		t.Fatalf("folder no parent: want 400, got %d", status)
	}

	// Missing displayName.
	status = testutil.DoJSON(t, "POST", base, map[string]any{"parent": "organizations/1"}, nil)
	if status != 400 {
		t.Fatalf("folder no displayName: want 400, got %d", status)
	}
}

func TestFolderGetNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", srv.URL+"/v3/folders/99999999", nil, nil)
	if status != 404 {
		t.Fatalf("get nonexistent folder: want 404, got %d", status)
	}
}

func TestFolderListFiltersByParent(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v3/folders"

	testutil.DoJSON(t, "POST", base, map[string]any{"parent": "organizations/111", "displayName": "A"}, nil)
	testutil.DoJSON(t, "POST", base, map[string]any{"parent": "organizations/111", "displayName": "B"}, nil)
	testutil.DoJSON(t, "POST", base, map[string]any{"parent": "organizations/222", "displayName": "C"}, nil)

	var list struct {
		Folders []Folder `json:"folders"`
	}
	testutil.DoJSON(t, "GET", base+"?parent=organizations/111", nil, &list)
	if len(list.Folders) != 2 {
		t.Fatalf("list by parent: want 2, got %d: %+v", len(list.Folders), list.Folders)
	}
	testutil.DoJSON(t, "GET", base+"?parent=organizations/222", nil, &list)
	if len(list.Folders) != 1 {
		t.Fatalf("list other parent: want 1, got %d", len(list.Folders))
	}
}

func TestFolderIAMPolicy(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v3/folders"

	// Create a folder.
	var op Operation
	testutil.DoJSON(t, "POST", base, map[string]any{
		"parent":      "organizations/100",
		"displayName": "IAM-Test",
	}, &op)
	var list struct {
		Folders []Folder `json:"folders"`
	}
	testutil.DoJSON(t, "GET", base, nil, &list)
	folderID := list.Folders[len(list.Folders)-1].Name[len("folders/"):]

	// Get IAM policy (empty).
	var policy FolderIAMPolicy
	status := testutil.DoJSON(t, "POST", base+"/"+folderID+":getIamPolicy", map[string]any{}, &policy)
	if status != 200 {
		t.Fatalf("getIamPolicy: want 200, got %d", status)
	}

	// Set IAM policy.
	var updated FolderIAMPolicy
	status = testutil.DoJSON(t, "POST", base+"/"+folderID+":setIamPolicy", map[string]any{
		"policy": map[string]any{
			"bindings": []map[string]any{
				{
					"role":    "roles/resourcemanager.folderAdmin",
					"members": []string{"user:admin@example.com"},
				},
			},
		},
	}, &updated)
	if status != 200 || len(updated.Bindings) != 1 {
		t.Fatalf("setIamPolicy: status=%d policy=%+v", status, updated)
	}

	// Get again — should reflect the set policy.
	testutil.DoJSON(t, "POST", base+"/"+folderID+":getIamPolicy", map[string]any{}, &policy)
	if len(policy.Bindings) != 1 || policy.Bindings[0].Role != "roles/resourcemanager.folderAdmin" {
		t.Fatalf("getIamPolicy after set: %+v", policy)
	}
}
