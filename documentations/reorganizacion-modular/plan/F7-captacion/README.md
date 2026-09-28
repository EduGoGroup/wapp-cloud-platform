# F7 · `captacion` — la cola que convierte una conversación en borrador (P2→P4, match, draft)

> **Estado: por empezar** (spec escrita el 2026-09-28 sobre `dev` @ `1b18932`). Norma:
> [`05`](../../05-metodo-contratos-y-tdd.md). Forma: [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).
> Rutas: **autoridad** [`FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) §2.5 y §2.8
> (H1, y E1–E2 por **D-FX-1** resuelta en su alternativa: las intenciones se mudan **aquí**).

## Objetivo, en tres líneas

1. Reconstruir por contrato → rojo → verde los **32 ficheros de producción** (11.144 líneas) de
   `internal/intake` (la cola `intake_jobs`), `intake/{pipeline,stages,anclaje}`, `internal/{intakeahead,
   evidence,reanalisis,casebank,intentcfg}` en `internal/modulos/captacion/…` (aplanado, `04` §4).
2. Conmutar: `cmd/server-modular` cablea el worker del pipeline (W=1), el aforo (K=1), las cinco
   etapas, el adelanto por pull y la puerta del re-análisis **nuevos**; la cara nueva sirve H1
   (`/reanalyze`) y E1–E2 (`/intents`); huella idéntica; `cmd/server` no cambia.
3. Congelar el **ciclo 2** de `02` §4 (`conversacion · captacion · catalogo · solicitudes`) con
   **tres puentes declarados** a la conversación vieja y **adaptadores** en `internal/arranque` hacia el
   agregador y el compositor viejos, todos con muerte en F8 (D-7).

## Entradas (tiene que ser cierto para empezar)

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | F6 cerrada: `modulos/solicitudes` conmutado (la etapa `draft` escribe por el `intakes` **nuevo**; `SanitizeNote` vive en `solicitudes/intakes/note.go`) | `ls internal/modulos/solicitudes/intakes/note.go` · `FaseActual = 6` |
| E2 | F5 cerrada: `modulos/catalogo` (modelo `Catalog`/`Article`…) y `modulos/catalogo/indice` (la caché del match) conmutados | `ls internal/modulos/catalogo/indice` |
| E3 | F4 cerrada (`inferencia/tenantllm`, selector de vía nuevo) y F2 (`acceso/entitlements`) | `grep -rn 'modulos/inferencia\|modulos/acceso/entitlements' internal/arranque \| wc -l` > 0 |
| E4 | El código viejo de referencia no cambió desde esta spec | `git log --oneline 1b18932..origin/dev -- internal/intake internal/intakeahead internal/evidence internal/reanalisis internal/casebank internal/intentcfg` vacío |
| E5 | `dev` verde con la toolchain fijada | skill `validar-antes-de-cerrar` |

## Salidas (es cierto al cerrar)

- `internal/modulos/captacion/{intake,pipeline,stages,anclaje,intakeahead,evidence,reanalisis,casebank,intentcfg}`
  con **32** ficheros en verde, cada uno con su `x_test.go`; suites `intaketest` (dos puertos:
  `JobStore` y `PipelineStore`), `intentcfgtest` y `casebanktest` (con **doble nuevo**: `casebank` no
  tiene gemelo).
- Pendientes en `internal/modulos/captacion` → **0**; SKIP → **0**; cobertura ≥ 80 % por fichero salvo
  `postgres.go`/`machine_postgres.go`/`store_postgres.go` (E-6).
- Puentes en `fronteras_test.go`: `captacion/stages → internal/flujos/store`,
  `captacion/reanalisis → internal/flujos/events`, `captacion/reanalisis → internal/flujos/runtime`
  (los tres «muere F8»). Adaptadores `internal/arranque/puente_captacion.go` (muere F8).
- `cmd/server-modular` enlaza `modulos/captacion/**`; H1, E1, E2 por la cara nueva (`FaseActual = 7`);
  G7 lee el plazo del `pipeline` **nuevo**; huella igual; candados I-CP-4 (W=1) e INV-1 en verde con
  sus rutas nuevas.
- Si D-F9-1 está aceptada: T9.28 (9C `captacion`) y los procesos P4 (mensaje a borrador) y P8
  (re-análisis) cerrados por la sesión local contra los dos binarios.

## Orden de lectura

[`requisitos.md`](requisitos.md) → [`arquitectura.md`](arquitectura.md) (ciclo 2, puentes, costuras,
estado en memoria) → [`diseno.md`](diseno.md) → [`reglas.md`](reglas.md) → [`tareas.md`](tareas.md).

## Bloques de sesión

| Bloque | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| **A** · inventario + hojas en rojo | 🌐 | T7.1–T7.6 | `evidence`, `intake`, `anclaje`, `intentcfg`, `casebank` en rojo con suites · PR |
| **B** · rojo de `stages` | 🌐 | T7.7–T7.9 | 10 ficheros de `stages` en rojo; puente a `flujos/store` declarado · PR |
| **C** · rojo de `pipeline`, `intakeahead`, `reanalisis` | 🌐 | T7.10–T7.13 | todo el módulo en rojo; 3 puentes; `make test-pendiente` anotado · PR |
| **D** · verde de las hojas | 🌐 | T7.14–T7.15 | 5 paquetes hoja en verde · PR |
| **E** · verde de `stages` | 🌐 | T7.16–T7.17 | `stages` en verde · PR |
| **F** · verde de `pipeline`, `intakeahead`, `reanalisis` | 🌐 | T7.18–T7.20 | pendientes del módulo = 0 · PR |
| **G** · cara HTTP (TX.19–TX.20 + intenciones) | 🌐 | T7.21–T7.22 | `reanalyze.go`, `intents.go` nuevos en verde · PR |
| **H** · conmutar (incl. TX.21) | 🌐 | T7.23–T7.26 | huella igual · cableado por tipo · INV-1 re-tocado · traspaso · PR |
| **I** · cierre local | 💻 | T7.27–T7.29 | P4 y P8 (y 9C si D-F9-1) contra los dos binarios · `dev` · `ESTADO.md` |

## Contradicciones encontradas (medidas contra el código)

1. **`05` E-6** lista `intentcfg` entre los 12 paquetes **sin** gemelo en memoria: **falso**.
   `internal/intentcfg/store.go:47` declara `MemoryStore` (con `sync.Mutex` `:48` y `time.Now()` `:76`).
   `casebank` **sí** carece de él (medido: `grep -n '^type' internal/casebank/*.go` → `Caso`, `Store`,
   `Servicio`, `Clase`, `Hallazgo`, `Anonimizador`, `Postgres`). Quedan **11** sin gemelo, no 12.
2. **El agregador de ventanas y el compositor del literal no son de captación**: viven en
   `internal/flujos/runtime/{aggregator.go,source_composer.go}` (F8), y también el `WebhookSink`.
   «La ráfaga y la ventana» de la tarea de F7 son **conversación**; captación solo aporta la cola
   (`intake.JobStore`), el adelanto (`intakeahead`) y el consumidor (`pipeline`). La goroutine
   `c.intakeAggregator.Run` (`fase9_fondo.go:75`) **no** se conmuta en F7.
3. **`02` §2** dice `intake → evidence, flujos, intakes, platform` a nivel de carpeta; el paquete raíz
   `internal/intake` no importa **nada** interno (`go list` → vacío): es una **hoja**. Las aristas son
   de sus subpaquetes (`pipeline`, `stages`).
4. **`casebank` no lo cablea el servidor**: su único importador de producción es `cmd/casebank`
   (`go list -f '{{.ImportPath}} {{.Imports}}' ./... | grep internal/casebank`). La huella del arranque
   es **ciega** a él y «conmutar» no aplica: D-F7-2.
5. **`05` §4.1** prevé puentes **nuevo → viejo**; F7 necesita además **viejo → nuevo**: el agregador
   viejo llama `AheadRequester.Request(intake.WindowKey, string)` (`aggregator.go:320-322`) y el
   compositor viejo exige `SourceTextWriter.PutSourceText(ctx, intake.WindowKey, intake.SourceText)`
   (`source_composer.go:277-279`), con tipos del `intake` **viejo**. Se resuelve en el arranque
   ([`arquitectura.md`](arquitectura.md) §4).
6. **FX `tareas.md` TX.21** está escrita para D-FX-1 **literal** («retirar el puente de intenciones»).
   Con D-FX-1 en su **alternativa** (las intenciones se mudan en F7, indicación del orquestador) TX.21
   **muda** E1–E2 en vez de retirar un puente, y F3 no crea ningún puente `apipublica → intentcfg`.
   A reconciliar en FX.
7. **`04` §2.3 (vieja)** decía que `inv1_aprobar_ast_test` «lee sus seis directorios» en F7: con
   `05` es el candado **nuevo** de `solicitudes/intakes` (F6) el que se re-toca aquí (T7.25).

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F7-1 | Costura con el agregador y el compositor viejos (F8): ¿segunda instancia **vieja** de `intake.Postgres` (sin estado) para ellos + adaptador de tipos para `Request`/`OnClassified`/`ComposeAtFlush`, o adaptar también la cola? | **Segunda instancia vieja** para `JobStore`/`SourceTextWriter` (solo `*sql.DB`, `grep -n 'sync\.' internal/intake/postgres.go` vacío) + **`puente_captacion.go`** con las tres conversiones de `WindowKey` (struct idéntico de 4 `string`, `store.go:60-65`: conversión directa `intakeviejo.WindowKey(k)`). Todo muere en F8 |
| D-F7-2 | `cmd/casebank` (CLI que siembra `intake_case_bank`) importa `internal/casebank`: ¿cambia a `modulos/captacion/casebank` en F7 o en F10? | **F10**, con `cmd/server`: `cmd/` no se toca durante la transición y el operador sigue usando el código de UAT. En F7 el paquete nuevo se prueba por su suite y su doble |
| D-F7-3 | `pipeline/memoria.go` (414 l, `sync.Mutex` `:86`, construye `cart.Article`/`catalogo.Construir` `:371-380`) está entre los ficheros de producción: ¿es producción o un doble para los guiones de test? | Verificar en T7.1 (`grep -rn 'NuevaMemoria\|memoria\.' internal --include='*.go' \| grep -v _test`): si solo lo usan tests, **pasa a `pipelinetest/`** (E-3, dobles); si no, se reconstruye como producción |
| D-F7-4 | Las rutas de intenciones E1–E2 | **F7** (D-FX-1 alternativa, confirmada por el orquestador): la cara vieja las sirve hasta aquí con el gw nuevo inyectado por `publicapi.ConfigPusher` (estructural) |
