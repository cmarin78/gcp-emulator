// Package assetinventory emulates a read-only subset of the Cloud Asset
// Inventory API (cloudasset.googleapis.com/v1): SearchAllResources. The real
// API is itself a read-only index over every other resource type in a
// project/folder/organization; this emulator follows the same shape but
// scoped to a single project, and builds its index by reading several other
// services' BoltDB buckets directly instead of importing those packages —
// the same avoid-an-import-cycle technique internal/iamenforce and
// billingbudgets/spend.go already use in this codebase. This is
// deliberately not exhaustive: it covers a representative cross-section of
// resource types (Compute instances, Cloud SQL instances, Cloud Run
// services, Pub/Sub topics, IAM service accounts) rather than every
// emulated service, the same "good enough for real Terraform/gcloud
// workflows, not a 1:1 reimplementation" scoping this project uses
// elsewhere (see spend.go's pricing table for a parallel case).
package assetinventory

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cesar/gcp-emulator/internal/activity"
	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

// Bucket names duplicated from each owning service rather than imported —
// see the package doc comment above.
const (
	bucketComputeInstances   = "compute.instances"
	bucketCloudSQLInstances  = "cloudsql.instances"
	bucketCloudRunServices   = "cloudrun.services"
	bucketPubsubTopics       = "pubsub.topics"
	bucketIAMServiceAccounts = "iam.service_accounts"
)

// ResourceSearchResult mirrors the relevant subset of cloudasset#ResourceSearchResult.
type ResourceSearchResult struct {
	Name        string `json:"name"`
	AssetType   string `json:"assetType"`
	Project     string `json:"project,omitempty"` // "projects/{projectId}", same form as the real API
	DisplayName string `json:"displayName,omitempty"`
	Location    string `json:"location,omitempty"`
}

type Service struct {
	db *storage.DB
}

func New(db *storage.DB) *Service { return &Service{db: db} }

// Register mounts Cloud Asset Inventory's routes, following the real path
// of cloudasset.googleapis.com/v1. Scope is restricted to "projects/{id}"
// (the real API also accepts folders/organizations, out of scope here
// since this emulator has no folder/org hierarchy).
func (s *Service) Register(mux *http.ServeMux) {
	// Go's ServeMux wildcards must be an entire path segment, so the real
	// API's "{project}:searchAllResources" verb suffix is split out inside
	// the handler via strings.Cut, same pattern as Cloud Tasks'
	// queueAction (":pause"/":resume") and Cloud Scheduler/Secret Manager.
	mux.HandleFunc("GET /v1/projects/{projectAction}", s.searchAllResources)
}

func (s *Service) searchAllResources(w http.ResponseWriter, r *http.Request) {
	project, action, ok := strings.Cut(r.PathValue("projectAction"), ":")
	if !ok || action != "searchAllResources" {
		server.WriteError(w, 404, "NOT_FOUND", "ruta no encontrada")
		return
	}
	query := strings.ToLower(r.URL.Query().Get("query"))
	wantTypes := map[string]bool{}
	for _, t := range r.URL.Query()["assetTypes"] {
		wantTypes[t] = true
	}

	results := []ResourceSearchResult{}
	collect := func(res ResourceSearchResult) {
		if res.Project != "" && res.Project != "projects/"+project {
			return
		}
		if len(wantTypes) > 0 && !wantTypes[res.AssetType] {
			return
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(res.Name), query) &&
			!strings.Contains(strings.ToLower(res.DisplayName), query) {
			return
		}
		results = append(results, res)
	}

	s.collectComputeInstances(collect)
	s.collectCloudSQLInstances(collect)
	s.collectCloudRunServices(collect)
	s.collectPubsubTopics(collect)
	s.collectServiceAccounts(collect)

	server.WriteJSON(w, 200, map[string]any{"results": results, "totalSize": len(results)})
}

// projectOfSelfLink extracts {project} from a selfLink of the form
// "(.../)projects/{project}/...". Duplicated from
// billingbudgets/spend.go's projectOfSelfLink (same technique, different
// package, to avoid an import between two leaf service packages).
func projectOfSelfLink(selfLink string) string {
	const marker = "/projects/"
	idx := strings.Index(selfLink, marker)
	if idx < 0 {
		return ""
	}
	rest := selfLink[idx+len(marker):]
	if end := strings.IndexByte(rest, '/'); end >= 0 {
		return rest[:end]
	}
	return rest
}

func (s *Service) collectComputeInstances(collect func(ResourceSearchResult)) {
	_ = s.db.List(bucketComputeInstances, "", func(_ string, raw []byte) error {
		var inst struct {
			Name        string `json:"name"`
			SelfLink    string `json:"selfLink"`
			Zone        string `json:"zone"`
			MachineType string `json:"machineType"`
		}
		if err := json.Unmarshal(raw, &inst); err != nil {
			return nil
		}
		project := projectOfSelfLink(inst.SelfLink)
		if project == "" {
			return nil
		}
		collect(ResourceSearchResult{
			Name:        inst.SelfLink,
			AssetType:   "compute.googleapis.com/Instance",
			Project:     "projects/" + project,
			DisplayName: inst.Name,
			Location:    inst.Zone,
		})
		return nil
	})
}

func (s *Service) collectCloudSQLInstances(collect func(ResourceSearchResult)) {
	_ = s.db.List(bucketCloudSQLInstances, "", func(_ string, raw []byte) error {
		var inst struct {
			Name     string `json:"name"`
			Project  string `json:"project"`
			Region   string `json:"region"`
			SelfLink string `json:"selfLink"`
		}
		if err := json.Unmarshal(raw, &inst); err != nil {
			return nil
		}
		if inst.Project == "" {
			return nil
		}
		collect(ResourceSearchResult{
			Name:        inst.SelfLink,
			AssetType:   "sqladmin.googleapis.com/Instance",
			Project:     "projects/" + inst.Project,
			DisplayName: inst.Name,
			Location:    inst.Region,
		})
		return nil
	})
}

func (s *Service) collectCloudRunServices(collect func(ResourceSearchResult)) {
	_ = s.db.List(bucketCloudRunServices, "", func(key string, raw []byte) error {
		var svc struct {
			Name string `json:"name"` // projects/{p}/locations/{l}/services/{s}
		}
		if err := json.Unmarshal(raw, &svc); err != nil {
			return nil
		}
		project := activity.ProjectOf(svc.Name)
		if project == "" {
			return nil
		}
		collect(ResourceSearchResult{
			Name:        svc.Name,
			AssetType:   "run.googleapis.com/Service",
			Project:     "projects/" + project,
			DisplayName: lastSegment(svc.Name),
			Location:    locationOf(svc.Name),
		})
		return nil
	})
}

func (s *Service) collectPubsubTopics(collect func(ResourceSearchResult)) {
	_ = s.db.List(bucketPubsubTopics, "", func(key string, raw []byte) error {
		var t struct {
			Name string `json:"name"` // projects/{p}/topics/{t}
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil
		}
		project := activity.ProjectOf(t.Name)
		if project == "" {
			return nil
		}
		collect(ResourceSearchResult{
			Name:        t.Name,
			AssetType:   "pubsub.googleapis.com/Topic",
			Project:     "projects/" + project,
			DisplayName: lastSegment(t.Name),
		})
		return nil
	})
}

func (s *Service) collectServiceAccounts(collect func(ResourceSearchResult)) {
	_ = s.db.List(bucketIAMServiceAccounts, "", func(key string, raw []byte) error {
		var sa struct {
			Name      string `json:"name"`
			ProjectID string `json:"projectId"`
			Email     string `json:"email"`
		}
		if err := json.Unmarshal(raw, &sa); err != nil {
			return nil
		}
		if sa.ProjectID == "" {
			return nil
		}
		collect(ResourceSearchResult{
			Name:        sa.Name,
			AssetType:   "iam.googleapis.com/ServiceAccount",
			Project:     "projects/" + sa.ProjectID,
			DisplayName: sa.Email,
		})
		return nil
	})
}

func lastSegment(name string) string {
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

// locationOf extracts {location} from "projects/{p}/locations/{l}/...".
func locationOf(name string) string {
	const marker = "/locations/"
	idx := strings.Index(name, marker)
	if idx < 0 {
		return ""
	}
	rest := name[idx+len(marker):]
	if end := strings.IndexByte(rest, '/'); end >= 0 {
		return rest[:end]
	}
	return rest
}
