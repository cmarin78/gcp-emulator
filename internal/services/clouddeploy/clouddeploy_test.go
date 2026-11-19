package clouddeploy

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

func TestCloudDeployPipelinesAndTargets(t *testing.T) {
	srv := newTestServer(t)

	// 1. Create Target
	var tgtOp Operation
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/my-proj/locations/us-central1/targets?targetId=prod-gke", map[string]any{
		"description": "Production GKE cluster",
		"gke": map[string]any{
			"cluster": "projects/my-proj/locations/us-central1/clusters/prod-cluster",
		},
	}, &tgtOp)
	if !tgtOp.Done {
		t.Fatal("expected create target op to be done")
	}

	// 2. Get Target
	var tgt Target
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/locations/us-central1/targets/prod-gke", nil, &tgt)
	if tgt.Description != "Production GKE cluster" || tgt.Gke == nil {
		t.Fatalf("unexpected target: %+v", tgt)
	}

	// 3. Create Delivery Pipeline
	var pipeOp Operation
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/my-proj/locations/us-central1/deliveryPipelines?deliveryPipelineId=my-pipeline", map[string]any{
		"description": "Standard delivery pipeline",
		"serialPipeline": map[string]any{
			"stages": []map[string]any{
				{"targetId": "prod-gke"},
			},
		},
	}, &pipeOp)
	if !pipeOp.Done {
		t.Fatal("expected create pipeline op to be done")
	}

	// 4. Get Delivery Pipeline
	var pipe DeliveryPipeline
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/locations/us-central1/deliveryPipelines/my-pipeline", nil, &pipe)
	if pipe.Description != "Standard delivery pipeline" {
		t.Fatalf("unexpected pipeline description: %s", pipe.Description)
	}

	// 5. Delete Delivery Pipeline
	testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/my-proj/locations/us-central1/deliveryPipelines/my-pipeline", nil, &pipeOp)
	status := testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/my-proj/locations/us-central1/deliveryPipelines/my-pipeline", nil, nil)
	if status != 404 {
		t.Fatalf("expected 404 for deleted pipeline, got %d", status)
	}
}
