# POC: real Terraform (init → plan → apply → destroy) against real-execution

Date: 2026-06-23
Environment: local emulator (`go run ./cmd/server`, port 8443, `docker-available=true`),
real Terraform (`hashicorp/google` v7.37.0), run on the user's machine
(PowerShell). Config in
[`examples/terraform-real-poc/`](examples/terraform-real-poc/main.tf).

## Summary

Full `init` → `plan` → `apply` → `destroy` cycle run against the
real-execution backend from Phases 12-15 (not the "shape-only" mode the
README already documented), using Terraform with no provider patches. 1
real issue was found and fixed during the first `destroy` pass; after the
fix, the full cycle finished green with no leftovers.

## What was tested

Unlike the existing README examples (Compute, Cloud Run, BigQuery, KMS),
which only create the resource's shape, this POC explicitly opts into the
real backend:

| Terraform resource | Opt-in used | Expected real backend |
|---|---|---|
| `google_sql_database_instance` (+`google_sql_database`, `google_sql_user`) | `settings.user_labels = { "emulator.dev/backend" = "real" }` | Real embedded Postgres engine (Phase 13) |
| `google_cloud_run_v2_service` | `labels = { "emulator.dev/backend" = "real" }` + real image (`nginx:alpine`) | Real Docker container (Phase 14) |

Both opt-ins use fields that already exist natively in the real provider
schema (`settings.user_labels`, `labels`) — no patch or extension to the
`google` provider was needed.

## Result: `init` / `plan` / `apply`

```
terraform init    → OK (hashicorp/google v7.37.0 installed)
terraform plan    → 4 to add, 0 to change, 0 to destroy
terraform apply   → Apply complete! Resources: 4 added, 0 changed, 0 destroyed.

Outputs:
cloud_run_uri               = "https://tf-real-svc-emulator-us-central1.a.run.app"
cloudsql_connection_name    = "tf-real-poc:us-central1:tf-real-pg"
cloudsql_instance_self_link = "projects/tf-real-poc/instances/tf-real-pg"
```

## Real-backend verification (not just "shape")

**Cloud SQL** — `GET /sql/v1beta4/.../instances/tf-real-pg` returned a
`realConnection` field with real connection data to the embedded engine:

```json
"realConnection": {
  "backend": "cloudsql-postgres-embedded",
  "host": "127.0.0.1",
  "port": 64447,
  "user": "postgres",
  "password": "..."
}
```

**Cloud Run** — `GET /v2/.../services/tf-real-svc` returned a
`realEndpoint` pointing at a real container, and hitting that URL
directly returned nginx's real welcome page:

```json
"realEndpoint": { "backend": "docker-container", "url": "http://127.0.0.1:64448" }
```

```
curl http://127.0.0.1:64448  →  <title>Welcome to nginx!</title>  (real nginx HTML)
```

**Real metrics (Phase 15)** — `GET /v3/.../timeSeries` returned 3 GAUGE
series with real data points, confirming that metric polling against real
backends also works end to end:

- `run.googleapis.com/container/cpu/utilizations` (2 points, real container)
- `run.googleapis.com/container/memory/utilizations` (2 points, ~25 MB real)
- `cloudsql.googleapis.com/database/postgresql/num_backends` (4 points, real `pg_stat_activity`, value 7)

## Result: `destroy`

### First attempt — failed

```
Error: cannot destroy service without setting deletion_protection=false
and running `terraform apply`
```

**Cause:** `google_cloud_run_v2_service` defaults to
`deletion_protection = true` as a guard from the provider itself (the same
kind of guard the README already documents for `google_bigquery_table`) —
it has nothing to do with the emulator, it's identical behavior against
real GCP. The initial `.tf` didn't declare it explicitly.

**Fix:** added `deletion_protection = false` to the
`google_cloud_run_v2_service` resource in
[`examples/terraform-real-poc/main.tf`](examples/terraform-real-poc/main.tf).

### Second attempt — OK

```
terraform apply -auto-approve    → Apply complete! Resources: 3 added, 1 changed, 0 destroyed.
terraform destroy -auto-approve  → Destroy complete! Resources: 4 destroyed.
```

(The "3 added" is because of the previous half-failed `destroy`: it had
already deleted the Cloud SQL instance before failing on Cloud Run, so the
second `apply` recreated it to complete the full cycle cleanly. The
behavior itself — real Postgres being created and destroyed correctly —
was already validated in the first pass.)

## Cleanup verification

```
curl .../sql/v1beta4/.../instances/tf-real-pg   → 404 instance not found
curl .../v2/.../services/tf-real-svc            → 404 service not found
docker ps                                        → no nginx container (only containers unrelated to the test)
```

No leftovers: the embedded Postgres engine and the nginx Docker container
were both fully released after `destroy`.

## Known limitation (not a bug, already documented for the shape-only case)

- `google_cloud_run_v2_service` requires explicit
  `deletion_protection = false` in the `.tf` to allow `terraform destroy` —
  a guard from the provider, not the emulator. It's now also annotated
  directly in this POC's `.tf` so it doesn't catch anyone off guard again.

## Files

- [`examples/terraform-real-poc/main.tf`](examples/terraform-real-poc/main.tf) — the Terraform config.
- [`examples/terraform-real-poc/README.md`](examples/terraform-real-poc/README.md) — how to run it.
