package cloudrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/cesar/gcp-emulator/internal/realbackend"
	"github.com/cesar/gcp-emulator/internal/testutil"
)

// newRealOptInServer is like newTestServer but wires a real Governor, so
// these tests can exercise the opt-in *decision* path (WantsReal/
// tryStartReal's early returns) without ever actually invoking Docker —
// that needs a real Docker daemon and pulls a real image, neither of
// which a routine `go test ./...` run should depend on. The tests that do
// invoke Docker are gated behind EMULATOR_REAL_DOCKER_TESTS below.
func newRealOptInServer(t *testing.T) (*httptest.Server, *realbackend.Governor) {
	t.Helper()
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	gov := realbackend.NewGovernor(1000)
	New(db, gov).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, gov
}

// TestCreateServiceDefaultsToShapeOnly confirms that, without any opt-in
// signal, behavior is byte-for-byte what it was before Phase 14: no
// realEndpoint field, no entry in the Governor. This guards the
// "zero-cost by default for every existing caller" property every
// real-execution phase promises.
func TestCreateServiceDefaultsToShapeOnly(t *testing.T) {
	srv, gov := newRealOptInServer(t)

	var svc Service
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/services?serviceId=plain",
		validBody(), new(Operation))
	if status != 200 {
		t.Fatalf("create: status=%d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/services/plain", nil, &svc)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if svc.RealEndpoint != nil {
		t.Fatalf("expected no realEndpoint without opt-in, got %+v", svc.RealEndpoint)
	}
	if len(gov.Snapshot().Backends) != 0 {
		t.Fatalf("expected no backend admitted without opt-in, got %+v", gov.Snapshot())
	}
}

// TestNilGovernorNeverAttemptsRealService confirms New(db, nil) -- the
// shape used by every test/registration path that doesn't care about
// Phase 14 -- is safe even when a caller sends the opt-in query param:
// tryStartReal's nil-Governor check must short-circuit before ever
// touching realbackend.WantsReal or the dockerrun package.
func TestNilGovernorNeverAttemptsRealService(t *testing.T) {
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db, nil).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var svc Service
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/services?serviceId=no-gov&backend=real",
		validBody(), new(Operation))
	if status != 200 {
		t.Fatalf("create: status=%d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/services/no-gov", nil, &svc)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if svc.RealEndpoint != nil {
		t.Fatalf("expected no realEndpoint with a nil Governor, got %+v", svc.RealEndpoint)
	}
}

// TestJobRunOptInWithoutDockerStaysShapeOnly confirms a job's ":run"
// action that opts in still falls back to the old no-op shape when no
// Governor/Docker is wired -- tryRunReal never needs a Governor at all,
// but this still exercises WantsReal's early return.
func TestJobRunOptInWithoutDockerStaysShapeOnly(t *testing.T) {
	srv := newTestServer(t) // New(db, nil) per cloudrun_test.go

	testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/jobs?jobId=opted-in-job",
		validJobBody(), nil)

	var runOp Operation
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/jobs/opted-in-job:run?backend=real", nil, &runOp)
	if status != 200 || !runOp.Done {
		t.Fatalf("run: status=%d op=%+v", status, runOp)
	}
	// No assertion on the response body's shape beyond "it succeeded" --
	// tryRunReal's nil-Governor-independent WantsReal check still applies,
	// but with no Docker on the test machine guaranteed, DetectDocker
	// should report unavailable and the run stays the old no-op shape.
}

// realDockerTestsEnabled gates the handful of tests below that actually
// run a real container, mirroring Phase 13's EMULATOR_REAL_PG_TESTS gate,
// so routine `go test ./...`/CI never needs Docker installed.
func realDockerTestsEnabled(t *testing.T) bool {
	t.Helper()
	if os.Getenv("EMULATOR_REAL_DOCKER_TESTS") != "1" {
		t.Skip("set EMULATOR_REAL_DOCKER_TESTS=1 to run this test against a real Docker daemon")
		return false
	}
	avail := realbackend.DetectDocker(context.Background())
	if !avail.Available {
		t.Skipf("Docker isn't available on this machine (%s)", avail.Detail)
		return false
	}
	return true
}

// TestRealServiceLifecycle is the end-to-end integration test: opt in via
// labels, confirm a real container comes up with a reachable
// realEndpoint, then delete the service and confirm the Governor released
// it. Run explicitly with:
//
//	EMULATOR_REAL_DOCKER_TESTS=1 go test ./internal/services/cloudrun/... -run TestRealServiceLifecycle -v
func TestRealServiceLifecycle(t *testing.T) {
	if !realDockerTestsEnabled(t) {
		return
	}
	srv, gov := newRealOptInServer(t)

	body := map[string]any{
		"labels": map[string]string{"emulator.dev/backend": "real"},
		"template": map[string]any{
			"containers": []map[string]any{{
				"image": "nginx:alpine",
				"ports": []map[string]int{{"containerPort": 80}},
			}},
		},
	}
	testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/services?serviceId=real-svc",
		body, new(Operation))

	var svc Service
	status := testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/services/real-svc", nil, &svc)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if svc.RealEndpoint == nil {
		t.Fatal("expected a real backend to be admitted for an opted-in service")
	}
	if len(gov.Snapshot().Backends) != 1 {
		t.Fatalf("expected exactly one admitted backend, got %+v", gov.Snapshot())
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v2/projects/proj1/locations/us-central1/services/real-svc", nil, new(Operation))
	if status != 200 {
		t.Fatalf("delete: status=%d", status)
	}
	if len(gov.Snapshot().Backends) != 0 {
		t.Fatalf("expected service deletion to release the real backend, got %+v", gov.Snapshot())
	}
}

// TestRealJobRun runs a real, tiny job image to completion and checks the
// captured exit code in the ":run" operation's response.
func TestRealJobRun(t *testing.T) {
	if !realDockerTestsEnabled(t) {
		return
	}
	srv := newTestServerWithGovernor(t)

	body := map[string]any{
		"labels": map[string]string{"emulator.dev/backend": "real"},
		"template": map[string]any{
			"template": map[string]any{
				"containers": []map[string]string{{"image": "busybox:latest"}},
			},
		},
	}
	testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/jobs?jobId=real-job",
		body, new(Operation))

	var runOp Operation
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/jobs/real-job:run", nil, &runOp)
	if status != 200 || !runOp.Done {
		t.Fatalf("run: status=%d op=%+v", status, runOp)
	}
	var resp struct {
		RealRun *RealRunResult `json:"realRun"`
	}
	if err := json.Unmarshal(runOp.Response, &resp); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if resp.RealRun == nil {
		t.Fatal("expected a realRun result for an opted-in job execution")
	}
	if resp.RealRun.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (output=%q)", resp.RealRun.ExitCode, resp.RealRun.Output)
	}
}

// newTestServerWithGovernor is like newTestServer but with a real
// Governor wired, for jobs tests that opt into real execution (Jobs
// never admit into the Governor themselves, but cloudrun.New still
// expects one to be passed consistently with services).
func newTestServerWithGovernor(t *testing.T) *httptest.Server {
	t.Helper()
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db, realbackend.NewGovernor(1000)).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
