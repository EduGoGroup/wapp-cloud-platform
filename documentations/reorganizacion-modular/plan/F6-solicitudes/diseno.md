# F6 · Diseño — contratos por paquete, puertos, suites, reglas E-8 y candados

> `S` = `internal/modulos/solicitudes`, `V` = el paquete viejo de referencia. Medido sobre `dev` @
> `1b18932`. 🔶 = regla **por completar en el bloque de contratos (lectura E-8 obligatoria)**: la
> spec no llegó a leer esos tests viejos entero; la sesión que escribe el contrato **debe** leerlos
> antes (E-8) y llevar sus reglas al comentario.

## 1 · Inventario y cómo se contó

- **Ficheros y líneas**: `ls V/*.go | grep -v _test.go | wc -l` y `cat … | wc -l`.
- **Tests viejos**: `grep -c '^func Test' V/*_test.go`; integración = fichero que llama a
  `openTestDB` o lee `WAPP_TEST_DB_DSN` (`grep -l 'openTestDB(\|WAPP_TEST_DB_DSN'`).
- **Exportados** (regla): `func`/`type`/`var`/`const` de primer nivel con mayúscula + métodos
  exportados de tipos exportados + nombres exportados dentro de bloques `const (`/`var (`.
  Total F6: **404**. Cuerpos que llevarán `panic(pendiente.Implementar)` (funciones + métodos
  exportados, `grep -hE '^func (\([a-z]+ \*?[A-Z]\w*\) )?[A-Z]'`): **≈191** (cota superior; la cifra
  real la fija T6.13 con `make test-pendiente`).

### 1.1 · Clasificación E-12 por paquete — provisional, sin medir: la fija el inventario E-12 (T6.1)

Deducida de `arquitectura.md` §1, §2 y §5. No clasifica fichero a fichero donde falta el dato (nº de consumidores:
**sin medir**, salvo `intakes` ← `publicapi`, README contradicción 7). Si un archivo sale peor, sube de nivel.

| Paquete | ¿Estado en memoria? | ¿Concurrencia? | ¿BD / transacciones? | Nivel provisional |
|---|---|---|---|---|
| `integrations/sigv1` | no | no | no | **simple** |
| `intakes/note.go` | no | no | no | **simple** |
| `intakes/telemetria` | no | no | no (escribe por un puerto) | **simple** |
| `integrations/crmpush` | no | no | no | **medio** (lógica de contrato externo, candado R-12) |
| `intakes/quotetext` | no | no | no | **medio** (lógica de negocio y fallback de P5) |
| `tenantvars` | `MemoryStore` con `sync.Mutex` | no | `postgres.go`: `Replace` en transacción | `tenantvars.go` **simple**; `memory.go` y `postgres.go` **complejo** por criterio, aunque pequeños (219 líneas) |
| `intakes` · tipos puros (10) | no | no | no | **medio** |
| `intakes` · acciones (9) | no (la marca vive en BD) | no | por el puerto `Store` | **medio**, salvo lo que el inventario suba (`approve.go`: INV-1 y tres efectos) |
| `intakes` · `memory.go`, `postgres.go`, `buyerdata_postgres.go`, `notifier.go` | `MemoryStore` con `sync.Mutex` | no | `WithTx` ×5, cifrado por campo | **complejo** (`notifier.go`: sin BD propia, sale a WhatsApp; nivel a confirmar) |
| `integrations` | no | worker: 1 goroutine, 2 tickers | `postgres.go` | `worker.go` y `postgres.go` **complejo**; `store.go`, `gate.go`, `crud.go`, `outbox_stats.go` **medio** |
| `internal/arranque/bridge_<x>.go` | no | no | no | **simple** (`05` §4.2) |

Qué se hace en cada nivel: [`reglas.md`](reglas.md) §5.

## 2 · Por paquete: ficheros, exportados y contrato

### 2.1 · `S/intakes` (24 ficheros · 279 + 4 exportados)

| Fichero | L | Exp. | Qué promete (contrato) | Tests viejos que se **leen** (E-8) |
|---|---:|---:|---|---|
| `status.go` | 228 | 21 | Los 11 estados, `NormalizeStatus` (alias `closed`→`confirmed`, un único punto), `IsStatus`, `AllowedTransitions`, `TransitionError`, avisos al cliente por estado (`StatusNotice`) | `status_test`, `status_notice_test` |
| `intakes.go` | 453 | 17 | Tipos `Intake`, `Item`, `Detail`, `Filter`/`Page` (`DefaultPageSize=50`, `MaxPageSize=200`), `MaxExportIntakes=5000`, `ErrNotFound`, `ErrConflict`, `ErrTooLarge`, **el puerto `Store`** (`:325`) | `filtro_args_test` (mixto), `filtro_lista_test`, `intake_type_integration_test` (→F9) |
| `service.go` | 509 | 21 | `Service` y opciones (`WithNotifier`, `WithDepositReminder`, `WithExpiryReminder`, `WithQuoteSender`, `WithCRMPusher`, `WithMetrics`); puertos `StatusNotifier`, `DepositTouch`, `ExpiryTouch`, `CRMPusher`, `QuoteSender`; lectura con `touch` perezoso (los dos recordatorios, independientes); `Summary` con **reloj inyectado** (D-F6-5) | `service_test`, `service_notify_test`, `tres_puertas_crm_test` |
| `approve.go` | 457 | 16 | `Approve`: la **única** acción que escribe estado + manda WhatsApp + empuja CRM (INV-1); `ErrNoQuoteSender` si falta el emisor; precios pendientes → `PendingPriceError`; `NotApprovableError`, `ApprovableStatus` | `approve_test` (15, mixto), `approve_contrato_test` 🔶 |
| `aprobadas.go` | 155 | 3 | Historial aprobado del tenant (lo lee P5: `ApprovedRenderedTexts`) | `aprobadas_test`, `aprobadas_integration_test` (→F9) |
| `edit.go` | 400 | 15 | `EditMode` (`EditPlain` / `EditAsCorrection`), `ValidateEditableItems`, `InvalidItemsError`, `TooManyItemsError`, `NotEditableError`, `EditableStatus`, `ReservedSKUPrefix = "_"` | `edit_test`, `correct_test` 🔶, `edit_integration_test` (→F9) |
| `discard.go` | 294 | 14 | Descarte en lote: `MaxDiscardBatch = 200`, `DiscardResult`/`DiscardSkip`, `ErrEmptyDiscardBatch`, `TooLargeBatchError` | `discard_test`, `discard_integration_test` (→F9) |
| `deposit.go` | 315 | 7 | Seña: `DepositStore` (puerto, `:71`), `DepositReminder` (`RemindContact` → lista de avisos; marca `deposit_reminded_at`) | `deposit_test`, `deposit_integration_test` (→F9) |
| `shipping.go` | 176 | 10 | `ShippingSKU = "_shipping"`, `ShippingPolicy`, `ShippingOnlyIfZones`, `ShippingZone`, `DesiredShippingLine` | `shipping_test`, `shipping_integration_test` (→F9) |
| `requestinfo.go` | 129 | 2 | `RequestInfo`: WhatsApp al cliente y paso a `needs_info` (INV-1); `ErrEmptyQuestion` | `requestinfo_test` |
| `vencimiento.go` | 383 | 12 | `QuoteDeadline = 24 * time.Hour` (**constante de plataforma**, D-044.50 §1, jamás `order_ttl`); `Overdue`; `ExpiryStore` (`:171`), `OwnerNotice` (`:196`), `NewExpiryReminder`, `NewLogOwnerNotice` (emisor = **traza en log**: nadie puede afirmar que la dueña recibe el aviso, `fase6_solicitudes.go:45-56`); el plazo **avisa y no mata** (ADR-0029 enm. 2) | `vencimiento_test` (16), `vencimiento_cas_test`, `vencimiento_sql_test` (→F9), candado §6 |
| `revisions.go` | 265 | 22 | `Revision`, `RevisionLine`, `RevisionPayloadVersion = 1`, `RevisionKindCart`/`RevisionKindInterpreted`, `RevisionBySystem`/`RevisionByOwner`, `CartRevisionPayload`, `LineChange`, puerto `RevisionWriter` (`:116`) | `revisions_test`, `revisions_integration_test` (→F9), `retencion_test` 🔶 |
| `reanalisis.go` | 143 | 3 | `ReanalysisTarget`, `ReanalysisTargetOf` (lo usa la puerta de re-análisis de F7, **sin** poder transicionar: INV-10), `PushRevisionByID` (nil-safe si no hay CRM) | 🔶 |
| `revalidate.go` | 380 | 14 | Revalidación de precios contra el catálogo: `PriceList`, `NewPriceList`, `CatalogEntry`, `Revalidate`, `Revalidation` (lo consume el carrito viejo) | `revalidate_test`, `revalidate_integration_test` (→F9) |
| `literal.go` | 311 | 12 | Literal del cliente en la revisión (nivel 2, cifrado por `ConCifraDeLiteral`); `PartirLiteral`, `ClavePayloadLines`; poda y retención | `literal_test`, `literal_integration_test` (→F9), `sello_poda_test` |
| `customernote.go` | 75 | 1 | La nota del pedido (`intakes.customer_note`) | `customernote_integration_test` (→F9) |
| `summary.go` | 110 | 4 | `Summary`, `BuildSummary(details, filter, now)` | `summary_test` |
| `crm.go` | 174 | 7 | `CRMReflection`, `IsCRMStatus`, el reflejo del callback `intake.status` en la solicitud | `crm_notifier_test`, `crm_integration_test` (→F9) |
| `metricas.go` | 342 | 6 | Puerto `PublicadorDeMetricas` (`:83`) y los tres eventos `intake_line_corrected` `:55`, `intake_approved` `:57`, `intake_info_requested` `:59` con su payload 🔶 | `metricas_test` |
| `notifier.go` | 574 | 15 | `Notifier` (salida hacia WhatsApp por `MessageSender.SendText(ctx, sessionID, to, text) (*cloudlinkv1.Ack, error)` `:102`), `Destinations` (`:114`), `SettingsReader` (`:133`); plantillas que ve el **cliente** 🔶 | `notifier_test`, `notifier_integration_test` (→F9) |
| `buyerdata.go` (+ `buyerdata_postgres.go`, D-F6-6) | 219 | 6 | `BuyerData`, `PostgresBuyerData` (sobre KEK por **fila**: el envelope de PII de negocio — 🔴 no es la DEK del ADR-0007), `PutBuyerField` | `buyerdata_test`, `buyerdata_integration_test` (→F9) |
| `memory.go` | 907 | 31 | `MemoryStore`: gemelo en memoria del `Store` (37 métodos, `sync.Mutex` `:20`). **Corre `intakeshelpertest.Contrato`** | todos los unitarios lo usan |
| `postgres.go` | 1.734 | 20 | Adaptador (`NewPostgres`, `ConCifraDeLiteral`, `ConLogDeRetencion`); SQL con `WithTx`; funciones puras extraídas para mapeo de filas y de `IsUniqueViolation`; `revisionsOf → ejecutarPoda → sellarPodada` (candado §6) | `postgres_integration_test` (16, →F9), `event_pair_*`, `proyeccion_cabecera_test` |
| `note.go` | 150 | 4 | `MaxNoteRunes = 280`, `NoteTooLongError{Runes,Max}` con su `Error()` **literal** `cart: …`, `SanitizeNote` (reglas en §4) | `cart/notes_test` (14), `cart/buyer_test` |

**Suite `S/intakes/intakeshelpertest`**: `Contrato(t *testing.T, nuevo func(t *testing.T) Montaje)` (firma
D-F1-1) sobre el puerto `Store` (`intakes.go:325`): alta por evento, lectura por tenant (otro tenant
→ `ErrNotFound`), paginación y filtros, transición válida/ inválida (`TransitionError`), revisiones
con número creciente, envío idempotente (`EnsureShippingLine`), historial aprobado. Se corre **en memoria** (`memory_test.go`, desde F6-02/F6-03) **y en Postgres** con el
arnés (P4; la pasada que cuenta es T6.27 = T9.27). La marca de estado de la suite vigila **todas** las
columnas que cada operación puede tocar, no una sola (hallazgo 35 de F1). 🔶 los casos exactos salen de
`postgres_integration_test.go` (16 tests).

### 2.2 · `S/intakes/quotetext` (4 · 47 exp.)

| Fichero | L | Exp. | Contrato |
|---|---:|---:|---|
| `quotetext.go` | 587 | 25 | `NewServicio(log, solicitudes LectorSolicitudes, historial LectorHistorial, sel ProviderSelector, …)`, `ConSemilla(LectorSemilla)` (ref `quote_style_examples` de `tenant_content`, opcional de verdad), `ConPlazo`; puertos `:192`, `:202`, `:210`, `:219`; temperatura greedy; `llm.ErrLLMQuality` → **fallback** con `fallback_reason` (los nueve motivos 🔶, `:137-160`) |
| `precios.go` | 526 | 14 | Precios pendientes (`intakes.PendingPriceLines`/`PendingPriceError`): P5 no redacta con precios sin poner |
| `borrador.go` | 143 | 6 | El borrador determinista (sin LLM) desde las líneas; ignora las de SKU reservado (`_`) |
| `render.go` | 145 | 2 | Render del texto final |

Tests viejos: `quotetext_test`, `precios_test`, `fewshot_test`, `atribucion_test`, `ayudas_test`,
`render_test`, `herminia_fixture_test`, `dobles_test` (40 `Test*`) 🔶.

### 2.3 · `S/intakes/telemetria` (1 · 6 exp.)

`telemetria.go`: `New(Outbox) …` que implementa `intakes.PublicadorDeMetricas` escribiendo
`store.FlowEvent` por `Outbox.InsertFlowEvent` (`:64-66`). **Existe porque `intakes` no puede
importar `flujos/store`** (ciclo con el test in-package de aquel, `fase6_solicitudes.go:94-98`).
🔶 Importa `internal/flujos/store` **viejo** (puente, `arquitectura.md` §2). Test viejo: `telemetria_test` (4).

### 2.4 · `S/integrations` (6 · 36 exp.) — **sin gemelo en memoria** (`05` E-6)

| Fichero | Exp. | Contrato |
|---|---:|---|
| `store.go` | 8 | Tipos `WebhookOutbox`, `TenantIntegration` (`EndpointURL "" == NULL`, `HasSecret`, `Enabled`) y el puerto `Store` (`:73`, 10 métodos: `EnqueueWebhook`, `ClaimWebhookBatch`, `MarkWebhookDelivered/Failed/Dead`, `RecoverOrphanDeliveries`, `Get/UpsertTenantIntegration`, `GetTenantSecret`, `DeleteTenantIntegration`) |
| `gate.go` | 4 | `EntitlementsGate.Enabled`: D-042.8 = feature `crm_bridge` **y** `events_adapter='webhook'` **y** `enabled=true`; puerto `FeatureResolver` |
| `worker.go` | 7 | `NewWorker(store, buyerData, intakes, tenantVars, log, WorkerConfig, onDelivery)`; `Run(ctx)`: ticker de sondeo + ticker de rescate (`RecoverOrphanDeliveries` con `ClaimLease = max(3×Timeout, 1 min)`), lote `BatchSize = 20`, firma `sigv1`, backoff por intento, `dead` al agotar `MaxAttempts`; puertos `BuyerDataReader` `:49`, `CustomerNoteReader` `:63`, `TenantVariablesReader` `:72`; **reloj inyectado** en el nuevo (hoy `time.Now()` `:247,460`) |
| `crud.go` | 3 | `Fingerprint(secret)` (pura) · `SecretFingerprint` pasa a `postgres.go` (D-F6-6) |
| `outbox_stats.go` | 2 | `OutboxCounts` · `CountOutbox` pasa a `postgres.go` (D-F6-6) |
| `postgres.go` | 12 | Adaptador; secreto HMAC cifrado con el `FieldCipher` compartido; `scanWebhookRows` y `closeClaim` como funciones puras testeables |

**Suite y doble nuevos** (`S/integrations/integrationshelpertest/`): `Contrato(t, func(t) Montaje)` sobre
`Store` + `Memoria` (doble con `sync.Mutex` y reloj inyectable): encolar devuelve id creciente;
reclamar un lote no devuelve filas ya reclamadas y vigentes; `MarkWebhookFailed` reprograma;
`RecoverOrphanDeliveries` devuelve a `pending` las reclamadas con lease vencido; secreto nunca se
devuelve en `GetTenantIntegration` (solo `HasSecret`). 🔶 afinar con `postgres_integration_test` (6),
`crud_integration_test`, `outbox_stats_integration_test`, `payload_purge_integration_test` (→F9).

`contrato_wapp_crm_v1_test.go` (D-F6-3): los 5 tests de `internal/contracts/contract_examples_test.go`
(ejemplos de `intake.push`, `intake.status`, `catalog.pull` contra su esquema; casos negativos de push
y de status; `external_ref` opcional; compilan como draft 2020-12), con
`const contractDir = "../../../../docs/contracts/wapp-crm-v1"` y `github.com/santhosh-tekuri/jsonschema/v6`
(solo en test).

### 2.5 · `S/integrations/crmpush` (2 · 18 exp.)

`push.go`: `Input`, `Item` (la forma **del contrato**, no `intakes.Item`), `Payload`, `Build`,
`Queuer`, `Gate`, `Pusher` (`now` inyectable, `:209`), `NewPusher`, `Result`. Reglas de los tests
viejos (`push_test`, 12): `Build` produce el documento del contrato; `LifecycleStatus` y `RevisionNo`
son **los del llamante**, nunca constantes; sin líneas → lista vacía (no `null`); no comparte la slice
del llamante; `event_history_id` omitido; no congela lo que rellena el worker; gate abierto encola
**una** vez; gate cerrado no encola y **no es error**. `desde_intakes.go`: `NewRevisionPusher`
(satisface `intakes.CRMPusher`) — tests `desde_intakes_test` (5) 🔶.

### 2.6 · `S/integrations/sigv1` y `S/tenantvars`

- `sigv1.go` (3 exp.): `Sign`, `SignatureHeader`, verificación HMAC-SHA256 sobre el cuerpo **crudo**,
  ventana ±300 s, comparación en **tiempo constante** (`:39`), reloj como parámetro. `sigv1_test` (7).
- `tenantvars.go` (2: `Variable`, puerto `Store` `:44`), `memory.go` (5, `SetClock`), `postgres.go`
  (4, `Replace` en transacción). Suite `tenantvarshelpertest.Contrato`: `Replace` sustituye el conjunto
  entero; `List` ordenado y por tenant. 🔶 `postgres_integration_test` (5).

## 3 · Dobles y suites (E-6)

Todo **puerto con BD** tiene su suite `Contrato(t, func(t) Montaje)` corrida **en memoria y en Postgres** con el
arnés de F9-A (P4): es lo que garantiza que memoria y Postgres se comportan igual. En F6 son tres. La marca de estado
vigila todas las columnas que la operación puede tocar (hallazgo 35).

| Puerto | Suite | Implementación en memoria | Postgres (arnés) |
|---|---|---|---|
| `intakes.Store` | `intakeshelpertest.Contrato` | `MemoryStore` (producción, `memory.go`) | `Postgres`, T9.27 |
| `integrations.Store` | `integrationshelpertest.Contrato` | `integrationshelpertest.Memoria` **nuevo** | `Postgres`, T9.27 |
| `tenantvars.Store` | `tenantvarshelpertest.Contrato` | `MemoryStore` (producción) | `Postgres`, T9.27 |
| Los demás puertos (notificador, emisores, lectores de P5, `Outbox`) | — | dobles locales del test del consumidor | — |

## 4 · Reglas E-8 que el contrato debe llevar (las no obvias)

| # | Regla | Origen |
|---|---|---|
| R-01 | INV-1: solo el POST de la dueña aprueba o pregunta; ningún camino automático (motor, carrito, pipeline, cola, el propio dominio) llama `Approve`/`RequestInfo` | `inv1_aprobar_ast_test.go:1-60`, `inv1_pedirinfo_ast_test.go` |
| R-02 | Aprobar sin `QuoteSender` corta con `ErrNoQuoteSender` en vez de confirmar dejando al cliente sin enterarse | `fase6_solicitudes.go:63-68` |
| R-03 | El `CRMPusher` del `Service` hoy **no empuja nada por sí solo**: solo encola quien llama a `PushRevisionToCRM`; el único productor de `intake.push` del pipeline normal es el sink del carrito | `fase6_solicitudes.go:69-81` |
| R-04 | Sin `WithMetrics`, el dominio funciona entero y **no publica nada** (no es error) | `fase6_solicitudes.go:100-104` |
| R-05 | Los dos recordatorios perezosos son **independientes**: `touch` pregunta por cada uno | `fase6_solicitudes.go:55-58` |
| R-06 | El plazo del presupuesto es `QuoteDeadline` (24 h), **constante de plataforma**; nunca `tenant_settings.order_ttl_seconds`; vencer **marca y avisa**, no mata ni emite evento de expiración | `inv_vencimiento_ast_test.go`, D-044.50 §1, ADR-0029 enm. 2 (fuera del repo: *«los objetos de negocio no mueren por tiempo, mueren por acción humana»*) |
| R-07 | La poda del literal **sella** su instante y lo devuelve: la lectura que poda no puede decir «nunca tuvo texto» | `sello_poda_ast_test.go:1-25` |
| R-08 | `SanitizeNote`: saltos y tabuladores (`\n \r \t \v \f U+2028 U+2029`) → un espacio; resto de controles C0/C1 fuera (0x00 revienta Postgres, SQLSTATE 22021); invisibles zero-width y bidi fuera; espacios colapsados y extremos recortados; **emojis se conservan**; se mide en **runas** y **después** de sanear; pasarse **no trunca** (el final es donde va el alérgeno): `NoteTooLongError`; vacío = «sin indicación», no error | `cart/note.go:1-100` |
| R-09 | Claves de estado y de wire en inglés; nombre de negocio solo en UI (I-CP-8) | `status.go:13` |
| R-10 | El literal va cifrado **por campo** dentro del payload de la revisión; la interpretación estructurada, en claro | `fase3_almacenes.go:124-135` |
| R-11 | Los datos del comprador los escribe **solo** `PostgresBuyerData` (el store normal no puede cifrar ni descifrar) | `fase3_almacenes.go:140-146` |
| R-12 | CRM: `RevisionNo`/`LifecycleStatus` nunca constantes (el puente hace UPSERT por `(intake_id, revision_no)`; un `1` fijo dejaba el CRM en el primer estado para siempre) | `crmpush/contrato_ast_test.go:12-38` |
| R-13 | Gate CRM cerrado no encola y no es error; un tenant no puede tener el puente abierto para una puerta y cerrado para otra (una sola evaluación de D-042.8) | `push_test`, `fase6_solicitudes.go:70-76` |
| R-14 | Worker: `claim_lost` cuando el lease del claim expiró antes de cerrar; cardinalidad de la métrica **fija** (4 valores), sin etiqueta de tenant | `platform/metrics/metrics.go:260-268` |
| R-15 | El callback no lleva JWT **a propósito**: se autentica por HMAC del cuerpo crudo con el secreto del tenant | `contratos.md` §2.7 |
| R-16 | 🔶 Reglas de `approve_contrato_test`, `correct_test`, `retencion_test`, `event_pair_*`, `tres_puertas_crm_test`, `quotetext/*` (nueve motivos de fallback) y `notifier_test` (plantillas) | lectura E-8 en T6.6–T6.12 |

## 5 · Textos observables (byte a byte)

- Estados: los 11 de R6.1.b y `closed` (alias). Etapas de revisión y `RevisionPayloadVersion = 1`.
- SKU reservados: `"_"` (prefijo), `"_shipping"`.
- Eventos: `intake_line_corrected`, `intake_approved`, `intake_info_requested`.
- `cart: la indicación mide %d runas y el máximo es %d` (D-F6-4).
- Métrica `wapp_webhook_deliveries_total` con `status ∈ {delivered, failed, dead, claim_lost}`.
- Cabeceras CRM: `X-Wapp-Tenant`, `X-Wapp-Timestamp`, `X-Wapp-Signature: v1=…`; `catalog.pull diferido` (422).
- 🔶 Todo `errors.New`/`fmt.Errorf` de los paquetes viejos que llegue a una respuesta HTTP o a un
  WhatsApp: inventario con `grep -rn 'errors.New("\|fmt.Errorf("' V --include='*.go' | grep -v _test`
  en T6.1, y cada uno con aserción literal en su test.

## 6 · Candados de invariante (`05` §3.2) — dónde quedan

| Candado viejo | Regla | Nuevo | Qué cambia |
|---|---|---|---|
| `intakes/inv1_aprobar_ast_test.go` (1 test) | R-01 (aprobar) | `S/intakes/inv1_aprobar_test.go` (AST permitido, E-7) | Control positivo `../../../apipublica` (exactamente 1). Directorios automáticos **en F6**: `../../../../flujos/runtime`, `../../../../flujos/modules/cart`, `../../../../intake`, `../../../../intake/pipeline`, `../../../../intake/stages`, `.` · **F7** sustituye los tres de `intake` por `../../captacion/{intake,pipeline,stages}` · **F8** los dos de `flujos` por `../../conversacion/{runtime,modules/cart}`. Guarda anti-hueco intacta (dir inexistente o vacío → `Fatalf`); no recursivo |
| `intakes/inv1_pedirinfo_ast_test.go` | R-01 (pedir info) | mismo fichero, `TestINV1_SoloElPOSTDelDueñoPregunta` | reusa el barrido (una sola lista de directorios) |
| `intakes/inv_vencimiento_ast_test.go` (2 tests) | R-06 | `S/intakes/vencimiento_test.go` (dos tests con nombre `Candado`) | `raízDelRepo = "../../../.."`; nombre del evento **por concatenación** (si se escribiera entero, el candado **viejo**, que barre el repo, se pondría rojo); control positivo `deposit_reminded_at` (texto) y `QuoteDeadline` (AST del paquete) |
| `intakes/sello_poda_ast_test.go` | R-07 | `S/intakes/postgres_test.go` | Nombres **no exportados** (`revisionsOf`, `ejecutarPoda`, `sellarPodada`): entra con el **verde** de `postgres.go` (T6.18), no en el rojo (patrón F1: el rojo solo lleva exportados; `05` E-4, P6: llevan test porque cargan regla de negocio, R-07) |
| `integrations/crmpush/contrato_ast_test.go` | R-12 | `S/integrations/crmpush/contrato_test.go` | `directoriosVigilados = {".", "../../../../flujos/runtime"}` hasta F8; en F8 `../../../conversacion/runtime`. Exige sitios en **cada** directorio |
| (BD) dos aprobaciones → un `intake_approved` | R-01 conducta | F9 P5 `TestP5_AprobarDosVecesUnSoloEfecto` (R9.6.c) | — |
