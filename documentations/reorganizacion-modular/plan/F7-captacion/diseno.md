# F7 · Diseño — contratos por paquete, puertos, suites, reglas E-8 y candados

> `C` = `internal/modulos/captacion`, `V` = paquete viejo. 🔶 = **por completar en el bloque de
> contratos (lectura E-8 obligatoria)**: la spec no leyó ese test viejo entero; quien escriba el
> contrato lo lee antes y lleva sus reglas al comentario (E-8).

## 1 · Cómo se contó

Igual que F6 `diseno.md` §1 (ficheros, líneas, `^func Test`, exportados con bloques `const (`/`var (`
y métodos de tipos exportados). F7: **32** ficheros, **11.144** líneas, **305** exportados, **≈119**
cuerpos de función/método exportados (cota de `make test-pendiente`, se fija en T7.13).

## 2 · Por paquete

### 2.1 · `C/evidence` (1 · 2 exp.) — primera hoja, y el ejemplo de `05` §10

`Normalize(s)` (minúsculas + colapsar blancos: la **única** normalización) y
`Contains(textoNorm, frase)` (frase vacía tras normalizar ⇒ `false`). El contrato y el test de `05`
§10 valen tal cual; tests viejos `TestNormalize_ColapsaBlancosYBaja`,
`TestContains_LasTresDecisionesDeLaRegla`. Lo usan `anclaje`, `stages` (P2/P3/P4, `fechas.go`) e
`intakeahead/saneo.go`.

### 2.2 · `C/intake` (6 · 56 exp.) — la cola, hoja

| Fichero | L | Exp. | Contrato |
|---|---:|---:|---|
| `store.go` | 207 | 12 | Estados `aggregating`·`pending`·`processing`·`done`·`failed` (`:31-48`); `WindowKey{TenantID, SessionID, ContactID, EventID}` (`:60-65`, `Valid()`: las 4 NOT NULL en la 0072); `Append`, `OpenJob`, `SourceText`; puerto **`JobStore`** (`:163`: `OpenOrAppend`, `CloseWindow`, `ListAggregating`, `PutSourceText`) |
| `machine.go` | 385 | 14 | Etapas `p2`·`p3`·`p4`·`match`·`draft` (`:65-69`); `ClaimedJob`, `Artifact`, `StageIndex`; puerto **`PipelineStore`** (`:280`); reintentos por calidad / por infra 🔶 |
| `reanalisis.go` | 216 | 4 | `SolicitudReanalisis`, `AbrirReanalisis` (segundo productor de jobs), `RequestedByOwner` |
| `memory.go` | 297 | 13 | `MemoryStore` (gemelo, `sync.Mutex` `:79`): **corre las dos suites** |
| `postgres.go` | 260 | 6 | Adaptador de `JobStore` — **sin cipher a propósito** (D-044.26: lo que llega a `PutSourceText` son bytes ya cifrados; `fase3_almacenes.go:171-176`) |
| `machine_postgres.go` | 431 | 7 | Adaptador de `PipelineStore` (reclamo, avance de etapa, castigo con causa, backoff, `Despertar`) 🔶 `FOR UPDATE SKIP LOCKED` y reloj |

Suites `C/intake/intakehelpertest`: `ContratoCola(t, …)` sobre `JobStore` (abrir o anexar a la ventana de
una clave; cerrar devuelve `true` una sola vez; listar solo `aggregating`; `PutSourceText` idempotente
por clave) y `ContratoMaquina(t, …)` sobre `PipelineStore` (reclamar solo `pending`; un job reclamado
no lo reclama otro; terminar/castigar; reintentos). 🔶 los casos exactos de
`V/{machine_test,backoff_integration,despertar_integration,machine_integration,postgres_integration,retry_integration,reanalisis_internal}_test.go`
(39 tests; 34 van a F9: P4, P8).

### 2.3 · `C/anclaje` (1 · 15 exp.)

`Repartir`, `Reparto`, `MediaRef`…: reparto **determinista, sin LLM** de adjuntos a líneas. **Sin
llamante de producción** (deuda D-6: `ThreadEntry` no trae media refs ni instante). Se reconstruye igual
(el hueco es nombrado, `pipeline.go:38`), con sus 19 casos de `anclaje_test.go` 🔶.

### 2.4 · `C/stages` (10 · 86 exp.)

| Fichero | L | Exp. | Contrato (reglas no obvias) |
|---|---:|---:|---|
| `plazo.go` | 99 | 2 | `ConPlazoPorLlamada` **obligatoria** en producción: sin ella el adaptador cae a sus 30 s, el umbral de lento baja a 24 s y una P3 caliente de 27 s cuenta como lenta ante el breaker («el default es COMPATIBLE, no seguro», `fase5_captacion.go:151-157`) |
| `p2.go` | 252 | 8 | `NewP2(log, sel ProviderSelector, store StageStore, …Opción)`; puertos `ProviderSelector` `:58`, `StageStore` `:70`; temperatura greedy; `llm.ErrLLMQuality` = fallo de calidad; ideas sin evidencia se descartan |
| `p3.go` | 451 | 8 | Especificación por ítem (fan-out), reintento con `TemperatureRetry` 🔶 |
| `tope.go` | 184 | 1 | Tope de **10** ítems a la entrada de P3 |
| `p4.go` | 440 | 5 | `NewP4(…, zona, …)`; `ZonaPorDefecto` = UTC (DEUDA-044.11: la zona se escribe en el arranque, no se hereda); I-CP-1 (el esquema no puede traer un valor que su validador rechace: P4 fue 0/14 por `"package_size": 0`) 🔶 el test AST de `p4_test.go` |
| `fechas.go` | 389 | 1 | Normalización de fechas con `evidence.Normalize` 🔶 |
| `match.go` | 457 | 26 | `NewMatch(log, store, …OpciónMatch)`: `OpciónMatch` es **otro tipo** que `Opción` para que «match con plazo» **no compile**; cascada `Exact → Fuzzy(0,85)`; **sin zona gris** en producción (`ConZonaGris` existe pero no se cablea: el tercer escalón llamaría al LLM por la misma plaza); `unmatched` con aviso; DEUDA-044.16 (un ítem malo no tira el borrador, `:42`, `:265`); nota del pedido por `SanitizeNote` (F6) |
| `match_cascada.go` | 481 | 8 | La cascada con `textmatch`; `VerificarNormalizador`: el normalizador de la caché y el del match son **el mismo** (`textmatch.Normalize`) o el arranque aborta |
| `match_lineas.go` | 404 | 0 | Solo no exportados (líneas, variantes en rango, envío): **no tiene contrato propio en el rojo** (patrón F1); su test nace con el verde de `match.go` 🔶 |
| `draft.go` | 1.038 | 27 | `NewDraft(log, store, solicitudes AlmacenSolicitudes, revision EscritorRevision, eventos EscritorEvento, …OpciónDraft)`; puertos `:330`, `:341`, `:347`, `EmpujadorCRM` `:367`; `ConEmpujeCRM`, `EmpujadorCRMFunc`; la revisión **solo** por `EscritorRevision` (el único store con cipher del literal: por el otro, texto en claro, `fase5_captacion.go:195-199`); empuje al CRM **solo** si `intake_jobs.requested_by` es de la dueña (D-044.19; el pipeline normal no empuja); eventos `intake_draft_created` (`:90`) e `intake_reanalyzed` (`:107`) 🔶 payload; `anclaje` importado sin `Repartir` (D-6); DEUDA-044.16 (`:259`) |

Por tamaño, `draft.go` **no** se parte (cambiaría el árbol de `04` sin necesidad); su contrato se
agrupa en el comentario por responsabilidad: cabecera · revisión · eventos · empuje.

### 2.5 · `C/pipeline` (4 · 57 exp.)

| Fichero | L | Exp. | Contrato |
|---|---:|---:|---|
| `pipeline.go` | 1.148 | 21 | `NewWorker(log, store PipelineStore, p2, p3, p4, match, draft, catalogos Catalogos, descifrador Descifrador, Config, …)`; puertos `Descifrador` `:95`, `Etapa*` `:100-123`, `Catalogos` `:136`, `ZonasDeEnvio` `:146`; `Run` (ticker `Cadencia`), `Despertar(tenantID, edgeID)`; `PlazoPorLlamadaSuelo = 48 s` (`:207`); `Config{}` por defecto: cadencia 5 s, backoff 30 s → 5 min, 3 intentos por calidad, 10 por infra (sin variable de entorno a propósito); `ConAforo`, `ConZonasDeEnvio` (sin ella todo borrador sale con el envío sin precio); DEUDA-044.10 (`:56`); el `source_text` se descifra con el **mismo** keyring que lo cifró el compositor |
| `plaza.go` | 243 | 9 | `KPorPlaza = 1`, `NuevoAforo`, `Plazas` (`:97`); «esperar tiene dos precios»; por vía `api` no hay plaza; el desalojo (Mecanismo 1) **no se construye** hasta tener dato de campo |
| `backoff.go` | 186 | 8 | Clasificación del fallo: `llm.ErrLLMQuality` (calidad) frente a `postgres.IsPermanentFailure`/`stages.ErrSinLiteral` (permanente) frente a infra 🔶 |
| `memoria.go` | 414 | 19 | D-F7-3 (¿producción o doble?) |

### 2.6 · `C/intakeahead` (3 · 22 exp.)

`New(log, cfg ConfigStore, sel ProviderSelector, sink Sink, …Option) *Pool`; `Request(key, texto)`
(sin uso, texto vacío o clave inválida ⇒ no hace nada; marca por clave para no pedir dos veces; cola
llena ⇒ suelta la marca y la ventana cierra por su reloj); `Run(ctx)` (4 workers); `Warm(tenantID,
edgeID, sessionID, kind)` (no bloquea; `WithCalentador`, `WithCalentamiento(bool)`); `SinkFunc`;
`saneo.go`: la clasificación se sanea con `evidence` (sin exportados). Es **el único consumidor real
de una clasificación** hoy (`arquitectura.md` del repo §2.4). 🔶 los 29 tests de
`intakeahead_test`/`calentamiento_test` (tiempos y dedupe).

### 2.7 · `C/reanalisis` (1 · 25 exp.)

`NewServicio(log, solicitudes, hilo, jobs, compositor, features, config)` con seis puertos
(`reanalisis.go:244-285`): `Solicitudes.ReanalysisTargetOf` (solo lectura: INV-10), `Hilo`
(`ListThread`, `ListPastedByOwner`, `AppendPastedMessage` — el texto pegado queda con
`origin='owner_pasted'`), `Jobs` (`JobNoTerminalDeEvento`, `AbrirReanalisis`: **no** es `JobStore` ni
`PipelineStore`, a propósito), `Compositor.ComposeAtFlush`, `Features.Has` (el **mismo** resolver
cacheado), `ConfigLLM.Get` (recortado: **sin** `APIKey`, solo si la hay). `ErrSinCablear` (texto
`reanalisis: el servicio necesita log, solicitudes, hilo, jobs, compositor, features y config LLM`),
`EnCursoError` (`reanalisis: el evento ya tiene un job vivo (%s)`), orígenes `OrigenAmbos`,
`OrigenHiloDelEvento`, `OrigenTextoPegado`; gates `llm_intake` y `api_llm` según la vía. Sin estado.
🔶 `reanalisis_test.go` (33) y el uso de `go/ast` en `dobles_test.go` (¿candado o comprobación de
dobles?).

### 2.8 · `C/casebank` (4 · 30 exp.) — sin gemelo

`casebank.go`: `Caso`, puerto `Store` (`Insertar` recibe el caso **ya anonimizado**; `Existe(tenant,
literal)` = guarda de idempotencia de la siembra), `Servicio` (`Insertar`, `Sembrar`). `anonimizar.go`:
`Anonimizador` (teléfonos y nombres con límites de palabra, `Restos` devuelve lo que quedó) 🔶 reglas
de `anonimizar_test`. `semilla.go`: `CasoAmbar`, `NombresDelCaso`, `EsperadoCasoAmbar`. `postgres.go`.
**Doble nuevo** `casebankhelpertest.Memoria` + `casebankhelpertest.Contrato` (E-6). `cmd/casebank` (se niega sin
`-consentido`, `cmd/casebank/main.go:53`) **no** cambia en F7 (D-F7-2).

### 2.9 · `C/intentcfg` (2 · 12 exp.)

`Config{Version, Blob, UpdatedAt}`, `Store` (`Get`, `Upsert`), `ErrNotFound`, `Kind` (el kind
`intents` del push al Edge), `MemoryStore` (gemelo: `05` E-6 se equivoca, README contradicción 1),
`PostgresStore`. **Aquí vive P1** (constitución §3.3: no en `prompts`). Suite `intentcfghelpertest.Contrato`
(sin versión ⇒ `ErrNotFound`; `Upsert` sustituye; por tenant).

## 3 · Dobles y suites (E-6)

| Puerto | Suite | Unitario | Postgres (F9, T9.28) |
|---|---|---|---|
| `intake.JobStore`, `intake.PipelineStore` | `intakehelpertest.ContratoCola`, `ContratoMaquina` | `MemoryStore` | `Postgres`, `machine_postgres` |
| `casebank.Store` | `casebankhelpertest.Contrato` | `casebankhelpertest.Memoria` **nuevo** | `Postgres` |
| `intentcfg.Store` | `intentcfghelpertest.Contrato` | `MemoryStore` | `PostgresStore` |
| Puertos de etapas, selector, sink, compositor, hilo | — | dobles locales en el test del consumidor | — |

## 4 · Reglas E-8 (las que un reimplementador olvidaría)

| # | Regla | Origen |
|---|---|---|
| R-01 | W=1 y K=1 por Edge; duplicar la línea del worker no da error, bloquea | I-CP-4 · `fase5_captacion.go:124-133` · ADR-0046 (fuera del repo: *«una cadena de lote en vuelo por (tenant, Edge)»*) |
| R-02 | No hay interruptor de configuración del pipeline: el único gate es la feature `llm_intake` (sin ella no hay ventana, ni job, y el worker gira en vacío) | `fase5_captacion.go:135-140` |
| R-03 | Plazo por llamada obligatorio (48 s) en las etapas LLM; `match` y `draft` **no** pueden llevarlo (tipos de opción distintos) | `fase5_captacion.go:151-181` |
| R-04 | Match sin zona gris; `unmatched` es el renglón que precifica la dueña, no una pérdida | `fase5_captacion.go:175-181` |
| R-05 | El normalizador de la caché y el del match son el mismo, verificado al arrancar | `fase5_captacion.go:238-246` |
| R-06 | La revisión la escribe el store con cipher del literal; nunca el `flowStore` | `fase5_captacion.go:195-199` |
| R-07 | El pipeline normal **no** empuja al CRM; solo el re-análisis pedido por la dueña (`requested_by`) | `fase5_captacion.go:209-213` · D-044.19 |
| R-08 | Un solo compositor en el proceso (ventana y re-análisis) | `fase5_captacion.go:53-62` |
| R-09 | La cola no lleva cipher: recibe bytes ya cifrados (D-044.26) | `fase3_almacenes.go:171-176` |
| R-10 | Re-análisis: no cambia el estado de la solicitud (INV-10); 400 de forma antes que 403; seis dependencias obligatorias | `reanalisis.go:238-243`, FX T-8 |
| R-11 | El adelanto es por **pull** (ADR-0045 fuera del repo: *«el Cloud orquesta la inferencia, el Edge solo la sirve»*); `Signal.Intent` es siempre nil (I-CP-2) | constitución I-CP-2 |
| R-12 | Cola del adelanto llena ⇒ se suelta la marca y la ventana cierra por su reloj (no es error) | `intakeahead.go:308-322` |
| R-13 | Los fallos de las goroutines de fondo son **mudos** (D-11): no se «arreglan» de paso | `deuda.md` D-11 |
| R-14 | `intake_jobs.artifacts` guarda la `evidence` en claro (D-2, decidido, no ejecutado) | `0083_intake_jobs_retencion.sql:80` |
| R-15 | 🔶 Reglas de los 17 tests de `stages`, 8 de `pipeline` (guiones «ámbar» y «hamburguesas», integridad T40, cadena Ola 3) y 33 de `reanalisis` | lectura E-8 en T7.7–T7.12 |

## 5 · Textos observables

Estados y etapas (§2.2); eventos `intake_draft_created`, `intake_reanalyzed`; `EnCursoError` y
`ErrSinCablear` (§2.7); el log `adelanto: cola de clasificación llena; la ventana cerrará por su reloj`
(no es contrato de wire, pero se conserva); el kind `intents`; los textos de `publicapi/reanalyze.go` e
`intents.go` (códigos y cuerpos 🔶, inventario en T7.1 con
`grep -rn 'errors.New("\|fmt.Errorf("' V`).

## 6 · Candados

| Candado | Regla | En F7 |
|---|---|---|
| `internal/arranque/pipeline_captacion_cableado_test.go` (copia F0) | I-CP-4: una goroutine del worker, cinco constructores de etapa, `ConAforo`, `ConZonasDeEnvio`, `gw.OnEdgeReady = …Despertar` | sigue verde con los paquetes nuevos (nombres cortos) — T7.23 |
| `internal/arranque/reanalisis_cableado_test.go` | `reanalisis.NewServicio` cableado, `stages.NewDraft` con `ConEmpujeCRM`, campo `Reanalysis` | la aserción del campo pasa a la cara nueva — T7.24 |
| `internal/arranque/calentamiento_cableado_test.go` | `gw.OnWarmup` asignado; `intakeahead.WithCalentamiento ← cfg.LLM.WarmupEnabled` | verde con el `Pool` nuevo |
| `S/intakes/inv1_aprobar_test.go` (F6) | INV-1 | T7.25: sustituye `../../../../intake{,/pipeline,/stages}` por `../../captacion/{intake,pipeline,stages}` y añade `../../captacion/{reanalisis,intakeahead}` |
| `stages/draft_test.go`, `p4_test.go`, `reanalisis/dobles_test.go` (usan `go/ast`) | 🔶 | leer en T7.7/T7.12: si son candados de invariante se reescriben (E-7); si no, se sustituyen por tests de conducta |
| (BD) de mensaje a borrador y re-análisis | cola, etapas, match y draft contra Postgres | F9 P4 y P8 |
