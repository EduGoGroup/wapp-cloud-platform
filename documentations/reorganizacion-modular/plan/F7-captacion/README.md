# F7 · `captacion` — la cola que convierte una conversación en borrador (P2→P4, match, draft)

> **Estado: por empezar** (spec escrita el 2026-09-28 sobre `dev` @ `1b18932`). Norma:
> [`05`](../../05-metodo-contratos-y-tdd.md). Forma: [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).
> Rutas: **autoridad** [`FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) §2.5 y §2.8
> (H1, y E1–E2 por **D-FX-1** resuelta en su alternativa: las intenciones se mudan **aquí**).
>
> ✎ **D-F1-10 (Jhoan, 2026-10-02)**: los paquetes de suite de contrato y de dobles llevan el sufijo compuesto
> **`helpertest`**, el único que los candados de fichero eximen ([`DECISIONES.md`](../DECISIONES.md) §2). Esta spec los
> nombraba con `…test` (`intaketest`, `casebanktest`, `intentcfgtest`, `pipelinetest`): se actualizó el sufijo, nada más.
>
> Recalibrado el 2026-10-03 tras la parada de F1 (`05` E-12, §4.2, E-9, E-4; `plan/DECISIONES.md` §3).

## Objetivo, en tres líneas

1. Reconstruir, con el nivel de ceremonia que fije el inventario E-12 (T7.1), los **32 ficheros de producción** (11.144 líneas) de
   `internal/intake` (la cola `intake_jobs`), `intake/{pipeline,stages,anclaje}`, `internal/{intakeahead,
   evidence,reanalisis,casebank,intentcfg}` en `internal/modulos/captacion/…` (aplanado, `04` §4).
2. Conmutar: `cmd/server-modular` cablea el worker del pipeline (W=1), el aforo (K=1), las cinco
   etapas, el adelanto por pull y la puerta del re-análisis **nuevos**; la cara nueva sirve H1
   (`/reanalyze`) y E1–E2 (`/intents`); huella idéntica; `cmd/server` no cambia.
3. Congelar el **ciclo 2** de `02` §4 (`conversacion · captacion · catalogo · solicitudes`) con
   **dos o tres puentes declarados** a la conversación vieja (el tercero, `reanalisis → flujos/runtime`,
   se evita si T7.12 pasa `DefaultThreadLimit` por parámetro: recomendado) y **un adaptador de arranque**,
   `internal/arranque/bridge_captacion.go` (`05` §4.2), hacia el agregador y el compositor viejos. Todo muere en F8 (D-7).

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
  con **32** ficheros en verde, cada uno con su `x_test.go`; suites `intakehelpertest` (dos puertos:
  `JobStore` y `PipelineStore`), `intentcfghelpertest` y `casebankhelpertest` (con **doble nuevo**: `casebank` no
  tiene gemelo). Las cuatro suites son `Contrato…(t, func(t) Montaje)` y corren **en memoria y en Postgres** (P4).
- Pendientes en `internal/modulos/captacion` → **0**; SKIP → **0**; sin umbral de cobertura (P2): un test por promesa
  del contrato; mutantes en el nivel complejo; procesos de F9. La verdad de `postgres.go`/`machine_postgres.go`/
  `store_postgres.go` la da la suite contra Postgres (P4) y F9.
- Puentes en `fronteras_test.go`: `captacion/stages → internal/flujos/store`,
  `captacion/reanalisis → internal/flujos/events` y, solo si T7.12 no lo evita,
  `captacion/reanalisis → internal/flujos/runtime` (todos «muere F8»; tabla única en
  [`../00-marco/estructura.md`](../00-marco/estructura.md) §2.1). Adaptador `internal/arranque/bridge_captacion.go` con su
  test de cableado (muere F8); el adaptador `llmConfigBridge` de `bridge_inferencia.go` (F4) **muere aquí**.
- `Conmutados` (`fronteras_test.go`): `captacion` **no** entra en F7; entra en F8, cuando muere `bridge_captacion.go`.
- `cmd/server-modular` enlaza `modulos/captacion/**`; H1, E1, E2 por la cara nueva (`FaseActual = 7`);
  G7 lee el plazo del `pipeline` **nuevo**; huella igual; candados I-CP-4 (W=1) e INV-1 en verde con
  sus rutas nuevas.
- Si D-F9-1 está aceptada: T9.28 (9C `captacion`) y los procesos P4 (mensaje a borrador) y P8
  (re-análisis) cerrados por la sesión local contra los dos binarios.

## Orden de lectura

[`requisitos.md`](requisitos.md) → [`arquitectura.md`](arquitectura.md) (ciclo 2, puentes, costuras,
estado en memoria) → [`diseno.md`](diseno.md) → [`reglas.md`](reglas.md) → [`tareas.md`](tareas.md).

## Bloques de sesión

| Bloque = sesión | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| **A** · [`F7-01`](../sesiones/F7-01-web-inventario-y-hojas.md) · inventario E-12 + hojas | 🌐❓ | T7.1–T7.6, T7.14–T7.15 | inventario **aprobado por Jhoan** · `evidence`, `intake`, `anclaje`, `intentcfg`, `casebank` en verde con sus suites en memoria |
| **B** · [`F7-02`](../sesiones/F7-02-web-stages.md) · `stages` | 🌐❓ | T7.7–T7.9, T7.16–T7.17 | 10 ficheros de `stages` en verde; puente (import) a `flujos/store` declarado |
| **C** · [`F7-03`](../sesiones/F7-03-web-pipeline-reanalisis.md) · `pipeline`, `intakeahead`, `reanalisis` | 🌐❓ | T7.10–T7.13, T7.18–T7.20 | pendientes del módulo = 0; 2 puentes (import) (3 si T7.12 no evita el de `runtime`) |
| **D** · [`F7-04`](../sesiones/F7-04-web-cara-http-y-conmutar.md) · cara HTTP + conmutar | 🌐❓ | T7.21–T7.26 (= TX.19–TX.21) | `reanalyze.go`, `intents.go` en verde · `bridge_captacion.go` con test de cableado · huella igual · INV-1 re-tocado |
| **E** · [`F7-05`](../sesiones/F7-05-cli-cierre.md) · cierre local | 💻 | T7.27–T7.29 | suites contra Postgres · P4 y P8 (T9.28) contra los dos binarios · `dev` · `ESTADO.md` |

Cada sesión: un bloque de 45–90 min y cierre de tres cosas (tareas `[x]` con SHA, bloque en `ESTADO.md`, hallazgos
aquí). 🌐❓ = web si queda saldo de la promoción; si no, local. Los bloques A–I de antes se reagruparon por paquete
(rojo y verde juntos); `DECISIONES.md` aún dice «F7 bloque H» para D-F7-1: hoy es el bloque **D**.

## Contradicciones encontradas (medidas contra el código)

1. **`05` E-6** lista `intentcfg` entre los 12 paquetes **sin** gemelo en memoria: **falso**.
   `internal/intentcfg/store.go:47` declara `MemoryStore` (con `sync.Mutex` `:48` y `time.Now()` `:76`).
   `casebank` **sí** carece de él (medido: `grep -n '^type' internal/casebank/*.go` → `Caso`, `Store`,
   `Servicio`, `Clase`, `Hallazgo`, `Anonimizador`, `Postgres`). La cifra buena de todo el repo es
   **7 sin gemelo de 22 con SQL** ([`../00-marco/tecnologia.md`](../00-marco/tecnologia.md) §9), no 12 ni 11.
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
6. ~~**FX `tareas.md` TX.21** está escrita para D-FX-1 **literal**~~ — **reconciliado** (2026-09-29):
   FX, F3 y el mapa planifican ya la recomendación (D-FX-1/D-F7-4): las intenciones se mudan **aquí**
   (TX.19–TX.21), F3 no crea ningún puente `apipublica → intentcfg` y la cara vieja las sirve de F3
   a F7 con el gw nuevo inyectado.
7. **`04` §2.3 (vieja)** decía que `inv1_aprobar_ast_test` «lee sus seis directorios» en F7: con
   `05` es el candado **nuevo** de `solicitudes/intakes` (F6) el que se re-toca aquí (T7.25).
8. 🟡 **Heredado del hallazgo 52 de F2 (2026-10-04; decisión de Jhoan: se anota aquí, sin reproducirlo ahora).** El proceso
   `TestP4_WindowRules/segunda_ventana` dio rojo **una vez** contra el binario **viejo**: el job de la segunda ventana
   quedó `failed` y el servidor dejó `pipeline: job FAILED … causa:job_invalido … stages: el job no trae literal que
   analizar (el compositor del flush no llegó a escribir el sobre)` (`internal/intake/stages/p2.go:90`,
   `ErrSinLiteral`). Verde al repetir: una pasada completa contra el viejo y cinco corridas sueltas. Ese día, de cinco
   pasadas completas contra el viejo, dos rojas (ésta y P5, hallazgo 50 de F2); de cuatro contra el nuevo, ninguna; las
   rojas coincidieron con más carga en la máquina. **Sin causa medida.** Qué mirar al reconstruir `stages` y `pipeline`:
   si el worker puede tomar el job **antes** de que el compositor del *flush* haya escrito el sobre (carrera real, que el
   nuevo heredaría al portar la conducta), o si son los plazos del test bajo carga. ⚠️ El compositor **no** es de F7: vive
   en `flujos/runtime` (T-2 de [`reglas.md`](reglas.md)), así que si es una carrera, la mitad del arreglo es de F8. Antes
   de portar `p2.go`, reproducir bajo carga (`TestP4_WindowRules` en bucle con la suite entera en paralelo) guardando el
   log del servidor.

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F7-1 | Costura con el agregador y el compositor viejos (F8): ¿segunda instancia **vieja** de `intake.Postgres` (sin estado) para ellos + adaptador de tipos para `Request`/`OnClassified`/`ComposeAtFlush`, o adaptar también la cola? | **Segunda instancia vieja** para `JobStore`/`SourceTextWriter` (solo `*sql.DB`, `grep -n 'sync\.' internal/intake/postgres.go` vacío) + **`bridge_captacion.go`** con las tres conversiones de `WindowKey` (struct idéntico de 4 `string`, `store.go:60-65`: conversión directa `intakeviejo.WindowKey(k)`). Todo muere en F8 |
| D-F7-2 | `cmd/casebank` (CLI que siembra `intake_case_bank`) importa `internal/casebank`: ¿cambia a `modulos/captacion/casebank` en F7 o en F10? | **F10**, con `cmd/server`: `cmd/` no se toca durante la transición y el operador sigue usando el código de UAT. En F7 el paquete nuevo se prueba por su suite y su doble |
| D-F7-3 | `pipeline/memoria.go` (414 l, `sync.Mutex` `:86`, construye `cart.Article`/`catalogo.Construir` `:371-380`) está entre los ficheros de producción: ¿es producción o un doble para los guiones de test? | Verificar en T7.1 (`grep -rn 'NuevaMemoria\|memoria\.' internal --include='*.go' \| grep -v _test`): si solo lo usan tests, **pasa a `pipelinehelpertest/`** (E-3, dobles); si no, se reconstruye como producción |
| D-F7-4 | Las rutas de intenciones E1–E2 | **F7** (D-FX-1 alternativa, confirmada por el orquestador): la cara vieja las sirve hasta aquí con el gw nuevo inyectado por `publicapi.ConfigPusher` (estructural) |
