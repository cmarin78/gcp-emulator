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
- Cloud Logging: project-level sinks, plus real `entries:write`/`entries:list`
  backed by `internal/activity` (Phase 11).
- Cloud Monitoring: alert policies, plus `timeSeries` populated from real
  activity recorded by Cloud Scheduler/Tasks/Pub/Sub (Phase 11).

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

## Phase 9 — Instance management, networking add-ons, serverless glue ✅ completed

All 8 items below are implemented, tested, and verified building/passing on
a real machine (Go, `go build`/`go vet`/`go test ./...` all clean across
every package, including `cmd/server`'s full-mux registration test).
Ordered cheapest/most-self-contained first, same convention as prior
phases.

| Service | Minimum resources | Depends on | Why | Effort | Status |
|---|---|---|---|---|---|
| Compute instance templates | `compute.instanceTemplates` (CRUD, immutable like the real API — no update, only create/delete) | `compute.disks`/`compute.images`/`compute.networks` (already done) | Extremely common in real Terraform (`google_compute_instance_template`); almost always paired with MIGs below. Reuses most of the existing `instances` shape. | S | ✅ |
| Compute managed instance groups (MIGs) | `compute.instanceGroupManagers`, `compute.autoscalers` (zonal/regional, CRUD, shape-only — no real scaling) | instance templates (above) | The standard way real Terraform deploys fleets of VMs (`google_compute_instance_group_manager`, `google_compute_autoscaler`); a large gap given Compute is otherwise our most complete service. | M | ✅ |
| Cloud Run Jobs | `/v2/projects/{p}/locations/{l}/jobs` (CRUD + manual `:run`, distinct resource from the already-implemented Cloud Run *services*) | cloudrun package (already done, same patterns) | Batch/one-off workloads are a different Terraform resource (`google_cloud_run_v2_job`) from services; cheap to add given the Cloud Run package already exists as a template. | S | ✅ |
| Serverless VPC Access connectors | `/v1/projects/{p}/locations/{l}/connectors` (CRUD) | `compute.networks` (already done) | Required by `google_vpc_access_connector`, which Cloud Run/Cloud Functions configs frequently reference for private VPC egress. | S | ✅ |
| Filestore | `/file/v1/projects/{p}/locations/{l}/instances` (CRUD, NFS shape only) | — | Common pairing with GKE/Compute for shared storage (`google_filestore_instance`); same CRUD-with-Operation pattern as Memorystore/Spanner, cheap to copy. | M | ✅ |
| Workflows | `/v1/projects/{p}/locations/{l}/workflows` (CRUD, `:execute` no-op) | — | Lightweight orchestration glue increasingly used alongside Cloud Run/Functions/Eventarc; small API surface. | S | ✅ |
| Eventarc | `/v1/projects/{p}/locations/{l}/triggers` (CRUD, no real event delivery) | pubsub, cloudrun (already done) | Standard event-routing layer wiring Pub/Sub/Cloud Storage events to Cloud Run; rounds out the serverless story. | M | ✅ |
| Cloud CDN | `cdnPolicy` sub-resource on existing `backendServices` (PATCH only, no new top-level resource) | `loadbalancing.backendServices` (already done) | Trivial extension — a single nested field most `google_compute_backend_service` configs set when fronting static content. | S | ✅ |

As with Phase 8, the instance template/MIG/VPC-connector/Filestore/
Workflows/Eventarc mutations return their respective `Operation`-shaped
resource (the simpler `google.longrunning.Operation` shape, same as
Memorystore/Spanner/GKE/Cloud Build — see Phase 8 notes), resolved
synchronously; Cloud CDN reuses Load Balancing's `compute`-style Operation
(`status: DONE`) since it's a field on an existing Compute resource, not a
new top-level one.

One real bug was found and fixed during this phase's build/test pass (same
class as the Phase 8 duplicate-route incident, not caught by `go
build`/`go vet`): Filestore's `instances` resource path
(`projects/{project}/locations/{location}/instances`) is byte-for-byte
identical to Memorystore's, so registering both on the shared bare `/v1/*`
prefix panicked `http.ServeMux` with a duplicate-route error the first
time `cmd/server`'s full-registration test was updated to actually include
the four new Phase 9 packages (it hadn't been updated yet when the
individual per-package tests were first written and passing, which is why
this stayed invisible until that omission was caught and fixed in the same
pass). Resolved by mounting Filestore on its own `/file/v1/*` prefix —
the same disambiguation technique already used for Storage (`/storage/v1/`)
and Compute (`/compute/v1/`) — rather than the bare `/v1/*` most other
services share. Point Terraform's `filestore_custom_endpoint` provider
field at `<emulator>/file/v1/`.

## Phase 10 — Networking, security & governance add-ons ✅ completed

All 6 items below are implemented, tested, and verified building/passing on
a real machine (`gofmt`/`go build`/`go vet`/`go test ./...` all clean across
every package, including `cmd/server`'s full-mux registration test — no
route collisions among the 6 new packages and the 27 pre-existing ones).
Reviewed after Phase 9 by auditing the 27 packages then implemented against
the resource types that show up most often in real-world `google` Terraform
configs alongside what we already emulate (Compute networking, IAM, Cloud
SQL, Memorystore, GKE). The gap that stood out: every one of these is a
small, frequently-referenced *supporting* resource that other
already-emulated services depend on in real IaC, rather than a new
standalone product. Implemented cheapest/most-connected-to-existing-code
first, same convention as prior phases.

| Service | Minimum resources | Depends on | Why | Effort | Status |
|---|---|---|---|---|---|
| Service Networking (private VPC connection) | `services/{service}/connections` (CRUD, `google_service_networking_connection`) | `compute.networks` (already done) | The standard way Terraform wires Cloud SQL/Memorystore/Filestore to a VPC for private IP; extremely common pairing with services we already emulate, currently has no emulated counterpart at all. | S | ✅ |
| Compute network peering | `networks.addPeering`/`networks.removePeering` as a sub-resource of the existing `networks` resource (`google_compute_network_peering`) | `compute.networks` (already done) | Small, additive extension to an existing resource (same pattern as Cloud CDN on `backendServices` in Phase 9); common in multi-VPC/shared-VPC Terraform layouts. | S | ✅ |
| Identity-Aware Proxy (IAP) | `iap.googleapis.com` IAP settings + `iap_brand`/`iap_client` shapes (CRUD, shape-only) | `compute` (already done) | Frequently paired with load balancers and GKE in security-conscious Terraform (`google_iap_brand`, `google_iap_client`, backend service IAP settings). | S | ✅ |
| Organization Policy | `policies` on `projects/{p}` / `organizations/{o}` (CRUD, `google_org_policy_policy`) | `resourcemanager` (already done) | Near-universal in landing-zone/governance Terraform modules; small API surface, reuses the existing resourcemanager package's project resource. | S | ✅ |
| Cloud Billing Budgets | `billingAccounts/{account}/budgets` (CRUD, `google_billing_budget`) | — | Common in cost-governance Terraform; self-contained, no real billing data needed since budgets are just thresholds + notification config. | S | ✅ |
| Certificate Manager | `projects/{p}/locations/{l}/certificates`, `certificateMaps` (CRUD, no real cert issuance — always report ACTIVE) | `loadbalancing` (already done) | `google_certificate_manager_certificate`/`certificate_map` are the modern way to attach TLS to load balancers/CDN, which we just added in Phase 9; closes that loop. | M | ✅ |

Service Networking and Certificate Manager mutations return the simpler
`google.longrunning.Operation` shape (same convention as
vpcaccess/workflows/eventarc/filestore), and Compute network peering reuses
Compute's own `Operation` shape (`s.ops.Done`) since it's a sub-resource of
the existing `networks` resource. Organization Policy and Cloud Billing
Budgets are a deliberate exception to the "Operation everywhere" pattern:
both are genuinely synchronous in the real API (no LRO wrapper at all), so
their handlers return the resource directly — matching real API behavior
rather than the codebase's more common async-style convention.

One bug was found and fixed during this phase's build/test pass — unlike
the Phase 8/9 incidents, this one was a test-logic bug, not a production
bug: `TestNetworkPeering` initially failed because it reused the same `net
Network` variable across two sequential `GET` calls. After `removePeering`
empties the peering list, the second `GET` response omits the
`peerings` field entirely (`omitempty` on an empty slice) — and
`json.Unmarshal` only overwrites fields actually present in the decoded
JSON, leaving the previous (non-empty) `Peerings` value from the first
decode untouched on the reused variable. The `removePeering` handler itself
was correct throughout. Fixed by decoding the second `GET` into a fresh
variable.

### Phase 11 — Behavioral logic layer (in progress)

Everything through Phase 10 is "shape-compatible, not behavior-complete":
resources are stored and returned correctly, but the emulator doesn't
*act* on them. Phase 11 closes part of that gap without adding any new
runtime dependency — pure Go logic over data already in BoltDB, the same
spirit as how tools like Packet Tracer or GNS3 simulate protocol behavior
without real hardware.

| Area | Behavior added | Status |
|---|---|---|
| Pub/Sub, Scheduler, Tasks | Real delivery — push subscriptions, cron fires, and task dispatch all become genuine outbound HTTP calls on schedule/trigger, not just state transitions. | **Done.** Cloud Scheduler: dependency-free cron evaluator (`internal/cronexpr`) drives a per-job goroutine that fires real HTTP requests to `httpTarget` on schedule, resumes on restart, and responds to `:run`/`:pause`/`:resume`. Cloud Tasks: `createTask` dispatches `httpRequest` for real (respecting `scheduleTime` and a `PAUSED` queue), incrementing `dispatchCount`. Pub/Sub: subscriptions with `pushConfig.pushEndpoint` deliver via real HTTP POST in the standard push wire format instead of queuing for pull; `modifyPushConfig` toggles push/pull mode. `pubsubTarget` on Scheduler jobs and `appEngineHttpRequest` on Tasks remain shape-only (would need to route through the emulator's own Pub/Sub or a real App Engine, respectively). |
| IAM / Org Policy | Enforcement middleware — requests actually get rejected when a policy/role would deny them, instead of every request silently succeeding. | **Done.** New `internal/iamenforce` package wraps the whole server (`cmd/server/main.go`, around `srv.Handler()`) with an **opt-in** project-level IAM check: only requests carrying a new `X-Emulator-Caller: <type>:<id>` header are enforced (real gcloud/Terraform clients never send it, so every existing flow and the 30+ existing service test suites are unaffected). When present, write requests (POST/PUT/PATCH/DELETE) and `:setIamPolicy` calls are checked against the project's stored IAM policy (the same `iam.policies` bucket `iam.go` already writes via `setIamPolicy`): `roles/owner` covers everything including `:setIamPolicy`, `roles/viewer` covers nothing (reads are never enforced anyway), everything else (editor, predefined service-admin roles, custom roles) is approximated as write-tier — a documented shape-level simplification. A project with no policy ever set is implicitly allowed (mirrors the real API's implicit project-creator-owner default, avoiding a lockout before any caller has ever called `setIamPolicy`). Separately, two concrete Org Policy constraints became real instead of inert CRUD: a new `orgpolicy.Denies(db, project, constraint)` helper (boolean-constraint semantics: `denyAll`, or `enforce` without `allowAll`) is called from `iam.go`'s `createServiceAccountKey` (`constraints/iam.disableServiceAccountKeyCreation`) and `compute.go`'s `insertInstance` (`constraints/compute.vmExternalIpAccess`, checked only when the request actually requests an `accessConfig`), both returning `412 FAILED_PRECONDITION` when enforced. |
| Networking | Real reachability evaluation across firewalls/peerings/routes (a `testIamPermissions`-style "can A reach B" trace), plus real DNS resolution for Cloud DNS zones. | **Done.** Real GCP has no `testIamPermissions`-style endpoint under Compute for this — the faithful analog is the Network Management API's Connectivity Tests (`google_network_management_connectivity_test` in Terraform), so this shipped as a new `internal/services/networkmanagement` package (`POST/GET/PATCH/DELETE .../connectivityTests`, plus the real API's `:rerun` custom method) instead of a Compute endpoint. Creation/update/rerun all evaluate reachability synchronously (no Operation wrapper, the same simplification `billingbudgets`/`orgpolicy` already use for non-Compute APIs) by reading Compute's `compute.networks`/`compute.firewalls` buckets directly (the avoid-an-import-cycle technique `internal/iamenforce` and `billingbudgets` already use), applying real GCP defaults — EGRESS allowed by default, INGRESS denied by default, lowest `priority` firewall rule wins within a direction — and real network-peering adjacency (an `ACTIVE` peering on either side connects two networks). This is deliberately narrower than the real Reachability Analyzer: it does not consult routes/routers, and firewall matching is scoped to `sourceRanges`/protocol/port since `compute.Firewall` has no `targetTags`/`destinationRanges` fields yet — both are documented simplifications, not oversights. `rerunTest` re-evaluates against current state, so a firewall added after a test was created changes the result on the next rerun, which is the actual point of this resource (catching "did I just lock myself out"). Separately, Cloud DNS gained `rrsets:resolve` (`internal/services/clouddns/resolve.go`), an **emulator-only extension** clearly documented as such — real Cloud DNS has no resolution endpoint at all (resolution happens via the VPC's internal resolver, not `dns.googleapis.com`). It walks the zone's `rrsets` for an exact `(name, type)` match, and failing that follows a same-zone CNAME chain (cycle-detected, capped at 10 hops), returning `NOERROR`/`NXDOMAIN`/`CNAME_EXTERNAL` (the last when a CNAME's target falls outside the zone's `dnsName`, since the emulator doesn't resolve across zones). |
| Eventarc | Real CloudEvent delivery to triggers — needs a new "publish a CloudEvent" API surface that doesn't exist yet in the emulated API. | **Done.** Real Eventarc has no publish endpoint at all — CloudEvents arrive automatically from the underlying source (Audit Logs, Pub/Sub, Cloud Storage, etc.), never via a direct call to `eventarc.googleapis.com`. So this shipped as `triggers/{trigger}:publishEvent` (`internal/services/eventarc/dispatch.go`), an explicitly documented **emulator-only extension**, following the same custom-method routing pattern (`strings.Cut` on a whole-segment wildcard) already used for Cloud Scheduler's `:run`/`:pause`/`:resume` and `networkmanagement`'s `:rerun`. Publishing a CloudEvent checks it against the trigger's `eventFilters` first (exact-match by default; the real `match-path-pattern` operator is also implemented, supporting `*`/`**` segment wildcards) and, only on a match, performs a genuine outbound HTTP POST to the resolved destination in CloudEvents binary content mode (`ce-id`/`ce-source`/`ce-type`/... headers, body = the event's `data`) — the same "really dispatch, don't just transition state" precedent Pub/Sub push delivery already set. `Destination` gained a real, modeled `httpEndpoint` variant (`destination.http_endpoint.uri` in `google_eventarc_trigger`, mirroring the real API) alongside the existing `cloudRun` one: `httpEndpoint` points at an arbitrary URL (always reachable, the same mechanism Cloud Scheduler/Tasks use, and what makes delivery deterministically testable against an `httptest.Server`); `cloudRun` is resolved by reading `cloudrun.services` directly (the avoid-an-import-cycle technique used throughout this phase) to its `uri`, which only succeeds if that Cloud Run service actually exists. No retries/dead-lettering, matching Pub/Sub push's already-accepted limitation; delivery success/failure is recorded via `internal/activity.RecordLog`/`IncrCounter`, observable through Cloud Logging/Monitoring like every other Phase 11 real-dispatch path. |
| Workflows | A real interpreter for the basic Workflows syntax (steps, conditionals, calls) instead of a fixed terminal status. | **Done.** `createExecution` (`internal/services/workflows/interpreter.go`) now really interprets `sourceContents` when it's a JSON workflow definition: a top-level object of named subworkflows (`main` plus any `call` targets), each with `params` and an ordered `steps` array of single-key `{stepName: body}` objects — the real JSON form Workflows source can take, not an emulator invention. Interpreted subset: sequential step execution, `assign` (ordered variable assignment), `switch` (ordered conditional return/next, with the step's own `next`/`return` acting as the implicit "else"), an unconditional `next` (including the `"end"` target), `return`, and `call` (either the `sys.log` builtin, wired to `internal/activity.RecordLog` like every other Phase 11 real-dispatch path, or another subworkflow in the same document). `"${...}"`-wrapped strings are evaluated by a small hand-written expression evaluator (literals, dotted property access into decoded JSON objects, arithmetic/comparison/logical operators) — no new dependency, per this phase's ground rules. A step-count cap (10000) fails an execution that loops forever instead of hanging the request. Real-world Workflows source is more commonly YAML, which isn't parsed here (a YAML parser would be this project's first non-bbolt runtime dependency): a `sourceContents` that isn't valid JSON for this shape falls back unchanged to the original behavior (`SUCCEEDED`, result = the input argument echoed back) — an explicit, tested fallback (`TestExecutionFallsBackForNonJSONSourceContents`), not a silent gap. A failed interpretation (e.g. `next` referencing an unknown step) now resolves to a real `FAILED` state with `execution.error.payload` set, instead of always succeeding. |
| Cloud Armor / Load Balancing | Real rule evaluation against simulated request attributes. | **Done.** Real Cloud Armor has no public "evaluate this request" endpoint — enforcement happens invisibly in front of the load balancer. So, following the same emulator-only-extension precedent as Cloud DNS's `rrsets:resolve` and Eventarc's `triggers:publishEvent`, this shipped as `securityPolicies/{name}:evaluate` (`internal/services/loadbalancing/evaluate.go`), routed via the same whole-segment `:action` wildcard-plus-`strings.Cut` technique `networkmanagement`'s `connectivityTests:rerun` already uses. Given a simulated `sourceIp`, it walks the policy's `rules` in ascending `priority` order (lower number wins, matching the real API) and returns the action of the first rule whose `match.config.srcIpRanges` contains that IP (CIDR-aware, `"*"` as catch-all), defaulting to `allow` when nothing matches — mirroring the implicit default rule every real policy carries. `SecurityPolicyRule.Match` became a real typed shape (`SecurityPolicyRuleMatch`/`Config`/`Expr`, previously an inert `json.RawMessage`) so `config.srcIpRanges` is actually evaluable; a rule's CEL `match.expr.expression` is still accepted and stored (so a real `google_compute_security_policy_rule` using CEL round-trips through create/get/list) but is documented as never evaluated — no CEL library, per this phase's no-new-dependency rule, the same boundary Workflows' interpreter draws for non-JSON `sourceContents`.
| Autoscaler, Billing Budgets | Real math — instance-group scaling decisions and budget accrual computed from actual usage signals instead of being static. | **Done.** Compute autoscalers: `autoscalingPolicy.min/maxNumReplicas` now has a real, immediate effect on its target MIG's `targetSize` instead of being stored-but-inert metadata (`internal/services/compute/instancegroups.go`). A new `migNameFromTarget` resolves `autoscaler.target` to a MIG name (handles both a bare name and a full `.../instanceGroupManagers/{name}` reference, since `normalizeRef` is currently a no-op). `clampToAutoscalerPolicy` enforces the bounds (a zero limit, meaning unset, is never enforced) at four call sites: resizing a MIG, PATCHing a MIG's `targetSize`, inserting an autoscaler (clamps an already-oversized MIG immediately), and PATCHing an autoscaler's policy (tightening `maxNumReplicas` below the current size shrinks the group right away, without waiting for a manual resize). Billing Budgets: `getBudget`/`listBudgets` now estimate real spend instead of treating `thresholdRules` as metadata that's never evaluated (`internal/services/billingbudgets/spend.go`). Spend is computed from actual Compute instances in the budget's filtered projects — reading `compute.instances` directly (the same avoid-an-import-cycle technique `internal/iamenforce` uses) and approximating cost as an hourly rate per machine-type family times hours since `creationTimestamp` (deliberately not real GCP pricing). The real API doesn't expose spend on the Budget resource itself (that's BigQuery billing export, out of scope), so the observable effect is a real Cloud Logging `WARNING` entry via `internal/activity.RecordLog` the first time a `thresholdPercent` (a 1.0-based fraction, matching the real API) is crossed, deduplicated per `(budget, threshold)` pair via a mutex-guarded map on `Service` so re-reading the budget doesn't reproduce the notification. |
| Logging / Monitoring | Populated from the internal events all of the above now generate, instead of being empty stubs. | **Done.** New `internal/activity` package (in-memory, capped, dependency-free) is the shared event recorder both sides depend on, avoiding an import cycle between the producer services and Logging/Monitoring. Cloud Scheduler dispatch, Cloud Tasks dispatch, and Pub/Sub push delivery now each call `activity.RecordLog`/`activity.IncrCounter` right after their real HTTP attempt, recording success/failure (severity INFO/ERROR). Cloud Logging gained real `entries:write`/`entries:list` endpoints (previously didn't exist at all — only sinks CRUD did) backed by `activity.RecordLog`/`ListLogs`, with a simple substring `filter`. Cloud Monitoring's `listTimeSeries` no longer returns a hardcoded empty list — it reads `activity.ListTimeSeries(project, metricType)`, parsing the real API's `metric.type="..."` filter syntax, and shapes points as `CUMULATIVE`/`INT64` `monitoring.v3.TimeSeries`. |

This phase has no Docker/engine dependency and keeps the project's
"single portable binary" property intact — it's a pure complexity/effort
question, not an architecture question.

### Phase 12 — Pluggable real-execution foundation

The foundation that Phases 13+ (real compute, real SQL, real network
traffic) build on. This phase itself doesn't add any user-visible real
backend — it adds the mechanism so that later phases can, without making
Docker a hard dependency of the project.

- **Backend interface. Done.** New `internal/realbackend` package defines
  `Backend` (`Kind() string`, `FootprintMB() int`, `Stop() error`) — the
  shape every Phase 13+ real backend (Docker-run Cloud Run/Functions, an
  embedded Postgres for Cloud SQL) will implement. No concrete `Backend`
  exists yet; this phase ships only the interface and the machinery
  around it.
- **Per-resource opt-in. Done.** `realbackend.WantsReal(r, labels)`
  checks for `?backend=real` on the request or a
  `labels["emulator.dev/backend"] == "real"` entry in the body — either
  is enough, neither is required. No existing caller (gcloud, Terraform,
  the 30+ existing test packages) sends either, so today's zero-cost
  shape-only behavior is unchanged for everyone until Phase 13+ wires a
  concrete backend behind this check.
- **Docker/engine detection with fallback. Done.** `realbackend.DetectDocker`
  shells out to `docker version` (no Docker Go SDK added — consistent
  with this project's no-new-dependency convention) with a 3s timeout,
  returning `Available`/`Detail` rather than an error, so probing is
  always safe to call speculatively. Logged once at startup; Phase 13+
  backends call it again per opt-in request and fall back to shape-only
  when it's false.
- **Resource governor, budget-based rather than a flat count. Done.**
  `realbackend.Governor` tracks admitted backends by RAM footprint
  against a detected budget:
  - `realbackend.DetectHostRAMMB` reads `/proc/meminfo` on Linux,
    PowerShell's `Get-CimInstance Win32_ComputerSystem` on Windows, and
    `sysctl hw.memsize` on macOS (shell-outs, not a new dependency, same
    approach as Docker detection) to get total host RAM.
    `DetectBudgetMB` takes 25% of that as the working budget, floored at
    a 512MB conservative default whenever detection fails or yields too
    little — startup never blocks or refuses on this.
  - `Governor.Admit(id, backend)` is budget-aware: it admits immediately
    if there's room, otherwise evicts least-recently-used backends
    (calling `Stop` on each) until there's room or nothing left to
    evict, in which case it refuses rather than over-committing. A
    backend whose own footprint exceeds the *entire* budget is refused
    outright.
  - **Documented simplification:** the roadmap calls for evicting
    LRU-among-*idle* backends specifically. This foundation has no
    in-use signal beyond `Governor.Touch`'s timestamp — what "currently
    in use" means depends on what a concrete backend actually does (an
    open connection vs. closed, mid-request vs. idle), which doesn't
    exist until Phase 13+ defines one. So for now every admitted backend
    is plain-LRU eviction-eligible; a backend that needs stronger
    in-use protection calls `Touch` on every active use.
  - `Governor.IdleTimeout()` scales linearly between 5min (full budget
    pressure) and 30min (no pressure), for Phase 13+ backends to poll
    instead of hardcoding one number.
  - `GET /admin/real-backends` (registered unconditionally at startup,
    `internal/realbackend/admin.go`) returns `{budgetMb, usedMb,
    backends: [...]}` — reports zero backends today, but is a stable
    place for tooling to point at before Phase 13+ adds a real one.
  - `EMULATOR_MAX_REAL_BACKENDS`/an explicit RAM-ceiling override is
    **not yet implemented** — deferred to whichever Phase 13+ backend
    first needs it, since there's nothing to cap yet.
- **Note:** within the committed scope below, every real backend has a
  no-Docker fallback (Postgres is embeddable outright). Flavors that have
  no embeddable option at all (MySQL/SQL Server real engines) are exactly
  why they're deferred rather than committed — see "Real-execution:
  committed scope" below.

### Real-execution: committed scope (proposed)

Reduced from the original three-tier plan after weighing it against what
LocalStack — with a funded team and a decade of work — actually chose to
build real engines for, versus what it deliberately left as shape-only
because dedicated tools already cover it better. Two items clear that
bar: each is the literal use case of "run what the user's resource
already points to," not a reimplementation of something better served by
an existing standalone tool.

- **Cloud Run / Cloud Functions:** real `docker run` execution of the
  user-supplied image, fronted by a reverse proxy. This is the #1 local
  IaC pain point (does my image actually start and respond?) and no
  standalone tool solves it in the context of a Terraform-provisioned
  resource the way the emulator can.
- **Cloud SQL (Postgres only):** a real embedded Postgres binary, no
  container. Cheap, no Docker required, and lets queries against a
  Terraform-provisioned instance actually return real results.
- **Monitoring / Logging real metrics:** fed from whatever the two items
  above are actually doing (Docker Stats API, Postgres query stats),
  replacing today's empty `timeSeries` stub.

MySQL/SQL Server real engines are intentionally *not* committed here:
when a developer needs a real MySQL or SQL Server to test against, the
better tool is testcontainers (or `docker run` directly) — bolting that
inside the emulator adds a maintenance-heavy wrapper around something
that already works well standalone, for a use case where realism is
about the engine itself, not about "what the Terraform resource points
to." If demand shows up for it, it slots into Phase 12's `Backend`
interface the same way Postgres did — it's deferred, not designed-out.

### Phase 13 — Cloud SQL: real embedded Postgres

The first slice of the committed real-execution scope above, and this
project's first actual new Go module dependency (`go.mod` previously had
none beyond `go.etcd.io/bbolt`).

- **`internal/realbackend/postgres`**: a `realbackend.Backend`
  implementation wrapping a real PostgreSQL server via
  `github.com/fergusstrange/embedded-postgres` — a real downloaded
  binary, not a reimplementation and not a Docker container (the first
  run on a machine needs network access to fetch it; cached after that).
  `Exec` runs DDL against it via `database/sql` + `github.com/lib/pq`.
  `FootprintMB` is a fixed, conservative 200MB estimate.
- **Wired into `internal/services/cloudsql`** behind the existing
  Phase 12 opt-in (`?backend=real` or
  `settings.userLabels["emulator.dev/backend"] == "real"`, checked via
  `realbackend.WantsReal`) and only for a Postgres `databaseVersion` — the
  only engine this emulator can run without Docker. `createInstance`
  calls `tryStartReal`, which starts the engine, admits it into the
  shared `realbackend.Governor`, and on success populates a new
  `realConnection` field on `DatabaseInstance` — an **emulator-only
  extension** (not part of the real sqladmin API, omitted from JSON for
  every shape-only instance) exposing the host/port/user/password a real
  Postgres client can connect with. `createDatabase`/`createUser` (and
  their delete counterparts) run real `CREATE`/`DROP DATABASE`/`ROLE`
  statements against the live engine when the owning instance has one.
- **Fallback boundary, documented and tested**: if the Governor isn't
  wired (`nil`), the opt-in isn't requested, the `databaseVersion` isn't
  Postgres, the engine fails to start (e.g. no network on first run), or
  the Governor's budget refuses admission, the instance silently stays
  exactly as shape-only as it was before this phase — the same pattern as
  Phase 11's Workflows YAML fallback and Cloud Armor's CEL boundary. Zero
  behavior change for every existing caller that never opts in.
- **`realbackend.Governor` gained `SetOnEvict`** (additive, doesn't change
  any existing method's signature) so `cloudsql` can keep its own
  governor-ID → live-engine map in sync when the Governor evicts or
  releases a backend — without it, a stale map entry could route a
  `CREATE DATABASE` to an engine the Governor already stopped. Documented
  limitation: only one callback is supported process-wide; revisit when a
  second real-backend consumer (Cloud Run/Functions via Docker) needs its
  own.
- **Test coverage split deliberately by network dependency**: the opt-in
  *decision* logic (defaults to shape-only without opt-in; non-Postgres
  opt-in stays shape-only; a `nil` Governor never attempts a real engine)
  is covered by ordinary tests that run in every `go test ./...`/CI pass,
  no network or real Postgres involved. The actual end-to-end real-engine
  test (`TestRealPostgresLifecycle`) is gated behind
  `EMULATOR_REAL_PG_TESTS=1` and skipped otherwise, since it needs to
  download a real postgres binary on a machine's first run and takes real
  wall-clock seconds — not something routine CI should pay for by
  default.
- **Not yet done**: Monitoring/Logging metrics fed from real Postgres
  query stats (the third item in the committed scope), and the Cloud
  Run/Functions Docker-execution item — both remain open for a future
  slice.

### Phase 14 — Cloud Run / Cloud Functions: real Docker execution

The second slice of the committed real-execution scope, closing out the
Cloud Run/Functions item left open by Phase 13. No new Go module
dependency — like the rest of this codebase's Docker-detection code, it
shells out to the `docker` CLI via `os/exec` rather than pulling in a
Docker Go SDK.

- **`internal/realbackend/dockerrun`**: a new package providing two
  shapes. `Backend` (implements `realbackend.Backend`) wraps a
  long-running `docker run -d` container, used by Cloud Run Services and
  Cloud Functions — `Start` reserves a free host port, runs the
  container, and polls the port until it's reachable (or a `readyTimeout`
  elapses). A separate, stateless `RunToCompletion` runs a container to
  completion and reports its exit code and captured output, used by Cloud
  Run Jobs' synchronous `:run` action — a Job execution is one-shot, so it
  is never admitted into the Governor the way a Service's container is.
  Both paths bound the underlying `docker run` invocation itself with its
  own timeout, independent of the port-wait, so a nonexistent or
  unpullable image fails in bounded time rather than hanging the
  triggering request.
- **`realbackend.Governor.SetOnEvict` is now additive**: it used to
  overwrite a single callback (Phase 13's documented limitation, now
  resolved); it now appends to a slice, so cloudsql, cloudrun, and
  cloudfunctions can each register their own eviction callback
  independently without clobbering one another.
- **Wired into `internal/services/cloudrun`** (both Services and Jobs)
  and **`internal/services/cloudfunctions`**, behind the same opt-in this
  project has used since Phase 12 (`?backend=real` or a
  `emulator.dev/backend: real` label, checked via
  `realbackend.WantsReal`):
  - **Cloud Run Services**: `createService`/`updateService` call
    `tryStartReal`, which starts `template.containers[0].image` (defaults
    to port 8080 if `containerPort` isn't set, matching Cloud Run's own
    `$PORT` convention), admits it into the shared `realbackend.Governor`,
    and on success populates a new `realEndpoint` field — an
    **emulator-only extension** (not part of the real Cloud Run API,
    omitted from JSON for every shape-only service) pointing at the real,
    locally reachable URL fronting the container. The pre-existing
    cosmetic `uri`/`fakeURI` fields are untouched. An update that changes
    the container template always stops any existing real container
    first, so an image change never leaves a stale container running.
  - **Cloud Run Jobs**: the `:run` action's opt-in is structurally
    different, since a Job execution isn't a long-running resource — it
    runs `RunToCompletion` synchronously and returns the result directly
    in the `:run` operation's response under a new `realRun` key
    (`{exitCode, output}`), never touching the Governor at all. The
    job's `timeout` field (e.g. `"600s"`) is parsed for the run's bound,
    falling back to a 300s default.
  - **Cloud Functions**: Gen2's real API has no field anywhere for
    specifying a container image (a real Gen2 deploy builds one from
    source via Cloud Build, which this emulator doesn't implement), so
    real execution here requires a second, explicit opt-in beyond the
    label check — a new emulator-only `realExecution.image` field naming
    a pre-built image to run directly. Labeling alone, without
    `realExecution.image`, logs why and stays shape-only.
- **Fallback boundary, same shape as Phase 13**: no Governor wired,
  opt-in not requested, the precondition specific to each resource
  (Services/Jobs need `containers[0].image`; Functions need
  `realExecution.image`), Docker unavailable, or the Governor's budget
  refusing admission — every one of these leaves the resource exactly as
  shape-only as it was before this phase, logged but never failing the
  request.
- **Test coverage split the same way as Phase 13**: opt-in decision logic
  (defaults to shape-only without opt-in; a `nil` Governor never attempts
  real execution; a missing per-resource precondition stays shape-only)
  is covered by ordinary tests with no Docker dependency, run in every
  `go test ./...`/CI pass. The actual end-to-end real-container tests are
  gated behind `EMULATOR_REAL_DOCKER_TESTS=1` (and additionally skip
  cleanly if Docker isn't actually installed on the machine, via the same
  `realbackend.DetectDocker` check used in production code) — not
  something routine CI should pay for by default.
- **Not yet done**: Monitoring/Logging metrics fed from Docker's own
  stats API for these real containers (the remaining item in the
  committed scope).

### Decoupled add-on: network fabric, mini-routers, and GKE/k3d (proposed)

These are the most expensive, most fragile-across-environments items
from the original plan (Docker networks, generated iptables/nftables
rules, "mini-router" containers, a k3d/kind-backed GKE). They're
genuinely interesting, but committing to them now risks the classic
"half-maintained reimplementation of an existing tool" outcome. So they
move out of the committed roadmap and into an explicit **optional
add-on**, with a hard design constraint:

- **The core emulator must never import this code.** It lives in its own
  Go module/package tree (e.g. `addons/realnet/`, `addons/realgke/`),
  built only behind its own build tag or as a fully separate companion
  binary — not linked into `cmd/server`.
- **Discovery, not dependency.** The core registers an extension point
  (a small local HTTP/gRPC hook a companion process can attach to,
  similar to how `kubectl` or `docker-compose` plugins attach) rather
  than calling into add-on code directly. If the add-on binary isn't
  present or isn't running, the core behaves exactly as it does today —
  no missing-symbol errors, no degraded core behavior, no required
  changes to existing services.
- **Independent versioning and lifecycle.** The add-on can be built,
  shipped, and iterated on its own schedule without ever requiring a
  release of the core emulator, and vice versa.
- **Promotion path, not a promise.** If/when there's concrete demand and
  the add-on proves stable, *then* it's a candidate for folding into the
  core's committed scope — but it starts and stays a puzzle piece that
  can be missing without anything breaking.

### Recommendation

Phase 9, Phase 10, and Phase 11 (behavioral logic) are all done. Phase 12
(foundation) is also done — `internal/realbackend` ships the `Backend`
interface, per-resource opt-in, Docker detection, and the budget-aware
governor, all with zero new Go module dependencies. Phase 13 (Cloud SQL
real embedded Postgres) and Phase 14 (Cloud Run/Functions real Docker
execution) are both implemented on top of that foundation. What's left
of the committed scope is Monitoring/Logging metrics fed from all three
real backends (embedded Postgres, and Docker containers for Cloud
Run/Functions). The network/mini-router and GKE/k3d add-on is explicitly
decoupled — build it opportunistically as a separate plug-in piece, never
as a dependency the core relies on. MySQL and SQL Server real engines
stay deferred to "if there's concrete demand," using
testcontainers/`docker run` directly as the recommended interim answer.
The pre-existing "larger/niche surfaces" backlog below is unrelated to
this real-execution work and remains separately prioritized.

### Backlog (proposed, lower priority) — Larger/niche surfaces

Each of these has a materially larger API surface and/or a narrower
audience for a *local* emulator (these services' value is usually tied to
real managed infrastructure — actual GPUs, actual Spark clusters, actual
App Engine sandboxing — that a shape-only emulator can't meaningfully
stand in for beyond satisfying `terraform apply`).

| Service | Minimum resources | Why | Effort |
|---|---|---|---|
| Vertex AI | `models`, `endpoints` (CRUD, no real inference) | Growing Terraform adoption, but a sprawling API (training, pipelines, feature store) — scoping to just models/endpoints keeps this tractable. | L |
| App Engine | `applications`, `services`, `versions` (CRUD, no real deploy) | Long-tail demand; declining relative to Cloud Run, but `google_app_engine_application` still appears in legacy IaC. | L |
| Dataproc | `clusters` (CRUD, shape-only — no real Spark/Hadoop) | Common in data-pipeline Terraform, but a cluster resource alone is a reasonable, bounded slice (vs. modeling jobs/workflows too). | L |
| Dataflow | `jobs` (create/get/list, status always a fixed terminal state) | Pairs with Dataproc/BigQuery in data pipelines; simpler surface than Dataproc since jobs are mostly fire-and-forget from the API's perspective. | M |
| Cloud Composer | `environments` (CRUD, shape-only — no real Airflow) | Highest effort-to-value ratio of this batch (environment creation alone is a large, slow-resolving real API); revisit only if there's specific demand. | L |
