# F8 · Diseño — la vista micro, paquete a paquete

> `C` = `internal/modulos/conversacion`. «Exp.» = exportados medidos con el contador AST de §0.
> «Leer (E-8)» = tests viejos que se **leen** antes de escribir el contrato; no se portan. Las
> reglas marcadas con un ID (plan, hallazgo, INV, D-…) vienen de comentarios-ADR o de tests del
> código viejo y **tienen que** acabar en el comentario del contrato y en una aserción.

## 0 · Cómo se contó

- Exportados: un programa de 60 líneas sobre `go/parser` (funciones, métodos de tipos exportados,
  tipos, `var`/`const`) corrido fichero a fichero. Reprodúcelo en T8.1 (sale igual con
  `go doc -all` contado a mano, más lento).
- Líneas: `wc -l`. Tests: `grep -c '^func Test'` (primer nivel; `t.Run` aparte).
- Regla de F1 que aplica a todo el módulo: **el rojo lleva solo exportados**; los no exportados
  (la mayoría de la lógica de `runtime`, `cart`, `screens`, `troceo`…) nacen con su verde.
  Ficheros con **0 exportados** (`cart/{consulta,preresolutor,screens,troceo,variants}.go`,
  `runtime/{exit_menu,keyedmutex,send,streak,thread}.go`, `turnoacotado/prompt.go`, `admin/doc.go`):
  en el rojo nacen como fichero con **solo** el comentario de paquete o de cabecera y su
  `_test.go` rojo que llama a la API del paquete que los ejercita; el candado
  `un_fichero_un_test` exige el test, `exportados_cubiertos` no tiene nada que exigir. Su test
  nombra los no exportados en el verde (E-7: se prueban los no exportados que los hermanos usan).

## 1 · Hojas: `model`, `trigger`, `content`, `store`, `modules`

### 1.1 · `C/model` ← `internal/flujos/model` (si D-F8-1 no lo hizo F5)

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `model.go` | 406 | 24 | `Flow`/`Node`/`Conversation`, `Content{Raw}`/`ContentItem`/`ContentRef`, `ParseAndValidate`, `NodeTerminal`, `ErrInvalidFlow` |

Reglas: `ErrInvalidFlow` = `errors.New("definición de flujo inválida")` (`model.go:87`); **todo**
error de definición la envuelve con `%w` (`:304-336`: JSON mal formado, `flow_id vacío`,
`version %d inválida (debe ser >= 1)`, `nodes vacío`…) y se inspecciona con `errors.Is`.
`Conversation.Finished()` ⇔ `CurrentNode == NodeTerminal`. Leer: `model_test.go` (4 · 8 `t.Run`).

### 1.2 · `C/trigger` ← `internal/flujos/trigger`

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `trigger.go` | 257 | 33 | Tipos de regla (`keyword`, `fallback`, `event_start`, `event_stop`, `escape`, `llm`), `Signal`, `Resolver` (`:211`), normalización del texto (NFC, `golang.org/x/text/unicode/norm`) |
| `config_resolver.go` | 375 | 5 | `ConfigResolver` sobre `Store`: `Resolve` e `IsEscape` |
| `store.go` | 36 | 2 | **Puerto**: `Store` + `ErrTriggerNotFound` = `"regla de disparo no encontrada"` (`:10`) |
| `store_memory.go` | 91 | 7 | Gemelo en memoria (`sync.Mutex`) |
| `store_postgres.go` | 172 | 7 | Adaptador `flow_triggers` (fuera del 80 %) |

Suite: **`C/trigger/triggerhelpertest.Contrato(t, func(t) trigger.Store)`**, la corren `store_memory_test`
(unitario) y el proceso de F9 contra Postgres. Reglas: 🔴 `KindLLM` (`trigger.go:45`) **no puede
disparar en producción** (`Signal.Intent` siempre `nil`, deuda D-5, `runtime/incoming.go:963`): se
conserva la rama y su test, **no se arregla**. INV-6: sin resolver real (`NoopResolver`) el
comportamiento es el previo al Plan 019. Leer: `config_resolver_test.go`, `trigger_test.go`,
`store_postgres_test.go` (5 · 52).

### 1.3 · `C/content` ← `internal/flujos/content`

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `content.go` | 30 | 1 | **Puerto** `Source` (resolver el contenido de un nodo) |
| `static.go` | 22 | 3 | Fuente pura (menú/encuesta sin `content`) |
| `json.go` | 81 | 4 | Fuente `tenant_content` por `ref`; errores envueltos en `ErrInvalidFlow` («el adapter json exige node.content con una ref», «…una ref no vacía», «leer contenido %q del tenant», «blob de contenido %q mal formado», `json.go:47-61`) |
| `router.go` | 46 | 3 | Enruta **por nodo** entre static y json; fuente desconocida → `"%w: content source %q no soportado"` (`:44`). El switch por fuente vive **solo** aquí |

Suite `C/content/contenthelpertest.Contrato` opcional (tres implementaciones pequeñas): recomendación,
test directo por fichero. Leer: 3 · 11.

### 1.4 · `C/store` ← `internal/flujos/store`

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `store.go` | 875 | 42 | Tipos (`Key`, `FlowEvent`, `Intake`, `TenantContentSummary`, `VersionSourceImportJSON/Tabular`, `DefaultConversationTTL`…), centinelas (`ErrTenantContentNotFound`…) y **13 interfaces** segregadas (`ConversationStore :43`, `DefinitionReader :59`, `DefinitionStore :71`, `SurveyResultStore :81`, `FlowEventStore :116`, `TenantContentReader :124`, `IntakeReader :133`, `IntakeWriter :182`, `IntakeStore :236`, `TenantSettingsReader :242`, `WelcomeStore :280`, `Repository :305`, `TenantContentVersioner :406`) |
| `repository_memory.go` | 817 | 37 | Gemelo en memoria (`sync.Mutex`, `time.Now()` en 8 sitios: **inyectar reloj** en el nuevo) |
| `repository_postgres.go` | 1.074 | 28 | Adaptador (fuera del 80 %); 4 `rows.Close` rituales (D-17, se portan igual, D-F8-6) |

Suite: **`C/store/storehelpertest.Contrato(t, func(t) store.Repository)`** — la más grande del módulo
(conversación, definiciones, `flow_events`, `tenant_content` con versiones, solicitudes abiertas,
ajustes, bienvenidas). Reglas: ISP del runtime (H12, Plan 027 · Ola 2 · T9): el runtime pide
`FlowStore` = `ConversationStore + DefinitionReader + IntakeReader + TenantSettingsReader`; un
tenant sin fila en `tenant_settings` hereda TTL **2 h** (`DefaultConversationTTL`, Plan 046 ·
T4.4), no cero. `flow_events.name` es TEXT libre (migración 0009). Leer: los 18 (12 de integración
son el guion de la suite contra Postgres en F9).

### 1.5 · `C/modules` ← `internal/flujos/modules`

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `registry.go` | 346 | 19 | `Registry` (`sync.RWMutex`, `:271`), `Module` (`:191`), `MediaEmitter` (`:240`), `Primer` (`:254`), `NodeValidator` (`:265`), `Result`, `Effect` |
| `ports.go` | 58 | 3 | `EffectMeta`, `Projector` (`:30`), `ResumePolicy` (`:48`, H9 Plan 027 · Ola 3 · T8) |
| `numbered.go` | 261 | 18 | Menú numerado común (reprompt máx. 3, H12) |
| `consulta.go` | 244 | 16 | Petición de consulta al resolutor (Plan 044 · Ola 3.5) |
| `coerce.go` | 45 | 3 | Coerciones de payload |

Leer: 4 · 23.

## 2 · El motor y sus satélites

### 2.1 · `C/engine` ← `internal/flujos/engine`

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `engine.go` | 443 | 12 | Núcleo **puro** (sin BD, sin transporte, sin logger); errores envueltos en `ErrInvalidFlow`: nodo inexistente, «no espera entrada», tipo desconocido, resolver contenido, emitir media, **cadena demasiado larga (¿ciclo?)** (`engine.go:286-431`) |
| `consulta.go` | 218 | 10 | `ConsultaResolver` (`:49`), `ObservadorConsulta`, desenlaces (`DesenlaceResuelto`…); el observador recibe 3 argumentos de **cardinalidad acotada**, nunca texto del cliente |

Reglas: re-entry de consultas (Plan 044 · Ola 3.5 · T3.5-2): sin resolutor → desenlace
«sin_resolutor» y el módulo repromptea. Leer: 9 · 31 (`engine_generic_test`, `engine_media_seam_test`,
`engine_module_terminal_test` con DSN).

### 2.2 · `C/modules/menu`, `survey`, `media`

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `menu/menu.go` | 67 | 9 | Módulo menú numerado |
| `survey/survey.go` | 109 | 9 | Pregunta y respuesta |
| `survey/projection.go` | 57 | 6 | Proyector a `survey_results`; efecto `EffectSurveyAnswer = "survey_answer"` (`:13`) |
| `media/media.go` | 106 | 11 | Envío de PDF/imagen que **no** espera input; `KindDocument="document"`, `KindImage="image"`; descriptor incompleto → error envuelto en `ErrInvalidFlow`: «nodo media sin content…», «sin key», «sin filename», «sin mime», «con kind %q inválido (document\|image)» (`media.go:86-103`) |

Leer: 1 · 4, 2 · 10, 1 · 3.

### 2.3 · `C/turnoacotado` ← `internal/turnoacotado`

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `turnoacotado.go` | 229 | 6 | `Resolver` (tercer escalón del carrito: código exacto → cascada determinista → **modelo del tenant**), `Turnero` (`:71`), `New(selector)` |
| `troceado.go` | 160 | 3 | Troceado de la consulta para el modelo |
| `prompt.go` | 235 | 0 | Texto del prompt: `"Elegí una opción:"`, `"¿Cuántas unidades querés?"` (`:177-178`), `"Opciones ofrecidas: (ninguna, es una pregunta abierta de cantidad)"` (`:183`) |

Importa `modulos/inferencia/llmvia` (única vía, I-CP-3) y `wapp-shared/llm`. Leer: 2 · 16. Candado de
cableado: `turno_acotado_cableado_test.go` (arranque).

### 2.4 · `C/events` ← `internal/flujos/events`

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `events.go` | 296 | 25 | `Event`, estados `open`/`closed`/`cancelled`, `Alive()`, `ListFilter`, `IsStatus`, `IsContentFilter`, `DefaultPageSize`, `Rescuable`, tipos de entrada del hilo (`summary`, `message`, `decision`, `message_out_of_turn`, `:113-148`) |
| `kinds.go` | 63 | 4 | `RuleLister` (`:15`), `KindFeatures()`, `AllowedKinds` |
| `dispatcher.go` | 425 | 12 | `Dispatcher` **solo lee** (eventos vivos, tipos que ofrece el tenant, features); `RescuableLister :37`, `KindOffer :48`; INV-20 |
| `menu.go` | 392 | 14 | Menú de eventos: `menuHeader`, `menuFooter`, `rescueHeader` (§5) |
| `summary.go` | 593 | 19 | Resumen durable del evento abandonado: `IntakeLineReader :220`, `SurveyAnswerReader :254`, `SummaryAppender :525`, `BuildCartSummary`, `Render` |
| `thread_reader.go` | 298 | 8 | `ListThread`, `ThreadEntry`, `KindMessage` (descifra con `FieldCipher`) |
| `store.go` | 1.060 | 32 | **Adaptador Postgres sin gemelo** (`Store{db, cipher, now}`, `WithClock :43`); escribe `conversation_event_messages` cifrada (`:811`) — 🔴 fuera del censo `rekeyTargets` (deuda D-1): **no se arregla aquí** |

Doble nuevo: **`C/events/eventshelpertest`** con un `Store` en memoria que satisfaga los puertos que
consumen `runtime` (`EventStore`, `SummaryAppender`, `ThreadReader`), `apipublica` y
`captacion/reanalisis` (contradicción 2 del README). Reglas (de `summary_test.go`, `dispatcher_test.go`):
`LoadSummary` **devuelve error** si falta el lector del tipo (no inventa un resumen vacío);
`PersistSummary` escribe **una** fila y un segundo abandono **no pisa** al primero; vacío no se
resume; el render habla de «pedido» y **nunca** de identificadores; el resumen es una **foto**
(no comparte memoria con el estado); **INV-13**: la indicación va bajo su línea (`"\n   ✏️ sin azúcar\n"`)
y el `TOTAL` sale solo de `qty × unit_price` (`"TOTAL  $5.00"`); la estructura se guarda en claro
**sin total derivado**. `var _ events.SummaryAppender = (*events.Store)(nil)` se conserva como
aserción de compilación. REQ-21 (sin clasificador) → `fronteras_test.go` (D-F8-4). Leer: 14 · 140.

### 2.5 · `C/admin` ← `internal/flujos/admin` (sin `sessions.go`)

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `doc.go` | 5 | 0 | Comentario de paquete (excepción E-3) |
| `handlers.go` | 362 | 6 | `DefinitionHandler(store, modules)`, `StartHandler(starter)`, `DefinitionStore :32`, `ModuleTypeSource :41`, `Starter :49`; `msgStreamCaidoStart` (`:272`); mapeo de `ErrSessionOffline` (`:326`, FX §4.4). **Sin `Register`** (D-F8-2) |
| `triggers.go` | 578 | 4 | `CreateTriggerHandler`/`ListTriggersHandler`/`DeleteTriggerHandler(store, durable)`, `TriggerStore :80` |
| `durable_flow.go` | 98 | 6 | `NewEngineDurableFlowChecker(store, engine)`: «¿el flujo de esta regla tiene contenido durable?» en tiempo de configuración (Plan 054 · F3, D-054.6/8); parámetro **posicional** de los tres CRUD |

Reglas: textos de error HTTP literales (leer `handlers_test.go`, `triggers_test.go`; 7 · 79).
`writeJSON` duplicado (D-16) se porta igual.

## 3 · `C/modules/cart` ← `internal/flujos/modules/cart` (14 ficheros)

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `cart.go` | 849 | 11 | `Module`, `New(WithLogger, WithMatchHook)`, `NodeTypeCart`, `Step`; `catalogUnavailable` (§5) |
| `troceo.go` | 536 | 0 | Troceo de la entrada del cliente |
| `projection.go` | 520 | 8 | `NewProjector(store, intakes, revisions, buyer)`, `ProjectionStore :32`, `RevisionWriter :53`, `ShippingEnsurer :64`, `BuyerDataWriter :79`; `postgres.IsUniqueViolation` (`:243`) |
| `preresolutor.go` | 448 | 0 | Cascada determinista (exact\|fuzzy\|ninguno → `wapp_cart_match_total`) |
| `screens.go` | 339 | 0 | Pantallas: `"(sin descripción)"` (`:42`), `"✏️ Escribe la indicación para 1 de las N \"…\"."` (`:197`) |
| `prime.go` | 278 | 1 | Siembra (`Primer`) |
| `state.go` | 270 | 16 | Estado del carrito en `Vars`; `variantLabelSep = " — "` (`:57`) |
| `effects.go` | 232 | 9 | Efectos: `cart_started`, `category_selected`, `item_viewed`, `item_added`, `cart_closed`, `cart_cancelled`, `note_added`, `buyer_data_captured`, `cart_expired` (`:14-36`) |
| `revalidate.go` | 195 | 2 | `PriceListOf(cat catalogo.Catalog) intakes.PriceList` — **se queda aquí** (`04` §5.2) |
| `buyer.go` | 188 | 1 | Captura de datos del comprador; `buyerRequiredMsg` (`:155`) |
| `consulta.go` | 172 | 0 | Petición de consulta (re-entry) |
| `variants.go` | 90 | 0 | Variantes |
| `resume.go` | 79 | 5 | `NewResumePolicy(store)`, `ResumeStore :19` (H9) |
| `validate.go` | 35 | 2 | `NodeValidator` del carrito |

Reglas: 🔴 **candado de orden** (§4.2); `ParseCatalog` y tipos vienen de `modulos/catalogo`,
`SanitizeNote`/`MaxNoteRunes`/`NoteTooLongError` de `modulos/solicitudes/intakes`; H24 (el segundo
pedido tras confirmar tiene su **propio** evento `cart` y su **propia** solicitud abierta), H29
(cancelar **dentro** del flujo deja el evento `cancelled`, no `closed`; el efecto sigue al estado),
ola6 (confirmar fija `CurrentNode = model.NodeTerminal` y el siguiente entrante **no** puede quedar
mudo), 054 #001/#003 (un flujo de solo `cart` por `keyword` no puede perder la comanda contra el
`NOT NULL` de `intakes.event_id`); goldens (D-F8-3). Leer: los 22 de conversación (de 25) · 176,
sobre todo `cart_test`, `consulta_test`, `preresolutor_test`, `projection_*`, `cart_fin_de_flujo_test`,
`revision_no_anotado_test`, `variants_test`, `golden_test`.

## 4 · `C/runtime` ← `internal/flujos/runtime` (23 ficheros)

| Fichero | Líneas | Exp. | Qué promete |
|---|---:|---:|---|
| `runtime.go` | 118 | 5 | Puertos `Sender :21`, `Presigner :34`, `TenantResolver :77`, `SelfNumberChecker :99`, `IngestDeduper :116`; perfiles `active`/`passive`; motivos `passive`·`self_loop`·`rate_limit`·`saturation` (cardinalidad fija 4) |
| `keyedmutex.go` | 55 | 0 | Single-flight por `store.Key` con refcount; borra la entrada al llegar a 0 |
| `event_sink.go` | 130 | 6 | `EffectContext`, `EventSink`, `SinkPhase`, `PhasedSink`; el fan-out se ordena por fase |
| `log_sink.go` | 39 | 3 | Sink por defecto |
| `tenant_resolver.go` | 106 | 4 | `PostgresTenantResolver`: tenant **y** perfil en **una** consulta; perfil vacío/desconocido = activo |
| `self_numbers.go` | 165 | 4 | `PostgresSelfNumbers`: predicado por **índice ciego** (Plan 046 · T4.1) con el **mismo** `KeyProvider` que escribe `fleet_sessions` |
| `summary_sources.go` | 116 | 2 | `NewSummarySources(store)`, `SummaryStore :13` |
| `streak.go` | 354 | 0 | Rachas (Plan 049 · Opción A): observa, **nunca** decide |
| `welcome.go` | 328 | 2 | Bienvenida única (Plan 044 · T1.8-2), `WelcomeStore :107` |
| `thread.go` | 270 | 0 | Hilo del evento (productor `message`), gate `llm_intake` |
| `send.go` | 247 | 0 | Envío texto/media; presigner nil → error controlado |
| `webhook_sink.go` | 184 | 6 | `NewWebhookSink(log, EffectCartClosed, store, gate)`: **solo encola** (INV-02), `PhaseNotify`, lee `intake_id` del payload |
| `persist_sink.go` | 227 | 6 | `NewPersistSink(store, projectors…)`, `WithDecisionThread`, `DecisionAppender :25` |
| `event_effects.go` | 160 | 10 | Efectos `event_started`, `event_switched`, `event_deactivated`, `event_inactivity_expired`, `event_closed`, `event_cancelled`, `event_escaped` (`:26-40`), `kind = "event"` (`:78`) |
| `source_composer.go` | 400 | 11 | `NewSourceTextComposer(log, events, jobs, cipher)`, `DefaultThreadLimit`, `ThreadReader :270`, `SourceTextWriter :277`; cabeceras del sobre (§5); O5: un solo volumen |
| `aggregator.go` | 987 | 20 | `IntakeAggregator` (ver reglas) |
| `runtime_engine.go` | 590 | 28 | `Runtime`, `New`, **22** `With*`, `FlowStore :45`, `DepositReminder :272`, `ReplyLimiter :281`, `MaxAutoreplyStreak :585` |
| `resume.go` | 354 | 1 | Políticas de reanudación (H9); reintento acotado D-054.4 (`time.After :351`, `postgres.IsPermanentFailure :319`) |
| `start.go` | 408 | 3 | `Start(ctx, tenant, flow, session, ref)` (API); `ErrConversationExists`, `ErrDurableFlowNeedsEvent` |
| `exit_menu.go` | 136 | 0 | Menú de salida |
| `event_lifecycle.go` | 437 | 3 | `GetEventForTenant`, `CancelEventForTenant`, `ErrNoEventPlane` |
| `events.go` | 1.501 | 6 | Plano de eventos (Plan 043): `EventStore :28`, `IntakeAbandoner :108`, `Dispatcher :120`, `OpeningBuilder :148`, `FlowForKind :169`, `StartNewOfKind :372` |
| `incoming.go` | 1.328 | 2 | `OnIncoming`, `HandleIncoming`; `defaultEscapeMessage`, `defaultDurableSinkFailureNotice` |

Dobles nuevos en **`C/runtime/runtimehelpertest`**: `Sender`, `Presigner`, `TenantResolver`,
`SelfNumberChecker`, `IngestDeduper`, `ReplyLimiter`, `DepositReminder` y los dos adaptadores sin
gemelo (`PostgresTenantResolver`, `PostgresSelfNumbers`). Reloj: **siempre** `WithClock` y
`WithAggregatorClock`; los dos relojes de un guion (runtime y `events.WithClock`) sobre la **misma**
función movible (precedente `event_clock_test.go`); 🚫 `time.Sleep`.

### 4.1 · Reglas del runtime que el contrato tiene que llevar (E-8)

| # | Regla | Origen |
|---|---|---|
| RT-1 | `OnIncoming` **no bloquea** el bucle `Recv`: lanza una goroutine con `context.WithTimeout(Background, incomingTimeout)`; procesar inline haría deadlock con el Ack | `incoming.go:34-50` |
| RT-2 | Semáforo: sin cupo dentro del plazo → se descarta, se cuenta `saturation` y WARN **sin PII** | H5 (Plan 027 · Ola 1 · T5), `incoming.go:59-72` |
| RT-3 | `incomingTimeout` ≤ 0 → 30 s; `MaxConcurrentIncoming` 0 → 64, < 0 → sin techo | H1, `runtime_engine.go:27-37` |
| RT-4 | **Save antes de Send**; un output de media con presigner nil → error controlado con el estado ya guardado | design §6, `runtime_engine.go` (campo `presigner`) |
| RT-5 | Dedupe persistente `Seen(session_id, wa_message_id)` **antes** de tocar el motor; nil → solo la consecutiva por `last_wa_message_id` | Plan 028 · T6 |
| RT-6 | Perfil `passive`: no dispara ni auto-responde; se cuenta `passive`; INFO **una vez por sesión**, luego Debug | Plan 020 · T1, Plan 046 |
| RT-7 | Anti-self-loop por índice ciego con el `KeyProvider` **compartido**; número ya normalizado (`contact.Normalize`, `KindPhoneE164`) | Plan 020 · T2, Plan 046 · T4.1 |
| RT-8 | Un token del limitador **antes de cada** auto-envío (arranque, avance, aviso de escape, reinicio de carrito); agotado → no responde, `rate_limit` | Plan 020 · T0 |
| RT-9 | Escape: si la regla trae `message`, se usa; si no, `defaultEscapeMessage` | Plan 019 · T4/T4b |
| RT-10 | Sink durable agotado → se **corta el turno** y el cliente recibe `defaultDurableSinkFailureNotice`; nunca un SQLSTATE | D-054.4 (Plan 054 · T3) |
| RT-11 | Todo `store.Delete` cierra la racha (**6** caminos: `events.go:716,1221`, `incoming.go:239,450,489,1077`); racha = emisiones de `send`, un entrante **no** la reinicia; cierre por inactividad 30 min; `Max` barre las vencidas **en el scrape** | Plan 049 · Opción A |
| RT-12 | Opciones nil = no-regresión total (INV-6); `WithOpeningBuilder` sustituye **solo** al texto del `fallback` (INV-20) | Plan 019, Plan 043 · T3.8 |
| RT-13 | `CancelEventForTenant`: terminal → `repairCancelled` sin tocar `closed_at`; vivo → lock → transición → abandono → puntero; `ErrNotOpen` = carrera benigna → re-leer | Plan 043 · T4.2/T4.3, E-8 §4 |
| RT-14 | T62: cancelar desde la app emite `event_cancelled` en `flow_events` y el cliente puede abrir un carrito **nuevo** en el acto | e2e `conversationeventcancel_t62_…` |
| RT-15 | T61: `events.WithClock` gobierna `IsSuspended`, `Touch` y `closed_at`; `runtime.WithClock` solo `conversationExpired` | e2e `eventclock_t61_…` |
| RT-16 | O5 (Plan 053): el dueño del estado (`owner_event_id`) y el evento al que habla el contacto son dos cosas; un `cart` heredado bajo un `menu` activo **cierra su propio evento** (INV-053.2) | e2e `ownerclose_o5_…` |
| RT-17 | W45: la cadena entera (trigger → evento → engine → sink → proyector) funciona **sin sembrar** ligaduras a mano | e2e `conversationchain_w45_…` |
| RT-18 | Sinks ordenados por fase: `PersistSink` antes que `WebhookSink` (`PhaseNotify`); el orden de las líneas del arranque no es load-bearing | Plan 042 · Ola 3.1 |
| RT-19 | `DepositReminder.RemindContact` devuelve los textos enviados; el runtime los escribe en el hilo si el tenant tiene `llm_intake` | Plan 041 · T4.4, D-044.24 |
| RT-20 | Hilo (`message`) y bienvenida solo con `llm_intake` (fail-closed con resolver nil) | Plan 044 · T1.6, T1.8-2 |
| AG-1 | `Observe`: **1** sentencia SQL (`OpenOrAppend`), **0** `SELECT`, **0** cripto, **0** red; guardas baratas primero | D-044.26 |
| AG-2 | `Observe` no devuelve error; un fallo se loguea y el turno sigue | INV-10 |
| AG-3 | La ventana de silencio es el camino **principal**; el intent (`"intake_request"`, confianza ≥ 0,7) solo adelanta; peor caso = `aggregation_max_seconds` (120 s) + pipeline | T1.7, T1.8-1 |
| AG-4 | `hintDueNow`: anotar bajo candado **y después** avisar (no bloqueante, buffer 1) | T1.8-1 (h) |
| AG-5 | `seen` **no** se borra al cerrar la ventana (una re-entrega tras el flush no reabre); un mensaje distinto sí abre la siguiente | T1.7 (b) |
| AG-6 | Sin `time.AfterFunc` por ventana: el plazo se recalcula en cada barrido desde `message_ts`; `RecoverAtBoot` = un `Sweep`; cierre idempotente | T1.1 |
| AG-7 | Tres llamantes de `observeForAggregation`: turno normal, mensaje que **arranca** el evento, reinicio por reanudación | `aggregator.go:962` |
| AG-8 | Sin `WithSourceComposer` → noop (jobs sin texto); sin `WithAheadRequester` → siempre por reloj | T1.4, T1.6-4 |

Leer (E-8): los 75 · 381. Los imprescindibles por regla: `aggregator_test` (1.993 l),
`aggregator_arranque_test`, `ingest_dedupe_test`, `passive_guard_test`, `self_loop_guard_test`,
`reply_limiter_test`, `saturacion_contador_test`, `racha_autorespuestas_test`, `streak_test`,
`escape_*`, `durable_*`, `event_*`, `opening_fallback_test`, `poison_event_id_leak_test`,
`resume_summary_test`, `welcome_test`, `thread_*`, `source_composer_*`, `webhook_sink_*`,
`cart_cancel_outcome_test` (H29), `start_no_reinicia_test`, `llm_caida_best_effort_test`,
`inv10_worker_pipeline_test`, `buyer_data_leak_test`, `customer_note_leak_test`, `webhook_sink_pii_test`
(PII: ni nota ni datos del comprador salen por log o webhook).

## 4.2 · Candados de invariante (`05` §3.2)

| Candado viejo | Regla | Dónde queda | ¿BD? |
|---|---|---|---|
| `runtime/streak_invariante_test.go` (AST, 185 l) | Todo `rt.store.Delete` va seguido, en el mismo bloque y a ≤ 3 sentencias, de `rt.autoreplyStreaks.Close`; exactamente **6** `Delete` (si cambia, alguien lo mira) | `C/runtime/streak_invariante_test.go` — excepción AST permitida; **re-medir** la constante sobre el código nuevo | No |
| `modules/cart/orden_consulta_ast_test.go` (AST, 168 l) | En `Module.Step`: `preresolveOConsulta` → `return` con la petición → `st.Started = true` → `advance(`, **una** de cada | `C/modules/cart/orden_consulta_ast_test.go` | No |
| `events/summary_test.go:784` (imports) | `events` no importa rutas con `intent`, `llm`, `ollama`, `openai`, `anthropic`, `clasific`, `classif` (REQ-21) | `internal/modulos/fronteras_test.go` (D-F8-4) | No |
| `events/summary_test.go:580` (conducta) | INV-13 | `C/events/summary_test.go` | No |
| D-054.4, `IsUniqueViolation` del proyector, `ON CONFLICT` de la ventana, 23502 de 054 #001 | Necesitan Postgres | Procesos F9: «Entrante a respuesta», «De mensaje a borrador» | **Sí** |

Candados de **cableado** del arranque que tocan esta fase (los porta F0; F8 cambia el paquete que
cablean): `flow_options_cableadas_test.go`, `turno_acotado_cableado_test.go`,
`send_budget_cableado_test.go` (sin leer: confirmar en T8.1 si es de F3 o de F8).

## 5 · Textos y nombres observables (byte a byte)

| Literal | Dónde hoy |
|---|---|
| `"Listo, cerramos esto. Escribe una palabra clave cuando quieras empezar de nuevo."` | `runtime/incoming.go:22` |
| `"No pudimos registrar tu pedido. Por favor, intenta de nuevo en unos minutos."` | `runtime/incoming.go:32` |
| `"### CONTEXTO PREVIO — NO es lo que el cliente está pidiendo ###"`, `"### FIN DEL CONTEXTO PREVIO ###"`, `"### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###"`, `"### FIN DE LOS MENSAJES ###"` | `runtime/source_composer.go:92-97` (entrada de P2) |
| `"¿Qué quieres hacer? Responde con el número de la opción:"` · `"Si prefieres otra cosa, escríbelo y te ayudamos."` · `rescueHeader` (`"Pasó un rato sin novedades, …"`) | `events/menu.go:250,254,331` |
| `"El catálogo no está disponible en este momento. Intenta más tarde."` | `modules/cart/cart.go:41` |
| `"Necesitamos ese dato para completar tu pedido."` | `modules/cart/buyer.go:155` |
| Pantallas del carrito | `testdata/cart_v1_transcript.golden.txt` (285 l), `cart_v2_…` (136 l) |
| `msgStreamCaidoStart` | `admin/handlers.go:272` |
| `"regla de disparo no encontrada"` · `"definición de flujo inválida"` | `trigger/store.go:10` · `model/model.go:87` |
| Efectos y `flow_events` | §3 (`effects.go:14-36`), §4 (`event_effects.go:26-40`), `survey_answer` |
| Métricas (vía hooks; el módulo **no** importa prometheus) | `wapp_flow_reactive_blocked_total{reason=passive\|self_loop\|rate_limit\|saturation}`, `wapp_flow_autoreply_streak`, `wapp_flow_autoreply_streak_max`, `wapp_cart_match_total`, `wapp_flow_event_lifecycle_total` (el colector lee los `flow_events` `kind='event'`) |
| Logs de campo | `"flujos: consulta resuelta"` / `"flujos: consulta NO resuelta"` (arranque), `"runtime: entrante descartado por saturación (sin cupo en el pool a tiempo)"` |
