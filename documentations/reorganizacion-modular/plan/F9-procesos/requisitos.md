# F9 · Requisitos — historias y criterios EARS

> Forma: [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §2. Cada criterio nombra el
> comando o el test que lo verifica. «Los dos binarios» = `cmd/server` (viejo) y
> `cmd/server-modular` (nuevo). `make test-procesos` nace en T9.4.

---

## H9.1 · El arnés: un Postgres por corrida, una base por proceso

> Como **la sesión local**, quiero un arnés que levante **un** Postgres 17 por corrida y dé a cada
> proceso su base clonada y su servidor, para correr la suite entera sin tocar un Postgres vivo y
> sin que un proceso pise a otro.

- **R9.1.a** · **CUANDO** empieza una corrida de `test/procesos`, **EL** `TestMain` **DEBERÁ** levantar
  exactamente **un** contenedor `postgres:17-alpine` con `postgres.Run` y terminarlo con
  `ctr.Terminate` al acabar. — Verifica: `docker ps --filter ancestor=postgres:17-alpine -q | wc -l`
  durante la corrida = 1; después de la corrida = 0.
- **R9.1.b** · **EL** `TestMain` **DEBERÁ** migrar la base `plantilla` **una sola vez** con el binario
  `cmd/migrate` compilado por el arnés, y dejarla **sin conexiones abiertas** antes del primer
  `CREATE DATABASE … TEMPLATE plantilla`. — Verifica: el log de la corrida muestra una única línea
  `migraciones aplicadas: version=0.48.0 … skipped=false` (formato de `cmd/migrate/main.go`).
- **R9.1.c** · **CUANDO** un proceso pide su base, **EL** arnés **DEBERÁ** crearla con
  `CREATE DATABASE proc_<proceso>_<binario> TEMPLATE plantilla` sobre la base de mantenimiento
  `postgres`. — Verifica: `TestArnes_BasePorProceso` (en `servidor_test.go`) comprueba que dos
  procesos ven bases distintas.
- **R9.1.d** · **SI** Docker no está disponible, **ENTONCES EL** `TestMain` **DEBERÁ** terminar con
  código ≠ 0 y el mensaje `procesos: no se pudo levantar Postgres (¿hay Docker?)`, sin saltar nada.
  — Verifica: `GOCACHE=$(go env GOCACHE) GOPATH=$(go env GOPATH) GOMODCACHE=$(go env GOMODCACHE)
  GOENV=$(go env GOENV) HOME=$(mktemp -d) DOCKER_HOST=unix:///nada WAPP_PROCESOS_BINARIO=viejo GOWORK=off go test -tags integracion -count=1 -v
  ./test/procesos/; echo rc=$?` → `rc≠0`, el mensaje de arriba y 0 `--- SKIP`. ⚠️ **`DOCKER_HOST=unix:///nada` a secas NO sirve**:
  testcontainers prueba ese host, falla y cae al socket del contexto activo (Docker Desktop) o a `/var/run/docker.sock`, y la
  corrida pasa (medido en macOS y en la VM web: README, contradicción 15). El `HOME` vacío anula el contexto y
  `~/.docker/run/docker.sock`; donde exista `/var/run/docker.sock` (Linux) hace falta además un espacio de montajes privado.
  ⚠️ **`HOME=$(mktemp -d)` va DESPUÉS de las variables de Go** (corregido el 2026-10-01): las asignaciones de una línea se evalúan
  de izquierda a derecha, y con `HOME` delante cada `$(go env …)` calcula su ruta bajo el `HOME` vacío (caché de compilación vacía y,
  sin `GOMODCACHE` exportada, caché de módulos vacía). Medido en bash 3.2, zsh, `sh` y `dash`: README, contradicción 15.
- **R9.1.e** · **EL** arnés **DEBERÁ** lanzar cada servidor con un entorno **construido desde cero**
  (nunca `os.Environ()`), con los cuatro listeners en `127.0.0.1:<puerto libre>`. — Verifica: el
  candado de H9.2 falla si aparece `os.Environ()` en `test/procesos/` (también con `os` importado con alias o con punto, desde
  `1c247f9`; lo que el candado sigue sin ver, en la contradicción 22 del README); `TestArnes_EntornoLimpio`
  arranca con `WAPP_DB_HOST=trampa` exportado en el shell y el servidor igualmente usa el contenedor.
- **R9.1.f** · **MIENTRAS** dos procesos corren en paralelo (`t.Parallel()`), **EL** arnés **DEBERÁ**
  darles puertos y bases disjuntos. — Verifica: `make test-procesos` con `-parallel 4` sin
  `address already in use` en los logs (`grep -c 'address already in use'` = 0).

## H9.2 · El candado: nunca un Postgres vivo

> Como **Jhoan**, quiero que el gate falle si alguien apunta un test de proceso a un Postgres vivo o
> esconde un rojo, para que el vicio de `WAPP_TEST_DB_DSN` y la deuda DT-52 no vuelvan.

- **R9.2.a** · **SI** algún fichero de `test/procesos/` contiene `WAPP_TEST_DB_DSN`,
  `WithReuseByName`, un literal con `:5432` o que empiece por `postgres://` (lo que ya caza el
  candado que crea **F0**, T0.8, su `diseno.md` §4.4) **o** —ampliación de F9, T9.3— `os.Environ()` o
  `t.Skip`, **ENTONCES EL** candado `sin_bd_viva_test.go` **DEBERÁ** fallar nombrando fichero y
  línea. — Verifica: los casos `muerde` de `internal/candados/testdata/sinbdviva/` (uno por patrón) y
  `GOWORK=off go test ./test/procesos/; echo rc=$?` → `rc=0` sobre el árbol limpio.
- **R9.2.b** · **EL** candado **DEBERÁ** correr en `make ci-local` **sin Docker** (fichero sin
  etiqueta de build; el resto del paquete lleva `//go:build integracion`). — Verifica:
  `GOWORK=off go test -v ./test/procesos/ 2>&1 | grep -c -- '--- PASS'` ≥ 1 y `--- SKIP` = 0, sin Docker.
- **R9.2.c** · **EL** candado **DEBERÁ** probar su propio detector con un caso `muerde` por patrón,
  para que un detector roto no dé verde. — Verifica: el test del detector en `internal/candados/`
  (mecánica de F0) con un caso por cada uno de los seis patrones.
- **R9.2.d** · *(D-F9-6, Jhoan, 2026-10-02)* **SI** un fichero de `test/procesos/` que no es
  `test/procesos/base_test.go` (ruta exacta) nombra una apertura de conexión de `database/sql`, `pgx`,
  `pgconn`, `pgxpool` o `pgx/stdlib` —con el nombre del paquete, con alias o con import de punto, llamada o
  como valor—, **ENTONCES EL** candado **DEBERÁ** fallar nombrando fichero, línea y apertura, diga lo que
  diga la cadena que recibe. La auto-exención de R9.2.a vale solo para la ruta exacta
  `test/procesos/sin_bd_viva_test.go`. — Verifica: `TestSinBDVivaMuerde`, `TestSinBDVivaOpenersBite` y
  `TestSinBDVivaExactPaths` en `internal/candados/`, y `GOWORK=off go test ./test/procesos/; echo rc=$?` → `rc=0`.

## H9.3 · Los dobles: el servidor de verdad arranca sin nada de fuera

> Como **la sesión web**, quiero dobles para todo lo que el servidor exige al arrancar (PKI, claves,
> S3, identity, LLM, CRM), para que el binario completo arranque en el arnés **sin tocar su código**.

- **R9.3.a** · **EL** arnés **DEBERÁ** generar en `t.TempDir()` una CA EC P-256, un certificado de
  servidor con SAN `localhost`/`127.0.0.1`, la clave Ed25519 del lease, la X25519 de la nube, la KEK
  e índice (proveedor `env`) y la clave ES256 del emisor (fichero `0600`), sin leer nada de `certs/`
  ni de `.env`. — Verifica: `grep -rn 'certs/\|\.env' test/procesos` vacío; P0 arranca.
- **R9.3.b** · **CUANDO** el servidor arranca con `WAPP_STORAGE_S3_ENDPOINT=http://127.0.0.1:<p>`,
  **EL** doble de S3 **DEBERÁ** recibir `HEAD /<bucket>` y el servidor **DEBERÁ** completar la fase
  `almacenes`. — Verifica: P0 (`TestP0_Arranque`) aserta la petición registrada por el doble y la
  línea `arranque: fase completada … nombre=almacenes`. (Confirma D-F9-2.)
- **R9.3.c** · **EL** doble de identity **DEBERÁ** servir un JWKS ES256 por HTTP en loopback y emitir
  Identity Tokens con `iss=identity-core` (`internal/bootstrap/arranque/auth.go:43`) y
  `system=wapp.bff` (`internal/iam/usecase/exchange.go:24`). — Verifica: P2 obtiene un Context Token
  por `POST /api/v1/auth/exchange`.
- **R9.3.d** · **EL** Edge de prueba **DEBERÁ** enrolar por `:8102` (TLS de servidor), conectar por
  `:8101` (mTLS con el certificado emitido), declarar `INFERENCE_READINESS_READY` en el latido,
  responder `Ack{ok}` a cada comando y responder cada `InferenceRequest` con la salida del guion
  sellada con `cloud_enc_pubkey`. — Verifica: P1 y P4.
- **R9.3.e** · **SI** un proceso configurase la vía LLM `api`, **ENTONCES EL** proceso **DEBERÁ**
  fallar antes de enviar nada (el arnés rechaza `PUT /api/v1/tenant-llm` con `via=api` en su cliente).
  — Verifica: `TestArnes_SinViaAPI` (cero gasto: la vía `api` no admite `BaseURL`,
  `internal/llmvia/llmvia.go:275`).

## H9.4 · Cada proceso, contra los dos binarios

> Como **Jhoan**, quiero que cada proceso de negocio pase primero contra el binario viejo y después
> contra el nuevo, para que el viejo valide el test y el test valide el código nuevo.

- **R9.4.a** · **EL** arnés **DEBERÁ** elegir el binario por `WAPP_PROCESOS_BINARIO=viejo|nuevo`
  (leída por el arnés, nunca por el test) y **DEBERÁ** fallar si falta o vale otra cosa. — Verifica:
  `WAPP_PROCESOS_BINARIO=otro GOWORK=off go test -tags integracion ./test/procesos/; echo rc=$?` → `rc≠0`
  (con `GOWORK=off`, para que el `rc≠0` sea el del binario y no el de D-F9-7: sin él, `TestMain` sale también con código 2).
- **R9.4.b** · **CUANDO** se escribe un proceso nuevo, **EL** proceso **DEBERÁ** pasar contra `viejo`
  antes de su commit `procesos(<proceso>)`. — Verifica: el traspaso del bloque cita el log con
  `RC=0` contra `viejo` y el conteo `--- PASS`.
- **R9.4.c** · **SI** un proceso falla contra `nuevo` y pasa contra `viejo`, **ENTONCES** la sesión
  **DEBERÁ** registrarlo como hallazgo de la reconstrucción (no se toca el test). — Verifica: sección
  §7 del traspaso.
- **R9.4.d** · **EL** proceso **DEBERÁ** entrar solo por las puertas reales (HTTP `:8100`/`:8103`,
  gRPC `:8101`/`:8102`) y leer Postgres por SQL; **NO DEBERÁ** importar paquetes de dominio. La excepción
  son las suites de contrato (H9.5, D-F1-8): los paquetes `…helpertest` y el constructor del adaptador Postgres
  del puerto que prueban, con los argumentos de ese constructor. —
  Verifica (imports **directos** del paquete, D-F1-8): `GOWORK=off go list -tags integracion -f '{{join .Imports "\n"}}{{"\n"}}{{join .TestImports "\n"}}{{"\n"}}{{join .XTestImports "\n"}}' ./test/procesos | sort -u | grep 'wapp-cloud-platform/internal/' | grep -v -e '/internal/\(modulos\|nucleo\)/.*helpertest$' -e '/internal/nucleo/contact$' -e '/internal/platform/crypto$' -e '/internal/candados$'`
  vacío. Se admiten `…helpertest`, `internal/nucleo/contact` (el adaptador, T1.13), `internal/platform/crypto` (sus
  argumentos) y `internal/candados` (el candado `sin_bd_viva_test.go`); cada suite nueva que necesite otro adaptador añade
  su `-e` en el mismo commit. ✎ **D-F1-8 (2026-10-02)**: el comando anterior usaba `-deps` sin `-test`, que solo veía
  `doc.go` y no detectaba nada; con `-test -deps` marcaría lo transitivo de la propia suite (`nucleo/contact`,
  `platform/*`). `-test` no hace falta: `TestImports` y `XTestImports` son campos del paquete base. Muerde: una sonda
  `_test.go` con `//go:build integracion` que importe `internal/platform/storage/postgres` sale en la lista. ✎ **D-F1-10 (2026-10-02)**: el filtro era
  `.*test$`, que admitía también un paquete de producción `latest` o `contest` —el defecto del hallazgo 21 del
  [README de F1](../F1-nucleo-contact/README.md)—; pasa a `helpertest$`, el sufijo que reconocen los candados. Hoy da vacío con
  los dos filtros: `test/procesos` no importa nada de `internal/`. D-F1-8 se decidió el 2026-10-02 (arriba).
- **R9.4.e** · **MIENTRAS** corre la suite, **EL** gate **DEBERÁ** leer el `rc` del log y contar
  `--- SKIP` = 0 y `--- FAIL` = 0 con `-v`. — Verifica: bloque «Antes de dar un proceso por bueno»
  de la skill `procesos-testcontainers`.

## H9.5 · Las suites de contrato contra Postgres

> Como **la sesión web**, quiero que las suites de contrato de cada puerto (E-6) corran también
> contra su adaptador Postgres, para que el SQL de los 20 adaptadores quede probado sin tests de
> integración por fichero.

- **R9.5.a** · **CUANDO** un módulo conmuta (ola 9C) — o en 9D si D-F9-1 se rechaza —, **EL** fichero
  `test/procesos/<paquete>_contrato_test.go` (convención que estrena F1, T1.13) **DEBERÁ** ejecutar
  `…helpertest.Contrato(t, nuevo)` del puerto con un `nuevo` que abre el adaptador Postgres sobre **una
  base clonada propia**. — Verifica: `WAPP_PROCESOS_BINARIO=viejo GOWORK=off go test -tags integracion -v -run '<Paquete>' ./test/procesos/`
  con un `--- PASS` por puerto (tabla de `diseno.md` §5). ✎ 2026-10-02: `TestMain` sale con código 2 sin `GOWORK=off` (D-F9-7) y sin
  `WAPP_PROCESOS_BINARIO=viejo|nuevo` (desde T9.5); a la suite le da igual cuál de los dos.
- **R9.5.b** · **AL** cerrar F9, **EL** conjunto de suites **DEBERÁ** cubrir los **22** paquetes con
  SQL medidos (`05` E-6 dice 20; ver `diseno.md` §5). — Verifica: la tabla de `diseno.md` §5 con 22 filas marcadas y su
  `--- PASS` en el log de T9.30.

## H9.6 · Los candados de invariante que necesitan BD

> Como **Jhoan**, quiero que las reglas de seguridad que solo se ven contra Postgres queden como
> aserciones de un proceso, para no perder la memoria de incidentes al borrar los candados AST viejos.

- **R9.6.a** · **EL** proceso P2 **DEBERÁ** asertar, sobre Postgres, las reglas de
  `internal/iam/infra/postgres/{canje_orden,canje_una_consulta,membresia_unica}_ast_test.go`
  (leídas antes, E-8) como conducta observable: N canjes concurrentes de la misma invitación dan
  **un** éxito y **una** membresía. — Verifica: `TestP2_InvitacionUnSoloCanje`.
- **R9.6.b** · **EL** proceso P2 **DEBERÁ** comprobar I-CP-5: con un Context Token de administradora
  **de cliente**, **cada** una de las 10 rutas `.any` del plano de plataforma
  (`internal/bootstrap/arranque/rutas_admin.go:76-94`) responde 403; con `platform_admin`, no.
  — Verifica: `TestP2_RutasDePlataformaDenegadasAlCliente` (tabla de 10 casos).
- **R9.6.c** · **EL** proceso P5 **DEBERÁ** asertar INV-1 (la aprobación tiene una sola puerta) como
  conducta: dos aprobaciones de la misma solicitud dejan **un** `intake_approved` en `flow_events`.
  — Verifica: `TestP5_AprobarDosVecesUnSoloEfecto`.
- **R9.6.d** · *(D-F1-11, decisión de Jhoan, 2026-10-02)* **EL** proceso P3 **DEBERÁ** asertar, sobre Postgres y
  contra los dos binarios, las tres reglas de `public.contacts` que `internal/nucleo/contact` difiere a F9 y
  que hoy solo fijan los tests de integración viejos de `internal/flujos/contact` (F10 los borra): **R-27** (el
  nombre tardío se sella), **R-28** (gana el primer nombre) y **R-29** (ráfaga sin `40P01`, con la siembra **sin**
  nombre como precondición afirmada). No son candados AST de `05` §3.2: entran en esta historia porque el
  motivo es el mismo, no perder una regla que solo se ve contra Postgres. — Verifica:
  `TestP3_LatePushNameIsSealed`, `TestP3_FirstPushNameWins` y `TestP3_HistoryBurstWithoutDeadlock` (pasos 6–8 de
  P3 y su tabla, `diseno.md` §4; nombres en inglés por `05` E-11).

## H9.7 · El reparto web ↔ local

> Como **la sesión web**, quiero escribir y compilar los procesos, y correrlos como pre-chequeo si
> mi Docker lo permite, para que la sesión local solo tenga que cerrar.

- **R9.7.a** · **EL** `ci-local` **DEBERÁ** incluir `GOWORK=off go vet -tags integracion
  ./test/procesos/...`. — Verifica: `grep -n 'tags integracion' Makefile`.
- **R9.7.b** · **DONDE** la prueba de T9.12 confirme que testcontainers funciona en la web, **LA**
  sesión web **DEBERÁ** correr `make test-procesos` como pre-chequeo y citar su log en el traspaso,
  **sin** declarar el proceso cerrado. — Verifica: sección §3 del traspaso con «pre-chequeo web».
- **R9.7.c** · **EL** cierre de cada bloque de F9 **DEBERÁ** hacerlo la sesión local con
  `make test-procesos` contra los dos binarios. — Verifica: sección `CERRADO <fecha>` del traspaso.

## H9.8 · Condición del relevo

> Como **la operación de UAT**, quiero que el binario nuevo haya pasado todos los procesos contra
> Postgres 17 antes de sustituir al viejo, para que la prueba de UAT (F10) no sea la primera vez que
> el SQL nuevo toca una base.

- **R9.8.a** · **AL** cerrar F9, **LA** suite **DEBERÁ** pasar `-count=3` contra `viejo` y contra
  `nuevo`, con 0 SKIP y 0 FAIL. — Verifica: T9.30, dos logs con `RC=0`.
- **R9.8.b** · **LA** dueña del negocio **NO DEBERÁ** notar diferencia: cada respuesta HTTP y cada
  frame gRPC que un proceso aserta es igual contra los dos binarios. — Verifica: los mismos tests,
  sin ramas por binario (`grep -n 'WAPP_PROCESOS_BINARIO' test/procesos/*_test.go` solo en
  `main_test.go`).
