# Sesiones — el orden de ejecución y el prompt de cada una

> Cada fichero de esta carpeta es **una sesión** de Claude Code: dice en qué entorno se hace, de qué
> depende, dónde se para, y trae el **prompt listo para pegar**. El *cómo* común de todas vive en
> dos protocolos que la propia sesión lee al empezar: [`PROTOCOLO-WEB.md`](PROTOCOLO-WEB.md) y
> [`PROTOCOLO-CLI.md`](PROTOCOLO-CLI.md). Así el prompt es corto y la regla no se duplica.
>
> 🔴 **Desde el 2026-10-04 todo es local (D-R-8, Jhoan)**: se acabó la promoción web y con ella la división web/local.
> Toda sesión pendiente es 💻 y lee [`PROTOCOLO-CLI.md`](PROTOCOLO-CLI.md); las fichas que eran 🌐 conservan «web» en el
> **nombre del fichero** (no se renombran: lo enlazan las specs) pero su prompt ya es local. No hay traspasos nuevos.
> Lo que NO cambia: el código nace en una rama partida de `dev` y entra por PR sin squash (regla 6 del `CLAUDE.md`).
>
> **56 pasos**: 23 hechos y **33 pendientes** (32 💻 · 1 🧑; recuento del 2026-10-04). Recalibrado el 2026-10-03 tras la
> parada de F1 ([`../DECISIONES.md`](../DECISIONES.md) §3): eran 81; las 66 pendientes se reagruparon en 41.

## Cómo se usa

1. **Una sola vez**: [`00-01`](00-01-jhoan-preparar-entorno-web.md) (el entorno web) y
   [`00-02`](00-02-jhoan-decisiones-iniciales.md) (las decisiones de [`../DECISIONES.md`](../DECISIONES.md) §1, §2 y §4).
2. **Sigue la tabla en orden.** Abre el fichero del paso, cumple su «Antes de pegar el prompt», y
   pega el bloque `Prompt`:
   - 💻 **CLI** (todas, desde el 2026-10-04): `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude` → pegar.
   - 🌐 / 🌐❓ en una fila **hecha** de la tabla: así se corrió entonces; es historia.
   - 🧑 **tuyas**: no hay prompt; es una lista de lo que decides tú.
3. **Entre dos sesiones**: toda sesión termina con un **PR hacia `dev`** desde su rama (regla 6 del `CLAUDE.md`). Antes
   de arrancar la siguiente, ese PR tiene que estar **integrado sin squash** (en GitHub, «Rebase and merge» o «Create a
   merge commit»; nunca «Squash and merge»). Directo a `dev` solo si Jhoan lo pide expresamente en la conversación.
4. **Marca el estado** en la columna de la tabla (`hecha`, con la fecha) cuando la sesión cierre. La
   verdad, de todos modos, son los `[x]` con SHA de cada `tareas.md`.

## Reglas del orden

- **El orden supone D-F9-1 = sí** (F9 adelantado). Si decides que no, los pasos de F9 que están
  antes de F2 (F9-01…F9-05) se saltan en su sitio y F9 entero se hace tras F8 (T9.34); las sesiones
  de cierre de cada módulo ignoran su pasada de procesos.
- **[F1-06](F1-06-cli-ajustes-previos-a-f2.md) está hecha** (2026-10-03): los ajustes de código que pedían P2, P4 y P5. Y **[F9-04](F9-04-cli-procesos-de-negocio.md) está hecha** (2026-10-03): P4–P8 y P10, T9.22, y la suite P0–P10 verde contra los dos binarios. Y **[F9-03](F9-03-cli-procesos-plataforma-y-acceso.md) está hecha** (2026-10-03): P1, P2, P3 y P9, y el mutante `maxTxAttempts = 1` cae. **Lo siguiente es [F9-04](F9-04-cli-procesos-de-negocio.md).** ✎ **[F2-01](F2-01-web-inventario-y-hojas.md) está hecha** (2026-10-04): inventario E-12 aprobado y las hojas simples de `acceso` en verde. ✎ **[F2-02](F2-02-web-usecase-identity.md) está hecha** (2026-10-04): `iam/infra/identity` e `iam/usecase` en verde. ✎ **[F2-03](F2-03-web-postgres-http-platformadmin.md) y [F2-04](F2-04-web-bridge-conmutar-y-rutas.md) están hechas** (2026-10-04): 0 pendientes en `acceso` y `conmutar(acceso)`. ✎ **[F2-05](F2-05-cli-cierre.md) está hecha** (2026-10-04, 💻): suites y mutantes del nivel complejo contra Postgres, procesos contra los dos binarios, arranque real; **F2 cerrada**. **Lo siguiente es [F3-01](F3-01-web-inventario-y-hojas.md)**, ya en local (D-R-8). ✎ Los PR #33 (F2-05) y #34 (las siete 🟡 de F2 decididas) están integrados en `dev` (`4f0ed06`).
  Sin ella no arranca F9-B ni F2.
- **Cada fase empieza por su inventario E-12** (`05` E-12): la primera sesión clasifica cada archivo (simple,
  medio, complejo) y lista los adaptadores `bridge_<x>.go`; **para hasta que lo apruebes**, y luego sigue.
- **En serie, no en paralelo.** Las sesiones de una fase comparten ficheros (`tareas.md`,
  `fronteras_test.go`, el arranque); dos a la vez chocarían. La única excepción razonable: una 🧑
  mientras corre otra.
- **Si una sesión se corta** (límite de uso, VM reciclada): se relanza **la misma** con el mismo
  prompt. Su verdad de campo lee lo commiteado y empujado y sigue desde ahí.
- **Tamaño**: una sesión es un bloque coherente de **45–90 min**. Si no cabe, para en un punto limpio
  (commits empujados, `[~]` en la tarea), cierra con las tres cosas y se relanza con el mismo prompt.
  ⚠️ Los tamaños están **sin medir** (D-R-6 en [`../DECISIONES.md`](../DECISIONES.md) §7): se ajustan con el dato de F2.
- **Cierre fijo, tres cosas**: tareas `[x]` con SHA, un bloque en `ESTADO.md`, y los hallazgos nuevos en el
  README de la fase. El traspaso web ↔ local solo se escribe si algo lo cierra el otro entorno o si la sesión
  se corta.

## Qué sesiones usan la web

✎ **2026-10-04 (D-R-8): la promoción se acabó; lo que sigue es historia.** Todas las pendientes son 💻. La frase final de
este apartado («no hay PR ni traspaso») quedó corregida por la regla 6 del `CLAUDE.md` (2026-10-03): PR sigue habiendo.

La promoción web (250 USD; ≈ 100 gastados; ≈ 11 USD por sesión, **estimación**) alcanza para unas **13–19
sesiones** más. Se gasta en lo que la web hace bien, **escribir código**; lo que necesita Docker, UAT o `main`
es local.

| | Sesiones | Cuántas |
|---|---|---:|
| 🌐 **web** | F2-01…04 · F3-01…04 · F45-01…02 · F6-01…03 | 13 |
| 🌐❓ **web si queda saldo; si no, local** | F6-04…05 · F7-01…04 | 6 |
| 💻 **solo local** | F1-06 (ajustes) · F9 entero (testcontainers) · los cierres de cada módulo (suites contra Postgres, procesos, mTLS) · F8 entero · F10 (UAT, `main`) | 21 |

Un relanzamiento de una sesión web también gasta saldo: si una 🌐 se corta dos veces, las 🌐❓ pasan a local antes.
Cuando se acabe la promoción desaparece la separación: todo es 💻 y no hay PR ni traspaso.

## El orden

La columna «Nivel» es el **provisional** de la spec (`05` E-12); el que vale es el del inventario aprobado.

| Paso | Sesión | | Fase | Bloque | Tareas | Nivel | Estado |
|---:|---|:-:|---|---|---|---|---|
| 1 | [`00-01-jhoan-preparar-entorno-web`](00-01-jhoan-preparar-entorno-web.md) | 🧑 | 00 | — | — | — | hecha 2026-09-30 (variables y *setup script*, con el paso 4 de F0-A-1; lo usan F0-01 a F0-05) |
| 2 | [`00-02-jhoan-decisiones-iniciales`](00-02-jhoan-decisiones-iniciales.md) | 🧑 | 00 | — | — | — | hecha 2026-09-30 |
| 3 | [`F0-01-web-entorno`](F0-01-web-entorno.md) | 🌐 | F0 | A | T0.0–T0.1 | — | hecha 2026-09-30 |
| 4 | [`F0-02-web-pendiente-y-make`](F0-02-web-pendiente-y-make.md) | 🌐 | F0 | B | T0.2–T0.4, T0.26 | — | hecha 2026-09-30 (PR #14, fusionado en `dev` sin squash) |
| 5 | [`F0-03-web-candados`](F0-03-web-candados.md) | 🌐 | F0 | C | T0.5–T0.9 | — | hecha 2026-09-30 (PR #15, fusionado en `dev` sin squash) |
| 6 | [`F0-04-web-arranque-y-huella`](F0-04-web-arranque-y-huella.md) | 🌐 | F0 | D | T0.10–T0.15 | — | hecha 2026-09-30 (PR #16, fusionado en `dev` sin squash) |
| 7 | [`F0-05-web-cara-vacia-platform-deriva`](F0-05-web-cara-vacia-platform-deriva.md) | 🌐 | F0 | E | T0.16–T0.21, T0.27 | — | hecha 2026-09-30 (PR #17, fusionado en `dev` sin squash) |
| 8 | [`F0-06-cli-cierre`](F0-06-cli-cierre.md) | 💻 | F0 | E/F | T0.16–T0.21, T0.27 + T0.22–T0.25 | — | hecha 2026-09-30 (F0 cerrada; `dev` empujado) |
| 9 | [`F9-01-web-arnes`](F9-01-web-arnes.md) | 🌐 | F9 | A | T9.1–T9.12 🕐 | — | hecha (2026-10-01) |
| 10 | [`F9-02-cli-arnes-cierre`](F9-02-cli-arnes-cierre.md) | 💻 | F9 | A | T9.1–T9.12 🕐 | — | hecha (2026-10-01; H-1 diferida a F6) |
| 11 | [`F1-01-web-contratos-y-rojo`](F1-01-web-contratos-y-rojo.md) | 🌐 | F1 | A | T1.1–T1.7 (+ T1.3b) | — | hecha (2026-10-01; PR #19, fusionado en `dev` sin squash: `6650e55`) |
| 12 | [`F1-02-web-verde`](F1-02-web-verde.md) | 🌐 | F1 | B | T1.8–T1.13 | — | hecha (2026-10-02; PR #23, fusionado en `dev` sin squash: `ca364de`) |
| 13 | [`F1-03-web-adaptador-y-conmutacion`](F1-03-web-adaptador-y-conmutacion.md) | 🌐 | F1 | C | T1.14–T1.16 | — | hecha (2026-10-03; PR #25, fusionado en `dev` sin squash: `ddcf7de`) |
| 14 | [`F1-04-cli-cierre-e-informe`](F1-04-cli-cierre-e-informe.md) | 💻 | F1 | D | T1.17–T1.19 | — | hecha (2026-10-02; sobre `dev` @ `ddcf7de`, cierre `d5228ac`) |
| 15 | [`F1-05-jhoan-parada`](F1-05-jhoan-parada.md) | 🧑 | F1 | — | — | — | hecha (2026-10-03, Jhoan contestó P1–P7) |
| 16 | [`F1-06-cli-ajustes-previos-a-f2`](F1-06-cli-ajustes-previos-a-f2.md) | 💻 | F1 | ajustes | A1–A6 (código: candados, cobertura a informe, cableado, marca de `Estado`, R9.4.d, corpus) | complejo | hecha (2026-10-03; A1–A6 `0689b4e`…`7937772`, cierre `596fc13`, en `dev`) |
| 17 | [`F9-03-cli-procesos-plataforma-y-acceso`](F9-03-cli-procesos-plataforma-y-acceso.md) | 💻 | F9 | B1 | T9.13–T9.16 (P1, P2, P3 con el reintento de `WithTx`, P9) | — | hecha (2026-10-03; `679ea52`, `052089e`, `8febd52`, `250916a`; rama `reorg/f9-b1`, por PR a `dev`) |
| 18 | [`F9-04-cli-procesos-de-negocio`](F9-04-cli-procesos-de-negocio.md) | 💻 | F9 | B2 | T9.17–T9.21, T9.35, T9.22 (P4–P8, P10; cierre de B1 y B2) | — | hecha (2026-10-03; `e3fc0de`, `49d6d9e`, `39c38be`, `9da68f7`, `0a2762d`, `56b99f6`, `2bb7989`; suite P0–P10 `RC=0 · PASS=537 · SKIP=0` por binario) |
| 19 | [`F2-01-web-inventario-y-hojas`](F2-01-web-inventario-y-hojas.md) | 🌐 | F2 | inventario + hojas | T2.1, T2.34, T2.2–T2.3, T2.5–T2.9, T2.17–T2.21 | simple | hecha (2026-10-04; `1d73874` … `354f060`; `GATE_RC=0`, 0 SKIP; ≈ 81 min activos, D-R-6) |
| 20 | [`F2-02-web-usecase-identity`](F2-02-web-usecase-identity.md) | 🌐 | F2 | `usecase`, `identity` | T2.10–T2.11, T2.16, T2.22–T2.23 | medio | hecha (2026-10-04; `5a686b8` … `fc8aa87`; `GATE_RC=0`, 0 SKIP; ≈ 60 min de pared, D-R-6) |
| 21 | [`F2-03-web-postgres-http-platformadmin`](F2-03-web-postgres-http-platformadmin.md) | 🌐 | F2 | Postgres, HTTP, `platformadmin` | T2.4, T2.12–T2.15, T2.24–T2.27 | complejo | hecha (2026-10-04; `7da5367` … `99336f3`; `GATE_RC=0`, 0 SKIP; pre-chequeo Postgres 148 PASS; ≈ 90 min de pared, D-R-6) |
| 22 | [`F2-04-web-bridge-conmutar-y-rutas`](F2-04-web-bridge-conmutar-y-rutas.md) | 🌐 | F2 | `bridge_iam` + conmutar + rutas | T2.28–T2.31 (= TX.5–TX.7) | simple + medio | hecha (2026-10-04; `94f2d26` … `8482dad`; `GATE_RC=0`, 0 SKIP; huella igual; pre-chequeo P2/P10 contra el binario nuevo 94 PASS; ≈ 65 min de pared, D-R-6) |
| 23 | [`F2-05-cli-cierre`](F2-05-cli-cierre.md) | 💻 | F2 | cierre | T2.32–T2.33 (= T9.23) | — | hecha (2026-10-04; `8677404` … `73b4541`; `GATE_RC=0`, 0 SKIP; suites 149 PASS y procesos 667 PASS × 2 binarios; 131 mutantes, 2 vivos 🟡; **F2 cerrada**; ≈ 47 min de pared, D-R-6) |
| 24 | [`F3-01-web-inventario-y-hojas`](F3-01-web-inventario-y-hojas.md) | 💻 | F3 | inventario + hojas | T3.1–T3.9, T3.15–T3.18 | medio / complejo | hecha (2026-10-04; `3ae565c` … `8a4dd34`, 29 commits de código; `ci-local` rc=0, 390 PASS y 0 SKIP en `edge` + candados; 0 pendientes; 6 suites verdes contra sus dobles; 132 mutantes, 1 vivo 🟡 equivalente; ≈ 55 min de pared, D-R-6) |
| 25 | [`F3-02-web-fleet-filtercfg`](F3-02-web-fleet-filtercfg.md) | 💻 | F3 | `fleet`, `filtercfg` | T3.10, T3.11, T3.19, T3.20 | complejo / medio | hecha (2026-10-04; `d7323b7` inventario y `532e62f` … `b86dd62`, 12 commits de código; `ci-local` rc=0, 319 PASS y 0 SKIP en `fleet` + `filtercfg`; 0 pendientes; `ContratoRepository` verde contra `Memoria`; 40 mutantes en lo complejo y `filtercfg`, 0 vivos; ≈ 55 min de pared, D-R-6) |
| 26 | [`F3-03-web-grpc`](F3-03-web-grpc.md) | 💻 | F3 | `grpc` | T3.12–T3.14, T3.21–T3.23 | complejo | ✅ **tanda 1 de 3 hecha** (2026-10-04; `b1a408b` inventario aprobado y `dd4f984` … `a0baf00`, 11 commits de código: T3.12 y T3.21; `ci-local` rc=0, 0 SKIP en `grpc`; 125 mutantes, 3 vivos equivalentes; ≈ 70 min de pared, D-R-6). **Tanda 2 de 3 hecha** (2026-10-05, rama `reorg/f3-03-grpc-tandas-2-3`; `f0019af` … `84c83f6`, 11 commits de código: T3.13 y T3.22; `ci-local` rc=0, 249 tests y 0 SKIP en `grpc`; 221 mutantes, 2 vivos equivalentes; ≈ 75 min de pared, D-R-6). **Tanda 3 de 3 hecha** (2026-10-05, rama `reorg/f3-03-grpc-tanda-3`; `185dcbc` … `c7a61a1`, 12 commits de código: T3.14 y T3.23, más 8 de test de los mutantes, `3758144` … `231c74b`; `ci-local` rc=0, 342 tests y 0 SKIP en `grpc`; 323 mutantes, 7 vivos equivalentes y 1 hueco abierto, hallazgo 56; sesión cortada y relanzada). ✅ **F3-03 hecha** |
| 27 | [`F3-04-web-bridge-conmutar-y-rutas`](F3-04-web-bridge-conmutar-y-rutas.md) | 💻 | F3 | `bridge_gateway` + conmutar + rutas | T3.24–T3.28 (TX.8–TX.11) | simple + cara | ✅ **hecha** (2026-10-06; `dd4cbd2` … `0ebb743`, 13 commits de código; `ci-local` rc=0, gate de la ficha rc=0 con 0 SKIP; T3.27/TX.10 `[~]`: el e2e es de F3-05) |
| 28 | [`F3-05-cli-cierre-mtls`](F3-05-cli-cierre-mtls.md) | 💻 | F3 | cierre, mTLS | T3.29–T3.30 (= T9.24) y la parte local de T3.27 | — | ✅ **hecha** (2026-10-06; `8121564` … `876b096`, 11 commits de test; `make test-procesos` viejo y nuevo `RC=0 · PASS=812 · SKIP=0`; `ci-local` rc=0; **F3 cerrada**) |
| 29 | [`F45-01-web-inventario-e-inferencia`](F45-01-web-inventario-e-inferencia.md) | 💻 | F4 + F5 | inventario de las dos + F4 entero | T4.1–T4.9, T4.11–T4.23 · T5.1 | medio / complejo | ✅ **hecha** (2026-10-07; `2783172` … `24f6450`, 2 commits de inventario, 7 de rojo y 18 de verde; inventarios E-12 de F4 y F5 aprobados; `internal/modulos/inferencia` entero en verde, sin conmutar; `ci-local` rc=0, 621 PASS y 0 SKIP con `-race`; 181 mutantes, 5 vivos equivalentes) |
| 30 | [`F45-02-web-conmutar-inferencia-y-catalogo`](F45-02-web-conmutar-inferencia-y-catalogo.md) | 💻 | F4 + F5 | `bridge_inferencia` + conmutar F4 · F5 entero | T4.10, T4.24–T4.28 (TX.12–TX.14) · T5.2–T5.19 (TX.15) | simple / medio | ✅ **hecha** (2026-10-07; `65f5348` … `13e869f`, 29 commits: 8 de rojo, 16 de verde, 3 de conmutar y 2 de test; F4 conmutada, `FaseActual = 4`, 33 rutas en la cara nueva, `bridge_gateway.go` borrado; F5 entero en verde y conmutación nominal, `FaseActual = 5`; huella igual; `Conmutados` es `{"acceso","edge"}` desde el cierre —D-F3-14: `internal/arranque/session_identity_test.go` borrado—; 42 mutantes en `indice/cache.go`, 1 vivo equivalente; gates en el bloque de la sesión en [`ESTADO.md`](../../ESTADO.md)) |
| 31 | [`F45-03-cli-cierre`](F45-03-cli-cierre.md) | 💻 | F4 + F5 | cierre de las dos | T4.29–T4.31 · T5.20–T5.21 · T9.25, T9.26 | — | ✅ **hecha** (2026-10-07; `ddb8baa`, `59a3e6b`, 2 commits de test; `make test-procesos` contra el nuevo `RC=0 · PASS=869 · SKIP=0`; `ci-local` rc=0; **F4 y F5 cerradas**) |
| 32 | [`F6-01-web-inventario-y-hojas`](F6-01-web-inventario-y-hojas.md) | 💻 | F6 | inventario + hojas | T6.1–T6.5, T6.14 | simple | ✅ **hecha** (2026-10-07; inventario E-12 aprobado `27a7924`; `sigv1`, `note.go` y `tenantvars` en verde, `98f9220` … `06879cb`; `ci-local` rc=0, 0 SKIP; rama `reorg/f6-01-inventario-y-hojas`, por PR a `dev`) |
| 33 | [`F6-02-web-intakes-1`](F6-02-web-intakes-1.md) | 💻 | F6 | `intakes` 1/2 | T6.6–T6.8, T6.15 | medio | ✅ **hecha** (2026-10-08; 40 ficheros de `S/intakes` con contrato y test, 9 tipos puros en verde, suite `intakeshelpertest` escrita; `113912e` … `c366c68`; `ci-local` rc=0; 0 SKIP en el código nuevo; `PENDIENTES=108`; rama `reorg/f6-02-intakes-contratos-y-tipos`, PR hacia `dev`) |
| 34 | [`F6-03-web-intakes-2`](F6-03-web-intakes-2.md) | 💻 | F6 | `intakes` 2/2 y candados | T6.9, T6.16–T6.18 | complejo | ✅ **hecha** (2026-10-08; `S/intakes` entero en verde: `PENDIENTES=0 · ROJOS=1`; suite `intakeshelpertest` 50/50 en memoria y, como pre-chequeo, contra Postgres; `e8f1e10` … `4fcccc6`; `ci-local` rc=0; 0 SKIP en el código nuevo; hallazgos 24–34 de F6) |
| 35 | [`F6-04-web-quotetext-integrations-crmpush`](F6-04-web-quotetext-integrations-crmpush.md) | 💻 | F6 | `quotetext`, `telemetria`, `integrations`, `crmpush` | T6.10–T6.13, T6.19–T6.21 | medio / complejo | ✅ **hecha** (2026-10-08; los cuatro paquetes en verde y `S` sin pendientes: `PENDIENTES=0 · ROJOS=1`; suite `integrationshelpertest` 61/61 con el doble `Memoria`, escrita y sin correr contra Postgres; candado R-12 y esquema `wapp-crm-v1` verdes; `31b9343` … `af68fe3`; `ci-local` rc=0; 0 SKIP en el código nuevo; hallazgos 35–44 de F6) |
| 36 | [`F6-05-web-cara-http-y-conmutar`](F6-05-web-cara-http-y-conmutar.md) | 💻 | F6 | cara HTTP + conmutar | T6.22–T6.26 (= TX.16–TX.18) | cara | pendiente |
| 37 | [`F6-06-cli-cierre`](F6-06-cli-cierre.md) | 💻 | F6 | cierre | T6.27–T6.29 (T6.27 = T9.27) | — | pendiente |
| 38 | [`F7-01-web-inventario-y-hojas`](F7-01-web-inventario-y-hojas.md) | 💻 | F7 | inventario + hojas | T7.1–T7.6, T7.14–T7.15 | medio / complejo | pendiente |
| 39 | [`F7-02-web-stages`](F7-02-web-stages.md) | 💻 | F7 | `stages` | T7.7–T7.9, T7.16–T7.17 | medio | pendiente |
| 40 | [`F7-03-web-pipeline-reanalisis`](F7-03-web-pipeline-reanalisis.md) | 💻 | F7 | `pipeline`, `intakeahead`, `reanalisis` | T7.10–T7.13, T7.18–T7.20 | complejo | pendiente |
| 41 | [`F7-04-web-cara-http-y-conmutar`](F7-04-web-cara-http-y-conmutar.md) | 💻 | F7 | cara HTTP + `bridge_captacion` + conmutar | T7.21–T7.26 (= TX.19–TX.21) | simple + cara | pendiente |
| 42 | [`F7-05-cli-cierre`](F7-05-cli-cierre.md) | 💻 | F7 | cierre | T7.27–T7.29 (T7.27 = T9.28) | — | pendiente |
| 43 | [`F8-01-cli-inventario-y-hojas`](F8-01-cli-inventario-y-hojas.md) | 💻 | F8 | inventario + hojas | T8.1–T8.8, T8.22 | medio | pendiente |
| 44 | [`F8-02-cli-motor`](F8-02-cli-motor.md) | 💻 | F8 | motor | T8.9–T8.11, T8.14, T8.23 | medio | pendiente |
| 45 | [`F8-03-cli-events-admin-cart`](F8-03-cli-events-admin-cart.md) | 💻 | F8 | `events`, `cart` (`admin` va en F8-06) | T8.12, T8.15–T8.17, T8.24, T8.25 | medio | pendiente |
| 46 | [`F8-04-cli-runtime-1`](F8-04-cli-runtime-1.md) | 💻 | F8 | `runtime`: contratos y soporte | T8.18–T8.21, T8.26 | complejo | pendiente |
| 47 | [`F8-05-cli-runtime-2`](F8-05-cli-runtime-2.md) | 💻 | F8 | `runtime`: núcleo, con mutantes | T8.27–T8.28 | complejo | pendiente |
| 48 | [`F8-06-cli-cara-http-y-conmutar`](F8-06-cli-cara-http-y-conmutar.md) | 💻 | F8 | `admin`, cara HTTP, conmutar, retirar adaptadores | T8.13, T8.29–T8.35 (TX.22–TX.24) | cara | pendiente |
| 49 | [`F8-07-cli-cierre`](F8-07-cli-cierre.md) | 💻 | F8 | cierre | T8.36–T8.38 (T8.37 = T9.29) | — | pendiente |
| 50 | [`F9-05-cli-cierre`](F9-05-cli-cierre.md) | 💻 | F9 | D | T9.30–T9.33 — condición del relevo | — | pendiente |
| 51 | [`F10-01-jhoan-decisiones`](F10-01-jhoan-decisiones.md) | 🧑 | F10 | — | — | — | pendiente |
| 52 | [`F10-02-cli-dorada`](F10-02-cli-dorada.md) | 💻 | F10 | A | T10.1–T10.3 | — | pendiente |
| 53 | [`F10-03-cli-prueba-uat`](F10-03-cli-prueba-uat.md) | 💻 | F10 | B | T10.4–T10.6 (UAT por SSH, ventana ≥ 24 h) | — | pendiente |
| 54 | [`F10-04-cli-relevo`](F10-04-cli-relevo.md) | 💻 | F10 | C | T10.7–T10.14 (TX.25) | — | pendiente |
| 55 | [`F10-05-cli-cierre-y-fuera-del-repo`](F10-05-cli-cierre-y-fuera-del-repo.md) | 💻 | F10 | D/E | T10.15–T10.21 | — | pendiente |
| 56 | [`F10-06-cli-main`](F10-06-cli-main.md) | 💻 | F10 | F | T10.22 — solo a petición de Jhoan | — | pendiente |
