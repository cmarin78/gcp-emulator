# Roadmap del emulador

Lista de servicios/recursos a agregar, ordenados por dependencias y valor
para flujos reales de `gcloud` / Terraform / SDKs. Cada fase es
autocontenida (se puede mergear y usar sin esperar a la siguiente).

Convención: cada servicio nuevo vive en `internal/services/<nombre>` con
su propio `Register(mux)`, siguiendo el patrón ya usado por IAM/GCS/Compute.

## Estado actual

- IAM: service accounts, IAM policy a nivel de proyecto, roles predefinidos (lista estática).
- Storage: buckets, objetos (upload simple, descarga, listado, borrado).
- Compute: zonas/machine types estáticos, instancias (CRUD básico, start/stop), operations.

## Fase 1 — Completar Compute para IaC real

Hoy `google_compute_instance` de Terraform falla porque la API real
exige `boot_disk` (referencia a un disco/imagen) y `network_interface`
(referencia a una red), y el emulador no modela esos recursos. Esta fase
cierra esa brecha.

| Recurso | Depende de | Por qué | Esfuerzo |
|---|---|---|---|
| `compute.networks` (VPC) | — | Requisito de `network_interface` | S |
| `compute.subnetworks` | networks | Requisito si se usa red custom-mode | S |
| `compute.firewalls` | networks | Común en cualquier módulo de red | S |
| `compute.images` (catálogo estático, ej. debian-12) | — | Requisito de `boot_disk.initialize_params.image` | S |
| `compute.disks` | images | Requisito de `boot_disk` explícito | M |
| `instances` (enriquecer) | networks, disks, images | Aceptar/devolver `disks[]` y `networkInterfaces[]` reales | M |

Al cerrar esta fase, `terraform apply` con `google_compute_instance` +
`google_compute_network` debería funcionar sin parches (igual que ya
funciona `google_storage_bucket` y `google_service_account`).

## Fase 2 — IAM avanzado

| Recurso | Depende de | Por qué | Esfuerzo |
|---|---|---|---|
| `iam.roles` (custom roles) | — | `google_project_iam_custom_role` | S |
| `iam.serviceAccountKeys` | service accounts (ya existe) | `google_service_account_key` | S |
| IAM bindings a nivel de recurso (bucket, service account) | storage/iam (ya existen) | `google_storage_bucket_iam_*`, `google_service_account_iam_*` | M |
| `resourcemanager.projects` (create/get) | — | Opcional: hoy "project" es un string opaco y ya funciona así; esto es solo para mayor realismo | S (baja prioridad) |

## Fase 3 — Servicios independientes de alto valor ✅ completada

Sin dependencias entre sí ni con fases anteriores; se pueden hacer en
cualquier orden o en paralelo.

| Servicio | Recursos mínimos | Esfuerzo | Estado |
|---|---|---|---|
| Pub/Sub | topics, subscriptions (subscriptions depende de topics) | M | ✅ |
| Secret Manager | secrets, versions | S | ✅ |
| Artifact Registry | repositories | S | ✅ |

## Fase 4 — Cómputo serverless

| Servicio | Depende de | Nota | Esfuerzo |
|---|---|---|---|
| Cloud Run | — (acepta referencias a imágenes sin validarlas contra Artifact Registry) | servicios + revisiones | M |
| Cloud Functions | — (acepta metadata de fuente sin validar contra Storage) | Gen2 está implementado sobre Cloud Run en GCP real; conviene hacerlo después de Cloud Run | M |

## Fase 5 — Datos

Independientes entre sí, cada uno es un paquete nuevo de tamaño similar
a Compute/Storage actuales.

| Servicio | Recursos mínimos | Esfuerzo |
|---|---|---|
| Cloud SQL | instances, databases, users | L |
| Firestore | databases, documents (CRUD simple) | L |
| BigQuery | datasets, tables | M |

## Fase 6 — Observabilidad y gobierno (baja prioridad)

| Servicio | Nota | Esfuerzo |
|---|---|---|
| Cloud KMS | keyrings, cryptokeys — útil si más adelante se emula cifrado real de Secret Manager | S |
| Cloud Logging | stub de sinks, sin pipeline real de logs | S |
| Cloud Monitoring | stub de métricas | S |

## Orden recomendado

1. Fase 1 (Compute completo) — es la brecha más visible ahora mismo (lo detectamos al probar Terraform).
2. Fase 3 (Pub/Sub, Secret Manager, Artifact Registry) — alto valor, cero dependencias, esfuerzo bajo/medio.
3. Fase 2 (IAM avanzado) — refuerza lo que ya existe.
4. Fase 4 (Cloud Run / Functions) — más esfuerzo, mayor superficie de API.
5. Fase 5 (datos) — el más caro de implementar, conviene dejarlo para cuando el patrón de servicio esté muy pulido.
6. Fase 6 — cuando haga falta para un caso de uso concreto.
