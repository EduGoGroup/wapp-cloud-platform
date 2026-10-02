# F9 · Tareas

> Skills: `procesos-testcontainers` (todas), `validar-antes-de-cerrar` (gates),
> `traspaso-web-local` (cierre de cada bloque), `contrato-tdd` (solo para leer cómo nace una suite).
> **🕐** = la tarea cambia si Jhoan **no** acepta adelantar F9 (D-F9-1): ver T9.34.
> Gate de la web en todas: `GOWORK=off make ci-local > /tmp/g.log 2>&1; echo "GATE_RC=$?" >> /tmp/g.log; tail -1 /tmp/g.log` → `GATE_RC=0`.
> Gate local de procesos: `make test-procesos` (nace en T9.4), `RC=0` por binario leído del log.

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

## Bloque B1 · procesos de plataforma y acceso · 🌐→💻 · T9.13–T9.16 🕐

Entrada: bloque A `CERRADO`. Para cuando: P1, P2, P3, P9 con `RC=0` contra viejo y nuevo, en local.
Cada tarea: **Ficheros** el `p<n>_…_test.go` de `diseno.md` §4 · **Hecho cuando** la definición de
hecho de un proceso (`reglas.md` §4) · **Gate** web: vet `-tags integracion` rc=0 (y pre-chequeo si
hay Docker); local: `make test-procesos` RC=0 ×2 · **Commit** `procesos(<proceso>): …`.

- [ ] **T9.13 · procesos(enrolamiento): P1, enrolamiento y lease** · 🌐→💻 · dep. T9.12 · cumple R9.4.b–d
- [ ] **T9.14 · procesos(canje): P2, canje y permisos, con I-CP-5 y el canje único** · 🌐→💻 · dep. T9.12 · cumple R9.6.a–b
- [ ] **T9.15 · procesos(entrante): P3, del entrante a la respuesta, con el literal del aviso** · 🌐→💻 · dep. T9.13 · cumple R9.6.d
  - Además (D-F1-11, decisión de Jhoan, 2026-10-02): los pasos 6–8 de P3 (`diseno.md` §4), que afirman R-27, R-28 y R-29 de F1 —las tres reglas de `contacts` que `internal/nucleo/contact` difiere a este proceso—; la variante de `entrante` con `push_name` y `from_lid` en `edge_falso_test.go` (`diseno.md` §3.3); y la **medición** del mutante `maxTxAttempts = 1`, con su resultado en el traspaso (se cierre o no la carencia de MP-12)
- [ ] **T9.16 · procesos(diagnostico): P9, diagnóstico remoto y config empujada** · 🌐→💻 · dep. T9.13
  - Cierre del bloque: traspaso `TRASPASO-F9-procesos-b1.md` con `CERRADO`.

## Bloque B2 · procesos de negocio · 🌐→💻 · T9.17–T9.21, T9.35 🕐

Entrada: B1 `CERRADO`. Misma forma que B1. Para cuando: P4–P8 (y P10 si D-F9-4) con `RC=0` contra
viejo y nuevo, en local; traspaso `TRASPASO-F9-procesos-b2.md` con `CERRADO`.

- [ ] **T9.17 · procesos(borrador): el guion de inferencia y P4, de mensaje a borrador** · 🌐→💻 · dep. T9.15
  - Además: `test/procesos/guion_test.go`; el comentario del guion dice **cómo** reconoce cada etapa (marcador del prompt o `max_output_tokens`, `diseno.md` §3.4) y de qué test viejo sale cada JSON
- [ ] **T9.18 · procesos(bandeja): P5, la bandeja, con INV-1** · 🌐→💻 · dep. T9.17 · cumple R9.6.c
- [ ] **T9.19 · procesos(crm): el CRM falso y P6** · 🌐→💻 · dep. T9.18
- [ ] **T9.20 · procesos(catalogo): P7, catálogo** · 🌐→💻 · dep. T9.17
- [ ] **T9.21 · procesos(reanalisis): P8, re-análisis** · 🌐→💻 · dep. T9.17

## Bloque C · pasada por conmutación · 🌐→💻 · T9.22–T9.29 🕐

Una tarea por módulo, **dentro de la sesión del `conmutar(<m>)` de su fase** (la fase del módulo la
cita como dependencia de su cierre). Forma común:

- **Ficheros**: `test/procesos/<paquete>_contrato_test.go`, uno por paquete de la fila de `diseno.md` §5
- **Hecho cuando**: web: vet rc=0 (y pre-chequeo si hay Docker); local: las suites del módulo
  `RC=0` contra Postgres **y** `BINARIO=nuevo make test-procesos` `RC=0`; los procesos marcados para
  el módulo en `arquitectura.md` §5.1 son el gate del `conmutar`
- **Gate**: local `make test-procesos` RC=0 ×2
- **Commit**: `procesos(<modulo>): suites de contrato contra Postgres`

- [ ] **T9.22 · `nucleo` (F1)** · dep. T9.12 · retroactiva: si F1 · T1.13/T1.18 ya corrió `contact_contrato_test.go`, solo la suite entera contra el nuevo
- [ ] **T9.23 · `acceso` (F2)** · dep. `conmutar(acceso)` · `iam/infra/postgres`, `entitlements`, `platformadmin`
- [ ] **T9.24 · `edge` (F3)** · dep. `conmutar(edge)` · `enroll`, `fleet`, `lease`, `diagnostics`, `ingest`, `receipts`
- [ ] **T9.25 · `inferencia` (F4)** · dep. `conmutar(inferencia)` · `tenantllm`, `degradation`
- [ ] **T9.26 · `catalogo` (F5)** · dep. `conmutar(catalogo)` · sin SQL propio: solo la suite entera contra el nuevo
- [ ] **T9.27 · `solicitudes` (F6)** · dep. `conmutar(solicitudes)` · `intakes`, `integrations`, `tenantvars`
- [ ] **T9.28 · `captacion` (F7)** · dep. `conmutar(captacion)` · `intake`, `casebank`, `intentcfg`
- [ ] **T9.29 · `conversacion` (F8)** · dep. `conmutar(conversacion)` · `flujos/store`, `flujos/trigger`, `flujos/events`, `flujos/runtime`

## Bloque D · cierre · 💻 · T9.30–T9.33

Entrada: F8 conmutada, T9.29 `CERRADO`, puentes = 0. Para cuando: condición del relevo cumplida.

- [ ] **T9.30 · Corrida final sin intermitencias** · 💻 · dep. T9.29 · cumple R9.8.a, R9.5.b
  - **Hecho cuando**: `CUENTA=3 make test-procesos` → viejo y nuevo `RC=0`, 0 SKIP, 0 FAIL; las 22 filas de `diseno.md` §5 con su `--- PASS` en el log; duración total anotada (referencia de F9-02, solo con P0 y el arnés: ≈ 25–31 s por pasada de los dos binarios, 33 s con `CUENTA=3`, Mac de 8 núcleos con la caché de Go caliente; ≈ 23 s en la VM web de 4 vCPU; crecerá con B1–C, hay que remedirla)
  - **Heredado (H-1 de F9-02, D-F6-7)**: para entonces F6 ya reconstruyó `integrations` y evaluó `sin_errores`; esta corrida **no** lleva la salvedad de la intermitencia: si P0 vuelve a dar rojo con las dos líneas `ERROR` del worker, F6 no lo cerró
  - ⚠️ **Revisión independiente (2026-10-01)** — hechos; el criterio de arriba **no se cambia** (es de Jhoan: pregunta abierta D-F9-10 del [README](README.md)). (i) T9.30 corre **antes** de F10 y contra los dos binarios: `viejo` conserva el worker viejo hasta el relevo (D-F6-7 no lo arregla), así que un rojo contra `viejo` con esas líneas no dice que F6 no lo cerrara. (ii) Contra `nuevo` puede salir la misma carrera con otro texto: el colector (`platform`) no se reconstruye, y el agregador y el pipeline dependen de lo que prometan F8 y F7 (contradicción 19, hechos 1 y 2). (iii) Un **verde** tampoco dice que esté cerrada: `CUENTA=3` son 2 arranques en frío expuestos y da verde ≈ 98,8 % de las veces sin arreglar nada (hecho 3)
  - **Gate**: los dos logs, leídos sin pipe
  - **Commit**: — (resultado en T9.33)
- [ ] **T9.31 · Recuento contra el código** · 💻 · dep. T9.30 · cumple R9.4.d, R9.8.b
  - **Hecho cuando**: el comando de R9.4.d (imports directos, D-F1-8) vacío; `WAPP_PROCESOS_BINARIO` solo en `main_test.go`; `grep -rn 't.Skip' test/procesos` vacío; el candado verde
- [ ] **T9.32 · docs: F9 cerrada** · 💻 · dep. T9.31
  - **Ficheros**: `plan/F9-procesos/README.md` (estado, números medidos), `ESTADO.md`, `documentations/operacion.md` §3 (fila `make test-procesos`), la skill `procesos-testcontainers` si algo de lo aprendido la contradice (T-4, T-7, T-8 de `reglas.md`)
  - **Commit**: `docs(reorganizacion-modular): F9 cerrada, la condición del relevo`
- [ ] **T9.33 · Traspaso final e integración** · 💻 · dep. T9.32
  - **Ficheros**: `traspasos/TRASPASO-F9-cierre.md` con `CERRADO <fecha>`; `git push origin dev` leyendo su `rc`

## Tareas condicionales

- [ ] **T9.34 · 🕐 (solo si D-F9-1 = no) · todas las suites de contrato contra Postgres de una vez** · 🌐→💻 · dep. T9.21 y F8 cerrada
  - Sustituye a T9.22–T9.29 (que se tachan: `~~T9.2x~~ — anulada: D-F9-1 rechazada`). Mismo contenido, en un bloque tras B2.
- [ ] **T9.35 · procesos(plataforma): P10 (solo si D-F9-4 = sí)** · 🌐→💻 · dep. T9.12
  - **Ficheros**: `test/procesos/p10_plataforma_test.go` (`diseno.md` §4 P10)
  - **Hecho cuando**: cubre las reglas de los 9 ficheros de BD de `internal/platform/` (lista en `F10-relevo/diseno.md` §3), leídos (E-8); `RC=0` ×2 en local
  - **Commit**: `procesos(plataforma): migraciones, grants, rekey y colector contra Postgres`
