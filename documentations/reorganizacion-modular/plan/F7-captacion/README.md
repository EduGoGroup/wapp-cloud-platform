# F7 · `captacion` — la cola que convierte una conversación en borrador (P2→P4, match, draft)

> **Estado: en curso** — arrancada el 2026-10-08 (F7-01) sobre `dev` @ `8d875ab`; inventario E-12 **aprobado** por Jhoan
> ese día ([`diseno.md`](diseno.md) §1.2). **F7-01 hecha** (bloque A): `evidence`, `anclaje`, `intake`, `intentcfg` y `casebank` en verde
> (19 ficheros de producción y dobles, 5 suites en memoria; `PENDIENTES=0`, `ROJOS=0`). **F7-02 hecha** (bloque B): `stages` en verde (14 ficheros de producción, hallazgo 18) y el puente `captacion/stages → internal/flujos/store` declarado. Falta `pipeline`, `intakeahead` y `reanalisis` (F7-03). Spec escrita el 2026-09-28 sobre `dev` @ `1b18932`. Norma:
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
  tiene gemelo). ✎ F7-01: son **cinco** (se añadió `intakehelpertest.ContratoReanalysis`, hallazgo 2). Las suites son `Contrato…(t, func(t) Montaje)` y corren **en memoria y en Postgres** (P4).
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
   ✎ **F7-02**: mirado por lectura de código antes de portar `p2.go`: la carrera existe y **no pasa por `stages`** (hallazgo 17).

9. **`intake.MemoryStore` no implementa `PipelineStore`** (F7-01, T7.1), contra `diseno.md` §2.2 y §3 («corre las dos
   suites»): es el gemelo de `JobStore` y nada más. El viejo lo declara a propósito (`internal/intake/machine.go:378`:
   «NO hay doble en memoria de PipelineStore, y es una decisión»: los guards viven en SQL) y, aun así, el doble existe:
   `StoreEnMemoria` de `pipeline/memoria.go:85`, con aserción de compilación (`:332`). Resuelto por **D-F7-5**.
10. **`diseno.md` §2.4 decía que `draft.go` no se parte**; `05` E-13 (2026-10-04) es posterior y estricta por encima de
    600 líneas. Afecta a `draft.go` (1.038), `pipeline.go` (1.148) y `reanalisis.go` (772). Resuelto por **D-F7-6**.
11. **`Despertar` no es de `machine_postgres.go`** (contra `diseno.md` §2.2): es `(*Worker).Despertar`
    (`pipeline.go:402`), un canal en memoria. `PipelineStore` tiene 7 métodos y el único instante que le llega de Go es
    el `next` de `Retry`; lo demás es `now()` de SQL.
12. **`Plazas` es estructural y ya está resuelta**: el `llmvia.Selector` nuevo (F4) la satisface sin importar `pipeline`,
    y el arranque nuevo ya lo entrega: no hace falta adaptador.

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F7-1 | Costura con el agregador y el compositor viejos (F8): ¿segunda instancia **vieja** de `intake.Postgres` (sin estado) para ellos + adaptador de tipos para `Request`/`OnClassified`/`ComposeAtFlush`, o adaptar también la cola? | **Segunda instancia vieja** para `JobStore`/`SourceTextWriter` (solo `*sql.DB`, `grep -n 'sync\.' internal/intake/postgres.go` vacío) + **`bridge_captacion.go`** con las tres conversiones de `WindowKey` (struct idéntico de 4 `string`, `store.go:60-65`: conversión directa `intakeviejo.WindowKey(k)`). Todo muere en F8 |
| D-F7-2 | `cmd/casebank` (CLI que siembra `intake_case_bank`) importa `internal/casebank`: ¿cambia a `modulos/captacion/casebank` en F7 o en F10? | **F10**, con `cmd/server`: `cmd/` no se toca durante la transición y el operador sigue usando el código de UAT. En F7 el paquete nuevo se prueba por su suite y su doble |
| D-F7-3 ✅ | `pipeline/memoria.go` (414 l, `sync.Mutex` `:86`, construye `cart.Article`/`catalogo.Construir` `:371-380`) está entre los ficheros de producción: ¿es producción o un doble para los guiones de test? | Verificar en T7.1 (`grep -rn 'NuevaMemoria\|memoria\.' internal --include='*.go' \| grep -v _test`): si solo lo usan tests, **pasa a `pipelinehelpertest/`** (E-3, dobles); si no, se reconstruye como producción. **Verificado en T7.1 (2026-10-08): es un doble** — cero usos fuera de `_test.go` en `internal/`, `cmd/` y `test/`; F7 queda en 31 ficheros de producción |
| D-F7-4 | Las rutas de intenciones E1–E2 | **F7** (D-FX-1 alternativa, confirmada por el orquestador): la cara vieja las sirve hasta aquí con el gw nuevo inyectado por `publicapi.ConfigPusher` (estructural) |
| D-F7-5 | *(F7-01, contradicción 9)* ¿Quién corre `ContratoMaquina` en memoria, si `MemoryStore` no implementa `PipelineStore`? | **Decidida (Jhoan, 2026-10-08)**: el doble de `PipelineStore` se porta de `pipeline/memoria.go` (`StoreEnMemoria`) a `intake/intakehelpertest/`; `CatalogoEnMemoria` va a `pipelinehelpertest/` en F7-03. La verdad de los guards SQL sigue siendo la suite contra Postgres (F7-05) |
| D-F7-6 | *(F7-01, contradicción 10)* ¿Manda E-13 sobre el «`draft.go` no se parte» de esta spec? | **Decidida (Jhoan, 2026-10-08)**: manda E-13; `draft.go`, `pipeline.go` y `reanalisis.go` se parten por tema al reconstruirlos |
| D-F7-8 | *(F7-01, hallazgos 1 y 7)* ¿Se cierran en el código nuevo los tres agujeros de PII del anonimizador y la clave de ventana incompleta que el gemelo aceptaba? | **Decidida (Jhoan, 2026-10-08)**: sí, los cuatro (mejora clara; divergencia deliberada del viejo), un commit por decisión |

## Hallazgos de la ejecución

*(Se numeran aparte de las contradicciones de la spec.)*

1. ✅ **El anonimizador de `casebank` dejaba pasar PII en tres casos que el viejo no declara** (F7-01; **cerrados en el
   código nuevo por decisión de Jhoan, 2026-10-08, D-F7-8**: divergencia deliberada del viejo). Medido ejecutando el viejo:
   (a) dos JID pegados (`584121234567@s.whatsapp.net584121234567@s.whatsapp.net`) dejaban el segundo número en claro,
   contra la cabecera de `internal/casebank/anonimizar.go:84-86`; (b) dos teléfonos seguidos separados solo por un espacio
   o un salto se fundían en una racha de más de 15 dígitos y pasaban enteros; (c) los dígitos árabes-índicos (U+0660–0669,
   U+06F0–06F9) y de ancho completo (U+FF10–FF19) pasaban enteros. Arreglos: `1d36dfe` (a), `f08e959` (b), `d88d200` (c),
   `cb41b42` (dos mutantes más). **Criterio de (b)** (`splitPhoneRun`, `anonymize_phones.go`): una racha de más de 15
   dígitos se corta por sus separadores y un teléfono es uno o varios trozos consecutivos que suman de 8 a 15; gana el
   reparto que deja menos dígitos en claro (no es voraz); más de 15 dígitos **sin** separador pasan como antes. Los
   esperados del corpus que cambian llevan «DIVERGE DEL VIEJO». ⚠️ **Sin efecto en UAT hasta F10**: `cmd/casebank` sigue
   usando el paquete viejo (D-F7-2). Lo que queda abierto, en el hallazgo 15.
2. **Las suites son cinco, no cuatro**: nació `intakehelpertest.ContratoReanalysis` (10 casos) para `LiveJobOfEvent` y
   `OpenReanalysis`, que en el viejo solo tenían tests del texto del SQL y no pertenecen a `JobStore` ni a
   `PipelineStore` (hallazgo 63 de F6: un caso por método con BD). Su puerto es `intakehelpertest.ReanalysisStore` y su
   doble, `reanalysis_memory.go` sobre `MachineMemory` (nuevo, sin fichero viejo): F7-03 puede usarlo para `reanalisis`.
   **F7-05 corre cinco suites contra Postgres** (`ContratoQueue` 14 casos, `ContratoMachine` 29, `ContratoReanalysis` 10,
   `intentcfg` 9, `casebank` 12).
3. **`intake/reanalisis.go` se partió en dos**: `reanalysis.go` (tipos) y `postgres_reanalysis.go` (los dos métodos SQL
   de `*Postgres`), siguiendo D-F6-6. Es convención, no candado: nada en `internal/candados/` exige que el SQL viva en
   `*postgres*`. El módulo pasa a 7 ficheros de producción en `intake`.
4. **El orden del verde de `intake` no fue el de T7.15** (`memory.go` primero): `store`, `machine` y `reanalysis` van
   antes, porque los dobles llaman a `SourceText.Complete`, `Artifact.Validate`/`StageIndex` y
   `ReanalysisRequest.Valid`; con esos en rojo el paquete no pasaba sin la etiqueta.
5. **`MachineMemory` no es una copia de `StoreEnMemoria`** (`pipeline/memoria.go:163-318`): para que `ContratoMachine`
   valga igual en Postgres guarda y mueve `updated_at`, el reclamo devuelve `Reanalysis` (el viejo lo dejaba a cero,
   `:228`), una transición con `jobID` vacío da error como el adaptador (el viejo, `(false, nil)`) y `View` devuelve
   copias profundas. Conserva el texto corto del viejo en `Fail` sin causa. F7-03, al portar los guiones de `pipeline`,
   tiene que contar con estas cuatro diferencias.
6. **`MemoryStore.Jobs` ya no ordena por `ID`** (`internal/intake/memory.go:152`): el viejo ordenaba como cadena y con
   10 filas o más `job-10` salía antes que `job-2`, contra su propio comentario. Es una ayuda de observación para tests,
   no conducta de producción: mejora clara, se hizo.
7. ✅ **Divergencia heredada, corregida: la clave de ventana incompleta** (`1db9266`, decisión de Jhoan, 2026-10-08,
   D-F7-8). El `MemoryStore` viejo la aceptaba (abría la ventana) y `Postgres` la rechaza (`postgres.go:73,120,174`). El
   gemelo nuevo la rechaza en `OpenOrAppend`, `CloseWindow` y `PutSourceText` con los textos literales del adaptador, y
   `ContratoQueue` lo afirma (de 14 a **17** casos; es la primera vez que la suite fija un texto de error). Orden en el
   gemelo: clave → fallo inyectado → sobre; la llamada rechazada cuenta en `Counters`. Queda una divergencia menor, sin
   tocar: con el sobre incompleto los dos dan error, con textos distintos (la suite solo exige error). Para F7-05: el
   `Montaje` de Postgres tiene que aceptar `Rows(t, "")` y devolver 0 filas. Para F8: los tests del agregador que usen
   claves a medias fallarán contra el gemelo nuevo, y es lo correcto.
8. **`intentcfg`: memoria y Postgres divergen en el blob, y la suite lo absorbe.** Memoria devuelve los bytes exactos;
   Postgres guarda JSONB y lee `config::text`, que reordena claves. La suite compara por equivalencia JSON (como el test de
   integración viejo). Un blob que no sea JSON, memoria lo acepta y Postgres lo rechazaría: queda fuera de la suite,
   documentado en el contrato de `Upsert` (validar es del llamante). `UpdatedAt` se refresca **siempre**, también
   reescribiendo lo mismo (al revés que `tenantvars`): fijado como promesa.
9. **`casebank`: el consentimiento lo rechazan los dos, con errores distintos.** El `Service` devuelve el centinela sin
   llamar al store; el store pasa `consented` como parámetro a propósito y lo rechaza el CHECK
   `intake_case_bank_consented_check`. La suite exige al store error, id 0, nada escrito y el nombre de la constraint
   en el texto, **no** `ErrNoConsent`; `Memory` lo reproduce. El caso `Insert_InvalidExpectedJSON_…` no tiene respaldo
   en el viejo (se da por cierto por ser JSONB): si en F7-05 diverge, se quita el caso y la validación del doble.
10. **Lo que F7-05 necesita para montar las suites en Postgres** (dicho por quienes las escribieron): la tabla
    `intake_jobs` **vacía por caso** (`ClaimNext` y `ListAggregating` no filtran por tenant; `validateMachineMontaje` lo
    comprueba); `EventID` e `IntakeID` son UUID; `Seed` inserta la fila tal cual (los ceros son NULL); `Now` es el
    `now()` de la base; `casebankhelpertest.Montaje.Rows` es un `SELECT id, tenant_id, consented, source_text, expected
    FROM public.intake_case_bank WHERE tenant_id=$1 ORDER BY id`; el `Advance` de `intentcfg` espera a que `now()` pase
    del microsegundo, como el de `tenantvars`. Fuera de las suites, para los procesos: `SKIP LOCKED` en carrera, un job
    `failed` que no impide abrir el siguiente, y el default de `consented` (`TestElDefaultDeConsentedRECHAZAALDescuidado`).
11. 🟡 **Hallazgo 63 de F6, repetido aquí como se esperaba**: de 115 mutantes sobre `intake` (114 muertos, 1 equivalente:
    el `if messageTS.Valid` de `scanClaim`), los de las guardas SQL —`status =`, `next_attempt_at <= now()`,
    `tenant_id = $1`, `array_position`, el vaciado de las tres columnas, el predicado del `ON CONFLICT`, el `IS NULL` del
    sobre— **solo los mata el test del texto de la sentencia**. En memoria no puede ser de otro modo; F7-05 los repite
    contra Postgres con las tres suites. `anclaje`: 22 mutantes, 2 vivos tras el rojo, 0 tras añadir sus dos tests.
    `casebank`: 6, 0 vivos.
12. **`anclaje` no consulta el reloj**: el `time.Now()` que contó el inventario es un comentario
    (`internal/intake/anclaje/anclaje.go:41`). Reglas que el test viejo no fijaba y ahora tienen caso: la ventana es `>`
    estricto (5 min exactos siguen dentro), un turno sin texto fuera de ventana también corta, una mención ambigua cae a
    proximidad, el mínimo de 4 se cuenta en runas. Los 115 casos del rojo pasaron también contra el paquete viejo.
13. **El lint encontró 14 avisos en los tests al cerrar** (los sub-agentes no corren `make lint`, que es del árbol
    entero): corregidos en `9bbc2dd` sin tocar producción. Coste: una pasada extra de gates. Para F7-02: que cada
    sub-agente corra el lint **solo de su paquete** antes de devolver.
14. **Un `\uFEFF` escrito con un heredoc llega al disco como el carácter literal** y no compila («illegal byte order
    mark»); durante un par de minutos rompió el parseo de los candados de `./internal/modulos/` para los otros
    sub-agentes, que comparten árbol. Los invisibles del corpus adversario se escriben escapados con un script.
15. 🟡 **Lo que el anonimizador nuevo sigue sin cerrar, y lo que ahora redacta de más** (F7-01, tras D-F7-8; para Jhoan,
    sin bloquear). (i) **Una excepción viva al invariante «`Remains(Anonymize(x))` vacío»**, heredada del viejo:
    `José.maria@lid` → `[NOMBRE].maria@lid`, y `Remains` delata el JID `.maria@lid` (la marca del nombre abre un límite de
    palabra que antes tapaba la «é»); con un JID de grupo de 18 dígitos el número saldría en claro. Declarada en la
    cabecera, única excepción enumerada en `TestAnonymize_ThenRemains_EmptyOverTheWholeCorpus` y fijada en
    `TestAnonymize_JIDAfterAccentedName_LeavesAJIDBehind`; cerrarla pide repetir las pasadas hasta punto fijo o tocar el
    límite izquierdo del JID. (ii) **Falsos positivos nuevos**, declarados: toda racha **con separadores** de más de 15
    dígitos se redacta como varios teléfonos (una tarjeta `4111 1111 1111 1111`, una cuenta en grupos), y
    `user@lidia584@lid` sale `[JID][JID]`. Para un banco de casos, redactar de más es el lado seguro. (iii) Fuera de
    alcance, fijado: devanagari y bengalí, y los separadores Unicode que el viejo ya declaraba.
16. **El lint por paquete necesita la toolchain fijada**: `.bin/golangci-lint run ./…/<paquete>/...` a pelo da rc=1 por
    `typecheck` contra la stdlib de go1.27.1; con `GOTOOLCHAIN=go1.26.5` delante, `0 issues.`. La skill
    `reconstruir-modulo` lo lleva así desde esta sesión.
17. 🟡 **Contradicción 8, mirada antes de portar `p2.go` (F7-02): la carrera existe por lectura de código y no pasa por
    `stages`.** No se reprodujo bajo carga; se leyó el código viejo. El agregador hace `CloseWindow` (`aggregator.go:892`;
    `postgres.go:108-123`, autocommit: el job ya es `pending` y reclamable) y **después** `ComposeAtFlush`
    (`aggregator.go:906`), que lee el hilo, compone, cifra y solo entonces llama a `PutSourceText`
    (`source_composer.go:347-377`). El reclamo no exige sobre (`machine_postgres.go:81-99` y `:121-139`: solo `pending`).
    Si un tic del worker (5 s) o un flanco READY cae en ese hueco, `literalDe` devuelve `ErrSinLiteral`
    (`pipeline.go:713-714`: **el texto observado lo emite el worker, no P2**; las guardas de `p2.go:149`, `p3.go:251` y
    `p4.go:171` no se alcanzan por esta vía) y `causaDe` lo da por permanente (`backoff.go:91`): `failed` sin reintento.
    `stages` se portó tal cual. **Firmas de log para distinguirla** al reproducir: carrera → solo el `Debug` «la ventana
    ya tenía literal; no se sobrescribe» (`source_composer.go:381-387`), sin `Warn` ni `Error`; hilo vacío → `Warn` «la
    ventana cerró sin una sola línea del hilo»; fallo al componer → `Error` «la ventana se cerró pero el literal no se pudo
    componer». **Reparto**: la causa es de `flujos/runtime` (F8: cierre y sobre no son un solo acto); la defensa, de
    `pipeline` y del reclamo (**F7-03**: hoy «sobre aún no escrito» y «sobre que nunca llegará» son lo mismo). Un
    `IS NOT NULL` a secas en el reclamo no vale: el sobre NULL es una forma legítima y definitiva (ventana solo de media,
    hilo apagado). **Para Jhoan, antes de F7-03**: ¿se porta la conducta tal cual (manda el viejo) o el worker nuevo
    defiende? Sin decisión, F7-03 porta tal cual y lo deja dicho en el contrato.
18. **`stages` son 14 ficheros de producción, no 10** (F7-02): `draft.go` en cuatro por tema (D-F7-6: `draft.go` 405 l,
    `draft_revision.go` 336, `draft_events.go` 236, `draft_push.go` 98) y `match_cascade.go` en dos (el barrido y su
    prefiltro en `match_cascade_sweep.go`, sin exportados, porque pasaba de 500). El mayor es `match.go`, 484; el mayor
    test, `p3_test.go`, 495 (sin margen). **`tope.go` no pudo ser nivel simple** (contra `diseno.md` §1.2): su lógica son
    métodos de `P3` y su promesa solo se ve por `P3.Run`; fue en rojo y pasó a verde con `p3.go`. `plazo.go` sí
    (`deadline.go`, una pasada).
19. **Tres commits de verde llevan más de un fichero, y uno intermedio no pasa el lint** (F7-02). `p3.go` con `cap.go`
    (`0177f3d`), `match.go` con `match_lines.go` (`705bda7`) y los cuatro de `draft` (`efe219d`): no compilan o no tienen
    llamante por separado; el cuerpo de cada commit lo explica. `c880cd5` (`match_cascade.go`) compila y pasa los tests,
    pero su lint da 13 `unused` (los auxiliares de la cascada no tienen llamante hasta `Run`, que llega en `705bda7`); en
    HEAD, `0 issues.`. Para F7-03: en ficheros que se necesitan mutuamente, un commit con los dos antes que uno a medias.
20. **Los tres tests con `go/ast` de `stages` no eran candados** (🔶 de `diseno.md` §6, resuelto): `TestStages_NoLeenElReloj`
    (`p4_test.go:368`) y `TestDraft_ElRelojSoloEntraPorElConstructor` (`draft_test.go:365`) no están en `05` §3.2 y
    protegen testabilidad, no una regla de negocio. Sustituidos por conducta: fechas contra un `message_ts` fijo, dos
    pasadas con artefacto idéntico byte a byte, `elapsed_ms` exacto con reloj inyectado. **Se pierde** la mitad que miraba
    `p2.go`/`p3.go` (ahí el reloj no tiene conducta observable) y `assertFixtureLejosDeHoy`, que leía el reloj real.
21. **R-03 («match y draft con plazo no compilan») no tenía ningún test en el viejo**: vivía en un comentario
    (`match.go:321`). Un test que compile no puede probar que algo no compila: quedó fijado con `reflect` (los tipos
    `Option`, `MatchOption` y `DraftOption` no son asignables ni convertibles entre sí). **I-CP-1** tampoco tenía test en
    `stages` (vive en `inferencia/prompts`): aquí queda la promesa vista desde la etapa (lo que P4 persiste pasa su propio
    validador por todos los caminos).
22. 🟡 **Conductas raras del viejo, conservadas y fijadas en los corpus** (F7-02; manda el viejo; para Jhoan, sin bloquear;
    ninguna tiene efecto nuevo: es lo que corre en UAT). **Match**: (a) la negación de un añadido solo se ve si el texto
    normalizado empieza por `"sin "` (`match_lineas.go:324`): «sin-sal», «sin,,sal» o «sin sal» con un espacio de ancho
    cero o un BOM delante **cobran «Sal»**; «nada de sal» y «queso sin sal» también salen como línea; (b) un sku mal
    escrito («pack-30», o con guion Unicode) puede casar por fuzzy la etiqueta de **otro** artículo (0,857); (c) «1.5 kg»
    se lee como los enteros 1 y 5 (`numerosDe`, `:277`): un rango 5–5 resuelve esa variante; (d) dígitos de otra escritura
    e invisibles cuentan como una edición y casan en etiquetas largas. **Fechas** (`fechas.go`): una regla que reconoce su
    patrón y no puede fechar no corta («el miércoles 31 de febrero» da el miércoles próximo); un año de 3 cifras se toma
    literal; «29/02» sin año no busca el bisiesto; marcas y días se buscan como subcadena («en el hoyo» da hoy). **Draft**:
    la quinta clave de la métrica entra con `RequestedBy != ""` y todo lo demás usa `IsFromOwner()` (`draft.go:852`); dos
    o más adjuntos huérfanos suben a la cabecera en el orden de iteración de un mapa (`:742-750`); si `SaveStage` falla,
    solicitud, revisión, empuje y eventos ya están escritos y el reintento escribe otra revisión (`:594-596`).
23. **El test viejo de rendimiento del match no medía el barrido** (`match_rendimiento_test.go:201`): con sus sondas el
    prefiltro lo descartaba todo y el comparador se llamaba 0 veces, así que el «~110x» y el p99 eran los de un bucle sin
    distancias de edición: **D-044.44 no está acreditado por ese test**. El nuevo cuenta comparaciones con un espía (2.000
    artículos × 10 ítems con sondas que casi casan: como mucho 2.000 de 20.000; medido contra el viejo, 888). El absoluto
    de 5 ms y `WAPP_PERF_ABSOLUTO` no se portaron (medida de hardware). Tampoco es portable
    `TestMatch_ElSKUDeVarianteEsElMISMOQueElDelCart` (compara con `cart.PriceListOf`, viejo): el `#` y el ` — ` quedan
    fijados con literales y duplicados; F8, al reconstruir el carrito, es quien los ata.
24. **Cosas menores de `stages`, para quien venga detrás** (F7-02). (i) El texto de `ErrNoTimeZone` sigue nombrando
    `stages.ZonaPorDefecto`, que hoy es `DefaultZone`: es texto observable y se copió literal. (ii)
    `stages.RevisionWriter` duplica la firma de `intakes.RevisionWriter` (F6): el viejo lo declaraba del lado del
    consumidor y así quedó; un test afirma que el de `intakes` lo satisface. (iii) `diseno.md` §2.4 atribuye
    `VerificarNormalizador` a `match_cascada.go`: vive en `catalogo/indice` y lo llama el arranque; en `stages` queda la
    regla R-05 y un test de que el barrido usa `textmatch.Normalize`. (iv) Tres tests de `draft` importan
    `internal/flujos/store` por los tipos de los puertos; los cubre el mismo puente y se re-tocan en F8. (v) El e2e que
    corría el `match` real dentro de los tests de `draft` queda para el proceso P4 de F9 (T-10). (vi) Tres bloques de
    comentario histórico no se portaron por caducados (el «plazo que esta etapa no fija» de `p3.go:16-62`, las cuentas de
    `tope.go` y la búsqueda de zona en `tenant_settings` de `p4.go`); ninguna regla de código quedó fuera. (vii) Un
    backslash-u escrito en los argumentos de **cualquier** herramienta llega al disco como el carácter literal (amplía el
    hallazgo 14): los corpus se generaron ejecutando el paquete viejo y se tocan solo por script.
25. **Lo que F7-03 necesita de `stages`**: `NewDraft(log, StageStore, IntakeStore, RevisionWriter, EventWriter,
    …DraftOption)`; `IntakeStore` y `EventWriter` los satisface el almacén viejo de `flujos/store` (puente), `RevisionWriter`
    **solo** el de `solicitudes/intakes` (R-06: el tipo no lo impide si alguien añade `InsertRevision` al otro), y
    `WithCRMPush` se cablea con `CRMPusherFunc` resuelta al llamar, porque `Draft` se construye antes que el `Service`
    (R-07). `Run` de `draft` no es idempotente en la revisión: lo evita el worker saltándose la etapa al reanudar.
    `Media.ByLine` nil es válido (D-6). `Analysis.Provider` lo rellena el worker con la vía.
