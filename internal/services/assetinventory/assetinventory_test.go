package assetinventory

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func newTestServer(t *testing.T) (*httptest.Server, func(bucket, key string, value any)) {
	t.Helper()
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	seed := func(bucket, key string, value any) {
		if err := db.Put(bucket, key, value); err != nil {
			t.Fatalf("seed %s/%s: %v", bucket, key, err)
		}
	}
	return srv, seed
}

func TestSearchAllResourcesAggregatesAcrossServices(t *testing.T) {
	srv, seed := newTestServer(t)

	seed(bucketComputeInstances, "us-central1-a/inst1", map[string]any{
		"name":     "inst1",
		"selfLink": "/compute/v1/projects/proj1/zones/us-central1-a/instances/inst1",
		"zone":     "us-central1-a",
	})
	seed(bucketCloudSQLInstances, "proj1/db1", map[string]any{
		"name":     "db1",
		"project":  "proj1",
		"region":   "us-central1",
		"selfLink": "/sql/v1beta4/projects/proj1/instances/db1",
	})
	seed(bucketCloudRunServices, "projects/proj1/locations/us-central1/services/svc1", map[string]any{
		"name": "projects/proj1/locations/us-central1/services/svc1",
	})
	seed(bucketPubsubTopics, "proj1/topic1", map[string]any{
		"name": "projects/proj1/topics/topic1",
	})
	seed(bucketIAMServiceAccounts, "proj1/sa1", map[string]any{
		"name":      "projects/proj1/serviceAccounts/sa1@proj1.iam.gserviceaccount.com",
		"projectId": "proj1",
		"email":     "sa1@proj1.iam.gserviceaccount.com",
	})
	// A resource in a different project must never show up when searching
	// proj1's scope.
	seed(bucketCloudSQLInstances, "proj2/db2", map[string]any{
		"name":    "db2",
		"project": "proj2",
	})

	var resp struct {
		Results   []ResourceSearchResult `json:"results"`
		TotalSize int                    `json:"totalSize"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1:searchAllResources", nil, &resp)
	if status != 200 {
		t.Fatalf("searchAllResources: want 200, got %d", status)
	}
	if resp.TotalSize != 5 || len(resp.Results) != 5 {
		t.Fatalf("expected 5 results scoped to proj1, got %d: %+v", resp.TotalSize, resp.Results)
	}
	for _, r := range resp.Results {
		if r.Project != "projects/proj1" {
			t.Fatalf("result leaked from another project: %+v", r)
		}
	}
}

func TestSearchAllResourcesFiltersByAssetTypeAndQuery(t *testing.T) {
	srv, seed := newTestServer(t)
	seed(bucketCloudRunServices, "projects/proj1/locations/us-central1/services/checkout", map[string]any{
		"name": "projects/proj1/locations/us-central1/services/checkout",
	})
	seed(bucketPubsubTopics, "proj1/orders", map[string]any{
		"name": "projects/proj1/topics/orders",
	})

	var resp struct {
		Results []ResourceSearchResult `json:"results"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1:searchAllResources?assetTypes=run.googleapis.com/Service", nil, &resp)
	if status != 200 || len(resp.Results) != 1 || resp.Results[0].AssetType != "run.googleapis.com/Service" {
		t.Fatalf("assetTypes filter: status=%d results=%+v", status, resp.Results)
	}

	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1:searchAllResources?query=order", nil, &resp)
	if status != 200 || len(resp.Results) != 1 || resp.Results[0].DisplayName != "orders" {
		t.Fatalf("query filter: status=%d results=%+v", status, resp.Results)
	}
}

func TestSearchAllResourcesEmptyProjectReturnsNoResults(t *testing.T) {
	srv, _ := newTestServer(t)
	var resp struct {
		Results   []ResourceSearchResult `json:"results"`
		TotalSize int                    `json:"totalSize"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/empty-project:searchAllResources", nil, &resp)
	if status != 200 || resp.TotalSize != 0 || len(resp.Results) != 0 {
		t.Fatalf("expected empty results for a project with no resources, got status=%d %+v", status, resp)
	}
}
