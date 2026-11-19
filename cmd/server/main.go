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
	"os/signal"
	"syscall"
	"time"

	"github.com/cesar/gcp-emulator/internal/iamenforce"
	"github.com/cesar/gcp-emulator/internal/realbackend"
	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/services/alloydb"
	"github.com/cesar/gcp-emulator/internal/services/artifactregistry"
	"github.com/cesar/gcp-emulator/internal/services/assetinventory"
	"github.com/cesar/gcp-emulator/internal/services/bigquery"
	"github.com/cesar/gcp-emulator/internal/services/appengine"
	"github.com/cesar/gcp-emulator/internal/services/bigtable"
	"github.com/cesar/gcp-emulator/internal/services/billingbudgets"
	"github.com/cesar/gcp-emulator/internal/services/composer"
	"github.com/cesar/gcp-emulator/internal/services/dataflow"
	"github.com/cesar/gcp-emulator/internal/services/memcache"
	"github.com/cesar/gcp-emulator/internal/services/dataproc"
	"github.com/cesar/gcp-emulator/internal/services/certificatemanager"
	"github.com/cesar/gcp-emulator/internal/services/cloudbuild"
	"github.com/cesar/gcp-emulator/internal/services/clouddeploy"
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
	"github.com/cesar/gcp-emulator/internal/services/metadata"
	"github.com/cesar/gcp-emulator/internal/services/memorystore"
	"github.com/cesar/gcp-emulator/internal/services/monitoring"
	"github.com/cesar/gcp-emulator/internal/services/networkmanagement"
	"github.com/cesar/gcp-emulator/internal/services/orgpolicy"
	"github.com/cesar/gcp-emulator/internal/services/privateca"
	"github.com/cesar/gcp-emulator/internal/services/pubsub"
	"github.com/cesar/gcp-emulator/internal/services/resourcemanager"
	"github.com/cesar/gcp-emulator/internal/services/secretmanager"
	"github.com/cesar/gcp-emulator/internal/services/servicenetworking"
	"github.com/cesar/gcp-emulator/internal/services/serviceusage"
	"github.com/cesar/gcp-emulator/internal/services/spanner"
	"github.com/cesar/gcp-emulator/internal/services/vertexai"
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
	cloudrun.New(db, realGovernor).Register(mux)
	cloudfunctions.New(db, realGovernor).Register(mux)
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
	assetinventory.New(db).Register(mux)
	dataflow.New(db).Register(mux)
	vertexai.New(db).Register(mux)
	dataproc.New(db).Register(mux)
	bigtable.New(db).Register(mux)
	memcache.New(db).Register(mux)
	appengine.New(db).Register(mux)
	composer.New(db).Register(mux)
	serviceusage.New(db).Register(mux)
	metadata.New(db).Register(mux)
	alloydb.New(db).Register(mux)
	privateca.New(db).Register(mux)
	clouddeploy.New(db).Register(mux)

	// Endpoint de salud, útil para chequear que el emulador está arriba.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		server.WriteJSON(w, 200, map[string]string{"status": "ok"})
	})

	// Frontend estático (consola tipo Google Cloud Console).
	if info, statErr := os.Stat(*staticDir); statErr == nil && info.IsDir() {
		mux.Handle("/", http.FileServer(http.Dir(*staticDir)))
	}

	log.Printf("GCP Emulator escuchando en %s (db=%s, web=%s)", *addr, *dbPath, *staticDir)
	log.Printf("Endpoints: /storage/v1/*  /compute/v1/* (Compute, instance templates/MIGs/autoscalers, Load Balancing + Cloud CDN, Cloud Armor, routers/routes, network peering)  /v1/* (IAM, Pub/Sub, Secret Manager, Artifact Registry, Firestore, KMS, Cloud Scheduler, Cloud Build, Memorystore, Cloud Spanner, GKE, VPC Access connectors, Workflows, Eventarc, Service Networking connections, IAP brands/clients, Cloud Billing Budgets, Certificate Manager, Network Management connectivity tests, Cloud Asset Inventory searchAllResources)  /file/v1/* (Filestore)  /v2/* (Cloud Run services + Jobs, Cloud Functions, Logging sinks, Cloud Tasks with retry/backoff, Org Policy)  /apis/domains.cloudrun.com/v1/* (Cloud Run Domain Mappings)  /sql/v1beta4/* (Cloud SQL)  /bigquery/v2/* (BigQuery)  /v1b3/* (Dataflow jobs)  /aiplatform/v1/* (Vertex AI models + endpoints)  /dataproc/v1/* (Dataproc clusters)  /bigtable/v2/* (Cloud Bigtable instances + clusters + appProfiles)  /memcache/v1beta2/* (Cloud Memcache instances)  /appengine/v1/* (App Engine applications + services + versions)  /composer/v1/* (Cloud Composer environments)  /v3/* (Monitoring alert policies, Resource Manager projects)  /dns/v1/* (Cloud DNS)  /healthz  /admin/real-backends (Phase 12 real-execution governor introspection)")
	// iamenforce envuelve todo el handler con una capa de IAM opcional: solo
	// actúa sobre requests que mandan el header X-Emulator-Caller (ningún
	// cliente real -- gcloud/Terraform -- lo manda), así que gcloud/Terraform
	// y los más de 30 paquetes de tests existentes siguen funcionando sin
	// cambios; un caller que sí lo manda obtiene un 403 PERMISSION_DENIED
	// real cuando la política IAM del proyecto no lo cubre.
	handler := iamenforce.Middleware(db)(srv.Handler())
	httpServer := &http.Server{
		Addr:    *addr,
		Handler: handler,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("error escuchando: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("Señal de apagado recibida, cerrando el emulador de GCP limpiamente...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("error cerrando servidor HTTP: %v", err)
	}

	realGovernor.StopAll()
	log.Printf("Emulador detenido limpiamente.")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
