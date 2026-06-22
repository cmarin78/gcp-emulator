package cloudsql

import (
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
// these tests can exercise the opt-in *decision* path
// (WantsReal/tryStartReal's early returns) without ever actually starting
// an embedded Postgres engine -- that needs network access on its first
// run (to download the binary) and takes real wall-clock time, neither of
// which a routine `go test ./...` run should depend on. The one test that
// does start a real engine is gated behind EMULATOR_REAL_PG_TESTS below.
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

// TestCreateInstanceDefaultsToShapeOnly confirms that, without any opt-in
// signal, behavior is byte-for-byte what it was before Phase 13: no
// realConnection field, no entry in the Governor. This is the test that
// guards the "zero-cost by default for every existing caller" property
// Phase 12/13 both promise.
func TestCreateInstanceDefaultsToShapeOnly(t *testing.T) {
	srv, gov := newRealOptInServer(t)

	var inst DatabaseInstance
	status := testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances", map[string]any{
		"name":            "plain",
		"databaseVersion": "POSTGRES_15",
	}, new(Operation))
	if status != 200 {
		t.Fatalf("create: status=%d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/projects/proj1/instances/plain", nil, &inst)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if inst.RealConnection != nil {
		t.Fatalf("expected no realConnection without opt-in, got %+v", inst.RealConnection)
	}
	if len(gov.Snapshot().Backends) != 0 {
		t.Fatalf("expected no backend admitted without opt-in, got %+v", gov.Snapshot())
	}
}

// TestCreateInstanceOptInNonPostgresStaysShapeOnly confirms the opt-in
// check is also gated on databaseVersion: a MySQL/SQL Server instance
// asking for backend=real never attempts to start Postgres (the only
// engine this emulator can run without Docker).
func TestCreateInstanceOptInNonPostgresStaysShapeOnly(t *testing.T) {
	srv, gov := newRealOptInServer(t)

	var inst DatabaseInstance
	status := testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances?backend=real", map[string]any{
		"name":            "mysql-inst",
		"databaseVersion": "MYSQL_8_0",
	}, new(Operation))
	if status != 200 {
		t.Fatalf("create: status=%d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/projects/proj1/instances/mysql-inst", nil, &inst)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if inst.RealConnection != nil {
		t.Fatalf("expected no realConnection for a non-Postgres opt-in, got %+v", inst.RealConnection)
	}
	if len(gov.Snapshot().Backends) != 0 {
		t.Fatalf("expected no backend admitted for a non-Postgres opt-in, got %+v", gov.Snapshot())
	}
}

// TestNilGovernorNeverAttemptsReal confirms New(db, nil) -- the shape used
// by every test/registration path that doesn't care about Phase 13 -- is
// safe even when a caller sends the opt-in query param: tryStartReal's
// nil-Governor check must short-circuit before ever touching
// realbackend.WantsReal or the postgres package.
func TestNilGovernorNeverAttemptsReal(t *testing.T) {
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db, nil).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var inst DatabaseInstance
	status := testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances?backend=real", map[string]any{
		"name":            "no-gov",
		"databaseVersion": "POSTGRES_15",
	}, new(Operation))
	if status != 200 {
		t.Fatalf("create: status=%d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/projects/proj1/instances/no-gov", nil, &inst)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if inst.RealConnection != nil {
		t.Fatalf("expected no realConnection with a nil Governor, got %+v", inst.RealConnection)
	}
}

// TestRealPostgresLifecycle is the actual end-to-end integration test:
// opt in via settings.userLabels, confirm a real embedded Postgres engine
// comes up, create a real database and a real user against it, then
// delete the instance and confirm the Governor released it. Gated behind
// EMULATOR_REAL_PG_TESTS=1 because the first run on a machine needs
// network access to download the real postgres binary (cached after
// that) and takes real wall-clock seconds -- not something a routine `go
// test ./...`/CI run should pay for by default. Run explicitly with:
//
//	EMULATOR_REAL_PG_TESTS=1 go test ./internal/services/cloudsql/... -run TestRealPostgresLifecycle -v
func TestRealPostgresLifecycle(t *testing.T) {
	if os.Getenv("EMULATOR_REAL_PG_TESTS") != "1" {
		t.Skip("set EMULATOR_REAL_PG_TESTS=1 to run the real embedded-Postgres integration test (downloads a real postgres binary on first run)")
	}
	srv, gov := newRealOptInServer(t)

	var inst DatabaseInstance
	status := testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances", map[string]any{
		"name":            "real-pg",
		"databaseVersion": "POSTGRES_15",
		"settings": map[string]any{
			"userLabels": map[string]string{"emulator.dev/backend": "real"},
		},
	}, new(Operation))
	if status != 200 {
		t.Fatalf("create: status=%d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/sql/v1beta4/projects/proj1/instances/real-pg", nil, &inst)
	if status != 200 {
		t.Fatalf("get: status=%d", status)
	}
	if inst.RealConnection == nil {
		t.Fatal("expected a real backend to be admitted for an opted-in Postgres instance")
	}
	if len(gov.Snapshot().Backends) != 1 {
		t.Fatalf("expected exactly one admitted backend, got %+v", gov.Snapshot())
	}

	// Phase 15: the GET above already triggered pollRealMetrics against
	// the real embedded engine, so a real GAUGE connection-count metric
	// should already be recorded for this project, and a real-backend
	// start log entry should be present too.
	series := activity.ListTimeSeries("proj1", "cloudsql.googleapis.com/database/postgresql/num_backends")
	if len(series) != 1 || series[0].Kind != "GAUGE" {
		t.Fatalf("expected one GAUGE num_backends series after GET, got %+v", series)
	}
	if len(series[0].Points) == 0 {
		t.Fatalf("expected at least one recorded point, got %+v", series[0])
	}
	if !logsContainSubstring(activity.ListLogs("proj1"), "real") {
		t.Fatalf("expected a real-backend log entry, got %+v", activity.ListLogs("proj1"))
	}

	status = testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances/real-pg/databases", map[string]any{
		"name": "appdb",
	}, new(Operation))
	if status != 200 {
		t.Fatalf("create database: status=%d", status)
	}

	status = testutil.DoJSON(t, "POST", srv.URL+"/sql/v1beta4/projects/proj1/instances/real-pg/users", map[string]any{
		"name":     "appuser",
		"password": "s3cret!",
	}, new(Operation))
	if status != 200 {
		t.Fatalf("create user: status=%d", status)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/sql/v1beta4/projects/proj1/instances/real-pg", nil, new(Operation))
	if status != 200 {
		t.Fatalf("delete instance: status=%d", status)
	}
	if len(gov.Snapshot().Backends) != 0 {
		t.Fatalf("expected instance deletion to release the real backend, got %+v", gov.Snapshot())
	}
}

// logsContainSubstring reports whether any recorded log entry's
// TextPayload contains substr. Small helper shared by the Phase 15
// real-backend lifecycle assertions above.
func logsContainSubstring(entries []activity.LogEntry, substr string) bool {
	for _, e := range entries {
		if strings.Contains(e.TextPayload, substr) {
			return true
		}
	}
	return false
}
