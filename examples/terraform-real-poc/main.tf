terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
  }
}

# Same pattern documented in the project README's "Using Terraform against
# the emulator" section: a dummy static access_token skips real OAuth, and
# each service has its own *_custom_endpoint attribute pointing back at the
# emulator's local HTTP server.
provider "google" {
  project      = "tf-real-poc"
  region       = "us-central1"
  zone         = "us-central1-a"
  access_token = "dummy-token"

  sql_custom_endpoint          = "http://localhost:8443/sql/v1beta4/"
  cloud_run_v2_custom_endpoint = "http://localhost:8443/v2/"
}

# Cloud SQL instance opted into the emulator's real-execution backend
# (Phase 13): settings.user_labels carries the opt-in label the emulator
# checks for (internal/realbackend.OptInLabel = "emulator.dev/backend"),
# and this maps 1:1 onto the real google_sql_database_instance resource's
# native `settings.user_labels` field -- no provider patch required. When
# the label is present and database_version is a Postgres version, the
# emulator starts a real embedded Postgres engine behind the instance.
resource "google_sql_database_instance" "real_pg" {
  name                = "tf-real-pg"
  database_version    = "POSTGRES_15"
  region              = "us-central1"
  deletion_protection = false

  settings {
    tier = "db-f1-micro"

    user_labels = {
      "emulator.dev/backend" = "real"
    }
  }
}

resource "google_sql_database" "appdb" {
  name     = "appdb"
  instance = google_sql_database_instance.real_pg.name
}

resource "google_sql_user" "appuser" {
  name     = "appuser"
  instance = google_sql_database_instance.real_pg.name
  password = "s3cret!"
}

# Cloud Run v2 service opted into the emulator's real-execution backend
# (Phase 14): the top-level `labels` field is the same opt-in carrier,
# again native to the real resource schema. When present, the emulator
# pulls and starts the template's container image with a real Docker
# engine and exposes a reachable realEndpoint.
resource "google_cloud_run_v2_service" "real_svc" {
  name     = "tf-real-svc"
  location = "us-central1"

  # The provider defaults this to true as a client-side safeguard (the
  # same kind of guard the README already notes for BigQuery tables) --
  # it has nothing to do with the emulator, but without it `terraform
  # destroy` fails with "cannot destroy service without setting
  # deletion_protection=false and running `terraform apply`".
  deletion_protection = false

  labels = {
    "emulator.dev/backend" = "real"
  }

  template {
    containers {
      image = "nginx:alpine"
      ports {
        container_port = 80
      }
    }
  }
}

output "cloudsql_instance_self_link" {
  value = google_sql_database_instance.real_pg.self_link
}

output "cloudsql_connection_name" {
  value = google_sql_database_instance.real_pg.connection_name
}

output "cloud_run_uri" {
  value = google_cloud_run_v2_service.real_svc.uri
}
