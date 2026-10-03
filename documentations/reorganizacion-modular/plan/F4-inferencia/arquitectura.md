# F4 · Arquitectura — la vista macro

> Medido el 2026-09-28 sobre `dev` @ `1b18932` con `GOWORK=off go list` y `wc -l`. Regla de las
> aristas: import de un fichero **de producción** (los tests se dicen aparte).

## 1 · Paquetes viejos → nuevos

| Viejo (referencia, no se toca) | Nuevo | Prod · líneas | Qué es |
|---|---|---|---|
| `internal/llmvia` | `internal/modulos/inferencia/llmvia` | 2 · 936 | El **único** `switch` por vía (`llmvia.go:219`) y sus hermanos `Warm`/`PlazaDe`/`Turno`; el decorador que avisa al dueño (`notify.go`) |
| `internal/llmvia/local` | `…/inferencia/llmvia/local` | 2 · 649 | Adaptador `llm.LLMProvider` contra el Ollama del Edge por el frame de inferencia; presupuesto de salida por etapa; calentamiento |
| `internal/prompts` | `…/inferencia/prompts` | 2 · 407 | Carga/vuelca/valida los `.tmpl` de P2–P5 (`WAPP_LLM_PROMPTS_DIR`) |
| `internal/tenantllm` | `…/inferencia/tenantllm` | 2 · 404 | Vía y credencial cifrada por tenant (`public.tenant_llm`, migraciones 0071/0073) |
| `internal/degradation` | `…/inferencia/degradation` | 2 · 676 | Avisos al dueño (`public.owner_degradation_notices`, 0075), dedupe por ventana |
| — | `…/tenantllm/tenantllmhelpertest` ✚ | 2 | Suite `Contrato(t, func(t) Montaje)` + doble en memoria (no existe hoy: es de los 12 de `05` E-6). La suite corre en memoria **y** en Postgres con el arnés (P4) |
| — | `…/degradation/degradationhelpertest` ✚ | 2 | Ídem |
| — | `internal/arranque/bridge_inferencia.go` ✚ | 1 | Adaptador de arranque (`05` §4.2) para dos consumidores viejos (§4). Nace en F4; muere por partes en F7 (`llmConfigBridge`) y F8 (`turneroBridge`) |

Totales: `wc -l internal/{llmvia,llmvia/local,prompts,tenantllm,degradation}/*.go` sin `_test` →
**10 ficheros · 3.072 líneas**; tests viejos **14 ficheros · 88 `Test*`** (21 de integración en 2
ficheros, que se saltan sin `WAPP_TEST_DB_DSN`: `tenantllm/postgres_integration_test.go:44`,
`degradation/postgres_integration_test.go:42`).

**Gemelo en memoria (verificado)**: ni `tenantllm` ni `degradation` tienen implementación en
memoria de producción (`ls internal/tenantllm internal/degradation` → solo `postgres.go` y el
dominio). Los dobles que existen son **de test y no exportados**: `storeFake` en
`internal/llmvia/selector_test.go:23` y `storeFalso` en `internal/degradation/degradation_test.go:44`.
⇒ confirmado que están entre los 12 de `05` E-6: F4 crea `tenantllmhelpertest` y `degradationhelpertest`.

## 2 · Grafo de imports y orden

Medido (`go list -f '{{.Imports}}'`, sin stdlib):

```
llmvia        → degradation · gateway/grpc · llmvia/local · tenantllm · wapp-shared/{llm,llm/api,logger}
llmvia/local  → gateway/grpc · wapp-shared/llm
prompts       → wapp-shared/llm
tenantllm     → platform/crypto
degradation   → (nada interno; solo database/sql)
```

Orden de contratos y de verde (hojas primero): **`prompts` · `degradation` · `tenantllm`** (en
paralelo, independientes) → **dobles** (`degradationhelpertest`, `tenantllmhelpertest`) → **`llmvia/local`** →
**`llmvia`** (`notify.go` antes que `llmvia.go` en el verde, porque `llmvia.go` llama a `avisar`).

Tests: `degradation_test.go` importa `tenantllm` (compara `ViaLocal`/`ViaAPI` de los dos,
`degradation_test.go:340`) y lee la `0075`; los tests de `tenantllm` y `degradation` de integración
importan `platform/storage/postgres{,/migrations}` (F9, no se escriben aquí).

## 3 · Imports hacia fuera del módulo

| Import nuevo | Clase | Estado |
|---|---|---|
| `internal/platform/crypto` (`FieldCipher`) | `platform` | permitido |
| `internal/modulos/edge/grpc` (`InferRequest`, `InferError` por duck-typing, `ClaseInteractivo`/`ClaseLote`, `DefaultInferGrace`, `*Server`) | módulo reconstruido en F3 | permitido si `fronteras_test.go` tiene `inferencia → edge` en la lista blanca (arista medida hoy: `llmvia → gateway`, `02` §2) |
| `wapp-shared/{llm,llm/api,logger}` | externo | permitido |
| — | **puente (import) al código viejo**, `05` §4.1 | **ninguno** |

⚠️ **Arista inversa solo de test**: el test viejo `internal/gateway/grpc/inference_vocabulario_internal_test.go:9`
importa `internal/degradation` (los `Motivo*` del transporte ⊆ `degradation.Reasons()`). Si F3
reprodujo ese test en `modulos/edge/grpc` contra el `degradation` **viejo** (único que existía), en
F4 se **re-toca** para que importe `modulos/inferencia/degradation` (T4.28). Es test, no cuenta para
el grafo de producción (`02` §0).

## 4 · Conmutación: quién consume el selector y los almacenes, y con qué tipos

Consumidores de producción medidos (`go list`, importadores): `llmvia` ← `bootstrap/arranque`,
`turnoacotado`; `tenantllm` ← `arranque`, `llmvia`, `publicapi`, `reanalisis`; `degradation` ←
`arranque`, `llmvia`, `publicapi`; `prompts` ← `arranque`, `cmd/prompts`. Y los que reciben el
selector por **interfaz** (`internal/bootstrap/arranque/fase5_captacion.go:91-126,162-170,282,342`,
`fase7_flujos.go:149-158`):

| Consumidor (viejo hasta) | Qué pide | ¿El nuevo lo satisface tal cual? |
|---|---|---|
| `intake/stages.NewP2/P3/P4` (F7) | `ProviderSelector{ For(ctx, tenantID, origin string) (llm.LLMProvider, error) }` (`stages/p2.go:58`) | **Sí** (solo tipos de `wapp-shared`) |
| `intake/pipeline.ConAforo` (F7) | `PlazaDe(ctx, tenantID, origin) (string, bool, error)` (`pipeline/plaza.go:98`) | **Sí** |
| `intakes/quotetext.NewServicio` (F6) | `For` (`quotetext.go:219`) | **Sí** |
| `intakeahead.New` + `WithCalentador` (F7) | `For` (`intakeahead.go:161`) y `Warm(ctx, tenantID, sessionID, llm.ClassifyRequestInput) error` (`calentamiento.go:71`); no compara `ErrViaSinCalentamiento` (solo lo loguea, `:167-178`) | **Sí** |
| `turnoacotado.New` (F8) | `Turno(ctx, t, s string, llmvia.TurnoRequest) (string, error)` con el tipo **viejo** (`turnoacotado.go:72`) y `errors.Is(err, llmvia.ErrViaSinTurnoAcotado)` **viejo** (`:133`, `troceado.go:149`) | 🔴 **No** ⇒ adaptador `turneroBridge` |
| `reanalisis.NewServicio` (F7) | `ConfigLLM{ Get(ctx, tenantID) (tenantllm.Config, bool, error) }` con `Config` **viejo** (`reanalisis.go:283`) | 🔴 **No** ⇒ adaptador `llmConfigBridge` |
| `publicapi` vieja (`Deps.TenantLLM`, `Deps.DegradationNotices`) | tipos viejos | Se ponen a `nil` en la vieja: las 4 rutas se mudan a `apipublica` (TX.12–14) |

**El caso que obliga al adaptador y no a un segundo selector.** Si `turnoacotado` recibiera el
selector nuevo sin adaptar, no compilaría (tipo de `TurnoRequest`); y si se «arreglara» con un
selector viejo aparte, habría **dos selectores** y el viejo necesitaría el adaptador de
`local.Frame` de F3 para siempre. Peor aún, sin traducir el centinela, un tenant en vía `api`
recibiría un **error** en vez de `modules.MotivoSinResolutor` (`turnoacotado.go:133-141`): cambio de
conducta observable. ⇒ `turneroBridge.Turno` llama al selector nuevo y, si
`errors.Is(err, N.ErrViaSinTurnoAcotado)`, devuelve el centinela **viejo**; el resto de errores pasa
intacto (el decorador de avisos ya corrió dentro del nuevo).

**El adaptador de F3 muere aquí.** F3 dejó en `internal/arranque/` un adaptador (`bridge_gateway.go`) que hace que el
`*Server` nuevo de `modulos/edge/grpc` satisfaga el `local.Frame` **viejo** (su `Infer` pide
`gatewaygrpc.InferRequest` viejo, `internal/llmvia/local/local.go:270-272`; ver
`../FX-cara-http/arquitectura.md` §4.1). Con el `llmvia/local` nuevo, el `*Server` nuevo satisface
`local.Frame` **sin adaptador** (misma firma, tipos nuevos). ⇒ `conmutar(inferencia)` **borra** ese
adaptador y su test (T4.24).

**Dos `tenantllm.Postgres` en el mismo proceso hasta F7** (el nuevo para selector y `apipublica`; el
viejo solo si se elige esa vía en vez de `llmConfigBridge`). Es inocuo: el store no guarda estado
(solo `*sql.DB` y `*crypto.FieldCipher`, `internal/tenantllm/postgres.go:16-19`). Se recomienda el
adaptador (una conversión de struct con los mismos campos) para no abrir un segundo camino al SQL.

## 5 · Estado en memoria, goroutines, métricas y relojes

| Qué | Dónde | Consecuencia para la conmutación |
|---|---|---|
| **Sin estado mutable de proceso** en todo el módulo: el `Selector` es inmutable tras `NewSelector` (copia las opciones por petición, `llmvia.go:241-252`); `local.Provider` inmutable (`local.go:274-275`); stores sin caché | `grep -n 'sync\.\|go func' internal/{llmvia,llmvia/local,prompts,tenantllm,degradation}/*.go` → vacío | Dos instancias en el proceso **no** son un bug; aun así, **un solo** selector (R4.7.b) |
| **Cero goroutines** propias | ídem | La huella de goroutines no cambia |
| Métrica `wapp_llm_degradacion_total{origen,via,reason}` | Declarada en `internal/platform/metrics/metrics.go:105`; el módulo solo llama al **callback** `ObservadorDegradacion` (`notify.go:259`) = `(*metrics.Metrics).LLMDegradacion` | El módulo no importa Prometheus; la huella de métricas no cambia |
| Relojes | `Selector.ahora` (inyectable, `llmvia.go:104,285`) · `degradation.Notifier.Ahora` (`degradation.go:342,365`) · `time.Until` sobre el deadline del ctx en `local.plazo` (`local.go:513-524`) | Tests con reloj inyectado; `plazo` se prueba con ctx de deadline relativo |
| Plazos con número razonado | `MargenVeredicto = 7 s` (`local.go:213`) · `DefaultTimeout = 30 s` (`:231`) · `PlazoTurno = 12 s` · `TechoTurno = 128` (`llmvia.go:544,552`) · `avisoTimeout = 3 s` (`llmvia.go:67`) · `VentanaPorDefecto = 15 min` (`degradation.go:208`) | Constantes idénticas; su porqué viaja (E-10) |

## 6 · Cableado en el arranque nuevo (D-F4-3: sin re-partir fases)

| Fichero (copia F0 en `internal/arranque/`) | Hoy (viejo) | Tras `conmutar(inferencia)` |
|---|---|---|
| `fase3_almacenes.go` | `tenantllm.NewPostgres(c.db, c.flowDeps.cipher)` (`:157`) · `degradation.NewPostgres(c.db)` + `NewNotifier(store, 0)` (`:169-170`) | Los mismos constructores del paquete **nuevo**; campos del contenedor con tipos nuevos |
| `prompts.go` | `prompts.Cargar(dir)` + línea de log (`:25-39`) | `N/prompts.Cargar`; misma línea de log, mismo prefijo de error `prompts ajustables de P2-P5: ` |
| `fase5_captacion.go` · `construirSelectorDeVia` | `llmvia.NewSelector(c.tenantLLMStore, c.log, WithFrame(c.gw), WithNotifier(…), WithLocalOptions(local.ConPlantillas(…)), WithLocalOptions(local.WithMaxOutputTokens(cfg.LLM.MaxOutputTokensEnabled)), WithDegradacionObservada(c.mtx.LLMDegradacion))` (`:91-112`) — **dos** llamadas a `WithLocalOptions` que **acumulan** | Idéntico con el paquete nuevo y `c.gw` **sin** adaptador; `turnoacotado.New(turneroBridge{c.llmSelector})` |
| `fase5_captacion.go` · `construirPuertasDelDueno` | `reanalisis.NewServicio(…, c.tenantLLMStore)` (`:319-320`) | `…, llmConfigBridge{c.tenantLLMStore}` |
| `fase8_transporte.go` | `TenantLLM: c.tenantLLMStore` (`:242`) y `DegradationNotices: c.degradationStore` (`:247`) en `publicapi.Deps` | `nil` en la vieja; `apipublica` recibe los nuevos (TX.14) |
| orden y `requiere()` | `faseCaptacion.requiere() = gateway, almacenes, cipher` | **sin cambio** (el primer error visible de un arranque caído no cambia) |

## 7 · Rutas (autoridad: `../FX-cara-http/mapa-de-rutas.md` filas F1–F4)

| Fila | Ruta | Scope · feature | Se muda en |
|---|---|---|---|
| F1 | `GET /api/v1/tenant-llm` | `llm.read` · `api_llm` | **F4** |
| F2 | `PUT /api/v1/tenant-llm` | `llm.write` (audita `tenant_llm`) · `api_llm` | **F4** |
| F3 | `DELETE /api/v1/tenant-llm` | ídem | **F4** |
| F4 | `GET /api/v1/degradation-notices` | `llm.read` · **`llm_intake`** (no `api_llm`: seis de los ocho motivos son de la vía local, `publicapi.go:998-1007`) | **F4** |

Monta si: F1–F3 `TenantLLM ≠ nil && Entitlements ≠ nil`; F4 `DegradationNotices ≠ nil && Entitlements ≠ nil`
(`publicapi.go:981,1025`). Se cuentan **registros en ejecución**: 4 patrones, 4 `mux.Handle`.
Ficheros de `apipublica`: `tenantllm.go` (449 l hoy) y `degradationnotices.go` (202 l); sus tareas
son TX.12–TX.14 de [`../FX-cara-http/tareas.md`](../FX-cara-http/tareas.md), ejecutadas en la sesión F45-02.

## 8 · Lo que no cambia hacia fuera

- Tablas `public.tenant_llm`, `public.owner_degradation_notices` y sus `CHECK` (0071/0073/0075):
  mismo SQL, mismo `ON CONFLICT`, mismo orden de `List`.
- `WAPP_LLM_PROMPTS_DIR`, `WAPP_LLM_MAX_OUTPUT_TOKENS_ENABLED`, `WAPP_LLM_WARMUP_ENABLED` (los lee
  `platform/config`, no este módulo).
- `cmd/prompts` (`-volcar`, `-comprobar`) sigue con el paquete viejo hasta F10 (README «Contradicciones» 3).
- El frame hacia el Edge: mismos campos, mismos techos, misma `class`, mismo `warmup`.
- Métrica, textos de error y literales de §4 de [`diseno.md`](diseno.md).
