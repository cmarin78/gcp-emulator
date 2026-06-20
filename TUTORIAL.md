# GCP Emulator — Getting Started Tutorial

This tutorial walks through installing the emulator, running it, and
exercising it three ways: through the built-in web console, through the
real `gcloud` CLI, and through real Terraform. Every command below was
verified against this repo.

## 1. Prerequisites

- **Go 1.22+** — required to build/run the emulator. Check with:
  ```bash
  go version
  ```
  If missing: https://go.dev/dl/, `winget install GoLang.Go` (Windows),
  `brew install go` (macOS), or `apt install golang-go` (Linux).
- **gcloud CLI** — optional, only needed for section 4.
- **Terraform** (with the `hashicorp/google` provider) — optional, only
  needed for section 5.
- **Docker** — optional alternative to a local Go install (section 2b).

## 2. Install and run

### 2a. Run from source

```bash
git clone <your-repo-url> gcp-emulator
cd gcp-emulator
go mod tidy          # downloads the one dependency: go.etcd.io/bbolt
go run ./cmd/server
```

You should see the server start and listen on `:8443` by default,
persisting state to `data/emulator.db`.

To build a standalone binary instead of `go run`-ing it every time:

```bash
go build -o bin/gcp-emulator ./cmd/server
./bin/gcp-emulator
```

Useful flags/env vars:

```bash
go run ./cmd/server -addr :9000 -db data/other.db -web web/console
# equivalently
EMULATOR_ADDR=:9000 EMULATOR_DB=data/other.db go run ./cmd/server
```

### 2b. Run with Docker (no Go install needed)

```bash
docker compose up --build -d
# or, without compose:
docker build -t gcp-emulator .
docker run --rm -p 8443:8443 -v emulator-data:/data gcp-emulator
```

State persists in the `emulator-data` volume across restarts (but is
wiped by `docker compose down -v`).

### 2c. Verify it's alive

```bash
curl -X POST localhost:8443/storage/v1/b -d '{"name":"smoke-test-bucket"}'
curl localhost:8443/storage/v1/b
```

Expect a JSON bucket object back from the first call, and a `{"items":
[...]}` listing from the second.

## 3. Try it in the web console

1. Open `http://localhost:8443` in a browser. This serves the bundled
   console (`web/console`) — a lightweight clone of the Google Cloud
   Console for inspecting whatever the emulator currently holds.
2. Create some resources first via `curl`/`gcloud`/Terraform (the console
   is a viewer/manager for state, not a resource designer), then refresh
   the console pages to see them — buckets, instances, and service
   accounts are the currently supported views.
3. Example: after running the bucket `curl` command from 2c, the console's
   Storage page should list `smoke-test-bucket`.

If you're iterating on the emulator itself and want a clean slate, stop
the server and delete `data/emulator.db` (or point `-db` at a fresh path).

## 4. Try it with the real `gcloud` CLI

The emulator works by pointing gcloud's `api_endpoint_overrides` at
`localhost` instead of the real Google endpoints. gcloud still requires an
"active" account, but the emulator never validates the token — reusing a
logged-in session is enough.

**Recommended:** isolate this in its own gcloud configuration so you don't
touch your real one:

```bash
gcloud config configurations create emulator-test
gcloud config configurations activate emulator-test
```

Then point it at the emulator:

```bash
# Linux/macOS
./scripts/configure-gcloud.sh http://localhost:8443

# Windows (PowerShell)
.\scripts\configure-gcloud.ps1 http://localhost:8443
```

This sets the endpoint overrides for storage, compute, and IAM (storage
and compute need the `v1/` suffix baked into the override; IAM appends it
itself — the script handles this).

### Practical example: storage + compute + IAM

```bash
# Storage
gcloud storage buckets create gs://my-bucket --project=demo-project
gcloud storage buckets list

# Compute (networking first, then an instance)
gcloud compute networks create demo-vpc --project=demo-project --subnet-mode=auto
gcloud compute instances create my-vm \
  --zone=us-central1-a --project=demo-project \
  --network=demo-vpc --image-family=debian-12 --image-project=debian-cloud
gcloud compute instances list --zones=us-central1-a --project=demo-project

# IAM
gcloud iam service-accounts create demo-sa --display-name="Demo SA" --project=demo-project
gcloud iam service-accounts list --project=demo-project
```

Each of these talks to the real `gcloud` binary, which sends real
Google-API-shaped HTTP requests — just to `localhost:8443` instead of
`*.googleapis.com`. You can inspect what gcloud actually sent/received
with `--log-http`, e.g.:

```bash
gcloud storage buckets list --log-http
```

### Switching back to real GCP

```bash
gcloud config configurations activate default   # or whatever your real config is named
```

## 5. Try it with Terraform

Point the `google` provider's custom-endpoint attributes at the emulator
and supply a dummy static `access_token` to skip real OAuth. Each Google
service family has its own custom-endpoint attribute name in the
provider — the ones currently exercised against this emulator are listed
below.

### Example A: networking + compute instance

```hcl
# main.tf
provider "google" {
  project                 = "demo-project"
  region                  = "us-central1"
  zone                    = "us-central1-a"
  access_token            = "dummy-token"
  storage_custom_endpoint = "http://localhost:8443/storage/v1/"
  compute_custom_endpoint = "http://localhost:8443/compute/v1/"
}

resource "google_compute_network" "vpc" {
  name                    = "tf-vpc"
  auto_create_subnetworks = true
}

resource "google_compute_instance" "vm" {
  name         = "tf-vm"
  machine_type = "e2-medium"
  zone         = "us-central1-a"

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      size  = 20
    }
  }

  network_interface {
    network       = google_compute_network.vpc.name
    access_config {}
  }
}
```

```bash
terraform init
terraform apply -auto-approve
terraform state list
terraform destroy -auto-approve
```

Both `apply` and `destroy` complete cleanly with no provider patches.

### Example B: Cloud Run service

```hcl
provider "google" {
  project                      = "demo-project"
  region                       = "us-central1"
  access_token                 = "dummy-token"
  cloud_run_v2_custom_endpoint = "http://localhost:8443/v2/"
}

resource "google_cloud_run_v2_service" "default" {
  name     = "tf-hello"
  location = "us-central1"

  template {
    containers {
      image = "gcr.io/cloudrun/hello"
      ports {
        container_port = 8080
      }
    }
  }
}
```

### Example C: BigQuery dataset + table

```hcl
provider "google" {
  project                   = "demo-project"
  region                    = "us-central1"
  access_token              = "dummy-token"
  big_query_custom_endpoint = "http://localhost:8443/bigquery/v2/"
}

resource "google_bigquery_dataset" "default" {
  dataset_id = "tf_dataset"
  location   = "US"
}

resource "google_bigquery_table" "default" {
  dataset_id          = google_bigquery_dataset.default.dataset_id
  table_id            = "tf_table"
  deletion_protection = false   # required by the provider itself for `destroy`

  schema = jsonencode([
    { name = "id", type = "STRING" },
  ])
}
```

### Example D: Cloud KMS key ring + crypto key

```hcl
provider "google" {
  project             = "demo-project"
  region              = "us-central1"
  access_token        = "dummy-token"
  kms_custom_endpoint = "http://localhost:8443/v1/"
}

resource "google_kms_key_ring" "default" {
  name     = "tf-keyring"
  location = "us-central1"
}

resource "google_kms_crypto_key" "default" {
  name     = "tf-key"
  key_ring = google_kms_key_ring.default.id
  purpose  = "ENCRYPT_DECRYPT"
}
```

Note: `terraform destroy` here calls `cryptoKeyVersions:destroy` under the
hood — the key ring/crypto key resources are never actually deleted,
matching real GCP behavior (Cloud KMS has no delete endpoint for either).

### Tips for any of the above

- Run each example in its own directory/workspace so state files don't
  collide.
- If `terraform apply` hangs or errors with a connection refused, confirm
  the emulator is actually listening (`curl localhost:8443/storage/v1/b`)
  and that the custom-endpoint URL in your provider block matches the
  port you started it on.
- `terraform destroy` should always be run before stopping the emulator
  if you want clean state for the next run — otherwise just delete
  `data/emulator.db` to reset everything at once.

## 6. Running the automated test suite

Independent of the manual exercises above, the repo ships its own
regression tests:

```bash
go build ./...
go vet ./...
go test ./... -v
```

This builds every package, runs static analysis, and runs each service's
lifecycle + validation/error-path tests (including duplicate-create
conflict checks) against an in-memory database — no live server or
network calls required. The same three commands run in CI on every
push/PR (`.github/workflows/ci.yml`).

## 7. Where to go next

- [README.md](README.md) — full list of emulated services and resources,
  plus design notes.
- [ROADMAP.md](ROADMAP.md) — phased history of what was built and why,
  including the most recent emulation-gap audit.
- [E2E_TEST_REPORT.md](E2E_TEST_REPORT.md) — detailed logs from prior
  live `gcloud`/Terraform verification passes.
