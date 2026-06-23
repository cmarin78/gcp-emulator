# POC: Terraform real (init → plan → apply → destroy) contra real-execution

Fecha: 2026-06-23
Entorno: emulador local (`go run ./cmd/server`, puerto 8443, `docker-available=true`),
Terraform real (`hashicorp/google` v7.37.0), corrido en la máquina del
usuario (PowerShell). Config en
[`examples/terraform-real-poc/`](examples/terraform-real-poc/main.tf).

## Resumen

Ciclo completo `init` → `plan` → `apply` → `destroy` ejecutado contra el
real-execution backend de las Fases 12-15 (no contra el modo "shape-only"
que ya documentaba el README), usando Terraform sin parches al provider.
Se encontró y corrigió 1 problema real durante la primera pasada del
`destroy`; tras el fix, el ciclo completo terminó en verde y sin residuos.

## Qué se probó

A diferencia de los ejemplos ya existentes en el README (Compute, Cloud
Run, BigQuery, KMS), que solo crean la forma del recurso, esta POC opta
explícitamente por el backend real:

| Recurso Terraform | Opt-in usado | Backend real esperado |
|---|---|---|
| `google_sql_database_instance` (+`google_sql_database`, `google_sql_user`) | `settings.user_labels = { "emulator.dev/backend" = "real" }` | Motor Postgres embebido real (Fase 13) |
| `google_cloud_run_v2_service` | `labels = { "emulator.dev/backend" = "real" }` + imagen real (`nginx:alpine`) | Contenedor Docker real (Fase 14) |

Ambos opt-ins usan campos que ya existen nativamente en el schema real del
provider (`settings.user_labels`, `labels`) — no hizo falta ningún parche
ni extensión del provider `google`.

## Resultado: `init` / `plan` / `apply`

```
terraform init    → OK (hashicorp/google v7.37.0 instalado)
terraform plan    → 4 to add, 0 to change, 0 to destroy
terraform apply   → Apply complete! Resources: 4 added, 0 changed, 0 destroyed.

Outputs:
cloud_run_uri               = "https://tf-real-svc-emulator-us-central1.a.run.app"
cloudsql_connection_name    = "tf-real-poc:us-central1:tf-real-pg"
cloudsql_instance_self_link = "projects/tf-real-poc/instances/tf-real-pg"
```

## Verificación de backend real (no solo "shape")

**Cloud SQL** — `GET /sql/v1beta4/.../instances/tf-real-pg` devolvió un
campo `realConnection` con datos de conexión reales al motor embebido:

```json
"realConnection": {
  "backend": "cloudsql-postgres-embedded",
  "host": "127.0.0.1",
  "port": 64447,
  "user": "postgres",
  "password": "..."
}
```

**Cloud Run** — `GET /v2/.../services/tf-real-svc` devolvió un
`realEndpoint` apuntando a un contenedor real, y golpear esa URL
directamente devolvió la página de bienvenida real de nginx:

```json
"realEndpoint": { "backend": "docker-container", "url": "http://127.0.0.1:64448" }
```

```
curl http://127.0.0.1:64448  →  <title>Welcome to nginx!</title>  (HTML real de nginx)
```

**Métricas reales (Fase 15)** — `GET /v3/.../timeSeries` devolvió 3 series
GAUGE con puntos reales, confirmando que el polling de métricas contra los
backends reales también funciona end-to-end:

- `run.googleapis.com/container/cpu/utilizations` (2 puntos, contenedor real)
- `run.googleapis.com/container/memory/utilizations` (2 puntos, ~25 MB reales)
- `cloudsql.googleapis.com/database/postgresql/num_backends` (4 puntos, `pg_stat_activity` real, valor 7)

## Resultado: `destroy`

### Primer intento — falló

```
Error: cannot destroy service without setting deletion_protection=false
and running `terraform apply`
```

**Causa:** `google_cloud_run_v2_service` trae `deletion_protection = true`
por defecto como guard del propio provider (el mismo tipo de guard que el
README ya documenta para `google_bigquery_table`) — no tiene nada que ver
con el emulador, es comportamiento idéntico contra GCP real. El `.tf`
inicial no lo declaraba explícitamente.

**Fix:** se agregó `deletion_protection = false` al recurso
`google_cloud_run_v2_service` en
[`examples/terraform-real-poc/main.tf`](examples/terraform-real-poc/main.tf).

### Segundo intento — OK

```
terraform apply -auto-approve    → Apply complete! Resources: 3 added, 1 changed, 0 destroyed.
terraform destroy -auto-approve  → Destroy complete! Resources: 4 destroyed.
```

(El "3 added" es por el `apply` previo fallido a medias: el destroy había
alcanzado a borrar la instancia de Cloud SQL antes de fallar en el Cloud
Run, así que el segundo `apply` la recreó para poder destruir el ciclo
completo de forma limpia. El comportamiento en sí — Postgres real
creándose y destruyéndose correctamente — ya estaba validado en la primera
pasada.)

## Verificación de limpieza

```
curl .../sql/v1beta4/.../instances/tf-real-pg   → 404 instancia no encontrada
curl .../v2/.../services/tf-real-svc            → 404 servicio no encontrado
docker ps                                        → sin contenedor nginx (solo containers ajenos al test)
```

Sin residuos: el motor Postgres embebido y el contenedor Docker de nginx
quedaron completamente liberados tras el `destroy`.

## Limitación conocida (no es un bug, ya documentada para el caso shape-only)

- `google_cloud_run_v2_service` requiere `deletion_protection = false`
  explícito en el `.tf` para permitir `terraform destroy` — guard del
  provider, no del emulador. Ahora también queda anotado directamente en
  el `.tf` de esta POC para que no se repita.

## Archivos

- [`examples/terraform-real-poc/main.tf`](examples/terraform-real-poc/main.tf) — la configuración Terraform.
- [`examples/terraform-real-poc/README.md`](examples/terraform-real-poc/README.md) — cómo correrla.
