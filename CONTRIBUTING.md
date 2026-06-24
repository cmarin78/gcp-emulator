# Contributing

Thanks for considering a contribution to gcp-emulator.

## Project conventions

- Each emulated GCP service lives in its own package under
  `internal/services/<name>`, with its own `Register(mux)` function. Follow
  the existing pattern (e.g. `internal/services/pubsub`, `internal/services/cloudsql`)
  rather than inventing a new structure.
- The emulator is "shape-compatible, not behavior-complete" by default:
  resources are stored and returned with the real API's JSON shape, but
  most services don't actually act on the data. Real-execution backends
  (Phase 12-15: embedded Postgres for Cloud SQL, Docker containers for
  Cloud Run/Functions) are an explicit, opt-in exception — see
  `ROADMAP.md` for the full history and design rationale before adding a
  new one.
- No new Go module dependency without a good reason. Most of this
  project's "real" behavior (Docker detection, host RAM detection) shells
  out to existing CLI tools via `os/exec` instead of adding an SDK.
- Every new service needs test coverage following the existing
  `internal/testutil` harness pattern: an in-memory BoltDB per test, a
  `DoJSON` helper, and at minimum a create/get/list/update/delete smoke
  test plus the duplicate-create-conflict (`409`) check used throughout
  the codebase.

## Before opening a PR

```
go build ./...
go vet ./...
go test ./... -race
```

All three must be clean. If you're adding a new service package, also run
`cmd/server`'s registration test to confirm your new routes don't collide
with an existing one on the shared `http.ServeMux`.

## Reporting bugs / proposing services

Open an issue describing the gap (missing resource, missing field, wrong
status code, etc.) ideally with the real `gcloud`/Terraform command that
exposes it. See `ROADMAP.md` for already-planned/backlog services before
proposing a new one — it may already be scoped.
