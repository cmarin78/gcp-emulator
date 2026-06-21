// Comando principal del emulador de GCP. Levanta un servidor HTTP que
// expone APIs compatibles con storage.googleapis.com, compute.googleapis.com
// e iam.googleapis.com, persistiendo todo en un único archivo embebido
// (BoltDB) para que el stack sea 100% portable: un solo binario + un
// archivo de datos, sin dependencias externas.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/cesar/gcp-emulator/internal/iamenforce"
	"github.com/cesar/gcp-emulator/internal/realbackend"
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
	"github.com/cesar/gcp-emulator/internal/services/networkmanagement"
	"github.com/cesar/gcp-emulator/internal/services/orgpolicy"
	"github.com/cesar/gcp-emulator/internal/services/pubsub"
	"github.com/cesar/gcp-emulator/internal/services/resourcemanager"
	"github.com/cesar/gcp-emulator/internal/services/secretmanager"
	"github.com/cesar/gcp-emulator/internal/services/servicenetworking"
	"github.com/cesar/gcp-emulator/internal/services/spanner"
	"github.com/cesar/gcp-emulator/internal/services/vpcaccess"
	"github.com/cesar/gcp-emulator/internal/services/workflows"
	"github.com/cesar/gcp-emulator/internal/storage"
)

func main() {
	addr := flag.String("addr", envOr("EMULATOR_ADDR", ":8443"), "dirección HTTP de escucha (host:puerto)")
	dbPath := flag.String("db", envOr("EMULATOR_DB", "data/emulator.db"), "ruta al archivo de datos embebido")
	staticDir := flag.String("web", envOr("EMULATOR_WEB", "web/console"), "directorio del frontend (consola)")
	flag.Parse()

	db, err := storage.Open(*dbPath)
	if err != nil {
		log.Fatalf("no se pudo abrir la base de datos: %v", err)
	}
	defer db.Close()

	srv := server.New()
	mux := srv.Mux()

	// Phase 12: pluggable real-execution foundation. No concrete real
	// backend exists yet (Phase 13+ adds Cloud Run/Functions via Docker
	// and an embedded Postgres for Cloud SQL) -- this just stands up the
	// budget-aware admission governor and its introspection endpoint so
	// later phases have somewhere to plug into from day one.
	realBudgetMB := realbackend.DetectBudgetMB()
	realGovernor := realbackend.NewGovernor(realBudgetMB)
	realbackend.RegisterAdmin(mux, realGovernor)
	dockerAvail := realbackend.DetectDocker(context.Background())
	log.Printf("real-backend foundation: budget=%dMB docker-available=%v (%s)", realBudgetMB, dockerAvail.Available, dockerAvail.Detail)

	iam.New(db).Register(mux)
	gcs.New(db).Register(mux)
	compute.New(db).Register(mux)
	pubsub.New(db).Register(mux)
	secretmanager.New(db).Register(mux)
	artifactregistry.New(db).Register(mux)
	cloudrun.New(db).Register(mux)
	cloudfunctions.New(db).Register(mux)
	server.RegisterV2Operations(mux)
	cloudsql.New(db, realGovernor).Register(mux)
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
	networkmanagement.New(db).Register(mux)

	// Endpoint de salud, útil para chequear que el emulador está arriba.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		server.WriteJSON(w, 200, map[string]string{"status": "ok"})
	})

	// Frontend estático (consola tipo Google Cloud Console).
	if info, statErr := os.Stat(*staticDir); statErr == nil && info.IsDir() {
		mux.Handle("/", http.FileServer(http.Dir(*staticDir)))
	}

	log.Printf("GCP Emulator escuchando en %s (db=%s, web=%s)", *addr, *dbPath, *staticDir)
	log.Printf("Endpoints: /storage/v1/*  /compute/v1/* (Compute, instance templates/MIGs/autoscalers, Load Balancing + Cloud CDN, Cloud Armor, routers/routes, network peering)  /v1/* (IAM, Pub/Sub, Secret Manager, Artifact Registry, Firestore, KMS, Cloud Scheduler, Cloud Build, Memorystore, Cloud Spanner, GKE, VPC Access connectors, Workflows, Eventarc, Service Networking connections, IAP brands/clients, Cloud Billing Budgets, Certificate Manager, Network Management connectivity tests)  /file/v1/* (Filestore)  /v2/* (Cloud Run services + Jobs, Cloud Functions, Logging sinks, Cloud Tasks, Org Policy)  /sql/v1beta4/* (Cloud SQL)  /bigquery/v2/* (BigQuery)  /v3/* (Monitoring alert policies, Resource Manager projects)  /dns/v1/* (Cloud DNS)  /healthz  /admin/real-backends (Phase 12 real-execution governor introspection)")
	// iamenforce envuelve todo el handler con una capa de IAM opcional: solo
	// actúa sobre requests que mandan el header X-Emulator-Caller (ningún
	// cliente real -- gcloud/Terraform -- lo manda), así que gcloud/Terraform
	// y los más de 30 paquetes de tests existentes siguen funcionando sin
	// cambios; un caller que sí lo manda obtiene un 403 PERMISSION_DENIED
	// real cuando la política IAM del proyecto no lo cubre.
	handler := iamenforce.Middleware(db)(srv.Handler())
	if err := http.ListenAndServe(*addr, handler); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
