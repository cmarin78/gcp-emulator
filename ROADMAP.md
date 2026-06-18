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

## Phase 8 — CI/CD, extended networking, managed data stores

New, unplanned phase. Ordered the same way as Phase 7: cheapest/most
self-contained items first, largest API surface last.

| Service | Minimum resources | Depends on | Why | Effort | Status |
|---|---|---|---|---|---|
| Cloud Build | `builds` (create/get/list, status always `SUCCESS`) | — | Ubiquitous in real Terraform/CI pipelines; trivial shape (no real build execution). | S | ✅ |
| Compute networking extensions | `compute.routers`, `compute.routes`, Cloud NAT config on routers | `compute.networks` (already done) | Rounds out the networking family already in place (networks/subnets/firewalls); common alongside Load Balancing. | S | ✅ |
| Cloud Armor | `securityPolicies` (global), referenced from `backendServices` | `loadbalancing.backendServices` (already done) | Direct extension of Load Balancing; low effort since it's a single new resource type. | S | ✅ |
| Memorystore (Redis) | `instances` (CRUD) | — | Same CRUD-with-Operation pattern as Cloud SQL; common pairing with Compute/Cloud Run. | M | ✅ |
| Cloud Spanner | `instances`, `databases` | — | Similar complexity to Cloud SQL (already implemented, good template to copy from). | M | ✅ |
| GKE (Kubernetes Engine) | `clusters`, `nodePools` (CRUD, shape-only — no real cluster) | `compute.networks` (already done) | High demand in real-world Terraform, but the largest surface in this phase; same "shape-compatible, not behavior-complete" approach as Cloud Run. | L | ✅ |

Lower priority, deliberately left out of this phase's table (large surface,
narrower audience for a local emulator): Vertex AI, App Engine, Cloud
Composer, Dataflow/Dataproc. Worth revisiting as a Phase 9 if there's
specific demand.

As with prior phases: mutations on the async-style services here
(Cloud Build, Memorystore, Spanner, GKE) return their respective
`Operation`-shaped resource, resolved synchronously, following the same
"shape-compatible, not behavior-complete" approach used throughout this
project.

✅ Verified 2026-06-18: built and smoke-tested live on a real machine (Go
1.26, ephemeral binary/db, see `E2E_TEST_REPORT.md` for the full Phase 8
annex). All six services — Cloud Build, Compute routers/routes/NAT, Cloud
Armor, Memorystore, Cloud Spanner (instance + database via DDL parsing),
GKE (cluster + nodePool) — worked correctly via direct HTTP calls. One real
bug was found and fixed during this pass: `http.ServeMux` panicked at
startup on a duplicate route pattern (`GET .../operations/{operation}`)
registered by both Memorystore and GKE, colliding with one already owned by
Artifact Registry on the shared `/v1/*` mux — not caught by `go build`/`go
vet`, only by actually running the binary. Fixed by removing the duplicate,
now-dead routes/handlers from both new services (their mutations already
resolve synchronously, so no client needs to poll). No leftover artifacts
after cleanup.

✅ Re-verified 2026-06-18 with **real `gcloud`/Terraform clients** (not
just direct HTTP calls) — see the "Phase 8 round 2" annex in
`E2E_TEST_REPORT.md`. A real client exercises follow-up calls a hand-built
smoke test wouldn't think to make (label reconciliation, post-apply
consistency reads, separate DDL-execution calls), and this round surfaced
4 further real bugs, all fixed: Cloud Armor's missing `setLabels` endpoint,
a GKE provider-plugin panic caused by an incomplete `Cluster`/`NodeConfig`
JSON shape (fixed by populating the substructures real GKE always
returns), a missing `instanceGroupManagers` endpoint that broke the GKE
provider's post-apply consistency check, and a missing Spanner database
DDL endpoint. Final `terraform apply` → `terraform destroy` cycle: 9/9
resources created and destroyed cleanly, zero errors.

## Automated test suite + CI

New, unplanned addition following Phase 8: every prior phase had only been
verified through manual/ephemeral runs against a real `gcloud`/Terraform
client (see the annexes above and `E2E_TEST_REPORT.md`). None of that was
captured as a repeatable, automated regression check.

- Added `internal/testutil` (a shared `httptest`-based harness: an
  in-memory BoltDB per test via `t.TempDir()`, plus a `DoJSON` helper for
  making requests and decoding responses) and a `*_test.go` smoke-test
  file for all 23 service packages, covering each service's main
  create/get/list/update/delete lifecycle and its most important
  validation/error paths (missing required fields, not-found lookups).
  Note: this initial pass did **not** actually cover duplicate-create
  conflicts despite the claim below — see "Emulation-gap audit" for the
  follow-up that added that coverage. `cmd/server` also has a registration test
  that wires every service onto a single mux and asserts no route
  collisions panic at startup — a direct regression test for the
  duplicate-route bug found and fixed during Phase 8.
- Added `.github/workflows/ci.yml`: a GitHub Actions workflow that runs
  `go build ./...`, `go vet ./...`, and `go test ./... -v -race` on every
  push/PR, on Go 1.22 (the toolchain version `go.mod` declares).
- Verified 2026-06-18 on a real machine (Windows, Go 1.26 installed locally
  — newer than the `go.mod` floor of 1.22, confirming the codebase doesn't
  rely on anything past 1.22): `go vet ./...` clean, `go test ./... -v`
  passing across all 23 service packages plus `cmd/server`. Two rounds of
  real test-writing bugs were caught and fixed in this pass (not source
  bugs): a `compute_test.go` package compile error from a missing local
  `Operation` type, and six test-logic errors in the same file from
  decoding `Operation`-wrapper responses directly into resource structs
  for networks/subnetworks/firewalls/disks/routers/routes (this codebase's
  insert/delete handlers return an `Operation`, not the resource — the
  real resource is always a separate `GET` away), plus one incorrect
  assumption about access-config IP synthesis only happening when the
  request already includes an `accessConfigs` entry to fill in.

## Emulation-gap audit — duplicate-create conflicts

New, unplanned pass following the automated test suite addition above,
triggered by re-checking whether "shape-compatible" emulation was missing
any behavior real clients actually depend on. The specific gap targeted:
real GCP APIs return `409 ALREADY_EXISTS` when a client tries to create a
resource under a client-specified ID/name that already exists, instead of
silently overwriting it — Terraform and `gcloud` both rely on this (e.g. to
surface a clear error on `terraform apply` re-runs against drifted state).
A prior pass had added this check to some handlers but not audited it
systematically across all 23 packages, and the test-suite section above
incorrectly claimed blanket coverage that didn't exist yet.

Real gaps found and fixed in production code (handlers that silently
overwrote on a duplicate client-specified ID, now returning 409):

- `iam.go` `createCustomRole` — had no check at all (`createServiceAccount`
  in the same file already had one, which is what made the asymmetry easy
  to miss on a casual read).
- `firestore.go` `createDatabase` — had no check (`createDocument` in the
  same file already had one).
- `cloudtasks.go` `createTask` — had no check for the case where the client
  supplies an explicit task name (the auto-generated-ID path was already
  fine, since the emulator's own sequence counter can't collide).
- `compute/routing.go` and `loadbalancing.go` — fixed in the prior
  "Priorizar y corregir las brechas encontradas" pass (#49), before this
  audit's test-writing phase; see git history for the exact diffs.

Packages confirmed correct on direct source inspection, no fix needed:
gcs (`createBucket`), pubsub (`createTopic`, `createSubscription`),
secretmanager (`createSecret`), artifactregistry (`createRepository`),
clouddns (`createZone`), cloudscheduler (`createJob`), compute.go/network.go
(`instance`, `network`, `subnetwork`, `firewall`, `disk`), iam.go
(`createServiceAccount`), firestore.go (`createDocument`).

Intentionally left unchanged (not gaps): `cloudbuild` and `monitoring`
mutations use server-generated IDs, which structurally cannot collide with
a client-specified name. `gcs` object upload (`uploadObject`) intentionally
has no check — re-uploading the same object name is supposed to replace it,
matching real GCS semantics.

Test coverage added for every fix and every already-correct handler above:
a `TestDuplicateCreateConflict`/`TestDuplicateCreateConflicts` function in
each of `compute_test.go`, `iam_test.go`, `gcs_test.go`, `pubsub_test.go`,
`secretmanager_test.go`, `artifactregistry_test.go`, `clouddns_test.go`,
`cloudtasks_test.go`, `cloudscheduler_test.go`, and `firestore_test.go` —
asserting a second create call with the same client-specified ID returns
409, immediately after a first call that returns 200.
