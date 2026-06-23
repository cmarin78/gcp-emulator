# Terraform real-execution POC

Exercises the emulator's real-backend opt-in (Phase 12-15) through plain,
unpatched Terraform HCL — no provider schema changes required:

- `google_sql_database_instance` (+ database + user) opts into a real
  embedded Postgres engine via `settings.user_labels`.
- `google_cloud_run_v2_service` opts into a real Docker container
  (`nginx:alpine`) via the resource's top-level `labels`.

Both opt-in mechanisms use fields that already exist on the real Terraform
`google` provider resources — see the main [README.md](../../README.md#using-terraform-against-the-emulator)
for the equivalent shape-only examples (Compute, Cloud Run, BigQuery, KMS).

## Prerequisites

- The emulator server running locally on `:8443` (`go run ./cmd/server`
  from the repo root).
- A real Docker daemon reachable from the machine running the emulator
  (required for the Cloud Run real container; Cloud SQL's embedded
  Postgres does not need Docker, but does need network access on its
  first run to download the Postgres binary).
- Terraform >= 1.5 and the `hashicorp/google` provider (`~> 7.0`).

## Run it

```bash
terraform init
terraform plan
terraform apply -auto-approve
# ... verify (see ../../TERRAFORM_REAL_POC.md) ...
terraform destroy -auto-approve
```

Results of an actual `init`/`plan`/`apply`/`destroy` run are documented in
[`TERRAFORM_REAL_POC.md`](../../TERRAFORM_REAL_POC.md) at the repo root.
