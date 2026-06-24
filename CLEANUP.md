# CLEANUP.md — Tareas de ordenamiento para Claude

Notas de mantenimiento de higiene de repo, generadas tras una auditoría comparativa
con `azure-emulator` y `ministack` (aws-emulator). No son features — son deuda de
housekeeping. Marcar con `[x]` a medida que se completen.

## Hecho en esta sesión

- [x] Eliminados del working dir (ya estaban en `.gitignore`, nunca llegaron a
      `git`): `err.log`, `out.log`, `data/emulator.db`, `data/test-phase3.db`,
      `tmp-e2e/` completo, y los binarios sueltos en `bin/` (`gcp-emulator.exe`,
      `test-phase6.exe~`).

## Pendiente

### Higiene de repo
- [ ] **Falta el archivo `LICENSE`** en la raíz del proyecto — a diferencia de
      `azure-emulator` y `ministack`, que sí lo tienen. Si el repo es público en
      GitHub (`cmarin78/gcp-emulator`), sin licencia explícita los términos de uso
      quedan ambiguos por defecto ("todos los derechos reservados"). Decidir la
      licencia (MIT, igual que los otros dos, parece la opción consistente) y
      añadir el archivo.
- [ ] Revisar `git log -- '**/*.tfstate'` para confirmar que ningún `.tfstate` con
      secretos (la contraseña de la DB demo del PoC, mencionada en el propio
      `.gitignore`) llegó a commitearse antes de que se agregara la regla.
- [ ] Igual que en azure-emulator: agregar un check en CI que falle si se
      intenta commitear un binario o `.db`.

### Documentación
- [ ] `ROADMAP.md` (772 líneas) mezcla fases cerradas (1-15+) con trabajo futuro.
      Separar en `CHANGELOG.md` (fases completadas, estilo Keep a Changelog como
      `ministack`) y dejar `ROADMAP.md` solo con lo no implementado.
- [ ] `E2E_TEST_REPORT.md` y `TERRAFORM_REAL_POC.md` son documentos de resultados
      de una corrida puntual — considerar si deben vivir en `docs/` en vez de la
      raíz, para no competir visualmente con README/ROADMAP/TUTORIAL al navegar
      el repo.
- [ ] Falta `CONTRIBUTING.md` y `SECURITY.md` (mismo gap que azure-emulator).

### CI / Calidad
- [ ] `ci.yml` solo corre `go build`, `go vet`, `go test -race` — sin linter ni
      cobertura. Añadir `golangci-lint` y `go test -coverprofile`.
- [ ] No hay `go mod verify` / `go mod tidy --diff` en CI.

### Tests
- [ ] La densidad de tests es la mejor de los tres proyectos (226 funciones
      `Test*` en 50 archivos para ~25K líneas), pero la mayoría de servicios
      "simples" (artifactregistry, bigquery, certificatemanager, cloudbuild,
      filestore, firestore, gcs, gke, iam, iap, kms, logging, memorystore,
      monitoring, orgpolicy, pubsub, resourcemanager, secretmanager,
      servicenetworking, spanner, vpcaccess) tienen exactamente 1 archivo de
      test para 2 archivos de implementación — vale la pena revisar si cubren
      casos de error, no solo el camino feliz, antes de sumar más servicios.

### Backend real (Phase 12-15)
- [ ] El backend real opt-in (Postgres embebido para Cloud SQL, Docker para
      Cloud Run/Functions) es una superficie nueva y compleja
      (`internal/realbackend/`). Confirmar que tiene tests de fallback cuando
      Docker no está disponible o no hay RAM suficiente (el "budget governor"
      mencionado en el commit de Phase 12) — es el tipo de código que falla
      silenciosamente en CI si no se prueba explícitamente sin Docker.

### Paridad entre los 3 proyectos
- [ ] Adoptar la disciplina de changelog por entrada con atribución, como
      `ministack`, si el objetivo es publicar el proyecto con el mismo nivel de
      pulido.
