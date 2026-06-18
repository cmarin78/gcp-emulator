# Reporte de pruebas E2E: gcloud CLI + Terraform

Fecha: 2026-06-17
Entorno: emulador local (`bin\e2e-test.exe`, efímero, puerto 8999), gcloud CLI real, Terraform real (`hashicorp/google` v7.37.0).

## Resumen

Ambas pruebas (gcloud CLI y Terraform) terminaron en verde. Se encontraron y
corrigieron 3 bugs reales en el emulador durante el proceso. No queda ningún
residuo de las pruebas: binario, base de datos, configuración de gcloud y
directorio temporal fueron eliminados; `git status --short` solo muestra los
3 archivos de código corregidos.

## Pruebas con gcloud CLI

Configuración aislada (`gcloud config configurations create emulator-e2e-test`)
para no tocar la configuración real del usuario.

| Servicio | Comandos probados | Resultado |
|---|---|---|
| Storage | `buckets create/list`, upload/download de objeto | OK |
| Compute | `instances create/list/delete`, `instances stop/start` | OK (tras fix) |
| Compute | `networks create/list/delete` | OK |
| IAM | `service-accounts create/list/delete` | OK |

## Pruebas con Terraform

Config en `tmp-e2e/tf/main.tf` (efímero), cubriendo las 6 fases del roadmap:
`google_compute_network` + `google_compute_instance`, `google_cloud_run_v2_service`,
`google_bigquery_dataset` + `google_bigquery_table`, `google_kms_key_ring` +
`google_kms_crypto_key`, `google_pubsub_topic` + `google_pubsub_subscription`,
`google_artifact_registry_repository`.

`terraform init` → `apply` → `destroy`: los 10 recursos se crearon y
destruyeron correctamente (KMS sin endpoint de delete, igual que en la API
real). `terraform apply`/`destroy` limpios, sin parches al provider.

## Bugs encontrados y corregidos

### 1. Fingerprint inválido en base64 (Compute)
Los campos `labelFingerprint`/`metadataFingerprint`/`tagsFingerprint` no
generaban base64 válido, lo cual rompía algunos clientes que los decodifican.
Corregido con un helper `fakeFingerprint(seed)` consistente.

### 2. `gcloud compute instances stop/start`: selfLink relativo
**Síntoma:** `UnknownCollectionException: unknown collection for [...]`.
**Causa:** el parser de recursos de gcloud (`resources.Parse`, sin
`collection=`) exige una URL absoluta para resolver la operación devuelta;
el emulador devolvía un `selfLink` relativo.
**Fix:** nuevo helper `opsBase(r)` en `network.go` que construye el prefijo
absoluto (`scheme://host/compute/v1`) a partir del propio `http.Request`,
aplicado en los 11 puntos donde se construye un `Operation`
(`compute.go`, `network.go`).

### 3. Falta el endpoint `operations/{operation}/wait` (Compute)
**Síntoma:** tras corregir el bug #2, gcloud resolvía la URL absoluta y
llamaba a `POST .../operations/{operation}/wait`, que no existía →
`HTTPError 404`.
**Fix:** 3 nuevas rutas (`zone`/`region`/`global`) reutilizando el handler
existente `getOperation`.

Ciclo completo verificado con gcloud real: `create → stop → start → list → delete`.

### 4. Artifact Registry: `repositoryId` no aceptaba `repository_id`
**Síntoma:** `terraform apply` fallaba con `repositoryId es requerido` aunque
el parámetro venía en la query string.
**Causa:** el provider de Terraform envía el query param como
`repository_id` (snake_case); el emulador solo aceptaba `repositoryId`
(camelCase). La API real de Artifact Registry acepta ambas formas.
**Fix:** `createRepository` ahora hace fallback a `repository_id` si
`repositoryId` viene vacío.

## Limitaciones conocidas (no son bugs, documentadas)

- `gcloud storage cp` con upload resumable no está soportado (el emulador
  solo implementa `uploadType=media`).
- `gcloud storage rm` no funciona sobre buckets; hay que usar
  `gcloud storage buckets delete`.
- `google_cloud_run_v2_service` requiere `deletion_protection = false` en
  el `.tf` para permitir `terraform destroy` — es un guard del propio
  provider, no del emulador (mismo comportamiento contra GCP real).

## Archivos modificados

- `internal/services/compute/compute.go`
- `internal/services/compute/network.go`
- `internal/services/artifactregistry/artifactregistry.go`

## Verificación de limpieza

```
git status --short
 M internal/services/artifactregistry/artifactregistry.go
 M internal/services/compute/compute.go
 M internal/services/compute/network.go
```

Sin archivos nuevos, sin binarios, sin bases de datos, sin configuraciones de
gcloud residuales.

## Anexo: Fase 7 (Resource Manager, Scheduler, Tasks, DNS, Load Balancing)

Fecha: 2026-06-18
Entorno: mismo binario efímero (`bin\e2e-test.exe`, puerto 8999), probado vía
PowerShell `Invoke-RestMethod` directamente en la máquina del usuario (el
sandbox de esta sesión no tiene toolchain de Go ni red de salida utilizable).

### Resumen

Las 5 fases del roadmap nuevo se probaron de punta a punta con resultado
verde a la primera, sin bugs. A diferencia de las pruebas de fases 1-6 (3
bugs encontrados), esta vez no se encontró ningún defecto.

| Servicio | Flujo probado | Resultado |
|---|---|---|
| Resource Manager | create + get project | OK |
| Cloud Scheduler | create job, `:run`, get (timestamps actualizados) | OK |
| Cloud Tasks | create queue, create task (`task-1` autogenerado), list tasks, `:pause` | OK |
| Cloud DNS | create zone (nameservers sintetizados), create change (additions), list rrsets | OK |
| Load Balancing | healthCheck → backendService → urlMap → targetHttpProxy → forwardingRule (cada insert devuelve `Operation` estilo Compute con `selfLink`), GETs de verificación | OK |

Load Balancing es el caso más relevante: reutiliza el `Operation` propio de
Compute (no el `google.longrunning.Operation` simple de los otros cuatro
servicios), y el build + las pruebas confirmaron que las 24 rutas registradas
no chocan entre sí en el `http.ServeMux` real (algo que antes solo se había
revisado manualmente).

### Bugs encontrados

Ninguno.

### Verificación de limpieza

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

Sin binario ni base de datos residual (`bin\e2e-test.exe` y
`data\e2e-test.db` eliminados tras la prueba).

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
