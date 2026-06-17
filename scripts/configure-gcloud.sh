#!/usr/bin/env bash
# Configura gcloud CLI para que apunte al emulador local en vez de a GCP real.
# Uso: ./scripts/configure-gcloud.sh [host:puerto]
set -euo pipefail

ENDPOINT="${1:-http://localhost:8443}"

echo "Configurando gcloud para usar el emulador en ${ENDPOINT} ..."

gcloud config set project demo-project
gcloud config set core/disable_prompts true

gcloud config set api_endpoint_overrides/storage "${ENDPOINT}/storage/v1/"
gcloud config set api_endpoint_overrides/compute "${ENDPOINT}/compute/v1/"
gcloud config set api_endpoint_overrides/iam "${ENDPOINT}/"

# El emulador no valida el token; necesitás una cuenta "activa" en gcloud
# para que el CLI no se queje (puede ser la misma que ya tenés logueada).
gcloud config set account "$(gcloud config get-value account 2>/dev/null)" 2>/dev/null || true

echo "Listo. Probá con: gcloud storage buckets list"
echo "Para revertir: gcloud config configurations create real-gcp && gcloud config configurations activate real-gcp"
