# Security Policy

gcp-emulator is a local development tool: a single binary that emulates
GCP APIs against a local BoltDB file, intended to run on a developer's own
machine for testing Terraform/gcloud/SDK workflows. It is **not** designed
to be exposed on a public network or used as a production service.

## Scope

- The emulator performs no real authentication by default — `access_token`
  values are accepted as-is, matching a local-only testing tool, not a
  production auth boundary.
- The opt-in real-execution backends (Phase 12-15: embedded Postgres for
  Cloud SQL, Docker containers for Cloud Run/Functions) run real
  processes/containers on the host. Treat any opted-in resource the same
  way you'd treat running `docker run`/a local Postgres yourself — don't
  point a real-backend Cloud SQL instance's generated credentials or a
  real-backend Cloud Run container's exposed port at anything you wouldn't
  otherwise run locally.
- Terraform state files (`.tfstate`/`.tfstate.backup`) generated against
  real-backend resources can contain plaintext connection details (see the
  `.gitignore` rule for this repo's own example POC). Never commit them.

## Reporting a vulnerability

If you find a security issue specific to this project (e.g. a way the
emulator's HTTP handlers could be abused beyond the "local dev tool"
threat model above), please open an issue describing it. There is no
formal disclosure SLA for this project at this stage.
