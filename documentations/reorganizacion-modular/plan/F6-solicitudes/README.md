# F6 · `solicitudes` — la solicitud, su bandeja, P5 y el puente CRM

> **Estado: por empezar** (spec escrita el 2026-09-28 sobre `dev` @ `1b18932`). Norma:
> [`05`](../../05-metodo-contratos-y-tdd.md). Forma: [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).
> Rutas: **autoridad** [`FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) §2.7 (G1–G18).
>
> Recalibrado el 2026-10-03 tras la parada de F1 (`05` E-12, §4.2, E-9, E-4; `plan/DECISIONES.md` §3).
>
> ✎ **D-F1-10 (Jhoan, 2026-10-02)**: los paquetes de suite de contrato y de dobles llevan el sufijo compuesto
> **`helpertest`**, el único que los candados de fichero eximen ([`DECISIONES.md`](../DECISIONES.md) §2). Esta spec los
> nombraba con `…test` (`intakestest`, `integrationstest`, `tenantvarstest`): se actualizó el sufijo, nada más.

## Objetivo, en tres líneas

1. Reconstruir, con la ceremonia que fije el inventario E-12 (`05` E-12), los **41 ficheros de producción** (12.236 líneas) de
   `internal/intakes/**` (+ `flujos/modules/cart/note.go`), `internal/integrations/**` e
   `internal/tenantvars` en `internal/modulos/solicitudes/…`; `internal/contracts` **desaparece**.
2. Conmutar: `cmd/server-modular` cablea los paquetes nuevos (stores, `Service`, notificador,
   recordatorios, P5, CRM, worker del outbox) y la cara nueva `internal/apipublica` sirve las **18**
   rutas G1–G18; huella idéntica; `cmd/server` no cambia ni un byte.
3. Mientras `conversacion` siga vieja (hasta F8), el carrito y el motor viejos siguen funcionando
   contra los objetos nuevos por **puertos estructurales**, una **segunda instancia vieja sin
   estado** o un **adaptador en `internal/arranque`** (FX `arquitectura.md` §4). Cero regresiones.

## Entradas (tiene que ser cierto para empezar)

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | F0–F5 cerrados en `dev`; la decisión de Jhoan tras el piloto F1 permite seguir | `ls internal/modulos/{acceso,edge,inferencia,catalogo}` · `ESTADO.md` |
| E2 | `nucleo/contact` (F1), `acceso/entitlements` (F2), `edge/grpc` (F3) y `inferencia/llmvia` (F4) están **conmutados**: el notificador nuevo usa el gw nuevo y el resolver de contactos nuevo; P5 usa el selector nuevo | `grep -rn 'modulos/edge/grpc\|nucleo/contact\|inferencia/llmvia' internal/arranque/*.go \| wc -l` > 0 |
| E3 | `internal/apipublica` tiene `FaseActual = 5` y el candado de mudanzas verde (TX.15) | `grep -n 'FaseActual' internal/apipublica/*.go` |
| E4 | El código viejo de referencia no cambió desde esta spec | `git log --oneline 1b18932..origin/dev -- internal/intakes internal/integrations internal/tenantvars internal/contracts internal/flujos/modules/cart/note.go` vacío; si no, se relee [`diseno.md`](diseno.md) §4 |
| E5 | `dev` verde con la toolchain fijada | skill `validar-antes-de-cerrar` |

## Salidas (es cierto al cerrar)

- `internal/modulos/solicitudes/{intakes,intakes/quotetext,intakes/telemetria,integrations,integrations/crmpush,integrations/sigv1,tenantvars}`
  con **41** ficheros de producción en verde y su `x_test.go` cada uno; paquetes de suite
  `intakeshelpertest`, `integrationshelpertest` (con el **doble nuevo**: `integrations` no tiene gemelo) y
  `tenantvarshelpertest`.
- `grep -rn 'pendiente.Implementar' internal/modulos/solicitudes | wc -l` → **0**; SKIP → **0**;
  sin umbral de cobertura (P2): **un test por promesa del contrato; mutantes en el nivel complejo;
  procesos de F9**. `make cobertura-ficheros` es un informe (la tabla va al PR; no bloquea). La verdad
  de los adaptadores SQL (`postgres.go` ×3, `buyerdata_postgres.go`) la da su suite con `Montaje`
  corrida en memoria y contra Postgres (P4) y F9.
- Inventario E-12 aprobado por Jhoan (T6.1): nivel de cada fichero y lista de adaptadores `bridge_<x>.go`.
- Los **cuatro candados de invariante** del módulo reescritos en el paquete nuevo (INV-1 aprobar,
  INV-1 pedir info, vencimiento, poda sellada) y el del contrato CRM (`crmpush`), **con sus rutas
  nuevas** y en verde; el esquema `wapp-crm-v1` validado desde `integrations` (sin paquete `contracts`).
- `cmd/server-modular` enlaza `internal/modulos/solicitudes/**` y la cara nueva sirve G1–G18
  (`FaseActual = 6`); huella igual; `go list -deps ./cmd/server | grep modulos/solicitudes` vacío.
- Puentes (import) declarados en `internal/modulos/fronteras_test.go`: **uno** (`solicitudes/intakes/telemetria
  → internal/flujos/store`, muere en F8). Adaptadores `bridge_<x>.go` e instancias transitorias en
  `internal/arranque` con fecha de muerte (F8), listados en [`arquitectura.md`](arquitectura.md) §4 y §4.1.
- `solicitudes` **no** entra en `Conmutados` al cerrar F6: entra cuando muere su último adaptador
  ([`reglas.md`](reglas.md) §4).
- Si D-F9-1 (F9 adelantado) está aceptada: T9.27 (9C `solicitudes`) cerrada por la sesión local.

## Orden de lectura

1. [`requisitos.md`](requisitos.md) — qué se exige, en EARS.
2. [`arquitectura.md`](arquitectura.md) — paquetes viejos → nuevos, imports, puentes, costuras con la
   conversación vieja, estado en memoria, cableado y rutas.
3. [`diseno.md`](diseno.md) — contratos por paquete, puertos y suites, reglas E-8, textos observables,
   candados.
4. [`reglas.md`](reglas.md) — trampas con `fichero:línea` y definición de hecho.
5. [`tareas.md`](tareas.md) — tareas y bloques de sesión.

## Bloques de sesión

Seis sesiones de 45–90 min, un bloque coherente cada una (P3). 🌐 web · 🌐❓ web si queda saldo de la
promoción, si no local · 💻 solo local. Cada una cierra con tres cosas: tareas `[x]` con SHA, un bloque en
`ESTADO.md` y los hallazgos nuevos en este README. El traspaso web ↔ local solo existe mientras haya dos entornos.

| Sesión | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| [**F6-01**](../sesiones/F6-01-web-inventario-y-hojas.md) · inventario E-12 + hojas | 🌐 | T6.1–T6.5, T6.14 | inventario E-12 **aprobado por Jhoan** (antes no se escribe código) · `sigv1` y `tenantvars` en verde, `note.go` con contrato y test · `ci-local` rc=0 · PR |
| [**F6-02**](../sesiones/F6-02-web-intakes-1.md) · `intakes` (1/2): contratos del paquete y tipos puros | 🌐 | T6.6–T6.8, T6.15 | los 24 ficheros de `intakes` con contrato y test + `intakeshelpertest`; `note.go` y los 10 tipos puros en verde · `vet -tags pendiente` rc=0 · PR |
| [**F6-03**](../sesiones/F6-03-web-intakes-2.md) · `intakes` (2/2): almacenes, acciones, notificador y candados | 🌐 | T6.9, T6.16–T6.18 | 0 pendientes en `S/intakes`; suite verde en memoria; candados del plazo y de la poda verdes, INV-1 escritos tras `pendiente` · PR |
| [**F6-04**](../sesiones/F6-04-web-quotetext-integrations-crmpush.md) · `quotetext`, `telemetria`, `integrations`, `crmpush` | 🌐❓ | T6.10–T6.13, T6.19–T6.21 | 0 pendientes en el módulo; puente (import) declarado; candado R-12 y esquema CRM verdes · PR |
| [**F6-05**](../sesiones/F6-05-web-cara-http-y-conmutar.md) · cara HTTP, cableado y conmutar G1–G18 | 🌐❓ | T6.22–T6.26 (= TX.16–TX.18) | 12 ficheros de `apipublica` en verde · huella igual · `go list -deps` · test de cableado completo · `FaseActual = 6` · PR |
| [**F6-06**](../sesiones/F6-06-cli-cierre.md) · cierre local | 💻 | T6.27–T6.29 | suites en memoria y contra Postgres; procesos P5/P6 (T9.27) contra los dos binarios · `dev` integrado · `ESTADO.md` |

Reparto de `intakes`: F6-02 escribe **todos** los contratos del paquete (las acciones y los almacenes dependen de los
tipos, T6.6 → T6.7 → T6.8) y deja en verde lo puro; F6-03 pone en verde lo que tiene estado, SQL o salida a WhatsApp
(`memory.go` primero, porque los tests de las acciones corren contra él) y cierra los candados. Tamaño: **sin medir**;
si una no cabe en ~90 min, para en un punto limpio y se relanza.

## Contradicciones encontradas (con `04`/`05`/`ESTADO`, medidas contra el código)

1. **`05` E-6** lista `integrations` entre los 12 sin gemelo en memoria: **cierto** (medido:
   `ls internal/integrations` → `crud gate outbox_stats postgres store worker`, ningún `memory`). Y
   `tenantvars` **sí** lo tiene (`internal/tenantvars/memory.go:14`, con reloj inyectable `SetClock`
   `:26`) — no estaba en la lista y no la contradice. `intakes` también (`memory.go`, 37 métodos de
   `*MemoryStore` frente a 21 de `*Postgres`: `grep -c 'func (m \*MemoryStore)'` / `'func (p \*Postgres)'`).
2. **`04` §3** pinta `solicitudes/contracts/(+ 1 _test.go)`: **desaparece** (`05` §6, D-10). Y hay
   **tres** tests que leen `docs/contracts/wapp-crm-v1/`, no uno: `internal/contracts/contract_examples_test.go:17`,
   `internal/integrations/contract_body_test.go` e `internal/publicapi/crmcallback_schema_test.go`
   (`grep -rln 'docs/contracts' --include='*_test.go' internal`). Destino: D-F6-3.
3. **`04` §3** pinta `intakes/(+ 50 _test.go)`: son los 50 tests **viejos** (316 `func Test`, 23 de
   ellos ficheros que llaman a `openTestDB`). Con `05` nacen **24** tests nuevos (uno por fichero)
   más la suite; los de integración van a F9 (P5, P6).
4. **`05` §3.2** nombra dos candados de `intakes` (`inv1_aprobar`, `{inv_vencimiento,sello_poda}`);
   hay **cuatro** ficheros AST: falta `inv1_pedirinfo_ast_test.go` (INV-1 para `RequestInfo`, reusa
   el barrido del de aprobar). Medido: `grep -l 'go/parser' internal/intakes/*_test.go`.
5. **`02` §4 / `05` §6** («puente de `telemetria` a `conversacion/store` hasta F8») es **el único
   puente de import** de F6. Pero no es la única costura con la conversación vieja: el **carrito
   viejo** exige tipos de `intakes` **viejo** en sus puertos (`cart/projection.go:53-66`:
   `intakes.Revision`, `intakes.ShippingPolicy`). `05` §4.1 solo prevé puentes nuevo → viejo; la
   dirección viejo → nuevo se resuelve en el arranque ([`arquitectura.md`](arquitectura.md) §4).
6. **`05` §3.2** dice «`inv1_aprobar_ast_test` lee sus seis directorios». Su **control positivo** es
   `../publicapi` (`inv1_aprobar_ast_test.go:46`, `var puertaDelDueño`): en el paquete nuevo pasa a ser `internal/apipublica`,
   y la lista de «flujos automáticos» **cambia por fase** (F6 viejos, F7 añade captación nueva, F8
   cambia a conversación nueva). La guarda anti-hueco **falla** si un directorio no existe: no se
   pueden listar hoy los de F7/F8.
7. **La cadena «`intakes` lo importa `publicapi` 36 veces»** se sostiene con esta regla: **8**
   ficheros de producción + **28** de test de `internal/publicapi` importan `internal/intakes`
   (`grep -l 'internal/intakes"' internal/publicapi/*.go`), con **118** usos de símbolo `intakes.X`
   en producción (incluye comentarios).
8. **`FX` §2.7** sitúa `quote-suggestion` (G7) en F6 con el plazo inyectado; el **constructor** de
   P5 vive hoy en la fase de **captación** del arranque viejo (`fase5_captacion.go:342-347`), no en la
   de solicitudes. La conmutación de F6 toca por tanto `fase3`, `fase5`, `fase6`, `fase7`, `fase8` y
   `fase9` del arranque nuevo (no solo `fase6`).

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F6-1 | El carrito viejo (F8) necesita un `RevisionWriter`/`ShippingEnsurer` con tipos de `intakes` **viejo**. ¿Segunda instancia vieja de `intakes.Postgres` (sin estado) para él hasta F8, o adaptador de tipos `bridge_intakes.go`? ⚠️ Decidida «segunda instancia» el 2026-09-30; **P5** (2026-10-03) hizo del adaptador el mecanismo estándar de F2–F7: Jhoan la confirma o la cambia al aprobar el inventario E-12 (T6.1) | **Segunda instancia vieja** (salida 3 de FX §4): `intakes.Postgres` no guarda estado (solo `*sql.DB`, cipher y logger; `grep -n 'sync\.' internal/intakes/postgres.go` vacío). Adaptar `Revision` (22 exportados en `revisions.go`) costaría más que lo que protege |
| D-F6-2 | Los candados AST INV-1 (aprobar/pedir info) tienen una lista de directorios que **cambia en F7 y F8**. ¿Se aceptan re-toques del candado en esas fases? | **Sí**, como tareas explícitas (T7.x, F8) y con la guarda anti-hueco intacta |
| D-F6-3 | Dónde vive la validación del esquema `wapp-crm-v1` al desaparecer `contracts` | `crmpush/push_test.go` valida `intake.push` (ejemplo, casos negativos y el payload de `Build`); `apipublica/crmcallback_test.go` valida `intake.status`; los **ejemplos contra su esquema** (5 tests de `contract_examples_test.go`, incl. `catalog.pull` y draft 2020-12) en **un** test con nombre propio `integrations/contrato_wapp_crm_v1_test.go`, **excepción declarada a E-3** (test de un contrato externo, sin fichero homónimo) |
| D-F6-4 | El texto `"cart: la indicación mide %d runas y el máximo es %d"` (`cart/note.go`) nace en `solicitudes/intakes/note.go` con el prefijo `cart:` | **Conservarlo byte a byte** (`05` §8: se renombra el paquete, no el texto observable) y decirlo en el comentario del contrato |
| D-F6-5 | `Service.Summary` usa `time.Now()` directo (`service.go:318`) | **Inyectar el reloj** en el contrato nuevo (opción con defecto `time.Now`), para que el test no dependa del reloj real (skill `contrato-tdd`) |
| D-F6-6 | Tres ficheros llevan SQL de un adaptador Postgres **sin** llamarse `*postgres*.go` (ya no hay umbral de cobertura, P2: la verdad de ese SQL la da la suite con `Montaje` corrida en memoria y contra Postgres): `intakes/buyerdata.go` (`PostgresBuyerData`), `integrations/crud.go` (`(*Postgres).SecretFingerprint`, `:42`) e `integrations/outbox_stats.go` (`(*Postgres).CountOutbox`, `:69`) | **Partirlos por la convención de nombres** (`estructura.md` §3): `buyerdata.go` (tipos, `Fingerprint` puro) + `buyerdata_postgres.go`; los dos métodos de `integrations` a `postgres.go`. Cambia el árbol de `04` §3 en tres nombres, sin cambiar nada observable |
| D-F6-7 | **Heredado de F9-02 (H-1)**: el *webhook worker* viejo (`internal/integrations/worker.go:209` y `:225`) loguea a `ERROR` cuando se cancela el contexto a mitad de su primera llamada a BD, y `TestP0_Arranque/sin_errores` (que exige «cero `ERROR`», parada incluida) falló 1 de 161 arranques en frío. Jhoan **difirió a propósito** el arreglo (2026-10-01): el código viejo no se toca y el test probablemente se redefine al reconstruir. ¿Qué promete el worker nuevo, y cómo queda el criterio de P0? | **Evaluar aquí, sin gastar tiempo antes**: (1) el contrato de `integrations/worker.go` promete «contexto cancelado → vuelve **sin** loguear a `ERROR`», con su caso (se escribe en T6.12 y no se retoca al final); (2) en T6.27, con ese worker, P0 se vuelve a medir (`CUENTA=3`, en frío): si `sin_errores` deja de ser intermitente, se queda; si no, se **redefine** (comprobar el log antes de la parada, o aceptar solo cancelaciones posteriores a «señal de parada recibida») o lo sustituye un test más acorde. Cifras y salidas: README de F9, contradicción 19. ⚠️ **Revisión independiente (2026-10-01)** — hechos, sin tocar esta decisión: (i) el mismo patrón está en otras tres goroutines de fondo que **F6 no reconstruye** (`platform/metrics/flowlifecycle/collector.go`; `flujos/runtime/aggregator.go`, F8; `intake/pipeline/pipeline.go`, F7), y el binario `viejo` conserva el worker viejo hasta F10; (ii) `CUENTA=3` son 2 arranques en frío expuestos y da verde ≈ 98,8 % de las veces sin arreglar nada; (iii) regla de triaje hasta entonces: es esta carrera un rojo cuyas líneas `ERROR` sean todas de cancelación, de una goroutine de fondo y de la parada. Detalle en la nota de revisión de la [contradicción 19 del README de F9](../F9-procesos/README.md); el alcance y el criterio de remedición quedan como pregunta abierta **D-F9-10** |
