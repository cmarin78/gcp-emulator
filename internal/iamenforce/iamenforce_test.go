package iamenforce

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// okHandler is the inner handler every test wraps: it always responds 200,
// so a 403/other-than-200 response can only come from the middleware
// itself.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
})

func do(t *testing.T, h http.Handler, method, path, caller string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if caller != "" {
		req.Header.Set(CallerHeader, caller)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestNoHeaderNeverEnforced covers the core opt-in behavior: without
// CallerHeader, every request passes through untouched -- this is what
// keeps every existing gcloud/Terraform flow and the other 30+ service
// test suites unaffected by this middleware.
func TestNoHeaderNeverEnforced(t *testing.T) {
	db := testutil.NewDB(t)
	h := Middleware(db)(okHandler)

	status := do(t, h, "POST", "/v1/projects/proj1/serviceAccounts", "")
	if status != 200 {
		t.Fatalf("request without caller header: want 200, got %d", status)
	}
}

// TestReadsNeverEnforced covers that GET/HEAD requests are never enforced
// even with a caller header set and no policy granting them anything.
func TestReadsNeverEnforced(t *testing.T) {
	db := testutil.NewDB(t)
	h := Middleware(db)(okHandler)

	status := do(t, h, "GET", "/v1/projects/proj1/serviceAccounts", "user:alice@example.com")
	if status != 200 {
		t.Fatalf("GET with caller header: want 200, got %d", status)
	}
}

// TestNoPolicyImplicitAllow covers the "no policy ever set" case: a write
// request with a caller header but no stored project policy is allowed
// through (mirrors the real API's implicit project-creator-owner default,
// which this emulator doesn't otherwise model, and avoids a chicken-and-egg
// lockout before any caller has ever called setIamPolicy).
func TestNoPolicyImplicitAllow(t *testing.T) {
	db := testutil.NewDB(t)
	h := Middleware(db)(okHandler)

	status := do(t, h, "POST", "/v1/projects/proj1/serviceAccounts", "user:alice@example.com")
	if status != 200 {
		t.Fatalf("write with caller header, no policy set: want 200, got %d", status)
	}
}

// TestWriteDeniedWithoutCoveringBinding covers the actual rejection path:
// a stored policy exists, but the caller's only binding is viewer-tier
// (read-only), so a write is denied with 403 PERMISSION_DENIED.
func TestWriteDeniedWithoutCoveringBinding(t *testing.T) {
	db := testutil.NewDB(t)
	if err := db.Put("iam.policies", "proj1", map[string]any{
		"bindings": []map[string]any{
			{"role": "roles/viewer", "members": []string{"user:alice@example.com"}},
		},
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	h := Middleware(db)(okHandler)

	status := do(t, h, "POST", "/v1/projects/proj1/serviceAccounts", "user:alice@example.com")
	if status != 403 {
		t.Fatalf("write by viewer-only caller: want 403, got %d", status)
	}
}

// TestWriteAllowedWithCoveringBinding covers the success path symmetric to
// the previous test: the same caller, but bound to roles/editor, can
// perform the same write.
func TestWriteAllowedWithCoveringBinding(t *testing.T) {
	db := testutil.NewDB(t)
	if err := db.Put("iam.policies", "proj1", map[string]any{
		"bindings": []map[string]any{
			{"role": "roles/editor", "members": []string{"user:alice@example.com"}},
		},
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	h := Middleware(db)(okHandler)

	status := do(t, h, "POST", "/v1/projects/proj1/serviceAccounts", "user:alice@example.com")
	if status != 200 {
		t.Fatalf("write by editor-bound caller: want 200, got %d", status)
	}
}

// TestSetIamPolicyRequiresAdminTier covers the admin tier: an
// editor-bound caller can't call setIamPolicy (only roles/owner can), but
// the same caller with roles/owner can.
func TestSetIamPolicyRequiresAdminTier(t *testing.T) {
	db := testutil.NewDB(t)
	if err := db.Put("iam.policies", "proj1", map[string]any{
		"bindings": []map[string]any{
			{"role": "roles/editor", "members": []string{"user:alice@example.com"}},
		},
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	h := Middleware(db)(okHandler)

	status := do(t, h, "POST", "/v1/projects/proj1:setIamPolicy", "user:alice@example.com")
	if status != 403 {
		t.Fatalf("setIamPolicy by editor-only caller: want 403, got %d", status)
	}

	if err := db.Put("iam.policies", "proj1", map[string]any{
		"bindings": []map[string]any{
			{"role": "roles/owner", "members": []string{"user:alice@example.com"}},
		},
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	status = do(t, h, "POST", "/v1/projects/proj1:setIamPolicy", "user:alice@example.com")
	if status != 200 {
		t.Fatalf("setIamPolicy by owner caller: want 200, got %d", status)
	}
}

// TestUnscopedPathNeverEnforced covers paths with no "/projects/{p}/"
// segment (e.g. GCS-style bucket paths): the middleware can't extract a
// project to check a policy against, so it lets the request through
// rather than guessing -- a documented scope limit, not a silent gap.
func TestUnscopedPathNeverEnforced(t *testing.T) {
	db := testutil.NewDB(t)
	h := Middleware(db)(okHandler)

	status := do(t, h, "POST", "/storage/v1/b/my-bucket/o", "user:alice@example.com")
	if status != 200 {
		t.Fatalf("unscoped path with caller header: want 200, got %d", status)
	}
}

func TestProjectFromPath(t *testing.T) {
	cases := map[string]string{
		"/v1/projects/proj1/serviceAccounts":     "proj1",
		"/v1/projects/proj1:setIamPolicy":        "proj1",
		"/compute/v1/projects/proj1/zones/z/foo": "proj1",
		"/storage/v1/b/my-bucket/o":              "",
	}
	for path, want := range cases {
		if got := projectFromPath(path); got != want {
			t.Errorf("projectFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}
