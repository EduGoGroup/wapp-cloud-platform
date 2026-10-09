# F9 · Tareas

> Skills: `procesos-testcontainers` (todas), `validar-antes-de-cerrar` (gates),
> `traspaso-web-local` (solo el bloque A, o si una sesión se corta), `contrato-tdd` (solo para leer cómo nace una suite).
> **🕐** = la tarea cambia si Jhoan **no** acepta adelantar F9 (D-F9-1): ver T9.34.
> **Entorno**: el bloque A fue 🌐→💻 (hecho). **B1, B2, C y D son 💻, solo local** (P3, 2026-10-03): la misma sesión
> escribe, corre y cierra; sin pre-chequeo web ni traspaso.
> Gate en todas: `GOWORK=off make ci-local > /tmp/g.log 2>&1; echo "GATE_RC=$?" >> /tmp/g.log; tail -1 /tmp/g.log` → `GATE_RC=0`, con 0 `--- SKIP`.
> Gate de procesos: `make test-procesos` (nace en T9.4), `RC=0` por binario leído del log.
> Sin umbral de cobertura (P2): los procesos son parte de lo que lo sustituye.
> Cierre de cada sesión (`05` E-12): tareas `[x]` con SHA, un bloque en `ESTADO.md`, hallazgos nuevos en el README.

## Bloque A · el arnés · 🌐→💻 · T9.1–T9.12 🕐

Entrada: F0 cerrada. Puede correr en paralelo con F1 (otra sesión). Para cuando: P0 verde contra los
dos binarios **en local** y `ci-local` rc=0 con el candado ampliado.

- [x] **T9.1 · docs: las decisiones de F9, escritas** · 🌐 · dep. — · cumple — — cerrada en `a374cdb` (sesión F9-01, 2026-10-01): 2026-09-30: las siete decisiones copiadas con su fecha en el README de F9; D-F9-1 = sí, las 🕐 no se reordenan
  - **Ficheros**: `plan/F9-procesos/README.md` (sección «Decisiones», con fecha y lo decidido)
  - **Hecho cuando**: D-13 y D-F9-1..D-F9-4 tienen respuesta de Jhoan escrita; si D-F9-1 = no, las
    tareas 🕐 se reordenan según `arquitectura.md` §5.3
  - **Gate**: revisión de enlaces a mano (`make check-docs` **no existe** en este repo: `grep -c check-docs Makefile` → 0, 2026-09-29; es un target de la raíz de wApp, que la web no ve)
  - **Commit**: `docs(reorganizacion-modular): F9, decisiones D-13 y D-F9`

- [x] **T9.2 · procesos(arnes): testcontainers-go en `go.mod`** · 🌐 · dep. T9.1 · cumple R9.1.a — cerrada en `37c7db7` (sesión F9-01, 2026-10-01): prefijo `chore(deps)` (decisión T-2), no `procesos(arnes)`; lleva además `test/procesos/deps_test.go` (imports en blanco) porque `go mod tidy` borra un `require` que nadie importa — T9.5 lo borró; `go 1.26.5` intacta, `go.sum` con red real, `go list -deps ./cmd/server | grep -c testcontainers` → 0, `ci-local` `GATE_RC=0` y `make test-integration` `rc=0` (sin `-v`: sin conteo de SKIP); suben `httpsnoop` 1.1.0, `otelhttp` 0.69.0 y `klauspost/compress` 1.18.6
  - **Ficheros**: `go.mod`, `go.sum` (`github.com/testcontainers/testcontainers-go` y `…/modules/postgres`, la última estable; `pgx/v5/stdlib` ya está)
  - **Hecho cuando**: `go 1.26.5` intacta en `go.mod`; `go.sum` generado **con red real**;
    `GOWORK=off go list -deps ./cmd/server | grep -c testcontainers` → 0 (no llega al binario)
  - **Gate**: gate de la web; `GOWORK=off go mod tidy && git diff --exit-code go.mod go.sum` (lo exige `ci.yml`)
  - **Commit**: `procesos(arnes): testcontainers-go para test/procesos`
  - Si F1 ya lo añadió (D-F1-2, T1.13): tarea **anulada** con esa referencia.

- [x] **T9.3 · procesos(arnes): el candado ampliado** · 🌐 · dep. T9.2 · cumple R9.2.a–c — cerrada en `f300aff` (sesión F9-01, 2026-10-01): `SinBDViva` persigue también `os.Environ`, `Skip`/`SkipNow`/`Skipf` (patrón `t.Skip`) y `testing.Short`; un solo commit (modifica un fichero ya verde) con el rojo medido en el cuerpo; `sinbdviva.go` 100 % por fichero
  - **Ficheros**: la lógica del candado que dejó F0 (`internal/candados/`, su `diseno.md` §4.4) y dos casos `muerde` nuevos en `internal/candados/testdata/sinbdviva/` (`os.Environ()`, `t.Skip`)
  - **Hecho cuando**: cada caso `muerde` hace fallar el detector; `test/procesos/` limpio pasa
  - **Gate**: gate de la web; `GOWORK=off go test -v ./test/procesos/ ./internal/candados/... 2>&1 | grep -c -- '--- SKIP'` → 0
  - **Commit**: `procesos(arnes): el candado sin_bd_viva prohíbe os.Environ y t.Skip`
  - ⚠️ **Revisión independiente (2026-10-01)**: `os.Environ` y `testing.Short` se esquivaban con un import con alias o con punto; corregido en `1c247f9` (`verde(candados)`, con el rojo medido en el cuerpo; `sinbdviva.go` sigue al 100 %). Lo que el candado sigue sin ver: contradicción 22 del README y D-F9-6.

- [x] **T9.4 · procesos(arnes): `make test-procesos`, `vet-integracion` y lint** · 🌐 · dep. T9.2 · cumple R9.7.a, R9.4.e — cerrada en `b5f1601` (sesión F9-01, 2026-10-01): `make test-procesos` (BINARIO, CUENTA, PROCESOS_LOG_DIR), `vet-integracion` dentro de `ci-local` y `run.build-tags: [integracion]`; probado con un test que falla adrede (retirado)
  - **Ficheros**: `Makefile` (target `test-procesos` de `diseno.md` §7; `vet-integracion` dentro de `ci-local`), `.golangci.yml` (`run.build-tags: [integracion]`)
  - **Hecho cuando**: `grep -n 'tags integracion' Makefile` ≥ 2 líneas; `make test-procesos` con un test que falla adrede sale ≠ 0 y deja `RC=` en su log (se comprueba y se retira el test trampa antes del commit)
  - **Gate**: gate de la web (`golangci-lint v2.12.2`)
  - **Commit**: `procesos(arnes): make test-procesos y vet con la etiqueta integracion`

- [x] **T9.5 · procesos(arnes): `TestMain` y la base por proceso** · 🌐→💻 · dep. T9.4 · cumple R9.1.a–d — cerrada en `576ba9a` (web, 2026-10-01) y **cerrada en local por F9-02 (💻, 2026-10-01; integrada en `dev` por el merge `af7b8e9`)**: `make test-procesos` viejo `RC=0 · PASS=146 · FAIL=0 · SKIP=0` (pasadas 2 y 3 y `CUENTA=3`: 438); 1 contenedor `postgres:17-alpine` durante la corrida y 0 a los ~15 s (R9.1.a, con su comando literal); R9.1.d medido con `HOME` vacío, porque `DOCKER_HOST=unix:///nada` **no** falla ni en macOS ni en la web (README de F9, contradicción 15)
  - **Ficheros**: `test/procesos/main_test.go` (si F1 · T1.13 lo creó, se **amplía**: compilar `cmd/migrate`, `cmd/server`, `cmd/server-modular`; `WAPP_PROCESOS_BINARIO`), `test/procesos/base_test.go`
  - **Hecho cuando**: web: `GOWORK=off go vet -tags integracion ./test/procesos/...` rc=0; local: un test mínimo `TestArnes_BasePorProceso` pasa y `docker ps` tras la corrida no deja contenedores
  - **Gate**: web → vet; local → `BINARIO=viejo make test-procesos` RC=0
  - **Commit**: `procesos(arnes): un Postgres por corrida y una base clonada por proceso`

- [x] **T9.6 · procesos(arnes): PKI y claves de prueba** · 🌐 · dep. T9.5 · cumple R9.3.a — cerrada en `2e2ecc1` (sesión F9-01, 2026-10-01): CA, certificado de servidor, lease, X25519, KEK y ES256 generados por corrida; sus tests hacen handshakes TLS 1.3 y mTLS reales por loopback
  - **Ficheros**: `test/procesos/pki_test.go`, `test/procesos/claves_test.go`
  - **Hecho cuando**: `grep -rn 'certs/\|\.env' test/procesos` vacío; un test del arnés parsea lo generado con `crypto/x509` y `crypto/ed25519`
  - **Gate**: vet `-tags integracion` rc=0
  - **Commit**: `procesos(arnes): PKI, lease, X25519, KEK y ES256 generados por corrida`

- [x] **T9.7 · procesos(arnes): dobles de S3 e identidad** · 🌐 · dep. T9.5 · cumple R9.3.b–c — cerrada en `7c63d9e` (sesión F9-01, 2026-10-01): S3 falso con endpoint IP y JWKS ES256 en loopback; el SDK real manda una sola `HEAD /<bucket>` path-style y `jwt.NewMultiVerifierFromJWKS` acepta el JWKS
  - **Ficheros**: `test/procesos/s3falso_test.go`, `test/procesos/identidad_test.go`
  - **Hecho cuando**: vet rc=0; los dos se levantan en `127.0.0.1` y registran lo que reciben
  - **Gate**: vet `-tags integracion` rc=0
  - **Commit**: `procesos(arnes): dobles de S3 e identity en el proceso de test`

- [x] **T9.8 · procesos(arnes): un servidor por proceso** · 🌐→💻 · dep. T9.6, T9.7 · cumple R9.1.e–f — cerrada en `660947d` (web, 2026-10-01) y **cerrada en local por F9-02 (💻, 2026-10-01; `dev` @ `af7b8e9`)**: `TestArnes_EntornoLimpio` (con `WAPP_DB_HOST=trampa`) y los dos servidores en paralelo pasan en viejo y nuevo; `address already in use`: 0 en 167 logs de la sesión (los 12 «puerto ocupado… reintento» son `TestArnes_ReintentoPuertoOcupado`, que ejerce ese camino adrede, uno por ejecución)
  - **Ficheros**: `test/procesos/servidor_test.go` (entorno de `diseno.md` §2, puertos libres, espera, parada, volcado de log)
  - **Hecho cuando**: local: `TestArnes_EntornoLimpio` pasa con `WAPP_DB_HOST=trampa` exportado; dos servidores en paralelo sin `address already in use`
  - **Gate**: local `BINARIO=viejo make test-procesos` RC=0
  - **Commit**: `procesos(arnes): el binario elegido, con base, puertos y entorno propios`

- [x] **T9.9 · procesos(arnes): clientes y fixtures** · 🌐 · dep. T9.8 · cumple R9.3.e — cerrada en `fa03e6b` (sesión F9-01, 2026-10-01): cliente con Context Token, `crearTenant`, fixtures sin puerta y `TestArnes_SinViaAPI` (24 casos); el canje contra el servidor real valida el doble de identidad (401 caducado, otro emisor y `system` ajeno; 400 cuerpo vacío)
  - **Ficheros**: `test/procesos/clientes_test.go`, `test/procesos/fixtures_test.go`
  - **Hecho cuando**: cada fixture dice en su comentario por qué no hay puerta HTTP; `TestArnes_SinViaAPI` existe
  - **Gate**: vet `-tags integracion` rc=0
  - **Commit**: `procesos(arnes): cliente con Context Token y fixtures sin puerta`

- [x] **T9.10 · procesos(arnes): el Edge de prueba** · 🌐 · dep. T9.8 · cumple R9.3.d — cerrada en `5519343` (sesión F9-01, 2026-10-01): Edge de prueba con núcleo separado del transporte; importa solo `wapp-cloudlink`, `wapp-shared/envelope`, grpc/protobuf y stdlib (`go list -deps … | grep internal/` vacío). Ejercitado contra el servidor real (`TestArnes_EdgeEnrolaYConecta`, `TestArnes_EdgeFrames`) más de lo que pedía la tarea: ver §7 del traspaso
  - **Ficheros**: `test/procesos/edge_falso_test.go` (`diseno.md` §3.3; lectura previa de `cmd/server/integration_test.go:185-360`)
  - **Hecho cuando**: vet rc=0; importa solo `wapp-cloudlink` (gen, lease, mtls) y `wapp-shared/envelope` además de stdlib
  - **Gate**: vet `-tags integracion` rc=0; `GOWORK=off go list -tags integracion -deps ./test/procesos | grep 'wapp-cloud-platform/internal/'` vacío (✎ D-F1-8, 2026-10-02: hoy el comando es el de R9.4.d, imports directos)
  - **Commit**: `procesos(arnes): el Edge de prueba, con mTLS, lease y sellado`

- [x] **T9.11 · procesos(arranque): P0, humo del arranque — y la prueba de D-F9-2** · 🌐→💻 · dep. T9.9, T9.10 · cumple R9.3.b, R9.4.a–b — cerrada en `10179d6` (web, 2026-10-01) y **cerrada en local por F9-02 (💻, 2026-10-01; `dev` @ `af7b8e9`) con una salvedad**: `make test-procesos` `RC=0` en las pasadas 2 y 3 y en `CUENTA=3`, pero la **pasada 1 dio `RC=1` contra `nuevo`** por una intermitencia de `TestP0_Arranque/sin_errores` (README de F9, contradicción 19: carrera con la parada, **diferida a F6** por decisión de Jhoan, D-F6-7); `HEAD /wapp-procesos` path-style con `Host: 127.0.0.1:<p>` en los dos binarios (**D-F9-2 reconfirmada en local**); `/metrics` sin tráfico = 11 familias, idénticas en viejo y nuevo, y las 7 declaradas que faltan son `CounterVec` (contradicción 12 confirmada)
  - **Ficheros**: `test/procesos/p0_arranque_test.go` (`diseno.md` §4 P0)
  - **Hecho cuando**: local: P0 `RC=0` contra **viejo y nuevo**; el doble de S3 registró `HEAD /procesos` con `Host: 127.0.0.1:<p>`. 🔴 Si el SDK pidió virtual-hosted, **parar**: traspaso a Jhoan con la petición registrada (D-F9-2)
  - **Gate**: local `make test-procesos` RC=0 ×2
  - **Commit**: `procesos(arranque): el binario completo arranca en el arnés`

- [x] **T9.12 · Cierre del bloque A: pre-chequeo web y traspaso** · 🌐→💻 · dep. T9.11 · cumple R9.7.b–c — pre-chequeo web en `6ee1c5e` (2026-10-01) y **cerrada en local por F9-02 (💻, 2026-10-01; cierre documental en `ac8ac5f`)**: `make test-procesos` viejo y nuevo `RC=0` ×2 (pasadas 2 y 3) y `CUENTA=3`; `make ci-local` `GATE_RC=0` (84 líneas `ok` —79 paquetes con tests + 5 que `cobertura-ficheros` vuelve a correr; no son 84 paquetes—, 0 issues, `go1.26.5`, lint `v2.12.2`); integración vieja con `-v` `RC=0 · 4.631 PASS · 0 SKIP · 0 FAIL`; la rama de la web ya estaba en `dev` sin squash (PR #18, `af7b8e9`); `TRASPASO-F9-arnes.md` con `CERRADO 2026-10-01`
  - **Ficheros**: `documentations/reorganizacion-modular/traspasos/TRASPASO-F9-arnes.md`
  - **Hecho cuando**: web: si el veredicto de F0 · T0.0 (`06-entorno-web.md` §5) fue «funciona», `make test-procesos` corrido en la web y su log citado como **pre-chequeo**; si fue «no funciona», se dice. Local: gates repetidos, sección `CERRADO <fecha>`, rama integrada en `dev` **sin squash**
  - **Gate**: local `make test-procesos` RC=0 ×2; `make ci-local` GATE_RC=0
  - **Commit**: `docs(reorganizacion-modular): F9, traspaso del arnés`

## Bloque B1 · procesos de plataforma y acceso · 💻 · T9.13–T9.16 🕐 · sesión F9-03

Entrada: bloque A `CERRADO` y **F1-06** (ajustes previos a F2) hecha. Para cuando: P1, P2, P3, P9 con `RC=0` contra
el **viejo** (y corridos contra el nuevo: un rojo solo contra el nuevo es hallazgo, R9.4.c), **y el mutante
`maxTxAttempts = 1` cae** (T9.15).
Cada tarea: **Ficheros** el `p<n>_…_test.go` de `diseno.md` §4 · **Hecho cuando** la definición de
hecho de un proceso (`reglas.md` §4) · **Gate** `make test-procesos` RC=0 ×2, 0 SKIP · **Commit** `procesos(<proceso>): …`.
Las tablas de casos de cada proceso llevan **casos adversarios** (`reglas.md` §2). Si los cuatro procesos no caben en
45–90 min, la sesión para en un punto limpio tras un proceso cerrado y se relanza.

- [x] **T9.13 · procesos(enrolamiento): P1, enrolamiento y lease** · 💻 · dep. T9.12 · cumple R9.4.b–d — cerrada en `679ea52` (sesión F9-03, 2026-10-03): `TestP1_EnrollmentAndLease` (17 subtests; `p1_enrolamiento_test.go` y sus piezas `_adversarial` y `_kill_switch`); viejo y nuevo `RC=0`, 0 SKIP. `restore` tras el corte comercial solo devuelve la operación **al reconectar** (hallazgo 38 del README)
- [x] **T9.14 · procesos(canje): P2, canje y permisos, con I-CP-5 y el canje único** · 💻 · dep. T9.12 · cumple R9.6.a–b — cerrada en `052089e` (sesión F9-03, 2026-10-03): `TestP2_InvitacionUnSoloCanje` (8 canjes simultáneos → 1 × 204 y 7 × 409, una membresía), `TestP2_RutasDePlataformaDenegadasAlCliente` (10 rutas × 2) y `TestP2_ExchangeAndPermissions`; viejo y nuevo `RC=0`, 0 SKIP
- [x] **T9.15 · procesos(entrante): P3, del entrante a la respuesta, con el literal del aviso y el reintento de `WithTx`** · 💻 · dep. T9.13 · cumple R9.6.d, R9.6.e — cerrada en `250916a` (sesión F9-03, 2026-10-03): `TestP3_IncomingToReply`, `TestP3_LatePushNameIsSealed`, `TestP3_FirstPushNameWins`, `TestP3_HistoryBurstWithoutDeadlock` y `TestP3_TxRetryOnSerializationFailure`, más `sealedIncoming` en el Edge de prueba (`TestArnes_EdgeSealedIncoming`); viejo y nuevo `RC=0`, 0 SKIP. 🔴 **El mutante `maxTxAttempts = 1` cae**: `TestP3_TxRetryOnSerializationFailure` 3 de 3 contra el viejo y 3 de 3 contra el nuevo (control sin mutar: 3 de 3 en verde en los dos); técnica y mensaje en el hallazgo 37 del README y en el bloque de `ESTADO.md`
  - Además (D-F1-11, decisión de Jhoan, 2026-10-02): los pasos 6–8 de P3 (`diseno.md` §4), que afirman R-27, R-28 y R-29 de F1 —las tres reglas de `contacts` que `internal/nucleo/contact` difiere a este proceso—; y la variante de `entrante` con `push_name` y `from_lid` en `edge_falso_test.go` (`diseno.md` §3.3).
  - 🔴 **El reintento de `postgres.WithTx`, con ejecución real** (hallazgo 38 de F1; R9.6.e): el paso 9 de P3 (`diseno.md` §4) provoca contra Postgres un conflicto de serialización o un deadlock y afirma que la operación termina bien tras reintentar. Ya no basta con **medir** el mutante: **Hecho cuando** `TestP3_TxRetryOnSerializationFailure` pasa contra los dos binarios **y el mutante `maxTxAttempts = 1` cae** (copia desechable de `internal/platform/storage/postgres/tx.go`; el nombre del test que cae y su mensaje van al bloque de `ESTADO.md`). Si el mutante sobrevive, T9.15 **no se cierra**: se queda `[~]` y vuelve a Jhoan. Tiene que existir **antes** de que F10 borre `deadlock_integration_test.go`.
- [x] **T9.16 · procesos(diagnostico): P9, diagnóstico remoto y config empujada** · 💻 · dep. T9.13 — cerrada en `8febd52` (sesión F9-03, 2026-10-03): `TestP9_DiagnosticsAndConfigPush` (14 subtests; `p9_diagnostico_test.go` y sus piezas `_remote` y `_config_push`); viejo y nuevo `RC=0`, 0 SKIP. Cierre documental de la sesión en el commit `docs(reorganizacion-modular): F9-03, bloque B1`
  - Cierre de la sesión F9-03: las tres cosas (sin traspaso).

## Bloque B2 · procesos de negocio · 💻 · T9.17–T9.21, T9.35 🕐 (+ T9.22) · sesión F9-04

Entrada: F9-03 cerrada. Misma forma que B1. Para cuando: P4–P8 (y P10, D-F9-4 = sí) con `RC=0` contra el viejo, y
**toda la suite B1 + B2** con `RC=0` contra viejo y nuevo; T9.22 hecha. De esta sesión depende **F2-01**. Si no cabe
en 45–90 min, para en un punto limpio tras un proceso cerrado y se relanza.

- [x] **T9.17 · procesos(borrador): el guion de inferencia y P4, de mensaje a borrador** · 💻 · dep. T9.15 — cerrada en `49d6d9e` y `39c38be` (sesión F9-04, 2026-10-03): `guion_test.go` (reconoce la etapa por un marcador del prompt; `TestArnes_GuionRecognizesStages`, `…RepliesPassTheirValidator`, `…Modes`), `TestP4_MessageToDraft` y `TestP4_WindowRules` (las tres reglas que salieron al leer enteros los tests viejos), más los helpers `draftScenario`/`createDraft` que usan P5–P8; viejo y nuevo `RC=0`, 0 SKIP (suite entera: 537 PASS por binario); hallazgos 46–51 del README
  - Además: `test/procesos/guion_test.go`; el comentario del guion dice **cómo** reconoce cada etapa (marcador del prompt o `max_output_tokens`, `diseno.md` §3.4) y de qué test viejo sale cada JSON
- [x] **T9.18 · procesos(bandeja): P5, la bandeja, con INV-1** · 💻 · dep. T9.17 · cumple R9.6.c — cerrada en `56b99f6` (sesión F9-04, 2026-10-03): `TestP5_OwnerInbox` y `TestP5_AprobarDosVecesUnSoloEfecto` (INV-1 como aserción; su mutante cae, medido contra el viejo); viejo y nuevo `RC=0`, 0 SKIP (suite entera: 537 PASS por binario); hallazgos 45, 46 y 52
- [x] **T9.19 · procesos(crm): el CRM falso y P6** · 💻 · dep. T9.18 — cerrada en `2bb7989` (sesión F9-04, 2026-10-03): `crmfalso_test.go` (`TestArnes_CRM`) y `TestP6_CRMBridge` (20 subtests; los campos del contrato como aserción sobre lo recibido); viejo y nuevo `RC=0`, 0 SKIP (suite entera: 537 PASS por binario); hallazgos 44, 53 y 54
- [x] **T9.20 · procesos(catalogo): P7, catálogo** · 💻 · dep. T9.17 — cerrada en `0a2762d` (sesión F9-04, 2026-10-03): `TestP7_Catalog` (16 subtests; 56 casos adversarios entre JSON y planilla); viejo y nuevo `RC=0`, 0 SKIP (suite entera: 537 PASS por binario); hallazgos 55 y 56
- [x] **T9.21 · procesos(reanalisis): P8, re-análisis** · 💻 · dep. T9.17 — cerrada en `9da68f7` (sesión F9-04, 2026-10-03): `TestP8_Reanalysis` (8 subtests); viejo y nuevo `RC=0`, 0 SKIP (suite entera: 537 PASS por binario); 🔴 sobre una aprobada el viejo **no** rechaza (hallazgo 44)

## Bloque C · pasada por conmutación · 💻 · T9.22–T9.29 🕐 · en el cierre local de cada módulo

Una tarea por módulo, **dentro de la sesión 💻 que cierra el `conmutar(<m>)` de su fase** (F2…F8; la fase del módulo
la cita como dependencia de su cierre). T9.22 (`nucleo`, F1) cae en **F9-04**. Forma común:

- **Ficheros**: `test/procesos/<paquete>_contrato_test.go`, uno por paquete de la fila de `diseno.md` §5
- **Hecho cuando**: las suites del módulo `RC=0` contra Postgres **y** `BINARIO=nuevo make test-procesos` `RC=0`; los
  procesos marcados para el módulo en `arquitectura.md` §5.1 son el gate del `conmutar`. Además:
  - 🔴 **el test de cableado del módulo está completo** (hallazgo 39 de F1; `05` §4.2): afirma que el arranque
    construye lo **nuevo** y que nadie importa lo viejo fuera de `bridge_<x>.go` (grep por ruta de import). Los
    procesos **no lo detectan**: con el resolver viejo cableado por error el comportamiento es el mismo y la suite pasa;
  - la marca de `Estado` de cada suite vigila todas las columnas que la operación puede tocar (R9.5.c);
  - las tablas de la suite y del corpus de equivalencia llevan casos adversarios (`reglas.md` §2).
- **Gate**: `make test-procesos` RC=0 ×2, 0 SKIP
- **Commit**: `procesos(<modulo>): suites de contrato contra Postgres`

- [x] **T9.22 · `nucleo` (F1)** · sesión F9-04 · dep. T9.12 · retroactiva: si F1 · T1.13/T1.18 ya corrió `contact_contrato_test.go`, solo la suite entera contra el nuevo — hecha en la sesión F9-04 (2026-10-03), sin commit de código, sobre `2bb7989`: `TestContactContrato_Postgres` 20 PASS, 0 SKIP en cada pasada; suite entera (P0–P10) contra el nuevo `RC=0 · PASS=537 · FAIL=0 · SKIP=0`; test de cableado completo: el mutante del hallazgo 39 de F1 da `go test ./internal/arranque/` rc=1 por `TestBootWiring_OnlyBridgeAndFlowsImportOldContact` (hallazgo 61 f)
- [x] **T9.23 · `acceso` (F2)** · dep. `conmutar(acceso)` · `iam/infra/postgres`, `entitlements`, `platformadmin` — hecha en la sesión F2-05 (2026-10-04) = T2.33 de F2: `8677404` (la 0038 en P2) y `40d1582`, `8c53ecf`, `d243f15`, `7f2b745`, `73b4541` (mutantes vivos nuevos); suites `entitlements`, `iam` (7) y `platformadmin` contra Postgres rc=0, 149 PASS con `contact`, 0 SKIP en cada binario; suite entera `RC=0 · PASS=667 · FAIL=0 · SKIP=0` contra el viejo y contra el nuevo; test de cableado completo (`TestBootWiring_Access*`, rc=0); hallazgos 43–51 del README de F2 (🟡 50: intermitencia de `TestP5_OwnerInbox/sugerencia_con_plazo` contra el viejo)
- [x] **T9.24 · `edge` (F3)** · dep. `conmutar(edge)` · `enroll`, `fleet`, `lease`, `diagnostics`, `ingest`, `receipts` — hecha en la sesión F3-05 (2026-10-06) = T3.30 de F3: `8121564`, `a28f28b`, `0caeef4`, `aaed74c`, `0133b4b`, `93b145b` (las 7 suites contra Postgres: 115 PASS, 0 SKIP) y `c004d9a`, `f5b0fa6`, `5e481c8`, `e0ba4f6`, `876b096` (lo que P1 no cubría: canal de control, cara de `edge` por el cable, reinicio); suite entera `RC=0 · PASS=812 · FAIL=0 · SKIP=0` contra el viejo y contra el nuevo; test de cableado completo desde F3-04 (`gateway_wiring_test.go`); hallazgos 74–83 del README de F3
- [x] **T9.25 · `inferencia` (F4)** · dep. `conmutar(inferencia)` · `tenantllm`, `degradation` — hecha en la sesión F45-03 (2026-10-07) = T4.31 de F4: `ddb8baa` (las 2 suites contra Postgres: 34 casos PASS, 0 SKIP, en cada binario) y `59a3e6b` (lo que ningún proceso cubría: `GET`/`PUT`/`DELETE /api/v1/tenant-llm` por el cable, paso `via_llm` de P4); suite entera `RC=0 · PASS=869 · FAIL=0 · SKIP=0` contra el nuevo y contra el viejo (con dos pasadas rojas por la intermitencia del compositor del *flush*, hallazgo 30 del README de F4); test de cableado completo desde F45-02 (`inference_wiring_test.go`); hallazgos 27–32 del README de F4
- [x] **T9.26 · `catalogo` (F5)** · dep. `conmutar(catalogo)` · sin SQL propio: solo la suite entera contra el nuevo — hecha en la sesión F45-03 (2026-10-07) = T5.20 de F5, sin commit de código, sobre `59a3e6b`: suite entera contra el nuevo `RC=0 · PASS=869 · FAIL=0 · SKIP=0` (`TestP7_Catalog` PASS); el proceso «Catálogo» sigue ejercitando el código **viejo** en los dos binarios (nadie cablea `internal/modulos/catalogo` hasta F7 y F8); hallazgos 25–27 del README de F5
- [x] **T9.27 · `solicitudes` (F6)** · dep. `conmutar(solicitudes)` · `intakes`, `integrations`, `tenantvars` — hecha en la sesión F6-06 (2026-10-08) = T6.27 de F6, sin commit de código, sobre `68e68a4`: suites `intakes` (50), `integrations` (61), `tenantvars` (12) y telemetría de eventos (13) contra Postgres rc=0, 140 PASS, 0 SKIP en cada binario, sin divergencias con los dobles; suite entera `RC=0 · PASS=1018 · FAIL=0 · SKIP=0` contra el viejo y contra el nuevo; hallazgos 61–65 del README de F6
- [x] **T9.28 · `captacion` (F7)** · dep. `conmutar(captacion)` · `intake`, `casebank`, `intentcfg` — hecha en la sesión F7-05 (2026-10-09) = T7.27 de F7: `30b0cf4` (las 5 suites contra Postgres: `intake` ×3 —17, 29 y 10 casos; 18, 30 y 10 al final—, `casebank` 12 e `intentcfg` 15; rc=0, 88 PASS, 0 SKIP en cada binario, sin divergencias con los dobles), `51d1bbb` (lo que ningún proceso cubría: el adelanto por clasificación, `TestP4_AheadClassification`), `fb33953` (mutantes vivos de `intake`: `ContratoMachine` pasa a 30 casos y nace `TestIntakeClaim_SkipsLockedRows_Postgres`) y `ca222ed` (ampliación pedida por Jhoan en el mismo PR: `ContratoQueue` pasa a 18 casos con `PutSourceText_CrossedMarks_LatestUpdateWins_CreationBreaksTies`, `QueueMontaje` gana `Seed` y se corrige el desempate del doble en memoria; de 78 mutantes SQL de `intake` queda 1 vivo, declarado y equivalente en la práctica); suite entera, final, sobre `13d2e59` (último commit de código, `ca222ed`): viejo y nuevo `RC=0 · PASS=1116 · FAIL=0 · SKIP=0` (104 tests de nivel superior, sin carga en la máquina; 0 apariciones de «el job no trae literal») (antes, sobre `fb33953`, pasada intermedia verde con 1115; y una pasada roja contra el viejo, bajo carga, por la intermitencia del rate-limit de P6, hallazgo 54 del README de F7); test de cableado completo desde F7-04 (`captacion_cableado_test.go`); hallazgos 50–60 del README de F7
- [ ] **T9.29 · `conversacion` (F8)** · dep. `conmutar(conversacion)` · `flujos/store`, `flujos/trigger`, `flujos/events`, `flujos/runtime`

## Bloque D · cierre · 💻 · T9.30–T9.33 · sesión F9-05

Entrada: F8 conmutada, T9.29 `CERRADO`, puentes = 0. Para cuando: condición del relevo cumplida.

- [ ] **T9.30 · Corrida final sin intermitencias** · 💻 · dep. T9.29 · cumple R9.8.a, R9.5.b
  - **Hecho cuando**: `CUENTA=3 make test-procesos` → viejo y nuevo `RC=0`, 0 SKIP, 0 FAIL; las 22 filas de `diseno.md` §5 con su `--- PASS` en el log; duración total anotada (referencia de F9-02, solo con P0 y el arnés: ≈ 25–31 s por pasada de los dos binarios, 33 s con `CUENTA=3`, Mac de 8 núcleos con la caché de Go caliente; ≈ 23 s en la VM web de 4 vCPU; crecerá con B1–C, hay que remedirla)
  - **Heredado (H-1 de F9-02, D-F6-7)**: para entonces F6 ya reconstruyó `integrations` y evaluó `sin_errores`; esta corrida **no** lleva la salvedad de la intermitencia: si P0 vuelve a dar rojo con las dos líneas `ERROR` del worker, F6 no lo cerró
  - ⚠️ **Revisión independiente (2026-10-01)** — hechos; el criterio de arriba **no se cambia** (es de Jhoan: pregunta abierta D-F9-10 del [README](README.md)). (i) T9.30 corre **antes** de F10 y contra los dos binarios: `viejo` conserva el worker viejo hasta el relevo (D-F6-7 no lo arregla), así que un rojo contra `viejo` con esas líneas no dice que F6 no lo cerrara. (ii) Contra `nuevo` puede salir la misma carrera con otro texto: el colector (`platform`) no se reconstruye, y el agregador y el pipeline dependen de lo que prometan F8 y F7 (contradicción 19, hechos 1 y 2). (iii) Un **verde** tampoco dice que esté cerrada: `CUENTA=3` son 2 arranques en frío expuestos y da verde ≈ 98,8 % de las veces sin arreglar nada (hecho 3)
  - ✎ **D-F9-10 decidida (2026-10-08, Jhoan): (a) + (c).** `sin_errores` tolera los `ERROR` de cancelación posteriores a la señal de parada (`p0CountedErrors`), en los dos binarios: esta corrida exige `RC=0` sin salvedad, y un rojo de `sin_errores` ya **no** es esta carrera. `CUENTA=3` sigue valiendo para cazar otras intermitencias, no para esta
  - **Gate**: los dos logs, leídos sin pipe
  - **Commit**: — (resultado en T9.33)
- [ ] **T9.31 · Recuento contra el código** · 💻 · dep. T9.30 · cumple R9.4.d, R9.8.b
  - **Hecho cuando**: `TestProcessImports` en verde (`test/procesos/domain_imports_test.go`: el candado por fichero de R9.4.d que creó F1-06, `7937772`) **y** sigue corriendo en `make ci-local` (hallazgo 37 de F1); la comprobación a mano del hallazgo 36 ya no hace falta; `WAPP_PROCESOS_BINARIO` solo en `main_test.go`; `grep -rn 't.Skip' test/procesos` vacío; el candado verde
- [ ] **T9.32 · docs: F9 cerrada** · 💻 · dep. T9.31
  - **Ficheros**: `plan/F9-procesos/README.md` (estado, números medidos), `ESTADO.md`, `documentations/operacion.md` §3 (fila `make test-procesos`), la skill `procesos-testcontainers` si algo de lo aprendido la contradice (T-4, T-7, T-8 de `reglas.md`)
  - **Commit**: `docs(reorganizacion-modular): F9 cerrada, la condición del relevo`
- [ ] **T9.33 · Cierre e integración** · 💻 · dep. T9.32
  - **Hecho cuando**: las tres cosas del cierre (tareas `[x]` con SHA, bloque en `ESTADO.md` con los dos `RC` y el estado `F9 CERRADA <fecha>`, hallazgos en el README); `git push origin dev` leyendo su `rc`. Sin traspaso (solo si la sesión se corta)

## Tareas condicionales

- [ ] **T9.34 · 🕐 (solo si D-F9-1 = no) · todas las suites de contrato contra Postgres de una vez** · 💻 · dep. T9.21 y F8 cerrada
  - Sustituye a T9.22–T9.29 (que se tachan: `~~T9.2x~~ — anulada: D-F9-1 rechazada`). Mismo contenido, en un bloque tras B2.
- [x] **T9.35 · procesos(plataforma): P10 (solo si D-F9-4 = sí; lo es)** · 💻 · dep. T9.12 · sesión F9-04 — cerrada en `e3fc0de` (sesión F9-04, 2026-10-03): `TestP10_MigrationsReplay` y `TestP10_Platform` (14 subtests); viejo y nuevo `RC=0`, 0 SKIP (suite entera: 537 PASS por binario). De los 25 `Test*` viejos: 17 enteros, 5 parciales, 3 no llevados; 🔴 **la rotación real de KEK no cabe en el arnés** (hallazgo 57: hay que resolverlo antes de que F10 borre `rekey_integration_test.go`)
  - **Ficheros**: `test/procesos/p10_plataforma_test.go` (`diseno.md` §4 P10)
  - **Hecho cuando**: cubre las reglas de los 9 ficheros de BD de `internal/platform/` (lista en `F10-relevo/diseno.md` §3), leídos (E-8); `RC=0` ×2 en local
  - **Commit**: `procesos(plataforma): migraciones, grants, rekey y colector contra Postgres`
