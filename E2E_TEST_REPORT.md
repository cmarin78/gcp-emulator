# E2E Test Report: gcloud CLI + Terraform

Date: 2026-06-17
Environment: local emulator (`bin\e2e-test.exe`, ephemeral, port 8999), real gcloud CLI, real Terraform (`hashicorp/google` v7.37.0).

## Summary

Both tests (gcloud CLI and Terraform) finished green. 3 real bugs in the
emulator were found and fixed along the way. No leftovers remain from
testing: the binary, database, gcloud configuration, and temp directory
were all removed; `git status --short` only shows the 3 fixed code files.

## gcloud CLI tests

Isolated configuration (`gcloud config configurations create emulator-e2e-test`)
to avoid touching the user's real configuration.

| Service | Commands tested | Result |
|---|---|---|
| Storage | `buckets create/list`, object upload/download | OK |
| Compute | `instances create/list/delete`, `instances stop/start` | OK (after fix) |
| Compute | `networks create/list/delete` | OK |
| IAM | `service-accounts create/list/delete` | OK |

## Terraform tests

Config in `tmp-e2e/tf/main.tf` (ephemeral), covering the roadmap's 6 phases:
`google_compute_network` + `google_compute_instance`, `google_cloud_run_v2_service`,
`google_bigquery_dataset` + `google_bigquery_table`, `google_kms_key_ring` +
`google_kms_crypto_key`, `google_pubsub_topic` + `google_pubsub_subscription`,
`google_artifact_registry_repository`.

`terraform init` → `apply` → `destroy`: all 10 resources were created and
destroyed correctly (KMS has no delete endpoint, matching the real API).
`terraform apply`/`destroy` ran clean, no provider patches required.

## Bugs found and fixed

### 1. Invalid base64 fingerprint (Compute)
The `labelFingerprint`/`metadataFingerprint`/`tagsFingerprint` fields weren't
generating valid base64, which broke clients that decode them. Fixed with a
consistent `fakeFingerprint(seed)` helper.

### 2. `gcloud compute instances stop/start`: relative selfLink
**Symptom:** `UnknownCollectionException: unknown collection for [...]`.
**Cause:** gcloud's resource parser (`resources.Parse`, without
`collection=`) requires an absolute URL to resolve the returned operation;
the emulator was returning a relative `selfLink`.
**Fix:** new `opsBase(r)` helper in `network.go` that builds the absolute
prefix (`scheme://host/compute/v1`) from the `http.Request` itself, applied
at all 11 places where an `Operation` is built (`compute.go`, `network.go`).

### 3. Missing `operations/{operation}/wait` endpoint (Compute)
**Symptom:** after fixing bug #2, gcloud resolved the absolute URL and
called `POST .../operations/{operation}/wait`, which didn't exist →
`HTTPError 404`.
**Fix:** 3 new routes (`zone`/`region`/`global`) reusing the existing
`getOperation` handler.

Full cycle verified with real gcloud: `create → stop → start → list → delete`.

### 4. Artifact Registry: `repositoryId` didn't accept `repository_id`
**Symptom:** `terraform apply` failed with `repositoryId is required` even
though the parameter was present in the query string.
**Cause:** Terraform's provider sends the query param as `repository_id`
(snake_case); the emulator only accepted `repositoryId` (camelCase). The
real Artifact Registry API accepts both forms.
**Fix:** `createRepository` now falls back to `repository_id` if
`repositoryId` is empty.

## Known limitations (not bugs, documented)

- `gcloud storage cp` with resumable upload is not supported (the emulator
  only implements `uploadType=media`).
- `gcloud storage rm` doesn't work on buckets; use
  `gcloud storage buckets delete` instead.
- `google_cloud_run_v2_service` requires `deletion_protection = false` in
  the `.tf` to allow `terraform destroy` — this is a guard from the
  provider itself, not the emulator (same behavior against real GCP).

## Files modified

- `internal/services/compute/compute.go`
- `internal/services/compute/network.go`
- `internal/services/artifactregistry/artifactregistry.go`

## Cleanup verification

```
git status --short
 M internal/services/artifactregistry/artifactregistry.go
 M internal/services/compute/compute.go
 M internal/services/compute/network.go
```

No new files, no binaries, no databases, no leftover gcloud configurations.

## Annex: Phase 7 (Resource Manager, Scheduler, Tasks, DNS, Load Balancing)

Date: 2026-06-18
Environment: same ephemeral binary (`bin\e2e-test.exe`, port 8999), tested via
PowerShell `Invoke-RestMethod` directly on the user's machine (this
session's sandbox has no usable Go toolchain or outbound network).

### Summary

All 5 phases of the new roadmap were tested end to end with a clean first
pass, no bugs. Unlike the phase 1-6 tests (3 bugs found), no defects were
found this time.

| Service | Flow tested | Result |
|---|---|---|
| Resource Manager | create + get project | OK |
| Cloud Scheduler | create job, `:run`, get (updated timestamps) | OK |
| Cloud Tasks | create queue, create task (auto-generated `task-1`), list tasks, `:pause` | OK |
| Cloud DNS | create zone (synthesized nameservers), create change (additions), list rrsets | OK |
| Load Balancing | healthCheck → backendService → urlMap → targetHttpProxy → forwardingRule (each insert returns a Compute-style `Operation` with `selfLink`), verification GETs | OK |

Load Balancing is the most relevant case: it reuses Compute's own
`Operation` (not the simple `google.longrunning.Operation` used by the
other four services), and the build + tests confirmed the 24 registered
routes don't collide with each other on the real `http.ServeMux` (something
that had previously only been checked manually).

### Bugs found

None.

### Cleanup verification

```
git status --short
 M README.md
 M ROADMAP.md
 M cmd/server/main.go
 M internal/services/artifactregistry/artifactregistry.go
 M internal/services/compute/compute.go
 M internal/services/compute/network.go
?? E2E_TEST_REPORT.md
?? internal/services/clouddns/
?? internal/services/cloudscheduler/
?? internal/services/cloudtasks/
?? internal/services/loadbalancing/
?? internal/services/resourcemanager/
```

No leftover binary or database (`bin\e2e-test.exe` and
`data\e2e-test.db` removed after testing).

## Annex: Phase 8 (Cloud Build, networking extensions, Cloud Armor, Memorystore, Cloud Spanner, GKE)

Date: 2026-06-18
Environment: ephemeral binary (`bin\emulator.exe`, port 8444, isolated test
DB `data\phase8test.db`), tested live via PowerShell `Invoke-RestMethod`
directly on the user's machine.

### Summary

All 6 new services were smoke-tested end to end with direct HTTP calls
against the running emulator, covering every service registered together
on the same mux. One real bug was found and fixed before testing could
even start; once fixed, all 6 services worked correctly on the first
re-attempt.

| Service | Flow tested | Result |
|---|---|---|
| Cloud Build | create build, get | OK |
| Compute networking extensions | create router (with NAT config), create route, list/get | OK |
| Cloud Armor | create securityPolicy, get | OK |
| Memorystore | create instance (`redis1`), get (state READY, host/port synthesized) | OK |
| Cloud Spanner | create instance (`spanner1`), create database via `CREATE DATABASE mydb` DDL parsing, get database | OK |
| GKE | create cluster (`cluster1`), create nodePool (`pool1`), get cluster (status RUNNING, endpoint synthesized) | OK |

### Bug found and fixed

**`http.ServeMux` route-pattern collision on startup.** Both `memorystore.go`
and `gke.go` initially registered their own generic
`GET /v1/projects/{project}/locations/{location}/operations/{operation}`
polling endpoint — the exact same path pattern already owned by
`artifactregistry.go` on the shared `/v1/*` mux. Go's `http.ServeMux`
panics at registration time on duplicate patterns, so the emulator
crashed immediately on startup with:

```
panic: pattern "GET /v1/projects/{project}/locations/{location}/operations/{operation}"
(registered at memorystore.go:65) conflicts with pattern
(registered at artifactregistry.go:56)
```

This was **not** caught by `go build ./...` or `go vet ./...` — it only
surfaced when the binary was actually run and `Register()` was called for
every service in sequence. Fixed by removing the duplicate route
registration and the now-dead `getOperation` handler from both
`memorystore.go` and `gke.go` (each annotated with a comment explaining
the omission): every mutation in both services already resolves
synchronously and returns `done: true` / `status: DONE` in its own
response, so no client has a real reason to poll. `spanner.go` was
unaffected since its own operations path is scoped under `/instances/`
rather than `/locations/`, so it didn't collide.

This validates that the "build and run the actual binary" step of this
phase's testing was more than a formality — static analysis alone would
have shipped a crashing binary.

### Verification of cleanup

Stopped the test emulator process and removed all ephemeral artifacts:
`bin\emulator.exe`, `data\phase8test.db`, `phase8_out.log`,
`phase8_err.log`, `phase8_pid.txt`. No leftover test artifacts remain.

## Annex: Phase 8 round 2 — real gcloud/Terraform clients

Date: 2026-06-18
Environment: ephemeral binary (`bin\emulator.exe`, port 8444, isolated test
DB `data\phase8tf.db`), exercised with the **real** `hashicorp/google` v7.37.0
Terraform provider (not direct HTTP calls like the round-1 smoke test) via a
`tmp-e2e/tf/main.tf` config covering 9 resources: network, router, NAT,
route, security policy, Memorystore (Redis) instance, Spanner instance,
Spanner database, and a GKE cluster.

### Summary

This round used a real client (Terraform's provider binary, which itself
wraps the real `google-api-go-client`/gRPC-style request shapes) instead of
hand-built HTTP calls, and it surfaced 4 real bugs that the round-1
direct-HTTP smoke test could not have caught — three were in code paths that
only a real client triggers automatically (provider-side label
reconciliation, post-create consistency reads, and follow-up DDL calls).
All 4 were found and fixed; the final `terraform apply` / `terraform
destroy` cycle completed cleanly with 9/9 resources created and 9/9
destroyed, zero errors.

| Service | Resource | Result |
|---|---|---|
| Compute networking | network, router, NAT, route | OK |
| Cloud Armor | security policy | OK (after fix #1) |
| Memorystore | Redis instance | OK |
| Cloud Spanner | instance, database (DDL) | OK (after fix #4) |
| GKE | cluster | OK (after fixes #2, #3) |

### Bugs found and fixed

#### 1. Cloud Armor: missing `setLabels` endpoint (404)
**Symptom:** `terraform apply` failed creating `google_compute_security_policy`
with a 404. **Cause:** Terraform's provider always issues a follow-up
`POST .../securityPolicies/{name}/setLabels` after create/update to apply
`effective_labels`/`terraform_labels` — a call a hand-written HTTP smoke test
would never think to make. This route didn't exist in
`loadbalancing.go`. **Fix:** added the route plus a no-op
`setSecurityPolicyLabels` handler in `securitypolicy.go` that refreshes the
fingerprint and returns a `DONE` operation.

#### 2. GKE: provider plugin panic on cluster create/read
**Symptom:** the Terraform provider process crashed with
`panic: runtime error: invalid memory address or nil pointer dereference`
inside `resourceContainerClusterRead`/`Create`. Because all Terraform
resources share one provider plugin process, this single panic also
corrupted concurrently in-flight operations for other resources in the same
apply (initially misread as a Spanner bug — see bug #4 below for why that
hypothesis was wrong). **Cause:** the emulator's `Cluster`/`NodeConfig`
JSON shape was missing many substructures the real GKE API always
populates (`nodePools` with an auto-created default pool, `masterAuth`,
`addonsConfig`, `ipAllocationPolicy`, `legacyAbac`, `releaseChannel`,
`shieldedNodes`, `workloadIdentityConfig`, `networkConfig`, and per-node
`workloadMetadataConfig`/`shieldedInstanceConfig`), and the provider's Go
struct deref code assumes they're present. The exact single field
responsible for the panic could not be pinpointed from source (GitHub
fetches of the ~2,300-line provider source file were silently truncated by
the fetch tool's size cap before reaching the relevant function bodies, and
web search turned up no matching issue reports), so the fix was made
empirically: populate every one of these substructures with sensible
defaults in `createCluster`. **Fix verified:** rebuilt and re-tested; the
panic is gone.

#### 3. GKE: missing `instanceGroupManagers` endpoint (404) breaking apply consistency
**Symptom:** once bug #2 was fixed, a new error appeared:
`Error: Provider produced inconsistent result after apply ... Root object
was present, but now absent`. **Cause:** the default-pool NodePool added
while fixing bug #2 included an `instanceGroupUrls` field, which caused the
provider to make a legitimate follow-up Compute Engine call,
`GET /compute/v1/projects/{project}/zones/{zone}/instanceGroupManagers`,
to resolve node-pool instance group state. This endpoint didn't exist
anywhere in `compute.go`, so the shared mux's catch-all handler returned a
plain-text 404 instead of valid JSON, which broke the provider's
post-apply consistency check. **Fix:** added
`GET /compute/v1/projects/{project}/zones/{zone}/instanceGroupManagers`
(and the `aggregated` variant) returning an empty
`compute#instanceGroupManagerList` — a real, valid "zero matches" response,
since this emulator doesn't model managed instance groups.

#### 4. Cloud Spanner: missing database DDL endpoint (404)
**Symptom:** `terraform apply` failed creating `google_spanner_database`
with `googleapi: got HTTP response code 404 with body: 404 page not found`
on the DDL step. **Cause:** real Spanner only accepts a database's initial
schema through a separate `PATCH .../databases/{database}/ddl` call after
`CreateDatabase` — Terraform's provider always makes this follow-up call,
but `spanner.go` never registered the route. This was originally
misdiagnosed (in an earlier re-test) as collateral damage from the GKE
panic (bug #2), since both failures appeared in the same apply run; a
clean re-test after fixing bug #2 proved Spanner had its own, fully
independent bug. **Fix:** added the route and an `updateDatabaseDdl`
handler that appends the incoming statements to the database's
`ExtraStatements` and returns a `DONE` operation — no real DDL parsing or
execution, consistent with this package's existing "shape-compatible, not
behavior-complete" approach.

### Test harness note (not an emulator bug)

The first `terraform destroy` attempt failed with
`Error: Cannot destroy cluster because deletion_protection is set to true`
(and the equivalent for the Spanner database). This is the Terraform
google provider's own client-side safety guard — it defaults
`deletion_protection`/`deletion_policy` to a protective value for
`google_container_cluster` and `google_spanner_database`, requiring the
`.tf` config to explicitly opt out. Same behavior occurs against real GCP.
Added `deletion_protection = false` and `deletion_policy = "ABANDON"` to
the test config's GKE/Spanner resources, then re-ran a clean
`apply` → `destroy` cycle from scratch (fresh DB, fresh Terraform state)
to confirm both completed with zero errors.

### Files modified this round

- `internal/services/loadbalancing/loadbalancing.go` (setLabels route)
- `internal/services/loadbalancing/securitypolicy.go` (setLabels handler)
- `internal/services/gke/gke.go` (expanded Cluster/NodeConfig/NodePool shape)
- `internal/services/compute/compute.go` (instanceGroupManagers endpoint)
- `internal/services/spanner/spanner.go` (database DDL endpoint)

### Verification of cleanup

Stopped the test emulator process and removed all ephemeral artifacts:
`bin\emulator.exe`, `data\phase8tf.db`, `phase8tf_out.log`,
`phase8tf_err.log`, `phase8tf_pid.txt`, and the entire `tmp-e2e\tf\`
directory (including `.terraform/`, `.terraform.lock.hcl`, Terraform state
files, and apply/destroy logs). No leftover test artifacts remain.
