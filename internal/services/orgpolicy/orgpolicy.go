// Package orgpolicy emulates a subset of the Organization Policy API
// (orgpolicy.googleapis.com/v2): policies attached to a project (the
// org/folder resource container variants are not modeled, only projects --
// the same scoping this codebase already applies elsewhere, e.g.
// resourcemanager only models projects, not folders/orgs). Unlike most
// other newer GCP APIs, org policy CRUD is synchronous in the real API (no
// Operation wrapper), so handlers here return the Policy resource directly.
package orgpolicy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketPolicies = "orgpolicy.policies"

// PolicySpec mirrors orgpolicy#PolicySpec (the "spec" applied at this
// resource, as opposed to "dryRunSpec").
type PolicySpec struct {
	Rules             []PolicyRule `json:"rules,omitempty"`
	InheritFromParent bool         `json:"inheritFromParent,omitempty"`
	Reset             bool         `json:"reset,omitempty"`
	Etag              string       `json:"etag,omitempty"`
}

// PolicyRule mirrors orgpolicy#PolicyRule.
type PolicyRule struct {
	Values    *StringValues  `json:"values,omitempty"`
	AllowAll  bool           `json:"allowAll,omitempty"`
	DenyAll   bool           `json:"denyAll,omitempty"`
	Enforce   bool           `json:"enforce,omitempty"`
	Condition map[string]any `json:"condition,omitempty"`
}

// StringValues mirrors orgpolicy#StringValues.
type StringValues struct {
	AllowedValues []string `json:"allowedValues,omitempty"`
	DeniedValues  []string `json:"deniedValues,omitempty"`
}

// Policy mirrors the relevant subset of orgpolicy#Policy.
type Policy struct {
	Name string      `json:"name"` // projects/{project}/policies/{constraint}
	Spec *PolicySpec `json:"spec,omitempty"`
}

type Service struct {
	db *storage.DB
}

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v2/projects/{project}/policies", s.createPolicy)
	mux.HandleFunc("GET /v2/projects/{project}/policies", s.listPolicies)
	mux.HandleFunc("GET /v2/projects/{project}/policies/{policy}", s.getPolicy)
	mux.HandleFunc("PATCH /v2/projects/{project}/policies/{policy}", s.updatePolicy)
	mux.HandleFunc("DELETE /v2/projects/{project}/policies/{policy}", s.deletePolicy)
}

func policyKey(project, policy string) string { return project + "/" + policy }

func policyName(project, policy string) string {
	return fmt.Sprintf("projects/%s/policies/%s", project, policy)
}

// constraintFromName extracts the trailing constraint id from a policy's
// full resource name ("projects/p/policies/compute.foo" -> "compute.foo").
func constraintFromName(name string) string {
	idx := strings.LastIndex(name, "/policies/")
	if idx == -1 {
		return name
	}
	return name[idx+len("/policies/"):]
}

func (s *Service) createPolicy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var policy Policy
	if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if policy.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "policy.name is required")
		return
	}
	constraint := constraintFromName(policy.Name)
	policy.Name = policyName(project, constraint)

	var existing Policy
	found, err := s.db.Get(bucketPolicies, policyKey(project, constraint), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if found {
		server.WriteError(w, 409, "ALREADY_EXISTS", "policy already exists: "+policy.Name)
		return
	}
	if err := s.db.Put(bucketPolicies, policyKey(project, constraint), policy); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, policy)
}

func (s *Service) listPolicies(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	items := []Policy{}
	err := s.db.List(bucketPolicies, project+"/", func(key string, raw []byte) error {
		var p Policy
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		items = append(items, p)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{"policies": items})
}

func (s *Service) getPolicy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	constraint := r.PathValue("policy")
	var policy Policy
	found, err := s.db.Get(bucketPolicies, policyKey(project, constraint), &policy)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "policy not found")
		return
	}
	server.WriteJSON(w, 200, policy)
}

func (s *Service) updatePolicy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	constraint := r.PathValue("policy")
	var existing Policy
	found, err := s.db.Get(bucketPolicies, policyKey(project, constraint), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "policy not found")
		return
	}
	var body Policy
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Spec != nil {
		existing.Spec = body.Spec
	}
	if err := s.db.Put(bucketPolicies, policyKey(project, constraint), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Service) deletePolicy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	constraint := r.PathValue("policy")
	if err := s.db.Delete(bucketPolicies, policyKey(project, constraint)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}

// Denies reports whether the given boolean-style constraint (e.g.
// "iam.disableServiceAccountKeyCreation", "compute.vmExternalIpAccess") is
// currently enforced as a deny for project. A rule counts as a deny if it
// sets DenyAll, or if it sets Enforce without AllowAll -- the two shapes a
// real client uses to turn a boolean constraint "on" (gcloud's
// `--enforce`/`resource-manager org-policies enable-enforce` and the
// Terraform google_org_policy_policy boolean_policy both end up writing one
// of these). If no policy was ever set for the constraint, this returns
// false: an unset constraint doesn't restrict anything in the real API
// either, and a handful of concrete handlers (service account key
// creation, Compute external IP) call this directly to become real,
// testable enforcement instead of a generic policy interpreter.
func Denies(db *storage.DB, project, constraint string) bool {
	var p Policy
	found, err := db.Get(bucketPolicies, policyKey(project, constraint), &p)
	if err != nil || !found || p.Spec == nil {
		return false
	}
	for _, rule := range p.Spec.Rules {
		if rule.DenyAll {
			return true
		}
		if rule.Enforce && !rule.AllowAll {
			return true
		}
	}
	return false
}
