# Estado de la reorganización modular — punto de retoma

> **Última actualización: 2026-10-05** (**F3-03, tanda 3 de 3 hecha: `grpc` entero**: `inference` (en 3 trozos), `plaza` y `greeting` en verde, el `InferenceResult` se entrega *inline*, el saludo cierra el job del latido y las inferencias en vuelo se cancelan al caer el stream; rama `reorg/f3-03-grpc-tanda-3`, PR hacia `dev`; siguiente paso, **F3-04** 💻. Antes, el mismo día: **F3-03, tanda 2 de 3 hecha**: `connect` (en 4 trozos), `auth`, `config_push`, `readiness` y `diagnostics` de `grpc` en verde, rama `reorg/f3-03-grpc-tandas-2-3`, PR hacia `dev`; siguiente paso, **relanzar F3-03** 💻 para la tanda 3. Antes, 2026-10-04: **F3-03, tanda 1 de 3 hecha**: inventario E-12 de `grpc` aprobado y `types`, `server`, `receipt_sink`, `worklane` y `send` en verde, rama `reorg/f3-03-grpc`, PR hacia `dev`; siguiente paso, **relanzar F3-03** 💻 para las tandas 2 y 3. Antes: **F3-02 hecha**: `fleet` y `filtercfg` de `edge` en verde, rama `reorg/f3-02-fleet-filtercfg`, PR hacia `dev`; siguiente paso, **F3-03** 💻. Antes: **F3-01 hecha**: inventario E-12 aprobado y las 7 hojas de `edge` en verde, rama `reorg/f3-01-inventario-hojas`, PR hacia `dev`; siguiente paso, **F3-02** 💻. Antes: **F2 cerrada e integrada**: los PR #33 (F2-05) y #34 (las siete 🟡 de F2 decididas, D-F2-10…D-F2-13) están en `dev`, merge `4f0ed06`. 🔴 **Desde hoy todo es local (D-R-8)**: se acabó la promoción web; toda sesión lee `PROTOCOLO-CLI.md`; sigue valiendo rama + PR. Siguiente paso, **F3-01** 💻). Antes, 2026-10-04 (**F2-05 hecha, F2 cerrada**: cierre local con las suites y los mutantes del nivel complejo contra Postgres, los procesos contra los dos binarios, el arranque real y las tres peticiones; el PR #32 de F2-04 está integrado en `dev`, merge `bfd31ce`; siguiente paso, **F3-01**). Antes, 2026-10-04 (**F2-04 hecha**: `bridge_iam.go`, la cara nueva de `acceso` en `apipublica` y `conmutar(acceso)`: el arranque nuevo cablea `acceso` y muda 23 rutas, huella igual; siguiente paso, **F2-05** 💻). Antes, 2026-10-04 (**F2-03 hecha**: `entitlements/postgres.go`, `iam/infra/postgres`, `iam/transport/http` y `platformadmin` en verde, **0 pendientes en `acceso`**, los 3 candados AST verdes y las suites contra Postgres escritas; siguiente paso, **F2-04**). Antes, 2026-10-04 (**F2-02 hecha**: `iam/infra/identity` e `iam/usecase` de `acceso` en verde, 0 pendientes en los dos paquetes; siguiente paso, **F2-03**). Antes, 2026-10-04 (**F2-01 hecha**: inventario E-12 de `acceso` aprobado por Jhoan, `auth.go` partido y las hojas simples de `acceso` en verde —`entitlements` sin `postgres.go`, `iam/domain`, `iam/ports/{in,out}`, las 7 suites de `outhelpertest` e `iam/infra/memory`—; siguiente paso, **F2-02**). Antes, 2026-10-03 (**F9-04 hecha**: bloque B2 de F9 —P4–P8 y P10, más T9.22— verde contra los dos binarios, suite entera P0–P10 `RC=0 · PASS=537 · SKIP=0`; siguiente paso, **F2-01**). Antes, 2026-10-03 (**F9-03 hecha**: bloque B1 de F9 —P1, P2, P3 y P9— verde contra los dos binarios y el mutante `maxTxAttempts = 1` cae; siguiente paso, **F9-04**). Antes, 2026-10-03 (**recalibración del plan tras el piloto**: specs F2–F10 y FX y `plan/sesiones/` alineadas con P1–P7; 81 sesiones → 56; siguiente paso, **F1-06**, ajustes de código previos a F2). Antes, 2026-10-03 (**parada de F1 resuelta**: Jhoan contestó P1–P7, `04` y `05` corregidos —`05` E-12 y §4.2—, skills y `CLAUDE.md` al día; ver `plan/DECISIONES.md` §3 y la §10 del informe). Antes, 2026-10-02 (noche, −03) (sesión **F1-04** 💻, bloque D de F1, el cierre local del piloto `nucleo/contact` sobre `dev` @ `ddcf7de`: T1.17–T1.19, [`informe-piloto.md`](plan/F1-nucleo-contact/informe-piloto.md) escrito, los dos traspasos de F1 **CERRADOS**; última fila de «Qué se hizo» y paso 2d de «Siguiente paso». **F1 espera la PARADA (T1.20)**). Antes, 2026-10-03 (UTC) (sesión **F1-03** 🌐, bloque C de F1, el adaptador `bridge_contact.go` y la conmutación de `nucleo/contact`, rama `reorg/f1-c-adaptador` sobre `origin/dev` @ `61a3c8b`: última fila de «Qué se hizo» y paso 2c de «Siguiente paso»). Antes, 2026-10-02 (sesión **F1-02** 🌐, bloque B de F1, el verde de `nucleo/contact`, rama `reorg/f1-b-verde`, que trae `dev` @ `0a377bc` —con el PR #22, D-F9-11— por el merge `a5d17b5`: última fila de «Qué se hizo» y paso 2b de «Siguiente paso»). Antes, (aplicadas las recomendaciones de la revisión independiente de S9–S11 en siete decisiones, rama `reorg/decisiones-revision-s9-s11`: última entrada de «Dónde estamos»). Antes, el mismo día, D-F1-10 aplicada en la rama de la revisión, `reorg/revision-s9-s11`, PR #20: el sufijo que exime a suites y dobles es `helpertest`. Antes, el 2026-10-01, la revisión independiente de S9–S11, sobre `dev` @ `6650e55`; y antes, al cerrar la sesión **F1-01** (🌐 · F1 · bloque A, contratos y rojo de `nucleo/contact`: PR #19, **integrado en `dev` sin squash**, merge `6650e55`). Este fichero es
> para **retomar**: dónde estamos, qué está decidido, qué falta decidir y cuál es el siguiente paso.
> Cada sesión de ejecución lo actualiza al cerrar (fase, bloque, siguiente paso, SHA).

## Dónde estamos

**Fase: F0 (andamiaje) ✅ cerrada el 2026-09-30 · F9 bloque A (el arnés) ✅ cerrado el 2026-10-01 (F9-01 🌐 + F9-02 💻), con una intermitencia de P0 diferida a F6 (H-1) · **F1 bloque A (contratos y rojo de `nucleo/contact`) escrito el 2026-10-01 (F1-01 🌐), PR #19 integrado en `dev` (`6650e55`)** · siguiente: F1-02 (el verde).** El árbol
nuevo tiene el andamiaje (`pendiente`, `candados`, `arranque`, `apipublica` vacía, `cmd/server-modular`) y, **en rojo**,
`internal/nucleo/contact` (4 contratos sin lógica: 11 `pendiente.Implementar` y 4 tests tras la etiqueta `pendiente`; del paquete
`contacthelpertest` (✎ D-F1-10, 2026-10-02: antes `contacttest`, y así lo nombran las entradas anteriores a esa fecha), el doble `EstadoMemoria` ya está en verde): **ningún módulo** de `internal/modulos/` existe aún. Existe el **plan ejecutable** en [`plan/`](plan/README.md): el marco común,
una *spec* por fase (F0–F10 y la transversal FX), el registro de decisiones y **56 sesiones** (eran 81 hasta la recalibración del 2026-10-03) con
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
- ✎ **D-R-1…D-R-6 decididas el 2026-10-03** (Jhoan: se aplica la recomendación de cada una; un commit por decisión en `dev`). D-R-5 ya no bloquea F2-01, ni D-R-1 a F2-03. Siguen abiertas D-F9-9 y D-F9-10, que no tienen recomendación firme.

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
  una sesión que ya reconectó. 🔴 El nuevo se aparta del viejo. Siguen abiertos el 38 y el 40.
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
| F3 | 🔄 **en curso** (F3-01 y F3-02 hechas e integradas el 2026-10-04 💻: PR #35 y PR #36, `dev` @ `c851591`; **F3-03, tanda 1 de 3** integrada, PR #37, `dev` @ `cbc5736`; **tanda 2 de 3** integrada, PR #38, `dev` @ `ec236b3`; **tanda 3 de 3, F3-03 hecha**, integrada, PR #39, `dev` @ `414b31b`; faltan F3-04 y F3-05) | F3-01 · T3.1–T3.9, T3.15–T3.18 · F3-02 · T3.10, T3.11, T3.19, T3.20 · F3-03 · T3.12–T3.14, T3.21–T3.23 | `3ae565c` y `bcd0f4e` … `8a4dd34` (F3-01) · `d7323b7` y `532e62f` … `b86dd62` (F3-02, 12 commits de `edge`) · `b1a408b` y `dd4f984` … `a0baf00` (F3-03 tanda 1, 11 commits de `edge`) · `f0019af` … `84c83f6` (F3-03 tanda 2, 11 commits de `edge`) · `185dcbc` … `231c74b` (F3-03 tanda 3, 20 commits de `edge`) |
| F4–F8 | pendiente | — | — |
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

0. **Siguiente sesión**: **F3-04** 💻 (`edge`: `bridge_gateway`, cara nueva y conmutación; T3.24–T3.28), en [`plan/sesiones/`](plan/sesiones/README.md), cuando el PR de la tanda 3 de F3-03 esté integrado en `dev`. `grpc` está entero (F3-03 hecha el 2026-10-05). **F2 está cerrada e integrada** (2026-10-04, `dev` @ `4f0ed06`). Todas las sesiones son locales (D-R-8).
1. Lee [`plan/README.md`](plan/README.md) y, si vas a ejecutar, el fichero de tu sesión en
   [`plan/sesiones/`](plan/sesiones/README.md) (él te dice qué más leer).
2. La norma: [`05-metodo-contratos-y-tdd.md`](05-metodo-contratos-y-tdd.md).
3. Skills del repo: `reconstruir-modulo`, `contrato-tdd`, `validar-antes-de-cerrar`,
   `traspaso-web-local`, `procesos-testcontainers`.
