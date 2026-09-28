# F8 · Requisitos

> Historias y criterios EARS ([`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md)
> §2). `C` = `internal/modulos/conversacion`. Cada criterio nombra el comando o el test que lo verifica.

## H8.1 · Inventario verificado antes de tocar nada

> Como **la sesión web**, quiero **re-medir el módulo y la lista real de puentes y adaptadores**
> contra `dev`, para **no reconstruir sobre cifras de otra fecha**.

- **R8.1.a** · **CUANDO** empiece F8, **LA** sesión web **DEBERÁ** re-contar ficheros, líneas y
  exportados de los 14 paquetes viejos y dejar el resultado en el `README.md`. — Verifica: los
  comandos de [`README.md`](README.md) §Tamaño; diff con la tabla ≤ lo que haya entrado en `dev`.
- **R8.1.b** · **LA** sesión web **DEBERÁ** listar los puentes declarados en
  `internal/modulos/fronteras_test.go` que apuntan a `internal/flujos/**` o `internal/turnoacotado`
  y los ficheros `internal/arranque/puente_*.go`. — Verifica: `grep -n 'flujos\|turnoacotado' internal/modulos/fronteras_test.go` · `ls internal/arranque/puente_*.go`.
- **R8.1.c** · **SI** la lista real difiere de [`arquitectura.md`](arquitectura.md) §5, **ENTONCES
  LA** sesión web **DEBERÁ** corregir esa tabla en el mismo commit y avisar en el README.

## H8.2 · Contratos y rojo de todo el módulo

> Como **la sesión web**, quiero **escribir primero el contrato y su test en rojo de los 74–75
> ficheros**, para **que los contratos se hablen entre sí antes de que exista lógica** (`05` E-2).

- **R8.2.a** · **EL** fichero de contrato **DEBERÁ** contener solo comentario-promesa, tipos,
  firmas, centinelas y cuerpos `panic(pendiente.Implementar("<pkg>.<Func>"))`. — Verifica:
  `grep -rn 'return nil, nil\|return ""$' C` sin aciertos en ficheros con `pendiente`.
- **R8.2.b** · **EL** contrato en rojo **DEBERÁ** llevar solo **exportados** (patrón F1: el linter
  `unused` rompe el gate con no exportados sin uso); los no exportados nacen con el verde. —
  Verifica: `make ci-local` rc=0 tras cada commit `rojo(conversacion)`.
- **R8.2.c** · **EL** test de cada `x.go` **DEBERÁ** mencionar cada exportado de `x.go` y llevar
  `//go:build pendiente`. — Verifica: candado `exportados_cubiertos_test.go` y
  `GOWORK=off go vet -tags pendiente ./internal/modulos/conversacion/...; echo rc=$?` → 0.
- **R8.2.d** · **SI** un test nuevo de `C` llama a `t.Skip`, **ENTONCES EL** gate **DEBERÁ**
  fallar. — Verifica: `grep -rn 't.Skip' C` vacío.
- **R8.2.e** · **DONDE** un fichero es solo de interfaces o tiene gemelo en memoria (`store`,
  `trigger`, `content`), **EL** paquete `…test` **DEBERÁ** exportar
  `func Contrato(t *testing.T, nuevo func(t *testing.T) <Puerto>)` y cada implementación
  ejecutarla. — Verifica: `go doc ./C/store/storetest Contrato`, ídem `triggertest`.
- **R8.2.f** · **DONDE** un adaptador Postgres no tiene gemelo (`events.Store`,
  `runtime.PostgresSelfNumbers`, `runtime.PostgresTenantResolver`), **EL** paquete `…test`
  **DEBERÁ** traer un doble en memoria con su propio test en verde. — Verifica: `ls C/events/eventstest C/runtime/runtimetest`.
- **R8.2.g** · **CUANDO** se cierre el bloque E, **`make test-pendiente`** **DEBERÁ** contar las
  llamadas de todo el módulo y **`make ci-local`** dar rc=0. — Verifica: los dos comandos.

## H8.3 · Verde fichero a fichero, sin olvidar comportamiento

> Como **la dueña del negocio**, quiero **que la conversación con mis clientes siga igual byte a
> byte**, para **no notar la reconstrucción**.

- **R8.3.a** · **CUANDO** un fichero pase a verde, **EL** commit **DEBERÁ** ser uno por fichero,
  quitar `//go:build pendiente` y alcanzar ≥ 80 % de sentencias (salvo adaptadores Postgres). —
  Verifica: `make cobertura-ficheros` · `git log --oneline` (un `verde(conversacion): <f>` por fichero).
- **R8.3.b** · **EL** código nuevo **DEBERÁ** conservar literal cada texto de [`diseno.md`](diseno.md)
  §5 (mensajes al cliente, cabeceras del sobre de P2, nombres de efecto y de `flow_events`, etiquetas
  de métrica, centinelas). — Verifica: test de cada fichero dueño con el literal; `grep -F` del literal en `C`.
- **R8.3.c** · **EL** carrito nuevo **DEBERÁ** producir las transcripciones de
  `testdata/cart_v{1,2}_transcript.golden.txt` idénticas (D-F8-3). — Verifica: test golden de `C/modules/cart`.
- **R8.3.d** · **SI** una regla de [`diseno.md`](diseno.md) §4 se decide no mantener, **ENTONCES EL**
  commit `verde(…)` **DEBERÁ** decirlo con el motivo (E-8).
- **R8.3.e** · **CADA** fichero nuevo **DEBERÁ** empezar con `// Porta internal/<ruta vieja> @ <sha>` (E-10). — Verifica: `grep -L '^// Porta internal/' $(find C -name '*.go' ! -name '*_test.go')` vacío.

## H8.4 · Los candados de invariante siguen expresados

> Como **Jhoan**, quiero **que las tres reglas estructurales del motor sigan vigiladas**, para
> **que un refactor no las deje ciegas sin ponerse rojo**.

- **R8.4.a** · **EL** runtime nuevo **DEBERÁ** tener un candado que falle si un `rt.store.Delete`
  no va seguido (≤ 3 sentencias, mismo bloque) de `rt.autoreplyStreaks.Close`, y que falle si deja
  de ver exactamente **6** `Delete`. — Verifica: `go test -run '^TestRacha_TodoDeleteCierraElEpisodio$' ./C/runtime/`.
- **R8.4.b** · **EL** carrito nuevo **DEBERÁ** tener un candado que falle si, en `Module.Step`, la
  petición de consulta no precede a `st.Started = true` y a `advance(`. — Verifica: `go test -run '^TestOrden_LaConsultaVaANTESDeTodaMutacion$' ./C/modules/cart/`.
- **R8.4.c** · **EL** candado de fronteras **DEBERÁ** fallar si `C/events` importa una ruta que
  contenga `intent`, `llm`, `ollama`, `openai`, `anthropic`, `clasific` o `classif` (D-F8-4). —
  Verifica: mutación local (import de `modulos/inferencia/llmvia` en `events`) pone rojo `fronteras_test.go`.
- **R8.4.d** · **EL** resumen nuevo **DEBERÁ** calcular el `TOTAL` solo de `qty × unit_price`: una
  indicación (`customization`) no lo cambia (INV-13). — Verifica: test de `C/events/summary.go`.

## H8.5 · Un solo motor vivo en el binario nuevo

> Como **el Edge**, quiero **que mis mensajes entren a UN runtime, serializado por conversación**,
> para **no recibir respuestas dobles ni estados pisados**.

- **R8.5.a** · **EL** arranque nuevo **DEBERÁ** construir **exactamente una** vez
  `runtime.New` y `runtime.NewIntakeAggregator`. — Verifica: `grep -c 'runtime\.New(' internal/arranque/*.go` → 1 y `grep -c 'NewIntakeAggregator(' …` → 1 (sin contar `_test`).
- **R8.5.b** · **EL** `Runtime` que recibe `gw.OnIncoming` **DEBERÁ** ser el mismo que sirven
  `Starter` (I4, J19) y `EventCanceller` (I19). — Verifica: test de cableado en `internal/arranque` (identidad de puntero).
- **R8.5.c** · **EL** arranque nuevo **DEBERÁ** pasar las **22** opciones de
  `construirRuntimeDeFlujos` (`fase7_flujos.go`, desde `flowruntime.New` en `:228` según FX) y los hooks `OnIncoming`, `OnHeartbeat`,
  `OnWarmup`, `OnEdgeReady` y `SetFlowAutoreplyStreakMaxSource`. — Verifica: el candado de
  cableado portado de `flow_options_cableadas_test.go` y `turno_acotado_cableado_test.go`.
- **R8.5.d** · **CUANDO** se conmute, **LA** huella del binario nuevo **DEBERÁ** coincidir con la del
  viejo en rutas, rpc, métricas y goroutines. — Verifica: `go test -run Huella ./internal/arranque/`.
- **R8.5.e** · **SI** alguien levanta `cmd/server` y `cmd/server-modular` a la vez contra la misma
  BD o puertos, **ENTONCES** la operación **DEBERÁ** considerarse inválida (no se hace). — Verifica: [`reglas.md`](reglas.md) §3.

## H8.6 · La cara HTTP queda entera en `apipublica`

> Como **la dueña del negocio**, quiero **que flujos, disparadores, contenido, catálogo y eventos
> de conversación respondan igual**, para **que la consola no cambie**.

- **R8.6.a** · **CUANDO** se conmute, **`apipublica`** **DEBERÁ** registrar las 19 rutas I1–I19 del
  mapa de FX con su permiso, recurso de auditoría, gate de feature y condición de montaje. —
  Verifica: huella de rutas; tests de FX TX.22–TX.23.
- **R8.6.b** · **EL** listener `:8100` **DEBERÁ** servir J18–J22 con los handlers de `C/admin`. — Verifica: huella de `:8100`.
- **R8.6.c** · **CUANDO** cierre F8, **EL** arranque nuevo **NO DEBERÁ** construir el `publicapi`
  viejo (FX TX.24). — Verifica: `grep -rn 'internal/publicapi' internal/arranque` vacío.
- **R8.6.d** · **SI** el `Starter` devuelve `ErrConversationExists`, `ErrDurableFlowNeedsEvent` o el
  `ErrSessionOffline` de `modulos/edge/session`, **ENTONCES** la ruta **DEBERÁ** responder el mismo
  código y cuerpo que hoy (`publicapi/flows.go:214-235`, `flujos/admin/handlers.go:326`). — Verifica: tests de `apipublica/flows.go` y `C/admin/handlers.go`.

## H8.7 · Cero puentes, cero adaptadores

> Como **Jhoan**, quiero **que al cerrar F8 el binario nuevo no importe ni una línea del código
> viejo**, para **que el relevo (F10) sea solo borrar**.

- **R8.7.a** · **CUANDO** cierre F8, **LA** lista de puentes de `fronteras_test.go` **DEBERÁ** estar
  vacía. — Verifica: el test con la lista vacía y `grep -c 'retira:' internal/modulos/fronteras_test.go` → 0.
- **R8.7.b** · **CUANDO** cierre F8, **NO DEBERÁ** quedar ningún `internal/arranque/puente_*.go`. — Verifica: `ls internal/arranque/puente_*.go` → sin coincidencias.
- **R8.7.c** · **LOS** paquetes re-tocados (`catalogo`, `catalogo/indice`,
  `solicitudes/intakes/telemetria`, `captacion/stages`, `captacion/reanalisis`, `edge/session`)
  **DEBERÁN** seguir en verde y ≥ 80 % tras el re-toque. — Verifica: `make cobertura-ficheros`.
- **R8.7.d** · **EL** centinela `ErrSessionOffline` **DEBERÁ** ser uno solo, el de
  `modulos/edge/session`, sin alias al viejo (D-FX-3 resuelto). — Verifica: `grep -rn 'gateway/session' internal/modulos` vacío.

## H8.8 · Cierre por la sesión local

> Como **la sesión local**, quiero **correr lo que la web no puede cerrar**, para **dar F8 por
> hecha con evidencia**.

- **R8.8.a** · **LA** sesión local **DEBERÁ** arrancar `cmd/server-modular` en local (nunca a la vez
  que `cmd/server`) y hacer un recorrido de conversación con un Edge de prueba o el e2e de
  `cmd/server-modular`. — Verifica: el traspaso `TRASPASO-F8-conversacion.md` con la salida.
- **R8.8.b** · **DONDE** F9 esté adelantado, **LA** sesión local **DEBERÁ** correr los procesos
  «Entrante a respuesta», «De mensaje a borrador» y «Re-análisis» contra el binario nuevo (🕐). —
  Verifica: `make test-procesos` rc=0 y 0 SKIP con `-v`.
- **R8.8.c** · **EL** cierre **DEBERÁ** reportar SKIP en código nuevo = 0. — Verifica:
  `GOWORK=off go test -v ./internal/modulos/... 2>&1 | grep -c -- '--- SKIP'` → 0.
