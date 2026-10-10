# Estado de la reorganización modular — punto de retoma

> **Última actualización: 2026-10-10** (**F8-05 hecha: los 14 del núcleo de `runtime` en verde (T8.27–T8.28)** — rama `reorg/f8-05-runtime-nucleo` desde `dev` @ `da82addf`, `46b6a1ec` … `f5fd0374`, PR #67 a `dev`, sin squash; `PENDIENTES=0 · ROJOS=0`; 362 mutantes válidos, 357 muertos, 4 equivalentes, 1 solo por `-timeout`; candado de rachas sin etiqueta, con 6; `GATE_RC=0`, 0 SKIP; D-F7-9 sigue sin arreglar. Antes: **F8-04b hecha: 9 de los 12 de soporte de `runtime` en verde (T8.26; D-F8-12: `welcome`, `thread` y `send` pasan a F8-05)** — rama `reorg/f8-04b-runtime-soporte` desde `dev` @ `cbebf10c`, `83565b4e` … `6216d1df`, PR #66 a `dev`, **integrado** por orden expresa de Jhoan (2026-10-10), merge `d80e7f56`, sin squash; `PENDIENTES=50 · ROJOS=40`; mutantes de `keyedmutex` y `streak` muertos; suites contra Postgres 13 + 18, 0 SKIP; `GATE_RC=0`. Antes: **F8-04 hecha (PR #65, integrado, merge `cbebf10c`): los 23 contratos de `runtime` en rojo (T8.18–T8.21)** — rama `reorg/f8-04-runtime-contratos` desde `dev` @ `e0159171`, `cf327ca5` … `de6a5411`, PR #65 a `dev`; 23 ficheros de producción, 52 de test y 391 tests; `runtimehelpertest` verde con sus dos suites (13 y 18 casos) contra el doble y escritas contra Postgres tras `integracion && pendiente`; candado de rachas tras `pendiente`; D-F8-4 en `fronteras_test.go`; `ci-local` `GATE_RC=0`, 0 SKIP; `PENDIENTES=60 · ROJOS=50`, todo en `runtime`; siguiente: **F8-04b**, el verde del soporte (T8.26). Antes, el mismo día: **F8-03 cerrada: `cart` en verde (T8.25)** — segunda mitad, rama `reorg/f8-03b-cart-verde` desde `dev` @ `8b841c8d`, `dd0771ce` … `44255a2b`, PR #63 a `dev`, **integrado** por orden expresa de Jhoan (2026-10-10), sin squash; el PR #62 de la primera mitad, **integrado** en `dev` por orden expresa de Jhoan, merge `8b841c8d`, sin squash; `cart` 20 ficheros de producción (`projection.go` partido en cuatro), goldens `cart_v1` y `cart_v2` verdes sin `-update` y `cmp` rc=0 contra los del viejo, candado de orden sin etiqueta, verde y mutado, 84 mutantes de la proyección y 84 muertos; `pendiente.Implementar` en producción de `conversacion` = 0; `ci-local` rc=0, 0 SKIP; `PENDIENTES=0 · ROJOS=0`; siguiente: **F8-04**, que desde el 2026-10-10 es solo los contratos de `runtime` (T8.18–T8.21); le sigue **F8-04b**, el verde del soporte (T8.26), D-F8-11. Antes, 2026-10-09: **F8-03 a medias: `events` verde, `cart` en rojo** — rama `reorg/f8-03-events-cart` desde `dev` @ `9d5a4b6`, `8c819b40` … `790ca522`, PR #62 a `dev`, abierto; `events` 11 ficheros y 114 exportados, suite de 51 casos en memoria y en Postgres; `cart` con sus 55 exportados en contrato, goldens y candado de orden; frontera `conversacion → catalogo` decidida (D-F8-10); `ci-local` `GATE_RC=0`, 0 SKIP; **falta T8.25, se relanza F8-03**. Antes, el mismo día: **F8-02 hecha: el motor de `conversacion` en verde** — `engine`, `menu`, `survey`, `media` y `turnoacotado` (9 ficheros, 66 exportados), rama `reorg/f8-02-motor` desde `dev` @ `42117b5`, `d8fd4ac` … `4ce435f`, PR #61, **integrado** en `dev` por orden expresa de Jhoan, merge `df340a6`, sin squash; `pendiente` del módulo = 0; `ci-local` `GATE_RC=0`, 0 SKIP; siguiente: **F8-03**. Antes, el mismo día: **F8-01 hecha: F8 EN CURSO** — inventario E-12 aprobado por Jhoan y las hojas de `conversacion` (`trigger`, `content`, `store`, `modules`; `model` venía de F5) en verde, rama `reorg/f8-01-inventario-y-hojas` desde `dev` @ `c0c0c03`, PR #60, **integrado** en `dev` por orden expresa de Jhoan, merge `8c5b27c`, sin squash; siguiente: **F8-02**. Antes: **F7-05 hecha: F7 CERRADA** — cierre local sobre `dev` @ `53e8f51`, siete commits de código (`30b0cf4`, `51d1bbb`, `fb33953` y, en la ampliación pedida por Jhoan en el mismo PR, #59, `ca222ed`, `b0056a4`, `a1a19ed` y `3d5fa59`; tests y, en `ca222ed`, el doble en memoria de `intake`; `b0056a4` y `a1a19ed`, rojo y verde de D-F7-12, son el único cambio de producción de la sesión: el push de `PUT /api/v1/intents` ya no muere con la petición, hallazgo 61 de F7; `3d5fa59` arregla una carrera del arnés en el test de P8, hallazgo 62 de F7): las cinco suites de `captacion` contra Postgres 88 PASS y 0 SKIP por binario sobre `30b0cf4`, sin divergencias con memoria (83 casos; 85 tras los casos adversarios, y una divergencia menor del doble de `intake` hallada y corregida en `ca222ed`, hallazgo 59 de F7); `make test-procesos` final, sobre `3d5fa59`: viejo y nuevo `RC=0 · PASS=1116 · FAIL=0 · SKIP=0` (104 tests de nivel superior; 0 apariciones de «el job no trae literal»; antes, sobre `a1a19ed`, dos pasadas rojas del binario nuevo por intermitencias —la carrera de D-F7-9, vista por primera vez, y una carrera del arnés del test de P8, arreglada en `3d5fa59`— y una tercera verde, hallazgo 62 de F7); ningún proceso ejercitaba el adelanto por clasificación («lo verá P4» era falso, hallazgo 51 de F7), corregido con `TestP4_AheadClassification`; 78 mutantes de las guardas SQL de `intake` contra Postgres, 8 vivos, 2 tras `fb33953` y **1 tras `ca222ed`**, declarado y equivalente en la práctica (52 y 59); gates finales sobre `3d5fa59` (último commit de código): `GOWORK=off make ci-local` `GATE_RC=0` (209 `ok`, lint `0 issues.`; cobertura, informe: `FICHEROS_EVALUADOS=300`, `POR_DEBAJO=7`); `make vet-pendiente` rc=0; `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`; `-v` del código nuevo, ahora con `apipublica` dentro de la cuenta, rc=0, 9.865 PASS, **0 SKIP** (las 8.355 de antes no la incluían: la cifra sube por lo que se cuenta, no por tests nuevos); `make ci-docker` rc=0 (lint `0 issues.`) sobre `a1a19ed`, a la segunda —la primera, rc=2 por un test de rendimiento del código viejo bajo carga, hallazgo 62 de F7—, y no repetido sobre `3d5fa59`, que solo cambia un test de `test/procesos`; la integración vieja, `GOFLAGS=-v INTEGRATION_PG_PORT=55432 make test-integration` (Postgres 16 efímero del propio target, `WAPP_TEST_REQUIRE_DB=1`), corrida sobre `13d2e59`: rc=0, 14.756 `--- PASS`, 0 FAIL, **0 `--- SKIP`** contados con `-v`, y no repetida después (el 43 (c) solo toca la cara nueva); 🟡 para F8, las marcas cruzadas de `PutSourceText` se alcanzan por `Release`/`Retry` (hallazgo 60 de F7); **el resto del hallazgo 43, decidido al cerrar** (D-F7-13, hallazgo 63 de F7: (e) y (a) se difieren, juntos, a F10 o DT-37; (b) y (d) se quedan; del 43 no queda nada abierto); rama `reorg/f7-05-cierre`, PR #59, **integrado** en `dev` por orden expresa de Jhoan en la conversación («mergear»), merge `c0c0c03`, sin squash (✎ F8-01: aquí decía «se integra»); siguiente: **F8-01**. Antes, el mismo día: **F7-04 hecha: la cara HTTP de captación y su conmutación** — H1, E1 y E2 por la cara nueva (`apipublica/reanalyze.go`, `apipublica/intents.go`; `FaseActual = 7`, 54 rutas); el arranque nuevo cablea `captacion` con `bridge_captacion.go`, `llmConfigBridge` muere y el candado INV-1 mira la captación nueva; `Conmutados` pasa a `{"acceso","edge","catalogo"}`; huella igual; `ci-local` `GATE_RC=0`, 0 SKIP; `PENDIENTES=0 · ROJOS=0`; `go list -deps` prueba lo nuevo (8 paquetes de `modulos/captacion` en `cmd/server-modular`, 0 en `cmd/server`); nada corrió contra Postgres ni contra un binario; rama `reorg/f7-04-cara-http-y-conmutar`, PR hacia `dev`; siguiente: **F7-05**. Antes, el mismo día: **F7-03 hecha: `pipeline`, `intakeahead` y `reanalisis` en verde** — 14 ficheros de producción y un doble; el puente `captacion/reanalisis → internal/flujos/events` declarado y el de `flujos/runtime` evitado; pendientes de `captacion` = 0; `ci-local` rc=0, 0 SKIP; `PENDIENTES=0 · ROJOS=0`; rama `reorg/f7-03-pipeline-reanalisis`, PR hacia `dev`; siguiente: **F7-04**. Antes, el 2026-10-08: **F7-02 hecha: `stages` en verde** — los 10 ficheros de la spec, 14 tras partir `draft` (D-F7-6) y `match_cascade` (E-13), y el puente `captacion/stages → internal/flujos/store` declarado; `ci-local` rc=0, 0 SKIP; `PENDIENTES=0 · ROJOS=0`; rama `reorg/f7-02-stages`, PR hacia `dev`; siguiente: **F7-03**. Antes, el mismo día: **F7-01 hecha: F7 arrancada** — inventario E-12 aprobado por Jhoan y las cinco hojas de `captacion` (`evidence`, `anclaje`, `intake`, `intentcfg`, `casebank`) en verde con sus cinco suites en memoria; `ci-local` rc=0, 0 SKIP; rama `reorg/f7-01-inventario-y-hojas`, PR hacia `dev`. Antes: **F6-06 hecha: F6 CERRADA** — cierre local sobre `dev` @ `68e68a4`, sin commit de código: las cuatro suites de `solicitudes` contra Postgres 140 PASS y 0 SKIP por binario, sin divergencias con memoria (136 casos); `make test-procesos` `RC=0 · PASS=1018 · SKIP=0` contra viejo y nuevo; 74 mutantes contra Postgres, ninguno vivo que no fuera un equivalente declarado, y 19 que solo mata el texto del SQL (🟡, hallazgo 63 de F6); `ci-local` `GATE_RC=0`; rama `reorg/f6-06-cierre-local`, PR hacia `dev`. Antes: **F6-05 hecha, en dos PR: la cara HTTP de solicitudes y su conmutación** — F6-05a (PR #52): 19 ficheros de producción en `internal/apipublica`, en rojo y verde, sin montar, y D-F6-12; F6-05b: el arranque nuevo cablea `solicitudes`, G1–G18 por la cara nueva (51 rutas), `FaseActual = 6`, huella igual, `PENDIENTES=0 · ROJOS=0`, `go list -deps` prueba lo nuevo (7 paquetes en `cmd/server-modular`, 0 en `cmd/server`), `ci-local` rc=0, 0 SKIP; `Conmutados` sigue `{"acceso","edge"}`; D-F6-13 (centinela de H1 en la cara vieja, muere en F7); ramas `reorg/f6-05a-cara-solicitudes` y `reorg/f6-05b-conmutar-solicitudes`, PR hacia `dev`; siguiente: **F6-06** 💻. Antes, el mismo día: **F6-04 hecha: `quotetext`, `telemetria`, `integrations` y `crmpush` en verde** — el módulo `solicitudes` queda sin pendientes (`PENDIENTES=0 · ROJOS=1`, los INV-1 hasta T6.25); suite `integrationshelpertest` 61/61 con el doble `Memoria` (sin correr contra Postgres); candado R-12 y esquema `wapp-crm-v1` verdes; primer puente (import) declarado; `ci-local` rc=0, 0 SKIP; rama `reorg/f6-04-quotetext-telemetria-integrations-crmpush`, PR hacia `dev`. Antes, el mismo día: **F6-03 hecha: `intakes` entero en verde** — almacenes (memoria y Postgres), las 9 acciones, notificador, comprador y candados; `PENDIENTES=0 · ROJOS=1` (los INV-1, hasta T6.25); `ci-local` rc=0, 0 SKIP; suite `intakeshelpertest` 50/50 en memoria y, como pre-chequeo, 50/50 contra Postgres real; rama `reorg/f6-03-intakes-almacenes-acciones-candados`, PR hacia `dev`. Antes, el mismo día: **F6-02 hecha: `intakes` entero con contrato y test, y sus tipos puros en verde** — 40 ficheros de producción en `S/intakes` (no 24), 9 tipos puros en verde, las 9 acciones, `MemoryStore`, el adaptador Postgres, el comprador y el notificador en rojo, y la suite `intakeshelpertest` (50 casos) escrita; `PENDIENTES=108 · ROJOS=43`; `ci-local` `GATE_RC=0`, 0 SKIP en el código nuevo (`-v` sobre `solicitudes`); rama `reorg/f6-02-intakes-contratos-y-tipos`, PR hacia `dev`; las tres 🟡 de los hallazgos 13–15 de F6 decididas por Jhoan el mismo día (D-F6-8…D-F6-10); siguiente: F6-03. Antes, 2026-10-07: **F6-01 hecha: F6 arranca** — inventario E-12 de `solicitudes` aprobado por Jhoan (D-F6-1 mantenida, D-F6-6 ampliada) y las hojas `integrations/sigv1`, `intakes/note.go` y `tenantvars` en verde; `ci-local` `GATE_RC=0`, 0 SKIP; rama `reorg/f6-01-inventario-y-hojas`, PR hacia `dev`; siguiente: F6-02. Antes, el mismo día: **F45-03 hecha: F4 y F5 CERRADAS**: las suites de `tenantllm` y `degradation` verdes contra Postgres (34 casos), `tenant-llm` por el cable en P4, `make test-procesos` contra el binario nuevo `RC=0 · PASS=869 · FAIL=0 · SKIP=0`, `ci-local` `GATE_RC=0`, arranque real 9/9 y abortado por una plantilla inválida; `Conmutados` sigue `{"acceso","edge"}`; rama `reorg/f45-03-cierre`, PR hacia `dev`; siguiente: F6-01. Antes, el mismo día: **F45-02 hecha: F4 conmutada —`bridge_inferencia.go`, el arranque nuevo cablea `inferencia`, 4 rutas mudadas (33), `bridge_gateway.go` borrado, `FaseActual = 4`— y F5 entero en verde —`conversacion/model`, `catalogo`, `catalogimport`, `indice`— con su conmutación nominal, `FaseActual = 5`**; huella igual; `Conmutados` es `{"acceso","edge"}` —D-F3-14 cerrada: `internal/arranque/session_identity_test.go` borrado—; rama `reorg/f45-02-conmutar-inferencia-catalogo`, PR hacia `dev`; siguiente: F45-03. Antes, el mismo día: **F45-01 hecha: inventarios E-12 de F4 y F5 aprobados e `internal/modulos/inferencia` entero en verde, sin conmutar**; rama `reorg/f45-01-inventario-inferencia`, PR hacia `dev`; siguiente: F45-02. Antes: **F3-05 hecha: F3 CERRADA**: las 7 suites de puerto con BD de `edge` verdes contra Postgres, el login de operador por el canal de control con tres Edge a la vez, la cara de `edge` por el cable y el reinicio con lease; `make test-procesos` viejo y nuevo `RC=0 · PASS=812 · SKIP=0`, `ci-local` rc=0; rama `reorg/f3-05-cierre-mtls`, PR hacia `dev`; siguiente paso, **F45-01** 💻. Antes, el mismo día: **F3-04 hecha**: `bridge_gateway.go`, la cara nueva de `edge` en `apipublica` (D1–D6) y `conmutar(edge)`: el arranque nuevo cablea **un solo** `edge/grpc.Server` y muda 6 rutas (29), `bridge_iam.go` borrado, `acceso` en `Conmutados`, huella igual; rama `reorg/f3-04-bridge-conmutar-rutas`, PR hacia `dev`; siguiente paso, **F3-05** 💻. Antes, el mismo día: las 🟡 abiertas de F3 decididas, D-F3-9 y D-F3-10, PR #40, `dev` @ `115a4ba`). Antes, 2026-10-05 (**F3-03, tanda 3 de 3 hecha: `grpc` entero**: `inference` (en 3 trozos), `plaza` y `greeting` en verde, el `InferenceResult` se entrega *inline*, el saludo cierra el job del latido y las inferencias en vuelo se cancelan al caer el stream; rama `reorg/f3-03-grpc-tanda-3`, PR hacia `dev`; siguiente paso, **F3-04** 💻. Antes, el mismo día: **F3-03, tanda 2 de 3 hecha**: `connect` (en 4 trozos), `auth`, `config_push`, `readiness` y `diagnostics` de `grpc` en verde, rama `reorg/f3-03-grpc-tandas-2-3`, PR hacia `dev`; siguiente paso, **relanzar F3-03** 💻 para la tanda 3. Antes, 2026-10-04: **F3-03, tanda 1 de 3 hecha**: inventario E-12 de `grpc` aprobado y `types`, `server`, `receipt_sink`, `worklane` y `send` en verde, rama `reorg/f3-03-grpc`, PR hacia `dev`; siguiente paso, **relanzar F3-03** 💻 para las tandas 2 y 3. Antes: **F3-02 hecha**: `fleet` y `filtercfg` de `edge` en verde, rama `reorg/f3-02-fleet-filtercfg`, PR hacia `dev`; siguiente paso, **F3-03** 💻. Antes: **F3-01 hecha**: inventario E-12 aprobado y las 7 hojas de `edge` en verde, rama `reorg/f3-01-inventario-hojas`, PR hacia `dev`; siguiente paso, **F3-02** 💻. Antes: **F2 cerrada e integrada**: los PR #33 (F2-05) y #34 (las siete 🟡 de F2 decididas, D-F2-10…D-F2-13) están en `dev`, merge `4f0ed06`. 🔴 **Desde hoy todo es local (D-R-8)**: se acabó la promoción web; toda sesión lee `PROTOCOLO-CLI.md`; sigue valiendo rama + PR. Siguiente paso, **F3-01** 💻). Antes, 2026-10-04 (**F2-05 hecha, F2 cerrada**: cierre local con las suites y los mutantes del nivel complejo contra Postgres, los procesos contra los dos binarios, el arranque real y las tres peticiones; el PR #32 de F2-04 está integrado en `dev`, merge `bfd31ce`; siguiente paso, **F3-01**). Antes, 2026-10-04 (**F2-04 hecha**: `bridge_iam.go`, la cara nueva de `acceso` en `apipublica` y `conmutar(acceso)`: el arranque nuevo cablea `acceso` y muda 23 rutas, huella igual; siguiente paso, **F2-05** 💻). Antes, 2026-10-04 (**F2-03 hecha**: `entitlements/postgres.go`, `iam/infra/postgres`, `iam/transport/http` y `platformadmin` en verde, **0 pendientes en `acceso`**, los 3 candados AST verdes y las suites contra Postgres escritas; siguiente paso, **F2-04**). Antes, 2026-10-04 (**F2-02 hecha**: `iam/infra/identity` e `iam/usecase` de `acceso` en verde, 0 pendientes en los dos paquetes; siguiente paso, **F2-03**). Antes, 2026-10-04 (**F2-01 hecha**: inventario E-12 de `acceso` aprobado por Jhoan, `auth.go` partido y las hojas simples de `acceso` en verde —`entitlements` sin `postgres.go`, `iam/domain`, `iam/ports/{in,out}`, las 7 suites de `outhelpertest` e `iam/infra/memory`—; siguiente paso, **F2-02**). Antes, 2026-10-03 (**F9-04 hecha**: bloque B2 de F9 —P4–P8 y P10, más T9.22— verde contra los dos binarios, suite entera P0–P10 `RC=0 · PASS=537 · SKIP=0`; siguiente paso, **F2-01**). Antes, 2026-10-03 (**F9-03 hecha**: bloque B1 de F9 —P1, P2, P3 y P9— verde contra los dos binarios y el mutante `maxTxAttempts = 1` cae; siguiente paso, **F9-04**). Antes, 2026-10-03 (**recalibración del plan tras el piloto**: specs F2–F10 y FX y `plan/sesiones/` alineadas con P1–P7; 81 sesiones → 56; siguiente paso, **F1-06**, ajustes de código previos a F2). Antes, 2026-10-03 (**parada de F1 resuelta**: Jhoan contestó P1–P7, `04` y `05` corregidos —`05` E-12 y §4.2—, skills y `CLAUDE.md` al día; ver `plan/DECISIONES.md` §3 y la §10 del informe). Antes, 2026-10-02 (noche, −03) (sesión **F1-04** 💻, bloque D de F1, el cierre local del piloto `nucleo/contact` sobre `dev` @ `ddcf7de`: T1.17–T1.19, [`informe-piloto.md`](plan/F1-nucleo-contact/informe-piloto.md) escrito, los dos traspasos de F1 **CERRADOS**; última fila de «Qué se hizo» y paso 2d de «Siguiente paso». **F1 espera la PARADA (T1.20)**). Antes, 2026-10-03 (UTC) (sesión **F1-03** 🌐, bloque C de F1, el adaptador `bridge_contact.go` y la conmutación de `nucleo/contact`, rama `reorg/f1-c-adaptador` sobre `origin/dev` @ `61a3c8b`: última fila de «Qué se hizo» y paso 2c de «Siguiente paso»). Antes, 2026-10-02 (sesión **F1-02** 🌐, bloque B de F1, el verde de `nucleo/contact`, rama `reorg/f1-b-verde`, que trae `dev` @ `0a377bc` —con el PR #22, D-F9-11— por el merge `a5d17b5`: última fila de «Qué se hizo» y paso 2b de «Siguiente paso»). Antes, (aplicadas las recomendaciones de la revisión independiente de S9–S11 en siete decisiones, rama `reorg/decisiones-revision-s9-s11`: última entrada de «Dónde estamos»). Antes, el mismo día, D-F1-10 aplicada en la rama de la revisión, `reorg/revision-s9-s11`, PR #20: el sufijo que exime a suites y dobles es `helpertest`. Antes, el 2026-10-01, la revisión independiente de S9–S11, sobre `dev` @ `6650e55`; y antes, al cerrar la sesión **F1-01** (🌐 · F1 · bloque A, contratos y rojo de `nucleo/contact`: PR #19, **integrado en `dev` sin squash**, merge `6650e55`). Este fichero es
> para **retomar**: dónde estamos, qué está decidido, qué falta decidir y cuál es el siguiente paso.
> Cada sesión de ejecución lo actualiza al cerrar (fase, bloque, siguiente paso, SHA).

## Dónde estamos

**Fase: F0 (andamiaje) ✅ cerrada el 2026-09-30 · F9 bloque A (el arnés) ✅ cerrado el 2026-10-01 (F9-01 🌐 + F9-02 💻), con una intermitencia de P0 diferida a F6 (H-1) · **F1 bloque A (contratos y rojo de `nucleo/contact`) escrito el 2026-10-01 (F1-01 🌐), PR #19 integrado en `dev` (`6650e55`)** · siguiente: F1-02 (el verde).** El árbol
nuevo tiene el andamiaje (`pendiente`, `candados`, `arranque`, `apipublica` vacía, `cmd/server-modular`) y, **en rojo**,
`internal/nucleo/contact` (4 contratos sin lógica: 11 `pendiente.Implementar` y 4 tests tras la etiqueta `pendiente`; del paquete
`contacthelpertest` (✎ D-F1-10, 2026-10-02: antes `contacttest`, y así lo nombran las entradas anteriores a esa fecha), el doble `EstadoMemoria` ya está en verde): **ningún módulo** de `internal/modulos/` existe aún. Existe el **plan ejecutable** en [`plan/`](plan/README.md): el marco común,
una *spec* por fase (F0–F10 y la transversal FX), el registro de decisiones y **57 sesiones** (eran 81 hasta la recalibración del 2026-10-03, que las dejó en 56; ✎ 2026-10-10, D-F8-11: 57, F8-04 se parte en dos) con
su prompt. **00-02 hecho (2026-09-30)**: Jhoan acepta en bloque las recomendaciones de
[`plan/DECISIONES.md`](plan/DECISIONES.md) §1, §2, §4, §5 y §6 (§3 sigue abierta hasta la parada de F1).

**F0-01 hecha (2026-09-30)**: entorno web verificado (`go1.26.5`, lint `v2.12.2`, `make ci-local`
`GATE_RC=0` en 217 s, **testcontainers funciona**: `TC_RC=0`, Ryuk limpia) y hook `SessionStart`
commiteado ([`06-entorno-web.md`](06-entorno-web.md) §5). Rama `reorg/f0-a-entorno-web`, PR a `dev`.

**F0-02 hecha (2026-09-30)**: `internal/pendiente` (rojo `d7600d3` → verde `f3b322c`, 100 %),
`make vet-pendiente` y `make test-pendiente` con `vet-pendiente` dentro de `ci-local` (`d74dd7f`) y
`make lint` que exige `v2.12.2` (T0.26, `d05ac3a`). `make test-pendiente` → `PENDIENTES=0`,
`ROJOS=0`, `rc=0`; gate ci-local `GATE_RC=0`. Rama `reorg/f0-b-pendiente-make`, PR a `dev`.

**F0-03 hecha (2026-09-30)**: `internal/candados` (rojo `2c2bbd6` → seis verdes, de `b2ecfce` a
`42884fb`, 91,7–100 % por fichero) y los cinco candados de fichero en `ci-local` sobre el árbol real:
`internal/modulos/{fronteras,un_fichero_un_test,exportados_cubiertos}_test.go` (`ca462a6`, lista
blanca **medida**: 13 aristas), `test/procesos/sin_bd_viva_test.go` sin etiqueta (`b522b0f`) y
`make cobertura-ficheros` (`3e85144`: `FICHEROS_EVALUADOS=7`, `POR_DEBAJO=0`). Cada uno muerde con su
caso de `testdata/` y con una demostración en el árbol real. `PENDIENTES=0`, `ROJOS=0`; gate ci-local
`GATE_RC=0` **en un *worktree* limpio** (contradicción 15 del README de F0: la raíz ignora `*.out`).
Rama `reorg/f0-c-candados`, PR a `dev`.

**F0-04 hecha (2026-09-30)**: `internal/arranque`, copia del viejo (`d64dbbf`: 21 + 19 ficheros, 53
tests, 0 SKIP), `cmd/server-modular` (`a953834`), `internal/arranque/huellatest` (rojo `c7ae487` →
verde `141d960`, 91,8 %), la huella del viejo en una dorada (`fde5849`: `huella_vieja_test.go`, único
fichero nuevo en `internal/bootstrap`; 95 rutas = 22 + 73, 2 rpc, 11 familias) y el candado
`TestHuella`/`TestHuellaEstatica` (`61ce04b`: 10 goroutines, **13** *hooks*, entorno ∅; muerde con una
ruta de menos y una goroutine de más). Gate ci-local `GATE_RC=0` en cada commit sobre un *worktree*
limpio. Contradicciones 17–21 del README de F0; la **19** (la fase 3 corre entera contra un S3 falso
en proceso, en vez de simular `flowDeps`) pide la mirada de Jhoan. Rama `reorg/f0-d-arranque-huella`
(`61ce04b` + cierre), PR a `dev` sobre `origin/dev` @ `80807ba`.

**F0-05 hecha (2026-09-30)**: bloque E y el tramo F0 de FX (TX.1–TX.4).
- **`internal/apipublica`**: la `Cara` y el estrangulador, rojo `5a11f5b` → verde `de0c29b` (100 %
  por fichero). Va montada **vacía** delante del `publicapi` viejo en el `:8103` (`9dcf7e8`), con una
  aserción de cableado.
- **Candado de mudanzas** (`7b7e01f`): `mapa.tsv` de 95 filas, `FaseActual = 0` y las 73 rutas
  resueltas por `Compuesto.Resolver`.
- **Los tres ✎ de `platform`**, con alias D-F0-3: `6d83620`, `b65b788` y `5305134`. `Agregado` va
  en el paquete hoja `platform/metrics/inferencia`; `go list` da 0 aristas `platform → dominio`.
- **Barridos AST viejos ciegos al árbol nuevo** (D-F4-1, `dd1e2bd`).
- **Deriva documental cerrada** (`8096232`).
- **Traspaso** (`15223ff`).
- **Verificación**: huella igual a la dorada tras cada paso; `GATE_RC=0` en *worktree* de ruta fija
  sobre `5a11f5b`, `de0c29b`, `7b7e01f` y `dd1e2bd`; `PENDIENTES=0`, `ROJOS=0`.
- **Contradicciones 23–30** del README de F0: la 23, la 24 y la 27 piden la mirada de Jhoan.
- Rama `reorg/f0-e-cara-platform`, PR a `dev` sobre `origin/dev` @ `d3deb27`.

**F0-06 hecha (2026-09-30, 💻)**: cierre local de F0 con `go1.26.5` y `golangci-lint v2.12.2` (la ficha daba
por hechos Docker y la toolchain; no lo estaban y se prepararon aislados).
- **T0.24**: la rama de la web ya estaba en `dev` (PR #17, `835a7be`, sin squash); los 34 SHA de T0.0–T0.27 son ancestros
  de `origin/dev`; gate `ci-local` en un *worktree* limpio: `GATE_RC=0`, 84 líneas `ok` (regla de conteo: abajo, «Revisión independiente»), `0 issues`; idéntico a la web.
- **T0.22**: integración vieja con Postgres real: `rc=0`, **0 SKIP, 0 FAIL, 4.618 PASS**; los 8 `TestCollector_*` pasan de
  SKIP a PASS.
- **T0.23**: arranque real de `cmd/server-modular`: **9/9** fases (migraciones desde cero y `HeadBucket` real), `/healthz`
  200, SIGINT limpio; contraste con el viejo: los mismos códigos en las 98 peticiones del barrido.
- **§7 del traspaso refutada contra lo que corre**: el 502 de `/admin/messages/send` con sesión offline (y su gemelo en
  `:8103`), los seis campos de `public.audit_events`, y las cinco `wapp_edge_*` con un Edge de mentira mTLS. Hallazgos
  nuevos: contradicciones 31–33 del README de F0.
- Detalle en el `CERRADO` de [`traspasos/TRASPASO-F0-andamiaje.md`](traspasos/TRASPASO-F0-andamiaje.md).

**F9-01 hecha (2026-10-01, 🌐)**: el arnés de procesos (ola 9A, adelantada por D-F9-1) y el proceso P0, rama
`reorg/f9-a-arnes` sobre `origin/dev` @ `45e01a4`, PR a `dev`.
- **T9.1–T9.4**: decisiones copiadas al README de F9 (`a374cdb`); testcontainers-go v0.44.0 en un commit `chore(deps)` aislado
  (`37c7db7`, T-2: sube `httpsnoop`, `otelhttp` y `klauspost/compress` de producción); el candado `sin_bd_viva` prohíbe también
  `os.Environ`, `t.Skip` y `testing.Short` (`f300aff`); `make test-procesos` y `vet-integracion` dentro de `ci-local` (`b5f1601`).
- **T9.5–T9.11**: `TestMain` + base clonada (`576ba9a`), PKI y claves (`2e2ecc1`), dobles de S3 e identity (`7c63d9e`), servidor por
  proceso (`660947d`), clientes y fixtures (`fa03e6b`), Edge de prueba (`5519343`) y **P0** (`10179d6`).
- **Verificación web (no cierra nada)**: `ci-local` `GATE_RC=0` (84 líneas `ok`, 0 issues, lint `v2.12.2`); `make test-procesos` viejo y
  nuevo `RC=0`, 146 PASS · 0 SKIP · 0 FAIL por binario, dos pasadas y `CUENTA=3` (438 PASS). **D-F9-2 confirmada ejecutándola**:
  con endpoint IP el SDK de S3 hace *path-style* (una `HEAD /<bucket>`); no hace falta ninguna opción nueva en el arranque.
- **Contradicciones 10–18** del README de F9; la 11 (`key_source=base64` del lease, no `config`), la 12 (9 métricas sin tráfico, no
  17) y la 15 (cómo medir «sin Docker») piden la mirada de la local. Traspaso: [`traspasos/TRASPASO-F9-arnes.md`](traspasos/TRASPASO-F9-arnes.md).

**F9-02 hecha (2026-10-01, 💻)**: cierre local del bloque A, sobre `dev` @ `af7b8e9` (PR #18 ya fusionado), con `go1.26.5` y `golangci-lint v2.12.2`
(el Go y el lint del sistema, `1.27.1` y `2.14.0`, no sirven).
- **T9.5, T9.8, T9.11 y T9.12 → `[x]`**. `make test-procesos`: pasadas 2 y 3 y `CUENTA=3` con `RC=0 · PASS=146 (438) · FAIL=0 · SKIP=0` en los dos
  binarios (≈ 25–33 s de pared; T9.30 la necesita); **la pasada 1 dio `RC=1` contra `nuevo`** (ver H-1 abajo). `ci-local` `GATE_RC=0` (84 líneas `ok` = 79 paquetes con tests + 5 que `cobertura-ficheros` vuelve a correr; 0 issues).
- **T-2 cerrada con `-v`**: integración vieja `RC=0 · 4.631 PASS · 0 SKIP · 0 FAIL` (79 paquetes `ok`: aquí sí son paquetes, los 79 con tests; los +13 frente a F0-06 son los casos del candado de T9.3,
  medidos); `go mod tidy` sin cambios, `go mod verify` OK, 0 testcontainers en los dos binarios.
- **Refutado**: R9.1.d (`DOCKER_HOST=unix:///nada` tampoco falla en macOS; corregido con `HOME` vacío) y la «escapatoria» `Parar` del `Cleanup`. **No refutado**:
  el alcance extra de `TestArnes_EdgeFrames` ni las 9 métricas de P0 (las 7 que faltan son `CounterVec`).
- **H-1 · diferida a F6 (decisión de Jhoan, 2026-10-01; D-F6-7)**: `TestP0_Arranque/sin_errores` es una carrera con la parada (el *webhook worker* loguea `ERROR` si el SIGTERM llega mientras hace su primera
  llamada a BD), con código compartido por los dos binarios; 1 fallo en 161 arranques en frío. No se tocó ni el test ni producción: se evalúa al reconstruir `integrations` en F6 y queda anotada en `deuda.md` §5, [`plan/F9-procesos/diseno.md`](plan/F9-procesos/diseno.md) §4 (el de F9, no el de F6) y T6.12/T6.20/T6.27. Contradicciones 19–21 del README de F9.
  ⚠️ Revisión independiente (2026-10-01): el mismo patrón está, sin haberse observado, en otras tres goroutines de fondo (colector de `platform`, agregador de F8, pipeline de F7) que F6 no reconstruye: nota de revisión de la [contradicción 19](plan/F9-procesos/README.md).
- Detalle en el `CERRADO` de [`traspasos/TRASPASO-F9-arnes.md`](traspasos/TRASPASO-F9-arnes.md).

**Revisión independiente de S9–S11 (2026-10-01)**: F9-01, F9-02 y F1-01 revisadas sobre `dev` @ `6650e55` (alcance `45e01a4..6650e55`).
Correcciones de código y de documentación en la rama `reorg/revision-s9-s11`: candado `SinBDViva` `1c247f9` · arnés `af372b6`, `84021ef`,
`5db3a72`, `41db3e4` · `Makefile` `1e135e5` · `nucleo/contact` `01ae55a`, `73eb2a5` · documentación `1c1040d`, `4fe2291`, `79cc073` y el commit de cierre.
**Gates sobre la rama, ya con las correcciones** (misma toolchain): `ci-local` `GATE_RC=0` (86 líneas `ok`, lint `0 issues`,
`FICHEROS_EVALUADOS=9 · POR_DEBAJO=0 · EXENTOS_POSTGRES=1`); `PENDIENTES=11 · ROJOS=4` (sin cambio); los dos `vet` rc=0; SKIP en código
nuevo 0; `make test-procesos`: `viejo RC=0 · PASS=158 FAIL=0 SKIP=0` y `nuevo RC=0 · PASS=158 FAIL=0 SKIP=0` (+12 por los tests de las
correcciones); el diff del código viejo (`internal/flujos`, `bootstrap`, `gateway`, `intakes`, `publicapi`, `cmd/server`) frente a `dev`, vacío.
- **Gates medidos hoy** con `go1.26.5` y `golangci-lint v2.12.2`: `ci-local` `GATE_RC=0` (86 líneas `ok`), lint `0 issues`,
  `FICHEROS_EVALUADOS=9 · POR_DEBAJO=0 · EXENTOS_POSTGRES=1`; `make test-pendiente` `PENDIENTES=11 · ROJOS=4`; `go vet -tags pendiente ./...`
  y `go vet -tags integracion ./test/procesos/...` rc=0; SKIP en código nuevo: 0; E-1: el diff del código viejo en el alcance, vacío;
  pre-chequeo `make test-procesos` en local: `viejo RC=0 PASS=146` y `nuevo RC=0 PASS=146`, 0 SKIP.
- **Hallazgos y decisiones que abre** (ninguna tomada): de F1, los hallazgos 21–24 y D-F1-10…D-F1-12 del
  [README de F1](plan/F1-nucleo-contact/README.md); de F9, la nota de revisión de la contradicción 19, las contradicciones 22–30 y
  D-F9-6…D-F9-12 del [README de F9](plan/F9-procesos/README.md).
- **Regla de conteo de «N `ok`»** (regla 6 del `CLAUDE.md` del ecosistema): los «84» y «86» de este fichero son **líneas `ok` del log de
  `make ci-local`**, no paquetes distintos: los paquetes con tests que corre `make test` más los que `make cobertura-ficheros` vuelve a
  correr. 84 = 79 + 5 (en `835a7be`, `6ee1c5e` y `77df20f`: 88 paquetes, 79 con tests); 86 = 80 + 6 (en `6650e55`: 90 paquetes, 80 con
  tests; entra `contacttest`, y `nucleo/contact` no da línea `ok` porque sus tests van tras la etiqueta `pendiente`). Los documentos de
  F0 y los traspasos cerrados que dicen «84 paquetes `ok`» cuentan con esta misma regla.
- **2026-10-02 · D-F1-10 aplicada** (decisión de Jhoan; misma rama, PR #20): el sufijo de nombre de paquete que exime a las suites de
  contrato y a los dobles de los tres candados de fichero (`un_fichero_un_test`, `exportados_cubiertos`, cobertura por fichero) pasa
  de `test` al compuesto **`helpertest`**. `a18d4c0`: `internal/nucleo/contact/contacttest` → `contacthelpertest` (movimiento puro).
  `06f08a8`: una definición, `isHelperTestPackage` (`internal/candados/candados.go:101`), para los tres candados y para
  `haySuiteContrato`; 17 mutantes, los 17 caen. `internal/arranque/huellatest` no se renombra (lo importa un test del código viejo,
  E-1), pierde la exención y vuelve a medirse sin morder: `FICHEROS_EVALUADOS` pasa de 9 a **10** (`POR_DEBAJO=0 ·
  EXENTOS_POSTGRES=1`). Cierra el hallazgo 21 del README de F1 y el «pendiente de mirar» de D-F1-6; deja abierta **D-F1-13** (los
  dobles con lógica dentro de `…helpertest` siguen sin medirse). Las cifras de arriba son las medidas el 2026-10-01 y no se
  reescriben. Gates sobre la rama, ya con D-F1-10 y con la toolchain fijada (línea siguiente): `make ci-local` `GATE_RC=0` (86 líneas
  `ok`, 0 issues, `FICHEROS_EVALUADOS=10 · POR_DEBAJO=0 · EXENTOS_POSTGRES=1`); `PENDIENTES=11 · ROJOS=4`; SKIP en código nuevo 0
  (430 PASS); `make test-procesos` `viejo RC=0 · PASS=158` y `nuevo RC=0 · PASS=158`, 0 FAIL, 0 SKIP; `make ci-docker` rc=0.
- **2026-10-02 · La toolchain fijada se usa sola** (decisión de Jhoan; misma rama, PR #20): no se aceptan dos versiones de Go ni de
  linter; lo que cambia es que la fijada ya no pide trabajo manual en la sesión local, cuyo `PATH` trae `go1.27.1` y
  `golangci-lint 2.14.0`. `0b78cd1`: el `Makefile` exporta `GOTOOLCHAIN=go$(GO_VERSION)`, `fmt-check` usa el `gofmt` de esa
  toolchain, `make tools` deja el `golangci-lint v2.12.2` oficial (sha256 verificado) en `.bin/` y `make toolchain` dice lo que corre
  de verdad. `26cbfbf`: el hook de `SessionStart` delega en `make toolchain`. `3ed9bd0`: `ci-docker` monta `GOMODCACHE` e instala el
  linter con `make tools`. Los gates de la línea anterior se midieron con el entorno **pelado** del Mac, sin exportar nada.
  ⚠️ Un `go` suelto, fuera de `make`, sigue siendo el del sistema: en local lleva `GOTOOLCHAIN=go1.26.5` delante. La diferencia entre
  web y local, en [`06-entorno-web.md`](06-entorno-web.md).
- **2026-10-02 · Aplicadas las recomendaciones de la revisión independiente de S9–S11** (a petición de Jhoan de aplicar las
  recomendaciones; se confirman al integrar el PR; rama `reorg/decisiones-revision-s9-s11`, partida de `dev` @ `c63aca7`, el merge del
  PR #20). Jhoan **no** decidió cada una por separado. Son siete, cada una en su commit:
  **D-F1-11** `4b226c9` (solo `.md`): R-27, R-28 y R-29 entran en la spec de P3 de F9 (`diseno.md` §4, pasos 6–8; R9.6.d; T9.15);
  queda «sin medir» si P3 vigila el reintento de `postgres.WithTx`.
  **D-F1-12** `1507d78` (solo `.md`): `05` cumple E-11 —el ejemplo de §10 en inglés, las dos firmas de D-F1-1 bajo la tabla de E-3,
  `sin_pendientes_test.go` → `no_pending_test.go`, el sufijo `_contrato` es vocabulario del método—.
  **D-F9-12** `5b817ba`: el Edge de prueba aplica a la inferencia el gate de lease del Edge real (2 s de gracia, sondeo de 50 ms,
  `InferenceResult{INFERENCE_ERROR_LEASE_INVALID}`, sin `Ack`); sin contador de bloqueos.
  **D-F9-7** `b7321bb`: `TestMain` sale con código 2 si `GOWORK` no es `off` (rc=2 el binario de test; `go test` lo devuelve como rc=1).
  **D-F9-8** `2a5aea1`: `TestMain` barre al entrar los directorios `procesos-<cifras>` huérfanos, solo los que llevan su marcador y
  tienen más de una hora. 🔴 Al probarlo, una mutación corrida sin aislar `TMPDIR` borró los tres directorios del 2026-10-01 que
  citaba la contradicción 24 del README de F9; no los borró el barrido publicado.
  **D-F9-6** `0e3a0f3`: `SinBDViva` añade la lista blanca de quién abre conexiones (solo `test/procesos/base_test.go`) y conserva la
  negra entera; 64 mutantes, caen los 64; lo que sigue sin ver, en `TestSinBDVivaKnownGaps` (10 casos).
  **D-F1-13** `88b1d85`: de un paquete `…helpertest`, la cobertura por fichero exime solo los ficheros de suite (`contrato.go`,
  `*_contrato.go`); `FICHEROS_EVALUADOS` pasa de 10 a **11** (entra `contacthelpertest/estado.go`, 91,1 %; medido por la sesión que lo
  implementó).
  **Registro**: las siete tienen fila en [`plan/DECISIONES.md`](plan/DECISIONES.md) (§2 y §4) y su nota ✎, con lo que cada una deja
  abierto, en los hallazgos 21–23 del [README de F1](plan/F1-nucleo-contact/README.md) y en las contradicciones 22–24 y 30 del
  [README de F9](plan/F9-procesos/README.md). De paso: `GOWORK=off` en las invocaciones `go test` documentadas que no lo llevaban,
  `no_pending_test` en los README de F10 y F0, F0 `diseno.md` §4.2/§4.4/§4.5 y la skill `procesos-testcontainers` al día, y dos
  hallazgos nuevos en el README de F1, **sin corregir** (25: firmas de suite contrarias a D-F1-1 en specs de F4, F8, F9 y el marco;
  26: restos de `05`).
  **Queda abierto**, todo ya con fila en `DECISIONES.md`: **D-F9-9**, **D-F9-10**, **D-F9-11** (recomendación sí; se aplica en un PR
  aparte, a continuación de este; ✎ ya aplicada, abajo), **D-F1-7**, **D-F1-8**, **D-F1-9** y **D-F1-14** (nueva: extender «solo los ficheros de suite» a
  `un_fichero_un_test` y `exportados_cubiertos`).
  **De paso, el gate**: `make lint` usa ahora una caché de `golangci-lint` **por *checkout*** (`.bin/lint-cache`; `fdbc0b2`, `6649ee6`).
  La del usuario, compartida entre *worktrees*, devolvió en esta rama 36 *issues* falsos (rc=2), todos con rutas de *worktrees* ya
  borrados y sobre líneas con su `//nolint`; con caché propia, 0.
  Gates (entorno pelado de la máquina local, rc leído del log): `make toolchain` → `TOOLCHAIN=OK`; `make ci-local` → `GATE_RC=0`, 86
  líneas `ok`, lint 0 *issues*, `FICHEROS_EVALUADOS=11 · POR_DEBAJO=0 · EXENTOS_POSTGRES=1`; `make test-pendiente` → `PENDIENTES=11 ·
  ROJOS=4` (sin cambio); `make ci-docker` → rc=0 (estos cuatro sobre `6649ee6`). `make test-procesos` → `viejo` y `nuevo` `RC=0 ·
  PASS=176 · FAIL=0 · SKIP=0` (158 en `dev`) y SKIP en código nuevo 0 con 674 PASS (estos dos sobre `88b1d85`; después solo cambian
  `.md` y el `Makefile`). **No corrido**: `make test-integration`, UAT.
- **2026-10-02 · D-F9-11 aplicada: los ficheros de test largos de `test/procesos`, partidos por tema** (misma petición de Jhoan; se
  confirma al integrar el PR; rama `reorg/partir-tests-procesos`, encima de `reorg/decisiones-revision-s9-s11`). Cinco commits
  `refactor(procesos)`, uno por fichero de origen (`6ed524b`, `4ac3dbd`, `c8f7d31`, `e9d93f4`, `eae418d`): 5 ficheros → 34, el
  paquete pasa de 14 a 43 ficheros `.go` y ninguno pasa de 500 líneas. **Solo se mueven declaraciones**: 461 de primer nivel y 140
  *specs*, iguales byte a byte, 0 comentarios perdidos (`go/parser` sobre los bytes del fuente). Detalle en la contradicción 29 del
  [README de F9](plan/F9-procesos/README.md).
  Cada pieza se llama `<fichero de origen>_<tema>_test.go` (`edge_falso_*`, `servidor_*`, `clientes_*`, `pki_*`, `p0_arranque_*`;
  indicación de Jhoan al revisar; `b4d9417`, 27 renombres sin tocar una declaración).
  Gates (sobre `b4d9417`, el último commit con código; rc leído del log): `make ci-local` → `GATE_RC=0`, 86 líneas `ok`, lint 0
  *issues*, `FICHEROS_EVALUADOS=11 · POR_DEBAJO=0`; `make test-procesos` → `viejo` y `nuevo` `RC=0 · PASS=176 · FAIL=0 · SKIP=0`, y
  los nombres de los 176 PASS, idénticos a los de antes de partir en los dos binarios. **No corrido**: `make ci-docker` (no cambia
  nada de lo que mira frente al PR anterior salvo ficheros con etiqueta `integracion`), `make test-integration`, UAT.

**Siguiente paso:**
1. **Jhoan**:
   - revisar las contradicciones 23, 24 y 27 del README de F0 (y la 19 de F0-04; la 27 bloquea F2, no F0);
   - para repetir el gate local: `GOTOOLCHAIN=go1.26.5` y `golangci-lint v2.12.2` en el `PATH`
     (`GOTOOLCHAIN=go1.26.5 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`).
2. **F1-01 ✅ hecha (🌐, 2026-10-01)**: bloque A del piloto `nucleo/contact`, T1.1–T1.7 + T1.3b; **PR #19 integrado en `dev` sin squash** (merge `6650e55`, 2026-10-01:
   rojo y verde de `candados` y `nucleo` siguen siendo commits distintos). **Jhoan**: decidir D-F1-7 y D-F1-8 del README de F1 (la 8 bloquea T1.13, bloque B) y mirar las que siguen abiertas de la revisión independiente (D-F9-9, D-F9-10 y D-F1-14, además de D-F1-9; D-F9-11 ya está aplicada, en su PR; ✎ 2026-10-02: **D-F1-10 ya está decidida**, y D-F1-11, D-F1-12, D-F1-13, D-F9-6, D-F9-7, D-F9-8 y D-F9-12 llevan aplicada la recomendación de la revisión, que se confirma al integrar el PR). Luego **F1-02 (🌐)**: el verde,
   T1.8–T1.13 ([`plan/sesiones/`](plan/sesiones/README.md)). Las F9-03…F9-05 (B1/B2) van **después** de la parada de F1 (F1-05), por D-F9-1. Para repetir los gates en local: `GOTOOLCHAIN=go1.26.5` y `golangci-lint v2.12.2` en el `PATH`.
2b. **F1-02 ✅ escrita (🌐, 2026-10-02)**: bloque B, T1.8–T1.13, rama `reorg/f1-b-verde` (último commit de código `4bc398d`), **PR hacia `dev`: integrar SIN squash**. D-F1-7 y D-F1-8 ya decididas. **Jhoan**: integrar el PR (✎ hecho: PR #23, merge `ca364de`) y decidir **D-F1-9** antes de T1.14 (✎ decidida el 2026-10-02: la recomendación; el adaptador se llama `bridge_contact.go`, rama `reorg/decision-d-f1-9`). Luego **F1-03 (🌐)**: bloque C (T1.14–T1.16). La sesión local **F1-04** corre T1.18 con el [traspaso del bloque B](traspasos/TRASPASO-F1-B-suite-postgres.md), además del de T1.16.
2c. **F1-03 ✅ escrita (🌐, 2026-10-03)**: bloque C, T1.14–T1.16, rama `reorg/f1-c-adaptador` (rojo `09f4b72`, verde `0c2bddf`, refactor `a62abea`, conmutar `ce98595`), **PR hacia `dev`: integrar SIN squash**. El arranque nuevo construye el resolver de contactos con `newContactResolver(db, cipher, kp)`: el `PostgresResolver` **nuevo** tras `contactBridge`, con las claves de la fase 3 (test de cableado; huella igual). **Siguiente: F1-04** (💻, bloque D, T1.17–T1.19), que recibe **dos** traspasos abiertos: [`TRASPASO-F1-B-suite-postgres.md`](traspasos/TRASPASO-F1-B-suite-postgres.md) y [`TRASPASO-F1-nucleo-contact.md`](traspasos/TRASPASO-F1-nucleo-contact.md). **Jhoan**: integrar el PR; D-F1-15 y D-F1-16 (nuevas, abiertas) no bloquean F1-04.
2d. **F1-04 ✅ hecha (💻, 2026-10-02)**: bloque D, T1.17–T1.19, sobre `dev` @ `ddcf7de` (PR #23, #24 y #25 ya integrados sin squash). Gates repetidos con la toolchain fijada, idénticos a la web; la suite de `contact` contra Postgres viejo y nuevo rc=0 · 20 PASS · 0 SKIP; `make test-procesos` viejo y nuevo `RC=0 · 196 PASS · 0 SKIP`; arranque real de `server-modular` 9/9 y `healthz` 200. Las dos §7 refutadas con mutantes: hallazgos **35–41** del README de F1 (la marca de `Estado` solo vigila `current_node`; R9.4.d mira el paquete y no está en ningún gate; el reintento de `WithTx` sin test de ejecución en ningún árbol; el test de cableado no ve lo que reciben las fases 6 y 7; el corpus de equivalencia es ciego a un mutante realista). ✎ **La PARADA (T1.20) se resolvió el 2026-10-03** (Jhoan contestó P1–P7; §10 del [`informe-piloto.md`](plan/F1-nucleo-contact/informe-piloto.md)). **Siguiente**: (1) una **sesión de ajustes de código** previa a F2 —`cobertura-ficheros` de gate a informe, candados sobre `bridge_*.go` y `Conmutados`, test de cableado, marca de `Estado`, R9.4.d en un gate, corpus de equivalencia—; (2) una **sesión de recalibración** de las specs F2–F10 y `plan/sesiones/` con E-12; luego F9-03/04/05 y F2. Hasta entonces las specs F2–F10 dicen lo de antes donde choquen con `05`: **manda `05`**.
3. **H-1 no bloquea nada**: se difirió a F6 (D-F6-7). Hasta entonces, un rojo de `TestP0_Arranque/sin_errores` con las dos líneas `ERROR` del *webhook worker* es esa carrera: se repite
   **una vez**, se compara y se anota (≈ 1–2 % de falso rojo por corrida). ⚠️ Revisión independiente (2026-10-01): la regla se **matiza**
   —vale para un rojo cuyas líneas `ERROR` sean todas de cancelación, de una goroutine de fondo y de la parada, no solo para «las dos
   del worker»— en la [contradicción 19 del README de F9](plan/F9-procesos/README.md).

**Recalibración hecha (2026-10-03, 💻, solo documentación)**: las decisiones P1–P7 de la parada de F1 están aplicadas
a las specs de F2–F10 y FX, al marco (`plan/00-marco/`), a los dos protocolos y a `plan/sesiones/`.
- **Specs**: cada fase F2–F8 abre con su **inventario E-12** (nivel por archivo + adaptadores `bridge_<x>.go`; lo aprueba
  Jhoan) y trae una clasificación **provisional** por paquete. Fuera el umbral de cobertura como condición de cierre:
  un test por promesa del contrato, mutantes en el nivel complejo, procesos de F9. Adaptadores `bridge_<x>.go` con test
  de cableado; `Conmutados` cuando muere el último. Suite con `Montaje` para todo puerto con BD.
- **F9**: hallazgos 35–41 de F1 dentro de la spec (T9.15 **exige** que caiga el mutante del reintento de `WithTx`;
  R9.5.c, la marca de `Estado`; R9.4.d corregida y a un gate). F9 pasa a **solo local**.
- **Sesiones**: 66 pendientes → **41** (13 🌐 · 6 🌐❓ · 21 💻 · 1 🧑); F4 y F5 comparten las tres `F45-*`. Cierre fijo de
  tres cosas. Ficha nueva [`F1-06`](plan/sesiones/F1-06-cli-ajustes-previos-a-f2.md) (código, 💻).
- **Abierto**: D-R-1…D-R-6 en [`plan/DECISIONES.md`](plan/DECISIONES.md) §7 (dónde corre la suite contra Postgres, las
  segundas instancias viejas frente a P5, `Conmutados` sin adaptador, el prefijo de commit del nivel simple y el tamaño
  de sesión, que está **sin medir**).
- ✎ **F1-06 lo cambió el mismo día** (bloque siguiente): `make cobertura-ficheros` ya es informe.
- ✎ **D-R-1…D-R-6 decididas el 2026-10-03** (Jhoan: se aplica la recomendación de cada una; un commit por decisión en `dev`). D-R-5 ya no bloquea F2-01, ni D-R-1 a F2-03. Siguen abiertas D-F9-9 y D-F9-10, que no tienen recomendación firme. ✎ 2026-10-08: **D-F9-10 decidida** (Jhoan): salidas (a) + (c), fila en `plan/DECISIONES.md` §4.

**F1-06 · ajustes de código previos a F2 (2026-10-03, 💻, sobre `dev` @ `3e181f6`).** Seis commits, uno por ajuste:

- **A1** `0689b4e` · `make cobertura-ficheros` es un **informe**: rc=0 con ficheros por debajo, sin exentos (los
  adaptadores Postgres se miden; fuera `EXENTOS_POSTGRES`). Un error real sigue rompiendo.
- **A2** `1622231` · `un_fichero_un_test` y el informe ven los `internal/arranque/bridge_*.go`, y solo esos (D-F1-16).
- **A3** `0a91857` · `Conmutados`: entra cuando muere el último adaptador (D-F1-15); sigue vacía.
- **A4** `cccee37` · el test de cableado prueba que ninguna fase importa el `contact` viejo fuera de
  `bridge_contact.go` y `flows.go` (hallazgo 39).
- **A5** `9001720` · la marca de `Estado` viaja en cinco columnas de `flow_state` (hallazgo 35).
- **A6** `7937772` · R9.4.d es un candado por fichero (`candados.ProcessImports`) dentro de `ci-local`, y el corpus de
  equivalencia gana 58 filas adversarias (hallazgos 36, 37, 40).
- **Mutantes**: todos los que sobrevivían caen (tabla en el [README de F1](plan/F1-nucleo-contact/README.md),
  «Hallazgos de la sesión F1-06», con los hallazgos 42–51).
- **Gates** (toolchain fijada, rc sin pipe): `make ci-local` rc=0 (`FICHEROS_EVALUADOS=18`, `POR_DEBAJO=1`, lint
  0 issues) · `make vet-pendiente` rc=0 · `make test-pendiente` rc=0 (`PENDIENTES=0`, `ROJOS=0`) · código nuevo con `-v`:
  1193 PASS, **0 SKIP** · suite de `contact` en memoria 20 PASS, 0 SKIP, y contra Postgres (testcontainers,
  `TestContactContrato_Postgres`, binario `nuevo`) 20 PASS, 0 SKIP.
- **No corrido**: `make test-procesos` entero contra los dos binarios (la sesión solo pedía la suite de `contact`) ni la
  integración vieja (no se tocó código compartido).
- **Siguiente paso: F9-03** (F9-B). `main` sin tocar.

**F9-03 · F9 bloque B1, procesos de plataforma y acceso (2026-10-03, 💻, rama `reorg/f9-b1` desde `dev` @ `25052d1`).**
Sesión completa (escribe, corre y cierra). Cuatro commits, uno por proceso, escritos por sub-agentes en *worktrees*
fijados en `25052d1` e integrados con `cherry-pick` en la rama; llega a `dev` por PR, sin squash:

- **T9.13** `679ea52` · **P1** `TestP1_EnrollmentAndLease`: alta, código, enrolamiento, lease, renovación, revocación,
  corte comercial y reactivación, con el candado de la doble llave (ADR-0007) como aserción.
- **T9.14** `052089e` · **P2**: `TestP2_InvitacionUnSoloCanje` (R9.6.a), `TestP2_RutasDePlataformaDenegadasAlCliente`
  (R9.6.b, I-CP-5) y `TestP2_ExchangeAndPermissions`.
- **T9.16** `8febd52` · **P9** `TestP9_DiagnosticsAndConfigPush`: diagnóstico remoto (ciclo, TTL, opt-out, aislamiento) y
  configuración empujada (`intents`, `filters`, reconexión).
- **T9.15** `250916a` · **P3**: `TestP3_IncomingToReply` (con el literal del aviso leído del `.md`), las tres reglas de
  `contacts` (`TestP3_LatePushNameIsSealed`, `TestP3_FirstPushNameWins`, `TestP3_HistoryBurstWithoutDeadlock`) y
  `TestP3_TxRetryOnSerializationFailure`. El Edge de prueba gana `sealedIncoming` (`push_name`, `from_lid`).
- **`make test-procesos`** (rc leído del log, suite entera, sobre el código final), **dos pasadas**:
  viejo `RC=0 · PASS=287 · FAIL=0 · SKIP=0` y nuevo `RC=0 · PASS=287 · FAIL=0 · SKIP=0`, las dos veces (≈ 15 s por
  binario). Antes de arreglar el lint, otras dos pasadas con el mismo resultado. **Ningún rojo solo contra el nuevo.**
- 🔴 **El reintento de `postgres.WithTx`, ejecutado de verdad (R9.6.e)**: `TestP3_TxRetryOnSerializationFailure` provoca
  un `40P01` real en el `Resolve` del contacto (dos transacciones del test hacen de compuerta; la suya con
  `deadlock_timeout = 60s`, así que la víctima es el servidor) y afirma que `pg_stat_database.deadlocks` sube, que el
  entrante se contesta y el sobre queda sellado, y que no hay `ERROR`. **Mutante `maxTxAttempts = 1`** (copia desechable,
  ya borrada): el test **cae 3 de 3 contra el viejo y 3 de 3 contra el nuevo**, con
  `(c) el entrante se perdió: WithTx no reintentó tras el deadlock: … runtime: resolver contacto: postgres: transacción tras 1 intentos (último deadlock/serialización): contact: buscar ref: ERROR: deadlock detected (SQLSTATE 40P01)`.
  Control sin mutar: 3 de 3 en verde en los dos. Con el mutante cae además `TestP3_HistoryBurstWithoutDeadlock` (medido
  por el sub-agente, solo contra el viejo).
- **Gates** (toolchain fijada, rc sin pipe): `make ci-local` `GATE_RC=0` (89 líneas `ok`, lint 0 issues,
  `FICHEROS_EVALUADOS=18`, `POR_DEBAJO=1`) · `make vet-pendiente` rc=0 · `make test-pendiente` rc=0 (`PENDIENTES=0`,
  `ROJOS=0`). La primera corrida de `ci-local` dio **`GATE_RC=2`** por 19 avisos de lint en los ficheros nuevos
  (corregidos sin tocar aserciones y fundidos en su commit; hallazgo 31 del README de F9).
- **Sin tocar**: `internal/**` y `cmd/**` (`git diff --stat dev -- internal cmd` vacío). Ningún fichero de
  `test/procesos` pasa de 500 líneas (el mayor, `p9_diagnostico_config_push_test.go`, 498).
- **No corrido**: `CUENTA=3 make test-procesos` (es de T9.30) y la integración vieja (no se tocó código compartido).
- **Hallazgos 31–43** en el [README de F9](plan/F9-procesos/README.md), «Hallazgos de la sesión F9-03».
- **Siguiente paso: F9-04** (F9 bloque B2). `main` sin tocar.

**F9-04 · F9 bloque B2, procesos de negocio, y cierre de B1 + B2 (2026-10-03, 💻, rama `reorg/f9-b2` desde `dev` @ `7b092d5`).**
Sesión completa. Siete commits de proceso, escritos por sub-agentes en *worktrees* (P4 y P10 sobre `7b092d5`; P5–P8 a la
vez sobre `49d6d9e`, con prefijo propio y sin editar ficheros existentes) e integrados con `cherry-pick`; llega a `dev`
por PR, sin squash:

- **T9.35** `e3fc0de` · **P10** `TestP10_MigrationsReplay` y `TestP10_Platform`: réplica de migraciones idempotente,
  grants, rekey (pasada sin nada que rotar y censo de los 8 sobres) y colector. De los 25 `Test*` viejos de BD de
  `platform`: 17 enteros, 5 parciales, 3 no llevados.
- **T9.17** `49d6d9e` + `39c38be` · el guion de inferencia (`guion_test.go`: reconoce la etapa por un marcador del prompt)
  y **P4** `TestP4_MessageToDraft` y `TestP4_WindowRules`, más los helpers `draftScenario`/`createDraft` de P5–P8.
- **T9.21** `9da68f7` · **P8** `TestP8_Reanalysis`.
- **T9.20** `0a2762d` · **P7** `TestP7_Catalog` (56 casos adversarios entre JSON y planilla).
- **T9.18** `56b99f6` · **P5** `TestP5_OwnerInbox` y `TestP5_AprobarDosVecesUnSoloEfecto` (INV-1; su mutante cae contra
  el viejo).
- **T9.19** `2bb7989` · el CRM falso (`crmfalso_test.go`, `TestArnes_CRM`) y **P6** `TestP6_CRMBridge`.
- **`make test-procesos`** (rc leído del log, suite entera P0–P10, sobre `2bb7989`): viejo `RC=0 · PASS=537 · FAIL=0 ·
  SKIP=0` (125 s) y nuevo `RC=0 · PASS=537 · FAIL=0 · SKIP=0` (114 s). **Una pasada** con todo; antes, otra sin P5 ni P6
  (`PASS=492` por binario, `RC=0`). **Ningún rojo solo contra el nuevo.**
- **T9.22 (`nucleo`)**, sin commit de código: `TestContactContrato_Postgres` 20 PASS y 0 SKIP en cada pasada; la suite
  entera contra el nuevo es la de arriba; y el test de cableado está completo: el mutante del hallazgo 39 de F1 (una fase
  pasa el resolver viejo), en copia desechable, da `go vet` rc=0 y `go test ./internal/arranque/` **rc=1**
  (`TestBootWiring_OnlyBridgeAndFlowsImportOldContact`).
- **Gates** (toolchain fijada, rc sin pipe): `make ci-local` `GATE_RC=0` a la primera (89 líneas `ok`, lint 0 issues,
  `FICHEROS_EVALUADOS=18`, `POR_DEBAJO=1`) · `make vet-pendiente` rc=0 · `make test-pendiente` rc=0 (`PENDIENTES=0`,
  `ROJOS=0`) · código nuevo con `-v`: 639 PASS, **0 SKIP**.
- **Sin tocar**: `internal/**` y `cmd/**` (`git diff --stat dev -- internal cmd` vacío). Ningún fichero de
  `test/procesos` pasa de 500 líneas; ningún `//nolint`, `t.Skip` ni `time.Sleep` en los nuevos.
- 🔴 **Para Jhoan** (hechos; no se decide aquí): (1) **la rotación real de KEK no cabe en el arnés** (hallazgo 57): hay
  que ampliarlo o llevarla a una suite antes de que F10 borre `rekey_integration_test.go`; (2) sobre una solicitud
  **aprobada** el re-análisis **no** se rechaza y se entrega al CRM (hallazgo 44; el diseño decía «rechazo»); (3) ningún
  borrador del pipeline es descartable (45); (4) `catalog_import` no gatea el `PUT` genérico de contenido (55); (5) un
  `intake_id` no UUID en el callback del CRM da 500 (53).
- **No corrido**: `CUENTA=3 make test-procesos` (es de T9.30), la integración vieja (no se tocó código compartido) y los
  mutantes de P4, P6, P7 y P8 (no se pedían; P7 no rompió ninguna aserción adrede: hallazgo 61 d).
- **Hallazgos 44–61** en el [README de F9](plan/F9-procesos/README.md), «Hallazgos de la sesión F9-04».
- **Siguiente paso: F2-01**. `main` sin tocar.

**F2-01 · F2, inventario E-12 y hojas simples de `acceso` (2026-10-04, 🌐, rama `reorg/f2-01-inventario-hojas` desde `origin/dev` @ `9a77307`).**
Sesión completa. El código lo escribieron sub-agentes en *worktrees* (paquete por paquete) integrados con `cherry-pick`;
llega a `dev` por PR, **sin squash**:

- **T2.1** `1d73874` · inventario E-12 de los 51 ficheros viejos de `acceso` y sus ✚, medido fichero a fichero (líneas,
  exportados, estado, concurrencia, BD, consumidores), en `plan/F2-acceso/diseno.md` §1.1. **Jhoan lo aprobó** con cuatro
  respuestas: P1 `iam/infra/memory` simple (dobles) · P2 adaptadores Postgres sin Tx medio (mutantes solo en los de Tx/cerrojo
  y estado) · P3 el candado de invitaciones parsea `auth_roleplane.go` · P4 `iam/domain` simple. Adaptadores: nace **1**
  (`bridge_iam.go`, F2-04), se retira 0. Re-medición: `ports/in` 447 líneas y total 10.444.
- **T2.34** `f46a107` · `internal/arranque/auth.go` → `auth_{stack,jwt,roleplane,invitaciones,empresa_activa}.go` y
  `edge_config.go` (268/178/106/57/57/233 l); multiconjunto de las 31 declaraciones igual; huella idéntica.
- **Hojas**: `entitlements` + `entitlementshelpertest` `01950de` · `middleware.go` rojo `43704f1` → verde `3742090` ·
  `iam/domain` `30345e7`, `e64cc3a`, `9b4e407`, `2868e61` + refactor E-11 `2776d82` · `ports/out` + 7 suites (63 casos)
  `8290814` · `iam/infra/memory` (7 + `redeem_store.go` ✚) `13d4171` … `38a8d0b` · `ports/in` `354f060`.
- **Candado** (decisión de Jhoan en la sesión): D-F2-5 chocaba con `un_fichero_un_test`; excepción verificada por lista
  cerrada en `internal/candados/inbound_ports.go`, rojo `4e97ee3` → verde `1d3b10b`.
- **Gates** (toolchain fijada, rc sin pipe, sobre `354f060`): `make ci-local` `GATE_RC=0` (99 `ok`, lint 0 issues) · `go vet
  -tags pendiente ./...` rc=0 · `make test-pendiente` `PENDIENTES=0 · ROJOS=0` · código nuevo con `-race -v` 1.210 PASS,
  **0 SKIP** · `cobertura-ficheros` (informe) 32 evaluados, 1 por debajo (`nucleo/contact/repository_postgres.go`, previo).
  Mutantes a mano: 12/12 `iam/domain`, 5/5 middleware, 4/4 `Fake`, 8/8 dobles y canje en memoria, 4/4 candado.
- ⏱️ **D-R-6, minutos con E-12**: ≈ **81 min activos** (02:35–03:31 Z, con la espera de la aprobación del inventario dentro,
  y 12:10–12:35 Z tras un reinicio del contenedor; el hueco 03:31–12:10 no cuenta). Simple fue la mayoría: 26 ficheros de
  producción nuevos + suites, en 4–5 sub-agentes a la vez; el cuello fue la suite + dobles de memoria (≈ 24 min un solo agente).
- 🟡 **Para Jhoan**: hallazgos 13 (cero de `RedemptionVerdict` = «procede»), 14 (U+200B/U+FEFF no se recortan en el
  token) y 18 (`05` E-3 sin la excepción nueva) del [README de F2](plan/F2-acceso/README.md); hallazgos 9–18 en total.
- **Siguiente paso: F2-02** (`usecase` e `identity`). `main` sin tocar.

**F2-02 · F2, `iam/infra/identity` e `iam/usecase` (2026-10-04, 🌐, rama `reorg/f2-02-usecase-identity` desde `origin/dev` @ `136b7ff`).**
Sesión completa. Ola 1 en dos *worktrees* en paralelo (identity; simples + rojo de usecase) integrada con `cherry-pick`; ola 2
(verde de usecase) en serie en el checkout principal. Llega a `dev` por PR, **sin squash**:

- **identity** (T2.10, T2.22): rojo `5a686b8` → verde `39688bf` (client, medio) · `744dd2a` (m2m, complejo) · cabeceras
  `86912f7`. `WithReloj` → `WithClock` (E-11). Mutantes del M2M sobre la caché y el candado: 19 sembrados · 18 muertos · 0 vivos
  · 1 equivalente.
- **usecase** (T2.11, T2.23): simples en una pasada `803cd77`, `0214e6a`, `79ded5c` · rojos `08cced4`, `4f3d718`, `cbf703d`,
  `ff462e6`, `5ad7390`, `563c7f9`, `482682e` · verdes `d09ff88` (grants, sin rojo: 0 exportados), `e32e4b5`, `fe49dda`, `505aeac`,
  `68696d9`, `55a9a67`, `b743b50`, `93aeb9d` · test que faltaba `7295586` · comentario `f0d777e`. Invariantes cruzados con test
  propio: R-U18, R-U6, R-U12.
- **Gates** (toolchain fijada, rc sin pipe, sobre `f0d777e`): `make ci-local` `GATE_RC=0` (103 `ok`, lint 0 issues) ·
  `make vet-pendiente` rc=0 · `make test-pendiente` `PENDIENTES=0 · ROJOS=0` · código nuevo `-v` 1.852 PASS, **0 SKIP** ·
  `cobertura-ficheros` (informe) 45 evaluados, 1 por debajo (previo); los 13 nuevos entre 87,0 y 100 %.
- ⏱️ **D-R-6, minutos con E-12**: ≈ **60 min** de pared (13:30–14:30 Z), casi todo esperando a sub-agentes: ola 1 ≈ 25 min
  (el rojo de 7 medios + 3 simples fue el cuello), verde ≈ 15 min en tres tandas, cierre ≈ 10 min. 13 ficheros de producción.
- **Hallazgos 19–25** en el [README de F2](plan/F2-acceso/README.md) (fichero sin exportados sin rojo, orden forzado por los
  contratos, clon superficial y cabeceras, `test-pendiente` cuenta los *worktrees*).
- **No corrido**: nada de este bloque necesita BD ni Docker; sin traspaso.
- **Siguiente paso: F2-03** (`infra/postgres`, `entitlements/postgres.go`, `transport/http`, `platformadmin`). `main` sin tocar.

**Todo es local desde el 2026-10-04 (D-R-8, Jhoan; commit de documentación directo sobre `dev` a petición expresa).** Se
acabó la promoción web: las 15 fichas pendientes que eran 🌐 / 🌐❓ (F3-01…04, F45-01…02, F6-01…05, F7-01…04) pasan a 💻 y su
prompt lee `PROTOCOLO-CLI.md`; conservan «web» en el nombre del fichero. `PROTOCOLO-WEB.md` queda como referencia de cómo se
escribe el código; `06-entorno-web.md` y la skill `traspaso-web-local`, en desuso. No cambia la regla 6: rama partida de
`dev` y PR sin squash. De paso: fila **D-F3-6** añadida a `DECISIONES.md` (la citaban las fichas de F3 y solo estaba en el
README de F3). **Los PR #33 y #34 están integrados en `dev`** (merge `4f0ed06`).

**Revisión de las 🟡 abiertas de F2 (2026-10-04, 💻, rama `reorg/f2-decisiones-abiertas`, partida de `reorg/f2-05-cierre-local`; integrada por el PR #34).**
Tras el cierre, Jhoan decidió una a una las siete 🟡 de F2; un commit por decisión:

- `478b70a` **D-F2-10** (hallazgo 13): el valor cero de `RedemptionVerdict` es `RedemptionMissing`.
- `4cd6066` **D-F2-11** (14): el token de invitación recorta U+200B y U+FEFF de los bordes (se aparta del viejo).
- `70395a4` (18): `05` E-3 recoge la excepción de los puertos de entrada.
- `20dd6ca` **D-F2-12** (29 y 45): la revocación en medio del canje, por conducta contra Postgres; la `tx` del alta, por
  candado AST nuevo. **Mutantes del nivel complejo: 131 sembrados · 126 muertos · 5 equivalentes · 0 vivos.**
- `0b8d089` **D-F2-13** (34): el alta solo acepta una dirección pelada y recorta los invisibles de los bordes (se aparta del
  viejo, también en lo que llega a identity).
- `96903ab` (36): los ficheros de `OversizedFiles` se parten cuando su fase los toque.
- `d0f7ec9` (50): P5 espera las líneas de log antes de contarlas.
- **Gates sobre `d0f7ec9`** (toolchain fijada, rc sin pipe): `make ci-local` `GATE_RC=0` (113 `ok`, lint 0 issues) ·
  `PENDIENTES=0 · ROJOS=0` · código nuevo `-v` 2.721 PASS, 0 SKIP · suites `Contrato` + `TestIAMRedeem_` 150 PASS, 0 SKIP con
  cada binario · `make test-procesos`: nuevo `RC=0 · PASS=668 · SKIP=0`; viejo, **rojo en la primera pasada**
  (`PASS=665 · FAIL=3`, `TestP4_WindowRules/segunda_ventana`) y `RC=0 · PASS=668 · SKIP=0` al repetir. 🟡 Hallazgo 52.
- Quedan fuera, sin decidir: el `switch` sin `default` que consume el veredicto del canje; un invisible **dentro** del
  correo; `ErrSignupNotAvailable` (hasta F10).

**F8-05 · F8, `runtime` (2): el núcleo, T8.27–T8.28 (2026-10-10, 💻, rama `reorg/f8-05-runtime-nucleo` desde `origin/dev` @ `da82addf`) — HECHA: los 14 en verde, `pendiente` del runtime = 0.**

- **De dónde parte**: F8-04b integrada (PR #66, merge `d80e7f56`, y el documental `da82addf`), comprobado con
  `git log origin/dev`. D-F8-12 rellena en `plan/DECISIONES.md`. `make toolchain` rc=0, `TOOLCHAIN=OK` (`go1.26.5`, lint
  `v2.12.2`); Docker disponible.
- **Lo hecho**: `persist_sink` `46b6a1ec` · `source_composer` `0c1d8973` · `aggregator` `0aaa18cd` (partido en 5) · **el
  núcleo del `Runtime`, 11 ficheros en un commit**, `1e0f6009` (`runtime_engine`, `resume`, `start`, `exit_menu`,
  `event_lifecycle`, `events`, `incoming`, `event_effects`, `welcome`, `thread`, `send`: cuelgan todos de `*Runtime`, se
  llaman entre sí y comparten arnés; ningún corte menor compila y pasa `unused`) · candado de rachas `a435884b` · comentarios
  `54c25319` y `6c9bd1f1` · tests de mutación `a6fb0215` y `f5fd0374`. `runtime` queda en **40** ficheros de producción
  (E-13: `runtime_engine` → 3, `event_lifecycle` → 2, `events` → 6, `incoming` → 5, `aggregator` → 5, más
  `aggregator_bridge.go`) y 58 de test; el mayor, `runtime_engine.go`, 553 líneas (tolerancia).
- **Por commit**: `go test -race`, `go vet -tags pendiente` y el lint (sin etiqueta y con `--build-tags pendiente`) rc=0 en
  **cada uno de los nueve commits** de código, medido en un *worktree* temporal ya borrado (36 de 36).
- **Tests corregidos: 2** de los ≈ 260 que nunca se habían ejecutado contra lógica real (hallazgo 42): uno se colgaba por
  `testing/synctest` sobre un `sync.Mutex`; otro usaba un TTL mayor que la ventana de la racha. Ningún fallo real del porte.
  Ningún aviso de lint; ningún `//nolint` en `C/runtime`.
- **Candado de rachas**: `streak_invariante_test.go` sin etiqueta y verde; `expectedDeletes` re-medida, **6** (una por
  sentencia de producción con `<x>.store.Delete(...)`: `events_clock.go`, `events_switch.go`, `incoming.go`,
  `incoming_advance.go` ×3). Mutación local, deshecha sin commitear: quitar el `Close` de `incoming.go`, de
  `events_switch.go` o de `incoming_advance.go` → rojo en `streak_invariante_test.go:81`.
- **Mutantes** (declarados por los sub-agentes; producción intacta al acabar, cada tanda en su *worktree*): núcleo **187**
  válidos, 185 muertos, 2 equivalentes (candado por conversación, semáforo y plazo, limitador, rachas, guardas, TTL, fan-out,
  ciclo de vida, bienvenida, hilo); agregador, sink y compositor **175** válidos, 172 muertos, 2 equivalentes y **1 que solo
  muere por `-timeout`** (`Run`: `return` → `continue` en `ctx.Done()`), sin arreglar. 23 supervivientes cerrados con test
  nuevo. Detalle y matices, en el hallazgo 43.
- **Gates sobre `f5fd0374`** (toolchain fijada, rc sin pipe y leído del log): `make ci-local` `GATE_RC=0` (237 `ok`, lint
  `0 issues`) · `make vet-pendiente` rc=0 · `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0` (eran 50 y 40) ·
  `grep pendiente.Implementar` en `C/runtime` → 0 (en `C` entero, 4: el texto del candado de `cart`) · código nuevo con `-v`
  (`modulos`, `nucleo`, `arranque`, `apipublica`): rc=0, 4.297 PASS de primer nivel, **0 SKIP** · `runtime`: 440 PASS ·
  `go test -race -count=10` y `-count=20` del paquete, rc=0 (sub-agentes) · cobertura (informe): `FICHEROS_EVALUADOS=407 ·
  POR_DEBAJO=8`, de `C/runtime` solo `events_summary.go` (76,3 %). `go.mod` y `go.sum` sin tocar.
- **No corrido**: `make test-procesos` y `make ci-docker` (el bloque no los pide: no cambia SQL ni cableado; el runtime nuevo
  aún no lo usa ningún binario, eso es F8-06); las dos suites contra Postgres de F8-04b, sin cambios desde `6216d1df`.
- **✎ D-F8-13 (Jhoan, 2026-10-10, tras la sesión)**: el arreglo de D-F7-9 va en una sesión nueva, **F8-06b** (T8.39), entre
  F8-06 y F8-07: el sobre primero, y cierre y sobre en una sola sentencia. Ficha, bloque 6b de `tareas.md` y fila en la
  tabla de sesiones, creados.
- **🟡 Para Jhoan, sin bloquear** (hallazgo 44): D-F7-9 sigue **sin arreglar** (✎ ya con sesión: F8-06b); las
  rarezas 34a–34i, portadas tal cual; el test `TestObserve_ResolverErrorWinsOverItsAnswer`, que solo muerde con un resolver
  que viola su contrato.
- **Siguiente paso: F8-06** (la cara HTTP y conmutar, T8.13 y T8.29–T8.35), cuando el PR de esta sesión esté en `dev`.
  `main` sin tocar.

**F8-04b · F8, `runtime` (1b): el verde del soporte, T8.26 (2026-10-10, 💻, rama `reorg/f8-04b-runtime-soporte` desde `origin/dev` @ `cbebf10c`) — HECHA: 9 de los 12 de soporte en verde (D-F8-12).**

- **De dónde parte**: F8-04 integrada (PR #65, merge `cbebf10c`, sin squash, por orden expresa de Jhoan), comprobado con
  `git log origin/dev`. D-F8-11 rellena. `make toolchain` rc=0, `TOOLCHAIN=OK` (`go1.26.5`, lint `v2.12.2`); Docker disponible.
- **✎ D-F8-12 (Jhoan, 2026-10-10)**: el verde son **9**; `welcome`, `thread` y `send` cuelgan de `*Runtime`, sus tests mueren
  en `runtime.WithClock` con el núcleo en rojo y **pasan a F8-05** con su etiqueta. Anotado en `plan/DECISIONES.md`, en las
  fichas de F8-04b y F8-05, en T8.26/T8.27 y en la tabla de sesiones del README de F8 (`a8c1f640`).
- **Lo hecho** (un commit `verde(conversacion): <fichero>` por fichero): `keyedmutex` `83565b4e` · `streak` `fc471fed` ·
  `event_sink` `163c193b` · `log_sink` `a446d4b2` · `webhook_sink` `91ffb00d` · `summary_sources` `093ea8fc` ·
  `tenant_resolver` + `self_numbers` `6216d1df` (juntos: las dos suites contra Postgres pierden `pendiente` a la vez, con
  `postgres_fakedb_test.go` y los dos tests unitarios). `runtime.go` no lleva commit: ya estaba declarado y con su test sin
  etiqueta. `go test -race`, `go vet -tags pendiente` y el lint (sin etiqueta y con `--build-tags pendiente`, sobre `runtime`
  y `test/procesos`) rc=0 **en cada uno de los siete commits**, medido en un *worktree* temporal ya borrado (28 de 28).
- **Mutantes** (declarados por los sub-agentes; producción idéntica a su copia buena al acabar): `keyedmutex.go` 5 de 5
  (quitar `ref++`, moverlo tras `rl.mu.Lock()`, quitar el `delete`, `ref == 0` por `ref <= 1`, retener el global);
  `streak.go` 16 muertos y **1 equivalente** (`>` por `>=` en `Max`): `Before` por `!After` en `Inc`, en `Max` y en el
  desalojo; quitar el `delete` de `Max` y el de `Close`; reportar dentro del candado (×3); quitar el reporte del desalojo;
  `e.n = 1` por `e.n++`; quitar la normalización de `idleTTL` y la de `maxEntries`; desalojar la más reciente; no reportar la
  vencida; no refrescar `lastSeen`; quitar el filtro `n > 0`. `event_sink.go` 3 de 3. SQL de los adaptadores, 9 de 9 contra
  Postgres.
- **Suites contra Postgres (P4, testcontainers; nunca `WAPP_TEST_DB_DSN`)**, repetidas por el orquestador sobre `6216d1df`:
  `TestRuntimeTenantResolverContrato_Postgres` 13 casos y `TestRuntimeSelfNumbersContrato_Postgres` 18; `PG_RC=0`,
  33 `--- PASS`, 0 SKIP, 0 FAIL.
- **Gates sobre `6216d1df`**, rc leídos del log: `make ci-local` `GATE_RC=0`, 237 paquetes `ok`, lint `0 issues.`;
  `make test-pendiente` rc=0, `PENDIENTES=50 · ROJOS=40` (antes 60 y 50); cobertura (informe, no bloquea)
  `FICHEROS_EVALUADOS=376 · POR_DEBAJO=7`; SKIP en código nuevo (`go test -v` sobre `modulos`, `nucleo`, `arranque` y
  `apipublica`): **0** de 11.576 `--- PASS`, rc=0. Ningún `//nolint` nuevo, ningún `time.Sleep`, ningún `t.Skip`. 100
  repeticiones con `-race` de los tests de `keyedmutex`, `streak` y la ordenación: rc=0.
- **No corrido**: `make test-procesos` entero (solo las dos suites nuevas, con `-run`), `make ci-docker`, la integración vieja
  y el arranque real: ningún paquete de la sesión entra todavía en un binario.
- **Hallazgos 37–40** en el [README de F8](plan/F8-conversacion/README.md). 🟡 Para Jhoan, sin bloquear: siguen sin decidir
  34d (reloj del `WebhookSink`) y 34f (el resolver no filtra por `state`; ahora lo fija un caso que muere si se filtra).
- **Tiempo (D-R-6)**: ≈ 40 min de pared (11:46–≈12:25): ≈ 12 los tres sub-agentes en paralelo (≈ 6, ≈ 7 y ≈ 12), ≈ 6 la
  comprobación commit a commit, ≈ 5 los gates, el resto cierre. Tres sub-agentes, mismo checkout, sin *worktrees* suyos.
- **Siguiente paso: F8-05** (el núcleo y `welcome`, `thread`, `send`: T8.27–T8.28), con el PR #66 de esta rama ya en `dev` (merge `d80e7f56`).
  `main` sin tocar.

**F8-04 · F8, `runtime` (1a): los contratos de los 23, T8.18–T8.21 (2026-10-10, 💻, rama `reorg/f8-04-runtime-contratos` desde `origin/dev` @ `e0159171`) — HECHA: todo `runtime` en rojo.**

- **De dónde parte**: F8-03 integrada con sus dos mitades (PR #62, merge `8b841c8d`; PR #63, merge `755f2cdf`, comprobado con
  `git log origin/dev`) y el PR #64 (D-F8-11). D-F8-4, D-F8-5 y D-F8-11, rellenas.
- **Código** (4 commits, todos `rojo`): `cf327ca5` (soporte y dobles) · `ee6da0af` (núcleo, más `welcome`, `thread` y `send`) ·
  `33e9eca2` (candado de rachas) · `de6a5411` (D-F8-4 en `fronteras_test.go`). Nace `internal/modulos/conversacion/runtime`:
  **23 ficheros de producción** (los 23 del viejo, uno a uno; ninguno pasa de 488 líneas), **52 de test** (50 tras `pendiente`)
  y **391** `func Test`; `pendiente.Implementar` en producción: **60**, todos en `runtime`. `runtimehelpertest` nace **verde**:
  10 ficheros, dobles de `Sender`, `Presigner`, `TenantResolver`, `SelfNumberChecker`, `IngestDeduper`, `ReplyLimiter` y
  `DepositReminder`, el gemelo en memoria de `fleet_sessions` y las suites `ContratoTenantResolver` (13 casos) y
  `ContratoSelfNumbers` (18), 46 PASS y 0 SKIP contra el doble.
- **Contra Postgres**: las dos suites están **escritas** en `test/procesos/runtime_tenant_resolver_contrato_test.go` y
  `runtime_self_numbers_contrato_test.go`, tras `//go:build integracion && pendiente`; `go vet` con las dos etiquetas, rc=0.
  No se corren aquí (los adaptadores hacen panic): F8-04b les quita `pendiente`, a las dos a la vez (la segunda usa las
  siembras de la primera) y junto a `postgres_fakedb_test.go`. Declarado por el sub-agente, como oráculo temporal sin commitear:
  con la lógica vieja pegada en los adaptadores, 33 PASS y 0 SKIP con testcontainers, y 5 mutantes del SQL muertos. El candado
  `ProcessImports` no se tocó.
- **Candados**: `TestStreak_EveryDeleteClosesTheEpisode` escrito tras `pendiente` (rojo por la cuenta, 0 de 6; comprobado por el
  sub-agente fuera del repo contra una copia del runtime viejo: verde con 6, rojo al quitar un `Close`).
  `TestEventsDoesNotDependOnTheClassifier` (D-F8-4), sin etiqueta y verde sobre los 11 ficheros de producción de `events`;
  mutación local del orquestador (un import de `inferencia/llmvia` en `events`) → rojo; deshecha sin commitear. La lista blanca
  de `conversacion` estaba completa: `runtime` no pidió ninguna arista.
- **Desvíos del plan, dichos**: (a) `welcome`, `thread` y `send` van en el commit del núcleo y no en el de soporte; (b)
  `keyedmutex.go` y `streak.go` no tienen tests en el rojo (sin exportados: nacen en el verde, con mutantes); (c)
  `runtime_test.go` y `event_effects_test.go` nacen sin etiqueta (ficheros de solo datos); (d) los tests del `Runtime` van en
  `package runtime_test` por un ciclo de imports con `runtimehelpertest`; (e) `postgres_fakedb_test.go`, un driver de mentira
  no previsto, para los unitarios de los dos adaptadores. Hallazgos 30 y 31.
- **Divergencia deliberada del viejo, prometida con su caso**: D-F9-10 (el barrido del agregador no loguea a ERROR con el
  contexto cancelado). **D-F7-9 no se toca**: anotada en el contrato como deuda que decide F8-05.
- 🔴 **Lo que este rojo no garantiza** (hallazgo 32): ≈ 260 de los 391 tests pasan por el arnés y **no se han ejecutado nunca**
  contra lógica real; compilan y pasan el lint. Sí se validaron contra el viejo, sin commitear: los 6 de soporte con
  exportados, `persist_sink`, `source_composer` y `aggregator` (68 de 69 tests), las dos suites y el candado.
- **Gates del orquestador, sobre `de6a5411`**, en serie, rc leído sin pipe: `make toolchain` rc=0, `TOOLCHAIN=OK`;
  `GOWORK=off make ci-local` `GATE_RC=0` (237 `ok`, 216 de ellos `(cached)`, 0 `FAIL`, 0 SKIP, lint `0 issues.`; cobertura,
  informe: `FICHEROS_EVALUADOS=368`, `POR_DEBAJO=7`); `make vet-pendiente` rc=0; `make test-pendiente` rc=0,
  `PENDIENTES=60 · ROJOS=50`; `go test -v` de `modulos`, `nucleo`, `arranque` y `apipublica` rc=0, 11.399 PASS, 0 FAIL,
  **0 SKIP**. El lint con la etiqueta a mano (hallazgo 28), `--build-tags pendiente` sobre `conversacion` y `test/procesos`:
  rc=0, `0 issues.`, sin ningún `//nolint` nuevo.
- **No corrido**: `make test-procesos` (las dos suites nuevas llevan `pendiente` y no entran), `make ci-docker`, la
  integración vieja y el arranque real: ningún paquete de la sesión entra todavía en un binario.
- **Hallazgos 30–36** en el [README de F8](plan/F8-conversacion/README.md). 🟡 Para Jhoan, sin bloquear (34): el sobre de P2
  sin escape, el id visto antes de escribir, el `time.After` real del reintento, el reloj del `WebhookSink`, el panic diferido
  de `WithEventSink(nil)`, y si las claves `dispatcher_menu*` de `Vars` suben al contrato.
- **Tiempo (D-R-6)**: ≈ 80 min de pared (09:48–≈11:10; ✎ 2026-10-10: aquí decía ≈ 65, 09:48–≈10:55): ≈ 21 el soporte y los dobles, ≈ 24 los contratos del núcleo, ≈ 21 sus
  tests (solapados con lo anterior), ≈ 3 los gates, el resto verificación y cierre. Siete sub-agentes, sin *worktrees*.
- **Siguiente paso: F8-04b** (el verde de los 12 de soporte, T8.26), cuando el PR #65 esté en `dev` (✎ 2026-10-10: **integrado** por orden expresa de Jhoan, merge `cbebf10c`, sin squash). `main` sin tocar.

**F8-03 · F8, segunda mitad: el verde de `cart`, T8.25 (2026-10-10, 💻, relanzamiento, rama `reorg/f8-03b-cart-verde` desde `origin/dev` @ `8b841c8d`) — F8-03 CERRADA.**

- **De dónde parte**: el PR #62 (primera mitad: `events` verde, `cart` en rojo) se **integró en `dev` por orden expresa de
  Jhoan** en la conversación, merge `8b841c8d`, sin squash. La segunda mitad nace en rama nueva desde ese merge; su PR a `dev` es
  el #63, integrado por orden expresa de Jhoan (2026-10-10), sin squash.
- **Código** (5 commits; un sub-agente, en serie, en el checkout): `dd0771ce` (previo, solo tests: 17 comentarios
  `//nolint:<linter> // motivo` en 10 ficheros —10 `errcheck` de aserción de tipo, 3 `ST1018`, 2 `gocritic` `mapKey`, 2
  `gocyclo`—; ninguna aserción ni dato cambia; hallazgo 28) → verdes `4045db25` (13 ficheros en un commit, por `unused`: `state`,
  `effects`, `variants`, `buyer`, `screens`, `preresolutor`, `consulta`, `troceo`, `cart`, `cart_levels`, `cart_notes`,
  `cart_navigation` y `prime`; quita la etiqueta `pendiente` a sus tests, a `helpers_test.go`, a `golden_test.go` y a
  `orden_consulta_ast_test.go`), `dd52dc54` (`resume`), `5774823d` (`revalidate`), `44255a2b` (`projection`, partido en cuatro
  por E-13: `projection.go` 268, `projection_lines.go` 222, `projection_close.go` 138, `projection_buyer.go` 68). **`cart`: 20
  ficheros de producción** (17 + 3 de la partición de `projection`; contados con `ls`); ningún `//go:build pendiente` en `cart`;
  `pendiente.Implementar` en producción de `conversacion`: **0** (el `grep` da 4, las cuatro en el texto del propio candado:
  `orden_consulta_ast_test.go:109,181,201,220`). `troceo.go` queda en 568 líneas (tolerancia de E-13, sin partir).
- **Desvíos del plan de T8.25, dichos**: (a) el commit previo de lint; (b) `prime.go` entra en el primer commit verde y no en uno
  propio: `cart_levels_test.go`, `cart_notes_test.go` y `consulta_test.go` usan la constante `continueBebidas` de
  `prime_test.go`; el traslado de `TestWithLogger_PrimeWarnsAboutDiscardedFields` a `prime_test.go` va ahí, cuerpo intacto; (c)
  `projection.go` nace partido en cuatro.
- **Decisiones de la sesión**: `loadCatalog` nace no exportado en `cart/state.go`; `cloneVars` se sustituye por
  `modules.CloneVars` (mismo cuerpo); las notas usan `SanitizeNote`, `NoteTooLongError` y `MaxNoteRunes` de
  `modulos/solicitudes/intakes`; los no exportados en español pasan al inglés por E-11 (`preresolveOConsulta` →
  `preresolveOrQuery`, `troceado` → `chunked`, `opcionesDelNivel` → `levelOptions`, `codeVolver` → `codeBack`) con los valores
  observables intactos (`"ninguno"`, `"troceo"`, `"troceo_perdido"`, `"producto"`, `"cantidad"`); el local `max` de `moreCode`
  (`screens.go`) pasa a `highest` por no sombrear el builtin.
- **Goldens** `cart_v1` y `cart_v2`: verdes **sin** `-update`; `cmp` contra los del viejo rc=0 (comprobado por el orquestador).
- **Candado de orden de `Step`** (`TestOrder_QueryIsRaisedBeforeAnyMutation`): sin etiqueta y verde. Mutación local sobre el
  `Step` real (el bloque `preresolveOrQuery` + `return` movido bajo `st.Started = true`) → rojo; deshecho sin commitear
  (declarado por el sub-agente).
- **Mutantes de la proyección** (declarado por el sub-agente): 84 válidos, 84 muertos, 0 equivalentes.
- **Gates del orquestador, sobre `44255a2b`**, en serie y sin carga, rc leído sin pipe: `make toolchain` rc=0, `TOOLCHAIN=OK`;
  `GOWORK=off make ci-local` rc=0 (233 `ok`, 219 de ellos `(cached)`, 0 `FAIL`, 0 SKIP, lint `0 issues.`; cobertura, informe:
  `FICHEROS_EVALUADOS=364`, `POR_DEBAJO=7`); `make vet-pendiente` rc=0; `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`;
  `go test -v` de `modulos`, `nucleo`, `arranque` y `apipublica` rc=0, 11.348 PASS, 0 FAIL, **0 SKIP**. Del sub-agente, por
  commit: `go test -race -v` de `cart`, 470 PASS, 0 FAIL, 0 SKIP en la cabeza; lint del paquete `0 issues.`.
- **No corrido**: `make test-procesos`, `make ci-docker`, la integración vieja y el arranque real (ningún paquete de la sesión
  entra todavía en un binario; los cierra F8-07).
- **Hallazgos 27 y 28** en el [README de F8](plan/F8-conversacion/README.md). 🟡 Para Jhoan, sin bloquear: el `item_added`
  reentregado que reescribe una solicitud cerrada (25) está **portado tal cual**, sin decidir; y el lint del repo no mira los
  tests con `//go:build pendiente` (28: los 17 avisos se silenciaron con `//nolint` motivado en `dd0771ce` y, ✎ por decisión de Jhoan el mismo día —«limpio»
  es sin avisos silenciados—, se **arreglaron de verdad** en `d3f6c31b`; las demás supresiones del repo quedan como deuda
  D-31, para el final del plan). Siguen abiertos el caso de esquema del TTL 7200 (24), los 15 y 17 de F8 y los 15 y 22 de F7.
- **Tiempo (D-R-6)**: ≈ 20 min de pared (08:32–≈08:52): ≈ 15 el sub-agente del verde con mutantes, ≈ 2 los gates, el resto
  verificación y cierre. Con los ≈ 75 de la primera mitad, F8-03 son ≈ 95 min en dos bloques.
- **Siguiente paso: F8-04** (`runtime` (1): contratos de los 23 y soporte). `main` sin tocar. ✎ 2026-10-10 (D-F8-11): F8-04 se parte antes de lanzarla; queda en los contratos de los 23 (T8.18–T8.21) y le sigue F8-04b, el verde de los 12 de soporte (T8.26).

**F8-03 · F8, `events` y `cart` (2026-10-09, 💻, rama `reorg/f8-03-events-cart` desde `origin/dev` @ `9d5a4b6`) — A MEDIAS: `events` verde, `cart` en rojo.**

✎ **2026-10-10**: el PR #62 de este bloque se **integró** en `dev` (merge `8b841c8d`, sin squash, por orden expresa de Jhoan) y T8.25 se cerró en el relanzamiento: es el bloque de arriba. Lo que sigue es la foto del 2026-10-09.

- **Por qué a medias**: 21 ficheros con dos complejos no caben en 90 min. La sesión paró en un punto limpio a los ≈ 75 min:
  `events` verde y empujado, `cart` con sus contratos en rojo. **Falta T8.25** (verde de `cart`); se relanza la misma sesión.
- **Código** (10 commits; dos sub-agentes, `events` en el checkout y `cart` en un *worktree*, integrado por `cherry-pick`):
  `events` rojo `8c819b40` → verdes `f909402e` (`menu` + `dispatcher`, juntos por `unused`), `6590724e` (`summary` +
  `summary_render`), `d3c2f89e` (`store`), `006e4248` (`store_list`), `6284c04e` (`store_append`), `1667bca9` (`thread_reader`);
  `cart` rojo `69d99d54` (estado y bordes, con `cart.go` y la frontera), `6dc8e574` (módulo y goldens), `790ca522` (candado de
  orden). **`events`: 11 ficheros de producción** (`store.go` partido en 4, `summary.go` en 2), **114 exportados**, 0
  `pendiente.Implementar`. **`cart`: 17 ficheros de producción** (`cart.go` partido en 4), **55 exportados**, `validate.go` verde y
  18 `pendiente.Implementar` en 5 ficheros. 86 ficheros, 18.237 líneas nuevas; ningún `.go` pasa de 500.
- **`events.Store` (P4)**: doble en memoria `eventshelpertest` y suite `Contrato` de **51 casos** (4 de carrera), verde contra el
  doble y contra Postgres (`test/procesos/events_contrato_test.go`, `//go:build integracion`): 51 PASS, 0 SKIP con los dos
  valores de `WAPP_PROCESOS_BINARIO` (sub-agente) y otra vez con `nuevo` por el orquestador sobre `790ca522`. **Mutantes** del
  adaptador contra Postgres (declarado por el sub-agente): 56 válidos, 55 muertos, 1 equivalente (hallazgo 24).
- **Equivalencia viejo ↔ nuevo** (declarada por los sub-agentes, con oráculo temporal sin commitear): `events`, mismo
  multiconjunto de literales de producción por AST (230 distintos, 0 diferencias) y tests + suite verdes igual con la lógica
  vieja que con la portada; `cart`, los 185 tests rojos pasan contra el carrito viejo.
- **Frontera (D-F8-10, Jhoan en la conversación)**: `"catalogo"` entra en `Capas["conversacion"]` y nace
  `TestConversacionDoesNotImportCatalogIndex` en `fronteras_test.go`, sin ampliar el motor; mutado (un import de
  `catalogo/indice` en `cart` → rojo en `fronteras_test.go:213`) y deshecho.
- **Candado de orden de `Step`** (T8.17): escrito tras `pendiente` y re-anclado a la partición; comprobado con un cuerpo temporal
  (pasa con el orden bueno, cae al mover la petición bajo `st.Started = true`). **Falta** quitarle la etiqueta y mutarlo sobre el
  código real: es de T8.25. Los goldens están copiados byte a byte (`cmp` rc=0) y su test, en rojo.
- **Gates, sobre `790ca522`**, en serie y sin carga: `make toolchain` rc=0, `TOOLCHAIN=OK`; `GOWORK=off make ci-local`
  `GATE_RC=0` (233 `ok`, 0 `FAIL`, lint `0 issues.`; cobertura, informe: `FICHEROS_EVALUADOS=345`, `POR_DEBAJO=7`, los de antes);
  `make vet-pendiente` rc=0; `make test-pendiente` rc=0, `PENDIENTES=18 · ROJOS=24` (todo `cart`); `-v` del código nuevo
  (`modulos`, `nucleo`, `arranque`, `apipublica`) rc=0, 10.890 PASS, 0 FAIL, **0 SKIP**.
- **No corrido**: `make test-procesos` entero y `make ci-docker` (de `test/procesos` solo se corrió
  `TestEventsContrato_Postgres`; ningún paquete de la sesión entra todavía en un binario); la integración vieja; el arranque
  real. Sin intermitencias que anotar.
- **Hallazgos 21–26** en el [README de F8](plan/F8-conversacion/README.md). 🟡 Para Jhoan, sin bloquear: el `item_added`
  reentregado tras el cierre que reescribe una solicitud cerrada (25) y el caso de esquema del TTL 7200 (24). Siguen pendientes
  los 15 y 17 de F8 y los 15 y 22 de F7.
- **Tiempo (D-R-6)**: ≈ 75 min de pared (21:56–23:11, con `date`): ≈ 10 de verdad de campo, medida del hallazgo 3 y prompts;
  ≈ 35 el rojo de `events` y ≈ 26 su verde con mutantes (en serie, el mismo sub-agente); ≈ 41 el rojo de `cart`, en paralelo;
  ≈ 4 de integración y gates (`ci-local` ≈ 1 min con caché) y ≈ 5 de cierre. **Dos bloques, no uno.**

**F8-02 · F8, el motor (2026-10-09, 💻, rama `reorg/f8-02-motor` desde `origin/dev` @ `42117b5`).**

- **Código** (8 commits; tres sub-agentes en *worktrees*, uno por paquete o grupo, integrados por `cherry-pick` en la rama, los
  rojos antes que los verdes): `engine` `d8fd4ac` → `8f5f667` (medio; `engine.go` 12 exportados y `consulta.go` 10 = 22, como el
  viejo) · `menu` `b566ae0`, `survey` `fc085d5`, `media` `17ca94d` (simple, una pasada; 9 + 15 + 11 = 35) · `turnoacotado`
  `346cf73` → `08b1e64` (medio; `turnoacotado.go` 6, `troceado.go` 3, `prompt.go` 0 = 9) · `4ce435f` (aserción de compilación:
  `*turnoacotado.Resolver` satisface `engine.QueryResolver`). **9 ficheros de producción, 66 exportados**, 12 ficheros de test,
  5.293 líneas nuevas. `engine/engine.go` tiene 520 líneas (tolerancia de E-13); el resto, bajo 500. `pendiente.Implementar` en
  `conversacion`: **0**.
- **Desvíos del plan, dichos**: los verdes de `engine` y de `turnoacotado` van en **un commit por paquete**, no por fichero (se
  llaman entre sí; ningún orden parcial pasa `unused`); el `G` se corrió **una vez**, sobre `4ce435f`, no por commit.
- **Nombres (E-11)**: `engine/consulta.go` y `turnoacotado` pasan sus exportados al inglés (`QueryResolver`/`ResolveQuery`,
  `QueryObserver`, `QueryOutcome…`, `WithQueryResolver`, `Turner`, `ErrUnknownClass`, `ErrNoTurner`, `MaxCallsPerTurn`,
  `ChunkingBudget`, `FloorPerCall`); tabla en [`tareas.md`](plan/F8-conversacion/tareas.md), antes del bloque 3. Los valores
  observables no cambian. `turnoacotado` importa `modulos/inferencia/llmvia` y su puerto lo satisface `*llmvia.Selector` **sin
  adaptador**; la arista `conversacion → inferencia` ya estaba en `Capas` (`fronteras_test.go` no se tocó).
- **Equivalencia viejo ↔ nuevo** (tests temporales de cada sub-agente, no commiteados; cifras declaradas por ellos): `engine`
  21.000 casos, 0 divergencias (control: quitar `StripQueryVerdict` del nuevo hace saltar 3.285); `menu`, `survey` y `media`
  45.038 casos de `Step`/`Render` cada uno, más 800 del proyector y 82.019 de `EmitMedia`, 0 divergencias; `turnoacotado` 1.600
  prompts byte a byte, 6.272 de troceado y 2.432 de veredicto, 0 divergencias, y 14 mutaciones rápidas, 14 rojas. **Comprobado
  por el orquestador** sobre la rama integrada: el conjunto de literales de texto de producción es idéntico viejo ↔ nuevo en los
  cinco paquetes (14 · 1 · 6 · 9 · 61, por AST).
- **Gates, sobre `4ce435f`**, en serie y sin carga propia: `make toolchain` rc=0, `TOOLCHAIN=OK` (`go1.26.5`, lint `v2.12.2`);
  `GOWORK=off make ci-local` `GATE_RC=0` (227 `ok`, 0 `FAIL`, lint `0 issues.`; cobertura, informe: `FICHEROS_EVALUADOS=331`,
  `POR_DEBAJO=7`, **ninguno** de `conversacion`: los 7 de antes); `make vet-pendiente` rc=0; `make test-pendiente` rc=0,
  `PENDIENTES=0 · ROJOS=0`; `-v` del código nuevo (`modulos`, `nucleo`, `arranque`, `apipublica`) rc=0, 10.649 PASS, 0 FAIL,
  **0 SKIP** (237 PASS en los cinco paquetes de la sesión).
- **No corrido**: `make test-procesos` y `make ci-docker` (ningún paquete de la sesión entra todavía en un binario: `go list
  -deps ./cmd/server-modular` da 0 para los cinco; no se tocó código viejo ni compartido); la integración vieja; el arranque
  real. Por eso no hay intermitencias que anotar.
- **Hallazgos 15–20** en el [README de F8](plan/F8-conversacion/README.md). 🟡 Para Jhoan, sin bloquear: las tres rarezas de
  `turnoacotado` (17: cantidad troceada como elección, la vía API descarta lo ya resuelto, prompt sin escapar) y las del `engine`
  (15). Siguen sin analizar los 15 y 22 de F7.
- **Tiempo (D-R-6)**: ≈ 25 min de pared (21:17–≈ 21:42, medido con `date`): ≈ 2 de verdad de campo y prompts, ≈ 16 los tres
  sub-agentes en paralelo (≈ 7 `menu`/`survey`/`media`, ≈ 13 `turnoacotado`, ≈ 16 `engine`), ≈ 3 de integración y gates
  (`ci-local` ≈ 2 min con la caché caliente) y ≈ 4 de cierre. Cabe holgado en una sesión.

**F8-01 · F8, inventario E-12 y hojas (2026-10-09, 💻, rama `reorg/f8-01-inventario-y-hojas` desde `origin/dev` @ `c0c0c03`). F8 EN CURSO.**

- **Inventario E-12 (T8.2), aprobado por Jhoan** antes de escribir código: 75 ficheros · 23.901 líneas re-medidos (74 por hacer;
  `model` ya estaba verde, **T8.3 tachada**). Frente al provisional: `trigger.go`, `trigger/store.go` y `survey` bajan a simple;
  `trigger/store_memory.go` y `store/store.go` a medio; `cart/projection.go` sube a complejo; `runtime` deja de ser complejo en
  bloque (12 · 6 · 5). Adaptadores que F8 retira (crea 0): `bridge_contact`, `bridge_inferencia`, `bridge_captacion` y la 2.ª
  instancia vieja de `intakes.Postgres`; puentes de import: 3. Todo en el [README de F8](plan/F8-conversacion/README.md).
- **Código** (13 commits, por tres sub-agentes en *worktrees* e integrados por `cherry-pick` en la rama):
  `content` `15524e1` (simple, una pasada; 4 ficheros, 11 exportados) · `modules` `ed3b867` → `e104f19` (5 ficheros, 59) ·
  `trigger` `907839a` → `08c3aff` (5 ficheros, 54; `triggerhelpertest.Contrato`, 18 casos) · `store` `b579591` → `24524de`,
  `a8450e2`, `f8d0294`, `9a3f7b3`, `3b1d3f9`, `96865ce`, `3a408dc` (los 3 ficheros viejos nacen partidos en **12** por E-13, 107
  exportados; `storehelpertest.Contrato`, 61 casos en 15 ficheros, con tres carreras que el viejo no fijaba). Ningún `.go` nuevo
  pasa de 500 líneas. `pendiente.Implementar` en `conversacion`: **0**.
- **Suites de contrato en memoria y en Postgres** (P4, testcontainers): `test/procesos/flowstore_contrato_test.go` (61 casos) y
  `trigger_contrato_test.go` (18), sin divergencias memoria ↔ Postgres. **Mutantes** (nivel complejo, `store`): 37 sobre el SQL
  (`ReplaceTenantContentVersioned`, `CloseIntake`, `TouchContact`, `MarkWelcomed`, `Save`, `GetOpenIntake`, `UpsertIntake`),
  **37 muertos**, corridos por el sub-agente con el verde real. `trigger`: equivalencia viejo ↔ nuevo de la normalización, 67
  filas de corpus adversario y 20.000 rondas aleatorias, 0 divergencias.
- **Gates, sobre `3a408dc`** (último commit de código), en serie: `GOWORK=off make ci-local` `GATE_RC=0` (217 `ok`, 0 `FAIL`, lint `0 issues.`; cobertura, informe: `FICHEROS_EVALUADOS=322`, `POR_DEBAJO=7`, **ninguno** de `conversacion`: los 7 son de `acceso/iam/infra/postgres` (5), `acceso/platformadmin/postgres.go` y `nucleo/contact/repository_postgres.go`, los de antes); `make vet-pendiente` rc=0; `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`; `-v` del código nuevo (`modulos`, `nucleo`, `arranque`, `apipublica`) rc=0, 10.410 PASS, 0 FAIL, **0 SKIP**.
- **`make test-procesos`**: binario **nuevo**, a la primera, `RC=0 · PASS=1197 · FAIL=0 · SKIP=0`. Binario **viejo**: **tres
  pasadas rojas seguidas y la cuarta verde** (`RC=0 · PASS=1197 · FAIL=0 · SKIP=0`), las tres por intermitencias ya conocidas y
  ninguna en las suites nuevas, que pasaron 61/61 y 18/18 en las cinco corridas (hallazgo 14 de F8): 1.ª, `broken pipe` en
  `TestP6_CRMBridge/callback_body_adversarial`; 2.ª y 3.ª, la carrera de D-F7-9 («el job no trae literal que analizar»), vista
  **por primera vez contra el viejo**, en `TestP4_MessageToDraft` y en `TestP6_CRMBridge`. La máquina tenía carga alta ajena a los
  gates (load ≈ 11–13). No se arregla aquí.
- **No corrido**: `make ci-docker`; la integración vieja (no se tocó código compartido ni viejo); el arranque real (no toca: nada
  se conmuta en este bloque). El `G` de T8.1 no se corrió al arrancar sino al cierre.
- **Hallazgos 1–14** en el [README de F8](plan/F8-conversacion/README.md). 🟡 Para Jhoan, sin bloquear: las tres divergencias
  deliberadas del gemelo de `store` (6), cuatro conductas raras del viejo portadas tal cual (7) y el renombre al inglés de los
  exportados de `modules/consulta.go` (13). El hallazgo 60 de F7 queda matizado (1). Siguen sin analizar los 15 y 22 de F7.
- **Tiempo (D-R-6)**: ≈ 25 min de inventario y plan + ≈ 105 min de pared de ejecución (17:27–19:12; de ellos ≈ 64 el rojo de
  `store` y su suite, ≈ 20 su verde y mutantes, ≈ 20 los gates y las repeticiones del binario viejo). **Se pasó de los 90 min**:
  se siguió porque el verde de `store` ya estaba validado contra Postgres con un port provisional.
- **Ampliación en el mismo PR (#60), pedida por Jhoan («todas las recomendaciones»)**: decididas **D-F8-7** (el gemelo de `store`
  imita a Postgres), **D-F8-8** y **D-F8-9** (los exportados de `modules/consulta.go`, en inglés). Dos commits de código, uno
  por arreglo, cada uno con su caso y comprobado que el caso mata el código de antes: `03d82e9` (`ListResults` de Postgres lee
  `event_id`) y `2677cb8` (el gemelo rechaza una segunda línea `_shipping`); la suite de `store` pasa a **62** casos. El hallazgo
  60 de F7 queda corregido con ✎ en su README. **Gates repetidos sobre `2677cb8`**, en serie: `GOWORK=off make ci-local`
  `CI_LOCAL_RC=0` (217 `ok`, lint `0 issues.`, `FICHEROS_EVALUADOS=322`, `POR_DEBAJO=7`); `make test-pendiente` rc=0,
  `PENDIENTES=0 · ROJOS=0`; `-v` del código nuevo rc=0, 10.412 PASS, **0 SKIP**; **`make ci-docker` rc=0** (lint `0 issues.`, 0
  `FAIL`), que no se había corrido; `make test-procesos`: viejo a la primera `RC=0 · PASS=1198 · FAIL=0 · SKIP=0`, nuevo a la
  segunda con las mismas cifras (la primera, otra vez el `broken pipe` de `TestP6_CRMBridge/callback_body_adversarial`, que hoy
  falló 2 de 7 pasadas, una por binario); 0 apariciones de «el job no trae literal»; `TestFlowStoreContrato_Postgres` 62/62 en
  las tres corridas. ≈ 35 min más de pared (D-R-6).
- **Estado de git**: PR #60, **integrado** en `dev` por orden expresa de Jhoan, merge `8c5b27c`, sin squash. **Siguiente paso: F8-02** (el motor). `main` sin tocar.

**F7-05 · F7, cierre local (2026-10-09, 💻, rama `reorg/f7-05-cierre` desde `origin/dev` @ `53e8f51`). F7 CERRADA.**
Sesión de cierre (T7.27–T7.29, T9.28), sin traspaso (T7.26 tachada: no hay `CERRADO` que escribir), orquestada con
sub-agentes (tres en *worktrees*, ya borrados). `53e8f51` es el merge del PR #58 (F7-04). **Siete commits de código**: los
tres del cierre, solo tests; `ca222ed`, de la ampliación, que toca además el doble en memoria de `intake` (`memory.go`,
que ningún código que no sea test construye); y `b0056a4` y `a1a19ed`, rojo y verde de D-F7-12, el **único cambio de
producción** de la sesión (`internal/apipublica/intents.go`); y `3d5fa59`, solo test, que arregla una carrera del arnés en P8 (hallazgo 62 de F7); ni el código viejo, ni `go.mod`; `Conmutados` no cambia
(`{"acceso","edge","catalogo"}`).
⏱️ **D-R-6: ≈ 60 min de pared** el cierre (13:30–14:30 local) **+ ≈ 50 min** la ampliación pedida por Jhoan (≈ 14:45–15:35)
**+ ≈ 65 min** el 43 (c), sus gates y la investigación de las intermitencias (≈ 15:40–16:45); casi todo, espera de gates. D-F9-1 estaba rellena («sí», 2026-09-30).
- **T7.27 = T9.28 · las cinco suites contra Postgres** (nunca se habían corrido; `30b0cf4`,
  `test/procesos/{intake,casebank,intentcfg}_contrato_test.go`): `ContratoQueue` 17 casos, `ContratoMachine` 29,
  `ContratoReanalysis` 10, `casebank` 12 e `intentcfg` 15 → rc=0, **88 PASS, 0 SKIP** en cada binario (83 casos + 5
  padres); los mismos **83** casos en memoria y en Postgres, **ninguna divergencia** con los dobles. Una base clonada por
  caso; el lector de `intake_jobs` lee la fila entera (23 columnas). El caso de `casebank` sin respaldo en el viejo
  (hallazgo 9) pasa: no se quita. La documentación contaba 9 casos de `intentcfg`; contados como subtests son 15.
- **Procesos**: P4, P7 (con `index_cache`) y P8 contra `nuevo` ×3 (`-count=3`): RC=0, 435 PASS, 0 SKIP; contra `viejo` ×1:
  RC=0, 145 PASS, 0 SKIP. Es la primera vez que el worker, el `Pool`, el índice de F5 y el re-análisis nuevos corren de
  extremo a extremo: no se pudieron tumbar. `make test-procesos` sobre `30b0cf4`: viejo y nuevo
  `RC=0 · PASS=1106 · FAIL=0 · SKIP=0` (1018 + 88; 102 tests de nivel superior). Sobre `51d1bbb`, con la máquina cargada
  por los mutantes en paralelo (carga ≈ 40): nuevo `RC=0 · PASS=1111 · SKIP=0` y **viejo `RC=1 · PASS=1109 · FAIL=2`**
  (`TestP6_CRMBridge/callback_body_adversarial`, «write: broken pipe» tras 76 «rate-limit excedido»): intermitencia **ya
  anotada** (hallazgo 52 de F2, README de F9; el mismo fallo en el cierre de F3), en código que F7 no toca y con el binario
  viejo sin cambios. Sobre `fb33953`, pasada intermedia verde: viejo y nuevo `RC=0 · PASS=1115 · FAIL=0 · SKIP=0`. Sobre `13d2e59` (entonces el último commit de código era `ca222ed`), verde: viejo y nuevo `RC=0 · PASS=1116 · FAIL=0 · SKIP=0` (104 tests de nivel superior, sin carga en la máquina; 0 apariciones de «el job no trae literal»). Hasta ahí, la carrera `CloseWindow` → `PutSourceText` (D-F7-9,
  hallazgo 17) no había aparecido en ninguna pasada. **Tras el 43 (c), sobre `a1a19ed`, tres pasadas enteras** (hallazgo 62 de F7): la primera, viejo `RC=0 · PASS=1116 · SKIP=0` y **nuevo `RC=1 · PASS=1113 · FAIL=3`** (`TestP8_Reanalysis/aprobada` y `cierre`: esa carrera, **vista por primera vez**; no es del 43 (c), P8 no publica intents; sigue siendo de F8); la segunda del nuevo, **`RC=1 · PASS=1114 · FAIL=2`** por otra causa (`TestP8_Reanalysis/reanalisis`: una carrera del arnés del test, que arregla `3d5fa59`); la tercera, viejo y nuevo `RC=0 · PASS=1116 · FAIL=0 · SKIP=0`. **Final, sobre `3d5fa59`**: viejo y nuevo `RC=0 · PASS=1116 · FAIL=0 · SKIP=0` (104 tests de nivel superior; 0 apariciones de «el job no trae literal»).
- 🔴 **Refutación de F7-04: ningún proceso ejercitaba el adelanto por clasificación** (el hueco del hallazgo 42; «lo verá
  P4» era **falso**). Sonda con contadores sobre la suite entera contra `nuevo`: `classifiedSink` **0** invocaciones,
  `aheadBridge.Request` 67, `composerBridge.ComposeAtFlush` 9. Mutantes: la clausura de `newClassifiedSink` no-op y
  `aheadBridge.Request` no-op, **vivos** con la suite entera (ningún escenario publicaba catálogo de intenciones: el pool
  descartaba antes de pedir P1); `composerBridge.ComposeAtFlush` → `return nil`, muerto por P8. **Corregido con `51d1bbb`**:
  `TestP4_AheadClassification` (`test/procesos/p4_borrador_ahead_test.go`; subtests `no_adelanta`, `adelanta`, `alcance`,
  `cierre`) publica el catálogo, P1 responde `intake_request` a 0.95 y el job llega a `done|draft` por el adelanto, sin
  `flushDraftWindow` y con los plazos de la ventana a 3600/3600; inferencias exactas `p1 p2 p3 p3 p3 p4`. Verde ×3 contra
  viejo y nuevo (15 PASS cada uno); mata los dos mutantes vivos. Sin divergencia viejo ↔ nuevo.
- **Mutantes de las guardas SQL de `intake` contra Postgres** (hallazgo 11; oráculo: **solo** las tres suites, sin el test
  del texto), sobre `30b0cf4`: **78 sembrados, 70 muertos** (68 por aserción, 2 por error SQL 42P10: el predicado del
  `ON CONFLICT`), **8 vivos** (`postgres.go` 20/19/1, `machine_postgres.go` 41/34/7, `postgres_reanalysis.go` 17/17/0). Las
  guardas que en F7-01 solo mataba el texto de la sentencia mueren ahora por aserción. **`fb33953` mata seis de los ocho**
  (re-sembrados uno a uno): caso nuevo `Retry_PastMark_IsWrittenAsGivenAndClaimableAtOnce` (`ContratoMachine` 29 → 30),
  orden de siembra invertido en dos casos y `TestIntakeClaim_SkipsLockedRows_Postgres` (solo-Postgres, la fila que ganaría
  el reclamo retenida por una transacción); tras él, `-run TestIntake`: 114 PASS, 0 SKIP en cada binario. Quedaban dos:
  cruzar las claves del `ORDER BY` de `PutSourceText` y `<=` → `<` en `ClaimNext`. **`ca222ed` mata el primero** (ver la
  ampliación). **Balance final: 78 sembrados; 70 muertos por las suites tal como estaban, 6 más con `fb33953` y 1 más con
  `ca222ed`; queda 1 vivo, declarado y equivalente en la práctica**: `<=` → `<` en `ClaimNext` (exige fijar el `now()` de la
  sentencia).
- **Ampliación pedida por Jhoan en el mismo PR** (#59; resolver lo que estaba a mano de los pendientes). `ca222ed` mata el
  mutante «cruzar las claves del `ORDER BY` de `PutSourceText`» (`updated_at DESC, created_at DESC` →
  `created_at DESC, updated_at DESC`): `QueueMontaje` gana `Seed` (mismo tipo y semántica que `Table.Seed`; obligatorio en
  `validateQueueMontaje`) y `ContratoQueue` pasa de 17 a 18 casos con
  `PutSourceText_CrossedMarks_LatestUpdateWins_CreationBreaksTies` (`intakehelpertest/queue_put_contrato.go`); el montaje de
  Postgres reutiliza la siembra de las otras dos suites. Mutante re-sembrado contra Postgres (`nuevo`): RC=1, solo ese caso
  en rojo (`queue_put_contrato.go:119` y `:120`). `-run TestIntake`: **115 PASS, 0 SKIP** en cada binario. 🟡 Tocó
  `internal/modulos/captacion/intake/memory.go`: `MemoryStore` gana `Seed(Job) string` (gancho de test, como `FailOpenWith`)
  y cambia el desempate de `lastPendingLocked`. **Divergencia menor doble ↔ Postgres, encontrada y corregida**: a igualdad
  de `UpdatedAt` el doble se quedaba con la ventana creada **antes**; el SQL (viejo `internal/intake/postgres.go:164` y
  nuevo `postgres.go:166`, idénticos), con la creada después. El doble desempata ahora por `CreatedAt` más reciente (✎
  divergencia con el gemelo viejo, a propósito, anotada en el comentario). El caso afirma también ese empate; contra
  Postgres el empate no mata de forma determinista «quitar la segunda clave», y no se comprobó que el caso dé rojo contra el
  doble sin el ajuste. Las marcas cruzadas **no** se alcanzan por el puerto de la cola (`intake.JobStore`): una ventana solo
  nace con la anterior cerrada y nada de la cola mueve `updated_at` de una `pending` que no es la última (lo del índice
  único parcial sale de leer el `ON CONFLICT … WHERE status = 'aggregating'` y la 0072, no de una prueba directa). 🟡 **Sí
  se alcanzan por la máquina** (hallazgo 60 de F7; leído en el código, no reproducido; conducta heredada, no se tocó):
  `Release` y `Retry` devuelven a `pending` un job viejo con `updated_at = now()`; si tiene el sobre vacío,
  `PutSourceText` escribiría en él el literal de la ventana recién cerrada. Pariente de la carrera de D-F7-9: lo hereda F8.
  `13d2e59` (docs): `plan/README.md`, cuya cabecera de estado y línea de sesiones hechas seguían en el 2026-10-07.
- **43 (c), decidido por Jhoan y corregido en el mismo PR** (#59; D-F7-12, 2026-10-09; hallazgo 61 de F7). `b0056a4`
  (rojo) y `a1a19ed` (verde). El push best-effort de `PUT /api/v1/intents` (E2, `internal/apipublica/intents.go`) ya no va
  con `r.Context()`: `context.WithTimeout(context.WithoutCancel(r.Context()), intentsPushTimeout)`, constante propia de
  5 s, como `pushProfileBestEffort`. Un cliente que cuelga ya no deja la config persistida y al Edge con el catálogo
  viejo. No cambian el best-effort (misma respuesta, mismo `Warn` literal) ni el `Upsert` (contexto de la petición, sin
  plazo). ✎ Divergencia con el viejo, a propósito: `internal/publicapi` no se toca. Sale
  `TestMountIntents_PutPushUsesTheRequestContext`, entra `TestMountIntents_PutPushSurvivesTheRequestCancellation`
  (la petición se cancela justo tras persistir: contexto del push vivo, con la `Identity` y con plazo de 4–5 s). Cuatro
  mutantes sobre el verde, los cuatro en rojo y revertidos (`r.Context()`; sin plazo; plazo de 30 s;
  `context.Background()`). Sobre `a1a19ed`: `go test -count=1 -race ./internal/apipublica/... ./internal/arranque/...`
  rc=0 (5 `ok`, 0 SKIP con `-v`), `make lint` rc=0 (`0 issues.`), `make fmt-check` rc=0, `make vet-pendiente` rc=0,
  `make test-pendiente` rc=0 (`PENDIENTES=0 · ROJOS=0`). Ningún otro test, candado ni proceso afirmaba la conducta
  vieja; la huella no cambia.
- **E1–E2 contra el JSONB real e invariantes de cierre**: la suite de `intentcfg` pasa en Postgres y P9
  (`intentsFirstPublish`) pasa contra los dos binarios; el GET no devuelve los bytes del PUT (Postgres canonicaliza) y nada
  depende de la identidad de bytes; el push sí lleva los bytes del PUT. `reglas.md` §cierre (R7.7.c):
  `git diff 8d875ab..HEAD -- cmd/server cmd/casebank internal/bootstrap internal/publicapi` vacío;
  `go list -deps ./cmd/server-modular | grep -c modulos/captacion` = 8 y 0 sobre `./cmd/server`; `captacion` fuera de
  `Conmutados`. Del hallazgo 43 (conductas heredadas de E1–E2), el (c) se decidió y se corrigió (viñeta anterior); (a), (b), (d) y (e) se decidieron al cerrar (viñeta siguiente).
- **Resto del 43, decidido al cerrar** (D-F7-13, hallazgo 63 de F7; Jhoan, «procede»). Tras una investigación de solo lectura en todo el ecosistema: el 403 de E2 sin `llm_intent` es prosa y no `feature_not_enabled` —único endpoint con gate de plan que lo hace—, pero ningún cliente llama a `PUT /api/v1/intents` (el editor de consola no existe, DT-37 de la deuda raíz) y corregirlo ahora rompería P9, que comprueba la prosa contra los dos binarios (R9.8.b). **(e) y (a) se difieren, juntos**: E2 migra a `RequireFeature` (cuerpo `feature_not_enabled` y *fail-closed*) en F10 o con DT-37, lo que llegue antes; **(b) y (d) se quedan**. Sin código: deuda D-30 de [`deuda.md`](../deuda.md) y nota en [`contratos.md`](../contratos.md) §2.7. Del 43 no queda nada abierto.
- **Gates finales, sobre `3d5fa59`** (último commit de código; el último de producción es `a1a19ed`, el verde de D-F7-12; rc leído del log): `make test-procesos` viejo y nuevo `RC=0 · PASS=1116 · FAIL=0 · SKIP=0` (104 tests de nivel superior; 0 apariciones de «el job no trae literal»); `GOWORK=off make ci-local` `GATE_RC=0` (209 `ok`, lint `0 issues.`; cobertura, informe: `FICHEROS_EVALUADOS=300`, `POR_DEBAJO=7`); `make vet-pendiente` rc=0; `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`; `-v` de `./internal/modulos/... ./internal/nucleo/... ./internal/arranque/... ./internal/apipublica/...` rc=0, 9.865 PASS, **0 SKIP** (sube respecto a las 8.355 anteriores porque ahora la cuenta incluye `apipublica`, no por tests nuevos); `make ci-docker`, verde sobre `a1a19ed` (rc=0, lint `0 issues.`) y no repetido sobre `3d5fa59`, que solo cambia un test de `test/procesos`; la integración vieja (`make test-integration`), corrida sobre `13d2e59` (rc=0, 14.756 PASS, 0 SKIP) y no repetida después: el 43 (c) solo toca la cara nueva. **Antes, sobre `a1a19ed`, los gates se repitieron y hubo tres rojos, ninguno del 43 (c)** (hallazgo 62 de F7). Primera tanda: `make test-procesos` viejo `RC=0 · PASS=1116 · SKIP=0` y **nuevo `RC=1 · PASS=1113 · FAIL=3`** (la carrera de D-F7-9, vista por primera vez); **`make ci-docker` rc=2** (`TestRendimiento_P99PorItem` de `internal/intake/catalogo`, código viejo: p99 por ítem de 8,145 ms contra los 5 ms de D-044.44, dentro del contenedor y con la máquina cargada); y verdes `GOWORK=off make ci-local` `GATE_RC=0` (209 `ok`, lint `0 issues.`), `make vet-pendiente` rc=0 y `make test-pendiente` rc=0 (`PENDIENTES=0 · ROJOS=0`). Repetición: `make ci-docker` **rc=0** (lint `0 issues.`) y `BINARIO=nuevo make test-procesos` otra vez rojo, por otra causa, **`RC=1 · PASS=1114 · FAIL=2`** (la carrera del arnés de P8, arreglada en `3d5fa59`; 0 apariciones de la carrera del literal). P8 aislado (`-run TestP8_Reanalysis -count=5`): nuevo y viejo `RC=0`, 5 de 5. Tercera pasada completa de `make test-procesos`: viejo y nuevo `RC=0 · PASS=1116 · FAIL=0 · SKIP=0`. **Antes, sobre `13d2e59`** (entonces el último commit de código era `ca222ed`; máquina sin carga, rc leído del log): `GOWORK=off make ci-local` `GATE_RC=0` (209 `ok`, lint `0 issues.`; cobertura, informe: `FICHEROS_EVALUADOS=300`, `POR_DEBAJO=7`); `make vet-pendiente` rc=0; `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`; `-v` del código nuevo rc=0, 8.355 PASS, **0 SKIP**; `make test-procesos`, arriba. Corridas también (en el primer cierre quedaban como «No corrido») la integración vieja, `GOFLAGS=-v INTEGRATION_PG_PORT=55432 make test-integration` (Postgres 16 efímero del propio target, `WAPP_TEST_REQUIRE_DB=1`): rc=0, 14.756 `--- PASS`, 0 FAIL, **0 `--- SKIP`** contados con `-v`; y `make ci-docker`: rc=0, lint `0 issues.`. Pasada intermedia, verde, sobre `fb33953`: `ci-local` `GATE_RC=0` y 8.353 PASS, 0 SKIP en el `-v`. Antes, sobre `30b0cf4` y `51d1bbb`: `GOWORK=off make ci-local`
  **`GATE_RC=0`** (209 `ok`, lint `0 issues.`; cobertura, informe: `FICHEROS_EVALUADOS=300`, `POR_DEBAJO=7`);
  `make vet-pendiente` rc=0; `-v` del código nuevo rc=0, 8.352 PASS, **0 SKIP**. **No corrido**: el arranque real de
  `cmd/server-modular` fuera del arnés y UAT (F10).
- **Hallazgos 50–63** e informe de fase en el [README de F7](plan/F7-captacion/README.md). Para F8 (58): el mutante vivo
  declarado, la carrera de D-F7-9 (vista una vez en F7-05, con su firma: hallazgo 62), las marcas cruzadas por `Release`/`Retry` (60) y `captacion`, que entra en `Conmutados`
  en F8. Del hallazgo 43 no queda nada abierto (el (c), corregido: D-F7-12; el resto, decidido: D-F7-13). **Siguen sin analizar con Jhoan**, sin bloquear, las 🟡 de los hallazgos 15 (anonimizador de `casebank`: excepción viva al invariante, fuga de JID) y 22 (conductas raras del viejo en match, fechas y draft): se retoman cuando Jhoan quiera. El PR #59, **integrado** en `dev` por orden expresa de Jhoan en la conversación («mergear»), merge `c0c0c03`, sin squash (✎ F8-01: aquí decía «se integra»). **Siguiente paso: F8-01.** `main` sin tocar.

**F7-04 · F7, cara HTTP (H1, E1, E2) y conmutar (2026-10-09, 💻, rama `reorg/f7-04-cara-http-y-conmutar` desde `origin/dev` @ `4f79bbf`).**
Sesión completa (T7.21–T7.25; T7.26, el traspaso, tachada: sesión local), orquestada con tres sub-agentes **en paralelo**
sobre el mismo árbol, por paquete, sin *worktrees* y sin commitear: el rojo conjunto y los verdes por fichero los compuso
el orquestador (hallazgo 48). ⏱️ **D-R-6: ≈ 20 min de pared de ejecución** (12:53–13:12 local): `reanalyze` ≈ 5 min,
`intents` ≈ 6 y la conmutación ≈ 12, en paralelo, más gates; precedidos de una fase de plan con dos exploraciones de solo
lectura (≈ 6 min cada una, en paralelo) que no se cronometró entera. **Cupo de sobra en 90.**
El código viejo y `cmd/` no se tocan (diff contra `origin/dev` vacío); `dev` y `main`, sin tocar.
- **Cara nueva**: `internal/apipublica/reanalyze.go` (H1, 392 líneas; 17 tests en `reanalyze_test.go`,
  `reanalyze_errors_test.go` y `reanalyze_internal_test.go`) e `internal/apipublica/intents.go` (E1 y E2, 299 líneas; 21
  tests en `intents_test.go` e `intents_put_test.go`). Rojo: `a688ca4` (los dos contratos, un commit). Verde: `068a202`
  (`reanalyze`) y `9d7fb94` (`intents`). Códigos, cuerpos y textos, literales del viejo.
- **Conmutación** (`6bdbe70`, T7.23 + T7.24 en **un** commit, hallazgo 39): cola, etapas, worker (W=1), aforo (K=1),
  `Pool` de adelanto, servicio de re-análisis y store de intenciones son los de `internal/modulos/captacion`; la caché del
  match es `indice.NewCache`, de `modulos/catalogo` (41). Nace `internal/arranque/bridge_captacion.go` (185 líneas, 10
  tests en `bridge_captacion_test.go`; muere en F8), único importador de captación vieja; muere `llmConfigBridge` de
  `bridge_inferencia.go`. Nace `captacion_cableado_test.go` (6 tests; cableado completo, `05` §4.2). **`FaseActual = 7`**:
  54 rutas por la cara nueva; en la vieja `Reanalysis`, `Intents`, `ConfigPush` e `Intakes` quedan sin asignar y muere el
  centinela `oldFaceIntakesMountSentinel` (D-F6-13; 47). G7 lee el plazo de `pipeline.CallTimeoutFloor` del `pipeline`
  nuevo, con su aserción de igualdad (T-13).
- **`Conmutados` = `{"acceso","edge","catalogo"}`**: entra `catalogo` (D-R-4 ✎ 2026-10-07: entra cuando F7 conmuta el
  índice); `captacion` no entra hasta F8, cuando muera su adaptador.
- **Candado INV-1** (`94f0648`, T7.25): la lista de flujos automáticos deja los tres paquetes viejos de `internal/intake`
  y mira `modulos/captacion/{intake,pipeline,stages}`, más `reanalisis` e `intakeahead`; los dos del motor siguen siendo
  los viejos hasta F8; la guarda anti-hueco no cambia.
- **Candados de cableado**: editados los tres de F0 que leían nombres viejos (hallazgo 28) y otros cinco que nombraban
  campos o constructores viejos (40); ninguno se debilitó, y `reanalisis_cableado_test` quedó más fuerte.
- **Divergencias viejo ↔ nuevo: manda el viejo.** Las conductas heredadas de E1–E2 se portaron literales y quedaron
  fijadas con test (🟡 hallazgo 43, para Jhoan, sin bloquear); H1 no promete «todo 400 antes de todo 403» (44).
  Ninguna decisión nueva. **Mutantes**: no corridos (nivel medio y simple: no obligatorios).
- **Gates sobre `94f0648`** (rc leído sin pipe): `make toolchain` rc=0, `TOOLCHAIN=OK` (go1.26.5, lint v2.12.2);
  `make ci-local` **`GATE_RC=0`** (209 paquetes `ok`, lint `0 issues.`; cobertura, informe: `FICHEROS_EVALUADOS=300`,
  `POR_DEBAJO=7`, los siete de `acceso` y `nucleo`, anteriores; nuevos: `apipublica/reanalyze.go` 100,0 %,
  `apipublica/intents.go` 87,3 %, `arranque/bridge_captacion.go` 88,8 %); `make test-pendiente` rc=0,
  `PENDIENTES=0 · ROJOS=0`; `-v` de `./internal/modulos/... ./internal/nucleo/... ./internal/arranque/...
  ./internal/apipublica/...` rc=0, **0 SKIP**; `internal/arranque` 155 PASS, 0 SKIP; huella y mapa sin tocar
  (`TestHuella`, `TestHuellaEstatica` y los 7 `TestMudanzas_*` verdes); `go list -deps ./cmd/server-modular | grep -c
  modulos/captacion` = 8, y sobre `./cmd/server` = 0; C2 (`llmvia`), fronteras, `un_fichero_un_test`, `file_size` e INV-1
  verdes; diff contra `origin/dev` de `internal/{intake,intakeahead,reanalisis,intentcfg,publicapi,bootstrap,flujos}`,
  `cmd` e `internal/arranque/testdata`, vacío. **No corrido** (es de F7-05): las suites de contrato contra Postgres;
  `make test-procesos` (P4, P8, T9.28) contra los dos binarios; la integración vieja; el arranque real de
  `cmd/server-modular`.
- **Hallazgos 39–49** en el [README de F7](plan/F7-captacion/README.md). 🔴 **Para F7-05** (49): nada de esta sesión
  corrió contra Postgres ni contra un binario; lo primero que hay que intentar refutar es P4 y P8 contra `nuevo`, E1–E2
  contra el JSONB real y la clausura del sink con el agregador vivo. El binario modular sigue enlazando captación vieja
  por `internal/publicapi` e `internal/flujos` (46). PR de la rama hacia `dev`, abierto al cierre, **integrar sin
  squash**. **Siguiente paso: F7-05** (cierre local). `main` sin tocar.

**F7-03 · F7, `pipeline`, `intakeahead`, `reanalisis` (2026-10-08/09, 💻, rama `reorg/f7-03-pipeline-reanalisis` desde `origin/dev` @ `56097aa`).**
Sesión completa (T7.10–T7.13, T7.18–T7.20), orquestada con tres sub-agentes **en paralelo** sobre el mismo árbol, uno por
paquete (no se importan entre sí), sin *worktrees*: cada uno hizo su rojo y, al volver, su verde con mutantes; más dos
pasadas de gates. ⏱️ **D-R-6: ≈ 106 min de pared** (23:22–01:00 local): rojo ≈ 13 min (`reanalisis`), ≈ 21 (`intakeahead`) y
≈ 35 (`pipeline`); verde ≈ 2, ≈ 4 y ≈ 53 (`pipeline`, con 216 mutantes); gates y cierre ≈ 20. **No cupo en 90**: lo que
manda es `pipeline` (rojo + verde + mutantes ≈ 88 min él solo); los otros dos paquetes cupieron dentro de esa espera.
El código viejo, `cmd/` y el arranque no se tocan; `Conmutados` no cambia; `FaseActual` sigue en 6.
- **`internal/modulos/captacion/{pipeline,intakeahead,reanalisis}` en verde**: 14 ficheros de producción y el doble
  `pipelinehelpertest/catalog_memory.go` (`CatalogoEnMemoria`, D-F7-5), ninguno por encima de 500 líneas (máx.
  `intakeahead.go`, 450; test mayor, 497). `pipeline.go` (1.148) partido en cuatro y `reanalisis.go` (772) en cuatro por
  D-F7-6; `intakeahead.go` en dos por E-13. Rojo: `403f73d` (el doble, en verde) y `1ff2012` (`pipeline`), `aacc30e`
  (`intakeahead`), `7cb66e2` (`reanalisis` y el puente). Verde: `8d63643`, `3b6dfd4`, `b6f55e3` (`pipeline`), `9d43c45`
  (`intakeahead`), `e813c06`, `5d3e1b1` (`reanalisis`). Tests que matan mutantes: `19a0c73`, `8e23511`. Candado: `69d142e`.
- **Puentes (import)**: declarado `captacion/reanalisis → internal/flujos/events` (`ThreadEntry`, `KindMessage`), nace F7,
  muere F8; prueba negativa: sin la entrada, `TestFronteras` da rojo. El de `flujos/runtime` se **evitó**:
  `reanalisis.NewService` recibe `threadLimit` como octavo parámetro y rechaza un valor ≤ 0. Comprobado con `go list`:
  ningún otro import a paquete viejo en los tres paquetes, tests incluidos.
- **Decisiones aplicadas**: **D-F9-10** — con el contexto cancelado, `Run` vuelve sin una línea a `ERROR` (los dos
  reclamos: el del drenaje y el del flanco), con su caso; con el contexto vivo el mismo fallo sigue yendo a `ERROR`.
  **D-F7-9** — el sobre incompleto deja el job `failed` sin reintento con el texto del viejo; lo dice el contrato de
  `RunOnce` y lo fija un caso; el arreglo es de F8. **D-F7-5**, **D-F7-6**. Ninguna decisión nueva.
- **Equivalencia viejo ↔ nuevo**: textos de error y de log copiados literales; cada rojo se validó contra un port fiel
  de la lógica vieja antes de commitearse (`pipeline` con `go test -overlay`, sin meter la lógica en el árbol). Los
  guiones viejos de `pipeline` (ámbar, hamburguesas, etapas reales) no se portan a test de fichero: van a P4 y P8 de F9
  (T-10). Los `go/ast` de `reanalisis` y las lecturas de fuente de `intakeahead` no eran candados: sustituidos por
  tests de conducta (hallazgo 32).
- **Mutantes** (a mano, sobre lo commiteado): `pipeline` 216 — 201 muertos, 0 vivos, 3 equivalentes, 12 que no compilan;
  `intakeahead` 79 — 76 muertos, 0 vivos, 3 que no compilan; `reanalisis` (medio) 19 de 19 (hallazgo 36).
- **Gates sobre `69d142e`** (rc leído sin pipe): `TOOLCHAIN=OK`; `make ci-local` **rc=0** (209 `ok`, lint `0 issues.`;
  cobertura, informe: `FICHEROS_EVALUADOS=297`, `POR_DEBAJO=7`, ninguno de `captacion`; `pipeline` 99,7 %, `intakeahead` 98,2 %, `reanalisis` 100 %; mínimo `backoff.go` 92,8 %); `make vet-pendiente` rc=0;
  `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`; `-race -v` de `captacion` rc=0, 573 tests de primer nivel (1.940
  PASS con subtests), **0 SKIP**; candados y arranque rc=0, 422 PASS, 0 SKIP. 🔴 La **primera** pasada de `ci-local`
  (sobre `8e23511`) dio **rc=2**: el candado C2 de `inferencia/llmvia` no tenía la entrada de `reanalisis_checks.go`;
  añadida en `69d142e` (hallazgo 38). **No corrido**: `make ci-docker`; `make test-procesos` y las suites contra Postgres
  (F7-05); la integración vieja (no se tocó código compartido).
- **Hallazgos 26–38** en el [README de F7](plan/F7-captacion/README.md). 🔴 **Para F7-04** (28): los candados de cableado
  de F0 leen texto con nombres que E-11 renombró (`ConAforo`, `ConZonasDeEnvio`, `Despertar`, `WithCalentamiento`,
  `NewServicio`, `ConEmpujeCRM`); ya no pueden quedar «verdes sin tocarlos». Las dos 🟡 que quedaban se decidieron el mismo día, un
  commit por decisión en la misma rama: **D-F7-10** (una parada dentro de una etapa devuelve el job sin castigo, 29.ii) y
  **D-F7-11** (`Warm` comprueba `log`, 34). PR **#57**, **integrado sin
  squash**. **Siguiente paso: F7-04** (cara HTTP y conmutar). `main` sin tocar.

**F7-02 · F7, `stages` (2026-10-08, 💻, rama `reorg/f7-02-stages` desde `origin/dev` @ `4cd9cfb`).**
Sesión completa (T7.7–T7.9, T7.16–T7.17), orquestada con seis sub-agentes **en serie** sobre el mismo árbol (un solo
paquete: comparten compilación), sin *worktrees*: tres de rojo (etapas LLM, match, draft) y tres de verde, más uno de solo
lectura para la contradicción 8. ⏱️ **D-R-6: ≈ 100 min de pared** (21:24–23:05 local), casi todo esperando: rojo ≈ 24 + 23
+ 16 min, verde ≈ 7 + 11 + 6, gates y cierre ≈ 13. **No cupo en 90**: el rojo de un paquete de 86 exportados son ≈ 65 min.
El código viejo, `cmd/` y el arranque no se tocan; `Conmutados` no cambia; `FaseActual` sigue en 6.
- **`internal/modulos/captacion/stages` en verde**: 14 ficheros de producción (los 10 de la spec; `draft` en cuatro por
  D-F7-6 y `match_cascade` en dos por E-13), ninguno por encima de 500 líneas (máx. `match.go`, 484). Rojo: `1a5219e`
  (P2, P3, P4, tope, fechas), `3d31361` (match), `3d380eb` (draft y el puente). Verde: `7711a55` (`deadline.go`, simple),
  `bd25bf5`, `864ca23`, `0177f3d`, `b41a6c7`, `c880cd5`, `705bda7`, `efe219d`.
- **Puente (import) declarado** en `internal/modulos/fronteras_test.go`: `captacion/stages → internal/flujos/store`
  (`store.Intake`, `store.FlowEvent` en `IntakeStore` y `EventWriter`), nace F7, muere F8. Prueba negativa: sin la entrada,
  `TestFronteras` da rojo con 5 violaciones. Ningún otro import a paquete viejo.
- **Equivalencia viejo ↔ nuevo**: los 36 textos de `errors.New`/`fmt.Errorf` idénticos (comparados por script); los tests
  de cada rojo se validaron contra un port fiel de la lógica vieja fuera del árbol antes de commitearse, y en el verde
  pasaron a la primera (una sola aserción añadida, en `draft_test.go`). Corpus adversarios de fechas (101 filas) y del
  match generados ejecutando el paquete viejo.
- **Gates sobre `efe219d`** (rc leído sin pipe): `TOOLCHAIN=OK`; `make ci-local` **rc=0** (201 `ok`, lint `0 issues.`;
  cobertura, informe: `FICHEROS_EVALUADOS=282`, `POR_DEBAJO=7`, ninguno de `captacion`; `stages` 98,3 %, mínimo
  `dates.go` 94,2 %); `make vet-pendiente` rc=0; `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`; `-race -v` de
  `captacion` rc=0, 373 tests de primer nivel (190 de `stages`; 1.515 PASS con subtests), **0 SKIP**; candados y arranque
  rc=0, 955 PASS, 0 SKIP. Mutantes a mano sobre el verde: etapas LLM 23 (1 vivo, equivalente), match 32 (0), draft 29 (0).
  **No corrido**: `make ci-docker`; `make test-procesos` y las suites contra Postgres (F7-05); la integración vieja (no se
  tocó código compartido); la reproducción bajo carga de la contradicción 8 (solo se leyó el código).
- **Hallazgos 17–25** en el [README de F7](plan/F7-captacion/README.md). ✅ **D-F7-9 (Jhoan, 2026-10-08)**: la carrera
  entre `CloseWindow` y `PutSourceText` (17) se porta tal cual en F7-03 y se arregla en F8, con el agregador. 🟡 sin
  bloquear: las conductas raras del viejo conservadas (22). PR **#56**. **Siguiente paso: F7-03** (`pipeline`, `intakeahead`, `reanalisis`). `main` sin tocar.

**F7-01 · F7, inventario E-12 y hojas (2026-10-08, 💻, rama `reorg/f7-01-inventario-y-hojas` desde `dev` @ `8d875ab`). F7 ARRANCADA.**
Sesión completa (T7.1–T7.6, T7.14–T7.15), orquestada con cuatro sub-agentes en paralelo, uno por paquete, sobre el mismo
árbol (sin *worktrees*). ≈ 50 min de pared (D-R-6), de los que ≈ 34 fueron `intake`. El código viejo, `cmd/` y el arranque
no se tocan; `Conmutados` no cambia; `FaseActual` sigue en 6.
- **T7.1 · inventario E-12 aprobado por Jhoan** (`6c461c6`, [`diseno.md`](plan/F7-captacion/diseno.md) §1.2): niveles de
  los 32 ficheros, la cara y el adaptador; 1 adaptador nace (`bridge_captacion.go`, F7-04), 1 muere (`llmConfigBridge`) y
  2 puentes de import. Decisiones: **D-F7-3** (`pipeline/memoria.go` es un doble: 31 de producción), **D-F7-5** (el doble
  de `PipelineStore` va a `intakehelpertest`: `MemoryStore` solo implementa `JobStore`), **D-F7-6** (manda E-13: `draft`,
  `pipeline` y `reanalisis` se parten) y **D-F7-7** (`intentcfg` y `casebank/postgres.go` en medio, `semilla.go` en simple).
- **Cinco paquetes en verde** en `internal/modulos/captacion/`: `evidence` (1 fichero), `anclaje` (1), `intake` (7:
  `reanalisis.go` se partió en tipos y SQL), `intentcfg` (2) y `casebank` (4), más los dobles `MachineMemory`,
  `reanalysis_memory` y `casebankhelpertest.Memory`. **Cinco suites con `Montaje`** corriendo en memoria: `ContratoQueue`
  14 casos, `ContratoMachine` 29, `ContratoReanalysis` 10 (nueva), `intentcfg` 9 y `casebank` 12; cada método con BD tiene
  caso y la marca de estado es la fila entera. Textos de error (49) y SQL idénticos al viejo, comparados por script.
- **Gates sobre `9bbc2dd`** (rc leído sin pipe): `TOOLCHAIN=OK`; `make ci-local` **rc=0** (199 `ok`, lint `0 issues.`;
  cobertura, informe: `FICHEROS_EVALUADOS=267`, `POR_DEBAJO=7`, ninguno de `captacion`, cuyo mínimo es 97,0 %);
  `make vet-pendiente` rc=0; `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`; `-race -v` de `captacion` rc=0, 172
  tests de primer nivel (679 PASS con subtests), **0 SKIP**; candados y arranque rc=0, 955 PASS, 0 SKIP. Mutantes a mano:
  `intake` 115 (1 vivo, equivalente), `anclaje` 22 (0 vivos), `casebank` 6 (0). **No corrido**: las cinco suites contra
  Postgres y `make test-procesos` (F7-05); la integración vieja (no se tocó código compartido).
- **Después del PR, en la misma sesión (D-F7-8, Jhoan)**: se cerraron en el código nuevo los tres agujeros de PII del
  anonimizador de `casebank` (`1d36dfe` JID pegados, `f08e959` dos teléfonos seguidos, `d88d200` dígitos no ASCII,
  `cb41b42` tests; nace `anonymize_phones.go`) y la clave de ventana incompleta, que el gemelo de `intake` rechaza ahora
  como Postgres (`1db9266`; `ContratoQueue` pasa a 17 casos). ≈ 25 min más. **Gates repetidos sobre `cb41b42`**:
  `make ci-local` rc=0 (199 `ok`, lint `0 issues.`, `FICHEROS_EVALUADOS=268`, `POR_DEBAJO=7`, ninguno de `captacion`);
  `vet-pendiente` rc=0; `test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`; `-race -v` de `captacion` rc=0, 183 tests de
  primer nivel (863 PASS con subtests), **0 SKIP**; candados y arranque rc=0, 955 PASS, 0 SKIP. Mutantes de los arreglos:
  `casebank` 24 (1 vivo, equivalente), `intake` 6 (0). Sin efecto en UAT hasta F10 (`cmd/casebank` usa el viejo).
- **Hallazgos 1–16** en el [README de F7](plan/F7-captacion/README.md). 🟡 para Jhoan, sin bloquear: una excepción viva
  al invariante del anonimizador (`José.maria@lid`) y los falsos positivos nuevos (15). **Siguiente paso: F7-02**
  (`stages`). `main` sin tocar.

**F6-06 · F6, cierre local (2026-10-08, 💻, rama `reorg/f6-06-cierre-local` desde `dev` @ `68e68a4`). F6 CERRADA.**
Sesión de cierre (T6.27–T6.29, T9.27), sin traspaso; los mutantes, por dos sub-agentes en *worktrees*. **Ningún commit de
código**: ni producción, ni tests, ni el código viejo, ni `go.mod`; `Conmutados` no cambia (`{"acceso","edge"}`). ≈ 35 min
de pared (D-R-6). ✎ Arrancó con el prompt de F2-05 pegado por error: F2-05 ya estaba integrada (PR #33) y Jhoan eligió F6-06.
- **T6.27 = T9.27 · las cuatro suites contra Postgres** (nunca se habían corrido): `intakes` 50 casos, `integrations` 61,
  `tenantvars` 12 y telemetría de eventos 13 → rc=0, **140 PASS, 0 SKIP** en cada binario; los mismos **136** casos en
  memoria (`-race`) y en Postgres, **ninguna divergencia**.
- **Procesos**: `make test-procesos` viejo y nuevo `RC=0 · PASS=1018 · FAIL=0 · SKIP=0` (leído del log; los mismos 97
  tests de nivel superior). P5 y P6 verdes en los dos; D-F9-10: `sin_errores` no se remidió con `CUENTA=3`.
- **Mutantes contra Postgres**: 74 sembrados (`intakes` 32, `integrations` 20, `tenantvars` 8, telemetría 14). Vivos contra
  todo, 2: los equivalentes ya declarados, que lo siguen siendo. 🟡 **19 solo los mata el test del texto del SQL** (la
  suite contra Postgres no los ve): los seis bloqueos (`FOR UPDATE` ×5 y el `SKIP LOCKED` del *claim* del outbox, donde el
  caso concurrente pasa igual sin él), los métodos que la suite no ejerce (datos del comprador, reflejo CRM, nota,
  destino del re-análisis, `CountOutbox`), la poda por TTL y el `ORDER BY` de telemetría. No se arregla sin decisión.
- **Gates sobre `68e68a4`** (rc leído del log): `TOOLCHAIN=OK`; `GOWORK=off make ci-local` **`GATE_RC=0`** (185 `ok`, lint
  `0 issues.`; cobertura, informe: `FICHEROS_EVALUADOS=249`, `POR_DEBAJO=7`, ninguno de F6); `make vet-pendiente` rc=0;
  `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`; `-v` del código nuevo rc=0, 7.800 PASS, **0 SKIP**; gate del
  arranque rc=0, 47 PASS. **No corrido**: la integración vieja (no se tocó código compartido) y UAT (F10).
- **Hallazgos 61–65** e informe de fase en el [README de F6](plan/F6-solicitudes/README.md). **Siguiente paso: F7-01.**
  `main` sin tocar.

**F6-04 · F6, `quotetext`, `telemetria`, `integrations` y `crmpush` (2026-10-08, 💻, rama `reorg/f6-04-quotetext-telemetria-integrations-crmpush` desde `dev` @ `36d5a04`).**

T6.10–T6.13 y T6.19–T6.21. Los cuatro paquetes no existían: se crearon enteros —contrato, rojo y verde— orquestando un
sub-agente por paquete, en paralelo y en directorios disjuntos, sin *worktrees*; **commiteó solo el orquestador**. 15
commits, `31b9343` … `af68fe3` (11 de verde, 3 de rojo y 1 de fix), y **los 14 primeros compilan solos** (medido por SHA; el
último se validó con `ci-local`). Ni una línea del
código viejo, del arranque, de la cara HTTP ni de `docs/contracts/`.

- **`telemetria`** (`31b9343`, simple, una pasada): `Publisher` satisface `intakes.MetricsPublisher`; su import de
  `internal/flujos/store` es el **primer puente (import) del repo**, declarado en `internal/modulos/fronteras_test.go`
  en el mismo commit (nace F6, muere F8).
- **`quotetext`** (`0d245b7`, `d87a9f9`, `0159604`, `6a202b0`, `ccd24a7`): borrador y render en una pasada; el
  verificador INV-2 y `Service.Suggest` (P5) con rojo y verde. 6 ficheros de producción (E-13 saca
  `precios_numbers.go` y `quotetext_fewshot.go`). ~35 exportados renombrados a inglés (tabla en `tareas.md`); los 13
  valores de `fallback_reason`, intactos.
- **`crmpush`** (`caa864a`, `3a94b94`, `b01d01c`): `Build`, `Pusher` y `RevisionPusher`; el **candado R-12** en verde y
  comprobado que muerde (7 siembras, 7 fallos); el payload de `Build` valida contra `intake.push.schema.json`.
- **`integrations`** (`d42a5d6`, `afd3eed`, `cb9e36b`, `c2803b5`, `8ebc6eb`): 8 ficheros de producción. Suite
  **`integrationshelpertest.Contrato`** nueva, 61 casos con marca de estado de fila entera, y el doble **`Memoria`**
  (R6.3.b), verdes desde el rojo. `postgres.go` con las 12 sentencias byte a byte y, por D-F6-6, `SecretFingerprint` y
  `CountOutbox`. El worker con reloj inyectado (`WithClock`) y **D-F6-7**: contexto cancelado → vuelve sin `ERROR`, sin
  contar el intento y sin tocar la métrica. Esquema `wapp-crm-v1` (D-F6-3) en verde. Y, pedido por Jhoan tras el cierre
  (`af68fe3`, hallazgo 38a): una plantilla `null` en el outbox es un fallo de entrega más y no un pánico de la goroutine.
- **Mutantes** (a mano, por overlay): nivel complejo, `postgres.go` 37 (34 muertos, 3 equivalentes) y `worker*.go` 46
  (46 muertos); `Memoria` contra la suite 51 (50 y 1 equivalente); nivel medio y simple, 85 (84 y 1 equivalente).
  **219 sembrados · 214 muertos · 5 equivalentes · 0 vivos.** Seis sobrevivían al rojo y pidieron test nuevo (2 en
  `precios`, 2 en `quotetext`, 2 del worker: los de D-F6-7).
- **Gates sobre `8ebc6eb`** (repetidos sobre `af68fe3`: `make ci-local` rc=0, lint `0 issues.`) (toolchain fijada `TOOLCHAIN=OK`, rc sin pipe): `make ci-local` rc=0 (183 `ok`, 0 FAIL, lint
  `0 issues.`) · `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=1` · código nuevo
  `go test -count=1 -race -v ./internal/modulos/solicitudes/...` rc=0, 630 PASS de primer nivel (1977 con subtests),
  **0 SKIP**, 0 FAIL · 0 SKIP en `internal/modulos`, `internal/nucleo` e `internal/arranque` · grep de parada → 0 ·
  `go vet -tags integracion ./test/procesos/...` rc=0. Cobertura (informe): los 16 ficheros nuevos medidos, entre 96,7 % y 100 % (`store.go` y `outbox_stats.go` solo llevan tipos).
- ⏱️ **D-R-6**: 65 min entre el primer commit y el último (10:36–11:41), dentro de los 45–90 del objetivo gracias al
  paralelo por paquete; el rojo de `integrations`, con suite y doble, fue el tramo largo (~50 min).
- **No corrido**: la suite `integrationshelpertest` contra Postgres (escrita y compilada; T6.27), `make test-procesos`,
  la integración vieja (no se tocó código compartido) y el arranque real de `cmd/server-modular` (nada nuevo se cablea
  hasta F6-05).
- **Hallazgos 35–44** en el [README de F6](plan/F6-solicitudes/README.md); 🟡 el 38 trae rarezas del viejo portadas
  fieles que esperan decisión de Jhoan (la principal, el payload `null`, ya está endurecida; queda sobre todo la 38b:
  el gate abre sin URL ni secreto y el worker los exige).
  **Siguiente paso: F6-05.** `main` sin tocar.

**F6-03 · F6, `intakes` (2/2): almacenes, acciones, notificador y candados (2026-10-08, 💻, rama `reorg/f6-03-intakes-almacenes-acciones-candados` desde `dev` @ `5fd533e`).**
Sesión completa (T6.9, T6.16–T6.18), orquestada con once sub-agentes de implementación en dos olas dentro del mismo
paquete (ficheros disjuntos, un solo commiteador, sin *worktrees*) y uno más para el lint. 28 commits, `e8f1e10` …
`4fcccc6`: 22 de verde, 1 de rojo, 1 de refactor y 4 de test. Ni una línea del código viejo, del arranque ni de la cara HTTP.
- **`S/intakes` sin pendientes**: los 108 `pendiente.Implementar` a **0**; 42 ficheros de producción (nacen
  `postgres_revisions_read.go` y `notifier_templates.go`, hallazgo 29) y 59 de test.
- **T6.16 · memoria y las 9 acciones**: `MemoryStore` (`4c93249`) con la suite `intakeshelpertest` **50 de 50** con `-race`;
  D-F6-8 (`UpdatedAt` en toda escritura de cabecera) y D-F6-9 (`StoredStatus`) aplicadas; el servicio (`21438bb`, D-F6-5:
  `Summary` usa `WithClock`) y las acciones, un commit por fichero. Los auxiliares que comparten los dos almacenes,
  unificados en `edit.go` y `discard.go` (`141acfd`).
- **T6.17 · notificador y comprador** (`cf61639`, `e8f1e10`): las 7 plantillas de estado y las 4 del CRM copiadas por rango
  de líneas del viejo (`diff` vacío). El JSON `null` del comprador (hallazgo 18) se **endureció** tras el cierre, por orden de Jhoan: error con rollback en vez del pánico del viejo (hallazgo 32).
- **T6.18 · adaptador Postgres** (`e8126b5` … `09872e6`): 13 trozos, SQL copiado por rangos y **afirmado byte a byte
  contra el texto del viejo** en el test de cada tema; el `diff` de `public\.[a-z_]*` viejo/nuevo solo difiere en
  `public.flow_events`, que en el viejo es una mención en un comentario. **Candado de la poda** (`ab2e4bb`, R6.2.d),
  comprobado que muerde.
- **T6.9 · candados**: los dos del plazo (R6.2.c), en verde, en `vencimiento_test.go` (`4c1258c`); los dos INV-1
  (`bd9ea5a`), tras `//go:build pendiente` hasta T6.25: su control positivo falla porque `apipublica` aún no sirve la
  aprobación, y la otra mitad (0 llamadas en los seis directorios automáticos) ya se cumple.
- **Suite contra Postgres, destapada y pre-chequeada** (`8d6cfa8`): `TestIntakesContrato_Postgres` rc=0, **50 PASS, 0 SKIP**
  contra el binario viejo, una vez. La primera pasada dio 48 de 50 y destapó un error de la suite; ✅ **Jhoan, 2026-10-08:
  sin una mejora clara, manda el viejo** (hallazgo 24). No cierra T6.27: eso es F6-06.
- **Mutantes** (a mano, por fichero): 253 probados, 252 muertos, 1 equivalente declarado. Tres pidieron test nuevo (hallazgo 33).
- **Gates sobre `4fcccc6`** (toolchain fijada `TOOLCHAIN=OK`, rc sin pipe): `make ci-local` rc=0 (173 `ok`, 0 FAIL, lint
  `0 issues.`) · `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=1` (fallan solo los dos INV-1, esperado) · código nuevo
  `go test -count=1 -race -v ./internal/modulos/solicitudes/...` rc=0, 430 PASS de primer nivel (1.280 con subtests),
  **0 SKIP**, 0 FAIL · grep de parada → 0. Cobertura (informe): los 40 ficheros evaluados de `S/intakes`, entre 91,7 %
  (`literal.go`) y 100 %; `POR_DEBAJO=7`, ninguno de `solicitudes`.
- ⏱️ **D-R-6**: 25 min entre el primer commit y el último (09:33 → 09:58, fecha de autor); la pared, unos 35 min.
- **No corrido**: `make test-procesos` completo (los dos binarios) ni `make test-integration` (no se tocó código viejo ni
  compartido).
- **Hallazgos 24–34** en el [README de F6](plan/F6-solicitudes/README.md); ninguna 🟡 abierta. **Siguiente paso: F6-04.** `main` sin tocar.

**F6-02 · F6, `intakes` (1/2): contratos del paquete y tipos puros (2026-10-08, 💻, rama `reorg/f6-02-intakes-contratos-y-tipos` desde `dev` @ `64c181a`).**
Sesión completa (T6.6–T6.8, T6.15), orquestada con sub-agentes por grupo de ficheros (tres para los tipos puros —rojo y
luego verde— y cinco para los contratos en rojo, los ocho de la segunda tanda a la vez en el mismo paquete; un solo
commiteador). 13 commits, `113912e` … `c366c68`: 3 de rojo, 9 de verde y 1 de test. Ni una línea del código viejo, del
arranque ni de la cara HTTP.
- **Recuento real**: `S/intakes` tiene **40** ficheros de producción (la ficha decía 24, que son los del viejo), **56** de
  test y **10** de la suite. D-F6-6 ampliada y E-13: `memory.go` nace en 4, `postgres.go` en 12; y dos que la spec no
  preveía, `service_metrics.go` y `service_revalidate.go` (hallazgo 11). Tipos puros: **9**, no 10 (`customernote.go` no nace).
- **T6.6 · rojo de los tipos** (`113912e`) y **T6.15 · verde, un commit por fichero** (`b45ad8d` `status` · `0698426`
  `intakes` · `7634ba7` `summary`, simple, una pasada · `0426da5` `revisions` · `eb26402` `literal` · `090ee38` `shipping` ·
  `743d85d` `revalidate` · `909c967` `metricas` · `c4420eb` `crm`). Equivalencia con el viejo por vectores literales
  calculados con `internal/intakes` y casos adversarios (T-15); 2–7 mutantes a mano por fichero, todos muertos salvo dos
  equivalentes declarados en su commit.
- **T6.7 · rojo de la bandeja** (`9f393c2`): las 9 acciones + los dos ficheros nuevos. **D-F6-5 = `WithClock`**,
  independiente de `WithMetricsClock`. Los tests del servicio, corridos contra la lógica vieja por `-overlay`, matan 55 de
  55 mutantes: es la lista que F6-03 tiene que matar con la lógica nueva.
- **T6.8 · rojo de almacenes, comprador y notificador** (`c366c68`): `MemoryStore`, el adaptador Postgres (con los cuatro
  métodos que el viejo tenía fuera de `postgres.go`), `buyerdata.go` + `buyerdata_postgres.go` (T-1 en el comentario),
  `notifier.go` con sus 11 plantillas + la de seña asertadas byte a byte, dos drivers `database/sql` falsos con
  transacciones, y la suite **`intakeshelpertest.Contrato`** (50 casos; la marca de estado compara la fila entera y dos
  testigos, hallazgo 35). La invocación contra Postgres queda **escrita y compilada, no corrida**
  (`test/procesos/intakes_contrato_test.go`, `integracion && pendiente`): la corre F6-06.
- **E-11**: ~30 exportados del viejo estaban en español y nacen en inglés; tabla en
  [`tareas.md`](plan/F6-solicitudes/tareas.md), bloque F6-02. Los nombres de fichero del inventario se conservan.
- ✅ **Decididas por Jhoan el 2026-10-08** (hallazgos 13–15 del [README de F6](plan/F6-solicitudes/README.md), en el mismo
  PR): **D-F6-8**, `UpdatedAt` se refresca en toda escritura de cabecera (como el Postgres viejo, no como el doble viejo;
  lo escribe el `MemoryStore` nuevo en F6-03) · **D-F6-9**, `(*MemoryStore).StoredStatus` se conserva · **D-F6-10**, la
  suite reexporta `WithLiteralCipher` y el `Montaje` de Postgres cablea el cifrador del literal con keyring propio, sin
  tocar el candado `ProcessImports` (escrito y compilado, no corrido: F6-06).
- **Gates sobre `c366c68`** (toolchain fijada, rc sin pipe): `make ci-local` `GATE_RC=0` (173 `ok`, 0 FAIL, lint 0 issues) ·
  `make test-pendiente` `RC=0`, `PENDIENTES=108 · ROJOS=43` (los rojos fallan por `pendiente`, esperado) · código nuevo
  `go test -count=1 -race -v ./internal/modulos/solicitudes/...` `RC=0`, 126 PASS, **0 SKIP**, 0 FAIL ·
  `go vet -tags 'integracion pendiente' ./test/procesos/...` rc=0 · candados de `internal/modulos` rc=0 con y sin etiqueta.
  Cobertura (informe): los 10 ficheros en verde de `S/intakes`, entre 91,7 % (`literal.go`) y 100 %.
- ⏱️ **D-R-6**: 26 min entre el primer commit y el último (23:57 → 00:23, fecha de autor); la pared de la sesión fue mayor
  y no se midió.
- **No corrido**: la suite contra Postgres, `make test-procesos` y `make test-integration` (no se tocó código viejo ni
  compartido, y no hay implementación nueva de los almacenes todavía).
- **Hallazgos 11–23** en el [README de F6](plan/F6-solicitudes/README.md). **Siguiente paso: F6-03.** `main` sin tocar.

**F6-01 · F6, inventario E-12 y hojas de `solicitudes` (2026-10-07, 💻, rama `reorg/f6-01-inventario-y-hojas` desde `dev` @ `3a21138`). F6 ARRANCA.**
Sesión completa (T6.1–T6.5, T6.14), con sub-agentes (uno por paquete) y los gates por el ejecutor de comandos. 8 commits,
`27a7924` … `06879cb`: 1 de inventario, 1 de rojo, 4 de verde, 1 de procesos y 1 de comentario. Ni una línea del código
viejo, del arranque ni de la cara HTTP.
- **T6.1 · inventario E-12** (`27a7924`): 41 ficheros, 12.235 líneas, medido sobre `3a21138` y **aprobado por Jhoan**
  ([`diseno.md`](plan/F6-solicitudes/diseno.md) §1.2). Entradas E1–E4 comprobadas (E4: el código viejo no cambió desde
  `1b18932`).
- **Decisiones de Jhoan en la sesión**: **D-F6-1 se mantiene** frente a P5 (segunda instancia vieja de
  `intakes.Postgres`; nacen 0 adaptadores; sirve a 4 puertos —carrito y 3 de captación— y muere en F7 y F8) ·
  **D-F6-6 se amplía** a `aprobadas.go`, `crm.go`, `customernote.go` y `reanalisis.go` · **`sigv1` fiel al viejo**
  (sin ventana ±300 s ni reloj: son de la cara HTTP) · niveles: `service.go` → complejo, `notifier.go` → medio,
  `summary.go`, `quotetext/borrador.go` y `quotetext/render.go` → simple; `integrations/crud.go` y `outbox_stats.go`
  se quedan en medio.
- **`integrations/sigv1`** (`98f9220`, simple, una pasada) e **`intakes/note.go`** (`571482b`, simple, una pasada; el
  texto `cart: …` byte a byte, D-F6-4): equivalencia con el viejo por vectores literales, con casos adversarios.
- **`tenantvars`**: rojo `8c7ce8b` (contratos, `tenantvars.go` ya en verde y la suite `tenantvarshelpertest.Contrato`,
  12 casos) → verdes `0108667` (`memory`) y `177f529` (`postgres`, con driver falso con transacciones); mutantes de
  los dos ficheros, todos muertos. `8cb2129`: la invocación de la suite contra Postgres, **escrita y compilada**.
- **Gates sobre `06879cb`** (rc leído del log): `make toolchain` `TOOLCHAIN=OK` (`go1.26.5`, lint `v2.12.2`);
  `GOWORK=off make ci-local` **`GATE_RC=0`** (173 `ok`, 0 `FAIL`, lint `0 issues.`; cobertura, informe:
  `FICHEROS_EVALUADOS=174`, `POR_DEBAJO=7`, ninguno de `solicitudes` — `note.go` 100 %, `sigv1.go` 94,1 %,
  `tenantvars/memory.go` y `postgres.go` 100 %); `make vet-pendiente` rc=0; `make test-pendiente` rc=0,
  `PENDIENTES=0 · ROJOS=0`; `go test -count=1 -race -v ./internal/modulos/solicitudes/...` rc=0, 157 PASS, **0 SKIP**;
  `go test -count=1 -v` de `internal/{modulos,nucleo,arranque}` rc=0, 4562 PASS, **0 SKIP**.
- ⏱️ **D-R-6**: ≈ 15 min de pared de ejecución (23:11 → 23:26, de la rama al último gate), más el inventario y su
  aprobación antes; 8 commits, 13 ficheros nuevos de Go.
- **No corrido**: la suite de `tenantvars` contra Postgres y `make test-procesos` (los corre F6-06, T6.27).
- **Hallazgos 1–10** en el [README de F6](plan/F6-solicitudes/README.md). **Siguiente paso: F6-02.** `main` sin tocar.

**F45-03 · F4 y F5, cierre local (2026-10-07, 💻, rama `reorg/f45-03-cierre` desde `dev` @ `6d53966`). F4 CERRADA. F5 CERRADA.**
Sesión de cierre (T4.29–T4.31, T5.20–T5.21, T9.25, T9.26), sin traspaso, con sub-agentes (uno por fichero) y los gates
por el ejecutor de comandos. 2 commits de test, `ddb8baa` y `59a3e6b`. Ni una línea de código de producción, ni del
código viejo, ni `go.mod`; `Conmutados` no cambia (`{"acceso","edge"}`).
- **Línea base** (antes de tocar nada, sobre `6d53966`): `make test-procesos` viejo y nuevo `RC=0 · PASS=828 · FAIL=0 ·
  SKIP=0`.
- **T4.31 = T9.25 · las dos suites contra Postgres** (`ddb8baa`): `TestTenantLLMContrato_Postgres` (16 casos) y
  `TestDegradationContrato_Postgres` (18), 34 PASS, 0 SKIP en cada binario, **ninguna divergencia con los dobles**.
  `degradationhelpertest` gana el alias `Notice` (candado `ProcessImports`, regla 3b; no se tocó el candado).
- **Lo que F45-02 dio por cierto, contra lo que corre**: (1) 🟡 las tres rutas de `tenant-llm` **no tenían proceso**:
  nace el paso `via_llm` de P4 (`59a3e6b`), 31 intercambios idénticos en los dos binarios; `degradation-notices` ya lo
  cubría P4. (2) El 502 de sesión offline sale por el cable (`TestP1_EdgeFaceOverTheWire/offline_session_is_502`) en
  todas las pasadas. (3) **Arranque real de `cmd/server-modular`** (Postgres `17-alpine` efímero, puerto libre, sin
  commit): con las plantillas volcadas, **9/9**, `/healthz` 200, 0 `ERROR`, `EXIT=0`; con `"package_size": 0` en P4,
  **`EXIT=1`** en la fase 5/9 (`prompts ajustables de P2-P5: …`), sin listeners. Sin token, las 4 rutas de inferencia dan
  401 y una inexistente 404. Ninguna de las tres quedó refutada.
- **Gates sobre `59a3e6b`** (rc leído del log): `make toolchain` `TOOLCHAIN=OK` (`go1.26.5`, lint `v2.12.2`);
  `GOWORK=off make ci-local` **`GATE_RC=0`** (167 `ok`, 0 `FAIL`, lint `0 issues.`; cobertura, informe:
  `FICHEROS_EVALUADOS=170`, `POR_DEBAJO=7`, ninguno de F4 ni F5); `make vet-pendiente` rc=0; `make test-pendiente` rc=0,
  `PENDIENTES=0 · ROJOS=0`; `go test -count=1 -v` de `internal/{modulos,nucleo,arranque,apipublica}` rc=0, 4.935 PASS,
  **0 SKIP** (`inferencia` 558; `catalogo` + `conversacion/model` 547).
  **`make test-procesos`**: 🟡 **pasada 1, viejo `RC=1 · PASS=854 · FAIL=15`** (`TestP6_CRMBridge/push_gate_closed` y 14
  en cascada) y nuevo `RC=0 · PASS=869 · FAIL=0 · SKIP=0`; **pasada 2, viejo `RC=0 · PASS=869`** y 🟡 **nuevo `RC=1 ·
  PASS=866 · FAIL=3`** (`TestP7_Catalog/index_cache`); **pasada 3, `BINARIO=nuevo`: `MAKE_RC=0`, `RC=0 · PASS=869 ·
  FAIL=0 · SKIP=0`**. Las dos rojas son la **intermitencia ya anotada** del compositor del *flush* (hallazgo 52 de F2,
  contradicción 8 de F7: `el job no trae literal que analizar`), en código de captación viejo que esta sesión no toca,
  y en un proceso distinto cada vez; repetidos `-count=4`: P6 8 de 8 (los dos binarios), P7 4 de 4 (nuevo). 0 SKIP en
  todas. 🟡 Sospecha nueva para F7: llegaron al sumar 34 bases clonadas a la corrida (hallazgo 30 de F4).
- ⏱️ **D-R-6**: ≈ 45 min de pared (21:46 → 22:30, hora local) para las dos fases; 2 commits de test, 5 ficheros `.go` (3 nuevos y 2 modificados, todos de test o de suite de contrato).
- **No corrido**: `make test-integration` (la integración vieja: no se tocó código viejo ni compartido). **UAT no es de
  esta sesión: es de F10** ([`plan/README.md`](plan/README.md)); el bloque de F45-02 la apuntaba aquí por error.
- **Hallazgos 27–32** en el [README de F4](plan/F4-inferencia/README.md) y **25–27** en el
  [README de F5](plan/F5-catalogo/README.md), que además deja escrito **lo que heredan F7 y F8**. **Siguiente paso:
  F6-01.** `main` sin tocar.

**F45-02 · F4 conmutada y F5 entero en verde, con su conmutación nominal (2026-10-07, 💻, rama `reorg/f45-02-conmutar-inferencia-catalogo` desde `dev` @ `3c74b80`).**
Sesión completa (D-R-8), 29 commits (8 `rojo`, 16 `verde`, 3 `conmutar`, 2 `test`), `65f5348` … `13e869f`. **F4 queda
conmutada (`FaseActual = 4`) y F5 con todo su código en verde y `FaseActual = 5`; ninguna de las dos está cerrada.**
⏱️ **D-R-6**: 46 min entre el primer commit y el último (19:41 → 20:27, fecha de autor); la pared de la sesión no se midió.
- **F4, adaptador y conmutación** (T4.10, T4.24–T4.28, TX.12–TX.14): `internal/arranque/bridge_inferencia.go`
  (`65f5348`: `turneroBridge`, `llmConfigBridge`); la cara nueva de `inferencia` en `apipublica` (rojo `fee3ed2`, verde
  `c619013` y `23a7d92`); `conmutar(inferencia)` (`98b24c5`): el arranque nuevo construye selector, almacenes y prompts
  **nuevos**, `llmvia.WithFrame(c.gw)` directo, `bridge_gateway.go` y su test **borrados**, 4 rutas mudadas (33 = 23 de
  acceso + 6 de edge + 4 de inferencia). El test de cableado completo (`c2f469a`, `inference_wiring_test.go`), el de
  prompts (`0e532e4`) y el candado copiado del turno (`75298b8`). **T4.24, TX.14 y el `FaseActual = 4` de T4.28 van en
  un solo commit**: por separado no compila o cambia la huella (hallazgos 12 y 13 de F4).
- **F5 entero** (T5.2–T5.19, TX.15): `conversacion/model` (`523ef63` → `b32145e`), `catalogo/catalog.go` (`2157217` →
  `f2591e6`), `catalogimport` (rojo `07f5c17`, `0b30c57`; verde `59dadba` … `1ffa9e8`, 6 commits) e `indice` (rojo
  `b5d4bb8`, `d84475c`, `dec75e7`; verde `91f5c96`, `f95e762`, `c71b785`; test `3a79ba8`, `0977194`). Conmutación
  nominal `13e869f`: solo `FaseActual` (4 → 5) y su aserción. E-13 partió `validator.go` (4 ficheros) y `tabular.go` (3).
  68 ficheros `.go` tocados, 14.352 líneas añadidas y 278 quitadas.
- **Decisiones de Jhoan en la sesión** ([`plan/DECISIONES.md`](plan/DECISIONES.md) D-F3-14, D-R-4 y D-F5-2):
  (a) `edge` entraba en `Conmutados` en T4.24 **salvo** que obligara a tocar aserciones de
  `internal/arranque/session_identity_test.go`: se cumplió la salvedad y **no entró**; (b) `catalogo` **no** entra hasta
  que F7 conmute el índice ([`plan/F5-catalogo/reglas.md`](plan/F5-catalogo/reglas.md) §4.10 corregido); (c) D-F5-2 sin
  ampliar el motor de fronteras: la arista `conversacion → catalogo/indice` vive en la regla 1 (`dec75e7`).
  Tras (a), `Conmutados` seguía `{"acceso"}` y quedaba abierto qué se hacía con `session_identity_test.go`.
- **D-F3-14 cerrada al cierre de la sesión (Jhoan, 2026-10-07): se borra `internal/arranque/session_identity_test.go`
  y `edge` entra; `Conmutados` es `{"acceso","edge"}`.** El fichero tenía 37 líneas y un test
  (`TestIdentidad_SessionOfflineIsOneSentinelAcrossBothTrees`, `dd4cbd2`, T3.27 = TX.10). Era redundante, medido con
  mutaciones en una copia: rompiendo el centinela nuevo dan rojo `TestErrSessionOfflineIsThePlatformSentinel` y
  `TestPushOfflineSession` (`internal/modulos/edge/session/registry_test.go:90-97`) y
  `TestP1_EdgeFaceOverTheWire/offline_session_is_502`; rompiendo el viejo, `TestSendMessageHandler_Offline` y
  `TestSendMessageHandler_StreamCaido_NoPisaEl502DeOffline` (`internal/platform/httpapi`). Se pierde la aserción
  directa `errors.Is(nuevo, viejo)`; P1 no corre en `make ci-local`. Se gana que la regla 3 vigila en
  `internal/arranque` todo lo viejo de `edge` (`gateway`, `diagnostics`, `inferstats`, `receipts`, `ingest`,
  `filtercfg`). Con `edge` dentro y el test presente, `TestFronteras` daba una violación (ese test); borrado:
  `go test -count=1 -v ./internal/modulos/ ./internal/arranque/ ./internal/modulos/edge/session/
  ./internal/platform/httpapi/ ./internal/candados/` rc=0, 0 SKIP. Detalle y alternativas descartadas en
  [`plan/F3-edge/reglas.md`](plan/F3-edge/reglas.md) §4.7.
- **Huella** con `FaseActual = 4` y `5`: idéntica sin tocar la dorada; perfiles `minimo` y `con-m2m`:
  `:8100=22 :8103=73 rpc=2 metricas=11`.
- **Mutantes**: cableado de F4, 4 a mano, 4 muertos (hallazgo 17 de F4); `indice/cache.go`, 42 a mano, 41 muertos y
  **1 vivo equivalente**; el único vivo no equivalente lo mata `3a79ba8` (hallazgo 17 de F5).
- **P99 del índice** (T5.14, `-count=5`, sin `-race`, plazo 5 ms): 56,125 · 62,375 · 56,125 · 41,25 · 76,917 µs; con
  `-race`, 126 µs.
- **Gates**: sobre `13e869f`, a solas y con los *worktrees* ya retirados: `make toolchain` rc=0 (`TOOLCHAIN=OK`, `go1.26.5`, lint `v2.12.2`); `GOWORK=off make ci-local` **`GATE_RC=0`** (167 paquetes `ok`, 0 `FAIL`, lint `0 issues.`); `make test-pendiente` rc=0, `PENDIENTES=0`, `ROJOS=0`; 0 `pendiente.Implementar` y 0 `go:build pendiente` en `internal/{modulos,apipublica,arranque,nucleo}`; `go test -count=1 -v` de `internal/{modulos,nucleo,arranque,apipublica}` rc=0, **0 `--- SKIP`**, 1.795 `--- PASS`, 0 `--- FAIL`; cobertura (informe, no bloquea): `FICHEROS_EVALUADOS=170`, `POR_DEBAJO=7` (los siete adaptadores Postgres de `acceso` y `nucleo/contact`, ninguno de esta sesión); `git diff --stat dev -- internal/arranque/testdata` vacío.
  **No corrido** (es de F45-03): `make test-procesos`, la integración vieja contra Postgres, las suites de contrato
  contra Postgres y el arranque real de `cmd/server-modular`. ✎ *(F45-03: aquí decía también «y UAT»; UAT es de F10.)*
- **Hallazgos 12–26** en el [README de F4](plan/F4-inferencia/README.md) y **6–24** en el
  [README de F5](plan/F5-catalogo/README.md). **Siguiente paso: F45-03** (cierre local de las dos: T4.29–T4.31,
  T5.20–T5.21, T9.25, T9.26). `main` sin tocar.

**F45-01 · F4 + F5, inventario E-12 de las dos e `inferencia` en verde (2026-10-06/07, 💻, rama `reorg/f45-01-inventario-inferencia` desde `dev` @ `ebf4eb7`).**
Sesión completa (D-R-8). **F4 arranca; `internal/modulos/inferencia` queda entero en verde, sin conmutar.**
⏱️ **D-R-6, minutos con E-12**: ≈ 100 min de pared de ejecución (23:05 → 00:42), más la exploración previa del inventario.
- **Los dos inventarios E-12, aprobados por Jhoan** antes de escribir código (`2783172` F4, `5ba9fed` F5): fichero a
  fichero en [`plan/F4-inferencia/diseno.md`](plan/F4-inferencia/diseno.md) §6.1 y
  [`plan/F5-catalogo/diseno.md`](plan/F5-catalogo/diseno.md) §6.1. F4: 10 ficheros, 3.072 líneas; se apartan de la
  provisional `degradation/postgres.go` (medio, D-F3-7), los dos `memoria.go` (simple, P1 de F2) y `calentamiento.go`
  (simple). F5: 11 ficheros, 4.195 líneas, **D-F5-1 = B**, 0 adaptadores, sin BD; `contract.go` y `prompt.go` a simple.
  **D-F4-2 aplicada a `05` §3.2** (C2 y vocabulario de `degradation`).
- **T4.2 verificada**: los dos barridos AST viejos saltan el árbol nuevo por ruta de primer nivel (`dd1e2bd`); una
  sonda `if via == "api"` en `internal/modulos/x` no los puso rojos y se borró.
- **Rojo de todo el módulo** (7 commits, `bccfa4b` … `b8ae091`): 45 `pendiente.Implementar` (la spec estimaba ≈ 54,
  con el adaptador), `ci-local` rc=0. **Verde** (15 commits, `24c0782` … `24f6450`): `prompts`, `tenantllm`,
  `degradation`, `llmvia/local`, `llmvia` y el **candado C2 nuevo**. Escrito por nueve sub-agentes en *worktrees*
  fijados en el SHA de la rama (uno por paquete; rojo antes que cualquier verde) e integrado con `cherry-pick`.
  45 ficheros `.go`, 12.133 líneas. E-13 partió `llmvia.go` (`llmvia_turno.go`) y `local.go` (`local_budget.go`).
- **Gates** (toolchain `go1.26.5` / `v2.12.2`): `make ci-local` `GATE_RC=0`, lint 0 issues · `make vet-pendiente` rc=0 ·
  `go test -race -v` de `inferencia` + candados de `modulos` + C2 viejo: **621 PASS, 0 SKIP, 0 FAIL** · en el árbol,
  0 `pendiente.Implementar`, 0 etiquetas, 0 `t.Skip` · cobertura (informe): `FICHEROS_EVALUADOS=151`, `POR_DEBAJO=7`
  (ninguno de `inferencia`: 12 ficheros al 100 %, `prompts.go` 97,4 %, `volcar.go` 91,8 %) · código viejo,
  `internal/arranque` y `cmd` **sin una línea tocada**. ⚠️ `make test-pendiente` imprime `PENDIENTES=33 · ROJOS=17`:
  son de un *worktree* de sub-agente que el harness dejó bloqueado (hallazgo 7 de F4), no del árbol.
  **No corrido**: las suites contra Postgres y `make test-procesos` (F45-03); la huella y la conmutación (F45-02).
- **Mutantes**: 181 a mano (53 en `tenantllm/postgres.go`, 41 en `notify.go`, 57 en `llmvia.go`, 30 en
  `llmvia_turno.go`): 176 muertos (4 de ellos solo tras un test nuevo: `21e8a5a`, `c5b4b74`) y **5 vivos
  equivalentes** (hallazgo 9 del [README de F4](plan/F4-inferencia/README.md), con los otros 10 hallazgos).
- **Siguiente paso: F45-02** (T4.10 `bridge_inferencia.go`, conmutar, las 4 rutas, y F5 entero). `main` sin tocar.

**F3-05 · F3, cierre local con mTLS (2026-10-06, 💻, rama `reorg/f3-05-cierre-mtls` desde `dev` @ `9d9c033`). F3 CERRADA.**
Sesión completa (D-R-8), T3.29–T3.30 (= T9.24) y la parte 💻 de T3.27 (= TX.10). Sin traspaso. Orquestada con cuatro
sub-agentes **en paralelo, cada uno en su *worktree*** puesto en el SHA de `dev`; sus 11 commits se integraron por
`cherry-pick` y los gates los repitió el agente principal sobre la rama entera. El cierre fue **solo tests y arnés**;
después, a petición de Jhoan, entraron en el mismo PR dos correcciones de producción del código nuevo (D-F3-12 y
D-F3-13, abajo). Ni una línea del código viejo, ni `go.mod`.
- **Línea base** (antes de tocar nada): `make test-procesos` viejo y nuevo `RC=0 · PASS=668 · FAIL=0 · SKIP=0`.
- **T3.30 / T9.24 · las 7 suites contra Postgres** (`8121564` `lease`, `a28f28b` `enroll` ×2, `0caeef4` `receipts`,
  `aaed74c` `ingest`, `0133b4b` `diagnostics`, `93b145b` `fleet`): 115 PASS, 0 SKIP, ninguna divergencia con los dobles.
  Más cinco tests propios: el borrado real de D-F3-11 y cuatro de `self_pn` (sobre + índice ciego, guarda, KEK rotada,
  `degraded_since`).
- **T3.29 · e2e con mTLS real**: `e0ba4f6` + `876b096` (login de operador por el canal de control, tres Edge de dos
  empresas a la vez, cada uno recibe el suyo; códigos de error idénticos en los dos binarios: hallazgo 69 confirmado),
  `c004d9a` + `5e481c8` (el arnés reinicia el servidor: revocación y corte sobreviven), `f5b0fa6` (mensajes por la cara
  nueva: hallazgo 70 confirmado; `self_pn`; corte de empresa con dos Edge vivos).
- **T3.27 / TX.10** (`f5b0fa6`): `/admin/flows/start` a una sesión offline → 502 con el mismo cuerpo en los dos binarios.
- **Gates** (rc leído del log): `make toolchain` `TOOLCHAIN=OK`; `make test-procesos` **viejo `RC=0 · PASS=812 · FAIL=0 ·
  SKIP=0`, nuevo igual**; `make ci-local` rc=0 (145 `ok`, lint 0 issues; cobertura, informe: `FICHEROS_EVALUADOS=136`,
  `POR_DEBAJO=7`); `make vet-pendiente` rc=0; `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`.
- ✎ **El mutante del cruce entre Edge (HS-14/HS-15) sí se corrió** al final, con permiso de Jhoan: respuesta de auth
  por el stream del primer Edge → `TestP1_OperatorLoginOverControlChannel` rc=1 contra el nuevo (la respuesta no llega
  a quien la pidió), rc=0 contra el viejo; deshecho, árbol limpio (hallazgo 82).
- **No corrido**: `make test-integration`
  viejo (no se tocó código compartido); el SKIP con `-v` sobre `internal/modulos/...` suelto. 🟡 Por decidir: hallazgo 81
  (parada de ≈ 10 s con Edge conectados); el 75 se resolvió en el mismo PR (abajo).
- ✎ **Dos hallazgos corregidos en el mismo PR** (decisión de Jhoan, 2026-10-06): el **75** (`61ded8b`: los `helpertest`
  de `enroll` y `fleet` exportan alias de sus tipos, como `iam`; mueren los rodeos genéricos) y la parte de `fleet` del
  **76** (`e41df74`: `PendingGreeting`, `MarkGreeted`, su carrera y el sobre incompleto contra Postgres; 12 mutantes,
  todos muertos, los tres de `fleet` pendientes incluidos). Con ellos, `make test-procesos` **viejo y nuevo `RC=0 ·
  PASS=821 · FAIL=0 · SKIP=0`** y `make ci-local` rc=0. 🟡 Nuevo, solo leído en el código: un sobre de `self_pn` a
  medias con índice y `kek_id` intactos no se auto-sana (hallazgo 76).
- ✎ **Dos correcciones de producción en el mismo PR, las dos apartándose del viejo a propósito** (Jhoan, 2026-10-06;
  [`DECISIONES.md`](plan/DECISIONES.md)): **D-F3-12** (`5d8acbd` rojo, `f9979fe` verde: `SetSelfPn` re-cifra un sobre de
  `self_pn` a medias con el latido siguiente) y **D-F3-13** (`66214d7`, `73407f2`, `831e480`, `648d270`: el CloudLink
  para en cuanto no queda nada en vuelo — `edge/grpc.Server.InFlight()` — en vez de agotar siempre los 10 s; medido
  10,014 s → 0,061 s con Edge conectados; huella igual). Ya son **cinco** las conductas en que el nuevo se aparta del
  viejo: D-F3-9 … D-F3-13. 🟡 Sin afirmar: el `MarkOffline` en Postgres tras la parada rápida (hallazgo 81).
  **Gates sobre `648d270`**: `make ci-local` `GATE_RC=0` (145 `ok`, lint 0 issues); `make test-pendiente` rc=0,
  `PENDIENTES=0 · ROJOS=0`; `make test-procesos`, **primera pasada en rojo** (viejo `RC=1 · PASS=826 · FAIL=2`:
  `TestP6_CRMBridge/callback_body_adversarial`, `broken pipe` al mandar el cuerpo grande; nuevo `RC=1 · PASS=825 ·
  FAIL=3`: `TestP4_WindowRules/segunda_ventana`, «el compositor del flush no llegó a escribir el sobre») — las dos son
  **intermitencias ya anotadas** (hallazgo 52 de F2 y README de F9), en código que esta sesión no toca y distintas en
  cada binario; los dos tests repetidos `-count=4` por binario, 16 de 16 PASS; **segunda pasada entera: viejo y nuevo
  `RC=0 · PASS=828 · FAIL=0 · SKIP=0`**.
- **Hallazgos 74–83** en el [README de F3](plan/F3-edge/README.md). **Siguiente paso: F45-01** (F4 + F5). `main` sin tocar.

**F3-04 · F3, adaptador, cara nueva y conmutación de `edge` (2026-10-06, 💻, rama `reorg/f3-04-bridge-conmutar-rutas` desde `origin/dev` @ `115a4ba`).**
Sesión completa (D-R-8), T3.24–T3.28 = FX TX.8–TX.11. Orquestada con cuatro sub-agentes **en serie y en el árbol
principal** (sin *worktrees*); los gates los repitió el agente principal tras cada uno.
- **T3.27 / TX.10** (`dd4cbd2`): `session_identity_test.go` fija que el `ErrSessionOffline` nuevo, el viejo y el de
  `platform/httpapi` son la misma variable. La producción ya lo declaraba así; sin puente. ✎ 2026-10-07: ese fichero
  se borró al cierre de F45-02 (D-F3-14). 🟡 Falta el e2e (F3-05): `[~]`.
- **T3.24** (`b6d47bd`): `bridge_gateway.go`, nivel simple en una pasada. `gatewayBridge{gw}` presenta el servidor nuevo como
  el `local.Frame` del selector viejo (los 9 campos de `InferRequest`, afirmados por reflexión) y expone `PlazaDe`.
- **T3.26 / TX.8 + TX.9** (`0e5b69a` … `d4ef6e6`, 3 rojos y 7 verdes): `apipublica/{deadlines,limits,messages,health,
  sessions,sessionadmin,diagnostics}.go` con `Mount{Messages,Sessions,Diagnostics}`; `sessionadmin.go` exporta los dos
  constructores (D-FX-2); `messages.go` mapea los centinelas del `edge/session` nuevo; ningún import viejo desde la cara.
- **T3.28 + T3.25 / TX.11** (`0ebb743`): **un** `edgegrpc.New` con las 12 opciones de siempre; lease, enroll, fleet, receipts,
  diagnostics, inferstats, ingest y filtercfg son los de `internal/modulos/edge`; el mismo gateway llega al runtime viejo
  (`Sender` + 4 hooks), al notificador, a `filtercfg.NewPusher`, a J12–J15, al `ConfigPush` de la cara vieja (E1–E2, hasta F7)
  y, detrás de `gatewayBridge`, al selector LLM. D1–D6 por la cara nueva, J16/J17 con los handlers de `sessionadmin.go` en
  el mismo commit. `FaseActual = 3` (29 rutas). **`bridge_iam.go` borrado; `acceso` en `Conmutados`, `edge` no** (hasta F4).
  `gateway_wiring_test.go`: solo el adaptador importa el gateway viejo, un solo gateway, y seis consumidores sobre la misma
  instancia. Huella idéntica en los dos perfiles **sin tocar la dorada**; `go.mod` intacto; código viejo intacto.
- **Gates** (toolchain `go1.26.5` / `v2.12.2`, `TOOLCHAIN=OK`): `make ci-local` `GATE_RC=0` leído del log (145 `ok`, lint
  0 issues) · gate de la ficha `-run 'Mudanzas|Huella|Cableado|Identidad'` rc=0, 28 PASS, 0 SKIP · código nuevo `-v`
  (`modulos`, `nucleo`, `arranque`, `apipublica`) rc=0, 1.375 PASS de primer nivel, 0 SKIP · `PENDIENTES=0 · ROJOS=0` ·
  cobertura (informe) `FICHEROS_EVALUADOS=136 · POR_DEBAJO=7`, ninguno de esta sesión (los ocho ficheros nuevos, entre
  88,5 % y 100 %) · `ls internal/arranque/bridge_iam.go` no existe · `grep -rn 'gatewaygrpc.New(' internal/arranque` vacío.
- **No corrido** (es de F3-05): el e2e con mTLS real, el arranque real de `cmd/server-modular`, `make test-procesos` contra
  los dos binarios, las suites contra Postgres. Sin mutantes: el bloque es nivel simple + cara.
- Hallazgos 65–73 en el [README de F3](plan/F3-edge/README.md). Los tres 🟡 (68, 69, 70) **se resolvieron en el mismo PR**,
  a petición de Jhoan: 68, corrigiendo `arquitectura.md` §6 y la verificación de R3.6.c (el binario enlaza cuatro paquetes
  de `internal/gateway/`, no uno, y no por el arranque); 69, con `TestCableado_RealAccessServicesSpeakTheSentinelsTheGatewayClassifies`
  (más el `diff` vacío de `authErrorCode` entre los dos gateways); 70, con `TestCableado_TheOldFaceNeverServesMessages` (la
  D1 vieja es inalcanzable, no solo tapada). También se corrigieron la línea caducada de `FX-cara-http/diseno.md` (66) y
  `PROTOCOLO-CLI.md` §1 (73). Del 71 (conductas heredadas) se corrigió una por **D-F3-11**: el *rollback* de D5 ya no muere con el contexto de la
  petición (🔴 la cara nueva se aparta de la vieja); el resto se queda, copiado y fijado por test.

**F3-03 (tercera sesión) · F3, `grpc` — tanda 3 de 3 (2026-10-05, 💻, rama `reorg/f3-03-grpc-tanda-3` desde `origin/dev` @ `ec236b3`).**
Sesión completa (D-R-8), **cortada a medias y relanzada**: al relanzar, la rama tenía la inferencia y la plaza, y el saludo
estaba en el *worktree* de un sub-agente, nacido de `dev`; se integró con `cherry-pick`, sin conflictos (hallazgo 64). La
tanda 2 (PR #38) quedó integrada en `dev` sin squash. **Con esta tanda `grpc` está entero y F3-03 se cierra.**
- **Tanda 3 en verde** (T3.14 + T3.23; 12 commits, `185dcbc` … `c7a61a1`) en `internal/modulos/edge/grpc/`: `inference.go`,
  `inference_result.go`, `inference_dispatch.go` (los tres trozos de la `inference.go` vieja, E-13), `plaza.go` y
  `greeting.go` 🔒. Cableados los tres `// TODO(F3-03 tanda 3)`: el `InferenceResult` se entrega *inline* en `route`
  (hallazgo 47 resuelto), `greetIfNeeded` es la última llamada del job del latido y `cancelSessionInfers` corre al caer el
  stream. El mayor de producción de la tanda, `inference.go` (302 l); ningún `.go` pasa de 500.
- **Literal y ADR-0048**: los tres tests del literal (`TestPassiveSessionNoticeGolden`, `…SaysTheThreeThingsAndNothingElse`,
  `…MatchesItsSourceDocument`, que **falla** si no encuentra el `.md`) y la pareja ADR-0048
  (`inference_dispatch_affinity_test.go` + `plaza_test.go`) verdes. `literal-aviso-sesion-pasiva.md` intacto.
- **Gates** sobre `231c74b` (toolchain `go1.26.5` / `v2.12.2`): `make ci-local` rc=0 (145 paquetes `ok`, lint 0 issues) ·
  `go test -v` de `grpc` rc=0, **342 tests, 0 SKIP** · 0 SKIP en `modulos`, `nucleo` y `arranque` · `-race -count=5` de
  `grpc` rc=0 · `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0` · `grep pendiente.Implementar internal/modulos/edge`
  → 0 · cobertura (informe): `FICHEROS_EVALUADOS=129 · POR_DEBAJO=7`, ninguno de `edge` · código viejo, `go.mod` y
  `fronteras_test.go` intactos.
- **Mutantes** (a mano, dos sub-agentes en copias fuera del árbol): 323 escritos; **10 huecos cerrados con 8 tests**
  (`3758144` … `231c74b`), 7 vivos equivalentes (hallazgo 55) y un hueco de la tanda 2 cerrado de paso (`db2d5bd`,
  hallazgo 59).
- ✅ **Hallazgo 56, resuelto el 2026-10-06** (decisión de Jhoan; rama `reorg/f3-decisiones-abiertas`): `edb08bf`, test con
  `testing/synctest` que acota la espera a plazo + un margen (el mutante `+ 2*s.inferGrace` cae: «duró 40s, se esperaba
  35s»), y `04eedd7`, el presupuesto se calcula una sola vez.
- ✅ **Hallazgo 45, resuelto el 2026-10-06 por D-F3-9** (misma rama): `1f96651`, el cierre del stream viejo no deja de rastrear
  una sesión que ya reconectó. 🔴 El nuevo se aparta del viejo.
- ✅ **Hallazgo 40, resuelto el 2026-10-06 por D-F3-10** (misma rama): `ce6911a`, `RevokeTenant` avisa también a los Edge vivos
  que `fleet` no lista. 🔴 El nuevo se aparta del viejo. El `Ping` sin `*SendError` queda aceptado.
- ✅ **Hallazgo 38, resuelto el 2026-10-06** (misma rama): `6446e10`, «la cola llena frena» y «`drain` espera» pasan a burbuja de
  `testing/synctest` y sus mutantes caen siempre. **No queda ninguna 🟡 de F3 por decidir entre los hallazgos 38, 40, 45 y 56.**
- ✅ **Menores, 2026-10-06**: hallazgo 21 aceptado (`defaultProfile` en `scanSession`, inobservable) y hallazgo 32 comprobado
  (el Edge manda `self_pn` ya limpio, `domain.SelfPNFromJID` en `main` de `wapp-edge-agent`).
- **No corrido**: `make test-procesos`, `make test-integration` y mTLS real (son de F3-05; `grpc` nuevo aún no está
  conmutado: lo construye F3-04).
- **Siguiente paso: F3-04** (`bridge_gateway`, cara nueva de `edge` y conmutación). `main` sin tocar.

**F3-03 (segunda sesión) · F3, `grpc` — tanda 2 de 3 (2026-10-05, 💻, rama `reorg/f3-03-grpc-tandas-2-3` desde `origin/dev` @ `cbc5736`).**
Sesión completa (D-R-8), ≈ 75 min de pared (D-R-6; de ellos ≈ 65 de tres sub-agentes en serie). **Se paró en el punto
limpio tras la tanda 2 y se relanza para la 3.** La tanda 1 (PR #37) quedó integrada en `dev` sin squash.
- **Tanda 2 en verde** (T3.13 + T3.22; 11 commits, `f0019af` … `84c83f6`) en `internal/modulos/edge/grpc/`: `diagnostics.go`,
  `readiness.go`, `config_push.go`, `auth.go`, `connect_heartbeat.go`, `connect_route.go`, `connect_session.go` (completo) y
  `connect.go`. **`Connect` ya atiende el stream.** El mayor de producción, `auth.go` (432 l); ningún `.go` pasa de 500.
- **Gates** (toolchain `go1.26.5` / `v2.12.2`): `make ci-local` rc=0 · `go test -v` de `grpc` rc=0, **249 tests, 0 SKIP**
  (0 SKIP en todo `edge`) · `-race -count=100` y `GOMAXPROCS=1 -race -count=10` estables · `make vet-pendiente` y
  `make test-pendiente` rc=0 · lint 0 issues en los 11 commits · código viejo, `go.mod` y `fronteras_test.go` intactos.
- **Mutantes** (a mano): 221 escritos; 219 muertos, **2 vivos equivalentes** (hallazgo 52). Mueren todos los obligatorios de
  T-5 (canal de control fuera del Registry) y T-6 (`UNSPECIFIED` elegible).
- **Hallazgo 32 afirmado sin corregir** (hallazgo 46): `persistSelfPn` no limpia el JID; corpus de 29 casos.
- 🔴 **Falta**: tanda 3 (`inference` ×3, `plaza`, `greeting` con el literal). Los tres tests del literal y la pareja ADR-0048
  **no existen aún**. Hasta entonces un `InferenceResult` se pierde en `route` (hallazgo 47). Lo que la tanda 3 hereda está
  en la nota del bloque F3-03 de `plan/F3-edge/tareas.md` y en tres `// TODO(F3-03 tanda 3)`.
- 🟡 **Por decidir (Jhoan)**: hallazgo 45 (el seguimiento por Edge no distingue streams: una reconexión rápida deja la sesión
  viva sin rastrear) del [README de F3](plan/F3-edge/README.md), con los 45–54. Siguen abiertos el 38 y el 40.

**F3-03 (primera sesión) · F3, `grpc` — tanda 1 de 3 (2026-10-04, 💻, rama `reorg/f3-03-grpc` desde `origin/dev` @ `c851591`).**
Sesión completa (D-R-8), ≈ 70 min de pared (D-R-6; de ellos ≈ 44 del sub-agente de la tanda 1). **No cupo en 90 min: se paró
en el punto limpio tras la tanda 1 y se relanza.**
- **R-S4 corregida** en `plan/F3-edge/diseno.md` §3 (`0117221`, hallazgo 10): el `Registry` no serializa el `Send`; lo hace
  `grpc.streamSender` (R-G12).
- **Inventario E-12 de `grpc` aprobado por Jhoan** antes de escribir código (`b1a408b`), en
  [`plan/F3-edge/arquitectura.md`](plan/F3-edge/arquitectura.md) §1.1.c: 13 ficheros viejos → 20 de producción (`connect` en 4,
  `inference` en 3 y `send` en 3, E-13); niveles **mixtos por trozo**; nombres E-11 (`Clase*` → `Class*`, `Motivo*` →
  `Reason*`, 4 centinelas), y **no** se renombran `StreamCaido()`, `Motivo()` ni `PlazaDe` (duck-typing desde código vivo).
- **Tanda 1 en verde** (T3.12 + T3.21; 11 commits, `dd4f984` … `a0baf00`) en `internal/modulos/edge/grpc/`: `types.go`,
  `server.go`, `receipt_sink.go`, `worklane.go`, `send.go` + `send_ack.go` + `send_revoke.go`, y `connect_session.go` con solo
  `sessionsForEdge` (adelantado). 8 ficheros de producción (1.548 l) y 14 de test; el mayor, `worklane.go` (426 l).
- **Gates** (toolchain `go1.26.5` / `v2.12.2`): `make ci-local` rc=0 · `go test -race` de `grpc` rc=0, 85 tests, **0 SKIP**
  (`-count=100` y `GOMAXPROCS=1 -count=10` estables) · `PENDIENTES=0`, `ROJOS=0` · lint 0 issues · sin imports de
  `internal/{gateway,flujos,llmvia}` · código viejo y `go.mod` intactos.
- **Mutantes** (a mano): 125 escritos; 120 muertos, **3 vivos equivalentes**, 2 que no compilan (hallazgo 44).
- 🔴 **Falta**: tanda 2 (`connect` ×4, `auth`, `config_push`, `readiness`, `diagnostics`) y tanda 3 (`inference` ×3, `plaza`,
  `greeting` con el literal). `Connect` devuelve `Unimplemented` hasta la tanda 2. Los tres tests del literal y la pareja
  ADR-0048 **no existen aún**. Lo que la tanda 2 hereda está en la nota del bloque F3-03 de `plan/F3-edge/tareas.md`.
- 🟡 **Por decidir (Jhoan)**: hallazgos 38 (negativas de concurrencia con muerte probabilística del mutante) y 40
  (`RevokeTenant` solo avisa a los Edge que lista `fleet`) del [README de F3](plan/F3-edge/README.md), con los 32–44.

**F3-02 · F3, `fleet` y `filtercfg` (2026-10-04, 💻, rama `reorg/f3-02-fleet-filtercfg` desde `origin/dev` @ `809345b`).**
Sesión completa (D-R-8), ≈ 55 min de pared (D-R-6; de ellos ≈ 36 de ejecución tras aprobar el inventario).
- **Inventario E-12 aprobado por Jhoan** antes de escribir código (`d7323b7`), en
  [`plan/F3-edge/arquitectura.md`](plan/F3-edge/arquitectura.md) §1.1.b: `repository_postgres.go` medido (11 métodos de una
  sentencia, sin transacción) → nivel **mixto por trozo**; driver falso **local**; `Memoria` **porta tal cual** al doble viejo.
- **`fleet`, `fleethelpertest` y `filtercfg`, en verde** en `internal/modulos/edge/` (12 commits, `532e62f` … `b86dd62`, más
  `docs(edge)`): `fleet.go` (421 l, sin partir), `repository_postgres.go` partido en 5 por tema (E-13), la suite
  `ContratoRepository` con `Montaje{Repository, SeedTenant, Profiles}` y su doble `Memoria`, `slowrepo.go` y `filtercfg.go`.
  Tres sub-agentes: dos en serie sobre la rama y uno en un *worktree* fijado en `147d178`, integrado con `cherry-pick` y borrado.
- **Corpus de equivalencia del índice ciego**: 26 entradas con casos adversarios; **ninguna diverge** entre la regla vieja y
  `nucleo/contact.Normalize`. SQL byte a byte; los dos centinelas y `"filters"`, literales.
- **Gates** (toolchain `go1.26.5` / `v2.12.2`): `make ci-local` rc=0, lint 0 issues · `go test -race -v` de `fleet` + `filtercfg`:
  319 PASS, **0 SKIP**, 0 FAIL (y 0 SKIP en `modulos` + `nucleo` + `arranque`) · `PENDIENTES=0`, `ROJOS=0` · cobertura
  (informe): `FICHEROS_EVALUADOS=109`, `POR_DEBAJO=7` (los mismos 7, ninguno de `edge`) · código viejo y `go.mod` intactos.
  **No corrido**: la suite de `fleet` contra Postgres y `make test-procesos` (F3-05, D-F3-8).
- **Mutantes** (a mano): 31 en `_selfpn` y `_greeting`, 9 en `filtercfg`, 10 contra `Memoria`; **0 vivos**. 🟡 Una línea se
  aparta del viejo sin efecto observable (hallazgo 21 del [README de F3](plan/F3-edge/README.md), con los hallazgos 18–31).
- **Siguiente paso: F3-03** (`grpc`). `main` sin tocar.

**F3-01 · F3, inventario E-12 y hojas (2026-10-04, 💻, rama `reorg/f3-01-inventario-hojas` desde `origin/dev` @ `8896f13`).**
Sesión completa (D-R-8), ≈ 55 min de pared (D-R-6; inicio estimado, sin marca exacta). **F3 arranca.**
- **Inventario E-12 aprobado por Jhoan** antes de escribir código (`3ae565c`): 21 ficheros viejos de 7 paquetes, fichero a
  fichero, en [`plan/F3-edge/arquitectura.md`](plan/F3-edge/arquitectura.md) §1.1.a. Decisiones nuevas: **D-F3-7** (adaptadores
  Postgres de una sentencia, nivel medio) y **D-F3-8** (las pasadas contra Postgres van en F3-05, en `test/procesos`).
- **Los 7 paquetes hoja, en verde** en `internal/modulos/edge/{session,inferstats,receipts,ingest,diagnostics,lease,enroll}`
  (29 commits, `bcd0f4e` … `8a4dd34`), escritos por tres sub-agentes en *worktrees* fijados en `3ae565c` e integrados con
  `cherry-pick`; los *worktrees* están borrados. Los dobles en memoria viven en `<paq>helpertest` (D-F3-1) y las **6 suites**
  (`receipts.Store`, `ingest.Deduper`, `diagnostics.Store`, `lease.Repository`, `enroll.CodeStore`, `enroll.EdgeCertRepository`)
  pasan contra ellos.
- 🔒 **`lease`**: `lease.go`, `repository_postgres.go` y `signingkey.go` comparados contra el viejo sin comentarios ni blancos:
  **0 líneas de código añadidas y 0 quitadas**; en `repository.go` solo falta el doble (76 líneas). SQL byte a byte, con la
  escritura de `public.tenants.revoked_at` (D-F3-4). `enroll`: 0 líneas añadidas en sus 6 ficheros.
- **Gates** (toolchain `go1.26.5` / `v2.12.2`): `make ci-local` rc=0, lint 0 issues · `go test -race -v` de `edge` + candados:
  390 PASS, **0 SKIP**, 0 FAIL · `PENDIENTES=0`, `ROJOS=0` · cobertura (informe): `FICHEROS_EVALUADOS=100`, `POR_DEBAJO=7`
  (los mismos 7 de antes, ninguno de `edge`) · código viejo y `go.mod` intactos · `edge` no importa nada viejo.
  **No corrido**: las suites contra Postgres y `make test-procesos` (F3-05, D-F3-8).
- **Mutantes**: 132, 131 muertos; el vivo es equivalente (hallazgo 15 del [README de F3](plan/F3-edge/README.md), con los
  otros 16 hallazgos).
- **Siguiente paso: F3-02** (`fleet` y `filtercfg`). `main` sin tocar.

**F2-05 · F2, cierre local (2026-10-04, 💻, rama `reorg/f2-05-cierre-local` desde `origin/dev` @ `bfd31ce`).** El PR #32
(F2-04) está integrado en `dev` (merge `bfd31ce`). Sesión de cierre, sin traspaso; cuatro sub-agentes (tres de mutantes en
copias desechables fuera del repo, uno para los tests de `canje.go`). **F2 queda cerrada.** Llega a `dev` por PR, **sin squash**:

- **Decisiones de la sesión (Jhoan)**: el «e2e de `cmd/server-modular`» de T2.32 no existe (F0 `diseno.md` §5.3) y se cumple
  con los procesos contra el binario nuevo más el arranque real (hallazgo 43); de los huecos de P2 frente a T2.33, matriz
  regla → prueba y al proceso solo la migración 0038 (hallazgo 44).
- **Gates repetidos con la toolchain fijada** (go1.26.5, lint v2.12.2, rc sin pipe), primero sobre `bfd31ce`, idénticos a
  los del PR #32 (`GATE_RC=0`, 113 `ok`), y al final sobre `73b4541`: `make ci-local` `GATE_RC=0` (113 `ok`, lint 0 issues)
  · `make test-pendiente` `PENDIENTES=0 · ROJOS=0` · código nuevo `-v` 2.709 PASS, **0 SKIP** · gate del arranque
  (`Mudanzas|Huella|PlatformPermissions|Cableado|BootWiring`) rc=0, 0 SKIP · R2.5.d por su cláusula (de lo viejo, solo
  `internal/entitlements` e `iam/{domain,ports/in,transport/http}`; en `internal/arranque`, solo `bridge_iam.go`) ·
  `git diff --stat origin/dev -- cmd/server internal/bootstrap` vacío.
- **T2.33 = T9.23 · suites contra Postgres** (testcontainers): `-run Contrato` rc=0, **149 PASS**, 0 FAIL, 0 SKIP con
  `WAPP_PROCESOS_BINARIO=viejo` y con `nuevo`. Los casos de las suites son los mismos en memoria y en Postgres:
  **sin divergencias**.
- **Mutantes del nivel complejo** (a mano; unitarios y Postgres): **131 sembrados · 124 muertos · 5 equivalentes · 2 vivos
  🟡**. Sobrevivían 8: cinco eran nuevos y se arreglaron sin tocar producción (`40d1582` M2M: esquema `Bearer` y timeout por
  defecto; `d243f15` `platformadmin`: el `ORDER BY` de la bandeja; `7f2b745` `canje.go`: el `Rollback` y el `now()` de la
  base; `73b4541`, un `errcheck` del montaje), uno resultó equivalente (`8c53ecf`) y dos son del hallazgo 29, re-medido: de sus cuatro de carrera, tres mueren ya
  **por el proceso P2**; siguen vivos el `UPDATE` sin `revoked_at IS NULL` y `Add` sin su transacción (0 de 6). Sin decisión,
  no se resolvió (hallazgo 45).
- **Procesos de F9**: `procesos(canje-permisos)` `8677404` (P2 afirma el desenlace de la 0038). `make test-procesos`:
  viejo `RC=0 · PASS=667 · FAIL=0 · SKIP=0`, nuevo `RC=0 · PASS=667 · FAIL=0 · SKIP=0` (sobre `73b4541`). 🟡 Una pasada
  anterior dio rojo contra el **viejo** en `TestP5_OwnerInbox/sugerencia_con_plazo` (bandeja, no `acceso`; intermitente:
  verde al repetir y en tres corridas sueltas; hallazgo 50). Y `ci-local` dio `GATE_RC=2` una vez por un `errcheck` propio,
  corregido en `73b4541` (hallazgo 51).
- **T2.32 · arranque real de `cmd/server-modular`** (Postgres `17-alpine` efímero en Docker y puerto libre, nunca el de la
  máquina; modo dual apagado): **9/9** fases, `/healthz` 200, 0 `ERROR`, SIGINT → `servidor detenido limpiamente`.
  Peticiones: `POST /api/v1/auth/exchange` → **503** · `GET /api/v1/entitlements` → 200 con `cache_ttl_seconds` **60** (plan
  `basic`) · `POST /admin/tenants` con token `tenant_admin` → **403**, y ninguna empresa creada.
- ⏱️ **D-R-6, minutos**: ≈ **47 min** de pared (19:07–19:54 Z); sub-agentes de mutantes 12, 18 y 23 min en paralelo;
  el resto, gates, procesos, arranque real y cierre.
- **Hallazgos 43–51 e informe de fase** en el [README de F2](plan/F2-acceso/README.md). 🟡 abiertas, que no bloquean: 13, 14,
  18, 29, 34, 36 y la 50 nueva.
- **No corrido**: `make test-integration` (la integración vieja: esta sesión no tocó código viejo ni compartido) y UAT.
- **Siguiente paso: F3-01** (🌐, `edge`: inventario y hojas). `main` sin tocar.

**F2-04 · F2, `bridge_iam.go`, conmutación y rutas (2026-10-04, 🌐, rama `reorg/f2-04-bridge-conmutar-rutas` desde
`origin/dev` @ `5547c6e`).** El PR #31 (F2-03) está integrado en `dev` (merge `5547c6e`). Sesión completa: cuatro
sub-agentes en *worktrees* (bridge y rojo de `apipublica` en paralelo; luego el verde de `apipublica` y la conmutación en
paralelo, ésta esperando al verde para su gate), integrados con `cherry-pick`. Llega a `dev` por PR, **sin squash**:

- **Decisiones de la sesión (Jhoan)**: `apipublica` de nivel **medio** (fila nueva en `diseno.md` §1.1); **R2.5.d** se
  verifica por su cláusula (hallazgo 37); gate de T2.31 ampliado con `BootWiring` (test en inglés, E-11).
- **T2.28** `bridge_iam.go` (simple, una pasada): `94f2d26`. `authenticatorBridge` y `auditorBridge` sobre los servicios
  nuevos, los 4 centinelas con `bridgeError`, `VerifyResult` incluido, nil de verdad (hallazgo 38).
- **T2.30 = TX.5 + TX.6** (`apipublica`, medio): rojo `799d821` → verdes `3745079` (`response.go`, sin rojo), `52a5d09`
  (`chain` + `roleplane`), `e546523`, `8a11c8d`, `4f28f4f` (hallazgo 39). `Mount{Auth,RolePlane,Audit,Entitlements}`.
- **T2.31 = TX.7 + T2.29**: `8482dad`. Un solo `entitlements.NewPostgres` nuevo en todos los consumidores (también
  `Deps.Entitlements` viejo); `Deps.{Roles,Members,Invitations,Audit}` = nil; A1–A7, B1–B14 y C1–C2 por la cara nueva;
  J4–J11 inline con `platformadmin` nuevo sin alias; gateway viejo con `bridge_iam.go`; `FaseActual = 2`;
  `access_wiring_test.go` (`TestBootWiring_Access*`). `acceso` **no** entra en `Conmutados` (F3).
- **Gates** (toolchain fijada, rc sin pipe, sobre `8482dad`): `make ci-local` `GATE_RC=0` (113 `ok`, lint 0 issues) ·
  `-run 'Mudanzas|Huella|PlatformPermissions|Cableado|BootWiring' ./internal/arranque` rc=0, 0 SKIP (huella igual en
  `minimo` y `con-m2m`) · `go vet -tags pendiente ./...` y `-tags integracion` rc=0 · `make test-pendiente`
  `PENDIENTES=0 · ROJOS=0` · código nuevo `-v` 2.686 PASS, **0 SKIP** · `NewPostgres` en `internal/arranque` → 1 ·
  `git diff --stat origin/dev -- cmd/server internal/bootstrap` vacío · `go list -deps ./cmd/server-modular`: de lo viejo
  solo `internal/entitlements`, `iam/{domain,ports/in,transport/http}`, importados por `publicapi`, `flujos/{events,runtime}`,
  `reanalisis`, `gateway/grpc` y, en `internal/arranque`, solo `bridge_iam.go` · `cobertura-ficheros` (informe): `apipublica`
  87,5–100 %, `bridge_iam.go` 100 %.
- **Pre-chequeo contra Postgres** (Docker en la web, **no cierra**: cierra F2-05): suites `Contrato` con
  `WAPP_PROCESOS_BINARIO=nuevo` rc=0, **148 PASS**, 0 FAIL, 0 SKIP; procesos `TestP2_*` y `TestP10_Platform` contra el
  binario **nuevo** ya conmutado rc=0, **94 PASS**, 0 FAIL, 0 SKIP.
- ⏱️ **D-R-6, minutos con E-12**: ≈ **65 min** de pared (17:41–18:46 Z), de ellos ≈ 15 de plan; sub-agentes: bridge ≈ 8,
  rojo de `apipublica` ≈ 17, verde ≈ 10, conmutación ≈ 19 (en paralelo con el verde); gates y cierre ≈ 15.
- **Hallazgos 37–42** en el [README de F2](plan/F2-acceso/README.md) (R2.5.d, lo que el bridge adapta de más, tests del
  rojo acoplados, elecciones del contrato de la cara, C2 en las dos caras, caché de lint compartida).
- **No corrido**: la pasada que **cuenta** de suites y procesos contra los dos binarios y el e2e local (T2.32–T2.33,
  sesión F2-05 💻). Sin traspaso: F2-05 ya lo tiene en su ficha.
- **Siguiente paso: F2-05** (cierre local de F2). `main` sin tocar.

**F2-03 · F2, `entitlements/postgres.go`, `iam/infra/postgres`, `iam/transport/http` y `platformadmin` (2026-10-04, 🌐, rama
`reorg/f2-03-postgres-http-platformadmin` desde `origin/dev` @ `976d70c`).** Sesión completa. Cuatro sub-agentes en *worktrees*
en paralelo (entitlements, iam/infra/postgres, transport/http, platformadmin en dos fases: la segunda esperó al verde de
`memberships.go`), integrados con `cherry-pick`. Llega a `dev` por PR, **sin squash**:

- **Decisiones de la sesión (Jhoan)**: **D-F2-9**, lista cerrada de pares suite → adaptador en `ProcessImports` (la zona
  hexagonal de `iam`), `7da5367` → `a9a5fdf`; y alias `outhelpertest.{Invitation,Membership}` para que la pasada de `iam`
  contra Postgres no necesite `iam/domain` (`f812cfb`).
- **entitlements/postgres.go** (T2.4, complejo): `90b786b` → `f7d36eb` · suite contra Postgres `368d2fd`. `WithClock`.
  Mutantes 15 · 15 muertos · 0 vivos.
- **iam/infra/postgres** (T2.12, T2.24): `postgres.go` simple `c182a0a`; medios rojo → verde `6cd7dcc`→`95cb3e3`,
  `1e3648e`→`3183dd2`, `cdba8b0`→`a8660ed`, `e0b7dff`→`dc9abcc`, `a82e5d3`→`2851eab`; complejos `975d92b`→`c482ae4`
  (memberships) y `e72425d`→`798bbb3` (canje), con los **3 candados AST** portados (D-F2-1) y verdes; el viejo, verde sin
  tocarlo (D-F4-1). Las 7 suites contra Postgres `e87b552`. Mutantes: sin BD, todos muertos; con BD sobreviven 4 de carrera
  (hallazgo 29, 🟡).
- **iam/transport/http** (T2.13, T2.25): `http.go` sin rojo `00ed7e4`; rojos `9452ca0`, `1da40da`, `8792bd9`, `aa8a009`,
  `674e13e`; verdes `95aa600`, `dd1d345`, `65b4e52`, `9c3f5d6`, `75bd52d`. R-H1…R-H9; textos de diseño §5 byte a byte.
- **platformadmin** (T2.14, T2.15, T2.26): rojos de tipos `2cb4954`, `5299fd5` · `ports.go` + `platformadminhelpertest`
  `3b58eba` · rojos `49a2cf8`, `0600a15`, `681ece7` · verdes `20330c6`, `e500186`, `3706323`, `8a38c67`, `a7e72b4` ·
  `access_requests_postgres.go` ✚ `a4464e6` → `27028a8` · casos nuevos `7a32260` · suite contra Postgres `99336f3`.
  Mutantes 16/16 sin BD y 13/13 contra Postgres.
- **Gates** (toolchain fijada, rc sin pipe, sobre `99336f3`): `make ci-local` `GATE_RC=0` (111 `ok`, lint 0 issues) ·
  `make vet-pendiente` rc=0 · `go vet -tags integracion ./...` rc=0 · `make test-pendiente` `PENDIENTES=0 · ROJOS=0` ·
  `pendiente.Implementar` en `acceso` → **0** · código nuevo `-v` 2.445 PASS, **0 SKIP** · `cobertura-ficheros` (informe)
  67 evaluados, 8 por debajo: los 7 adaptadores Postgres nuevos (fuera del umbral) y el previo de `contact`.
- **Pre-chequeo contra Postgres** (Docker en la web, **no cierra**: cierra F2-05): `contact`, `entitlements`, `iam` (7) y
  `platformadmin` con `WAPP_PROCESOS_BINARIO=viejo` → rc=0, **148 PASS, 0 FAIL, 0 SKIP**, sin divergencias memoria ↔ Postgres.
- ⏱️ **D-R-6, minutos con E-12**: ≈ **90 min** de pared (14:59–16:29 Z), casi todo esperando a sub-agentes: entitlements
  ≈ 14 min, transport/http ≈ 22, iam/infra/postgres ≈ 47 (el cuello), platformadmin ≈ 39 + 23 (fase 2), gates y cierre ≈ 20.
  37 ficheros de producción y de suite.
- **Hallazgos 26–35** en el [README de F2](plan/F2-acceso/README.md) (D-F2-9, dónde viven las pasadas contra Postgres,
  mutantes de carrera, *driver* SQL falso para Tx, huecos de alcance por empresa, orden forzado por los tipos).
- **No corrido**: la pasada que **cuenta** de las suites contra Postgres y los procesos de acceso contra los dos binarios
  (T2.32–T2.33 = T9.23, sesión F2-05 💻). Sin traspaso: nada de F2-03 queda a medias; F2-05 ya lo tiene en su ficha.
- **Corte por tamaño** (a petición de Jhoan, tras abrir el PR #31): los 6 ficheros de F2-03 de más de 500 líneas
  (`3aacb27`, `3147c55`, `23ab8e1`, `de3f593`, `c1a4acc`, `5a91467`) y los 3 de F2-02 de más de 600 (`f26af82`, `4f6e178`),
  partidos por tema solo moviendo declaraciones. **Regla nueva**: D-R-7 y `05` E-13 (500, tolerancia 600, estricto
  por encima) con candado `internal/modulos/file_size_test.go` (`181458d` → `bec5116`); 5 ficheros de otras fases en
  lista cerrada con techo (hallazgo 36, 🟡).
- **Siguiente paso: F2-04** (`bridge_iam.go`, conmutación y rutas). `main` sin tocar.

## Avance de la ejecución

| Fase | Estado | Último bloque cerrado | SHA |
|---|---|---|---|
| F0 | ✅ cerrada (2026-09-30) | F · cierre local (T0.22–T0.25) | A: `98e806d`, `de04088`. B: `d7600d3`, `f3b322c`, `d74dd7f`, `d05ac3a`. C: `3040e82`, `2c2bbd6`, `b2ecfce`, `65d4bc0`, `e61567e`, `681d84e`, `d48e319`, `42884fb`, `b522b0f`, `ca462a6`, `3e85144` (en `dev` @ `80807ba`). D: `d64dbbf`, `a953834`, `c7ae487`, `141d960`, `fde5849`, `61ce04b` (en `dev` @ `d3deb27`). E: `8096232`, `5a11f5b`, `6d83620`, `b65b788`, `5305134`, `de0c29b`, `9dcf7e8`, `7b7e01f`, `dd1e2bd`, `15223ff` (en `dev` @ `835a7be`, PR #17). F: T0.22 y T0.23 sin commit (evidencia en el `CERRADO` del traspaso), T0.24 `835a7be` verificado, T0.25 `d3b3f3f` |
| F9-A (adelantado) | ✅ cerrado (2026-10-01): escrito en la web (F9-01), cerrado en local (F9-02); H-1 (intermitencia de P0) diferida a F6 | T9.1–T9.12 | `a374cdb`, `37c7db7`, `f300aff`, `b5f1601`, `576ba9a`, `2e2ecc1`, `7c63d9e`, `660947d`, `fa03e6b`, `5519343`, `10179d6`, `6ee1c5e` (en `dev` por el merge `af7b8e9`, PR #18); el cierre local es solo documental, en **tres** commits: `ac8ac5f` (el cierre), `79c7160` (su SHA) y `77df20f` (H-1 diferida a F6: ahí vive D-F6-7) |
| Recalibración (docs) | ✅ hecha (2026-10-03) | specs F2–F10, FX y sesiones | commit `docs(reorganizacion-modular): recalibración tras el piloto` |
| F1-06 (ajustes previos a F2) | ✅ hecha (2026-10-03) | A1–A6 | `0689b4e`, `1622231`, `0a91857`, `cccee37`, `9001720`, `7937772` |
| F9-B (adelantado) | ✅ **B1 y B2 hechos** (2026-10-03, F9-03 y F9-04) | B2 · T9.17–T9.21, T9.35 (P4–P8, P10) y T9.22 | B1: `679ea52`, `052089e`, `8febd52`, `250916a` (en `dev` por el PR #27, merge `7b092d5`). B2: `e3fc0de`, `49d6d9e`, `39c38be`, `9da68f7`, `0a2762d`, `56b99f6`, `2bb7989` (rama `reorg/f9-b2`, por PR a `dev`) |
| F1 | ✅ **cerrada** (A–D en `dev`: PR #19, #23, #25; cierre local F1-04 el 2026-10-02; **parada resuelta el 2026-10-03**) | D · cierre local e informe (T1.17–T1.19) | **D**: T1.17–T1.19 sin commit de código sobre `ddcf7de`, cierre documental `d5228ac` · T1.14 `09f4b72` · T1.15 `0c2bddf` (+ `a62abea`) · T1.16 `ce98595` · T1.1 `afa63f3` · T1.2 `b37a8c8` · T1.3 `d915d41` (+ `b001c35`) · T1.3b `68897a8`, `776d6a2` · T1.4 `8f2a4db` · T1.5 `89b223b` · T1.6 `32b7bfb` · cierre del bloque `b9dd1e7` · tras el cierre: E-11 `8365132` y el troceo de `contacttest/contrato.go` `7069532`; sobre `origin/dev` @ `77df20f`, rama `reorg/f1-a-contratos-rojo`. Gate `ci-local` `GATE_RC=0` (86 líneas `ok`, 0 issues); `make test-pendiente` `PENDIENTES=11` `ROJOS=4`; 0 SKIP · **B**: decisiones `ccc9a6b` · T1.8 `9e8f740` · T1.9 `8e7a891` · T1.10 `222c4c8` · T1.11 `8307afb` · D-F1-7 `4bbd138` · T1.13 `4bc398d` (sobre `origin/dev` @ `5847ad4`). Gate `GATE_RC=0` (88 `ok`, 0 issues); `PENDIENTES=0 · ROJOS=0`; cobertura 97,6 · 100 · 95,6 · 92,6 % y Postgres exento (31,1 %); 0 SKIP; pre-chequeo de T1.13 viejo y nuevo 20 PASS |
| F2 | ✅ **cerrada** (2026-10-04, F2-05 💻; F2-01…F2-04 en `dev`, PR #32 = merge `bfd31ce`) | F2-05 · T2.32–T2.33 (= T9.23) (`8677404`, `40d1582`, `8c53ecf`, `d243f15`, `7f2b745`, `73b4541` y el cierre documental, rama `reorg/f2-05-cierre-local`, PR #33; las 🟡 decididas, PR #34; los dos en `dev` por el merge `4f0ed06`); F2-04 · T2.28–T2.31 (= TX.5–TX.7) (`94f2d26`…`8482dad`, rama `reorg/f2-04-bridge-conmutar-rutas`, por PR a `dev`); F2-03 · T2.4, T2.12–T2.15, T2.24–T2.27 (`7da5367`…`99336f3`, rama `reorg/f2-03-postgres-http-platformadmin`, por PR a `dev`); F2-02 · T2.10–T2.11, T2.16, T2.22–T2.23 (F2-02: `5a686b8`…`f0d777e`, rama `reorg/f2-02-usecase-identity`, por PR a `dev`); F2-01 · T2.1, T2.34, T2.2–T2.3, T2.5–T2.9, T2.17–T2.21 | `1d73874`, `f46a107`, `01950de`, `43704f1`, `3742090`, `30345e7`, `e64cc3a`, `9b4e407`, `2868e61`, `2776d82`, `8290814`, `13d4171`…`38a8d0b`, `4e97ee3`, `1d3b10b`, `354f060` (rama `reorg/f2-01-inventario-hojas`, por PR a `dev`) |
| F3 | ✅ **cerrada** (2026-10-06, F3-05 💻: `8121564` … `876b096`, rama `reorg/f3-05-cierre-mtls`, por PR a `dev`) (F3-01 y F3-02 hechas e integradas el 2026-10-04 💻: PR #35 y PR #36, `dev` @ `c851591`; **F3-03, tanda 1 de 3** integrada, PR #37, `dev` @ `cbc5736`; **tanda 2 de 3** integrada, PR #38, `dev` @ `ec236b3`; **tanda 3 de 3, F3-03 hecha**, integrada, PR #39, `dev` @ `414b31b`; **F3-04 hecha** el 2026-10-06, rama `reorg/f3-04-bridge-conmutar-rutas`, por PR a `dev`: `dd4cbd2` … `0ebb743`; falta F3-05) | F3-01 · T3.1–T3.9, T3.15–T3.18 · F3-02 · T3.10, T3.11, T3.19, T3.20 · F3-03 · T3.12–T3.14, T3.21–T3.23 · F3-04 · T3.24–T3.26, T3.28 (T3.27 `[~]`) | `3ae565c` y `bcd0f4e` … `8a4dd34` (F3-01) · `d7323b7` y `532e62f` … `b86dd62` (F3-02, 12 commits de `edge`) · `b1a408b` y `dd4f984` … `a0baf00` (F3-03 tanda 1, 11 commits de `edge`) · `f0019af` … `84c83f6` (F3-03 tanda 2, 11 commits de `edge`) · `185dcbc` … `231c74b` (F3-03 tanda 3, 20 commits de `edge`) |
| F4 | ✅ **cerrada** (2026-10-07, F45-03 💻: `ddb8baa`, `59a3e6b`, rama `reorg/f45-03-cierre`, por PR a `dev`; `inferencia` fuera de `Conmutados` hasta F8) (F45-02 hecha el 2026-10-07 💻: `bridge_inferencia.go`, el arranque nuevo cablea `inferencia`, 4 rutas mudadas, `FaseActual = 4`; rama `reorg/f45-02-conmutar-inferencia-catalogo`, por PR a `dev`; `inferencia` fuera de `Conmutados` hasta F8) (F45-01 hecha el 2026-10-07 💻: inventario E-12 aprobado e `internal/modulos/inferencia` entero en verde, **sin conmutar**; rama `reorg/f45-01-inventario-inferencia`, por PR a `dev`) | F45-03 · T4.29–T4.31 (= T9.25) · F45-02 · T4.10, T4.24–T4.28 (= TX.12–TX.14) · F45-01 · T4.1–T4.9, T4.11–T4.23 | `ddb8baa`, `59a3e6b` (F45-03) · `65f5348`, `fee3ed2`, `c619013`, `23a7d92`, `98b24c5`, `c2f469a`, `0e532e4`, `75298b8` (F45-02) · `2783172`, `bccfa4b` … `b8ae091` (rojo), `24c0782` … `24f6450` (verde) (F45-01) |
| F5 | ✅ **cerrada** (2026-10-07, F45-03 💻: sin commit de código, T9.26 sobre `59a3e6b`; rama `reorg/f45-03-cierre`, por PR a `dev`; `catalogo` fuera de `Conmutados` hasta F7) (F45-02 hecha el 2026-10-07 💻: `conversacion/model`, `catalogo`, `catalogimport` e `indice` sin etiqueta `pendiente`; `FaseActual = 5`; ninguna ruta ni cableado; `catalogo` fuera de `Conmutados` hasta F7; rama `reorg/f45-02-conmutar-inferencia-catalogo`, por PR a `dev`) (inventario E-12 aprobado en F45-01, 2026-10-06; D-F5-1 = B) | F45-03 · T5.20–T5.21 (= T9.26) · F45-02 · T5.2–T5.19 (= TX.15) · F45-01 · T5.1 | `523ef63` … `13e869f` (F45-02, 21 commits) · `5ba9fed` (F45-01) |
| F6 | ✅ **cerrada** (2026-10-08, F6-06 💻: sin commit de código, T6.27 = T9.27 sobre `68e68a4`; rama `reorg/f6-06-cierre-local`, por PR a `dev`; `solicitudes` fuera de `Conmutados` hasta F8) (F6-05b hecha el 2026-10-08 💻: **el arranque nuevo cablea `solicitudes` y muda G1–G18** (51 rutas en la cara nueva, `FaseActual = 6`, huella igual, INV-1 sin etiqueta, `PENDIENTES=0 · ROJOS=0`; `solicitudes` fuera de `Conmutados` hasta F8; centinela de H1, D-F6-13); `79f274e`, rama `reorg/f6-05b-conmutar-solicitudes`, por PR a `dev` tras el de F6-05a (#52). **Falta F6-06**, el cierre local. Antes, F6-05a hecha el 2026-10-08 💻: la cara HTTP de solicitudes en verde en `internal/apipublica`, **sin montar** (6 `Mount*`, 19 ficheros de producción, suite y doble de telemetría), D-F6-12, `PENDIENTES=0 · ROJOS=1`; `2cc4cde` … `6675927`, rama `reorg/f6-05a-cara-solicitudes`, por PR a `dev`. **Falta F6-05b**: cablear y conmutar G1–G18, T6.24–T6.25. Antes, F6-04 hecha el 2026-10-08 💻: `quotetext`, `telemetria`, `integrations` y `crmpush` en verde, módulo sin pendientes, `PENDIENTES=0 · ROJOS=1`; rama `reorg/f6-04-quotetext-telemetria-integrations-crmpush`, por PR a `dev`. Antes, F6-03 el 2026-10-08 💻: `intakes` entero en verde, `PENDIENTES=0 · ROJOS=1`; rama `reorg/f6-03-intakes-almacenes-acciones-candados`, por PR a `dev`. Antes, F6-02 el 2026-10-08 💻: `intakes` entero con contrato y test, 9 tipos puros en verde, `PENDIENTES=108`; rama `reorg/f6-02-intakes-contratos-y-tipos`, por PR a `dev`. Antes, F6-01 el 2026-10-07: inventario E-12 aprobado, `sigv1`, `intakes/note.go` y `tenantvars` en verde) | F6-01 · T6.1–T6.5, T6.14 · F6-02 · T6.6–T6.8, T6.15 · F6-03 · T6.9, T6.16–T6.18 · F6-04 · T6.10–T6.13, T6.19–T6.21 | `27a7924`, `98f9220`, `571482b`, `8c7ce8b`, `0108667`, `177f529`, `8cb2129`, `06879cb` · F6-02: `113912e` … `c366c68` · F6-03: `e8f1e10` … `4fcccc6` · F6-04: `31b9343` … `af68fe3` |
| F7 | ✅ **cerrada** (2026-10-09, F7-05 💻: `30b0cf4`, `51d1bbb`, `fb33953`, `ca222ed`, `b0056a4`, `a1a19ed` —estos dos, D-F7-12— y `3d5fa59`, T7.27 = T9.28; rama `reorg/f7-05-cierre`, por PR a `dev`; `captacion` fuera de `Conmutados` hasta F8) (F7-04 hecha el 2026-10-09 💻: H1, E1 y E2 por la cara nueva, el arranque nuevo cablea `captacion` con `bridge_captacion.go`, `FaseActual = 7` (54 rutas), `catalogo` en `Conmutados`, huella igual; rama `reorg/f7-04-cara-http-y-conmutar`, PR #58, merge `53e8f51`) (2026-10-09, F7-03 💻: `pipeline`, `intakeahead` y `reanalisis` en verde, puente a `flujos/events` declarado, pendientes del módulo = 0; rama `reorg/f7-03-pipeline-reanalisis`, PR #57) (2026-10-08, F7-02 💻: `stages` en verde y el puente a `flujos/store` declarado; rama `reorg/f7-02-stages`, por PR a `dev`) (F7-01 💻: inventario E-12 aprobado y hojas en verde — `evidence`, `anclaje`, `intake`, `intentcfg`, `casebank`; rama `reorg/f7-01-inventario-y-hojas`, PR #55, merge `4cd9cfb`) | F7-05 · T7.27–T7.29 (= T9.28) · F7-04 · T7.21–T7.26 (= TX.19–TX.21) · F7-03 · T7.10–T7.13, T7.18–T7.20 · F7-02 · T7.7–T7.9, T7.16–T7.17 · F7-01 · T7.1–T7.6, T7.14–T7.15 | `30b0cf4`, `51d1bbb`, `fb33953`, `ca222ed`, `b0056a4`, `a1a19ed`, `3d5fa59` (F7-05) · `a688ca4` … `94f0648` (F7-04) · `7cb66e2` … `69d142e` (F7-03) · `7711a55` … `efe219d` (F7-02) · `6c461c6` … `cb41b42` (F7-01) |
| F8 | 🔧 **en curso** (F8-05, 2026-10-10: los 14 del núcleo de `runtime` en verde, `PENDIENTES=0 · ROJOS=0`, rama `reorg/f8-05-runtime-nucleo`, `46b6a1ec` … `f5fd0374`, PR #67 a `dev` · antes: F8-01, 2026-10-09: inventario E-12 aprobado; hojas `model`·`trigger`·`content`·`store`·`modules` verdes · F8-02, 2026-10-09: motor `engine`·`menu`·`survey`·`media`·`turnoacotado` verde · F8-03, 2026-10-09 y 2026-10-10, en dos mitades: `events` y `cart` verdes, `pendiente.Implementar` en producción de `conversacion` = 0 · F8-04, 2026-10-10: los 23 contratos de `runtime` en rojo, `runtimehelpertest` verde, `PENDIENTES=60 · ROJOS=50` · F8-04b, 2026-10-10: 9 de los 12 de soporte de `runtime` verdes (D-F8-12), `PENDIENTES=50 · ROJOS=40`) | `6216d1df` | F8-05 (`runtime`: el núcleo y `welcome`, `thread`, `send`; T8.27–T8.28) |
| F9-D | pendiente | — | — |
| F10 | pendiente | — | — |

*(La sesión que cierre un bloque actualiza esta tabla y la línea «Siguiente paso».)*

## Qué se hizo

| Fecha | Qué | Dónde |
|---|---|---|
| 2026-09-27 | Análisis de factibilidad, grafo medido, árbol destino, **el método** (reconstruir por contratos y TDD, arranque paralelo, procesos con testcontainers), cinco skills del repo | `01`–`06`, `.claude/skills/` |
| 2026-09-27 | La rama `refactor/arranque-por-fases` a `dev`, con integración real 4.318 PASS · 0 SKIP; `main` alineado en `2da10b4` | git |
| 2026-09-28 | **El plan de trabajo**, escrito por un equipo de agentes sobre el código real (`dev` @ `1b18932`) y validado en dos pasadas (coherencia entre fases y verdad de campo) | [`plan/`](plan/README.md) |
| 2026-09-28 | `06-entorno-web.md` corregido con la documentación oficial de Claude Code (Docker sí; hooks sí; push solo a la rama de la sesión; *setup* que no aborta) | [`06`](06-entorno-web.md) |
| 2026-09-28 | `04` marca `publicapi` como sustituido por D-10 | [`04`](04-estructura-final.md) |
| 2026-09-30 | **Paso 00-02**: `DECISIONES.md` rellenado con la recomendación por defecto (§1, §2, §4, §5, §6); §3 abierta | [`plan/DECISIONES.md`](plan/DECISIONES.md) |
| 2026-09-30 | **F0-01**: entorno web medido (Docker: daemon a mano, Docker Hub 429 → espejo `mirror.gcr.io`; testcontainers v0.44.0 `TC_RC=0`; el proxy acepta `--force-with-lease`) y hook `SessionStart` (`.claude/settings.json`) | [`06`](06-entorno-web.md) §5 · [`plan/F0-andamiaje/tareas.md`](plan/F0-andamiaje/tareas.md) |
| 2026-09-30 | **F0-02**: `internal/pendiente` (rojo→verde), `make vet-pendiente`/`test-pendiente`, `vet-pendiente` en `ci-local`, `make lint` exige `v2.12.2` (T-1) | [`plan/F0-andamiaje/tareas.md`](plan/F0-andamiaje/tareas.md) · `Makefile` |
| 2026-09-30 | **F0-03**: `internal/candados` (rojo → 6 verdes) y los cinco candados de fichero en `ci-local` (fronteras con lista blanca medida, un fichero un test, exportados cubiertos, sin BD viva, cobertura por fichero ≥ 80 %), cada uno con su caso que muerde | [`plan/F0-andamiaje/tareas.md`](plan/F0-andamiaje/tareas.md) T0.5–T0.9 |
| 2026-09-30 | **F0-04**: `internal/arranque` (copia del viejo, D-F0-1) y `cmd/server-modular`; `huellatest` (rojo→verde); dorada desde el arranque viejo (`huella_vieja_test.go`, D-F0-2) y candado de huella entre los dos arranques, que muerde | [`plan/F0-andamiaje/tareas.md`](plan/F0-andamiaje/tareas.md) T0.10–T0.15 |
| 2026-09-30 | **F0-05**: `apipublica` (rojo→verde) montada vacía delante del `publicapi` viejo; candado de mudanzas; los tres ✎ de `platform` con alias; barridos AST viejos ciegos al árbol nuevo; deriva documental cerrada; traspaso a la local | [`plan/F0-andamiaje/tareas.md`](plan/F0-andamiaje/tareas.md) T0.16–T0.21, T0.27 · [`plan/FX-cara-http/tareas.md`](plan/FX-cara-http/tareas.md) TX.1–TX.4 · [`traspasos/`](traspasos/TRASPASO-F0-andamiaje.md) |
| 2026-09-30 | **F0-06** (💻): **F0 cerrada**. Integración vieja con Postgres real (4.618 PASS · 0 SKIP · 0 FAIL, los 8 del *collector* en PASS), arranque real de `cmd/server-modular` (9/9, `/healthz` 200, SIGINT limpio) contrastado con el viejo (98 peticiones, mismos códigos), gate `ci-local` con `go1.26.5` y `golangci-lint v2.12.2` (`GATE_RC=0`) y §7 del traspaso refutada contra lo que corre | [`plan/F0-andamiaje/tareas.md`](plan/F0-andamiaje/tareas.md) T0.22–T0.25 · [`traspasos/`](traspasos/TRASPASO-F0-andamiaje.md) `CERRADO` |
| 2026-09-30 | **F0-A-1 aplicada en claude.ai/code** (Jhoan): las cinco variables del entorno web, `TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX=mirror.gcr.io/` incluida, y el paso 4 del *setup script* (arranca `dockerd` y trae `postgres:17-alpine` y `ryuk` por `mirror.gcr.io`). Hasta hoy figuraba «pendiente» en tres ficheros por no haberse anotado | [`plan/DECISIONES.md`](plan/DECISIONES.md) F-2 y F0-A-1 · [`plan/00-marco/flujo-web-local.md`](plan/00-marco/flujo-web-local.md) §3 |
| 2026-10-01 | **F9-01** (🌐): el arnés de procesos y P0 — testcontainers-go v0.44.0 (`chore(deps)` aislado, T-2), candado ampliado, `make test-procesos`, `TestMain` + base clonada, PKI/claves, dobles de S3 e identity, servidor por proceso, clientes, Edge de prueba y `TestP0_Arranque`. Pre-chequeo web: viejo y nuevo `RC=0` (146 PASS · 0 SKIP); D-F9-2 confirmada ejecutándola | [`plan/F9-procesos/tareas.md`](plan/F9-procesos/tareas.md) T9.1–T9.11 · [`traspasos/`](traspasos/TRASPASO-F9-arnes.md) |
| 2026-10-01 | **F9-02** (💻): **bloque A de F9 cerrado**. `make test-procesos` `RC=0 · 146 PASS · 0 SKIP` por binario (pasadas 2 y 3; `CUENTA=3`: 438) salvo la pasada 1, con un rojo intermitente de `TestP0_Arranque/sin_errores` contra `nuevo` (H-1, diferida a F6); `ci-local` `GATE_RC=0`; integración vieja con `-v` 4.631 PASS · 0 SKIP · 0 FAIL (T-2); R9.1.d corregido (el comando de la spec no mide nada en macOS) y la «escapatoria» `Parar` refutada | [`plan/F9-procesos/tareas.md`](plan/F9-procesos/tareas.md) T9.5, T9.8, T9.11, T9.12 · [`traspasos/`](traspasos/TRASPASO-F9-arnes.md) `CERRADO` |
| 2026-10-01 | **F1-01** (🌐): **bloque A de F1**. `internal/nucleo/contact` nace en rojo: 4 contratos sin lógica (11 `pendiente.Implementar`: 3+2+3+3) con su test tras `pendiente` (`ROJOS=4`), la suite `contacttest.Contrato` (19 casos, validada contra el `MemoryResolver` viejo: 19/19 y 28 de 31 mutantes cazados) y el doble `EstadoMemoria`. **D-F1-6** (Jhoan): los paquetes `…test` salen también de la cobertura por fichero (`internal/candados`, rojo→verde; `FICHEROS_EVALUADOS` 10→9 por `huellatest`). 10 hallazgos nuevos (9–18) en el README de F1; D-F1-7 y D-F1-8 abiertas. Sin traspaso: nada del bloque lo cierra la local | [`plan/F1-nucleo-contact/`](plan/F1-nucleo-contact/README.md) · [`tareas.md`](plan/F1-nucleo-contact/tareas.md) |
| 2026-10-01 | **F1-01, tras el cierre del bloque** (🌐): la regla de idioma **E-11** (`8365132`; L-1 en `DECISIONES.md`; hallazgo 19 del README de F1) y `contacttest/contrato.go` partido por tema en 9 ficheros (`7069532`, movimiento puro; hallazgo 20). **PR #19 integrado en `dev` sin squash** (`6650e55`) | [`05`](05-metodo-contratos-y-tdd.md) E-11 · [`plan/F1-nucleo-contact/README.md`](plan/F1-nucleo-contact/README.md) |
| 2026-10-01 | **Revisión independiente de S9–S11** (F9-01, F9-02, F1-01; `45e01a4..6650e55`): gates repetidos, erratas corregidas (R9.1.d, cifras, referencias, restos contrarios a E-11) y hallazgos anotados con sus decisiones abiertas, sin tomar ninguna | [`plan/F1-nucleo-contact/README.md`](plan/F1-nucleo-contact/README.md) hallazgos 21–24 · [`plan/F9-procesos/README.md`](plan/F9-procesos/README.md) contradicciones 19 y 22–30 |
| 2026-10-02 | **D-F1-10 aplicada** (decisión de Jhoan; rama `reorg/revision-s9-s11`, PR #20): el sufijo que exime a suites de contrato y dobles de los tres candados de fichero es el compuesto `helpertest`. `contacttest` → `contacthelpertest` (`a18d4c0`); `isHelperTestPackage` en `internal/candados` (`06f08a8`); `huellatest` vuelve a medirse (`FICHEROS_EVALUADOS` 9→10). `05` (E-3, E-6), las specs de F0–F9 y FX, el marco y las skills nombran ya `…helpertest`. Queda abierta D-F1-13 | [`plan/DECISIONES.md`](plan/DECISIONES.md) D-F1-10 · [`plan/F1-nucleo-contact/README.md`](plan/F1-nucleo-contact/README.md) hallazgos 9 y 21 |
| 2026-10-02 | **Aplicadas las recomendaciones de la revisión de S9–S11** (a petición de Jhoan; se confirman al integrar el PR; rama `reorg/decisiones-revision-s9-s11`): D-F1-11 `4b226c9`, D-F1-12 `1507d78`, D-F9-12 `5b817ba`, D-F9-7 `b7321bb`, D-F9-8 `2a5aea1`, D-F9-6 `0e3a0f3` y D-F1-13 `88b1d85`. Todas las decisiones de F1 y de F9 que vivían solo en su README tienen ya fila en `DECISIONES.md`. Siguen abiertas D-F9-9, D-F9-10, D-F9-11 (PR aparte), D-F1-7, D-F1-8, D-F1-9 y D-F1-14 (nueva) | [`plan/DECISIONES.md`](plan/DECISIONES.md) §2 y §4 · [`plan/F1-nucleo-contact/README.md`](plan/F1-nucleo-contact/README.md) hallazgos 21–23, 25 y 26 · [`plan/F9-procesos/README.md`](plan/F9-procesos/README.md) contradicciones 22–24, 29 y 30 |
| 2026-10-02 | **F1-02** (🌐): **bloque B de F1**. D-F1-7 y D-F1-8 decididas por Jhoan y registradas antes del código (`ccc9a6b`); los cuatro ficheros de `nucleo/contact` en verde, uno por commit (`9e8f740`, `8e7a891`, `222c4c8`, `8307afb`: 11 → 0 `pendiente.Implementar`); `Estado` gana la marca (`4bbd138`); la suite contra Postgres (`4bc398d`) con el grep de R9.4.d corregido (miraba solo `doc.go`). Pre-chequeo web 19/19 contra Postgres, sin divergencias; la corrida que cuenta es T1.18 (F1-04) | [`plan/F1-nucleo-contact/tareas.md`](plan/F1-nucleo-contact/tareas.md) · [`traspasos/TRASPASO-F1-B-suite-postgres.md`](traspasos/TRASPASO-F1-B-suite-postgres.md) · hallazgos 27–29 del README de F1 |

| 2026-10-03 | **F1-03** (🌐): **bloque C de F1**. `internal/arranque/bridge_contact.go` (D-F1-9: nombres en inglés) en rojo (`09f4b72`: 4 tests del adaptador en rojo y el corpus de equivalencia viejo ↔ nuevo, 109 casos, **sin una diferencia**) y en verde (`0c2bddf` + `a62abea`, 100 %); la copia de `flows.go` cablea `newContactResolver` y pierde `contactsPG` (T-8) (`ce98595`). Huella igual; `go list -deps` 1 · 0; código viejo intacto desde `77df20f`. Hallazgos 30–34 (`Conmutados` choca con el adaptador; `go list -deps` no prueba la conmutación; los candados de fichero no miran `internal/arranque`) y **D-F1-15**, **D-F1-16** abiertas | [`plan/F1-nucleo-contact/tareas.md`](plan/F1-nucleo-contact/tareas.md) T1.14–T1.16 · [`traspasos/TRASPASO-F1-nucleo-contact.md`](traspasos/TRASPASO-F1-nucleo-contact.md) |
| 2026-10-02 | **F1-04** (💻): **bloque D de F1**, el cierre local del piloto. Gates idénticos a la web (`GATE_RC=0`, 542 PASS · 0 SKIP en el código nuevo); T1.18: suite de `contact` contra Postgres con los dos binarios rc=0 · 20 PASS · 0 SKIP, sin divergencias; `make test-procesos` 196 PASS × 2; arranque real de `server-modular` 9/9. Refutadas las dos §7 con mutantes (hallazgos 35–41). Escrito `informe-piloto.md`; traspasos de F1 **CERRADOS** | [`plan/F1-nucleo-contact/informe-piloto.md`](plan/F1-nucleo-contact/informe-piloto.md) · [`traspasos/TRASPASO-F1-B-suite-postgres.md`](traspasos/TRASPASO-F1-B-suite-postgres.md) · [`traspasos/TRASPASO-F1-nucleo-contact.md`](traspasos/TRASPASO-F1-nucleo-contact.md) |
| 2026-10-03 | **F9-03** (💻): **bloque B1 de F9**. P1, P2, P3 y P9 escritos y verdes contra los dos binarios (`make test-procesos` ×2: `RC=0 · 287 PASS · 0 FAIL · 0 SKIP` por binario); el reintento de `postgres.WithTx` se ejecuta de verdad y el mutante `maxTxAttempts = 1` cae en los dos binarios; `ci-local` `GATE_RC=0`; ningún rojo solo-nuevo; hallazgos 31–43 en el README de F9 | `679ea52`, `052089e`, `8febd52`, `250916a` |
| 2026-10-03 | **Recalibración tras el piloto** (💻, solo `.md`): P1–P7 aplicadas a las specs F2–F10 y FX, al marco, a los protocolos y a las sesiones (81 → 56; 9 sub-agentes, uno por fase); hallazgos 35–41 a F9; ficha F1-06; D-R-1…D-R-6 abiertas | [`plan/README.md`](plan/README.md) · [`plan/sesiones/README.md`](plan/sesiones/README.md) · [`plan/DECISIONES.md`](plan/DECISIONES.md) §7 |
| 2026-10-09 | **F7-05** (💻): **F7 cerrada**. Las cinco suites de `captacion` contra Postgres (88 PASS · 0 SKIP por binario; 83 casos, sin divergencias con los dobles); `make test-procesos` final, sobre `3d5fa59`: viejo y nuevo `RC=0 · PASS=1116 · FAIL=0 · SKIP=0` (104 tests de nivel superior; 0 apariciones de «el job no trae literal»; antes, sobre `a1a19ed`, dos pasadas rojas del binario nuevo por intermitencias —la carrera de D-F7-9, vista por primera vez, y una carrera del arnés del test de P8, arreglada en `3d5fa59`— y una tercera verde, hallazgo 62 de F7); refutado el hueco del hallazgo 42 (ningún proceso ejercitaba el adelanto por clasificación) y corregido con `TestP4_AheadClassification`; 78 mutantes de las guardas SQL de `intake` contra Postgres, 8 vivos, 2 tras `fb33953` y 1 tras `ca222ed`, declarado y equivalente en la práctica; **ampliación pedida por Jhoan en el mismo PR**: `ca222ed` (`QueueMontaje.Seed`, `ContratoQueue` 17 → 18 casos, y una divergencia menor del doble en memoria de `intake` con Postgres, corregida) y `13d2e59` (`plan/README.md` al día); gates finales sobre `3d5fa59` (último commit de código): `GOWORK=off make ci-local` `GATE_RC=0` (209 `ok`, lint `0 issues.`; cobertura, informe: `FICHEROS_EVALUADOS=300`, `POR_DEBAJO=7`); `make vet-pendiente` rc=0; `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0`; `-v` del código nuevo, ahora con `apipublica` dentro de la cuenta, rc=0, 9.865 PASS, **0 SKIP** (las 8.355 de antes no la incluían: la cifra sube por lo que se cuenta, no por tests nuevos); `make ci-docker` rc=0 (lint `0 issues.`) sobre `a1a19ed`, a la segunda —la primera, rc=2 por un test de rendimiento del código viejo bajo carga, hallazgo 62 de F7—, y no repetido sobre `3d5fa59`, que solo cambia un test de `test/procesos`; la integración vieja, `GOFLAGS=-v INTEGRATION_PG_PORT=55432 make test-integration` (Postgres 16 efímero del propio target, `WAPP_TEST_REQUIRE_DB=1`), corrida sobre `13d2e59`: rc=0, 14.756 `--- PASS`, 0 FAIL, **0 `--- SKIP`** contados con `-v`, y no repetida después (el 43 (c) solo toca la cara nueva); **43 (c), decidido por Jhoan y corregido en el mismo PR** (D-F7-12): el push de `PUT /api/v1/intents` va con `WithoutCancel` y un plazo propio de 5 s, `b0056a4` (rojo) y `a1a19ed` (verde); al repetir los gates sobre el verde salieron tres intermitencias, ninguna del 43 (c) (hallazgo 62 de F7), y `3d5fa59` arregla la que era del arnés (el test de P8 espera la línea del re-análisis antes de contarla); el resto del 43, decidido al cerrar sin código (D-F7-13: (e) y (a) diferidos a F10 o DT-37, (b) y (d) se quedan; deuda D-30); hallazgos 50–63 e informe de fase en el README de F7 | `30b0cf4`, `51d1bbb`, `fb33953`, `ca222ed`, `b0056a4`, `a1a19ed`, `3d5fa59` · docs `333f414`, `13d2e59`, `db7fbab` · [`plan/F7-captacion/tareas.md`](plan/F7-captacion/tareas.md) T7.27–T7.29 · [`plan/F9-procesos/tareas.md`](plan/F9-procesos/tareas.md) T9.28 |
| 2026-10-10 | **F8-03** (💻, segunda mitad: **F8-03 cerrada**): `cart` de `conversacion` en verde (T8.25; 20 ficheros de producción, `projection.go` partido en cuatro; goldens `cart_v1` y `cart_v2` verdes sin `-update` y `cmp` rc=0 contra los del viejo; candado de orden sin etiqueta, verde y mutado; 84 mutantes de la proyección, 84 muertos); `pendiente.Implementar` en producción de `conversacion` = 0; `ci-local` rc=0, 11.348 PASS y 0 SKIP con `-v`; el PR #62 de la primera mitad, integrado en `dev` (merge `8b841c8d`) | `dd0771ce`, `4045db25`, `dd52dc54`, `5774823d`, `44255a2b` · `plan/F8-conversacion/`, `internal/modulos/conversacion/modules/cart` |
| 2026-10-09 | **F8-03** (💻, a medias): `events` de `conversacion` en verde (11 ficheros, 114 exportados; doble y suite de 51 casos en memoria y en Postgres; 56 mutantes, 55 muertos y 1 equivalente) y `cart` en rojo (55 exportados, goldens, candado de orden); frontera `conversacion → catalogo` (D-F8-10) | `plan/F8-conversacion/`, `internal/modulos/conversacion/{events,modules/cart}`, `internal/modulos/fronteras_test.go`, `test/procesos/events_contrato_test.go` |
| 2026-10-09 | **F8-02** (💻): el motor de `conversacion` en verde (`engine`, `menu`, `survey`, `media`, `turnoacotado`; 9 ficheros, 66 exportados); equivalencia viejo ↔ nuevo sin divergencias; `turnoacotado` encaja con el `llmvia` nuevo sin adaptador | `plan/F8-conversacion/`, `internal/modulos/conversacion/{engine,turnoacotado,modules/{menu,survey,media}}` |
| 2026-10-09 | **F8-01** (💻): inventario E-12 de `conversacion` aprobado y sus hojas en verde (`trigger`, `content`, `store`, `modules`); suites de contrato de `store` (61 casos) y `trigger` (18) en memoria y en Postgres; 37 mutantes del SQL de `store`, 37 muertos | `plan/F8-conversacion/`, `internal/modulos/conversacion/{trigger,content,store,modules}`, `test/procesos/{flowstore,trigger}_contrato_test.go` |

## 🔒 Decisiones de Jhoan (cerradas)

1. **No es un movimiento mecánico: es una reconstrucción** por contrato → rojo → verde (2026-09-27).
2. **Arranque paralelo**: `cmd/server` (viejo, oráculo) y `cmd/server-modular` → `internal/arranque`.
3. **Los tests viejos no se portan**, se consultan.
4. **La integración no se porta**: se escribe de cero, por proceso, en caja negra, contra los dos
   binarios; es condición del relevo.
5. **Los tests de proceso usan testcontainers** con una instancia compartida; nunca un Postgres vivo.
6. **La documentación de este trabajo se commitea y pushea a `dev`**; `main`, solo a petición.
7. **Implementa Claude Code en la web**, que solo ve este repo; la sesión local cierra.
8. *(2026-09-27, sesión de plan)* **D-2** `internal/modulos/<m>/` · **D-5** los 7 módulos de `04` ·
   **D-9** `cmd/server-modular`, una prueba en UAT antes del relevo · **D-11** etiquetas `pendiente`
   e `integracion`, cero `t.Skip` · **D-12** 80 % por fichero, recalibrar tras F1.
9. 🔄 **D-10 · la cara HTTP es ÚNICA y NUEVA, `internal/apipublica`, construida por olas**
   (estrangulador delante del `publicapi` viejo; cada módulo muda sus rutas al conmutar). Sustituye
   la recomendación de repartir en `modulos/<m>/http` y el §9.2 de `05`. Ver [`plan/FX-cara-http/`](plan/FX-cara-http/README.md).
10. **Docker en la web**: la primera sesión web lo prueba; si testcontainers funciona, la web corre
    los procesos como pre-chequeo; **cierra la sesión local**.
11. **Idioma (L-1 · `05` E-11, 2026-10-02)**: en lo nuevo, nombres en inglés y solo los comentarios en español; lo ya escrito y lo ya decidido no se renombra.
    ⚠️ *Sobre esa fecha (nota de la revisión independiente, 2026-10-01)*: los «2026-10-02» de E-11, de L-1 y de los hallazgos 19–20 del
    README de F1 son la fecha **UTC** de la VM web. Los commits son `8365132` (2026-10-02 01:37Z) y `7069532` (01:46Z): en hora local
    (−03), las 22:37 y las 22:46 del **2026-10-01**; el merge `6650e55` es de las 22:52 −03. No es una fecha futura ni una errata, y la
    norma no se toca.

## Decisiones abiertas

Todas, con su recomendación y la sesión que bloquean, en [`plan/DECISIONES.md`](plan/DECISIONES.md).
Las que cambian el plan entero: **D-F9-1** (adelantar F9: el orden de sesiones lo asume), las
excepciones a E-1 de F0 (**D-F0-1/2/3**, **D-F4-1**) y la **parada tras F1**.

**D-F3-14** (F45-02, 2026-10-07): ✅ **cerrada el mismo día por Jhoan**. `internal/arranque/session_identity_test.go` se borró y `edge`
está en `Conmutados` (`{"acceso","edge"}`). Fila en `DECISIONES.md` §5; porqué en [`plan/F3-edge/reglas.md`](plan/F3-edge/reglas.md) §4.7.

**D-F1-10** (Jhoan, 2026-10-02): el sufijo que exime a las suites de contrato y a los dobles de los tres candados de fichero es el
compuesto `helpertest` (`a18d4c0`, `06f08a8`); estrecha D-F1-3 y D-F1-6 y tiene fila en `DECISIONES.md` §2.

**Las que abrió F1-01 y la revisión independiente del 2026-10-01** tienen fila en `DECISIONES.md` desde el 2026-10-02 (§2 las de F1,
§4 las de F9); hasta entonces vivían solo en el README de su fase.
- **Aplicada la recomendación de la revisión** (2026-10-02, a petición de Jhoan de aplicar las recomendaciones; se confirma al
  integrar el PR): **D-F1-11** (R-27/R-28/R-29 a P3), **D-F1-12** (`05` cumple E-11), **D-F1-13** (medir los dobles con lógica de los
  paquetes `…helpertest`), **D-F9-6** (lista blanca en `SinBDViva`), **D-F9-7** (`GOWORK=off`), **D-F9-8** (directorios `procesos-*`)
  y **D-F9-12** (gate de lease de la inferencia en el Edge de prueba).
- ⚠️ **Abiertas** (✎ 2026-10-03, F1-03: más **D-F1-15** —`Conmutados` frente a `bridge_<x>.go`— y **D-F1-16** —los candados de fichero y `internal/arranque/bridge_*.go`—): el resto de **D-F1-9** (¿el patrón `puente_<x>.go` de F2–F7 pasa a `bridge_<x>.go`?; ✎ 2026-10-02: **D-F1-7**, **D-F1-8** y **D-F1-9** para F1 ya decididas por Jhoan); **D-F1-14** (nueva: extender «solo los ficheros
  de suite exentos» a `un_fichero_un_test` y `exportados_cubiertos`), en el [README de F1](plan/F1-nucleo-contact/README.md); y
  **D-F9-9** (las dos respuestas al §8 del traspaso), **D-F9-10** (el alcance de D-F6-7) y **D-F9-11** (los ficheros de test largos:
  ✎ aplicada el 2026-10-02 en su propio PR; se confirma al integrarlo), en el [README de F9](plan/F9-procesos/README.md).

## Lo que el plan corrigió de los documentos 01–05 (resumen)

La norma (`05`) **sigue mandando**; estas son erratas o precisiones medidas, no cambios de método
(salvo D-10, arriba). El detalle, en la sección «Contradicciones encontradas» de cada fase.

- **`05` E-6**: los paquetes con SQL son **22** (no 20); **15 tienen gemelo en memoria y 7 no**
  (`casebank`, `degradation`, `flujos/events`, `flujos/runtime`, `integrations`, `platformadmin`,
  `tenantllm`), no «12 sin gemelo». La suite de contrato de un puerto con BD toma la forma
  `Contrato(t, func(t) Montaje)` (D-F1-1), no `func() Puerto`.
- **`05` §5**: los candados cubren también `internal/nucleo` e `internal/apipublica`, no solo
  `internal/modulos/` (T0.7).
- **`05` §3.2**: faltan el candado C2 de `llmvia`, el de vocabulario de `degradation` y
  `inv1_pedirinfo_ast_test.go`; de `events/summary_test.go` solo 1 de 26 tests es candado; los
  candados AST del canje **no** necesitan BD (D-F2-1).
- **`05` §4 y `04` §2.2**: las ventanas de agregación son **durables** (`intake_jobs`), no memoria;
  el singleton `*runtime.Runtime` sí guarda estado (mutex por clave, limitador, semáforo, rachas).
  Y en la transición `cmd/server-modular` **sí** enlaza paquetes viejos (vía adaptadores).
- **`05` §7 / `03` / skill `traspaso-web-local`**: «la web no tiene Docker» está desfasado.
- **`04` §5**: `catalog.go` no es autocontenido y los alias en el carrito viejo no aplican con E-1;
  `model` se reconstruye en F5 (D-F5-1).
- **Cifras**: 97 ficheros `*_integration_test.go` (132 con BD), no 107 · 71 variables de entorno
  efectivas · 2.623 menciones de `internal/` fuera del repo · 12 comentarios en repos hermanos.
- **Documentación del repo con rutas caducadas**: `contratos.md` (`internal/bootstrap/http.go`),
  `operacion.md` y la constitución (`internal/publicapi/flows.go:75`, que está en
  `internal/bootstrap/arranque/flows.go:75`). ✅ **Cerrada en F0 (T0.20, `8096232`)**: el `grep` de
  R0.8.a da solo la línea de historia (`deuda.md:139`).

## Pendientes y obstáculos conocidos

- ✅ **El lint de `ci-local` está fijado** (T-1, `d05ac3a`): `make lint` aborta si no es `v2.12.2`. Consecuencia local: el Go (`1.27.1`) y el lint (`2.14.0`) del sistema **no** sirven; hace falta `GOTOOLCHAIN=go1.26.5` y `v2.12.2` en el `PATH`.
- ✅ **testcontainers sube dependencias de producción** (T-2): hecho en `37c7db7` (commit aislado). Suben `httpsnoop` 1.1.0, `otelhttp` 0.69.0 y `klauspost/compress` 1.18.6; `ci-local` y `make test-integration` (sin `-v`) dan rc=0 sobre esa base. **Cerrada con `-v` en F9-02**: `RC=0 · 4.631 PASS · 0 SKIP · 0 FAIL` (los +13 frente a los 4.618 de F0-06 son los casos del candado de T9.3, medidos), `go mod tidy` sin cambios y `go mod verify` OK.
- 🟡 **H-1 · `TestP0_Arranque/sin_errores` intermitente** (F9-02): carrera entre la parada y la primera llamada a BD del *webhook worker* (código compartido por los dos binarios; el mismo patrón, sin observar, en otras tres goroutines de fondo que F6 no reconstruye: revisión del 2026-10-01); **diferida a F6 por decisión de Jhoan (2026-10-01)**: se evalúa al reconstruir `integrations` (D-F6-7), sin arreglar nada antes. Ver el README de F9, contradicción 19.
- ✅ **R2 / `HeadBucket`** (`internal/bootstrap/arranque/flows.go:75`): **resuelto y ejecutado** en F9-01
  sin tocar el arranque (endpoint IP → *path-style*; S3 falso en el proceso de test; P0 lo aserta en los dos binarios); el arranque
  real de F0 (T0.23) sí lo ejecutó contra el R2 de desarrollo de `.env`, solo lectura.
- `make test-integration` usa `postgres:16`; UAT corre `postgres:17-alpine`; la VM web trae un
  PostgreSQL 16 que **no se usa** para tests.
- Fuera de este repo (F10, sesión local): la documentación del ecosistema, la regla de conteo del
  ADR-0010 y los comentarios en repos hermanos.

## Estado de git

- **F8-05 (el verde del núcleo de `runtime`)**: rama `reorg/f8-05-runtime-nucleo`, partida de `origin/dev` @ `da82addf`; 9 commits de código (`46b6a1ec`, `0c1d8973`, `0aaa18cd`, `1e0f6009`, `a435884b`, `54c25319`, `a6fb0215`, `6c9bd1f1`, `f5fd0374`; los dos de tests de mutación, hechos por sub-agentes en *worktrees* e integrados con `cherry-pick`) y los de cierre; PR #67 a `dev`, sin squash (lo integra Jhoan). *Worktrees* temporales fuera del repo, ya borrados. `origin/main` sin tocar.
- **F8-04b (el verde del soporte de `runtime`)**: rama `reorg/f8-04b-runtime-soporte`, partida de `origin/dev` @ `cbebf10c`; 1 commit de decisión (`a8c1f640`), 7 de código (`83565b4e`, `fc471fed`, `163c193b`, `a446d4b2`, `91ffb00d`, `093ea8fc`, `6216d1df`) y los de cierre; PR #66 a `dev`, **integrado** por orden expresa de Jhoan (2026-10-10), merge `d80e7f56`, sin squash. `origin/main` sin tocar.
- **F8-04 (los contratos de `runtime`)**: rama `reorg/f8-04-runtime-contratos`, partida de `origin/dev` @ `e0159171`; 4 commits de código (`cf327ca5`, `ee6da0af`, `33e9eca2`, `de6a5411`) y los de cierre; PR #65 a `dev`, **integrado** por orden expresa de Jhoan (2026-10-10), merge `cbebf10c`, sin squash. Sin *worktrees*. `origin/main` sin tocar.
- **F8-03, segunda mitad (el verde de `cart`, T8.25)**: rama `reorg/f8-03b-cart-verde`, partida de `origin/dev` @ `8b841c8d`; 5 commits de código (`dd0771ce`, `4045db25`, `dd52dc54`, `5774823d`, `44255a2b`) y el de cierre; PR #63 a `dev`, **integrado** por orden expresa de Jhoan (2026-10-10), sin squash (lo integra Jhoan, sin squash). Sin *worktrees*: el sub-agente trabajó en el checkout. `dev` y `origin/main` sin tocar.
- **F8-03, primera mitad (`events` verde y `cart` en rojo)**: rama `reorg/f8-03-events-cart`, partida de `origin/dev` @ `9d5a4b6`; 10 commits de código (`8c819b40`, `f909402e`, `6590724e`, `d3c2f89e`, `006e4248`, `6284c04e`, `1667bca9`, `69d99d54`, `6dc8e574`, `790ca522`) y el de cierre; PR #62, **integrado** en `dev` por orden expresa de Jhoan en la conversación, merge `8b841c8d`, sin squash (✎ 2026-10-10: aquí decía «abierto, sin integrar»). El *worktree* del sub-agente, borrado. `dev` y `origin/main` sin tocar.
- **F8-02 (el motor de F8)**: rama `reorg/f8-02-motor`, partida de `origin/dev` @ `42117b5`; 8 commits de código (`d8fd4ac`, `346cf73`, `b566ae0`, `fc085d5`, `17ca94d`, `8f5f667`, `08b1e64`, `4ce435f`) y el de cierre; PR #61, **integrado** en `dev` por orden expresa de Jhoan en la conversación («mergea a dev»), merge `df340a6`, sin squash. Los tres *worktrees* de los sub-agentes, borrados. `dev` y `origin/main` sin tocar.
- **F8-01 (inventario y hojas de F8)**: rama `reorg/f8-01-inventario-y-hojas`, partida de `origin/dev` @ `c0c0c03`; 2 commits de documentación (`7e24504`, `98a6214`), 13 de código (`15524e1` … `3a408dc`) (`03d82e9`, `2677cb8` en la ampliación) y los de cierre; PR #60, **integrado** en `dev` por orden expresa de Jhoan, merge `8c5b27c`, sin squash.
- **F7-05 (cierre local de F7)**: rama `reorg/f7-05-cierre`, partida de `origin/dev` @ `53e8f51` (PR #58 dentro); `30b0cf4`, `51d1bbb`, `fb33953`, `ca222ed`, `b0056a4`, `a1a19ed` y `3d5fa59` (7 commits de código: tests y, en `ca222ed`, también el doble en memoria de `intake`; `b0056a4` y `a1a19ed`, rojo y verde de D-F7-12, con el único cambio de producción, `apipublica/intents.go`; `3d5fa59`, el arreglo del test de P8), 3 de documentación (`333f414`, `docs(reorganizacion-modular): F7 cerrada`; `13d2e59`, el resumen del plan al día; y `db7fbab`, la ampliación en el mismo PR) más el de esta puesta al día del cierre; el resto del hallazgo 43 se decidió al cerrar, sin código (D-F7-13, hallazgo 63 de F7); el PR #59, **integrado** en `dev` por orden expresa de Jhoan en la conversación («mergear»), merge `c0c0c03`, sin squash (✎ F8-01: aquí decía «se integra»). Los tres *worktrees* de los sub-agentes, borrados. `dev` y `origin/main` sin tocar.
- **F7-04 (cara HTTP y conmutar)** (✎ F7-05: **integrada** en `dev` @ `53e8f51`, PR #58, sin squash): rama `reorg/f7-04-cara-http-y-conmutar`, partida de `origin/dev` @ `4f79bbf` (PR #57 dentro); `a688ca4` … `94f0648` (5 commits: 1 de rojo, 2 de verde, 1 de conmutar y 1 de refactor) más el cierre documental; PR de la rama hacia `dev`, abierto al cierre, **integrar sin squash**. Sin *worktrees*. `dev` y `origin/main` sin tocar.
- **F7-03 (`pipeline`, `intakeahead`, `reanalisis`)**: rama `reorg/f7-03-pipeline-reanalisis`, partida de `origin/dev` @ `56097aa` (PR #56 dentro); `7cb66e2` … `69d142e` (4 de rojo y doble, 6 de verde, 3 de test) más el cierre documental y D-F7-10 y D-F7-11; PR **#57**, sin squash. Sin *worktrees*. `origin/main` sin tocar.
- **F7-02 (`stages`)**: rama `reorg/f7-02-stages`, partida de `origin/dev` @ `4cd9cfb` (PR #55 dentro); `7711a55` … `efe219d` (3 de rojo, 8 de verde) más los commits documentales; **PR #56** hacia `dev`, integrado sin squash a petición de Jhoan. Sin *worktrees*.
- **F7-01 (inventario y hojas de F7)**: rama `reorg/f7-01-inventario-y-hojas`, partida de `origin/dev` @ `8d875ab` (PR #54 dentro); `6c461c6` … `cb41b42` (1 de inventario, 4 de rojo, 17 de verde, 2 de test y 4 `fix` de D-F7-8) más los commits documentales; **PR #55** hacia `dev`, **integrar sin squash**. Sin *worktrees*.
- **F6-06 (cierre local de F6)**: rama `reorg/f6-06-cierre-local`, partida de `origin/dev` @ `68e68a4` (PR #52 y #53 dentro); **solo** el cierre documental (`docs(reorganizacion-modular): F6 cerrada`); PR hacia `dev`, **integrar sin squash**. Los dos *worktrees* de los sub-agentes de mutantes, borrados. `origin/main` sin tocar.
- **F6-05** (✎ F6-06: los dos PR, #52 y #53, **integrados** en `dev` @ `68e68a4`, sin squash): **F6-05a**, rama `reorg/f6-05a-cara-solicitudes`, partida de `dev` @ `40dc145`; `2cc4cde` … `6675927` (23 commits: 4 de rojo, 18 de verde y 1 de test) y el cierre documental `72ded2c`; **PR #52**. **F6-05b**, rama `reorg/f6-05b-conmutar-solicitudes`, partida de la anterior @ `72ded2c`; `79f274e` (1 commit de conmutar) y el cierre documental; PR hacia `dev`, **después del #52**. Los dos, **integrar sin squash**. Sin *worktrees* (los cinco de los sub-agentes, borrados). `origin/main` sin tocar.
- **F6-04**: rama `reorg/f6-04-quotetext-telemetria-integrations-crmpush`, partida de `dev` @ `36d5a04` (PR #48 dentro); `31b9343` … `af68fe3` (15 commits: 11 de verde, 3 de rojo y 1 de fix) y el cierre documental; PR hacia `dev`, **integrar sin squash**. Sin *worktrees*. `origin/main` sin tocar.
- **F6-03** (integrada en `dev` @ `36d5a04`, PR #48): rama `reorg/f6-03-intakes-almacenes-acciones-candados`, partida de `dev` @ `5fd533e` (PR #47 dentro); `e8f1e10` … `4fcccc6` (28 commits: 22 de verde, 1 de rojo, 1 de refactor y 4 de test) y el cierre documental; PR hacia `dev`, **integrar sin squash**. Sin *worktrees*. `origin/main` sin tocar.
- **F6-02** (integrada en `dev` @ `5fd533e`, PR #47): rama `reorg/f6-02-intakes-contratos-y-tipos`, partida de `dev` @ `64c181a` (PR #46 dentro); `113912e` … `c366c68` (13 commits: 3 de rojo, 9 de verde y 1 de test) y el cierre documental; PR hacia `dev`, **integrar sin squash**. Sin *worktrees*. `origin/main` sin tocar.
- **F6-01** (integrada en `dev` @ `64c181a`, PR #46): rama `reorg/f6-01-inventario-y-hojas`, partida de `dev` @ `3a21138` (PR #45 dentro); `27a7924` … `06879cb` (8 commits: 1 de inventario, 1 de rojo, 4 de verde, 1 de procesos y 1 de comentario) y el cierre documental; PR hacia `dev`, **integrar sin squash**. Sin *worktrees*. `origin/main` sin tocar.
- **F45-03**: rama `reorg/f45-03-cierre`, partida de `dev` @ `6d53966` (PR #44 dentro); `ddb8baa` y `59a3e6b` (2 commits de test) y el cierre documental (dos commits); PR hacia `dev`, **integrar sin squash**. Sin *worktrees*. `origin/main` sin tocar.
- **F45-02**: rama `reorg/f45-02-conmutar-inferencia-catalogo`, partida de `dev` @ `3c74b80`; `65f5348` … `13e869f` (29 commits: 8 de rojo, 16 de verde, 3 de conmutar y 2 de test) y el cierre documental; PR hacia `dev`, **integrar sin squash**. `origin/main` sin tocar.
- **F45-01**: rama `reorg/f45-01-inventario-inferencia`, partida de `dev` @ `ebf4eb7` (PR #42 dentro); `2783172` … `24f6450` (2 commits de inventario, 7 de rojo, 15 de verde y test) y el cierre documental; PR hacia `dev`, **integrar sin squash**. Queda un *worktree* de sub-agente bloqueado por el harness (`.claude/worktrees/`, ignorado por los gates de Go), ya integrado.
- **F3-05**: rama `reorg/f3-05-cierre-mtls`, partida de `dev` @ `9d9c033` (PR #41 dentro); `8121564` … `876b096` (11 commits de test y arnés) y el cierre documental; PR hacia `dev`, **integrar sin squash**. Los *worktrees* de los sub-agentes ya están borrados; quedan sus ramas locales `f305-*`, ya integradas por `cherry-pick`. `origin/main` sin tocar.
- **F3-04** (integrada en `dev` @ `9d9c033`, PR #41): rama `reorg/f3-04-bridge-conmutar-rutas`, partida de `origin/dev` @ `115a4ba` (PR #40 dentro); `dd4cbd2` … `0ebb743` (13 commits de código) y el cierre documental; PR hacia `dev`, **integrar sin squash**. Sin *worktrees*. `origin/main` sin tocar.
- **F3-03 (tanda 3)** (integrada en `dev` @ `414b31b`, PR #39): rama `reorg/f3-03-grpc-tanda-3`, partida de `origin/dev` @ `ec236b3`; `185dcbc` … `231c74b` y el cierre documental; PR hacia `dev`, **integrar sin squash**. El *worktree* del sub-agente del saludo y las copias de los mutantes ya están borrados; queda la rama local `wt/f3-03-greeting`, ya integrada por `cherry-pick`. `origin/main` sin tocar.
- **F3-03 (tanda 2)** (integrada en `dev` @ `ec236b3`, PR #38): rama `reorg/f3-03-grpc-tandas-2-3`, partida de `origin/dev` @ `cbc5736`; `f0019af` … `84c83f6` y el cierre documental; PR hacia `dev`, **integrar sin squash**. Sin *worktrees*. `origin/main` sin tocar.
- **F3-03 (tanda 1)** (integrada en `dev` @ `cbc5736`, PR #37): rama `reorg/f3-03-grpc`, partida de `origin/dev` @ `c851591`; `0117221`, `b1a408b`, `dd4f984` … `a0baf00` y el cierre documental; PR hacia `dev`, **integrar sin squash**. Sin *worktrees*. `origin/main` sin tocar.
- **F3-02**: rama `reorg/f3-02-fleet-filtercfg`, partida de `origin/dev` @ `809345b`; `d7323b7` … `b86dd62` y el cierre documental; PR hacia `dev`, **integrar sin squash**. El *worktree* del sub-agente ya está borrado. `origin/main` sin tocar.
- **F3-01** (integrada en `dev` @ `809345b`, PR #35): rama `reorg/f3-01-inventario-hojas`, partida de `origin/dev` @ `8896f13`; `3ae565c` … `8a4dd34` y el cierre documental; PR hacia `dev`, **integrar sin squash**. Los *worktrees* de los sub-agentes ya están borrados. `origin/main` sin tocar.
- **`origin/dev` = `4f0ed06`** (2026-10-04): contiene F2 entera, con el PR #33 (F2-05) y el #34 (las siete 🟡 decididas), sin squash. Las ramas `reorg/f2-05-cierre-local` y `reorg/f2-decisiones-abiertas` ya no hacen falta. `origin/main` sin tocar.
- **F2-05 (cierre local de F2)**: rama `reorg/f2-05-cierre-local`, partida de `origin/dev` @ `bfd31ce` (PR #32 dentro). Seis commits de test (`8677404`, `40d1582`, `8c53ecf`, `d243f15`, `7f2b745`, `73b4541`) y el cierre documental; PR hacia `dev`, **integrar sin squash**. Las copias desechables de los mutantes ya están borradas. `origin/main` sin tocar.
- **F2-01**: rama `reorg/f2-01-inventario-hojas`, partida de `origin/dev` @ `9a77307`; commits de arriba y el cierre documental; PR hacia `dev`, **integrar sin squash**. Los *worktrees* de los sub-agentes se borran al cerrar. `origin/main` sin tocar.
- **F9-04 (bloque B2 de F9)**: rama `reorg/f9-b2`, partida de `dev` @ `7b092d5` (PR #27 dentro). Siete commits de proceso (`e3fc0de`, `49d6d9e`, `39c38be`, `9da68f7`, `0a2762d`, `56b99f6`, `2bb7989`) y el cierre documental; PR hacia `dev`, **integrar sin squash**. Los *worktrees* de los sub-agentes ya están borrados. `origin/main` sin tocar.
- **F1-06 (ajustes previos a F2)**: seis commits de código y uno de documentación directamente sobre `dev`, partiendo de `3e181f6`. Escritos por sub-agentes en *worktrees* fijados en ese SHA e integrados con `cherry-pick`; los *worktrees* ya están borrados. `origin/main` sin tocar.
- **F1-04 (bloque D de F1)**: commits solo de documentación directamente sobre `dev` (`d5228ac` y el que anota su SHA), partiendo de `ddcf7de` (PR #25). Los dos traspasos de F1 están **CERRADOS**. `origin/main` sin tocar.
- **F1-03 (bloque C de F1)**: rama `reorg/f1-c-adaptador`, partida de `origin/dev` @ `61a3c8b` (PR #23 y #24 dentro). Commits `09f4b72`, `0c2bddf`, `a62abea`, `ce98595` y el cierre documental; PR hacia `dev`, **integrar sin squash**. Traspasos **abiertos** para F1-04: [`TRASPASO-F1-B-suite-postgres.md`](traspasos/TRASPASO-F1-B-suite-postgres.md) y [`TRASPASO-F1-nucleo-contact.md`](traspasos/TRASPASO-F1-nucleo-contact.md).
- **F1-02 (bloque B de F1)**: rama `reorg/f1-b-verde`, partida de `origin/dev` @ `5847ad4` (el `dev` local de la VM iba 66 commits por detrás: se avanzó con `--ff-only` antes de nada). PR #23 hacia `dev`, **integrar sin squash**. ✎ Tras integrarse el PR #22 en `dev` (`0a377bc`), la rama lo trae por merge (`a5d17b5`, sin conflictos; merge y no rebase para que los SHA citados sigan valiendo) y los gates se repitieron sobre él: `GATE_RC=0` (88 `ok`), `PENDIENTES=0 · ROJOS=0`, `POR_DEBAJO=0`, 0 SKIP, `vet -tags integracion` rc=0, R9.4.d vacío y el pre-chequeo `-run Contact` viejo y nuevo 20 PASS · 0 SKIP. Traspaso **abierto**: [`traspasos/TRASPASO-F1-B-suite-postgres.md`](traspasos/TRASPASO-F1-B-suite-postgres.md) (T1.18).
- **F1-01 (bloque A de F1)**: rama `reorg/f1-a-contratos-rojo`, partida de `origin/dev` @ `77df20f`; **PR #19 integrado en `dev` sin squash** (merge `6650e55`, 2026-10-01 22:52 −03; 12 commits, de `afa63f3` a `7069532`). El `dev` local de la VM web iba 17 commits por detrás: se trabajó siempre desde `origin/dev`.
- **Decisiones de la revisión de S9–S11** (2026-10-02): rama `reorg/decisiones-revision-s9-s11`, partida de `dev` @ `c63aca7` (PR nuevo hacia `dev`; integrar sin squash, para que cada decisión conserve su commit). Commits: arriba, «Aplicadas las recomendaciones».
- **Revisión independiente de S9–S11** (2026-10-01): rama `reorg/revision-s9-s11`, partida de `dev` @ `6650e55` (PR hacia `dev`, **pendiente de revisar por Jhoan**; integrar sin squash). Commits: arriba, «Revisión independiente». ✎ 2026-10-02: **PR #20 integrado en `dev`** (merge `c63aca7`, sin squash).
- `origin/dev` contiene **F0 entera** (PR #13–#17, sin squash; el último merge es `835a7be`) más el cierre de F0-06.
  `origin/main` = `2da10b4`, sin tocar. Traspaso de F0: [`traspasos/TRASPASO-F0-andamiaje.md`](traspasos/TRASPASO-F0-andamiaje.md), **CERRADO**.
- `origin/dev` contiene también el bloque A de F9 (PR #18, sin squash; el merge es `af7b8e9`; la rama `reorg/f9-a-arnes` ya está borrada). El cierre local
  de F9-02 son **tres** commits solo de documentación sobre `dev` (`git log --oneline af7b8e9..77df20f`: `ac8ac5f`, `79c7160` y `77df20f`). Traspaso: [`traspasos/TRASPASO-F9-arnes.md`](traspasos/TRASPASO-F9-arnes.md), **CERRADO**.

## Para retomar

0. **Siguiente sesión**: **F8-06** ([`F8-06-cli-cara-http-y-conmutar`](plan/sesiones/F8-06-cli-cara-http-y-conmutar.md): `admin`, los ficheros de conversación de `apipublica` y la conmutación del runtime; T8.13, T8.29–T8.35), sobre `dev` con el PR #67 de F8-05 (rama `reorg/f8-05-runtime-nucleo`, `46b6a1ec` … `f5fd0374`) **integrado**; la sesión lo comprueba con `git log origin/dev`. Le deja dicho F8-05: los hallazgos 41–46 del README de F8; `runtime` son 40 ficheros de producción y ninguno lleva `pendiente`; el runtime nuevo no lo cablea todavía ningún binario; D-F7-9 **no** se arregla en F8-06: va después, en **F8-06b** ([ficha](plan/sesiones/F8-06b-cli-d-f7-9-cierre-y-sobre.md), D-F8-13), y F8-07 pasa a depender de ella; un solo `runtime.New` en el arranque (T-1) y `WithQueryResolver` (T-6).
1. Lee [`plan/README.md`](plan/README.md) y, si vas a ejecutar, el fichero de tu sesión en
   [`plan/sesiones/`](plan/sesiones/README.md) (él te dice qué más leer).
2. La norma: [`05-metodo-contratos-y-tdd.md`](05-metodo-contratos-y-tdd.md).
3. Skills del repo: `reconstruir-modulo`, `contrato-tdd`, `validar-antes-de-cerrar`,
   `traspaso-web-local`, `procesos-testcontainers`.
