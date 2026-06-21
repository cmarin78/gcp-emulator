package realbackend

import "net/http"

// OptInLabel is the resource-body label key per-resource opt-in looks
// for, e.g. {"labels": {"emulator.dev/backend": "real"}} on a create
// request — the same place Terraform already puts ordinary GCP labels,
// so no new request shape is needed for a resource that already has a
// labels map.
const OptInLabel = "emulator.dev/backend"

// OptInQueryParam is the alternate, body-shape-independent way to opt
// in: ?backend=real on the create request itself. Useful for resources
// (or callers) that don't carry a labels map at all.
const OptInQueryParam = "backend"

// OptInValue is the only value either mechanism recognizes as "yes,
// please use a real backend if one is available." Anything else,
// including absence, keeps today's zero-cost shape-only behavior — no
// existing caller (gcloud, Terraform, the more-than-30 existing test
// packages) sends either of these, so nothing changes for them.
const OptInValue = "real"

// WantsReal reports whether r's query string and/or labels request a
// real backend. Either nil is treated as "absent" rather than panicking,
// so callers can pass whichever of the two a given resource shape
// actually has.
func WantsReal(r *http.Request, labels map[string]string) bool {
	if r != nil && r.URL.Query().Get(OptInQueryParam) == OptInValue {
		return true
	}
	if labels != nil && labels[OptInLabel] == OptInValue {
		return true
	}
	return false
}
