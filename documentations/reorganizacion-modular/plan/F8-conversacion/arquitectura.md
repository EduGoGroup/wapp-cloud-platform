# F8 · Arquitectura — la vista macro

> `C` = `internal/modulos/conversacion`. Todo medido sobre `dev` @ `1b18932` (2026-09-28) salvo
> donde dice «sin medir». Imports con
> `GOWORK=off go list -f '{{.ImportPath}}|{{join .Imports ","}}' ./internal/flujos/... ./internal/turnoacotado/...`
> y, por fichero, con el bloque `import` de cada `.go`.

## 1 · Paquetes viejos → nuevos (aplanado, D-3)

| Paquete viejo | Paquete nuevo | Prod | Líneas | Exp. | Tests viejos (fich. · `Test*`) | Integración vieja (se salta sin `WAPP_TEST_DB_DSN`) |
|---|---|---:|---:|---:|---|---|
| `internal/flujos/model` | `C/model` (**o F5**, D-F8-1) | 1 | 406 | 24 | 1 · 4 | — |
| `internal/flujos/trigger` | `C/trigger` | 5 | 931 | 54 | 5 · 52 | `store_postgres_test.go` |
| `internal/flujos/content` | `C/content` | 4 | 179 | 11 | 3 · 11 | — |
| `internal/flujos/store` | `C/store` | 3 | 2.766 | 107 | 18 · 71 | 12 `*integration*` (`integration_test.go`, `aggregation_window_…`, `engine_json_…`, `owner_event_id_…`, `tenant_content_…`, `welcome_…`, …) |
| `internal/flujos/modules` | `C/modules` | 5 | 954 | 59 | 4 · 23 | — |
| `internal/flujos/engine` | `C/engine` | 2 | 661 | 22 | 9 · 31 | `engine_module_terminal_test.go` (usa DSN) |
| `internal/flujos/modules/menu` | `C/modules/menu` | 1 | 67 | 9 | 1 · 4 | — |
| `internal/flujos/modules/survey` | `C/modules/survey` | 2 | 166 | 15 | 2 · 10 | — |
| `internal/flujos/modules/media` | `C/modules/media` | 1 | 106 | 11 | 1 · 3 | — |
| `internal/turnoacotado` | `C/turnoacotado` | 3 | 624 | 9 | 2 · 16 | — |
| `internal/flujos/events` | `C/events` | 7 | 3.127 | 114 | 14 · 140 | 5 `*integration*` + `pasted_internal_test.go` |
| `internal/flujos/admin` (sin `sessions.go`, D-FX-2) | `C/admin` | 4 | 1.043 | 16 | 7 · 79 (de ellos, los de `sessions` se leen en F3) | — |
| `internal/flujos/modules/cart` (sin `catalog.go`, `note.go`) | `C/modules/cart` | 14 | 4.231 | 55 | 25 · 176 (`catalog_test`, `catalog_v2_test`, `notes_test` son de F5/F6) | `projection_integration_test.go`, `cart_fin_de_flujo_test.go` |
| `internal/flujos/runtime` | `C/runtime` | 23 | 8.640 | 122 | 75 · 381 | 7 `*integration*` + `cart_persist_sink_test.go`, `persist_sink_test.go` |
| **Total** | | **75** | **23.901** | **628** | **167 · 1.001** | 25 `*integration*` + 6 con DSN sin ese nombre |

Conteos: `ls`, `wc -l`, `grep -c '^func Test'`, contador AST de exportados ([`diseno.md`](diseno.md)
§0); integración: `grep -l 'WAPP_TEST_DB_DSN' …/*_test.go` y `ls …/*integration*_test.go`.
Además, **13 e2e** de `internal/publicapi/*e2e*_integration_test.go` recorren este módulo de punta a
punta (H24, H29, W45, T61, T62, O5 ×2, 054, ola6…): son material de lectura (E-8) y el guion de los
procesos de F9.

## 2 · Orden interno (hojas primero)

Grafo de imports **dentro** del módulo (producción):

```
model ← (nada interno)            trigger ← (nada interno)
content → model                   store → model
modules → model                   engine → content, model, modules
menu, media → model, modules      survey → model, modules, store
turnoacotado → modules (+ inferencia/llmvia)
events → trigger (+ acceso/entitlements)
cart → model, modules, store (+ catalogo, solicitudes/intakes)
admin → model, runtime, store, trigger (+ nucleo/contact, edge/session)
runtime → contact*, engine, events, model, modules, store, trigger (+ acceso, captacion/intake, solicitudes/integrations/crmpush)
```

Orden de la pasada de contratos **y** de la de verde: `model` → `trigger` → `content` → `store` →
`modules` → `engine` → `menu`·`survey`·`media`·`turnoacotado` → `events` → `cart` → `runtime` →
`admin`. Dentro de `runtime`, de hojas a raíz (§3 de [`diseno.md`](diseno.md)): `runtime.go`,
`keyedmutex.go`, `event_sink.go`, `log_sink.go`, `tenant_resolver.go`, `self_numbers.go`,
`summary_sources.go`, `streak.go`, `welcome.go`, `thread.go`, `send.go`, `webhook_sink.go`,
`persist_sink.go`, `event_effects.go`, `source_composer.go`, `aggregator.go`, `runtime_engine.go`,
`resume.go`, `start.go`, `exit_menu.go`, `event_lifecycle.go`, `events.go`, `incoming.go`.

## 3 · Imports hacia fuera del módulo — todos a módulos ya reconstruidos

| Paquete nuevo | Importa (fuera de `C`) | Clase |
|---|---|---|
| `C/store`, `C/events`, `C/runtime`, `C/modules/cart` | `internal/platform/storage/postgres`, `internal/platform/crypto` | `platform` ✔ |
| `C/admin` | `internal/platform/httpapi` | `platform` ✔ |
| `C/runtime`, `C/admin` | `internal/nucleo/contact` (F1) | `nucleo` ✔ |
| `C/events` (`dispatcher.go`, `store.go`), `C/runtime` (`aggregator.go`, `runtime_engine.go`, `thread.go`, `welcome.go`) | `internal/modulos/acceso/entitlements` (F2) | módulo anterior ✔ |
| `C/admin/handlers.go` | `internal/modulos/edge/session` (F3; `ErrSessionOffline`) | módulo anterior ✔ |
| `C/turnoacotado` (`troceado.go`, `turnoacotado.go`) | `internal/modulos/inferencia/llmvia` (F4) | módulo anterior ✔ |
| `C/modules/cart` (`cart.go`, `prime.go`, `validate.go`, …) | `internal/modulos/catalogo` (F5: `Catalog`, `Article`, `Category`, `Variant`, `ParseCatalog`…) | módulo anterior ✔ |
| `C/modules/cart` (`projection.go`, `revalidate.go`) | `internal/modulos/solicitudes/intakes` (F6: `PriceList`, `SanitizeNote`, `NoteTooLongError`, escritores) | módulo anterior ✔ |
| `C/runtime/webhook_sink.go` | `internal/modulos/solicitudes/integrations/crmpush` (F6) | módulo anterior ✔ |
| `C/runtime/aggregator.go`, `source_composer.go` | `internal/modulos/captacion/intake` (F7: `WindowKey`, `JobStore`, `OpenJob`, …) | módulo anterior ✔ |
| externos | `wapp-cloudlink v0.17.0` (`cloudlinkv1`), `wapp-shared/{logger,textmatch,llm}`, `google/uuid`, `golang.org/x/text/unicode/norm` | sin cambio de versión |

**Ningún puente (import) sale de `C`**: todo destino ya está reconstruido. La lista blanca de
`fronteras_test.go` necesita, para `conversacion`: `acceso`, `edge`, `inferencia`, `catalogo`,
`solicitudes`, `captacion` (D-F8-5), `nucleo`, `platform`. Y en sentido contrario siguen
**permitidas** (ciclo de negocio congelado, D-7): `captacion → conversacion` (`stages`, `reanalisis`),
`solicitudes → conversacion` (`intakes/telemetria`), `catalogo → conversacion` (`model`).

## 4 · 🔴 El singleton `*runtime.Runtime`: estado en memoria, goroutines, relojes

| Estado | Dónde | Qué pasa con DOS instancias en el proceso |
|---|---|---|
| `locks *keyedMutex` — un mutex por `store.Key` (tenant·sesión·contacto), con refcount | `runtime/keyedmutex.go:18-54`; lo toman `incoming.go:138`, `start.go:114`, `event_lifecycle.go:333,408` | `Start`/`CancelEventForTenant` (API) y `HandleIncoming` (Edge) dejan de excluirse: Load→Save concurrentes sobre la misma fila `flow_state`, puntero de evento re-escrito tras apagarlo |
| `replyLimiter` — token-bucket por conversación (`ratelimit.Limiter`) | construido en `fase7_flujos.go` (`c.replyLimiter = ratelimit.NewLimiter(...)`), usado vía `ReplyLimiter` (`runtime_engine.go:281`) | cupo doble: el anti-bucle (Plan 020 · T0) deja pasar el doble de auto-respuestas |
| `incomingSem chan struct{}` — techo de concurrencia (64 por defecto, `WAPP_FLOW_MAX_CONCURRENT_INCOMING`) | `runtime_engine.go:27,109,527-529` | techo doble |
| `autoreplyStreaks *streakCounter` — rachas por conversación, TTL 30 min, tope 10.000 claves | `runtime/streak.go:~70-100` (mutex `:87`) | el gauge `wapp_flow_autoreply_streak_max` solo lee una instancia; rachas partidas |
| `passiveAnnounced sync.Map` | `runtime_engine.go:229` | log INFO duplicado (inocuo) |
| `now func() time.Time` (`WithClock`) | `runtime_engine.go:146,359` | — (inyectable) |
| `IntakeAggregator`: `seen map[intake.WindowKey]string`, pistas `dueNow`, canal `despertar` (buffer 1) | `runtime/aggregator.go:326-382`, `:480` | pistas del adelanto (`OnClassified`) llegan a una sola instancia; la red secundaria de dedupe (`seen`) se parte. **Las ventanas no**: viven en `intake_jobs` y su cierre es idempotente (README, contradicción 1) |
| `events.Store.now` (`WithClock`) | `events/store.go:34,43` | — |
| `modules.Registry` (`sync.RWMutex`) | `modules/registry.go:271` | dos registros: un módulo registrado en uno no existe en el otro |

**Goroutines y relojes del módulo:**

| Qué | Dónde | Vida |
|---|---|---|
| Una goroutine **por entrante** con `context.WithTimeout(Background, incomingTimeout)` (30 s por defecto, `WAPP_FLOW_INCOMING_TIMEOUT`) | `runtime/incoming.go:52` | corta; acotada por el semáforo |
| `IntakeAggregator.Run`: `RecoverAtBoot` + `time.NewTicker(sweepEvery)` (5 s, `aggregator.go:186`) + `despertar` | `aggregator.go:699-733`; lanzada **fuera** del módulo en `fase9_fondo.go:75` | larga — es una de las **cinco** goroutines de fondo |
| Reintento acotado del sink durable (`time.After`) | `runtime/resume.go:351` (D-054.4) | dentro del turno |
| Barrido perezoso de rachas en el **scrape** de `/metrics` (`MaxAutoreplyStreak`) | `runtime_engine.go:585`; cableado `c.mtx.SetFlowAutoreplyStreakMaxSource(...)` en `fase7_flujos.go` | sin goroutine (ADR-0003) |

**Quién toca el singleton desde fuera** (todo cambia a la vez en la conmutación):

| Consumidor | Cable hoy | Tras F8 |
|---|---|---|
| Gateway: `gw.OnIncoming = c.flowRuntime.OnIncoming` | `fase7_flujos.go` (FX: `:127`) | `fase7_flujos.go` con el `gw` nuevo de F3 |
| `POST /api/v1/flows/{id}/start` (I4, `Starter`) · `POST /api/v1/conversation-events/{id}/cancel` (I19, `EventCanceller`) | `fase8_transporte.go:177,229` → `publicapi` viejo | `apipublica` (FX TX.24) |
| `/admin/flows/start` (J19, `flowadmin.StartHandler(c.flowRuntime)`) | `fase8_transporte.go:129` | `rutas_admin.go` con `C/admin` |
| Métricas (`SetFlowAutoreplyStreakMaxSource`) | `fase7_flujos.go` | ídem, runtime nuevo |
| Fondo (`go c.intakeAggregator.Run(ctx)`) | `fase9_fondo.go:75` | `fase9_fondo.go` del arranque nuevo, agregador nuevo |

Por eso **no hay conmutación parcial**: `store`, `events`, `engine`, módulos y `runtime` se
conmutan en **un solo** commit `conmutar(conversacion)`, junto con las rutas I1–I19 y J18–J22.

## 5 · 🔍 Puentes y adaptadores que mueren en F8 — el re-toque

### 5.1 · Puentes de import de otras fases hacia el código viejo de conversación

Medidos sobre el código viejo (quién fuera de `flujos/**` importa `flujos/**`, producción):

| Fase | Paquete nuevo (su referencia vieja) | Importa del viejo | Símbolos reales (no comentarios) | Re-toque en F8 |
|---|---|---|---|---|
| F5 (si D-F8-1 = no) | `catalogo` (`flujos/modules/cart/catalog.go`) | `flujos/model` | `model.Content`, `model.ErrInvalidFlow` (`catalog.go:259-277,581`) | 1 fichero + test: import → `C/model`. 🔴 El envoltorio `%w` sobre `ErrInvalidFlow` tiene que ser el de `C/model` |
| F5 (ídem) | `catalogo/indice` (`intake/catalogo/cache.go`) | `flujos/model` | `model.Content{Raw: m}` (`cache.go:306`) | 1 fichero + test. (`LectorContenido` es estructural, `cache.go:89-95`: no cruza) |
| F6 | `solicitudes/intakes/telemetria` (`intakes/telemetria/telemetria.go:23`) | `flujos/store` | `store.FlowEvent` en su puerto | 1 fichero + test |
| F7 | `captacion/stages` (`intake/stages/draft.go`) | `flujos/store` | `store.Intake` (`draft.go:331-332,517`), `store.FlowEvent` (`:348,855,896`) | 1 fichero + test; el arranque le pasa el `store` **nuevo** |
| F7 | `captacion/reanalisis` (`reanalisis/reanalisis.go:53-55`) | `flujos/events`, `flujos/runtime` | `events.ThreadEntry` (`:251,704`), `events.KindMessage` (`:706`), `runtime.DefaultThreadLimit` (`:666`); `*runtime.SourceTextComposer` solo en comentario (`:266`) | 1 fichero + test |
| *(solo si D-F3-2 = no)* F3 (FX D-FX-3) | `edge/session` | `internal/gateway/session` (identidad de `ErrSessionOffline`) | una línea `var ErrSessionOffline = …` | 1 fichero. **Con la recomendación (D-F3-2) no existe**: viejo y nuevo son el centinela de `platform` desde F0 (T0.17) y aquí no hay nada que retirar |

No son puentes (medido): `captacion/pipeline` (`memoria.go`: tipos de catálogo → `catalogo`),
`captacion/stages/match*.go` (`SanitizeNote` → `solicitudes/intakes`; `cart.PriceListOf` es solo
comentario, `match_lineas.go:56`), `catalogo/catalogimport` (tipos de catálogo → `catalogo`),
`solicitudes/intakes/quotetext` (`LectorSemilla` estructural, `quotetext.go:210`).

### 5.2 · Adaptadores de arranque (`internal/arranque/bridge_<x>.go`, `05` §4.2)

Cuando un módulo **nuevo** ya conmutado tiene que servir a un consumidor **viejo** que el arranque nuevo aún
cablea, el tipo se traduce en `internal/arranque/bridge_<x>.go` (nivel simple, sin estado). No son los puentes
(import) de §5.1. **F8 no crea ninguno**: el runtime viejo es el mayor consumidor de tipos de otros módulos, así que
**todos los que quedan vivos** mueren aquí, y con cada muerte su módulo dueño entra en `Conmutados`:

| Adaptador | Nace | Por qué existe | Muere | Entra en `Conmutados` |
|---|---|---|---|---|
| `bridge_contact.go` (✎ D-F1-9) | F1 | el runtime/admin viejos piden `flujos/contact.Resolver`; se les da el `nucleo/contact` nuevo | **aquí** (T8.32) | `nucleo` |
| `bridge_iam.go` | F2 | el gateway viejo pide `in.Authenticator`/`in.Auditor` viejos | **F3** (T3.28): ya no existe al llegar aquí | lo anota F3 |
| `bridge_gateway.go` (`Infer` + `PlazaDe`) | F3 | el `local.Frame` del `llmvia` viejo pide `InferRequest` viejo. El runtime viejo recibe `c.gw` **sin** adaptador (estructural, F3 `arquitectura.md` §4) | **F4** (T4.24): ya no existe al llegar aquí | lo anota F4 |
| `bridge_inferencia.go` | F4 | `llmConfigBridge` (`reanalisis` viejo, `tenantllm.Config` viejo) y `turneroBridge` (`turnoacotado` viejo recibe `llmvia.TurnoRequest` y compara `ErrViaSinTurnoAcotado` viejos) | `llmConfigBridge` en **F7**; `turneroBridge` **aquí** (T8.32), y con él el fichero | `inferencia` |
| `bridge_captacion.go` | F7 | los adaptadores del adelanto y del compositor (`aheadBridge`, `composerBridge` en la spec de F7, con el nombre en inglés que F7 les dé) y la clausura del sink (`intakeahead.SinkFunc` → `IntakeAggregator.OnClassified` viejo), conversión de `WindowKey` | **aquí** (T8.32) | `captacion` |
| 2.ª instancia vieja de `intakes.Postgres` (no es fichero; si D-F6-1 = no, es `bridge_intakes.go`) | F6 (D-F6-1) | `cart.NewProjector` viejo pide `RevisionWriter`/`ShippingEnsurer` con tipos de `intakes` viejo | **aquí** (T8.32) | `solicitudes` |
| 2.ª instancia vieja de `intake.Postgres` | F7 (D-F7-1) | `NewIntakeAggregator`/`NewSourceTextComposer` viejos piden `JobStore`/`SourceTextWriter` viejos | **aquí** (T8.32) | `captacion` (junto con `bridge_captacion`) |
| — acceso · solicitudes | — | `WithEntitlements`, `events.NewDispatcher` (`entitlements.Resolver`) y los puertos del runtime/sink de solicitudes son **estructurales** (F2 §4, F6 §4): el objeto nuevo entra tal cual, sin adaptador | — | — |

`conversacion` no tiene adaptador propio: entra en `Conmutados` con su `conmutar(conversacion)`. Al cerrar F8 la lista
está **completa**. `FaseActual` sigue existiendo y no cambia.

Tabla única del plan: [`../00-marco/estructura.md`](../00-marco/estructura.md) §2.1.
T8.2 la re-mide contra el árbol real (hoy, 2026-10-03, en `internal/arranque` solo existe `bridge_contact.go`) antes de tocar nada.

**Dimensión del re-toque** (lo más arriesgado de F8): **6 paquetes nuevos** re-tocados (§5.1, ~6
ficheros y sus tests), **todos** los `bridge_*.go` que quedan borrados (3: `bridge_contact`, `bridge_inferencia`,
`bridge_captacion`), las dos segundas instancias viejas fuera, y
`fase7_flujos.go` + `fase5_captacion.go` + `fase6_solicitudes.go` + `fase8_transporte.go` +
`fase9_fondo.go` + `rutas_admin.go` del arranque nuevo re-cableados en la misma ola. El
riesgo no es de compilación (el compilador lo caza) sino de **identidad**: dos instancias de algo
que tiene que ser una (`entResolver` con su caché TTL, `flowDeps.kp` del índice ciego, el `gw`).
[`reglas.md`](reglas.md) §2 las lista.

## 6 · Cableado en el arranque nuevo (`internal/arranque/fase7_flujos.go`)

Hoy `internal/bootstrap/arranque/fase7_flujos.go` (`requiere`: `gateway`, `selector`,
`almacenes`, `solicitudes`; marca `flujos`). En F8 la fase nueva construye, **con paquetes de `C`**:

1. `modules.NewRegistry()` + `menu.New()`, `survey.New()`,
   `cart.New(cart.WithLogger(c.log), cart.WithMatchHook(c.mtx.CartMatch))`, `media.New()` — **4**
   módulos, en ese orden.
2. `engine.New(reg, WithContentSource(content.NewRouter(content.NewStatic(), content.NewJSON(flowStore))), WithConsultaResolver(turnoacotado), WithConsultaObserver(observaConsultas(log)))`.
3. `admin.NewEngineDurableFlowChecker(flowStore, engine)`; `replyLimiter`.
4. La ventana de captación (`cablearVentanaDeCaptacion`): `intakeahead.New(…)` **nuevo de F7**,
   `gw.OnWarmup`, log de interruptores efectivos (`WAPP_LLM_WARMUP_ENABLED`,
   `WAPP_LLM_MAX_OUTPUT_TOKENS_ENABLED`), `runtime.NewIntakeAggregator(log, intakeJobStore, flowStore, entResolver, WithSourceComposer(composer), WithAheadRequester(ahead))`, `gw.OnEdgeReady = pipeline.Despertar`.
5. `events.NewDispatcher(eventStore, events.NewTriggerKindOffer(triggerStore), entResolver)`.
6. `runtime.New(flowStore, engine, gw, flowResolver, contacts, log, <22 opciones>)` — la lista
   entera, con su orden y sus comentarios, en [`diseno.md`](diseno.md) §3.14.
7. `SetFlowAutoreplyStreakMaxSource`, `gw.OnIncoming`, `gw.OnHeartbeat` (solo log Debug).

Y en otras fases del arranque nuevo, lo que hoy construye conversación fuera de la fase 7:
`fase3` (`flowStore`, `flowResolver`, `triggerStore`, `eventStore`, `flowDeps`), `fase5`
(`NewSourceTextComposer`, `turnoacotado.New`, `stages.NewDraft(…, flowStore, …, flowStore)`,
`indice.NewFuenteContenido(flowStore)`, `quotetext.ConSemilla(flowStore)`), `fase8`
(`FlowDeps`, `MediaDeps`, `Triggers`, `ConversationEvents`, `EventCanceller`, handlers `:8100`),
`fase9` (`go aggregator.Run`).

**Cómo se comprueba que el binario nuevo usa lo nuevo**: `go list -deps ./cmd/server-modular | grep -E 'internal/(flujos|turnoacotado)'` → vacío; la huella; y el test de cableado de identidad
(R8.5.b).

## 7 · Rutas y rpc afectados

**Rutas** (autoridad: FX [`mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md), regla de conteo:
registro en ejecución, una vez por patrón):

- `:8103` → `apipublica`, **19**: I1–I4 `flows` (4) · I5 `media/upload-url` · I6–I10
  `tenant-content` (5) · I11–I13 `triggers` (3) · I14–I17 `catalog/import*` (4) · I18–I19
  `conversation-events` (2). Tareas FX **TX.22–TX.24**.
- `:8100`, cambian de handler, **5**: J18 `/admin/flows`, J19 `/admin/flows/start`, J20–J22
  `/admin/triggers` (3).
- Tras TX.24 el `publicapi` viejo **no se construye** en el binario nuevo (queda para F10 borrarlo).

**rpc**: ninguno cambia. `Connect` entra al runtime por `gw.OnIncoming`; el proto sigue en
`wapp-cloudlink v0.17.0`.

## 8 · Lo que NO cambia hacia fuera

Tablas (`flow_definitions`, `flow_state`, `flow_events`, `flow_triggers`, `conversation_events`,
`conversation_event_messages`, `conversation_welcomes`, `tenant_content`, `survey_results`,
`intake_jobs` vía captación…) y sus migraciones; las 19 + 5 rutas y sus textos; las métricas
`wapp_flow_reactive_blocked_total{reason}`, `wapp_flow_autoreply_streak`,
`wapp_flow_autoreply_streak_max`, `wapp_cart_match_total`, `wapp_flow_event_lifecycle_total`; los
nombres de efecto y de `flow_events`; las variables `WAPP_FLOW_REPLY_RATE`, `WAPP_FLOW_REPLY_BURST`,
`WAPP_FLOW_INCOMING_TIMEOUT`, `WAPP_FLOW_MAX_CONCURRENT_INCOMING` (nombre efectivo con `WAPP_`); los
textos al cliente; el comportamiento del Edge. `cmd/server` (el viejo) no cambia ni un byte.
