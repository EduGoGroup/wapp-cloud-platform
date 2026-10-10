# F8 · Tareas

> Formato de [`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills:
> `reconstruir-modulo` (la fase), `contrato-tdd` (cada fichero), `validar-antes-de-cerrar` (cada
> gate), `procesos-testcontainers` (T8.37). `C` = `internal/modulos/conversacion`. **Las ocho sesiones** (✎ 2026-10-10, D-F8-11: eran siete; F8-04 se parte en F8-04 y F8-04b)
> **son 💻**: commits en **la rama de la sesión** (partida de `dev`), push de esa rama y PR a `dev` sin squash (regla 6
> del `CLAUDE.md`; ✎ F8-01: aquí decía «commits en `dev`, sin PR»). Donde una tarea diga «`dev` empujado», léase «rama empujada y PR abierto». Rojo y verde siguen siendo commits distintos. Todo gate se lee **sin pipe**:
>
> `G`: `GOWORK=off make ci-local > /tmp/g.log 2>&1; echo GATE_RC=$? >> /tmp/g.log; tail -1 /tmp/g.log` → `GATE_RC=0`
> `V`: `GOWORK=off go vet -tags pendiente ./...; echo rc=$?` → `rc=0`
>
> **Nivel de ceremonia (`05` E-12)**: el del inventario que Jhoan aprueba en T8.2 (provisional en
> [`diseno.md`](diseno.md) §0.1). Las tareas `rojo` y `verde` describen el esquema completo, que es el del nivel
> **complejo**. En **medio**, rojo y verde por archivo agrupados por paquete. En **simple**, la `rojo` y su parte
> de la `verde` se hacen en **una pasada** y las dos se marcan `[x]` con el mismo SHA.
> **Sin umbral de cobertura (P2)**: un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9.
> `make cobertura-ficheros` es un informe: su tabla va al bloque de `ESTADO.md`; no bloquea.
>
> **Patrón de rojo (F1)**: el contrato lleva **solo exportados** con `panic(pendiente.Implementar(…))`;
> los no exportados nacen con el verde, y su test también, **solo si llevan regla de negocio o ramas no
> triviales** (P6; ni fontanería ni `if err != nil`; el resto lo cubre F9). Cada `rojo` se comprueba corriendo
> **un** test suelto: `GOWORK=off go test -tags pendiente -run '^TestX$' ./C/<pkg>/; echo rc=$?` → rc≠0.
>
> **Adaptadores (E-11, `05` §4.2)**: el fichero de adaptador deja el prefijo `puente` y es `bridge_<x>.go` (`contact`, `iam`,
> `gateway`, `inferencia`, `captacion`, `intakes`); tipos `puenteTurnero` → `turneroBridge`, `puenteConfigLLM` → `llmConfigBridge`,
> `puenteGateway` → `gatewayBridge`. Los «puentes» de `05` §4.1 (imports) no cambian de nombre.

## Bloque 1 · inventario E-12 y hojas · 💻 · sesión F8-01 · T8.1–T8.8, T8.22
Para cuando: el inventario E-12 está **aprobado por Jhoan** (antes no se escribe código); `model`, `trigger`, `content`, `store`, `modules` verdes; `storehelpertest` y `triggerhelpertest` exportan `Contrato` y corren en memoria y en Postgres; `G` y `V` rc=0. (~18 ficheros de producción + 2 suites.)

- [x] **T8.1 · Verdad de campo** · 💻 · dep. F7 cerrado · cumple R8.1.a — `7e24504` (el `G` de esta tarea se corrió al cierre, sobre `3a408dc`, no al arrancar: T8.8)
  - **Ficheros**: `plan/F8-conversacion/README.md` (estado «en curso», SHA)
  - **Hecho cuando**: las 5 entradas del README comprobadas con su comando; `go list ./internal/modulos/...` lista `acceso`, `edge`, `inferencia`, `catalogo`, `solicitudes`, `captacion`; `pendiente` = 0 fuera de F8.
  - **Gate**: `G`
  - **Commit**: `docs(reorganizacion-modular): F8 arranca — entradas verificadas`
- [x] **T8.2 · Inventario E-12** · 💻 · dep. T8.1 · cumple R8.1.a–d — `98a6214` (aprobado por Jhoan el 2026-10-09; inventario en el README de la fase)
  - **Ficheros**: `README.md` (tabla de tamaño y tabla de niveles), `arquitectura.md` §1 y §5 (si difieren), `diseno.md` §0.1
  - **Produce**: (1) la tabla `archivo · estado en memoria · concurrencia · BD/transacciones · nº de consumidores · nivel (simple/medio/complejo)` de los 74–75 ficheros, re-contados (ficheros/líneas/exportados/tests de los 14 paquetes, comandos de `diseno.md` §0); (2) la **lista de adaptadores `bridge_<x>.go`**: F8 **crea 0** y **retira** todos los vivos — `ls internal/arranque/bridge_*.go` y las segundas instancias viejas, volcados en `arquitectura.md` §5.2 con «retira: T8.32» y el módulo que entra en `Conmutados`; (3) los puentes (import): `grep -n 'flujos\|turnoacotado\|gateway/session' internal/modulos/fronteras_test.go` en §5.1 con «retira: T8.31»; (4) decidido si `send_budget_cableado_test.go` es de F3 o F8; si `C/model` ya existe (F5), T8.3 se tacha.
  - **Hecho cuando**: **Jhoan aprueba la tabla. Antes de eso no se escribe código.** Si un archivo sale peor de lo previsto, sube de nivel.
  - **Gate**: `G`
  - **Commit**: `docs(reorganizacion-modular): F8 — inventario E-12 y adaptadores verificados`
- [x] ~~**T8.3 · rojo(conversacion): `model/model.go`**~~ — **tachada**: D-F8-1 = D-F5-1, `C/model` lo reconstruyó F5 y está verde (441 l, 24 exportados)
  - **Ficheros**: `C/model/model.go`, `…/model_test.go`
  - **Hecho cuando**: 24 exportados con promesa; `ErrInvalidFlow` y sus textos de `diseno.md` §1.1 escritos en el comentario; test rojo por `-run`.
  - **Gate**: `V` · test suelto rc≠0
  - **Commit**: `rojo(conversacion): contrato de model`
- [x] **T8.4 · rojo(conversacion): `trigger` (5) y `triggerhelpertest.Contrato`** · 💻 · dep. T8.3 · cumple R8.2.a–e — `907839a` (`trigger.go` y `store.go`, nivel simple, nacen ya verdes en este commit)
  - **Ficheros**: `C/trigger/{trigger,config_resolver,store,store_memory,store_postgres}.go` + tests; `C/trigger/triggerhelpertest/contrato.go`
  - **Hecho cuando**: 54 exportados; `store.go` sin test propio (puerto, E-3) y su suite `Contrato(t, func(t) Montaje)` corrida desde `store_memory_test.go` y, con el arnés de F9-A, contra Postgres (P4); regla D-5 (`KindLLM`) escrita; `ErrTriggerNotFound` literal; el corpus de normalización lleva casos adversarios (`reglas.md` §0).
  - **Gate**: `V` · `go doc ./internal/modulos/conversacion/trigger/triggerhelpertest Contrato`
  - **Commit**: `rojo(conversacion): contrato de trigger y su suite`
- [x] **T8.5 · rojo(conversacion): `content` (4)** · 💻 · dep. T8.3 · cumple R8.2.a–c — `15524e1` (nivel simple: una pasada, mismo SHA que su verde)
  - **Ficheros**: `C/content/{content,static,json,router}.go` + tests (`content.go` es puerto: lo prueban los de sus 3 implementaciones)
  - **Hecho cuando**: 11 exportados; textos de error de `diseno.md` §1.3 en los comentarios.
  - **Gate**: `V`
  - **Commit**: `rojo(conversacion): contrato de content`
- [x] **T8.6 · rojo(conversacion): `store` (3) y `storehelpertest.Contrato`** · 💻 · dep. T8.3 · cumple R8.2.a–e — `b579591` (`store (3)` nace partido en **12** ficheros por E-13; suite de 61 casos en 15 ficheros; los 4 `store*.go` de declaraciones nacen verdes)
  - **Ficheros**: `C/store/{store,repository_memory,repository_postgres}.go` + tests; `C/store/storehelpertest/contrato.go`
  - **Hecho cuando**: 107 exportados; la suite `Contrato(t, func(t) Montaje)` cubre las 13 interfaces de `store.go` (lectura de los 18 tests viejos, 12 de integración) y su marca de estado vigila **todas** las columnas que cada operación puede tocar (hallazgo 35); `repository_memory` con **reloj inyectable**; `repository_postgres_test.go` corre la suite con el arnés de F9-A (P4).
  - **Gate**: `V` · `go doc ./internal/modulos/conversacion/store/storehelpertest Contrato`
  - **Commit**: `rojo(conversacion): contrato de store y su suite`
- [x] **T8.7 · rojo(conversacion): `modules` (5)** · 💻 · dep. T8.3 · cumple R8.2.a–c — `ed3b867` (`ports.go` y `coerce.go`, nivel simple, nacen ya verdes)
  - **Ficheros**: `C/modules/{registry,ports,numbered,consulta,coerce}.go` + tests
  - **Hecho cuando**: 59 exportados; `ports.go` con aserciones de compilación en el test de sus implementadores.
  - **Gate**: `V`
  - **Commit**: `rojo(conversacion): contrato de modules`
- [x] **T8.22 · verde(conversacion): `model`, `trigger`, `content`, `store`, `modules`** · 💻 · dep. T8.3–T8.7 · cumple R8.3.a–e — `content` `15524e1` · `modules` `e104f19` · `trigger` `08c3aff` · `store` `24524de` (`repository_memory.go` + `_tenant_content.go`, juntos por el lint `unused`), `a8450e2`, `f8d0294`, `9a3f7b3`, `3b1d3f9`, `96865ce`, `3a408dc`; 37 mutantes del SQL de `store` contra Postgres, 37 muertos
  - **Ficheros**: los 18 de T8.3–T8.7, en el orden de `arquitectura.md` §2
  - **Hecho cuando**: por paquete, `GOWORK=off go test -race ./C/<pkg>/; echo rc=$?` → 0; un test por promesa del contrato; `storehelpertest`/`triggerhelpertest` verdes **en memoria y en Postgres** (P4), que es lo que da la verdad de `repository_postgres.go` y `store_postgres.go`; mutantes en lo que el inventario marque complejo (`store`).
  - **Gate**: `G` por commit · **Commit**: `verde(conversacion): <fichero>` (uno por fichero en complejo; por paquete en medio; una pasada en simple)
- [x] **T8.8 · Cierre del bloque 1** · 💻 · dep. T8.3–T8.7, T8.22 — commit de cierre de F8-01 (el que trae esta línea): `pendiente` = 0, `G` `GATE_RC=0` sobre `3a408dc`, rama empujada y PR a `dev`
  - **Hecho cuando**: `grep -rn 'pendiente.Implementar' --include='*.go' C | wc -l` → 0; `G` rc=0; las tres cosas del cierre (tareas `[x]` con SHA, bloque en `ESTADO.md`, hallazgos en el README); `dev` empujado.
  - **Gate**: skill `validar-antes-de-cerrar`

> 🔤 **Correspondencia de nombres (E-11) que F8-01 deja a las sesiones siguientes** — `modules/consulta.go` tenía los
> exportados en español; el nuevo los lleva en inglés (el fichero conserva su nombre; los valores observables no cambian:
> `"consulta_veredicto"`, `"opcion"`, `"cantidad"`, `"sin_resolutor"`, `"fallo"`, `"no_concluyente"`). `engine`, `cart` y
> `turnoacotado` usan estos:
>
> | Viejo | Nuevo |
> |---|---|
> | `Consulta` {`Clase`, `Nivel`, `Texto`, `Opciones`, `Trozos`} · `Result.Consulta` | `Query` {`Class`, `Level`, `Text`, `Options`, `Chunks`} · `Result.Query` |
> | `Veredicto` {`Codigo`, `Motivo`, `Codigos`} · `Resuelto` / `ResueltoAlguno` | `Verdict` {`Code`, `Reason`, `Codes`} · `Resolved` / `ResolvedAny` |
> | `ClaseConsulta`, `ClaseOpcion`, `ClaseCantidad` | `QueryClass`, `QueryClassOption`, `QueryClassQuantity` |
> | `OpcionConsulta` {`Codigo`, `Etiqueta`} | `QueryOption` {`Code`, `Label`} |
> | `MotivoConsulta`, `MotivoSinResolutor`, `MotivoFallo`, `MotivoNoConcluyente` | `QueryReason`, `QueryReasonNoResolver`, `QueryReasonFailure`, `QueryReasonInconclusive` |
> | `VarConsultaVeredicto` · `VeredictoDe` / `ConVeredicto` / `StripConsultaVeredicto` | `VarQueryVerdict` · `VerdictFrom` / `WithVerdict` / `StripQueryVerdict` |
>
> En `store` y `trigger` los exportados ya estaban en inglés y se conservan; en `store`, los no exportados `isUUID`,
> `intakeHeaderCols` y `scanIntakeHeader` son nombres nuevos. Las suites de Postgres viven en
> `test/procesos/{flowstore,trigger}_contrato_test.go` (no en `repository_postgres_test.go`, como decía T8.6): es el patrón de F9-A.

## Bloque 2 · el motor · 💻 · sesión F8-02 · T8.9–T8.11, T8.14, T8.23
Para cuando: `engine`, `menu`, `survey`, `media`, `turnoacotado` verdes; `G`, `V` rc=0. (9 ficheros.)

- [x] **T8.9 · rojo(conversacion): `engine` (2)** · 💻 · dep. T8.8 · cumple R8.2.a–c — `d8fd4ac` (22 exportados: `engine.go` 12, `consulta.go` 10; los de `consulta.go`, en inglés: tabla de abajo)
  - **Ficheros**: `C/engine/{engine,consulta}.go` + tests
  - **Hecho cuando**: 22 exportados; «núcleo puro» y «cardinalidad acotada del observador» escritos.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de engine`
- [x] **T8.10 · rojo(conversacion): `menu`, `survey` (2), `media`** · 💻 · dep. T8.9 — nivel simple, una pasada, mismo SHA que su verde: `menu` `b566ae0` · `survey` `fc085d5` · `media` `17ca94d` (35 exportados: 9 + 9 + 6 + 11)
  - **Ficheros**: `C/modules/menu/menu.go`, `C/modules/survey/{survey,projection}.go`, `C/modules/media/media.go` + tests
  - **Hecho cuando**: 35 exportados; textos de `media` literales.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de menu, survey y media`
- [x] **T8.11 · rojo(conversacion): `turnoacotado` (3)** · 💻 · dep. T8.9 — `346cf73` (9 exportados: `turnoacotado.go` 6, `troceado.go` 3, `prompt.go` 0; `prompt.go` nace en el verde —solo no exportados, lint `unused`— y su test rojo va por la API; la arista `conversacion → inferencia` ya estaba en `Capas`: no se tocó `fronteras_test.go`)
  - **Ficheros**: `C/turnoacotado/{turnoacotado,troceado,prompt}.go` + tests
  - **Hecho cuando**: importa `modulos/inferencia/llmvia` (no el viejo) y la arista `conversacion → inferencia` queda en la lista blanca de `fronteras_test.go`; `prompt.go` sin exportados con su test rojo a través de la API.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de turnoacotado`
- [x] **T8.23 · verde(conversacion): `engine`, `menu`, `survey`, `media`, `turnoacotado`** · 💻 · dep. T8.9–T8.11 — `menu` `b566ae0` · `survey` `fc085d5` · `media` `17ca94d` · `engine` `8f5f667` (los dos ficheros juntos: se llaman entre sí y separados no pasan `unused`) · `turnoacotado` `08b1e64` (los tres juntos, por lo mismo) · `4ce435f` (aserción de compilación `Resolver` ⊨ `engine.QueryResolver`). El `G` se corrió **una vez**, sobre `4ce435f`, no por commit
  - **Ficheros**: los 9
  - **Hecho cuando**: `go test -race` rc=0 por paquete; un test por promesa del contrato.
  - **Gate**: `G` por commit · **Commit**: `verde(conversacion): <fichero>` (según nivel, como T8.22)
- [x] **T8.14 · Cierre del bloque 2** · 💻 · dep. T8.9–T8.11, T8.23 — commit de cierre de F8-02 (el que trae esta línea): `pendiente` = 0, `G` `GATE_RC=0` sobre `4ce435f`, rama `reorg/f8-02-motor` empujada y PR #61 integrado en `dev` (merge `df340a6`)
  - **Hecho cuando**: `pendiente` del módulo = 0; las tres cosas del cierre; `dev` empujado.
  - **Gate**: `validar-antes-de-cerrar`

> 🔤 **Correspondencia de nombres (E-11) que F8-02 deja a las sesiones siguientes** — `engine.go`, `menu`, `survey` y `media`
> ya tenían sus exportados en inglés y se conservan. Cambian `engine/consulta.go` y `turnoacotado` (los ficheros conservan su
> nombre; los valores observables no cambian: `"resuelto"`, `"no_concluyente"`, `"sin_resolutor"`, `"fallo"`, `"parcial"`,
> `"bucle"` y los textos de los dos centinelas). `runtime` (F8-04/05) y el cableado (F8-06) usan estos:
>
> | Viejo | Nuevo |
> |---|---|
> | `engine.ConsultaResolver` (método `ResolverConsulta`) · `engine.WithConsultaResolver` | `engine.QueryResolver` (método `ResolveQuery`) · `engine.WithQueryResolver` |
> | `engine.ObservadorConsulta` · `engine.WithConsultaObserver` | `engine.QueryObserver` · `engine.WithQueryObserver` |
> | `engine.DesenlaceResuelto` / `NoConcluyente` / `SinResolutor` / `Fallo` / `Parcial` / `Bucle` | `engine.QueryOutcomeResolved` / `Inconclusive` / `NoResolver` / `Failure` / `Partial` / `Loop` |
> | `turnoacotado.Turnero` (método `Turno`) · `turnoacotado.New(Turnero)` | `turnoacotado.Turner` (método `Turno`, sin renombrar: es el del `llmvia.Selector`) · `turnoacotado.New(Turner)` |
> | `(*turnoacotado.Resolver).ResolverConsulta` | `(*turnoacotado.Resolver).ResolveQuery` |
> | `turnoacotado.ErrClaseDesconocida` · `ErrSinTurnero` | `turnoacotado.ErrUnknownClass` · `ErrNoTurner` |
> | `turnoacotado.MaxLlamadasPorTurno` · `PresupuestoTroceado` · `SueloPorLlamada` | `turnoacotado.MaxCallsPerTurn` · `ChunkingBudget` · `FloorPerCall` |
>
> No exportados que un test de cableado lee por reflexión: el campo `turnero` de `Resolver` es ahora `turner`
> (`internal/arranque/inference_wiring_test.go:187,226`, para T8.32/T8.33). `diseno.md` §2.1, `arquitectura.md` §4 y la
> trampa T-6 de `reglas.md` llevan ya los nombres nuevos, con ✎.

## Bloque 3 · `events` y `cart` · 💻 · sesión F8-03 · T8.12, T8.15–T8.17, T8.24, T8.25
Para cuando: `events` (7) y `cart` (14) verdes; `eventshelpertest` con doble y suite; goldens idénticos; candado de orden sin etiqueta, verde y mutado. (21 ficheros + doble + testdata.)

- [x] **T8.12 · rojo(conversacion): `events` (7) y el doble `eventshelpertest`** · 💻 · dep. T8.4 · cumple R8.2.f, R8.4.d — `8c819b40` (114 exportados; `store.go` nace partido en 4: `store`, `store_list`, `store_filter`, `store_append`; `events.go`, `kinds.go` y `store_filter.go` nacen ya verdes; suite de 51 casos, 4 de carrera; la de Postgres vive en `test/procesos/events_contrato_test.go`)
  - **Ficheros**: `C/events/{events,kinds,dispatcher,menu,summary,thread_reader,store}.go` + tests; `C/events/eventshelpertest/{store,contrato}.go` (+ test **verde** del doble)
  - **Hecho cuando**: 114 exportados; reglas de `diseno.md` §2.4 (INV-13 incluida) en los comentarios; D-1 citada en `store.go`; el doble satisface `EventStore`, `SummaryAppender`, `ThreadReader`; la suite `Contrato(t, func(t) Montaje)` corre contra el doble y, con el arnés de F9-A, contra `events.Store` en Postgres (P4); arista `conversacion → acceso` en la lista blanca.
  - **Gate**: `V` · `go test ./internal/modulos/conversacion/events/eventshelpertest/; echo rc=$?` → 0
  - **Commit**: `rojo(conversacion): contrato de events y su doble en memoria`
- [x] **T8.24 · verde(conversacion): `events` (7)** · 💻 · dep. T8.12, T8.23 — `menu` + `dispatcher` `f909402e` (juntos: separados no compilan ni pasan `unused`) · `summary` `6590724e` (se parte: `summary.go` + `summary_render.go`) · `store.go` `d3c2f89e` · `store_list.go` `006e4248` · `store_append.go` `6284c04e` · `thread_reader.go` `1667bca9`; suite 51/51 en memoria y en Postgres; 56 mutantes del adaptador, 55 muertos y 1 equivalente. El `G` se corrió **una vez**, sobre la cabeza integrada (`790ca522`), no por commit — *`admin` (4) pasa a T8.13: importa `runtime` y no puede nacer antes que sus contratos*
  - **Hecho cuando**: un test por promesa del contrato; la verdad de `events/store.go` y `events/thread_reader.go` la da la suite contra Postgres (P4) y F9; textos del menú literales.
  - **Gate**: `G` por commit · **Commit**: `verde(conversacion): <fichero>`
- [x] **T8.15 · rojo(conversacion): `cart` — estado y bordes (8)** · 💻 · dep. T8.7, T8.6 — `69d99d54` (trae también `cart.go`: `Prime` y `ValidateNode` son métodos de `Module` y sin él los ocho no compilan; `validate.go` nace verde; la frontera `conversacion → catalogo` y su test, D-F8-10)
  - **Ficheros**: `C/modules/cart/{state,effects,variants,validate,buyer,revalidate,resume,prime}.go` + tests
  - **Hecho cuando**: importa `modulos/catalogo` y `modulos/solicitudes/intakes` (aristas en la lista blanca); nombres de efecto y textos literales; `revalidate.go` devuelve `intakes.PriceList`.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de cart (estado y bordes)`
- [x] **T8.16 · rojo(conversacion): `cart` — el módulo (6) y los goldens** · 💻 · dep. T8.15 · cumple R8.3.c — `6dc8e574` (`cart.go` nace partido en 4: `cart`, `cart_levels`, `cart_notes`, `cart_navigation`; goldens y catálogos de `testdata` copiados byte a byte, `cmp` rc=0)
  - **Ficheros**: `C/modules/cart/{cart,troceo,preresolutor,screens,consulta,projection}.go` + tests; `C/modules/cart/testdata/cart_v{1,2}_transcript.golden.txt` (copia literal, D-F8-3)
  - **Hecho cuando**: H24, H29, ola6 y 054 escritos como promesas de `cart.go`/`projection.go`; el test golden compila y cae en rojo; el corpus de equivalencia de `troceo`/`preresolutor` lleva casos adversarios (`reglas.md` §0).
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato de cart (módulo) y sus goldens`
- [x] **T8.17 · rojo(conversacion): candado de orden de `Step`** · 💻 · dep. T8.16 · cumple R8.4.b — `790ca522` (re-anclado: busca `Step` donde viva; comprobado con un cuerpo temporal que pasa con el orden bueno y cae al mover la petición bajo `st.Started = true`)
  - **Ficheros**: `C/modules/cart/orden_consulta_ast_test.go` (con `//go:build pendiente` hasta el verde de `cart.go`)
  - **Hecho cuando**: exige una de cada pieza; comprobado que **ve** `Step` cuando exista el cuerpo.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): candado del orden de la consulta en cart`
- [x] **T8.25 · verde(conversacion): `cart` (14)** · 💻 · dep. T8.15–T8.17, T8.24 · cumple R8.3.c, R8.4.b — relanzamiento del 2026-10-10, rama `reorg/f8-03b-cart-verde` desde `origin/dev` @ `8b841c8d` (el merge del PR #62): `dd0771ce` (previo, solo tests: 17 `//nolint:<linter> // motivo` en 10 ficheros, porque el lint no veía los tests con etiqueta; ✎ arreglados de verdad y sin `//nolint` en `d3f6c31b`, por decisión de Jhoan; hallazgo 28) · `4045db25` (13 ficheros en un commit, obligado por `unused`: `state`, `effects`, `variants`, `buyer`, `screens`, `preresolutor`, `consulta`, `troceo`, `cart`, `cart_levels`, `cart_notes`, `cart_navigation` y `prime`; quita la etiqueta `pendiente` a sus tests, a `helpers_test.go`, a `golden_test.go` y a `orden_consulta_ast_test.go`) · `dd52dc54` (`resume.go`) · `5774823d` (`revalidate.go`) · `44255a2b` (`projection.go`, que nace partido en cuatro por E-13: `projection` 268, `projection_lines` 222, `projection_close` 138, `projection_buyer` 68; mutantes declarados por el sub-agente: 84 válidos, 84 muertos, 0 equivalentes). **Desvíos del plan del verde**: (a) el commit previo de lint; (b) `prime.go` entra en el primer commit y no en uno propio, porque `cart_levels_test.go`, `cart_notes_test.go` y `consulta_test.go` usan la constante `continueBebidas` de `prime_test.go` (el traslado de `TestWithLogger_PrimeWarnsAboutDiscardedFields` a `prime_test.go` va ahí, cuerpo intacto); (c) `projection.go` partido en cuatro. Resuelto en el primer commit: `loadCatalog` nace no exportado en `cart/state.go` y `cloneVars` se sustituye por `modules.CloneVars` (mismo cuerpo). `cart` queda en **20** ficheros de producción (17 + 3 de la partición de `projection`); `pendiente.Implementar` en producción de `conversacion` = 0; goldens verdes **sin** `-update` y `cmp` rc=0 contra los del viejo; candado de orden sin etiqueta, verde y mutado sobre el `Step` real (rojo; deshecho sin commitear). Hallazgos 27 y 28. PR #63 a `dev`, **integrado** por orden expresa de Jhoan (2026-10-10), sin squash
  - **Hecho cuando**: test golden verde **sin** `-update`; `orden_consulta_ast_test.go` sin `//go:build pendiente` y verde; mutación local del orden → rojo (se deshace sin commitear); las tres cosas del cierre; `dev` empujado.
  - **Gate**: `G` · **Commit**: `verde(conversacion): <fichero>`

> ✎ **2026-10-10 (D-F8-11, Jhoan; hallazgo 29 del README)**: el bloque 4 («`runtime` (1): contratos de los 23 y soporte», sesión
> F8-04, T8.18–T8.21 y T8.26) se parte en **4a** y **4b**, una sesión cada uno. Las tareas no cambian de texto ni de número.

## Bloque 4a · `runtime` (1a): los contratos de los 23 · 💻 · sesión F8-04 · T8.18–T8.21
Para cuando: los 23 contratos de `runtime` en rojo (12 de soporte + 11 de núcleo), `runtimehelpertest` en verde con sus dobles y sus suites `Contrato`, candado de rachas escrito tras `pendiente`; D-F8-4 en `fronteras_test.go` y lista blanca de `conversacion` completa; `make test-pendiente` cuenta solo `runtime`; `G`, `V` rc=0. **Ningún verde de `runtime`.** Nivel **complejo**: esquema completo E-2…E-9.
(Los contratos de los 23 van antes que cualquier verde: `send.go`, `thread.go` y `welcome.go` cuelgan de `*Runtime`.)

- [x] **T8.18 · rojo(conversacion): runtime — soporte (12) y `runtimehelpertest`** · 💻 · dep. T8.12, T8.16 · cumple R8.2.a–f — `cf327ca5` (9 de los 12: `runtime`, `keyedmutex`, `event_sink`, `log_sink`, `summary_sources`, `streak`, `webhook_sink`, `tenant_resolver`, `self_numbers`; `runtimehelpertest` verde, 46 PASS y 0 SKIP, con `ContratoTenantResolver` (13 casos) y `ContratoSelfNumbers` (18); las dos suites contra Postgres, escritas en `test/procesos/runtime_{tenant_resolver,self_numbers}_contrato_test.go` tras `integracion && pendiente`) · `ee6da0af` (**desvío**: `welcome`, `thread` y `send` entran con el núcleo, porque su contrato cuelga de `*Runtime`). `keyedmutex.go` y `streak.go`, sin exportados: contrato = comentario-promesa, tests en el verde (hallazgo 31)
  - **Ficheros**: `C/runtime/{runtime,keyedmutex,event_sink,log_sink,tenant_resolver,self_numbers,summary_sources,streak,welcome,thread,send,webhook_sink}.go` + tests; `C/runtime/runtimehelpertest/*.go` (+ tests verdes)
  - **Hecho cuando**: puertos de `runtime.go` con los motivos y perfiles; `webhook_sink` importa `modulos/solicitudes/integrations/crmpush`; dobles de §4 de `diseno.md`; `self_numbers` y `tenant_resolver` con su suite `Contrato(t, func(t) Montaje)` contra el doble y contra Postgres (P4).
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato del runtime (soporte) y sus dobles`
- [x] **T8.19 · rojo(conversacion): runtime — núcleo (11)** · 💻 · dep. T8.18 · cumple R8.2.a–c — `ee6da0af` (los 11 más `welcome`, `thread` y `send`; las `With*` son **21** en `runtime_engine.go` y `WithWelcomeStore` en `welcome.go`; tests del `Runtime` en `package runtime_test` sobre `harness_test.go`; hallazgos 30–35)
  - **Ficheros**: `C/runtime/{persist_sink,event_effects,source_composer,aggregator,runtime_engine,resume,start,exit_menu,event_lifecycle,events,incoming}.go` + tests
  - **Hecho cuando**: reglas RT-1…RT-20 y AG-1…AG-8 en los comentarios de su fichero; `aggregator.go` importa `modulos/captacion/intake`; `runtime_engine.go` declara las 22 `With*`; literales de §5.
  - **Gate**: `V` · **Commit**: `rojo(conversacion): contrato del runtime (núcleo)`
- [x] **T8.20 · rojo(conversacion): candado de rachas** · 💻 · dep. T8.19 · cumple R8.4.a — `33e9eca2` (`TestStreak_EveryDeleteClosesTheEpisode`, E-11: era `TestRacha_TodoDeleteCierraElEpisodio`; `expectedDeletes = 6`, a re-medir; hoy rojo por la cuenta, 0 de 6; comprobado fuera del repo contra una copia del viejo: verde con 6, rojo sin un `Close`)
  - **Ficheros**: `C/runtime/streak_invariante_test.go` (`//go:build pendiente` hasta el verde de `incoming.go`/`events.go`)
  - **Hecho cuando**: constante `esperados` a **re-medir** en el verde (hoy 6).
  - **Gate**: `V` · **Commit**: `rojo(conversacion): candado de cierre de rachas`
- [x] **T8.21 · Cierre de la pasada de contratos** · 💻 · dep. T8.3–T8.20 (menos T8.13) · cumple R8.2.g — `de6a5411` (D-F8-4: `TestEventsDoesNotDependOnTheClassifier` en `fronteras_test.go`, verde y rojo por mutación local con un import de `inferencia/llmvia`; lista blanca de `conversacion` completa sin tocarla) y el commit de cierre de F8-04 (el que trae esta línea): **N = 60** `pendiente.Implementar` en producción, todos en `C/runtime` (`make test-pendiente`: `PENDIENTES=60 · ROJOS=50`; el `grep` literal sobre `C` da 64: las 60 y las 4 del texto del candado `cart/orden_consulta_ast_test.go`); `G` `GATE_RC=0` y `V` rc=0 sobre `de6a5411`, 0 SKIP
  - **Hecho cuando**: `make test-pendiente` = `grep -rn 'pendiente.Implementar' --include='*.go' C | wc -l` (anotar N: solo queda el runtime); `G` rc=0; D-F8-4 aplicado en `fronteras_test.go` (regla «`events` sin clasificador») y lista blanca de `conversacion` **completa** (`arquitectura.md` §3; las aristas se fueron añadiendo al nacer cada import, aquí se comprueban).
  - **Gate**: `validar-antes-de-cerrar` · **Commit**: `rojo(conversacion): fronteras del módulo`
## Bloque 4b · `runtime` (1b): el verde del soporte · 💻 · sesión F8-04b · T8.26
Para cuando: los 12 de soporte verdes, mutantes de `keyedmutex.go` y `streak.go` muertos, suites contra Postgres de `self_numbers` y `tenant_resolver` verdes; `pendiente.Implementar` de `runtime` solo en los 11 del núcleo; `G` rc=0. Depende de F8-04 integrada en `dev`. Nivel **complejo**: esquema completo E-2…E-9.

- [x] **T8.26 · verde(conversacion): runtime — soporte (12)** · 💻 · dep. T8.21 — **9 de los 12** (D-F8-12), rama `reorg/f8-04b-runtime-soporte` desde `origin/dev` @ `cbebf10c`: `keyedmutex` `83565b4e` · `streak` `fc471fed` · `event_sink` `163c193b` · `log_sink` `a446d4b2` · `webhook_sink` `91ffb00d` · `summary_sources` `093ea8fc` · `tenant_resolver` + `self_numbers` `6216d1df` (juntos: las dos suites contra Postgres pierden `pendiente` a la vez, y con ellas `postgres_fakedb_test.go` y los dos tests unitarios). `runtime.go` no lleva commit: nació declarado y con su test sin etiqueta en F8-04. `go test -race`, `go vet -tags pendiente` y el lint (sin etiqueta y con `--build-tags pendiente`) rc=0 **en cada uno de los siete commits**, medido en un *worktree* temporal. Mutantes: `keyedmutex.go` 5 de 5 muertos; `streak.go` 16 muertos y 1 equivalente (`>` por `>=` en `Max`); `event_sink.go` 3 de 3; SQL de los dos adaptadores 9 de 9. Suites contra Postgres con testcontainers: 13 + 18 casos, rc=0, 0 SKIP. `PENDIENTES=50 · ROJOS=40` (eran 60 y 50): quedan en 8 ficheros del núcleo y en `welcome.go`. `welcome`, `thread` y `send` **pasan a T8.27** con su etiqueta. `G` `GATE_RC=0` sobre `6216d1df`, 0 SKIP. Hallazgos 37–40
  - ✎ **2026-10-10 (D-F8-12, Jhoan)**: el verde de T8.26 son **9** de los 12 (`runtime`, `keyedmutex`, `event_sink`, `log_sink`, `summary_sources`, `streak`, `webhook_sink`, `tenant_resolver`, `self_numbers`); `welcome`, `thread` y `send` cuelgan de `*Runtime`, sus tests mueren en `runtime.WithClock` con el núcleo en rojo y **pasan a T8.27**, con su etiqueta `pendiente`.
  - **Hecho cuando**: `go test -race` rc=0 por commit; un test por promesa del contrato; mutantes en `keyedmutex.go` y `streak.go` (estado y concurrencia); la verdad de `self_numbers.go` y `tenant_resolver.go` la da la suite contra Postgres (P4) y F9; `streak.go` con reloj inyectado (T-7); las tres cosas del cierre; `dev` empujado.
  - **Gate**: `G` · **Commit**: `verde(conversacion): <fichero>`

## Bloque 5 · `runtime` (2): el núcleo · 💻 · sesión F8-05 · T8.27–T8.28
Para cuando: los 11 del núcleo verdes, mutantes muertos, `grep -rn 'pendiente.Implementar' --include='*.go' C/runtime | wc -l` → **0**. Nivel **complejo**, con mutantes.

- [x] **T8.27 · verde(conversacion): runtime — núcleo (11)** · 💻 · dep. T8.26 — **los 14** (D-F8-12), rama `reorg/f8-05-runtime-nucleo` desde `origin/dev` @ `da82addf`: `persist_sink` `46b6a1ec` · `source_composer` `0c1d8973` (corpus del sobre de P2 con 4 casos adversarios más, comprobado contra el `ComposeSourceText` viejo) · `aggregator` `0aaa18cd` (nace partido en 5 por E-13: `aggregator`, `_observe`, `_hint`, `_sweep`, `_run`; D-F9-10 con su caso) · **el núcleo del `Runtime`, 11 ficheros en un commit**, `1e0f6009` (`runtime_engine`, `resume`, `start`, `exit_menu`, `event_lifecycle`, `events`, `incoming`, `event_effects`, `welcome`, `thread`, `send`) · candado de rachas `a435884b` (`streak_invariante_test.go` sin etiqueta; `expectedDeletes` re-medida: **6**; quitar un `Close` → rojo en `:81`, probado con tres distintos y deshecho sin commitear) · tests que matan mutantes supervivientes `a6fb0215` (núcleo) y `f5fd0374` (agregador). **Desvío del «un commit por fichero» (hallazgos 19, 27 y 39; se dice aquí como pedía la ficha)**: los 11 del `Runtime` van juntos porque todos cuelgan de `*Runtime` y se llaman entre sí, los campos del struct solo tienen lector en los demás ficheros (lint `unused`) y todos sus tests pasan por el mismo arnés, que no monta hasta que `New` y las `With*` tienen cuerpo: ningún corte menor compila y pasa `unused`. **Particiones E-13** (solo moviendo declaraciones): `runtime_engine` → 3 (`_pool`, `_streak`), `event_lifecycle` → 2 (`_cancel`), `events` → 6 (`_start_new`, `_switch`, `_summary`, `_clock`, `_menu`), `incoming` → 5 (`_advance`, `_trigger`, `_guards`, `_limiter`), y `aggregator_bridge.go` (`observeForAggregation`, que en el viejo vivía en `aggregator.go` y cuelga de `*Runtime`); `runtime` queda en **40** ficheros de producción y 58 de test, el mayor `runtime_engine.go` con 553 líneas (tolerancia). `go test -race`, `go vet -tags pendiente` y el lint (sin etiqueta y con `--build-tags pendiente`) rc=0 **en cada uno de los nueve commits** (36 de 36), medido en un *worktree* temporal. **Mutantes**: núcleo 187 válidos, 185 muertos, 2 equivalentes; agregador, sink y compositor 175 válidos, 172 muertos, 2 equivalentes y 1 que solo muere por `-timeout` (`Run` con `return` → `continue` en `ctx.Done()`). **Tests corregidos: 2** de los ≈ 260 que pasan por el arnés (hallazgo 42). `PENDIENTES=0 · ROJOS=0` (eran 50 y 40); 0 `_test.go` del paquete con `//go:build pendiente`. D-F7-9 **no** se arregla: portado tal cual. Hallazgos 41–46
  - ✎ **2026-10-10 (D-F8-12, Jhoan)**: T8.27 lleva además `welcome`, `thread` y `send` (de soporte, pero colgados de `*Runtime`; venían de T8.26): son **14** ficheros, y `pendiente` del runtime = 0 los incluye.
  - **Ficheros**: en el orden `persist_sink`, `event_effects`, `source_composer`, `aggregator`, `runtime_engine`, `resume`, `start`, `exit_menu`, `event_lifecycle`, `events`, `incoming`. Si `events.go` (1.501 l) o `incoming.go` (1.328 l) no caben, se para en un punto limpio (fichero verde y empujado), se cierra con las tres cosas y **se relanza la misma sesión**.
  - **Hecho cuando**: un test por promesa del contrato (RT-1…RT-20, AG-1…AG-8); **mutantes** por fichero sobre lo que guarda estado o concurre (candado por conversación, semáforo, limitador, rachas, `seen` y pistas del agregador): cada mutante mata al menos un test; `streak_invariante_test.go` sin etiqueta y verde con la constante re-medida; mutación local (quitar un `Close`) → rojo; el corpus del sobre de P2 (`source_composer`) lleva casos adversarios.
  - **Gate**: `G` · **Commit**: `verde(conversacion): <fichero>`
- [x] **T8.28 · refactor y cierre de los verdes** · 💻 · dep. T8.27 — `54c25319` y `6c9bd1f1` (solo comentarios de contrato que el verde dejó caducados: «nace en el verde», «lo decide el verde (F8-05)»; ninguna promesa cambia) y el commit de cierre de F8-05 (el que trae esta línea). ✎ Después del cierre, en el mismo PR: `c44c212f`, **D-F8-14** (hallazgo 34b: un entrante cuyo guardado falla ya no queda anotado como visto; divergencia deliberada del viejo); `6eb31a3d`, **D-F8-15** (34c: el reintento del sink durable retoma donde falló y no duplica `flow_events`); `a9da0105`, **D-F8-16** (34e: `WithEventSink(nil)` y `WithResumePolicy(tipo, nil)` se ignoran). `grep -rn 'pendiente.Implementar' --include='*.go' C/runtime | wc -l` → **0** (sobre `C` entero da 4: el texto del candado `cart/orden_consulta_ast_test.go`); `make test-pendiente` `PENDIENTES=0 · ROJOS=0`; `G` `GATE_RC=0` sobre `f5fd0374` (237 paquetes `ok`, lint `0 issues`), `V` rc=0, **0 SKIP** con `-v` (4.297 PASS de primer nivel en `modulos`, `nucleo`, `arranque` y `apipublica`; 440 en `runtime`); ningún `//nolint` en `C/runtime`; `go.mod` y `go.sum` sin tocar. PR #67 a `dev` desde `reorg/f8-05-runtime-nucleo`, **integrado** por orden expresa de Jhoan (2026-10-10), sin squash
  - **Hecho cuando**: `pendiente` del runtime = 0; SKIP = 0; los `refactor(conversacion): …` que hagan falta con los tests verdes; las tres cosas del cierre; `dev` empujado.
  - **Gate**: `validar-antes-de-cerrar`

## Bloque 6 · la cara HTTP y conmutar · 💻 · sesión F8-06 · T8.13, T8.29–T8.35
Para cuando: `admin` y los handlers de I1–I19 verdes; huella igual, 0 puentes (import), 0 adaptadores, `Conmutados` completo, `cmd/server-modular` compila sin un solo paquete viejo.
⚠️ T8.31–T8.34 **no compilan por separado** (los paquetes re-tocados cambian de tipos y el
arranque tiene que pasarles los nuevos a la vez): van en **un** commit `conmutar(conversacion)`,
con un párrafo por tarea en el mensaje.

- [x] **T8.13 · conversacion: `admin` (4, sin `sessions.go`) — rojo y verde** · 💻 · dep. T8.6, T8.9, T8.28 · cumple R8.6.d — rojo `5ba7f419` · verde `b310e7d0` (`durable_flow`), `f7aa209f` (`handlers`), `cebe65f0` (`triggers`, partido en tres por E-13). ✎ F8-06: son **15** exportados, no 16 (la cifra contaba `Register`, que no se reconstruye, D-F8-2); hallazgo 48
  - **Ficheros**: `C/admin/{doc,handlers,triggers,durable_flow}.go` + tests (menos `doc.go`)
  - **Hecho cuando**: 16 exportados; **sin** `Register` (D-F8-2, dicho en el commit); `handlers.go` compara `modulos/edge/session.ErrSessionOffline` y los centinelas de `C/runtime`; un test por promesa; textos de error HTTP literales; `pendiente` del módulo = 0.
  - **Gate**: `V` · `G` · **Commit**: `rojo(conversacion): contrato de admin (sin Register, D-7)` y `verde(conversacion): <fichero>` (según nivel)
- [x] **T8.29 · FX TX.22–TX.23: rojo y verde de los ficheros de conversación de `apipublica`** · 💻 · dep. T8.13 · cumple R8.6.a, R8.6.d — rojo `ffc26a60` (siete ficheros) y `aee427ed` (`flows`, que esperaba al contrato de `admin`) · verde `62b61dc0` (`media`), `27fa4bb4` (`tenantcontent`), `1827e486` (`conversationevents`), `4e2727aa` (`conversationeventcancel`), `d5cf1823` (`flows`), `8ebe7b79` (`catalogimport`, partido en dos), `8588a5b9` (`catalogtabular`), `ecf25862` (`catalogtemplate`); hallazgo 49
  - **Ficheros**: los que liste [`../FX-cara-http/tareas.md`](../FX-cara-http/tareas.md) TX.22 (flujos, contenido de tenant, media, disparadores, catálogo, eventos de conversación)
  - **Hecho cuando**: lo dicen TX.22 y TX.23 de FX; importan **solo** `C/**`, `nucleo`, `platform` y módulos nuevos; aún **sin montar**.
  - **Gate**: los de FX · **Commit**: los de FX
- [x] **T8.30 · Ensayo en seco de la conmutación** · 💻 · dep. T8.29 — sin commit propio: ensayado en un *worktree* aparte sobre `aee427ed` (`go build ./...` rc=0 a la primera; `go list -deps` 0 viejos), ya borrado; su resultado es `fe6305b9`
  - **Hecho cuando**: rama local con T8.31–T8.34 aplicados compila (`GOWORK=off go build ./...; echo rc=$?` → 0) y la lista de ficheros tocados queda anotada para el mensaje del commit; se descarta si la huella no cuadra.
  - **Gate**: `go build` rc=0
- [x] **T8.31 · Retirar los puentes (import) de F5–F7** · 💻 · dep. T8.30 · cumple R8.7.a, R8.7.c — `fe6305b9`
  - **Ficheros**: `internal/modulos/catalogo/catalog.go` y `catalogo/indice/cache.go` (si D-F8-1 = no) · `solicitudes/intakes/telemetria/telemetria.go` · `captacion/stages/draft.go` · `captacion/reanalisis/reanalisis.go` · sus `_test.go` · `internal/modulos/fronteras_test.go` (lista de puentes → vacía)
  - **Hecho cuando**: `grep -rn 'internal/flujos\|internal/turnoacotado' internal/modulos --include='*.go'` → vacío; cada paquete re-tocado sigue en verde (`go test -race`, rc=0).
- [x] **T8.32 · Retirar todos los adaptadores `internal/arranque/bridge_*.go` vivos** · 💻 · dep. T8.31 · cumple R8.7.b — `fe6305b9`
  - **Ficheros**: todos los `bridge_*.go` y sus `bridge_*_test.go` (lista de T8.2; previstos: `bridge_contact`, `bridge_inferencia` con `turneroBridge`, `bridge_captacion`); `internal/arranque/fase{3,5,6,8,9}_*.go` re-cableados con los tipos nuevos; `internal/modulos/fronteras_test.go` (`Conmutados`)
  - **Hecho cuando**: `ls internal/arranque/bridge_*.go` → nada ([`../00-marco/estructura.md`](../00-marco/estructura.md) §2.1); fuera las dos **segundas instancias viejas** (`intakes.Postgres` de F6, `intake.Postgres` de F7); un solo `entResolver`, un solo `flowDeps.kp`, un solo `gw` (T-2, T-3); con cada muerte, el módulo dueño entra en `Conmutados` (`nucleo`, `inferencia`, `captacion`, `solicitudes`) y `conversacion` con este commit: **`Conmutados` completo**; el test de cableado afirma que el arranque construye los servicios **nuevos** y que **ninguna fase importa un paquete viejo** (grep por ruta de import, no solo el campo del contenedor: hallazgo 39).
- [x] **T8.33 · `fase7_flujos.go` y `:8100`** · 💻 · dep. T8.32 · cumple R8.5.a–c, R8.6.b — `fe6305b9`
  - **Ficheros**: `internal/arranque/fase7_flujos.go`, `rutas_admin.go` (J18–J22 con `C/admin`), los candados de cableado portados (`flow_options_cableadas`, `turno_acotado_cableado`)
  - **Hecho cuando**: un `runtime.New`, un `NewIntakeAggregator`, las 22 opciones, los 4 hooks y la fuente del gauge; test de identidad del `Runtime` (gateway = Starter = EventCanceller).
- [x] **T8.34 · FX TX.24: 19 rutas, cara vieja fuera y centinela único** · 💻 · dep. T8.33 · cumple R8.6.a, R8.6.c, R8.7.d — `fe6305b9` (✎ F8-06: los dos `grep … → vacío` de T8.31 y T8.34 casan las cabeceras «Porta internal/flujos/…»; se midió por línea de import y con `go list -deps`: 0; hallazgo 50)
  - **Ficheros**: los de TX.24 de FX. Con **D-F3-2** (recomendación) `edge/session.ErrSessionOffline` ya es el de `platform` desde F3 y **no se toca**; solo si D-F3-2 = no (D-FX-3) se edita `internal/modulos/edge/session/registry.go` para que deje de ser alias del viejo
  - **Hecho cuando**: `grep -rn 'internal/publicapi\|internal/gateway/session' internal/arranque internal/modulos internal/apipublica` → vacío.
  - **Gate** (T8.31–T8.34 juntos): `G` · `V` · `go test -run Huella ./internal/arranque/` rc=0 · `go list -deps ./cmd/server-modular` sin paquetes viejos (`reglas.md` §4.5)
  - **Commit**: `conmutar(conversacion): el arranque nuevo cablea conversacion, 19+5 rutas, cero puentes y cero adaptadores`
- [x] **T8.35 · Lo que la conmutación deja sin comprobar** · 💻 · dep. T8.34 — *sustituye al traspaso web → local: ya no hay dos entornos; el traspaso (skill `traspaso-web-local`) solo se escribe si la sesión se corta* — el commit `docs(reorganizacion-modular): F8 conmutada — pendiente de cierre` de esta rama; lo que F8-07 tiene que refutar, en el hallazgo 51 del README y en el bloque F8-06 de `ESTADO.md`
  - **Ficheros**: `ESTADO.md` (bloque de la sesión) y el README de la fase (hallazgos)
  - **Hecho cuando**: escrito lo que F8-07 tiene que refutar: la identidad de `entResolver`/`kp`/`gw` en ejecución, el barrido del agregador con BD real, el golden en un Edge real; las tres cosas del cierre; `dev` empujado.
  - **Commit**: `docs(reorganizacion-modular): F8 conmutada — pendiente de cierre`

## Bloque 6b · D-F7-9: cierre y sobre en un solo acto · 💻 · sesión F8-06b · T8.39–T8.40
> ✎ **2026-10-10 (D-F8-13, Jhoan; hallazgo 44 del README)**: bloque nuevo, entre conmutar y el cierre. El diseño decidido y lo que queda fuera, en la ficha [`F8-06b`](../sesiones/F8-06b-cli-d-f7-9-cierre-y-sobre.md).

Para cuando: un job de ventana nunca es visible para el worker en `pending` sin sobre (salvo el hilo vacío); caso de P4 verde contra el binario nuevo. Nivel **complejo**: rojo y verde, suite contra Postgres, mutantes.

- [x] **T8.39 · D-F7-9: el agregador compone antes de cerrar y cierra con el sobre en una sentencia** · 💻 · dep. T8.35 — rojo `a0144628` (contrato de `intake.JobStore.CloseWithSourceText`, la quinta operación, y sus siete casos de suite) · verde `cae02e23` (`intake/postgres`) y `fc466f66` (`intake/memory`) · el arreglo `ef480998` (`fix(conversacion)`: compositor `Compose` sin escribir, `closeWindow` compone y cierra en una sentencia) · `8e02d07b` (comentarios del pipeline) · `83254490` (aserción en P4). Guarda de «la ventana no cambió»: `id` + `updated_at` igual al leído. 14 mutantes del store y 14 del agregador y el compositor, todos muertos; suite `Contrato` de `intake` verde en memoria y en Postgres. ✎ El caso de P4 **no ramifica por binario** (R9.8.b de F9; decisión de Jhoan, 2026-10-10: foco en el binario nuevo); hallazgos 53 y 56 del README
  - **Ficheros**: `internal/modulos/captacion/intake/{store,postgres,memory}.go` y `intakehelpertest/` (operación nueva de `JobStore`, con su caso en la suite); `C/runtime/source_composer.go` (componer separado de escribir) y `C/runtime/aggregator_sweep.go` (`closeWindow`) + tests; el caso de P4 en `test/procesos/`; comentarios de D-F7-9 caducados (`aggregator_sweep.go`, `source_composer.go`, `captacion/pipeline/pipeline_chain.go`).
  - **Hecho cuando**: cierre y sobre en **un** `UPDATE` (sin transacción, sin migración), que solo cierra si la ventana no cambió desde que se leyó; hilo vacío → cierra con sobre NULL, como hoy; `PutSourceText` intacto; D-F9-10 se sigue cumpliendo; mutantes muertos (quitar la guarda, cerrar sin sobre, sobre sin cerrar, orden viejo); suite `Contrato` de `intake` verde en memoria y en Postgres; P4 contra el binario nuevo sin ningún `failed` por «el job no trae literal que analizar»; D-F7-9 marcada arreglada en el README. **Fuera**: el código viejo (el re-análisis es T8.40).
  - **Gate**: `G` · `make test-procesos` (nuevo y viejo) · **Commit**: `fix(conversacion): cierre y sobre de la ventana en un solo acto (D-F7-9)`

- [x] **T8.40 · El job de re-análisis nace con su sobre** · 💻 · dep. T8.39 — ✎ 2026-10-10 (Jhoan): entra en F8-06b como segunda tarea (hallazgo 1) — rojo `7c5feccb` (`ReanalysisRequest.SourceText`, siete tests rojos por aserción) · verde `b623498f` (el mismo `INSERT` escribe las tres columnas) · el arreglo `1aad38cd` (`fix(captacion)`: el servicio compone antes de abrir; fallo al componer → no se abre job y la petición devuelve el error) · `42a06574` (aserción en P8 y P6). 11 mutantes, todos muertos. `PutSourceText` y `ComposeAtFlush` quedan **sin llamante de producción** en el árbol nuevo: dicho y preguntado, **no borrados** (Jhoan, 2026-10-10; deuda D-35); hallazgos 54 y 55 del README
  - **Ficheros**: `internal/modulos/captacion/intake/{reanalysis,postgres_reanalysis,memory}.go` e `intakehelpertest/` (el alta del job con su sobre, en una sentencia); `internal/modulos/captacion/reanalisis/reanalisis.go` (componer antes de abrir) + tests; el compositor de `C/runtime` (reutiliza el «componer sin escribir» de T8.39); su caso de proceso en `test/procesos/`.
  - **Hecho cuando**: un job de re-análisis nunca es visible en `pending` sin sobre; fallo al componer → no se abre job y la petición lo dice (hoy: job abierto sin literal que el worker mata); mutantes muertos; suite `Contrato` de `intake` verde en memoria y en Postgres; si `PutSourceText` queda sin llamante de producción, dicho y preguntado, no borrado.
  - **Gate**: `G` · `make test-procesos` (nuevo y viejo) · **Commit**: `fix(captacion): el job de re-análisis nace con su sobre (D-F7-9)`

## Bloque 7a · limpieza tras D-F7-9 · 💻 · sesión F8-07a · T8.41–T8.43

> ✎ **2026-10-10 (Jhoan, tras F8-06b)**: el bloque 7 se parte en **tres sesiones**. Este, con lo que salió del repaso de
> pendientes de F8-06b; el **7b**, con Docker y el arranque real; y el **7**, que solo cierra la fase. Lo que queda debajo
> de esta línea y antes de T8.41 es la cabecera del bloque 7 original.
Para cuando: la definición de hecho de [`reglas.md`](reglas.md) §4 entera.

- [ ] **T8.41 · Borrar `PutSourceText` y `ComposeAtFlush` (deuda D-35)** · 💻 · dep. T8.40 — ✎ 2026-10-10 (Jhoan, tras F8-06b; hallazgo 55 del README)
  - **Ficheros**: `C/runtime/source_composer_flush.go` y su test (`ComposeAtFlush`), `C/runtime/source_composer.go` (`SourceTextWriter`, el campo `jobs` y el argumento del constructor), `internal/modulos/captacion/intake/{store,postgres,memory}.go` y sus tests (`PutSourceText`), `intakehelpertest/` (los casos `PutSourceText_*` y lo que solo ellos usen), `internal/arranque/fase5_captacion.go` y las aserciones de cableado que miran `composer.jobs`.
  - **Hecho cuando**: ninguna de las dos operaciones existe en el árbol nuevo; `intake.JobStore` vuelve a cuatro operaciones; los tests de cableado se actualizan en el mismo commit **sin debilitarse** (lo que afirmaba la identidad de `composer.jobs` desaparece con el campo, no se relaja); `G` rc=0 con 0 SKIP; suite `Contrato` de `intake` verde en memoria y en Postgres; D-35 marcada cerrada en `deuda.md`. **Fuera**: el código viejo.
  - **Gate**: `G` · `make test-procesos` (nuevo) · **Commit**: `refactor(captacion): fuera PutSourceText y ComposeAtFlush, sin llamante desde F8-06b (D-35)`

- [ ] **T8.42 · El caso adversario del callback del CRM tolera el corte de conexión** · 💻 · dep. T8.35 — ✎ 2026-10-10 (Jhoan, tras F8-06b; hallazgos 14 y 58 del README)
  - **Ficheros**: `test/procesos/p6_crm_test.go` (`callback_body_adversarial`, ≈ :270) y sus helpers.
  - **Antes de tocar**: leer el caso y **confirmar** que el «connection reset by peer» / «broken pipe» es el servidor rechazando el cuerpo adversario mientras el test aún lo envía, y no otra cosa. En F8-06b solo se leyó el mensaje del log. Si no es eso, PARA y dilo.
  - **Hecho cuando**: el caso acepta como rechazo válido la respuesta de error **o** el corte al enviar, y sigue fallando si el servidor **acepta** el cuerpo; sin tocar el servidor; sin `time.Sleep` ni `t.Skip`; `-run TestP6_CRMBridge -count=10` verde contra los dos binarios.
  - **Gate**: `make vet-integracion` · `make test-procesos` · **Commit**: `procesos(solicitudes): el callback adversario del CRM tolera el corte de conexión`

- [ ] **T8.43 · Repetir los mutantes que sostienen el arreglo de D-F7-9** · 💻 · dep. T8.41 — ✎ 2026-10-10 (Jhoan, tras F8-06b; hallazgo 57 del README)
  - **Qué**: en F8-06b, 37 de los 39 mutantes los corrieron los sub-agentes que escribieron el código; el orquestador solo repitió los dos del SQL contra Postgres real. Aquí los repite **quien no escribió el código**, sobre la cabeza integrada y **después de T8.41** (que cambia el compositor).
  - **Los siete**: en `C/runtime/aggregator_sweep.go` — (1) volver al orden viejo (cerrar y después componer y escribir), (2) cerrar sin sobre, (3) escribir el sobre sin cerrar, (4) quitar la guarda de «la ventana no cambió» (en el gemelo `intake/memory.go`, corriendo los tests de `runtime`); en `captacion/reanalisis/reanalisis.go` — (5) abrir el job y componer después, (6) abrir el job pese al fallo de composición, (7) tragarse el fallo de composición.
  - **Hecho cuando**: cada uno **compila** y mata al menos un test, con el nombre del test anotado; un mutante que no compila no cuenta como muerto; en serie y por `go test -overlay` sobre copias fuera del repo, nunca editando el checkout; si alguno sobrevive, nace su caso en un commit `test(…)`.
  - **Gate**: la tabla mutante → test en el hallazgo de cierre · **Commit**: ninguno si todos mueren

## Bloque 7b · Docker y arranque real · 💻 · sesión F8-07b · T8.36–T8.37

Para cuando: `make ci-docker` rc=0; las suites `Contrato` de los puertos con BD verdes contra Postgres; `cmd/server-modular` arranca solo y recorre una conversación con el Edge falso; refutado el hallazgo 51.

- [ ] **T8.36 · Gates y arranque local del binario nuevo** · 💻 · dep. T8.35, T8.39, T8.40 (✎ D-F8-13), T8.41–T8.43 (✎ 2026-10-10) · cumple R8.8.a, R8.8.c
  - **Hecho cuando**: `validar-antes-de-cerrar` con la toolchain fijada; las suites `Contrato` de los puertos con BD verdes contra Postgres (P4, arnés de F9-A); `cmd/server-modular` arranca en local **solo** (sin `cmd/server`), un Edge de prueba (o el e2e de `cmd/server-modular`) recorre «carrito» → línea → confirmar y el cliente recibe las pantallas; SKIP = 0; refutado lo que dejó T8.35.
  - ✎ **2026-10-10 (Jhoan)**: incluye **`make ci-docker`** (los gates dentro de un contenedor Linux limpio), que **no corre desde F8-01** (2026-10-09): ninguna sesión de F8-02 a F8-06b lo corrió, así que la conmutación y el arreglo de D-F7-9 no se han validado fuera del Mac. Va **el primero**. El «Edge de prueba» es el Edge falso de `test/procesos` (habla el contrato real de CloudLink, sin WhatsApp); la sesión de WhatsApp del Edge real no hace falta.
  - **Gate**: `make ci-docker` rc=0 · `G` · e2e rc=0
- [ ] **T8.37 · 🕐 Procesos del módulo contra el binario nuevo (= T9.29, 9C de `conversacion`)** · 💻 · dep. T8.36 · cumple R8.8.b — *con D-F9-1 = sí (recomendación); si D-F9-1 = no, se tacha y lo cubre T9.34 tras F8*
  - **Hecho cuando**: T9.29 (9C de `conversacion`, [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md)) pasa: «Entrante a respuesta», «De mensaje a borrador», «Re-análisis», con las aserciones de BD de `diseno.md` §4.2 (D-054.4, 23502, `ON CONFLICT` de la ventana); `make test-procesos` rc=0 y 0 SKIP con `-v`.
  - **Gate**: `make test-procesos > /tmp/p.log 2>&1; echo RC=$? >> /tmp/p.log; tail -1 /tmp/p.log`

## Bloque 7 · cierre · 💻 · sesión F8-07 · T8.38

- [ ] **T8.38 · Cerrar F8** · 💻 · dep. T8.36, T8.41, T8.42, T8.43 (y T8.37 si aplica)
  - **Ficheros**: `ESTADO.md`, este `README.md` (estado «cerrada», SHA, hallazgos)
  - **Hecho cuando**: los 10 puntos de `reglas.md` §4 escritos con su número en `ESTADO.md`; `origin/dev` al día; la siguiente es F9/F10 (decisión de Jhoan). No se toca `main`.
  - **Commit**: `docs(reorganizacion-modular): F8 cerrada`
