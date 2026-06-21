package realbackend

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func TestAdminRealBackendsReportsEmptyByDefault(t *testing.T) {
	mux := http.NewServeMux()
	g := NewGovernor(1000)
	RegisterAdmin(mux, g)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var snap GovernorSnapshot
	status := testutil.DoJSON(t, "GET", srv.URL+"/admin/real-backends", nil, &snap)
	if status != 200 {
		t.Fatalf("GET /admin/real-backends: status=%d", status)
	}
	if snap.BudgetMB != 1000 || snap.UsedMB != 0 || len(snap.Backends) != 0 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

func TestAdminRealBackendsReflectsAdmittedBackend(t *testing.T) {
	mux := http.NewServeMux()
	g := NewGovernor(1000)
	RegisterAdmin(mux, g)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g.Admit("a", &fakeBackend{kind: "cloudsql-postgres-embedded", footprint: 150})

	var snap GovernorSnapshot
	status := testutil.DoJSON(t, "GET", srv.URL+"/admin/real-backends", nil, &snap)
	if status != 200 {
		t.Fatalf("GET /admin/real-backends: status=%d", status)
	}
	if snap.UsedMB != 150 || len(snap.Backends) != 1 || snap.Backends[0].Kind != "cloudsql-postgres-embedded" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}
