# Estado de la reorganización modular — punto de retoma

> **Última actualización: 2026-10-01** (revisión independiente de S9–S11, sobre `dev` @ `6650e55`). Antes, al cerrar la sesión **F1-01** (🌐 · F1 · bloque A, contratos y rojo de `nucleo/contact`: PR #19, **integrado en `dev` sin squash**, merge `6650e55`). Este fichero es
> para **retomar**: dónde estamos, qué está decidido, qué falta decidir y cuál es el siguiente paso.
> Cada sesión de ejecución lo actualiza al cerrar (fase, bloque, siguiente paso, SHA).

## Dónde estamos

**Fase: F0 (andamiaje) ✅ cerrada el 2026-09-30 · F9 bloque A (el arnés) ✅ cerrado el 2026-10-01 (F9-01 🌐 + F9-02 💻), con una intermitencia de P0 diferida a F6 (H-1) · **F1 bloque A (contratos y rojo de `nucleo/contact`) escrito el 2026-10-01 (F1-01 🌐), PR #19 integrado en `dev` (`6650e55`)** · siguiente: F1-02 (el verde).** El árbol
nuevo tiene el andamiaje (`pendiente`, `candados`, `arranque`, `apipublica` vacía, `cmd/server-modular`) y, **en rojo**,
`internal/nucleo/contact` (4 contratos sin lógica: 11 `pendiente.Implementar` y 4 tests tras la etiqueta `pendiente`; del paquete
`contacttest`, el doble `EstadoMemoria` ya está en verde): **ningún módulo** de `internal/modulos/` existe aún. Existe el **plan ejecutable** en [`plan/`](plan/README.md): el marco común,
una *spec* por fase (F0–F10 y la transversal FX), el registro de decisiones y **81 sesiones** con
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

**Siguiente paso:**
1. **Jhoan**:
   - revisar las contradicciones 23, 24 y 27 del README de F0 (y la 19 de F0-04; la 27 bloquea F2, no F0);
   - para repetir el gate local: `GOTOOLCHAIN=go1.26.5` y `golangci-lint v2.12.2` en el `PATH`
     (`GOTOOLCHAIN=go1.26.5 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`).
2. **F1-01 ✅ hecha (🌐, 2026-10-01)**: bloque A del piloto `nucleo/contact`, T1.1–T1.7 + T1.3b; **PR #19 integrado en `dev` sin squash** (merge `6650e55`, 2026-10-01:
   rojo y verde de `candados` y `nucleo` siguen siendo commits distintos). **Jhoan**: decidir D-F1-7 y D-F1-8 del README de F1 (la 8 bloquea T1.13, bloque B) y mirar las que abre la revisión independiente (D-F1-10…12 y D-F9-6…11). Luego **F1-02 (🌐)**: el verde,
   T1.8–T1.13 ([`plan/sesiones/`](plan/sesiones/README.md)). Las F9-03…F9-05 (B1/B2) van **después** de la parada de F1 (F1-05), por D-F9-1. Para repetir los gates en local: `GOTOOLCHAIN=go1.26.5` y `golangci-lint v2.12.2` en el `PATH`.
3. **H-1 no bloquea nada**: se difirió a F6 (D-F6-7). Hasta entonces, un rojo de `TestP0_Arranque/sin_errores` con las dos líneas `ERROR` del *webhook worker* es esa carrera: se repite
   **una vez**, se compara y se anota (≈ 1–2 % de falso rojo por corrida). ⚠️ Revisión independiente (2026-10-01): la regla se **matiza**
   —vale para un rojo cuyas líneas `ERROR` sean todas de cancelación, de una goroutine de fondo y de la parada, no solo para «las dos
   del worker»— en la [contradicción 19 del README de F9](plan/F9-procesos/README.md).

## Avance de la ejecución

| Fase | Estado | Último bloque cerrado | SHA |
|---|---|---|---|
| F0 | ✅ cerrada (2026-09-30) | F · cierre local (T0.22–T0.25) | A: `98e806d`, `de04088`. B: `d7600d3`, `f3b322c`, `d74dd7f`, `d05ac3a`. C: `3040e82`, `2c2bbd6`, `b2ecfce`, `65d4bc0`, `e61567e`, `681d84e`, `d48e319`, `42884fb`, `b522b0f`, `ca462a6`, `3e85144` (en `dev` @ `80807ba`). D: `d64dbbf`, `a953834`, `c7ae487`, `141d960`, `fde5849`, `61ce04b` (en `dev` @ `d3deb27`). E: `8096232`, `5a11f5b`, `6d83620`, `b65b788`, `5305134`, `de0c29b`, `9dcf7e8`, `7b7e01f`, `dd1e2bd`, `15223ff` (en `dev` @ `835a7be`, PR #17). F: T0.22 y T0.23 sin commit (evidencia en el `CERRADO` del traspaso), T0.24 `835a7be` verificado, T0.25 `d3b3f3f` |
| F9-A (adelantado) | ✅ cerrado (2026-10-01): escrito en la web (F9-01), cerrado en local (F9-02); H-1 (intermitencia de P0) diferida a F6 | T9.1–T9.12 | `a374cdb`, `37c7db7`, `f300aff`, `b5f1601`, `576ba9a`, `2e2ecc1`, `7c63d9e`, `660947d`, `fa03e6b`, `5519343`, `10179d6`, `6ee1c5e` (en `dev` por el merge `af7b8e9`, PR #18); el cierre local es solo documental, en **tres** commits: `ac8ac5f` (el cierre), `79c7160` (su SHA) y `77df20f` (H-1 diferida a F6: ahí vive D-F6-7) |
| F9-B (adelantado) | pendiente | — | — |
| F1 | en curso: bloque A (rojo) escrito e **integrado en `dev`** (PR #19, merge `6650e55`, 2026-10-01) | A · contratos y rojo (T1.1–T1.7 + T1.3b) | T1.1 `afa63f3` · T1.2 `b37a8c8` · T1.3 `d915d41` (+ `b001c35`) · T1.3b `68897a8`, `776d6a2` · T1.4 `8f2a4db` · T1.5 `89b223b` · T1.6 `32b7bfb` · cierre del bloque `b9dd1e7` · tras el cierre: E-11 `8365132` y el troceo de `contacttest/contrato.go` `7069532`; sobre `origin/dev` @ `77df20f`, rama `reorg/f1-a-contratos-rojo`. Gate `ci-local` `GATE_RC=0` (86 líneas `ok`, 0 issues); `make test-pendiente` `PENDIENTES=11` `ROJOS=4`; 0 SKIP |
| F2–F8 | pendiente | — | — |
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

⚠️ **Todavía sin fila en `DECISIONES.md`** (viven en el README de su fase): **D-F1-7, D-F1-8 y D-F1-9** (de F1-01) y las que abre la
revisión independiente del 2026-10-01: **D-F1-10** (estrechar la exención de los paquetes `…test`), **D-F1-11** (R-27/R-28/R-29 a P3),
**D-F1-12** (actualizar `05` por E-11) en el [README de F1](plan/F1-nucleo-contact/README.md), y **D-F9-6…D-F9-12** (candado `SinBDViva`,
`GOWORK=off`, directorios `procesos-*`, las dos respuestas al §8 del traspaso, el alcance de D-F6-7, los ficheros de test largos y el gate de lease de la inferencia en el Edge de prueba) en el
[README de F9](plan/F9-procesos/README.md).

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

- **F1-01 (bloque A de F1)**: rama `reorg/f1-a-contratos-rojo`, partida de `origin/dev` @ `77df20f`; **PR #19 integrado en `dev` sin squash** (merge `6650e55`, 2026-10-01 22:52 −03; 12 commits, de `afa63f3` a `7069532`). El `dev` local de la VM web iba 17 commits por detrás: se trabajó siempre desde `origin/dev`.
- **Revisión independiente de S9–S11** (2026-10-01): rama `reorg/revision-s9-s11`, partida de `dev` @ `6650e55` (PR hacia `dev`, **pendiente de revisar por Jhoan**; integrar sin squash). Commits: arriba, «Revisión independiente».
- `origin/dev` contiene **F0 entera** (PR #13–#17, sin squash; el último merge es `835a7be`) más el cierre de F0-06.
  `origin/main` = `2da10b4`, sin tocar. Traspaso de F0: [`traspasos/TRASPASO-F0-andamiaje.md`](traspasos/TRASPASO-F0-andamiaje.md), **CERRADO**.
- `origin/dev` contiene también el bloque A de F9 (PR #18, sin squash; el merge es `af7b8e9`; la rama `reorg/f9-a-arnes` ya está borrada). El cierre local
  de F9-02 son **tres** commits solo de documentación sobre `dev` (`git log --oneline af7b8e9..77df20f`: `ac8ac5f`, `79c7160` y `77df20f`). Traspaso: [`traspasos/TRASPASO-F9-arnes.md`](traspasos/TRASPASO-F9-arnes.md), **CERRADO**.

## Para retomar

1. Lee [`plan/README.md`](plan/README.md) y, si vas a ejecutar, el fichero de tu sesión en
   [`plan/sesiones/`](plan/sesiones/README.md) (él te dice qué más leer).
2. La norma: [`05-metodo-contratos-y-tdd.md`](05-metodo-contratos-y-tdd.md).
3. Skills del repo: `reconstruir-modulo`, `contrato-tdd`, `validar-antes-de-cerrar`,
   `traspaso-web-local`, `procesos-testcontainers`.
