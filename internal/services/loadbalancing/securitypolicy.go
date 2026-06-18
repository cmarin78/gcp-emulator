// Phase 8 of the roadmap: Cloud Armor, modeled as the global
// compute#securityPolicy resource. Direct extension of Load Balancing —
// backendServices can reference a securityPolicy by selfLink (see the
// SecurityPolicy field on BackendService in loadbalancing.go). Same
// "shape-compatible, not behavior-complete" approach as the rest of this
// package: no real traffic inspection or rule evaluation, just the
// resource graph Terraform/gcloud expect.
package loadbalancing

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

// fakeFingerprint produces a syntactically valid base64 fingerprint, same
// approach as compute.go's helper of the same name (duplicated here per
// this package's existing convention of not importing the compute
// package). Real clients — gcloud in particular — decode fingerprint
// fields as base64 and crash with "Incorrect padding" on anything else.
func fakeFingerprint(seed string) string {
	return base64.StdEncoding.EncodeToString([]byte(seed))
}

const bucketSecurityPolicies = "loadbalancing.securityPolicies"

// SecurityPolicyRule mirrors the real API's nested rule shape.
type SecurityPolicyRule struct {
	Priority    int64           `json:"priority"`
	Action      string          `json:"action"`
	Description string          `json:"description,omitempty"`
	Match       json.RawMessage `json:"match,omitempty"`
}

// SecurityPolicy mirrors the real compute#securityPolicy resource (global).
type SecurityPolicy struct {
	ID                string               `json:"id"`
	Name              string               `json:"name"`
	Description       string               `json:"description,omitempty"`
	Type              string               `json:"type,omitempty"`
	Rules             []SecurityPolicyRule `json:"rules,omitempty"`
	Fingerprint       string               `json:"fingerprint,omitempty"`
	CreationTimestamp string               `json:"creationTimestamp"`
	SelfLink          string               `json:"selfLink"`
}

// normalizeSecurityPolicyRef accepts a short name or an already-complete
// selfLink/URL and always returns a full reference relative to the global
// securityPolicies collection (same convention as compute/network.go's
// normalizeGlobalRef, duplicated here per this package's existing pattern
// of not importing helpers from the compute package).
func normalizeSecurityPolicyRef(project, ref string) string {
	if ref == "" {
		return ""
	}
	if strings.Contains(ref, "/") {
		return ref
	}
	return selfLink(project, "securityPolicies", ref)
}

func (s *Service) insertSecurityPolicy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	var body struct {
		Name        string               `json:"name"`
		Description string               `json:"description"`
		Type        string               `json:"type"`
		Rules       []SecurityPolicyRule `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.Name == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "name is required")
		return
	}
	sp := SecurityPolicy{
		ID:                fmt.Sprintf("%d", s.nextSeq()),
		Name:              body.Name,
		Description:       body.Description,
		Type:              orDefault(body.Type, "CLOUD_ARMOR"),
		Rules:             body.Rules,
		Fingerprint:       fakeFingerprint(fmt.Sprintf("%s-%d", body.Name, time.Now().UnixNano())),
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		SelfLink:          selfLink(project, "securityPolicies", body.Name),
	}
	if err := s.db.Put(bucketSecurityPolicies, sp.Name, sp); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("insert", sp.SelfLink, opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

func (s *Service) listSecurityPolicies(w http.ResponseWriter, r *http.Request) {
	items := []SecurityPolicy{}
	_ = s.db.List(bucketSecurityPolicies, "", func(key string, raw []byte) error {
		var sp SecurityPolicy
		if err := json.Unmarshal(raw, &sp); err != nil {
			return err
		}
		items = append(items, sp)
		return nil
	})
	server.WriteJSON(w, 200, map[string]any{"kind": "compute#securityPolicyList", "items": items})
}

func (s *Service) getSecurityPolicy(w http.ResponseWriter, r *http.Request) {
	var sp SecurityPolicy
	found, err := s.db.Get(bucketSecurityPolicies, r.PathValue("securityPolicy"), &sp)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "security policy not found")
		return
	}
	server.WriteJSON(w, 200, sp)
}

func (s *Service) deleteSecurityPolicy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("securityPolicy")
	if err := s.db.Delete(bucketSecurityPolicies, name); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("delete", selfLink(project, "securityPolicies", name), opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

// setSecurityPolicyLabels handles the setLabels action that Terraform's
// google_compute_security_policy resource always calls after create/update
// to apply effective_labels/terraform_labels. This emulator doesn't track
// labels on security policies, so this is a no-op that just refreshes the
// fingerprint (real setLabels calls require a matching labelFingerprint and
// bump it on success) and returns a DONE operation — enough to satisfy the
// provider without modeling label storage.
func (s *Service) setSecurityPolicyLabels(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	name := r.PathValue("securityPolicy")
	var sp SecurityPolicy
	found, err := s.db.Get(bucketSecurityPolicies, name, &sp)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "security policy not found")
		return
	}
	sp.Fingerprint = fakeFingerprint(fmt.Sprintf("%s-%d", name, time.Now().UnixNano()))
	if err := s.db.Put(bucketSecurityPolicies, sp.Name, sp); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	op := s.ops.Done("setLabels", sp.SelfLink, opsCollection(r, project))
	server.WriteJSON(w, 200, op)
}

// orDefault returns def when v is empty, same small helper duplicated
// across packages in this project (see compute.go's version).
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
