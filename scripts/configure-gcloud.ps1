# Configura gcloud CLI para que apunte al emulador local en vez de a GCP real.
# Uso: .\scripts\configure-gcloud.ps1 [host:puerto]
param(
    [string]$Endpoint = "http://localhost:8443"
)

Write-Host "Configurando gcloud para usar el emulador en $Endpoint ..."

gcloud config set project demo-project
gcloud config set core/disable_prompts true

gcloud config set api_endpoint_overrides/storage "$Endpoint/storage/"
gcloud config set api_endpoint_overrides/compute "$Endpoint/compute/"
gcloud config set api_endpoint_overrides/iam "$Endpoint/"

Write-Host "Listo. Probá con: gcloud storage buckets list"
Write-Host "Para revertir: gcloud config configurations create real-gcp; gcloud config configurations activate real-gcp"
