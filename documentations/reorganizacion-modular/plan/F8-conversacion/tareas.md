# F8 · Tareas

> Formato de [`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills:
> `reconstruir-modulo` (la fase), `contrato-tdd` (cada fichero), `validar-antes-de-cerrar` (cada
> gate), `traspaso-web-local` (bloque K → L), `procesos-testcontainers` (T8.37). `C` =
> `internal/modulos/conversacion`. La web trabaja en su rama y abre PR hacia `dev` (**sin
> squash**: rojo y verde siguen siendo commits distintos). Todo gate se lee **sin pipe**:
>
> `G`: `GOWORK=off make ci-local > /tmp/g.log 2>&1; echo GATE_RC=$? >> /tmp/g.log; tail -1 /tmp/g.log` → `GATE_RC=0`
> `V`: `GOWORK=off go vet -tags pendiente ./...; echo rc=$?` → `rc=0`
>
> **Patrón de rojo (F1)**: el contrato lleva **solo exportados** con `panic(pendiente.Implementar(…))`;
> los no exportados nacen con el verde. Cada `rojo` se comprueba corriendo **un** test suelto:
> `GOWORK=off go test -tags pendiente -run '^TestX$' ./C/<pkg>/; echo rc=$?` → rc≠0.

## Bloque A · verdad de campo e inventario · 🌐 · T8.1–T8.2
Para cuando: el README tiene el inventario re-medido, la lista real de puentes y `puente_*.go`, y D-F8-1 aplicada.

- [ ] **T8.1 · Verdad de campo** · 🌐 · dep. F7 cerrado · cumple R8.1.a
  - **Ficheros**: `plan/F8-conversacion/README.md` (estado «en curso», SHA)
  - **Hecho cuando**: las 5 entradas del README comprobadas con su comando; `go list ./internal/modulos/...` lista `acceso`, `edge`, `inferencia`, `catalogo`, `solicitudes`, `captacion`; `pendiente` = 0 fuera de F8.
  - **Gate**: `G`
  - **Commit**: `docs(reorganizacion-modular): F8 arranca — entradas verificadas`
- [ ] **T8.2 · Inventario verificado y puentes reales** · 🌐 · dep. T8.1 · cumple R8.1.a–c
  - **Ficheros**: `README.md` (tabla de tamaño), `arquitectura.md` §1 y §5 (si difieren)
  - **Hecho cuando**: re-contados ficheros/líneas/exportados/tests de los 14 paquetes (comandos de `diseno.md` §0); `grep -n 'flujos\|turnoacotado\|gateway/session' internal/modulos/fronteras_test.go` y `ls internal/arranque/puente_*.go` volcados en `arquitectura.md` §5 con «retira: T8.3x»; decidido si `send_budget_cableado_test.go` es de F3 o F8; si `C/model` ya existe (F5), T8.3 se tacha.
  - **Gate**: `G`
  - **Commit**: `docs(reorganizacion-modular): F8 — inventario y puentes verificados`

## Bloque B · contratos y rojo de las hojas · 🌐 · T8.3–T8.8
Para cuando: `model`, `trigger`, `content`, `store`, `modules` en rojo; `storetest` y `triggertest` exportan `Contrato`; `G` y `V` rc=0. (~18 ficheros de producción + 2 suites.)

- [ ] **T8.3 · rojo(conversacion): `model/model.go`** · 🌐 · dep. T8.2 · cumple R8.2.a–c — *solo si D-F8-1 = no*
  - **Ficheros**: `C/model/model.go`, `…/model_test.go`
  - **Hecho cuando**: 24 exportados con promesa; `ErrInvalidFlow` y sus textos de `diseno.md` §1.1 escritos en el comentario; test rojo por `-run`.
  - **Gate**: `V` · test suelto rc≠0
  - **Commit**: `rojo(conversacion): contrato de model`
- [ ] **T8.4 · rojo(conversacion): `trigger` (5) y `triggertest.Contrato`** · 🌐 · dep. T8.3 · cumple R8.2.a–e
  - **Ficheros**: `C/trigger/{trigger,config_resolver,store,store_memory,store_postgres}.go` + tests; `C/trigger/triggertest/contrato.go`
  - **Hecho cuando**: 54 exportados; `store.go` sin test propio (puerto, E-3) y su suite corrida desde `store_memory_test.go`; regla D-5 (`KindLLM`) escrita; `ErrTriggerNotFound` literal.
  - **Gate**: `V` · `go doc ./internal/modulos/conversacion/trigger/triggertest Contrato`
  - **Commit**: `rojo(conversacion): contrato de trigger y su suite`
- [ ] **T8.5 · rojo(conversacion): `content` (4)** · 🌐 · dep. T8.3 · cumple R8.2.a–c
  - **Ficheros**: `C/content/{content,static,json,router}.go` + tests (`content.go` es puerto: lo prueban los de sus 3 implementaciones)
  - **Hecho cuando**: 11 exportados; textos de error de `diseno.md` §1.3 en los comentarios.
  - **Gate**: `V`
  - **Commit**: `rojo(conversacion): contrato de content`
- [ ] **T8.6 · rojo(conversacion): `store` (3) y `storetest.Contrato`** · 🌐 · dep. T8.3 · cumple R8.2.a–e
  - **Ficheros**: `C/store/{store,repository_memory,repository_postgres}.go` + tests; `C/store/storetest/contrato.go`
  - **Hecho cuando**: 107 exportados; la suite cubre las 13 interfaces de `store.go` (lectura de los 18 tests viejos, 12 de integración); `repository_memory` con **reloj inyectable**; `repository_postgres_test.go` solo lo que no necesita BD.
  - **Gate**: `V` · `go doc ./internal/modulos/conversacion/store/storetest Contrato`
  - **Commit**: `rojo(conversacion): contrato de store y su suite`
- [ ] **T8.7 · rojo(conversacion): `modules` (5)** · 🌐 · dep. T8.3 · cumple R8.2.a–c
  - **Ficheros**: `C/modules/{registry,ports,numbered,consulta,coerce}.go` + tests
  - **Hecho cuando**: 59 exportados; `ports.go` con aserciones de compilación en el test de sus implementadores.
  - **Gate**: `V`
  - **Commit**: `rojo(conversacion): contrato de modules`
- [ ] **T8.8 · Cierre del bloque B** · 🌐 · dep. T8.3–T8.7
  - **Hecho cuando**: `grep -rn 'pendiente.Implementar' --include='*.go' C | wc -l` anotado; `G` rc=0; PR abierto.
  - **Gate**: skill `validar-antes-de-cerrar`

## Bloque C · rojo del motor y sus satélites · 🌐 · T8.9–T8.14
Para cuando: `engine`, `menu`, `survey`, `media`, `turnoacotado`, `events`, `admin` en rojo; `eventstest` con doble en verde; `G`, `V` rc=0. (~20 ficheros + doble.)

- [ ] **T8.9 · rojo(conversacion): `engine` (2)** · 🌐 · dep. T8.8 · cumple R8.2.a–c
  - **Ficheros**: `C/engine/{engine,consulta}.go` + tests
  - **Hecho cuando**: 22 exportados; «núcleo puro» y «cardinalidad acotada del observador» escritos.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de engine`
- [ ] **T8.10 · rojo(conversacion): `menu`, `survey` (2), `media`** · 🌐 · dep. T8.9
  - **Ficheros**: `C/modules/menu/menu.go`, `C/modules/survey/{survey,projection}.go`, `C/modules/media/media.go` + tests
  - **Hecho cuando**: 35 exportados; textos de `media` literales.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de menu, survey y media`
- [ ] **T8.11 · rojo(conversacion): `turnoacotado` (3)** · 🌐 · dep. T8.9
  - **Ficheros**: `C/turnoacotado/{turnoacotado,troceado,prompt}.go` + tests
  - **Hecho cuando**: importa `modulos/inferencia/llmvia` (no el viejo); `prompt.go` sin exportados con su test rojo a través de la API.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de turnoacotado`
- [ ] **T8.12 · rojo(conversacion): `events` (7) y el doble `eventstest`** · 🌐 · dep. T8.4 · cumple R8.2.f, R8.4.d
  - **Ficheros**: `C/events/{events,kinds,dispatcher,menu,summary,thread_reader,store}.go` + tests; `C/events/eventstest/store.go` (+ test **verde**)
  - **Hecho cuando**: 114 exportados; reglas de `diseno.md` §2.4 (INV-13 incluida) en los comentarios; D-1 citada en `store.go`; el doble satisface `EventStore`, `SummaryAppender`, `ThreadReader`.
  - **Gate**: `V` · `go test ./internal/modulos/conversacion/events/eventstest/; echo rc=$?` → 0
  - **Commit**: `rojo(conversacion): contrato de events y su doble en memoria`
- [ ] **T8.13 · rojo(conversacion): `admin` (4, sin `sessions.go`)** · 🌐 · dep. T8.6, T8.9 · cumple R8.6.d
  - **Ficheros**: `C/admin/{doc,handlers,triggers,durable_flow}.go` + tests (menos `doc.go`)
  - **Hecho cuando**: 16 exportados; **sin** `Register` (D-F8-2, dicho en el commit); `handlers.go` compara `modulos/edge/session.ErrSessionOffline`.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de admin (sin Register, D-7)`
- [ ] **T8.14 · Cierre del bloque C** · 🌐 · dep. T8.9–T8.13 · **Gate**: `validar-antes-de-cerrar`

## Bloque D · rojo del carrito · 🌐 · T8.15–T8.17
Para cuando: los 14 ficheros de `cart` en rojo, goldens copiados, candado de orden escrito. (14 + testdata.)

- [ ] **T8.15 · rojo(conversacion): `cart` — estado y bordes (8)** · 🌐 · dep. T8.7, T8.6
  - **Ficheros**: `C/modules/cart/{state,effects,variants,validate,buyer,revalidate,resume,prime}.go` + tests
  - **Hecho cuando**: importa `modulos/catalogo` y `modulos/solicitudes/intakes`; nombres de efecto y textos literales; `revalidate.go` devuelve `intakes.PriceList`.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de cart (estado y bordes)`
- [ ] **T8.16 · rojo(conversacion): `cart` — el módulo (6) y los goldens** · 🌐 · dep. T8.15 · cumple R8.3.c
  - **Ficheros**: `C/modules/cart/{cart,troceo,preresolutor,screens,consulta,projection}.go` + tests; `C/modules/cart/testdata/cart_v{1,2}_transcript.golden.txt` (copia literal, D-F8-3)
  - **Hecho cuando**: H24, H29, ola6 y 054 escritos como promesas de `cart.go`/`projection.go`; el test golden compila y cae en rojo.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de cart (módulo) y sus goldens`
- [ ] **T8.17 · rojo(conversacion): candado de orden de `Step`** · 🌐 · dep. T8.16 · cumple R8.4.b
  - **Ficheros**: `C/modules/cart/orden_consulta_ast_test.go` (con `//go:build pendiente` hasta el verde de `cart.go`)
  - **Hecho cuando**: exige una de cada pieza; comprobado que **ve** `Step` cuando exista el cuerpo.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): candado del orden de la consulta en cart`

## Bloque E · rojo del runtime · 🌐 · T8.18–T8.21
Para cuando: los 23 ficheros de `runtime` en rojo, `runtimetest` en verde, candado de rachas escrito; `make test-pendiente` cuenta **todo** el módulo; `G`, `V` rc=0.

- [ ] **T8.18 · rojo(conversacion): runtime — soporte (12) y `runtimetest`** · 🌐 · dep. T8.12, T8.16 · cumple R8.2.a–f
  - **Ficheros**: `C/runtime/{runtime,keyedmutex,event_sink,log_sink,tenant_resolver,self_numbers,summary_sources,streak,welcome,thread,send,webhook_sink}.go` + tests; `C/runtime/runtimetest/*.go` (+ tests verdes)
  - **Hecho cuando**: puertos de `runtime.go` con los motivos y perfiles; `webhook_sink` importa `modulos/solicitudes/integrations/crmpush`; dobles de §4 de `diseno.md`.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato del runtime (soporte) y sus dobles`
- [ ] **T8.19 · rojo(conversacion): runtime — núcleo (11)** · 🌐 · dep. T8.18 · cumple R8.2.a–c
  - **Ficheros**: `C/runtime/{persist_sink,event_effects,source_composer,aggregator,runtime_engine,resume,start,exit_menu,event_lifecycle,events,incoming}.go` + tests
  - **Hecho cuando**: reglas RT-1…RT-20 y AG-1…AG-8 en los comentarios de su fichero; `aggregator.go` importa `modulos/captacion/intake`; `runtime_engine.go` declara las 22 `With*`; literales de §5.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato del runtime (núcleo)`
- [ ] **T8.20 · rojo(conversacion): candado de rachas** · 🌐 · dep. T8.19 · cumple R8.4.a
  - **Ficheros**: `C/runtime/streak_invariante_test.go` (`//go:build pendiente` hasta el verde de `incoming.go`/`events.go`)
  - **Hecho cuando**: constante `esperados` a **re-medir** en el verde (hoy 6).
  - **Gate**: `V` · **Commit**: `rojo(conversacion): candado de cierre de rachas`
- [ ] **T8.21 · Cierre de la pasada de contratos** · 🌐 · dep. T8.3–T8.20 · cumple R8.2.g
  - **Hecho cuando**: `make test-pendiente` = `grep -rn 'pendiente.Implementar' --include='*.go' C | wc -l` (anotar N); `G` rc=0; D-F8-4 aplicado en `fronteras_test.go` (regla «`events` sin clasificador») y lista blanca de `conversacion` escrita (`arquitectura.md` §3).
  - **Gate**: `validar-antes-de-cerrar` · **Commit**: `rojo(conversacion): fronteras del módulo`

## Bloque F · verde de las hojas · 🌐 · T8.22–T8.23
Para cuando: 27 ficheros verdes, un commit cada uno, ≥ 80 %.

- [ ] **T8.22 · verde(conversacion): `model`, `trigger`, `content`, `store`, `modules`** · 🌐 · dep. T8.21 · cumple R8.3.a–e
  - **Ficheros**: los 18 del bloque B, en el orden de `arquitectura.md` §2
  - **Hecho cuando**: por fichero, `GOWORK=off go test -race ./C/<pkg>/; echo rc=$?` → 0 y `make cobertura-ficheros` ≥ 80 % (fuera `repository_postgres.go`, `store_postgres.go`); `storetest`/`triggertest` verdes contra memoria.
  - **Gate**: `G` por commit · **Commit**: `verde(conversacion): <fichero>` (uno por fichero)
- [ ] **T8.23 · verde(conversacion): `engine`, `menu`, `survey`, `media`, `turnoacotado`** · 🌐 · dep. T8.22
  - **Ficheros**: los 9 · **Gate**: ídem · **Commit**: ídem

## Bloque G · verde de `events`, `admin` y `cart` · 🌐 · T8.24–T8.25
Para cuando: 25 ficheros verdes; goldens idénticos; candado de orden sin etiqueta y verde.

- [ ] **T8.24 · verde(conversacion): `events` (7) y `admin` (4)** · 🌐 · dep. T8.23
  - **Hecho cuando**: ≥ 80 % salvo `events/store.go`, `events/thread_reader.go`; textos del menú literales.
  - **Gate**: `G` por commit · **Commit**: `verde(conversacion): <fichero>`
- [ ] **T8.25 · verde(conversacion): `cart` (14)** · 🌐 · dep. T8.24 · cumple R8.3.c, R8.4.b
  - **Hecho cuando**: test golden verde **sin** `-update`; `orden_consulta_ast_test.go` sin `//go:build pendiente` y verde; mutación local del orden → rojo (se deshace sin commitear).
  - **Gate**: `G` · **Commit**: `verde(conversacion): <fichero>`

## Bloque H · verde del runtime (I) · 🌐 · T8.26
Para cuando: los 12 de soporte verdes.

- [ ] **T8.26 · verde(conversacion): runtime — soporte (12)** · 🌐 · dep. T8.25
  - **Hecho cuando**: `go test -race` rc=0 por commit; ≥ 80 % salvo `self_numbers.go`, `tenant_resolver.go`; `streak.go` con reloj inyectado (T-7).
  - **Gate**: `G` · **Commit**: `verde(conversacion): <fichero>`

## Bloque I · verde del runtime (II) · 🌐 · T8.27–T8.28
Para cuando: `grep -rn 'pendiente.Implementar' --include='*.go' C | wc -l` → **0**.

- [ ] **T8.27 · verde(conversacion): runtime — núcleo (11)** · 🌐 · dep. T8.26
  - **Ficheros**: en el orden `persist_sink`, `event_effects`, `source_composer`, `aggregator`, `runtime_engine`, `resume`, `start`, `exit_menu`, `event_lifecycle`, `events`, `incoming`. Si `events.go` (1.501 l) o `incoming.go` (1.328 l) no caben en una sesión, cada uno es **su** sesión.
  - **Hecho cuando**: ≥ 80 % por fichero; `streak_invariante_test.go` sin etiqueta y verde con la constante re-medida; mutación local (quitar un `Close`) → rojo.
  - **Gate**: `G` · **Commit**: `verde(conversacion): <fichero>`
- [ ] **T8.28 · refactor y cierre de los verdes** · 🌐 · dep. T8.27
  - **Hecho cuando**: `pendiente` del módulo = 0; SKIP = 0; los `refactor(conversacion): …` que hagan falta con los tests verdes.
  - **Gate**: `validar-antes-de-cerrar`

## Bloque J · la cara: conversación en `apipublica` · 🌐 · T8.29
Para cuando: los handlers de I1–I19 verdes en `apipublica`, **sin montar** todavía.

- [ ] **T8.29 · FX TX.22–TX.23: rojo y verde de los ficheros de conversación de `apipublica`** · 🌐 · dep. T8.28 · cumple R8.6.a, R8.6.d
  - **Ficheros**: los que liste [`../FX-cara-http/tareas.md`](../FX-cara-http/tareas.md) TX.22 (flujos, contenido de tenant, media, disparadores, catálogo, eventos de conversación)
  - **Hecho cuando**: lo dicen TX.22 y TX.23 de FX; importan **solo** `C/**`, `nucleo`, `platform` y módulos nuevos.
  - **Gate**: los de FX · **Commit**: los de FX

## Bloque K · conmutar y retirar todos los puentes · 🌐→💻 · T8.30–T8.35
Para cuando: huella igual, 0 puentes, 0 adaptadores, `cmd/server-modular` compila sin un solo paquete viejo; traspaso escrito.
⚠️ T8.31–T8.34 **no compilan por separado** (los paquetes re-tocados cambian de tipos y el
arranque tiene que pasarles los nuevos a la vez): van en **un** commit `conmutar(conversacion)`,
con un párrafo por tarea en el mensaje.

- [ ] **T8.30 · Ensayo en seco de la conmutación** · 🌐 · dep. T8.29
  - **Hecho cuando**: rama local con T8.31–T8.34 aplicados compila (`GOWORK=off go build ./...; echo rc=$?` → 0) y la lista de ficheros tocados está en el traspaso; se descarta si la huella no cuadra.
  - **Gate**: `go build` rc=0
- [ ] **T8.31 · Retirar los puentes de import de F5–F7** · 🌐 · dep. T8.30 · cumple R8.7.a, R8.7.c
  - **Ficheros**: `internal/modulos/catalogo/catalog.go` y `catalogo/indice/cache.go` (si D-F8-1 = no) · `solicitudes/intakes/telemetria/telemetria.go` · `captacion/stages/draft.go` · `captacion/reanalisis/reanalisis.go` · sus `_test.go` · `internal/modulos/fronteras_test.go` (lista de puentes → vacía)
  - **Hecho cuando**: `grep -rn 'internal/flujos\|internal/turnoacotado' internal/modulos --include='*.go'` → vacío; cada fichero re-tocado ≥ 80 %.
- [ ] **T8.32 · Borrar los adaptadores `internal/arranque/puente_*.go`** · 🌐 · dep. T8.31 · cumple R8.7.b
  - **Ficheros**: todos los `puente_*.go` y sus tests (lista de T8.2); `internal/arranque/fase{3,5,6,8,9}_*.go` re-cableados con los tipos nuevos
  - **Hecho cuando**: `ls internal/arranque/puente_*.go` → nada (quedaban `puente_contact`, `puenteTurnero` de `puente_inferencia` y `puente_captacion`: [`../00-marco/estructura.md`](../00-marco/estructura.md) §2.1); fuera las dos **segundas instancias viejas** (`intakes.Postgres` de F6, `intake.Postgres` de F7); un solo `entResolver`, un solo `flowDeps.kp`, un solo `gw` (T-2, T-3).
- [ ] **T8.33 · `fase7_flujos.go` y `:8100`** · 🌐 · dep. T8.32 · cumple R8.5.a–c, R8.6.b
  - **Ficheros**: `internal/arranque/fase7_flujos.go`, `rutas_admin.go` (J18–J22 con `C/admin`), los candados de cableado portados (`flow_options_cableadas`, `turno_acotado_cableado`)
  - **Hecho cuando**: un `runtime.New`, un `NewIntakeAggregator`, las 22 opciones, los 4 hooks y la fuente del gauge; test de identidad del `Runtime` (gateway = Starter = EventCanceller).
- [ ] **T8.34 · FX TX.24: 19 rutas, cara vieja fuera y centinela único** · 🌐 · dep. T8.33 · cumple R8.6.a, R8.6.c, R8.7.d
  - **Ficheros**: los de TX.24 de FX. Con **D-F3-2** (recomendación) `edge/session.ErrSessionOffline` ya es el de `platform` desde F3 y **no se toca**; solo si D-F3-2 = no (D-FX-3) se edita `internal/modulos/edge/session/registry.go` para que deje de ser alias del viejo
  - **Hecho cuando**: `grep -rn 'internal/publicapi\|internal/gateway/session' internal/arranque internal/modulos internal/apipublica` → vacío.
  - **Gate** (T8.31–T8.34 juntos): `G` · `V` · `go test -run Huella ./internal/arranque/` rc=0 · `go list -deps ./cmd/server-modular` sin paquetes viejos (`reglas.md` §4.5)
  - **Commit**: `conmutar(conversacion): el arranque nuevo cablea conversacion, 19+5 rutas y cero puentes`
- [ ] **T8.35 · Traspaso a la sesión local** · 🌐 · dep. T8.34
  - **Ficheros**: `documentations/reorganizacion-modular/traspasos/TRASPASO-F8-conversacion.md` (skill `traspaso-web-local`; §7 con lo que no se pudo comprobar: la identidad de `entResolver`/`kp`/`gw` en ejecución, el barrido del agregador con BD real, el golden en un Edge real)
  - **Commit**: `docs(reorganizacion-modular): traspaso F8`

## Bloque L · cierre · 💻 · T8.36–T8.38
Para cuando: la definición de hecho de [`reglas.md`](reglas.md) §4 entera.

- [ ] **T8.36 · Gates y arranque local del binario nuevo** · 💻 · dep. T8.35 · cumple R8.8.a, R8.8.c
  - **Hecho cuando**: `validar-antes-de-cerrar` con la toolchain fijada; `cmd/server-modular` arranca en local **solo** (sin `cmd/server`), un Edge de prueba (o el e2e de `cmd/server-modular`) recorre «carrito» → línea → confirmar y el cliente recibe las pantallas; SKIP = 0.
  - **Gate**: `G` · e2e rc=0
- [ ] **T8.37 · 🕐 Procesos del módulo contra el binario nuevo (= T9.29, 9C de `conversacion`)** · 🌐→💻 · dep. T8.36 · cumple R8.8.b — *con D-F9-1 = sí (recomendación); si D-F9-1 = no, se tacha y lo cubre T9.34 tras F8*
  - **Hecho cuando**: T9.29 (9C de `conversacion`, [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md)) pasa: «Entrante a respuesta», «De mensaje a borrador», «Re-análisis», con las aserciones de BD de `diseno.md` §4.2 (D-054.4, 23502, `ON CONFLICT` de la ventana); `make test-procesos` rc=0 y 0 SKIP con `-v`.
  - **Gate**: `make test-procesos > /tmp/p.log 2>&1; echo RC=$? >> /tmp/p.log; tail -1 /tmp/p.log`
- [ ] **T8.38 · Cerrar F8** · 💻 · dep. T8.36 (y T8.37 si aplica)
  - **Ficheros**: `ESTADO.md`, este `README.md` (estado «cerrada», SHA), sección `CERRADO <fecha>` del traspaso
  - **Hecho cuando**: integrado en `dev` sin squash; `origin/dev` al día; la siguiente es F9/F10 (decisión de Jhoan).
  - **Commit**: `docs(reorganizacion-modular): F8 cerrada`
