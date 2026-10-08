# F8 · Reglas — lo que no se toca, las trampas y la definición de hecho

> Se suman a [`05`](../../05-metodo-contratos-y-tdd.md), al marco
> [`../00-marco/`](../00-marco/README.md) y a las reglas de FX
> ([`../FX-cara-http/reglas.md`](../FX-cara-http/reglas.md)). `C` = `internal/modulos/conversacion`.

## 0 · Niveles de ceremonia y lo que sustituye al umbral (`05` E-12, E-9, E-4)

- **Quién clasifica**: el inventario E-12 (T8.2), que aprueba Jhoan. Provisional en [`diseno.md`](diseno.md) §0.1.
  - **simple**: contrato, test y lógica en **una pasada**, varios archivos por sesión;
  - **medio**: rojo y verde por archivo, **agrupados por paquete**, un test por promesa;
  - **complejo** (`runtime`, los almacenes): esquema completo E-2…E-9, con **mutantes** donde haga falta.
- 🔴 **No se relaja en ningún nivel**: equivalencia viejo ↔ nuevo, `make ci-local` rc=0 con **0 SKIP**, procesos de F9.
- **Sin umbral de cobertura (P2)**: un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9.
  `make cobertura-ficheros` es un informe: se mira, no bloquea.
- **Auxiliares no exportados (P6)**: su test nace en el **verde** y solo si llevan regla de negocio o ramas no
  triviales; no se testea fontanería ni `if err != nil`; el resto lo cubre F9.
- **Puertos con BD (P4)**: `store`, `trigger`, `events.Store`, `PostgresSelfNumbers` y `PostgresTenantResolver` tienen
  su suite `Contrato(t, func(t) Montaje)` corrida **en memoria y en Postgres** con el arnés de F9-A. La marca de estado
  vigila **todas** las columnas que la operación puede tocar (hallazgo 35).
- **Corpus adversario (hallazgo 40)**: los corpus de equivalencia viejo ↔ nuevo (normalización de `trigger`, troceo y
  cascada del carrito, sobre de P2) llevan casos adversarios —separadores repetidos como `a@@b`, dígitos no ASCII,
  espacios Unicode—, no solo casos felices.
- **Adaptadores (`05` §4.2)**: F8 no crea ninguno y retira todos (T8.32). Un módulo entra en `Conmutados` cuando
  **muere su último adaptador**. Los «puentes» de §4.1 son otra cosa: imports nuevo → viejo.

## 1 · Lo que no se toca

- **El código viejo** (`internal/flujos/**`, `internal/turnoacotado`, `internal/publicapi`,
  `internal/bootstrap/**`): es lo que corre en UAT y el oráculo (E-1). Se lee; no se edita.
- **Las tablas y migraciones** del motor (`internal/platform/storage/postgres/migrations/`, runner
  full-replay). Ni una columna nueva en F8.
- **El proto** (`wapp-cloudlink v0.17.0`) y el contrato del Edge: `OnIncoming` recibe
  `*cloudlinkv1.IncomingMessage` y el envío espera el `Ack` como hoy.
- **Deudas conocidas**, que se portan tal cual y se citan en el comentario: D-1
  (`conversation_event_messages` fuera de `rekeyTargets`), D-5 (`KindLLM` sin productor), D-7
  (`admin.Register` muerto: no se reconstruye, D-F8-2), D-16 (`writeJSON`), D-17 (`rows.Close`).
- 🔴 **El homónimo DEK**: la «DEK» de `events/store.go` (`body_dek`) es la del **envelope de PII de
  negocio**, no la DEK del ADR-0007 (la del almacén de `whatsmeow`, que custodia el cliente y nunca
  cruza a la nube). No se confunden.

## 2 · Trampas conocidas

| # | Trampa | Dónde | Cómo se evita |
|---|---|---|---|
| T-1 | **Dos instancias del runtime** en el binario nuevo (una para el gateway, otra para la API) no dan error: parten el `keyedMutex`, el limitador, las rachas y el semáforo | `fase7_flujos.go` · `fase8_transporte.go:129,177,229` | Un solo `runtime.New`; test de identidad (R8.5.b); conmutar rutas I4/I19/J19 **en el mismo commit** |
| T-2 | **Un segundo resolver de entitlements**: dos cachés TTL y dos verdades de lo contratado | comentario del agregador en `fase7_flujos.go` | El arranque pasa **el mismo** `entResolver` (de `acceso`, F2) al agregador, al despachador, al runtime y a `apipublica` |
| T-3 | **Un segundo `KeyProvider`** en el anti-self-loop: ningún índice ciego casa y el guard **deja de bloquear sin un solo error** | comentario de `WithSelfNumbers` en `fase7_flujos.go` | `NewPostgresSelfNumbers(db, flowDeps.kp)` con el **mismo** `kp` que usa `fleet` |
| T-4 | **Olvidar el `Run` del agregador** en `fase9_fondo.go`: las ventanas se abren y nunca cierran, en silencio | `fase9_fondo.go:75` | Huella de goroutines; candado de cableado |
| T-5 | **Olvidar `gw.OnIncoming`**: el proceso arranca entero y ninguna conversación avanza | cabecera de `fase7_flujos.go` («sin los hooks del final, esta fase entera no hace nada») | Test de cableado; R8.8.a |
| T-6 | **Olvidar `WithConsultaResolver`** (turnoacotado): el engine devuelve «sin_resolutor» y el carrito repromptea como siempre — ya pasó dos veces | `turno_acotado_cableado_test.go` | Portar el candado de cableado |
| T-7 | **`/metrics` no es inocuo**: el scrape barre las rachas vencidas (`MaxAutoreplyStreak`); sin scrape, los episodios abandonados no se cierran | `runtime_engine.go:585`, `contratos.md` §3 | Se conserva; test de `streak.go` con reloj inyectado |
| T-8 | **Centinelas por identidad**: `errors.Is` falla si el `ErrSessionOffline` que devuelve el `gw` nuevo no es el que compara el handler | `publicapi/flows.go:235`, `flujos/admin/handlers.go:326`; D-F3-2 (alternativa: FX D-FX-3) | En F8 el centinela es **uno**, el de `modulos/edge/session` (= el de `platform`, D-F3-2); `C/admin` y `apipublica/flows.go` comparan ese |
| T-9 | **`ErrInvalidFlow` doble**: si `catalogo` quedó envolviendo el del `flujos/model` viejo, `errors.Is(err, C/model.ErrInvalidFlow)` da false | `cart/catalog.go:259-277` (hoy) | Re-toque de `catalogo` en T8.31 (si D-F8-1 = no) |
| T-10 | **Borrar `seen` al cerrar la ventana** «por limpieza» reabre ventanas con mensajes ya procesados | `aggregator.go` (bloque 🔴 tras `takeHints`) | Regla AG-5 en el contrato y su test |
| T-11 | **Mover la petición de consulta** por debajo de `st.Started = true` en el carrito deja la suite de conducta verde y duplica `cart_started` | `orden_consulta_ast_test.go` | Candado AST (R8.4.b) |
| T-12 | **Un `Delete` nuevo sin `Close`** infla `wapp_flow_autoreply_streak_max` media hora; cinco de seis cierres se podían borrar con la suite verde | `streak_invariante_test.go` | Candado AST (R8.4.a) |
| T-13 | **El runtime de hoy recibe `c.gw` sin adaptador**: `Sender` encaja con `(*gatewaygrpc.Server).SendText/SendMedia`. Si F3 cambió firmas, el adaptador vive en `bridge_*.go` y **muere aquí** | `runtime/runtime.go:18-28`; FX `arquitectura.md` §4.2 | T8.32 borra el adaptador y el `Sender` nuevo encaja con el `gw` nuevo sin adaptador |
| T-14 | **Levantar los dos binarios a la vez**: los dos migran, escuchan `:8100-8103`, y dos agregadores barren la misma tabla | `04` §2.2 | Prohibido (§3) |
| T-15 | **Contar rutas por líneas** (un bucle sobre 3 elementos son 3 rutas; un comentario con `mux.Handle` no cuenta) | regla del ecosistema | Contar por la huella (patrones registrados en ejecución) |
| T-16 | **Leer un `rc` con pipe** o contar SKIP sin `-v` | `validar-antes-de-cerrar` | Siempre `…; echo rc=$?` sin pipe y `go test -v … \| grep -c -- '--- SKIP'` |
| T-17 | **Variables por su nombre efectivo**: el loader compone `WAPP_`; `FLOW_INCOMING_TIMEOUT` es `WAPP_FLOW_INCOMING_TIMEOUT` | `internal/platform/config` | Nombrar siempre con `WAPP_` |

> ✎ **D-F9-10 (Jhoan, 2026-10-08), para la goroutine de fondo de esta fase**: al reconstruir el **agregador** (`flujos/runtime/aggregator.go`, `Run` → `RecoverAtBoot` → `Sweep`, «agregador: no se pudieron listar las ventanas vivas»), su contrato promete «contexto cancelado → vuelve **sin** loguear a `ERROR`», con su caso, como el *webhook worker* de F6 (D-F6-7; `solicitudes/integrations/worker.go` y `TestRun_ContextCancelled_…` son el modelo). El test de P0 ya tolera esas líneas tras la parada; esto es para que el log de producción no las lleve.

## 3 · Prohibiciones

- 🚫 `t.Skip` en código nuevo; 🚫 un Postgres vivo (`WAPP_TEST_DB_DSN`, `localhost:5432`, el
  Postgres 16 de la VM web); 🚫 `time.Sleep` en tests del runtime (reloj inyectado).
- 🚫 `git mv` o reescritura por script de imports del código viejo.
- 🚫 Conmutar con un solo fichero del módulo aún en rojo.
- 🚫 Conmutar el runtime sin mudar en el mismo commit I4, I19 y J19.
- 🚫 Dejar un puente (import) o un adaptador `bridge_*.go` «para luego»: F8 es la última fase de módulo.
- 🚫 Crear un adaptador nuevo en F8.
- 🚫 Levantar `cmd/server` y `cmd/server-modular` a la vez contra la misma BD o los mismos puertos.
- 🚫 Arreglar de paso D-1, D-5, D-16, D-17 o cualquier comportamiento observable.
- 🚫 Tests AST fuera de los dos candados de §4.2 de [`diseno.md`](diseno.md) (E-7).

## 4 · Definición de hecho

F8 está hecha cuando **todo** esto es cierto y está escrito con su número en el bloque de `ESTADO.md`:

1. `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/conversacion | wc -l` → **0**.
2. `make ci-local` → `GATE_RC=0` leído del log; `go vet -tags pendiente ./...` rc=0; lint `v2.12.2`.
3. Un test por promesa del contrato en cada fichero de `C`; mutantes muertos en el nivel complejo (`runtime` y lo
   que fije el inventario E-12); las suites `Contrato` de `store`, `trigger`, `events.Store`, `self_numbers` y
   `tenant_resolver` verdes **en memoria y en Postgres** (P4). `make cobertura-ficheros` va como informe a
   `ESTADO.md`; no bloquea.
4. `GOWORK=off go test -v ./internal/modulos/... ./internal/nucleo/... ./internal/arranque/... ./internal/apipublica/... 2>&1 | grep -c -- '--- SKIP'` → **0**.
5. `go list -deps ./cmd/server-modular | grep -cE 'wapp-cloud-platform/internal/(flujos|turnoacotado|publicapi|intake|intakes|intakeahead|reanalisis|catalogimport|gateway|iam|platformadmin|entitlements|llmvia|prompts|tenantllm|degradation|integrations|tenantvars|diagnostics|inferstats|receipts|ingest|filtercfg|evidence|casebank|intentcfg|bootstrap)(/|$)'` → **0**.
6. Adaptadores: en F8 **nacen 0** y **mueren todos** los vivos (`bridge_contact`, `bridge_inferencia`,
   `bridge_captacion`, y las dos segundas instancias viejas de `intakes`/`intake`); no queda ninguno para otra fase
   (`bridge_iam` murió en F3, `bridge_gateway` en F4). `ls internal/arranque/bridge_*.go` → sin coincidencias; la lista
   de puentes (import) de `internal/modulos/fronteras_test.go` → vacía; `Conmutados` → **completa**: con cada muerte
   entró su dueño (`nucleo`, `inferencia`, `captacion`, `solicitudes`) y `conversacion` con su `conmutar`.
   `FaseActual` sigue existiendo y no cambia.
7. `internal/arranque/huella_test.go` → igual al viejo en rutas (95), rpc (2), métricas (17 + 5) y
   goroutines (5 de fondo).
8. Candados R8.4.a–c en verde **y** comprobados por mutación local (se rompe el invariante, se ve
   rojo, se deshace sin commitear).
9. La sesión de cierre arrancó `cmd/server-modular` y recorrió una conversación (R8.8.a); los procesos del
   módulo (T9.29; si D-F9-1 = no, T9.34) pasaron contra el binario nuevo (R8.8.b).
10. `ESTADO.md` y el README de esta fase dicen «cerrada» con los SHA.
