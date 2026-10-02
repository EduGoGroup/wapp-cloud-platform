# F6 · Reglas — lo que no se toca, las trampas y la definición de hecho

## 1 · Lo que no se toca

- **El código viejo**: `internal/intakes/**`, `internal/integrations/**`, `internal/tenantvars`,
  `internal/contracts`, `internal/flujos/modules/cart/note.go`, `internal/publicapi/**`,
  `internal/bootstrap/**`, `cmd/server`. Es la referencia y lo que corre en UAT (E-1).
- **`docs/contracts/wapp-crm-v1/`** (7 ficheros + `examples/`): contrato externo congelado. Se lee
  desde el test nuevo con otra ruta relativa; la carpeta no se mueve.
- **Las migraciones** (`internal/platform/storage/postgres/migrations/structure/*.sql`): cero cambios.
- **La lista `fases` y el orden de construcción** de `internal/arranque` (`orquestador.go:51-61`; el comentario «el orden ES el contrato», `:45-50`).
- `documentations/literal-aviso-sesion-pasiva.md` (no es de F6, pero ningún bloque lo toca).

## 2 · Trampas conocidas

| # | Trampa | `fichero:línea` | Qué hacer |
|---|---|---|---|
| T-1 | 🔴 **Homónimo DEK**: `buyerdata.go` y las columnas `*_dek` son el envelope de PII de negocio, **no** la DEK del ADR-0007 | `internal/intakes/buyerdata.go:11` · `constitucion.md` §3.1 | Ni un comentario nuevo que diga «DEK del cliente»; el sobre lo custodia esta pieza con su KEK |
| T-2 | `intake` (cola, F7) ≠ `intakes` (solicitud, F6) | `internal/intake/store.go` («⚠️ NO CONFUNDIR») | En el arranque, alias `intakesviejo`/`intakeviejo`, nunca abreviar a `in` |
| T-3 | Los candados AST INV-1 **fallan** si un directorio listado no existe (guarda anti-hueco) | `inv1_aprobar_ast_test.go` (`llamadasA` → `Fatalf`) | Listar solo lo que existe en la fase (diseño §6); re-tocar en F7 y F8 |
| T-4 | El candado de vencimiento **viejo** barre el repo entero como **texto**: un literal completo del evento de expiración en cualquier fichero nuevo lo pone rojo | `inv_vencimiento_ast_test.go:44,69` (`raízDelRepo = "../.."`) | Componerlo siempre por concatenación, también en tests y en esta documentación |
| T-5 | El prefijo `cart:` del error de `note.go` **es observable** aunque el fichero cambie de paquete | `cart/note.go` (`NoteTooLongError.Error`) | D-F6-4: se conserva |
| T-6 | Candados de cableado copiados en F0 buscan el **texto** `quotetext.NewServicio`, `quotetext.ConSemilla`, `stages.ConEmpujeCRM` y el campo `QuoteSuggestions` | `internal/bootstrap/arranque/quotetext_cableado_test.go:56-69`, `reanalisis_cableado_test.go:59-91` | El paquete **nuevo** conserva el nombre corto en el arranque; el viejo va con alias; reajustar la aserción de `QuoteSuggestions` a la cara nueva (T6.25) |
| T-7 | El `Service` se necesita **antes** de existir: la etapa `draft` (captación) recibe una clausura que lee `c.intakeService`, asignado una fase después | `fase5_captacion.go:218-233`, `fase6_solicitudes.go:61` (`c.intakeService = intakes.NewService(…)`) | No «arreglar» el ciclo: la clausura se resuelve al llamar; `PushRevisionByID` es nil-safe |
| T-8 | El recordatorio del plazo a la dueña es un **sumidero de log** (el push del Plan 045 no existe) | `fase6_solicitudes.go:45-56` (`NewExpiryReminder(intakes.NewLogOwnerNotice(…))`, `:56`) | El contrato lo dice; nadie afirma que la dueña lo recibe |
| T-9 | `quote-suggestion` espera al modelo dentro de la petición (24,8–35,5 s en UAT) contra `WriteTimeout` 10 s | `plazoescritura.go:67,87` · `publicapi.go:770` | Plazo propio **derivado** (48 s + 12 s), inyectado (FX §4.3) |
| T-10 | Un `CounterVec` no aparece en `/metrics` hasta su primer incremento | `contratos.md` §8 | La huella compara **nombres declarados**, no el cuerpo de `/metrics` |
| T-11 | Tres ficheros con SQL que no se llaman `*postgres*.go` | `buyerdata.go`, `integrations/crud.go:42`, `outbox_stats.go:69` | D-F6-6 |
| T-12 | Dos `ServeMux`: un comodín de la cara nueva tapa un literal de la vieja | FX mapa §4.2 | G2 · G9 · G10 en el mismo commit |
| T-13 | `rows.Close()` con `_ = cerr` (41 sitios, 5 en `intakes/postgres.go`) | `deuda.md` D-17 | No se porta el ritual mudo: se registra (o se decide en el commit, E-8) |
| T-14 | `make test-integration` usa `postgres:16` y UAT `postgres:17-alpine` | `Makefile:57` | Los procesos de F9 con 17 |

## 3 · Prohibiciones

- `t.Skip` en código nuevo; tests contra Postgres vivo; `WAPP_TEST_DB_DSN` en `internal/modulos`.
- Importar desde `internal/modulos/solicitudes` un paquete viejo que no sea el puente declarado
  (`telemetria → internal/flujos/store`).
- Crear una segunda instancia de `Service`, notificador, recordatorios o worker del outbox.
- Renombrar un texto observable, una clave de wire, un estado, un evento o una métrica.
- Un `rojo` y su `verde` en el mismo commit; squash al integrar el PR.

## 4 · Definición de hecho (F6)

1. 41 ficheros de producción + `note.go` en `S/…`, cada uno con `x_test.go`; `intakeshelpertest`,
   `integrationshelpertest` (con doble), `tenantvarshelpertest`.
2. `grep -rn 'pendiente.Implementar' internal/modulos/solicitudes | wc -l` → 0.
3. `GOWORK=off go test -count=1 -race -v ./internal/modulos/solicitudes/... > "$TMPDIR/s.log" 2>&1; echo rc=$?; grep -c -- '--- SKIP' "$TMPDIR/s.log"` → `rc=0` y `0`.
4. `make cobertura-ficheros` ≥ 80 % por fichero, fuera los adaptadores Postgres (D-F6-6).
5. Los 5 candados de §6 de `diseno.md` en verde con sus rutas nuevas.
6. `make ci-local` rc=0 leído del log; `go vet -tags pendiente ./...` rc=0.
7. Huella igual; `go list -deps ./cmd/server-modular | grep -c modulos/solicitudes` > 0 y
   `go list -deps ./cmd/server | grep -c modulos/solicitudes` = 0; test de cableado de §6 de
   `arquitectura.md` verde.
8. G1–G18 servidas por `apipublica`; `FaseActual = 6`; candado de mudanzas verde (TX.18).
9. Traspaso `documentations/reorganizacion-modular/traspasos/TRASPASO-F6-solicitudes.md` con su
   `CERRADO` (procesos P5/P6 y, si D-F9-1, T9.27); `ESTADO.md` al día.
