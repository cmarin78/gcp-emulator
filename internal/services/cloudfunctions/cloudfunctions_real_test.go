package cloudfunctions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cesar/gcp-emulator/internal/activity"
	"github.com/cesar/gcp-emulator/internal/realbackend"
	"github.com/cesar/gcp-emulator/internal/testutil"
)

// newRealOptInServer is like newTestServer but wires a real Governor, so
// these tests can exercise the opt-in *decision* path (WantsReal/
// tryStartReal's early returns) without ever actually invoking Docker --
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

// TestCreateFunctionDefaultsToShapeOnly confirms that, without any opt-in
// signal, behavior is byte-for-byte what it was before Phase 14: no
// realEndpoint field, no entry in the Governor.
func TestCreateFunctionDefaultsToShapeOnly(t *testing.T) {
	srv, gov := newRealOptInServer(t)

	var fn Function
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/functions?functionId=plain",
		validBody(), new(Operation))
	if status != 200 {
		t.Fatalf("create: status=%d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/functions/plain", nil, &fn)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if fn.RealEndpoint != nil {
		t.Fatalf("expected no realEndpoint without opt-in, got %+v", fn.RealEndpoint)
	}
	if len(gov.Snapshot().Backends) != 0 {
		t.Fatalf("expected no backend admitted without opt-in, got %+v", gov.Snapshot())
	}
}

// TestCreateFunctionOptInWithoutImageStaysShapeOnly confirms the opt-in
// check is also gated on RealExecution.Image being set: Gen2's real API
// has no image field at all, so labeling alone is never enough.
func TestCreateFunctionOptInWithoutImageStaysShapeOnly(t *testing.T) {
	srv, gov := newRealOptInServer(t)

	body := map[string]any{
		"buildConfig": map[string]string{"runtime": "go121", "entryPoint": "HelloWorld"},
		"labels":      map[string]string{"emulator.dev/backend": "real"},
	}
	var fn Function
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/functions?functionId=no-image",
		body, new(Operation))
	if status != 200 {
		t.Fatalf("create: status=%d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/functions/no-image", nil, &fn)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if fn.RealEndpoint != nil {
		t.Fatalf("expected no realEndpoint without realExecution.image, got %+v", fn.RealEndpoint)
	}
	if len(gov.Snapshot().Backends) != 0 {
		t.Fatalf("expected no backend admitted without realExecution.image, got %+v", gov.Snapshot())
	}
}

// TestNilGovernorNeverAttemptsRealFunction confirms New(db, nil) is safe
// even when a caller sends every opt-in signal: tryStartReal's
// nil-Governor check must short-circuit first.
func TestNilGovernorNeverAttemptsRealFunction(t *testing.T) {
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db, nil).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body := map[string]any{
		"buildConfig":   map[string]string{"runtime": "go121", "entryPoint": "HelloWorld"},
		"labels":        map[string]string{"emulator.dev/backend": "real"},
		"realExecution": map[string]string{"image": "nginx:alpine"},
	}
	var fn Function
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/functions?functionId=no-gov",
		body, new(Operation))
	if status != 200 {
		t.Fatalf("create: status=%d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/functions/no-gov", nil, &fn)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if fn.RealEndpoint != nil {
		t.Fatalf("expected no realEndpoint with a nil Governor, got %+v", fn.RealEndpoint)
	}
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

// TestRealFunctionLifecycle is the end-to-end integration test: opt in
// via labels + realExecution.image, confirm a real container comes up
// with a reachable realEndpoint, then delete the function and confirm
// the Governor released it. Run explicitly with:
//
//	EMULATOR_REAL_DOCKER_TESTS=1 go test ./internal/services/cloudfunctions/... -run TestRealFunctionLifecycle -v
func TestRealFunctionLifecycle(t *testing.T) {
	if !realDockerTestsEnabled(t) {
		return
	}
	srv, gov := newRealOptInServer(t)

	body := map[string]any{
		"buildConfig":   map[string]string{"runtime": "go121", "entryPoint": "HelloWorld"},
		"labels":        map[string]string{"emulator.dev/backend": "real"},
		"realExecution": map[string]string{"image": "nginx:alpine"},
	}
	testutil.DoJSON(t, "POST",
		srv.URL+"/v2/projects/proj1/locations/us-central1/functions?functionId=real-fn",
		body, new(Operation))

	var fn Function
	status := testutil.DoJSON(t, "GET", srv.URL+"/v2/projects/proj1/locations/us-central1/functions/real-fn", nil, &fn)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if fn.RealEndpoint == nil {
		t.Fatal("expected a real backend to be admitted for an opted-in function")
	}
	if len(gov.Snapshot().Backends) != 1 {
		t.Fatalf("expected exactly one admitted backend, got %+v", gov.Snapshot())
	}

	// Phase 15: the GET above already triggered pollRealMetrics against
	// the real container, so memory/CPU GAUGE series should already be
	// recorded for this project, alongside a real-backend start log entry.
	mem := activity.ListTimeSeries("proj1", "cloudfunctions.googleapis.com/function/user_memory_bytes")
	cpu := activity.ListTimeSeries("proj1", "cloudfunctions.googleapis.com/function/cpu_utilizations")
	if len(mem) != 1 || mem[0].Kind != "GAUGE" || len(mem[0].Points) == 0 {
		t.Fatalf("expected a GAUGE memory series with points, got %+v", mem)
	}
	if len(cpu) != 1 || cpu[0].Kind != "GAUGE" || len(cpu[0].Points) == 0 {
		t.Fatalf("expected a GAUGE cpu series with points, got %+v", cpu)
	}
	found := false
	for _, e := range activity.ListLogs("proj1") {
		if strings.Contains(e.TextPayload, "real") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a real-backend log entry, got %+v", activity.ListLogs("proj1"))
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v2/projects/proj1/locations/us-central1/functions/real-fn", nil, new(Operation))
	if status != 200 {
		t.Fatalf("delete: status=%d", status)
	}
	if len(gov.Snapshot().Backends) != 0 {
		t.Fatalf("expected function deletion to release the real backend, got %+v", gov.Snapshot())
	}
}
