// Package iamenforce adds an opt-in, project-level IAM enforcement layer on
// top of every registered service: when a request carries the
// X-Emulator-Caller header (the emulator's stand-in for a real, verifiable
// caller identity -- gcloud/Terraform never send this, so by default
// nothing changes for them), mutating requests get checked against the
// calling project's IAM policy (the same policy iam.googleapis.com's
// setIamPolicy/getIamPolicy already store, see internal/services/iam) and
// rejected with a real-shaped 403 PERMISSION_DENIED when no binding covers
// the caller for the tier the request needs.
//
// This intentionally stays opt-in rather than a default-deny global gate:
// every other service in this emulator was built, and is still tested,
// against a server that accepts all requests unconditionally (there's no
// real OAuth flow to verify a caller's identity against a local fake API
// server). Flipping that to default-deny would break every existing
// client/test rather than adding a genuine capability. Opt-in via a header
// keeps both true: a caller that wants to exercise real enforcement (a
// test, or a deliberately security-conscious client/script) gets a real
// decision; every other client is completely unaffected.
package iamenforce

import (
	"net/http"
	"strings"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

// CallerHeader is the opt-in header carrying the calling member, in the
// same "type:id" shape real IAM policy bindings use (e.g.
// "user:alice@example.com", "serviceAccount:x@proj.iam.gserviceaccount.com").
// Requests without this header are never enforced.
const CallerHeader = "X-Emulator-Caller"

// bucketPolicies must match internal/services/iam's bucketPolicies bucket
// name and key convention exactly, since this package reads the same
// stored project policies without importing the iam package (avoiding any
// risk of an import cycle, the same technique internal/activity used for
// internal/services/logging in the previous Phase 11 slice).
const bucketPolicies = "iam.policies"

// policy/binding mirror iam.Policy/iam.Binding's JSON shape -- a local,
// minimal copy rather than an import of internal/services/iam.
type policy struct {
	Bindings []binding `json:"bindings"`
}

type binding struct {
	Role    string   `json:"role"`
	Members []string `json:"members"`
}

// tier ranks the permission level a request needs, and the level a role
// grants; a role satisfies a request when its tier is >= the request's.
type tier int

const (
	tierNone  tier = iota // reads: never enforced
	tierWrite             // POST/PUT/PATCH/DELETE
	tierAdmin             // :setIamPolicy and equivalent
)

// roleTier approximates each role's permission ceiling. This is a
// shape-level simplification (the real API has much more granular
// per-permission semantics) consistent with this project's existing
// "close enough to be useful, not a full reimplementation" choices, e.g.
// predefined service-admin roles (roles/storage.admin, ...) and custom
// roles (projects/{p}/roles/{id}) are all approximated as write-tier.
func roleTier(role string) tier {
	switch role {
	case "roles/owner":
		return tierAdmin
	case "roles/viewer":
		return tierNone
	default:
		return tierWrite
	}
}

// requiredTier derives the permission tier a request needs from its method
// and path: GET/HEAD are reads and are never enforced; any other verb is
// write-tier; an explicit ":setIamPolicy" action is admin-tier.
func requiredTier(method, path string) tier {
	if strings.Contains(path, ":setIamPolicy") {
		return tierAdmin
	}
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return tierWrite
	default:
		return tierNone
	}
}

// projectFromPath extracts {project} out of a path containing
// ".../projects/{project}/..." or ending in ".../projects/{project}" --
// covers the large majority of registered routes (IAM, Compute, Pub/Sub,
// Cloud SQL, Scheduler/Tasks, ...). Paths that don't follow this
// convention (e.g. GCS bucket paths keyed by bucket name, with project
// only as a query param) aren't enforced by this middleware -- a known,
// documented scope limit rather than a silent gap.
func projectFromPath(path string) string {
	const marker = "/projects/"
	idx := strings.Index(path, marker)
	if idx < 0 {
		return ""
	}
	rest := path[idx+len(marker):]
	if end := strings.IndexByte(rest, '/'); end >= 0 {
		return rest[:end]
	}
	if cut, _, ok := strings.Cut(rest, ":"); ok {
		return cut
	}
	return rest
}

// satisfies reports whether caller has a binding in p covering at least
// need.
func satisfies(p policy, caller string, need tier) bool {
	if need == tierNone {
		return true
	}
	for _, b := range p.Bindings {
		if roleTier(b.Role) < need {
			continue
		}
		for _, m := range b.Members {
			if m == caller || m == "allUsers" || m == "allAuthenticatedUsers" {
				return true
			}
		}
	}
	return false
}

// Middleware wraps next with opt-in, project-level IAM enforcement. See the
// package doc for why this stays opt-in instead of a default-deny gate.
func Middleware(db *storage.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := r.Header.Get(CallerHeader)
			if caller == "" {
				next.ServeHTTP(w, r)
				return
			}
			need := requiredTier(r.Method, r.URL.Path)
			if need == tierNone {
				next.ServeHTTP(w, r)
				return
			}
			project := projectFromPath(r.URL.Path)
			if project == "" {
				next.ServeHTTP(w, r)
				return
			}
			var p policy
			found, err := db.Get(bucketPolicies, project, &p)
			if err != nil || !found {
				// No policy was ever set for this project: matches the
				// real-world default of an implicit project-creator owner
				// the emulator doesn't otherwise model -- enforcing here
				// would lock every caller out before they could ever call
				// setIamPolicy in the first place.
				next.ServeHTTP(w, r)
				return
			}
			if satisfies(p, caller, need) {
				next.ServeHTTP(w, r)
				return
			}
			server.WriteError(w, 403, "PERMISSION_DENIED",
				"el caller "+caller+" no tiene un rol vinculado en el proyecto "+project+" que cubra esta acción")
		})
	}
}
