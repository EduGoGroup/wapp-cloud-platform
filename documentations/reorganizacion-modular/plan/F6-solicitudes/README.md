# F6 · `solicitudes` — la solicitud, su bandeja, P5 y el puente CRM

> **Estado: en curso** — F6-03 hecha el 2026-10-08 (`intakes` entero en verde: almacenes, las 9 acciones, notificador y candados; `PENDIENTES=0`, `ROJOS=1`). Antes, F6-02 el mismo día (contratos y tipos puros). Arrancada el 2026-10-07 (F6-01) sobre `dev` @ `3a21138`; inventario E-12 **aprobado** por
> Jhoan ([`diseno.md`](diseno.md) §1.2). Spec escrita el 2026-09-28 sobre `dev` @ `1b18932`. Norma:
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
| [**F6-02**](../sesiones/F6-02-web-intakes-1.md) · `intakes` (1/2): contratos del paquete y tipos puros | 🌐 | T6.6–T6.8, T6.15 | los 24 ficheros de `intakes` con contrato y test + `intakeshelpertest`; `note.go` y los 10 tipos puros en verde · `vet -tags pendiente` rc=0 · PR ✅ hecha el 2026-10-08: son **40** ficheros y **9** tipos puros (hallazgo 11) |
| [**F6-03**](../sesiones/F6-03-web-intakes-2.md) · `intakes` (2/2): almacenes, acciones, notificador y candados | 🌐 | T6.9, T6.16–T6.18 | 0 pendientes en `S/intakes`; suite verde en memoria; candados del plazo y de la poda verdes, INV-1 escritos tras `pendiente` · PR ✅ hecha el 2026-10-08 (hallazgos 24–34) |
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

## Hallazgos

### F6-01 (2026-10-07; rama `reorg/f6-01-inventario-y-hojas` desde `dev` @ `3a21138`)

1. **`sigv1` no tiene ventana ±300 s ni reloj.** El timestamp es un parámetro `int64` de `Sign`/`Verify`; la ventana
   vive en `internal/publicapi/crmcallback.go:38` (`crmCallbackWindow`) y se compara en `:293`. `diseno.md` §2.6 y T6.2
   se la atribuían a `sigv1`. ✅ Jhoan (2026-10-07): el contrato nuevo es **fiel al viejo**; la ventana la promete y la
   prueba `apipublica/crmcallback` (TX.16, F6-05). R6.4.c ya decía «la cara nueva»: no se toca.
2. **El SQL fuera de `postgres.go` está en siete ficheros, no en tres.** D-F6-6 y T-11 no listaban
   `intakes/aprobadas.go:83`, `crm.go:113`, `customernote.go:46` ni `reanalisis.go:89`, cada uno con un método de
   `*Postgres`. ✅ Jhoan amplió D-F6-6: nacen en `postgres_<tema>.go` (`diseno.md` §1.2).
3. **La costura viejo → nuevo no es solo con la conversación.** Tres puertos de la captación vieja exigen tipos del
   `intakes` viejo (`stages/draft.go:342`, `pipeline/pipeline.go:147`, `reanalisis/reanalisis.go:245`) y la spec no los
   recogía. La segunda instancia vieja de D-F6-1 sirve a **cuatro** puertos y muere en F7 **y** F8; necesita
   `ConCifraDeLiteral`. ✅ Jhoan mantuvo D-F6-1 sabiéndolo (`arquitectura.md` §4). Afecta a T6.24 (F6-05) y a F7.
4. **`diseno.md` §5 no enumeraba los textos observables.** Recuento: 156 `errors.New`/`fmt.Errorf` (121 envoltorios
   `%w`, 35 propios), más tres familias que el comando no ve: siete errores tipados con `Sprintf`, las plantillas de
   `notifier.go` y los `Motivo…` de `quotetext/precios.go`. Anotado en §5; lo asertan F6-02…F6-04.
5. **Cifras y comandos de esta spec que no cuadran** (ninguno cambia el trabajo): son **12.235** líneas, no 12.236
   (`arquitectura.md` §1 acierta); el comando de la entrada E3 sale vacío porque `FaseActual` vive en
   `internal/arranque/mudanzas.go:23`, no en `internal/apipublica`; `grep -l 'go/parser' internal/intakes/*_test.go`
   devuelve 3 candados y no 4 (`inv1_pedirinfo_ast_test.go` reusa el barrido sin importar `go/parser`); `note.go`
   tiene **4** exportados con la regla del candado (`MaxNoteRunes`, `NoteTooLongError`, su `Error`, `SanitizeNote`);
   de los 14 tests de `cart/notes_test.go` solo 2 son de `SanitizeNote`.
6. **`bridge_contact` solo ofrece el resolver envuelto.** `newContactResolver` devuelve hoy el `contactBridge`; el
   notificador nuevo necesitará el resolver de `nucleo/contact` sin envolver. Lo resuelve T6.24 (F6-05).
7. **No había en el repo un driver `database/sql` falso con transacciones.** Los tres modelos (`receipts`,
   `entitlements`, `degradation`) responden «sin transacciones» a `Begin`. `tenantvars/postgres_fakedb_test.go`
   (`8c7ce8b`, `177f529`) es el primero que las soporta (`Begin`, `CheckNamedValue` para los `[]string` y marca `inTx`
   por conexión): es el modelo para los `WithTx` de `intakes/postgres*.go` (F6-03).
8. **El orden de `List` puede divergir entre memoria y Postgres.** La memoria ordena byte a byte y Postgres con la
   colación de la base; con mayúsculas, signos o acentos en la clave no tienen por qué coincidir. El viejo ya era así.
   La suite solo usa claves en minúsculas ASCII y lo dice; no se «arregla» (sería cambiar lo observable). 🟡 Si F6-06
   lo ve divergir contra Postgres con claves reales, es decisión de Jhoan.
9. **La suite de `tenantvars` no depende del reloj de pared.** `Montaje.Advance` adelanta el reloj inyectado en memoria
   y, contra Postgres, lee `clock_timestamp()` hasta que avanza (sin `Sleep`); el test viejo confiaba en que dos
   transacciones seguidas caen en microsegundos distintos. La semántica real de `IS DISTINCT FROM` solo la prueba esa
   suite contra Postgres (`test/procesos/tenantvars_contrato_test.go`, `8cb2129`), **escrita y compilada, no corrida**:
   la corre T6.27 (F6-06).
10. **Comportamientos del viejo fijados con vectores literales** (el árbol nuevo no puede importar lo viejo): `Verify`
    acepta hex en mayúsculas y rechaza el prefijo `v1=`; `SanitizeNote` quita el ZWJ (U+200D: un emoji compuesto sale
    descompuesto), convierte U+00A0 y U+3000 en espacio corriente, **deja pasar** U+2060, U+00AD, U+2065, U+206A y
    U+FEFE, y cuenta un U+FFFD por byte malformado; `NoteTooLongError.Runes` es el largo **saneado**. Mutante
    equivalente: quitar U+2029 de `isLayoutRune` no cambia nada (`strings.Fields` ya lo trata como espacio).

### F6-02 (2026-10-08; rama `reorg/f6-02-intakes-contratos-y-tipos` desde `dev` @ `64c181a`)

11. **`S/intakes` son 40 ficheros de producción, no 24.** La ficha contaba los del viejo. Con D-F6-6 ampliada y E-13 nacen
    4 `memory*.go`, 12 `postgres*.go` (8 por tema + `approved`, `crm`, `customernote`, `reanalysis`), `buyerdata_postgres.go`
    y dos que la spec no preveía: `service_metrics.go` y `service_revalidate.go`. El viejo colgaba métodos de `*Service` y
    opciones (`WithMetrics`, `WithMetricsClock`, `ApplyRevalidation`) de `metricas.go` y `revalidate.go`, que aquí son
    ficheros puros y se pusieron en verde antes de existir `Service`. Los tipos puros son **9**, no 10 (`customernote.go` no nace).
12. **E-11 obliga a renombrar ~30 exportados del viejo** (`PartirLiteral` → `SplitLiteral`, `ConCifraDeLiteral` →
    `WithLiteralCipher`, `PublicadorDeMetricas` → `MetricsPublisher`…). La spec, `arquitectura.md` y las tareas de F6-03…F6-05
    los citan con el nombre viejo: la tabla está en [`tareas.md`](tareas.md), bloque F6-02. Los nombres de **fichero** en
    español se conservaron (`metricas.go`, `vencimiento.go`, `aprobadas.go`, `reanalisis.go`), porque así los nombra el
    inventario aprobado; los ficheros que no existían en el viejo nacen en inglés. 🟡 Criterio del orquestador: si Jhoan
    prefiere los ficheros en inglés, es un `git mv` sin tocar contenido.
13. ✅ **La suite exige que `UpdatedAt` se refresque en toda escritura de cabecera** (`UpdateStatus`, `EnsureShippingLine`
    si cambia algo, `ReplaceItems`, `ApplyRevalidation`, `Discard`, `AbandonByEvent`; no en los dos recordatorios). Es lo
    que hace el Postgres viejo (`updated_at = now()`); el `MemoryStore` viejo solo lo movía en `AbandonByEvent`. Memoria y
    Postgres divergían ya en el viejo, y `UpdatedAt` es la base de `Overdue`. Se eligió Postgres, que es lo que corre en
    UAT. Punto único donde vive la regla: `adoptRefreshedUpdatedAt` en `intakeshelpertest/snapshot_contrato.go`.
    **Decidido por Jhoan el 2026-10-08 (D-F6-8): se mantiene.** El `MemoryStore` nuevo refresca `UpdatedAt` en toda
    escritura de cabecera, a diferencia del doble viejo: lo escribe F6-03 (T6.16).
14. ✅ **`(*MemoryStore).StoredStatus` es un exportado nuevo**, fuera del API viejo: todas las lecturas del puerto
    normalizan el estado y, sin ese mirador, la marca de estado (hallazgo 35) no distingue `closed` de `confirmed`.
    Alternativa: quitar `Montaje.StoredStatus` y perder esa columna de la vigilancia. **Decidido por Jhoan el
    2026-10-08 (D-F6-9): se conserva.**
15. ✅ **El `Montaje` de Postgres no cableaba el cifrador del literal** (decidido por Jhoan el 2026-10-08, **D-F6-10**: la
    suite reexporta la opción; el candado no se toca). La regla 3b del candado `ProcessImports` solo deja usar los `New…`
    del paquete del puerto desde `test/procesos`: `intakes.WithLiteralCipher` muerde, y sin cifrador
    `InsertRevision_LiteralLeavesThePayloadAndReturnsOnRead` habría fallado contra Postgres en F6-06. Ahora
    `intakeshelpertest.WithLiteralCipher` la reexporta y el `Montaje` la cablea con un `crypto.FieldCipher` de keyring
    propio, como `tenantllm_contrato_test.go`. Escrito y compilado, **no corrido** (lo corre F6-06). Además, `test/procesos/intakes_contrato_test.go` lleva `integracion && pendiente` (el adaptador en
    rojo hace `panic` y abortaría el binario de procesos): F6-03 quita `&& pendiente` al poner `postgres*.go` en verde.
16. **D-F6-5 quedó en dos relojes independientes**: `WithClock` (nuevo, para `Summary`) y `WithMetricsClock` (viejo).
    Ninguno mueve al otro. Es lo fiel al viejo, donde el reloj de las métricas no tocaba `Summary`.
17. **Conductas incidentales del viejo, fijadas en test para que cambiarlas sea una decisión**: (a) `PushRevisionByID`
    lee por `Service.Get`, así que con el puente CRM cableado el pipeline **dispara los recordatorios perezosos**
    (`V/reanalisis.go:137`); (b) `Approve` y `RequestInfo` rechazan el texto vacío **antes** de resolver la solicitud,
    al revés de lo que dice su propio comentario («el recurso antes que el cuerpo»); orden real de `Approve`:
    `ErrNoQuoteSender` > `ErrNoRevisionWriter` > `ErrEmptyQuoteText` > `ErrNotFound` > `NotApprovableError` >
    `PendingPriceError` > `ErrEmptyQuote`; (c) un texto que solo lleva U+200B **no** es vacío y se aprueba tal cual;
    (d) `NotifyCRMStatus` exige el lector de configuración aunque no lo lee, y `SendQuote`/`SendQuestion` no.
18. **Defecto latente del viejo, sin fijar en test**: si el contenido descifrado de una fila de datos del comprador fuera
    el JSON `null`, `GetBuyerData` devuelve `(nil, true, nil)` y `PutBuyerField` entraría en pánico al asignar a un mapa
    nil (`internal/intakes/buyerdata.go:107`, `:217`). Hoy es inalcanzable (solo se escriben objetos). F6-03 decide si se endurece.
19. **Lo que el contrato dice ahora y el viejo callaba** (hallado con los corpus adversarios, T-15): la comparación de
    estados es byte a byte (ni mayúsculas, ni espacios, ni U+200B, ni la `ｃ` de ancho completo son el alias `closed`);
    `Offset` no sanea (página 0 → −50); `SplitLiteral` reserializa con claves en orden alfabético y escapa `<`, `>`, `&`
    y U+2028; `MergeLiteral` acepta las posiciones `"+0"`, `"-0"` y `"01"`; `PriceList.Lookup` es exacto, sin plegar
    mayúsculas; dentro del céntimo de tolerancia la línea se lleva igualmente el precio vigente.
20. **`diseno.md` §2.1 atribuye mal dos símbolos**: `StatusNotice` vive en `notifier.go` (no en `status.go`) y
    `LineChange` en `revalidate.go` (no en `revisions.go`). El candado de `approve_contrato_test` contra `stages.Linea`
    no cabe en `S/intakes`: toca a quien reconstruya el productor (F7).
21. **Los escapes `\uXXXX` llegan al disco como runa cruda** con las herramientas de escritura de los agentes (le pasó
    a cinco de ocho). Un U+200B o un U+00A0 crudos en un literal de test son invisibles en la revisión. Barrido usado antes
    de cada commit: `perl -CSD -ne 'print "$ARGV:$.\n" if /[\x{00A0}\x{00AD}\x{200B}-\x{200F}\x{2028}-\x{202E}\x{2060}-\x{206F}\x{3000}\x{FEFF}]/'`.
    Candidato a candado (junto al de identificadores no ASCII que `05` E-11 deja por decidir).
22. **Ocho sub-agentes a la vez en un mismo paquete funcionó**, con tres condiciones: ficheros disjuntos, el API
    exportado fijado de antemano (copia fiel del viejo + tabla de renombres) y un solo commiteador. Cada agente validó
    lo suyo con `go … -overlay` mientras los demás escribían. Coste: dos commits intermedios no compilan solos
    (`743d85d` usa `ReservedSKUPrefix`, que entra en `9f393c2`; `9f393c2` con `-tags pendiente` necesita `c366c68`).
23. **No leído entero (E-8)**: de los `*_integration_test.go` de `intakes` distintos de `postgres_integration_test.go`
    solo se leyeron las cabeceras de cada test, no los cuerpos. F6-03 los lee al poner en verde cada almacén.

### F6-03 (2026-10-08; rama `reorg/f6-03-intakes-almacenes-acciones-candados` desde `dev` @ `5fd533e`)

24. **La suite de F6-02 tenía dos errores, y los destapó ponerla contra algo real.** (a) `caseDiscard` esperaba
    `from_status` con la clave **almacenada** (`closed`); el viejo normaliza en `DiscardedRevisionPayload` para los dos
    almacenes, y el contrato nuevo y su test también: corregida (`7a70dfe`). (b) Los dos casos «no le toca» de los
    recordatorios afirmaban `(false, nil)` para un id que no es UUID; el Postgres viejo devuelve `ErrNotFound` y el doble
    `(false, nil)`. ✅ **Jhoan, 2026-10-08**: sin una mejora clara, manda el viejo; el caso sale de la suite compartida y lo
    afirma el test de cada adaptador (`14393a6`). Con eso la suite da **50 de 50 contra Postgres real**.
25. **Pre-chequeo contra Postgres, corrido** (no previsto: la sesión solo debía dejarlo escrito). Con el adaptador en verde,
    `test/procesos/intakes_contrato_test.go` pierde `&& pendiente` (`8d6cfa8`, hallazgo 15) y
    `TestIntakesContrato_Postgres` da rc=0, 50 PASS, 0 SKIP contra el binario viejo, una vez. **No cierra T6.27**: la
    corrida que cuenta, con los dos binarios, es de F6-06.
26. **`diseno.md` §6 da rutas que no existen para el candado INV-1**: `../../../../flujos/runtime` y las tres de `intake`
    llevan cuatro `..` (saldrían del repo); son tres, como el control positivo `../../../apipublica`. El candado nace con
    las correctas; con las del diseño, la guarda anti-hueco habría cortado siempre. `diseno.md` no se ha tocado.
27. **Los nombres del candado de la poda nacieron en inglés** (`revisionsOf → runPrune → sealPruned`), no los
    `ejecutarPoda`/`sellarPodada` que cita `diseno.md:277`: son identificadores nuevos (E-11). Tabla en `tareas.md`,
    bloque F6-03. El candado nuevo caza además la asignación al identificador en blanco, que el viejo no cubría.
28. **El `MemoryStore` nuevo se aparta del doble viejo en cinco puntos**, todos hacia lo que hace Postgres: `UpdatedAt` en
    toda escritura de cabecera (D-F6-8); `AddedAt` del reloj inyectado; lo rechazado **no escribe nada** (el viejo mutaba
    líneas antes de validar el texto de la revalidación); `ApprovedRenderedTexts` recorta como `btrim`, no `TrimSpace`
    (un texto de solo NBSP ahora cuenta); y no se porta el segundo CAS del evento en `Discard`, inalcanzable.
29. **El inventario se quedó corto en dos ficheros**: `postgres_revisions_read.go` (la lectura de revisiones, que `Get`
    necesita; el adaptador queda en 13 trozos) y `notifier_templates.go` (sin él `notifier.go` pasaba de 600).
    En la franja 500–600 quedan `notifier.go` 575, `service.go` 561, `vencimiento_test.go` 537 y
    `approve_service_test.go` 503.
30. **Portar en paralelo dentro de un paquete duplica los auxiliares compartidos.** Los que el viejo tenía una sola vez
    para los dos almacenes (`señalDeCorrección`, `correctedRevision`, `revisionLinesOf`, `systemItems`,
    `discardedRevision`) acabaron copiados tres veces con prefijos (`memory…`, `pg…`, `approved…`) porque el contrato en
    rojo no los nombraba y nadie era su dueño. Unificados al final (`141acfd`). Para la próxima: el brief de la ola fija
    **también** el dueño y el nombre de los no exportados compartidos.
31. **El lint no había visto nunca los tests rojos**: `golangci-lint` no lee lo que va tras `//go:build pendiente`, así
    que al quitar la etiqueta salieron 31 avisos de golpe (19 `errcheck`, 6 `gocyclo`…), casi todos de tests escritos en
    F6-02. A 0 sin `//nolint` (`4fcccc6`). Candidato a mejora del gate: un `lint` con `--build-tags pendiente` en el rojo.
32. 🟡 **JSON `null` en los datos del comprador (hallazgo 18), sin endurecer.** Medido: `PutBuyerField` entra en pánico
    (`assignment to entry in nil map`) **y la transacción no se revierte** (el `defer` solo revierte con `err != nil`);
    `GetBuyerData` devuelve `found=true` con mapa nil. Es idéntico al viejo y hoy inalcanzable (el blob lo escribe siempre
    este código como objeto). Con el criterio del hallazgo 24, se conserva. Si se endurece: tratar `data == nil` tras el
    `Unmarshal` como «no es un objeto» en los dos puntos.
33. **Tres mutantes que el rojo de F6-02 no mataba**, ahora con test: el orden `ErrNoQuoteSender` > `ErrNoRevisionWriter`
    (el caso usaba un store que sí escribía revisiones), el total de la revisión `approved` y `BuyerDataPresent` del
    detalle. En total, **253 mutantes a mano, 252 muertos y 1 equivalente** declarado en su commit (`7080179`).
34. **No leído entero (E-8), otra vez**: de los `*_integration_test.go` viejos de recordatorios, envío, descarte y lectura
    solo se leyeron nombres o parte; `postgres_integration_test.go` no se leyó para lectura y estado. Lo compensa en parte
    la corrida real del hallazgo 25. 🔴 **14 de los 28 commits no compilan solos** (medido: `go vet` del paquete en
    cada SHA; fallan `bb50152` … `7a70dfe`, y desde `4c93249` todos pasan): con once agentes escribiendo a la vez y un
    commit por fichero, cada verde se commiteó al llegar su agente, antes que los ficheros de los que dependía. Peor
    que los dos de F6-02 (hallazgo 22): `git bisect` no sirve dentro de ese tramo. Para la próxima, commitear al cerrar
    la ola, en orden de dependencias. Un solo commiteador, sin *worktrees*.
