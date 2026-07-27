package vertexai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func modelsBase(srvURL string) string {
	return srvURL + "/aiplatform/v1/projects/proj1/locations/us-central1"
}

// ── Model tests ───────────────────────────────────────────────────────────────

func TestModelUploadAndGet(t *testing.T) {
	srv := newTestServer(t)
	base := modelsBase(srv.URL)

	// Upload a model.
	var op Operation
	status := testutil.DoJSON(t, "POST", base+"/models:upload", map[string]any{
		"model": map[string]any{
			"displayName":       "my-model",
			"metadataSchemaUri": "gs://google-cloud-aiplatform/schema/trainingjob/metadata/automl_image_classification_1.0.0.yaml",
		},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("upload model: status=%d op=%+v", status, op)
	}
	if op.Name == "" {
		t.Fatal("upload model: missing operation name")
	}

	// Extract model ID from the operation name.
	parts := strings.Split(op.Name, "/")
	opID := parts[len(parts)-1]
	modelID := strings.TrimPrefix(opID, "op-")

	// Get by ID.
	var m Model
	status = testutil.DoJSON(t, "GET", base+"/models/"+modelID, nil, &m)
	if status != 200 {
		t.Fatalf("get model: want 200, got %d", status)
	}
	if m.DisplayName != "my-model" {
		t.Fatalf("get model: displayName=%q", m.DisplayName)
	}
	if m.VersionID != "1" {
		t.Fatalf("get model: versionId=%q, want 1", m.VersionID)
	}
	if !strings.HasPrefix(m.Name, "projects/proj1/locations/us-central1/models/") {
		t.Fatalf("get model: unexpected name %q", m.Name)
	}
}

func TestModelList(t *testing.T) {
	srv := newTestServer(t)
	base := modelsBase(srv.URL)

	testutil.DoJSON(t, "POST", base+"/models:upload", map[string]any{"model": map[string]any{"displayName": "m1"}}, nil)
	testutil.DoJSON(t, "POST", base+"/models:upload", map[string]any{"model": map[string]any{"displayName": "m2"}}, nil)

	var list struct {
		Models []Model `json:"models"`
	}
	status := testutil.DoJSON(t, "GET", base+"/models", nil, &list)
	if status != 200 {
		t.Fatalf("list models: want 200, got %d", status)
	}
	if len(list.Models) != 2 {
		t.Fatalf("list models: want 2, got %d", len(list.Models))
	}
}

func TestModelPatch(t *testing.T) {
	srv := newTestServer(t)
	base := modelsBase(srv.URL)

	var op Operation
	testutil.DoJSON(t, "POST", base+"/models:upload", map[string]any{"model": map[string]any{"displayName": "original"}}, &op)
	parts := strings.Split(op.Name, "/")
	modelID := strings.TrimPrefix(parts[len(parts)-1], "op-")

	var m Model
	status := testutil.DoJSON(t, "PATCH", base+"/models/"+modelID, map[string]any{
		"displayName": "updated",
		"labels":      map[string]string{"env": "test"},
	}, &m)
	if status != 200 {
		t.Fatalf("patch model: want 200, got %d", status)
	}
	if m.DisplayName != "updated" {
		t.Fatalf("patch model: displayName=%q", m.DisplayName)
	}
	if m.Labels["env"] != "test" {
		t.Fatalf("patch model: labels=%v", m.Labels)
	}
}

func TestModelDelete(t *testing.T) {
	srv := newTestServer(t)
	base := modelsBase(srv.URL)

	var op Operation
	testutil.DoJSON(t, "POST", base+"/models:upload", map[string]any{"model": map[string]any{"displayName": "to-delete"}}, &op)
	parts := strings.Split(op.Name, "/")
	modelID := strings.TrimPrefix(parts[len(parts)-1], "op-")

	status := testutil.DoJSON(t, "DELETE", base+"/models/"+modelID, nil, &op)
	if status != 200 || !op.Done {
		t.Fatalf("delete model: want 200+done, got status=%d", status)
	}

	// Gone.
	status = testutil.DoJSON(t, "GET", base+"/models/"+modelID, nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestModelGetNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", modelsBase(srv.URL)+"/models/99999", nil, nil)
	if status != 404 {
		t.Fatalf("get nonexistent model: want 404, got %d", status)
	}
}

// ── Endpoint tests ────────────────────────────────────────────────────────────

func TestEndpointLifecycle(t *testing.T) {
	srv := newTestServer(t)
	base := modelsBase(srv.URL)

	// Create.
	var op Operation
	status := testutil.DoJSON(t, "POST", base+"/endpoints", map[string]any{
		"displayName": "my-endpoint",
		"labels":      map[string]string{"team": "ml"},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("create endpoint: status=%d op=%+v", status, op)
	}

	// Decode endpoint from operation response.
	var ep Endpoint
	if err := op.Response.UnmarshalJSON(op.Response); err == nil {
	}
	// Use list to get the endpoint.
	var list struct {
		Endpoints []Endpoint `json:"endpoints"`
	}
	testutil.DoJSON(t, "GET", base+"/endpoints", nil, &list)
	if len(list.Endpoints) != 1 {
		t.Fatalf("list endpoints: want 1, got %d", len(list.Endpoints))
	}
	ep = list.Endpoints[0]
	epID := ep.Name[strings.LastIndex(ep.Name, "/")+1:]

	// Get.
	var got Endpoint
	status = testutil.DoJSON(t, "GET", base+"/endpoints/"+epID, nil, &got)
	if status != 200 {
		t.Fatalf("get endpoint: want 200, got %d", status)
	}
	if got.DisplayName != "my-endpoint" || got.Labels["team"] != "ml" {
		t.Fatalf("get endpoint: unexpected %+v", got)
	}

	// Patch.
	status = testutil.DoJSON(t, "PATCH", base+"/endpoints/"+epID, map[string]any{
		"displayName": "my-endpoint-v2",
	}, &got)
	if status != 200 || got.DisplayName != "my-endpoint-v2" {
		t.Fatalf("patch endpoint: status=%d got=%+v", status, got)
	}

	// Delete.
	status = testutil.DoJSON(t, "DELETE", base+"/endpoints/"+epID, nil, &op)
	if status != 200 || !op.Done {
		t.Fatalf("delete endpoint: status=%d op=%+v", status, op)
	}

	// Gone.
	status = testutil.DoJSON(t, "GET", base+"/endpoints/"+epID, nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestEndpointRequiresDisplayName(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", modelsBase(srv.URL)+"/endpoints", map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without displayName: want 400, got %d", status)
	}
}

func TestEndpointGetNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "GET", modelsBase(srv.URL)+"/endpoints/99999", nil, nil)
	if status != 404 {
		t.Fatalf("get nonexistent endpoint: want 404, got %d", status)
	}
}

func TestDeployAndUndeployModel(t *testing.T) {
	srv := newTestServer(t)
	base := modelsBase(srv.URL)

	// Create endpoint.
	var op Operation
	testutil.DoJSON(t, "POST", base+"/endpoints", map[string]any{"displayName": "ep"}, &op)
	var list struct {
		Endpoints []Endpoint `json:"endpoints"`
	}
	testutil.DoJSON(t, "GET", base+"/endpoints", nil, &list)
	ep := list.Endpoints[0]
	epID := ep.Name[strings.LastIndex(ep.Name, "/")+1:]

	// Upload a model.
	testutil.DoJSON(t, "POST", base+"/models:upload", map[string]any{"model": map[string]any{"displayName": "m"}}, &op)
	parts := strings.Split(op.Name, "/")
	modelID := strings.TrimPrefix(parts[len(parts)-1], "op-")
	modelName := fmt.Sprintf("projects/proj1/locations/us-central1/models/%s", modelID)

	// Deploy.
	status := testutil.DoJSON(t, "POST", base+"/endpoints/"+epID+":deployModel", map[string]any{
		"deployedModel": map[string]any{
			"model":       modelName,
			"displayName": "deployed-m",
		},
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("deployModel: status=%d op=%+v", status, op)
	}

	// Verify endpoint has deployed model.
	var got Endpoint
	testutil.DoJSON(t, "GET", base+"/endpoints/"+epID, nil, &got)
	if len(got.DeployedModels) != 1 {
		t.Fatalf("after deploy: want 1 deployed model, got %d", len(got.DeployedModels))
	}
	deployedID := got.DeployedModels[0].ID

	// Undeploy.
	status = testutil.DoJSON(t, "POST", base+"/endpoints/"+epID+":undeployModel", map[string]any{
		"deployedModelId": deployedID,
	}, &op)
	if status != 200 || !op.Done {
		t.Fatalf("undeployModel: status=%d op=%+v", status, op)
	}

	// Endpoint should have no deployed models. Use a fresh variable so that
	// json.Unmarshal doesn't silently keep the previous DeployedModels value
	// when the omitempty field is absent from the response JSON.
	var gotAfterUndeploy Endpoint
	testutil.DoJSON(t, "GET", base+"/endpoints/"+epID, nil, &gotAfterUndeploy)
	if len(gotAfterUndeploy.DeployedModels) != 0 {
		t.Fatalf("after undeploy: want 0 deployed models, got %d", len(gotAfterUndeploy.DeployedModels))
	}
}

func TestGetOperation(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/aiplatform/v1/projects/proj1/locations/us-central1/operations/op-1"
	var op Operation
	status := testutil.DoJSON(t, "GET", base, nil, &op)
	if status != 200 || !op.Done {
		t.Fatalf("getOperation: status=%d op=%+v", status, op)
	}
}
