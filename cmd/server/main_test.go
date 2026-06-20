package main

// This test exists specifically because of the Phase 8 incident documented
// in E2E_TEST_REPORT.md: two services registered the exact same
// http.ServeMux route pattern, which only panics at registration time when
// every service's Register() is actually called together, in the same
// process, against the same mux -- exactly what main() does and exactly
// what no per-package unit test exercises. `go build`/`go vet` are
// powerless against this class of bug; only running the registration code
// catches it. This test reproduces main()'s wiring (same services, same
// order) without starting an HTTP listener, so CI catches a duplicate-route
// panic before it ever reaches a real machine.

import (
	"net/http"
	"testing"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/services/artifactregistry"
	"github.com/cesar/gcp-emulator/internal/services/bigquery"
	"github.com/cesar/gcp-emulator/internal/services/billingbudgets"
	"github.com/cesar/gcp-emulator/internal/services/certificatemanager"
	"github.com/cesar/gcp-emulator/internal/services/cloudbuild"
	"github.com/cesar/gcp-emulator/internal/services/clouddns"
	"github.com/cesar/gcp-emulator/internal/services/cloudfunctions"
	"github.com/cesar/gcp-emulator/internal/services/cloudrun"
	"github.com/cesar/gcp-emulator/internal/services/cloudscheduler"
	"github.com/cesar/gcp-emulator/internal/services/cloudsql"
	"github.com/cesar/gcp-emulator/internal/services/cloudtasks"
	"github.com/cesar/gcp-emulator/internal/services/compute"
	"github.com/cesar/gcp-emulator/internal/services/eventarc"
	"github.com/cesar/gcp-emulator/internal/services/filestore"
	"github.com/cesar/gcp-emulator/internal/services/firestore"
	"github.com/cesar/gcp-emulator/internal/services/gcs"
	"github.com/cesar/gcp-emulator/internal/services/gke"
	"github.com/cesar/gcp-emulator/internal/services/iam"
	"github.com/cesar/gcp-emulator/internal/services/iap"
	"github.com/cesar/gcp-emulator/internal/services/kms"
	"github.com/cesar/gcp-emulator/internal/services/loadbalancing"
	"github.com/cesar/gcp-emulator/internal/services/logging"
	"github.com/cesar/gcp-emulator/internal/services/memorystore"
	"github.com/cesar/gcp-emulator/internal/services/monitoring"
	"github.com/cesar/gcp-emulator/internal/services/orgpolicy"
	"github.com/cesar/gcp-emulator/internal/services/pubsub"
	"github.com/cesar/gcp-emulator/internal/services/resourcemanager"
	"github.com/cesar/gcp-emulator/internal/services/secretmanager"
	"github.com/cesar/gcp-emulator/internal/services/servicenetworking"
	"github.com/cesar/gcp-emulator/internal/services/spanner"
	"github.com/cesar/gcp-emulator/internal/services/vpcaccess"
	"github.com/cesar/gcp-emulator/internal/services/workflows"
	"github.com/cesar/gcp-emulator/internal/testutil"
)

func TestAllServicesRegisterWithoutPanic(t *testing.T) {
	db := testutil.NewDB(t)

	srv := server.New()
	mux := srv.Mux()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registering all services together panicked (likely a duplicate "+
				"http.ServeMux route pattern across two services): %v", r)
		}
	}()

	iam.New(db).Register(mux)
	gcs.New(db).Register(mux)
	compute.New(db).Register(mux)
	pubsub.New(db).Register(mux)
	secretmanager.New(db).Register(mux)
	artifactregistry.New(db).Register(mux)
	cloudrun.New(db).Register(mux)
	cloudfunctions.New(db).Register(mux)
	server.RegisterV2Operations(mux)
	cloudsql.New(db).Register(mux)
	firestore.New(db).Register(mux)
	bigquery.New(db).Register(mux)
	kms.New(db).Register(mux)
	logging.New(db).Register(mux)
	monitoring.New(db).Register(mux)
	resourcemanager.New(db).Register(mux)
	cloudscheduler.New(db).Register(mux)
	cloudtasks.New(db).Register(mux)
	clouddns.New(db).Register(mux)
	loadbalancing.New(db).Register(mux)
	cloudbuild.New(db).Register(mux)
	memorystore.New(db).Register(mux)
	spanner.New(db).Register(mux)
	gke.New(db).Register(mux)
	vpcaccess.New(db).Register(mux)
	filestore.New(db).Register(mux)
	workflows.New(db).Register(mux)
	eventarc.New(db).Register(mux)
	servicenetworking.New(db).Register(mux)
	iap.New(db).Register(mux)
	orgpolicy.New(db).Register(mux)
	billingbudgets.New(db).Register(mux)
	certificatemanager.New(db).Register(mux)

	// A trivial sanity request: the mux should at least be wired up enough
	// to return *some* response (even a 404) instead of nil-dereferencing.
	req, _ := http.NewRequest("GET", "/healthz", nil)
	rec := &discardResponseWriter{header: http.Header{}}
	srv.Handler().ServeHTTP(rec, req)
}

// discardResponseWriter is a minimal http.ResponseWriter that throws away
// the body -- enough to drive a single ServeHTTP call in a unit test
// without spinning up a real listener.
type discardResponseWriter struct {
	header http.Header
	status int
}

func (w *discardResponseWriter) Header() http.Header         { return w.header }
func (w *discardResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *discardResponseWriter) WriteHeader(status int)      { w.status = status }
