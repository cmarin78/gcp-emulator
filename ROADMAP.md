# Emulator roadmap

List of services/resources to add, ordered by dependencies and value for
real `gcloud` / Terraform / SDK workflows. Each phase is self-contained
(it can be merged and used without waiting for the next one).

Convention: each new service lives in `internal/services/<name>` with its
own `Register(mux)`, following the pattern already used by IAM/GCS/Compute.

## Current status

- IAM: service accounts, project-level IAM policy, predefined roles (static list),
  custom roles (with soft-delete + undelete), service account keys,
  resource-level IAM bindings (service accounts).
- Storage: buckets, objects (simple upload, download, listing, delete),
  bucket-level IAM bindings.
- Compute: static zones/machine types, instances (basic CRUD, start/stop), operations.
- Pub/Sub: topics, subscriptions, publish/pull/acknowledge.
- Secret Manager: secrets, versions, addVersion/access/destroy.
- Artifact Registry: repositories, longrunning operations.
- Cloud Run: v2 services (create/get/list/update/delete), longrunning operations.
- Cloud Functions: Gen2 functions (create/get/list/update/delete), longrunning operations.
- Cloud SQL: instances, databases, users, sqladmin#operation + operations.get.
- Firestore: databases (admin), simple document CRUD (fields as passthrough JSON).
- BigQuery: datasets, tables (synchronous, no Operation).

## Phase 1 — Complete Compute for real IaC

Today Terraform's `google_compute_instance` fails because the real API
requires `boot_disk` (a reference to a disk/image) and `network_interface`
(a reference to a network), and the emulator didn't model those resources.
This phase closes that gap.

| Resource | Depends on | Why | Effort |
|---|---|---|---|
| `compute.networks` (VPC) | — | Required by `network_interface` | S |
| `compute.subnetworks` | networks | Needed when using a custom-mode network | S |
| `compute.firewalls` | networks | Common in any networking module | S |
| `compute.images` (static catalog, e.g. debian-12) | — | Required by `boot_disk.initialize_params.image` | S |
| `compute.disks` | images | Required by an explicit `boot_disk` | M |
| `instances` (enrich) | networks, disks, images | Accept/return real `disks[]` and `networkInterfaces[]` | M |

✅ completed — `terraform apply` with `google_compute_instance` +
`google_compute_network` works without patches (just like
`google_storage_bucket` and `google_service_account` already did).

## Phase 2 — Advanced IAM ✅ completed

| Resource | Depends on | Why | Effort | Status |
|---|---|---|---|---|
| `iam.roles` (custom roles) | — | `google_project_iam_custom_role` | S | ✅ |
| `iam.serviceAccountKeys` | service accounts (already exists) | `google_service_account_key` | S | ✅ |
| Resource-level IAM bindings (bucket, service account) | storage/iam (already exist) | `google_storage_bucket_iam_*`, `google_service_account_iam_*` | M | ✅ |
| `resourcemanager.projects` (create/get) | — | Optional: today "project" is an opaque string and that already works fine; this is just for added realism | S (low priority) | — |

## Phase 3 — High-value standalone services ✅ completed

No dependencies on each other or on previous phases; can be done in any
order or in parallel.

| Service | Minimum resources | Effort | Status |
|---|---|---|---|
| Pub/Sub | topics, subscriptions (subscriptions depend on topics) | M | ✅ |
| Secret Manager | secrets, versions | S | ✅ |
| Artifact Registry | repositories | S | ✅ |

## Phase 4 — Serverless compute ✅ completed

| Service | Depends on | Note | Effort | Status |
|---|---|---|---|---|
| Cloud Run | — (accepts image references without validating against Artifact Registry) | services + revisions | M | ✅ |
| Cloud Functions | — (accepts source metadata without validating against Storage) | Gen2 is implemented on top of Cloud Run in real GCP; best done after Cloud Run | M | ✅ |

Both share the real `/v2/.../operations/{operation}` path; since the emulator
multiplexes everything on a single `http.ServeMux`, this is registered once
centrally (`server.RegisterV2Operations`) instead of per-service. Verified
with `terraform apply`/`destroy` against `google_cloud_run_v2_service` using
`cloud_run_v2_custom_endpoint` in the provider block.

## Phase 5 — Data ✅ completed

Independent of each other; each is a new package similar in size to the
current Compute/Storage.

| Service | Minimum resources | Effort | Status |
|---|---|---|---|
| Cloud SQL | instances, databases, users | L | ✅ |
| Firestore | databases, documents (simple CRUD) | L | ✅ |
| BigQuery | datasets, tables | M | ✅ |

Cloud SQL and Firestore mutations return their respective async-style
`Operation` resource (`sqladmin#operation` / `google.longrunning.Operation`),
always resolved (`status: DONE` / `done: true`), matching how the real APIs
shape responses even though the emulator does everything synchronously.
BigQuery's real API is synchronous, so its mutations return the resource
directly. Verified with `terraform apply`/`destroy` against
`google_bigquery_dataset` + `google_bigquery_table` using
`big_query_custom_endpoint` (note: the provider itself requires
`deletion_protection = false` on the table to allow `terraform destroy`).

## Phase 6 — Observability and governance (low priority)

| Service | Note | Effort |
|---|---|---|
| Cloud KMS | keyrings, cryptokeys — useful if Secret Manager later emulates real encryption | S |
| Cloud Logging | sink stub, no real log pipeline | S |
| Cloud Monitoring | metrics stub | S |

## Recommended order

1. Phase 1 (Complete Compute) — the most visible gap right now (found while testing Terraform).
2. Phase 3 (Pub/Sub, Secret Manager, Artifact Registry) — high value, zero dependencies, low/medium effort.
3. Phase 2 (Advanced IAM) — reinforces what already exists.
4. Phase 4 (Cloud Run / Functions) — more effort, larger API surface. ✅ done.
5. Phase 5 (data) — the most expensive to implement, best left until the service pattern is well polished. ✅ done.
6. Phase 6 — whenever a concrete use case needs it. ← next.
