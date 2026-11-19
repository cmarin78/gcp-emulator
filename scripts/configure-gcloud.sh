#!/usr/bin/env bash
# Configura gcloud CLI y variables de entorno para que apunten al emulador local en vez de a GCP real.
# Uso: ./scripts/configure-gcloud.sh [host:puerto]
set -euo pipefail

ENDPOINT="${1:-http://localhost:8443}"
# Strip protocol for metadata host variable
HOST_PORT="${ENDPOINT#http://}"
HOST_PORT="${HOST_PORT#https://}"

echo "Configurando gcloud para usar el emulador en ${ENDPOINT} ..."

gcloud config set project demo-project
gcloud config set core/disable_prompts true

# Core & Infrastructure
gcloud config set api_endpoint_overrides/storage "${ENDPOINT}/storage/v1/"
gcloud config set api_endpoint_overrides/compute "${ENDPOINT}/compute/v1/"
gcloud config set api_endpoint_overrides/iam "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/cloudresourcemanager "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/serviceusage "${ENDPOINT}/"

# Messaging & Data
gcloud config set api_endpoint_overrides/pubsub "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/secretmanager "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/sqladmin "${ENDPOINT}/sql/v1beta4/"
gcloud config set api_endpoint_overrides/bigquery "${ENDPOINT}/bigquery/v2/"
gcloud config set api_endpoint_overrides/bigtableadmin "${ENDPOINT}/bigtable/v2/"
gcloud config set api_endpoint_overrides/spanner "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/redis "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/memcache "${ENDPOINT}/memcache/v1beta2/"
gcloud config set api_endpoint_overrides/file "${ENDPOINT}/file/v1/"
gcloud config set api_endpoint_overrides/alloydb "${ENDPOINT}/alloydb/"

# Serverless & Orchestration
gcloud config set api_endpoint_overrides/run "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/cloudfunctions "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/appengine "${ENDPOINT}/appengine/v1/"
gcloud config set api_endpoint_overrides/cloudtasks "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/cloudscheduler "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/workflows "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/composer "${ENDPOINT}/composer/v1/"

# Containers, CI/CD & Deploy
gcloud config set api_endpoint_overrides/container "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/artifactregistry "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/cloudbuild "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/clouddeploy "${ENDPOINT}/"

# Analytics & AI
gcloud config set api_endpoint_overrides/dataproc "${ENDPOINT}/dataproc/v1/"
gcloud config set api_endpoint_overrides/dataflow "${ENDPOINT}/v1b3/"
gcloud config set api_endpoint_overrides/aiplatform "${ENDPOINT}/aiplatform/v1/"

# Networking & Security
gcloud config set api_endpoint_overrides/dns "${ENDPOINT}/dns/v1/"
gcloud config set api_endpoint_overrides/vpcaccess "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/servicenetworking "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/networkmanagement "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/cloudkms "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/certificatemanager "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/privateca "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/iap "${ENDPOINT}/"

# Observability, Governance & Assets
gcloud config set api_endpoint_overrides/logging "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/monitoring "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/billingbudgets "${ENDPOINT}/"
gcloud config set api_endpoint_overrides/cloudasset "${ENDPOINT}/"

# El emulador no valida el token; necesitás una cuenta "activa" en gcloud
# para que el CLI no se queje (puede ser la misma que ya tenés logueada).
gcloud config set account "$(gcloud config get-value account 2>/dev/null)" 2>/dev/null || true

echo "------------------------------------------------------------------"
echo "Listo. Para que SDKs (Go, Python, Java, Node) detecten el Metadata Server local, ejecutá:"
echo "  export GCE_METADATA_HOST=\"${HOST_PORT}\""
echo ""
echo "Probá con: gcloud storage buckets list"
echo "Para revertir: gcloud config configurations create real-gcp && gcloud config configurations activate real-gcp"
echo "------------------------------------------------------------------"
