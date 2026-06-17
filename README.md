# GCP Emulator

Emulador local de Google Cloud Platform escrito en Go. Expone APIs REST
compatibles con `storage.googleapis.com`, `compute.googleapis.com` e
`iam.googleapis.com`, persiste todo en un único archivo embebido (BoltDB)
y trae una consola web simple (clon liviano de Google Cloud Console) para
inspeccionar los recursos.

Objetivo: un binario portable (sin Docker, sin Postgres, sin nada externo)
que corra igual en Windows, Linux o macOS, y contra el cual se pueda usar
`gcloud` CLI y los SDKs oficiales de Google apuntando a `localhost`.

## Estado actual

Implementado (subset funcional, no exhaustivo):

- **IAM**: crear/listar/obtener/borrar service accounts, get/set de IAM
  policy a nivel de proyecto, lista de roles predefinidos básicos.
- **Cloud Storage (GCS)**: crear/listar/obtener/borrar buckets; subir
  (`uploadType=media`), listar, descargar (`alt=media`) y borrar objetos.
- **Compute Engine**: listar zonas y machine types (estáticos), crear/
  listar/obtener/borrar instancias, start/stop, y recurso `Operation`
  (síncrono) para compatibilidad con el flujo real de gcloud.
- **Consola web** (`web/console`): UI mínima para ver y administrar
  buckets, instancias y service accounts.

Pendiente / roadmap natural: Pub/Sub, Cloud Functions, Cloud Run,
Firestore, BigQuery, autenticación OAuth simulada más completa,
SQL emulado, etc. La arquitectura (`internal/services/<servicio>`) está
pensada para agregar servicios nuevos sin tocar los existentes.

## Estructura

```
cmd/server/main.go          punto de entrada, arma el server HTTP
internal/storage/           persistencia embebida (BoltDB)
internal/server/            router, middlewares, helpers JSON/error, Operations
internal/services/iam/      emulación de IAM
internal/services/gcs/      emulación de Cloud Storage
internal/services/compute/  emulación de Compute Engine
web/console/                frontend estático (HTML/CSS/JS sin build step)
scripts/                    scripts para apuntar gcloud CLI al emulador
data/                       archivo de datos embebido en runtime (gitignored)
```

## Requisitos

- Go 1.22+ (usa `net/http` con routing por método/patrón, sin frameworks).
- gcloud CLI (opcional, para probar comandos reales contra el emulador).

> Nota: este repo no embebe el toolchain de Go. Si no lo tenés instalado,
> bajalo de https://go.dev/dl/ (o `winget install GoLang.Go` en Windows,
> `brew install go` en macOS, `apt install golang-go` en Linux).

## Levantar el emulador

```bash
cd gcp-emulator
go mod tidy        # descarga la única dependencia externa: go.etcd.io/bbolt
go run ./cmd/server
```

Por defecto escucha en `:8443`, persiste en `data/emulator.db` y sirve la
consola web en `/`. Se puede configurar con flags o variables de entorno:

```bash
go run ./cmd/server -addr :9000 -db data/otro.db -web web/console
# o
EMULATOR_ADDR=:9000 EMULATOR_DB=data/otro.db go run ./cmd/server
```

Para producir un binario portable:

```bash
go build -o bin/gcp-emulator ./cmd/server
./bin/gcp-emulator
```

Abrí `http://localhost:8443` para la consola web.

## Levantar con Docker (recomendado: portable y sin instalar Go)

```bash
docker compose up --build -d
# o, sin compose:
docker build -t gcp-emulator .
docker run --rm -p 8443:8443 -v emulator-data:/data gcp-emulator
```

La imagen es multi-stage (build con `golang:1.22-alpine`, runtime en
`alpine` sin toolchain) y corre como usuario no-root. Los datos persisten
en el volumen `emulator-data` (`/data/emulator.db` dentro del contenedor),
así que sobreviven a `docker compose down` / recreaciones del contenedor
(no a `docker compose down -v`, que borra también el volumen).

## Usar gcloud CLI contra el emulador

```bash
# Linux/macOS
./scripts/configure-gcloud.sh http://localhost:8443

# Windows (PowerShell)
.\scripts\configure-gcloud.ps1 http://localhost:8443
```

Esto configura `api_endpoint_overrides` para storage/compute/iam apuntando
al emulador (storage e iam usan distinta forma de URL porque sus clientes
arman el path de manera distinta: storage y compute necesitan el `v1/` en
el override, iam lo agrega solo). gcloud además exige una cuenta "activa";
si ya tenés una sesión logueada en tu configuración `default`, alcanza con
reusarla (el script lo hace automáticamente) — el emulador no valida el
token. Luego, por ejemplo:

```bash
gcloud storage buckets create gs://mi-bucket --project=demo-project
gcloud storage buckets list
gcloud compute instances create mi-vm --zone=us-central1-a --project=demo-project
gcloud compute instances list --zones=us-central1-a --project=demo-project
gcloud iam service-accounts create demo-sa --display-name="Demo SA" --project=demo-project
gcloud iam service-accounts list --project=demo-project
```

Tip: usá una `gcloud configuration` separada (`gcloud config configurations
create emulator-test`) para no pisar tu configuración real mientras probás.
Para volver a la GCP real, creá/activá otra `gcloud configuration`:

```bash
gcloud config configurations create real-gcp
gcloud config configurations activate real-gcp
```

## Probar sin gcloud (curl)

```bash
curl -X POST localhost:8443/storage/v1/b -d '{"name":"mi-bucket"}'
curl localhost:8443/storage/v1/b

curl -X POST "localhost:8443/upload/storage/v1/b/mi-bucket/o?name=hola.txt" \
  -H "Content-Type: text/plain" --data-binary "hola mundo"
curl "localhost:8443/storage/v1/b/mi-bucket/o/hola.txt?alt=media"
```

## Diseño

- **Portabilidad**: un solo binario Go + un archivo BoltDB. No requiere
  Docker, ni base de datos externa, ni variables de entorno obligatorias.
- **Compatibilidad de API**: las rutas HTTP replican los paths reales de
  las APIs de Google (`/storage/v1/b/...`, `/compute/v1/projects/.../zones/...`)
  para que `api_endpoint_overrides` de gcloud y los SDKs oficiales puedan
  apuntar directo al emulador sin parches.
- **Extensible**: cada servicio vive en `internal/services/<nombre>` con su
  propio `Register(mux)`; agregar un servicio nuevo no toca los existentes.
