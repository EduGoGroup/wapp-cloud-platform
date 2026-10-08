# F7 · Diseño — contratos por paquete, puertos, suites, reglas E-8 y candados

> `C` = `internal/modulos/captacion`, `V` = paquete viejo. 🔶 = **por completar en el bloque de
> contratos (lectura E-8 obligatoria)**: la spec no leyó ese test viejo entero; quien escriba el
> contrato lo lee antes y lleva sus reglas al comentario (E-8).

## 1 · Cómo se contó

Igual que F6 `diseno.md` §1 (ficheros, líneas, `^func Test`, exportados con bloques `const (`/`var (`
y métodos de tipos exportados). F7: **32** ficheros, **11.144** líneas, **305** exportados, **≈119**
cuerpos de función/método exportados (cota de `make test-pendiente`, se fija en T7.13).

### 1.1 · Clasificación E-12 por paquete — **provisional, sin medir: la fija el inventario E-12** (T7.1)

Deducida de `arquitectura.md` §1, §2 y §5. «Consumidores» = paquetes de producción de F7 que lo importan según §2 de
`arquitectura.md`; el recuento exacto por archivo está **sin medir**.

| Paquete | Estado en memoria | Concurrencia | BD | Consumidores | Nivel provisional |
|---|---|---|---|---|---|
| `evidence` (1) | no | no | no | `anclaje`, `stages`, `intakeahead` | **simple** |
| `anclaje` (1) | no | no | no | `stages`, `pipeline` (sin llamante de `Repartir`) | **medio** |
| `intentcfg` (2) | `MemoryStore` con `sync.Mutex` | no | `PostgresStore` | `intakeahead`, cara HTTP | **complejo** por criterio (128 líneas: lo confirma o lo baja Jhoan) |
| `casebank` (4) | no | no | `postgres.go` | solo `cmd/casebank` | **medio**; `postgres.go` **complejo** |
| `intake` (6) | `MemoryStore` con `sync.Mutex` | `FOR UPDATE SKIP LOCKED` | `postgres.go`, `machine_postgres.go` | `pipeline`, `stages`, `intakeahead`, `reanalisis` | **complejo** |
| `stages` (10) | no | no | no (por puertos) | `pipeline`, `reanalisis` | **medio**; `plazo.go` y `tope.go` candidatos a simple |
| `pipeline` (4) | canal, aforo con `sync.Mutex`/`sync.Once` | goroutine, ticker | por `PipelineStore` | el arranque | **complejo** (`pipeline.go`, `plaza.go`); `backoff.go` medio; `memoria.go` según D-F7-3 |
| `intakeahead` (3) | marcas por clave, cola | 4 workers, goroutine suelta | no | el arranque, agregador viejo (por adaptador) | **complejo**; `saneo.go` sin exportados |
| `reanalisis` (1) | no | no | no (seis puertos) | cara HTTP | **medio** |
| `apipublica/{reanalyze,intents}.go` | no | no | no | la cara | **medio** |
| `internal/arranque/bridge_captacion.go` | no | no | no | el arranque | **simple** (`05` §4.2) |

### 1.2 · Inventario E-12 — **aprobado por Jhoan el 2026-10-08** (sesión F7-01, T7.1)

Medido sobre `dev` @ `8d875ab`. Recuento confirmado: **32 ficheros, 11.144 líneas**; con D-F7-3 resuelta quedan **31 de
producción** y los dobles. «Consum.» = paquetes de producción distintos, fuera del propio, que usan un exportado del fichero
(regla: `pkg.Exportado` atribuido al fichero que lo declara; los métodos, por `.Método(`, que cuenta también las llamadas
por interfaz; se cuentan paquetes, no ficheros). En los paquetes de sesiones posteriores solo se midió el paquete entero.

| Fichero | L | Estado en memoria | Concurrencia | BD | Consum. | Nivel |
|---|---:|---|---|---|---:|---|
| `evidence/evidence.go` | 80 | no | no | no | 3 | **simple** |
| `anclaje/anclaje.go` | 441 | no | no | no | 2 | **medio** |
| `intake/store.go` | 207 | no | no | no (tipos + puerto `JobStore`) | 7 | **medio** |
| `intake/machine.go` | 385 | no | no | no (tipos + puerto `PipelineStore`) | 3 | **medio** |
| `intake/memory.go` | 297 | sí, `sync.Mutex` | cerrojo | no | 0 | **complejo** |
| `intake/postgres.go` | 260 | no | — | 4 sentencias únicas | 3 | **complejo** |
| `intake/machine_postgres.go` | 431 | no | `FOR UPDATE SKIP LOCKED` ×2 | 7 operaciones, sentencias únicas | 2 (por el puerto) | **complejo** |
| `intake/reanalisis.go` | 216 | no | no | 2 sentencias | 1 | **complejo** |
| `intentcfg/store.go` | 78 | `MemoryStore`, `sync.Mutex` | cerrojo | no | 4 | **medio** (bajado: 78 líneas, dos operaciones) |
| `intentcfg/store_postgres.go` | 50 | no | no | 2 sentencias | 2 | **medio** (bajado) |
| `casebank/casebank.go` | 173 | no | no | no (puerto `Store`) | 1 | **medio** |
| `casebank/anonimizar.go` | 386 | no | no | no | 1 | **medio** |
| `casebank/semilla.go` | 152 | no | no | no (datos) | 1 | **simple** (bajado) |
| `casebank/postgres.go` | 75 | no | no | 2 sentencias | 1 | **medio** (bajado) |
| `stages/plazo.go`, `tope.go` | 99 · 184 | no | no | no | ≈2 | **simple** |
| `stages/{p2,p3,p4,fechas,match,match_cascada,match_lineas}.go` | 252–481 | no | no | no (por puertos) | ≈2 | **medio** |
| `stages/draft.go` | 1.038 | no | no | no (4 puertos) | ≈2 | **medio**; se parte (E-13, D-F7-6) |
| `pipeline/pipeline.go` | 1.148 | canal | goroutine, ticker | por `PipelineStore` | ≈1 | **complejo**; se parte (E-13) |
| `pipeline/plaza.go` | 243 | `sync`, canales | aforo K=1 | no | ≈1 | **complejo** |
| `pipeline/backoff.go` | 186 | no | no | no | ≈1 | **medio** |
| `pipeline/memoria.go` | 414 | doble de test | — | — | 0 | fuera de producción (D-F7-3, D-F7-5) |
| `intakeahead/intakeahead.go` | 592 | marcas por clave, cola | 4 workers | no | ≈1 | **complejo** (592: tolerancia de E-13) |
| `intakeahead/calentamiento.go` | 216 | no | goroutine suelta | no | ≈1 | **complejo** |
| `intakeahead/saneo.go` | 147 | no | no | no | 0 (sin exportados) | **medio** |
| `reanalisis/reanalisis.go` | 772 | no | no | no (seis puertos) | ≈1 | **medio**; se parte (E-13) |
| `apipublica/{reanalyze,intents}.go` | — | no | no | no | la cara | **medio** |
| `internal/arranque/bridge_captacion.go` | — | no | no | no | el arranque | **simple** (`05` §4.2) |

Las cuatro bajadas (`intentcfg` entero, `casebank/postgres.go`, `casebank/semilla.go`) van contra el criterio literal de
E-12 y las aprobó Jhoan: la verdad de esos adaptadores es su suite con `Montaje` en memoria y en Postgres, **un caso por
método** (hallazgo 63 de F6).

**Adaptadores y puentes** (1 nace, 1 muere, 2 puentes de import):

- **Nace** `internal/arranque/bridge_captacion.go` (F7-04; muere F8): `aheadBridge`, `composerBridge`, la clausura del
  sink y la segunda instancia vieja de `intake.Postgres` construida dentro (D-F7-1, D-R-3).
- **Muere** (F7-04) `llmConfigBridge` de `bridge_inferencia.go`, con `llmConfigReader` y `toLegacyLLMConfig`;
  `turneroBridge` sigue hasta F8.
- **Muere** (F7-04, T7.24) el centinela `oldFaceIntakesMountSentinel` (`internal/arranque/fase8_transporte.go:293`,
  hallazgo 55 de F6, D-F6-13), cuando H1 pase a la cara nueva.
- **Cambia de origen** (T7.24) `quoteCallTimeout` (`internal/arranque/fase5_captacion.go:43`, hallazgo 59 de F6): pasa
  a leer `PlazoPorLlamadaSuelo` del `pipeline` nuevo.
- **Puentes de import**: `stages → flujos/store`, `reanalisis → flujos/events`; el tercero se evita pasando
  `DefaultThreadLimit` por constructor.
- **`Plazas` no necesita adaptador**: interfaz de un método (`PlazaDe(ctx, tenantID, originSessionID) (edgeID, ok, err)`,
  `plaza.go:97`), consumida solo por el worker, que el `llmvia.Selector` **nuevo** satisface estructuralmente
  (`modulos/inferencia/llmvia/llmvia.go:524`); el arranque nuevo ya lo entrega así.

**🔶 resueltos**:

- **Reloj de `machine_postgres.go`**: todo es `now()` de SQL; el único instante que viene de Go es el parámetro `next` de
  `Retry` (lo calcula el worker con su reloj inyectado, `pipeline.go:1014`; se rechaza `IsZero()`). Sin transacciones
  explícitas en ningún paquete hoja: sentencias únicas. `FOR UPDATE SKIP LOCKED` solo en las subconsultas de los dos
  reclamos (`:90`, `:130`).
- **`PipelineStore` tiene 7 métodos**: `ClaimNext`, `ClaimNextIgnorandoBackoff`, `SaveStage`, `Release`, `Retry`,
  `Finish`, `Fail`. **`Despertar` no es del store** (contra §2.2): es `(*Worker).Despertar` (`pipeline.go:402`), un canal
  en memoria que acaba en `ClaimNextIgnorandoBackoff`.
- **Tablas**: `public.intent_configs` (`tenant_id` PK, `version`, `config` JSONB, `updated_at`; migración `0033`) y
  `public.intake_case_bank` (`id`, `tenant_id`, `consented` con `CHECK`, `source_text`, `expected`, `created_at`; `0082`).
- **Columnas de `intake_jobs` por operación** (lo que vigila la marca de estado de las suites, hallazgo 35):

  | Operación | Columnas que toca | Guarda |
  |---|---|---|
  | `OpenOrAppend` | INSERT `tenant_id, session_id, contact_id, event_id, status='aggregating', message_ts, source_refs`; en conflicto `source_refs ‖`, `updated_at` | índice parcial `status='aggregating'` |
  | `CloseWindow` | `status='pending'`, `updated_at` | `status='aggregating'` |
  | `PutSourceText` | `source_text_enc`, `source_text_dek`, `source_text_kek_id`, `updated_at` | el `pending` más reciente con `source_text_enc IS NULL` |
  | `ListAggregating` | ninguna (lectura) | `status='aggregating'` |
  | `ClaimNext` / `ClaimNextIgnorandoBackoff` | `status='processing'`, `updated_at` | `pending` (+ `next_attempt_at <= now()` / + `tenant_id`) |
  | `SaveStage` | `stage`, `artifacts ‖`, `updated_at` | `processing` y monotonía de etapa |
  | `Release` | `status='pending'`, `updated_at` | `processing` |
  | `Retry` | `status='pending'`, `attempts+1`, `next_attempt_at`, `updated_at` | `processing` |
  | `Finish` | `status='done'`, `intake_id` (COALESCE), las tres del sobre a NULL, `updated_at` | `processing` |
  | `Fail` | `status='failed'`, `error`, las tres del sobre a NULL, `updated_at` (`stage` se conserva) | `processing` |
  | `JobNoTerminalDeEvento` | ninguna (lectura) | `status = ANY($3)` |
  | `AbrirReanalisis` | INSERT `…, status='pending', message_ts` (COALESCE del primer job del evento, o `now()`), `source_refs='[]', intake_id, requested_by, reanalysis_via, reanalysis_source, reanalyzed_from` | no es idempotente |

- **Textos observables de las hojas** (se copian literales; el identificador va en inglés, E-11): estados
  `aggregating`·`pending`·`processing`·`done`·`failed`; etapas `p2`·`p3`·`p4`·`match`·`draft`; `RequestedByOwner="owner"`;
  `Kind="intents"`; `ErrNotFound` = `config de intents no encontrada`; kinds de `anclaje` (`image`, `audio`, `ptt`,
  `voice`, `video`, `document`) y `EtiquetaAudio`; marcas y clases del anonimizador (`[JID]`, `[TELEFONO]`, `[NOMBRE]`;
  `jid`, `telefono`, `nombre`); los tres centinelas de `casebank` y `errIncompleteEnvelope`; y los ≈45 literales de
  `fmt.Errorf`/`errors.New` de `V/intake/{machine,memory,postgres,reanalisis,machine_postgres}.go`,
  `V/intentcfg/*.go` y `V/casebank/{casebank,postgres}.go`, que quien porta copia del fichero viejo. `evidence` y
  `anclaje` no tienen ninguno.
- **Textos de la cara vieja** (para F7-04; el cuerpo es `{"error":"…"}`, **sin** clave `code`):
  - H1 `POST /api/v1/intakes/{id}/reanalyze` (`intakes.write`, auditada, sin middleware de feature: `llm_intake` y
    `api_llm` viven en el dominio). Orden: forma → gate base → gate de vía → credencial → solicitud → job vivo → fuente.
    401 `autenticación requerida` · 400 `cuerpo JSON inválido` · 400 `invalid_via` (+`via`, `configured_via`) · 400
    `text_too_long` (+`runes`, `max`) · 403 `feature_not_enabled` (+`feature`) · 422 `llm_credentials_missing` (+`via`) ·
    404 `solicitud no encontrada` · 422 `reanalysis_in_progress` (+`job_id`) · 422 `source_unavailable` (+`reason`:
    `purged`/`never_stored`) · 500 `no se pudo pedir el re-análisis de la solicitud` · 200 `reanalyzeResponse`.
  - E1 `GET /api/v1/intents` (`intents.read`): 401 · 500 `store de intents no configurado` · 404 `el tenant no tiene
    config de intents` · 500 `no se pudo leer la config de intents` · 200 `{version, config}`.
  - E2 `PUT /api/v1/intents` (`intents.write`, auditada; gate `llm_intent` dentro del handler): 401 · 500 `API de intents
    no configurada` · 500 `no se pudo verificar el entitlement` · 403 `el plan del tenant no incluye la clasificación de
    intenciones` (prosa, no `feature_not_enabled`) · 400 `no se pudo leer el cuerpo` · 413 `la config excede el tamaño
    máximo de %d bytes` · 400 `config de intents inválida: …` · 400 `el cuerpo debe ser JSON válido` · 500 `no se pudo
    persistir la config de intents` · 200 `{"version":"<12 hex>"}`; el push es best-effort.
  - Tests viejos: `reanalyze_test` 16, `intents_test` 6, `intents_aditividad_test` 2.
- **Tests viejos de las hojas**: `evidence` 2 · `intake` 39 (14 unitarios, 25 de integración que se saltan sin
  `WAPP_TEST_DB_DSN`) · `anclaje` 19 · `intentcfg` 4 (3 + 1) · `casebank` 30 (25 + 5).

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
| `memory.go` | 297 | 13 | `MemoryStore` (gemelo de `JobStore`, `sync.Mutex` `:79`): corre `ContratoCola`. ✎ **No** implementa `PipelineStore` (§1.2, D-F7-5): `ContratoMaquina` la corre en memoria el doble de `intakehelpertest` |
| `postgres.go` | 260 | 6 | Adaptador de `JobStore` — **sin cipher a propósito** (D-044.26: lo que llega a `PutSourceText` son bytes ya cifrados; `fase3_almacenes.go:171-176`) |
| `machine_postgres.go` | 431 | 7 | Adaptador de `PipelineStore` (reclamo, avance de etapa, castigo con causa, backoff, `Despertar`) 🔶 `FOR UPDATE SKIP LOCKED` y reloj |

Suites `C/intake/intakehelpertest`: `ContratoCola(t, func(t) Montaje)` sobre `JobStore` (abrir o anexar a la ventana de
una clave; cerrar devuelve `true` una sola vez; listar solo `aggregating`; `PutSourceText` idempotente
por clave) y `ContratoMaquina(t, func(t) Montaje)` sobre `PipelineStore` (reclamar solo `pending`; un job reclamado
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
| `match_lineas.go` | 404 | 0 | Solo no exportados (líneas, variantes en rango, envío): **no tiene contrato propio en el rojo** (patrón F1); su test nace con el verde de `match.go` y cubre solo sus reglas de negocio y ramas no triviales, no la fontanería ni los `if err != nil`; el resto lo cubre F9 (E-4) 🔶 |
| `draft.go` | 1.038 | 27 | `NewDraft(log, store, solicitudes AlmacenSolicitudes, revision EscritorRevision, eventos EscritorEvento, …OpciónDraft)`; puertos `:330`, `:341`, `:347`, `EmpujadorCRM` `:367`; `ConEmpujeCRM`, `EmpujadorCRMFunc`; la revisión **solo** por `EscritorRevision` (el único store con cipher del literal: por el otro, texto en claro, `fase5_captacion.go:195-199`); empuje al CRM **solo** si `intake_jobs.requested_by` es de la dueña (D-044.19; el pipeline normal no empuja); eventos `intake_draft_created` (`:90`) e `intake_reanalyzed` (`:107`) 🔶 payload; `anclaje` importado sin `Repartir` (D-6); DEUDA-044.16 (`:259`) |

~~Por tamaño, `draft.go` **no** se parte~~ — ✎ **D-F7-6 (Jhoan, 2026-10-08)**: manda `05` E-13, que es posterior a esta
spec: `draft.go` (1.038), `pipeline.go` (1.148) y `reanalisis.go` (772) **se parten por tema** al reconstruirlos; el
contrato de `draft` se reparte por responsabilidad: cabecera · revisión · eventos · empuje.

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
`saneo.go`: la clasificación se sanea con `evidence` (sin exportados: su test nace en el verde, solo por su regla de saneo, E-4). Es **el único consumidor real
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

## 3 · Dobles y suites (E-6, P4)

Todo puerto con BD tiene su suite `Contrato…(t, func(t) Montaje)`, corrida **en memoria y en Postgres** con el arnés de
F9-A: garantiza que los dos se comportan igual, y es la verdad de `postgres.go`, `machine_postgres.go` y
`store_postgres.go`. La marca de estado vigila **todas** las columnas que la operación puede tocar (hallazgo 35); la
lista de columnas de `intake_jobs` por operación se fija en T7.3 🔶.

| Puerto | Suite | En memoria | Postgres (arnés; T7.27 = T9.28) |
|---|---|---|---|
| `intake.JobStore` | `intakehelpertest.ContratoCola` | `MemoryStore` | `Postgres` |
| `intake.PipelineStore` | `intakehelpertest.ContratoMaquina` | doble de `intakehelpertest` (porta `StoreEnMemoria` de `V/pipeline/memoria.go`, D-F7-5) | `machine_postgres` |
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
