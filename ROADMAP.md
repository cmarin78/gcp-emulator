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
- Cloud KMS: keyrings, cryptokeys, cryptoKeyVersions (`:destroy` action); no delete on
  keyrings/cryptokeys, matching the real API.
- Cloud Logging: project-level sinks (stub, no real log pipeline).
- Cloud Monitoring: alert policies + empty `timeSeries` stub.

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

## Phase 6 — Observability and governance ✅ completed

| Service | Note | Effort | Status |
|---|---|---|---|
| Cloud KMS | keyrings, cryptokeys, no delete (real API behavior); cryptoKeyVersions `:destroy` | S | ✅ |
| Cloud Logging | sink stub, no real log pipeline | S | ✅ |
| Cloud Monitoring | alertPolicies + empty `timeSeries` stub | S | ✅ |

Cloud KMS faithfully omits delete for keyrings/cryptokeys (the real API has none);
Terraform's `google_kms_crypto_key` destroy instead calls `cryptoKeyVersions:destroy`
on each version, which is what's modeled here. Verified with `terraform apply`/`destroy`
against `google_kms_key_ring` + `google_kms_crypto_key` using `kms_custom_endpoint`.
Note: the `hashicorp/google` provider's KMS path templates already include a `v1/`
prefix for some calls (e.g. `cryptoKeyVersions` listing) while requiring the endpoint
to supply `v1/` for others (key ring/crypto key create) — the emulator normalizes the
resulting occasional `/v1/v1/...` request path centrally in `server.Handler()`.

## Recommended order

1. Phase 1 (Complete Compute) — the most visible gap right now (found while testing Terraform).
2. Phase 3 (Pub/Sub, Secret Manager, Artifact Registry) — high value, zero dependencies, low/medium effort.
3. Phase 2 (Advanced IAM) — reinforces what already exists.
4. Phase 4 (Cloud Run / Functions) — more effort, larger API surface. ✅ done.
5. Phase 5 (data) — the most expensive to implement, best left until the service pattern is well polished. ✅ done.
6. Phase 6 (observability/governance) — ✅ done.

## Phase 7 — Resource Manager, scheduling, DNS, and load balancing

New, unplanned phase, decided after the full 1-6 plan was completed and
verified end-to-end with gcloud CLI and Terraform. Ordered by
dependency/effort: items with no dependencies and low effort first, Load
Balancing last since it's by far the largest API surface.

| Service | Minimum resources | Depends on | Why | Effort | Status |
|---|---|---|---|---|---|
| Resource Manager | `projects` (create/get) | — | Today "project" is an opaque string accepted by every service; this models `google_project` for added realism. Low priority but trivial once started. | S | ✅ |
| Cloud Scheduler | `jobs` (CRUD), manual `:run` trigger | — | Completes the serverless combo already in place (Pub/Sub, Cloud Functions, Cloud Run); commonly paired with both. | S | ✅ |
| Cloud Tasks | `queues`, `tasks` (CRUD, create-task only — no real delivery) | — | Same family as Cloud Scheduler; low effort given the Pub/Sub pattern already exists to copy from. | S | ✅ |
| Cloud DNS | `managedZones`, `resourceRecordSets` (CRUD) | — | Very common in Terraform alongside Compute (`google_dns_managed_zone`, `google_dns_record_set`). | S/M | ✅ |
| Load Balancing | `backendServices`, `urlMaps`, `targetHttpProxies`/`targetHttpsProxies`, `forwardingRules`, `healthChecks` (all global, simplified — no regional/SSL variants initially) | `compute.networks`, `compute.instances` (already done) | Highest value for realistic Compute architectures, but the largest API surface of any service so far — same family of complexity as `compute.googleapis.com` itself. | L | ✅ |

Cloud Scheduler/Tasks and Cloud DNS don't model real delivery/resolution (no
actual HTTP calls fired by Scheduler, no actual DNS resolution) — same
"shape-compatible, not behavior-complete" approach used by Pub/Sub and the
other async services. Load Balancing reuses Compute's own `Operation` shape
(`internal/server/operations.go`, not the simpler `google.longrunning.Operation`
used by the other three) since its resources live under
`compute.googleapis.com` and gcloud polls/parses operations the same way it
does for networks/instances; it's similarly shape-only and won't actually
proxy traffic.

✅ Verified 2026-06-18: built and smoke-tested on a real machine (Go 1.26,
ephemeral binary/db, see `E2E_TEST_REPORT.md` for the full Phase 7 section).
All five services — Resource Manager, Cloud Scheduler (`:run`), Cloud Tasks
(`:pause`), Cloud DNS (zone + change + rrsets), Load Balancing (full chain:
healthCheck → backendService → urlMap → targetHttpProxy → forwardingRule) —
worked correctly on first try, zero bugs found. No leftover artifacts
(`git status --short` clean of binaries/dbs after cleanup).
