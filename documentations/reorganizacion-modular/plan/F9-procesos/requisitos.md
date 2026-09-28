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
  — Verifica: `DOCKER_HOST=unix:///nada make test-procesos; echo rc=$?` → `rc≠0` y 0 `--- SKIP`.
- **R9.1.e** · **EL** arnés **DEBERÁ** lanzar cada servidor con un entorno **construido desde cero**
  (nunca `os.Environ()`), con los cuatro listeners en `127.0.0.1:<puerto libre>`. — Verifica: el
  candado de H9.2 falla si aparece `os.Environ()` en `test/procesos/`; `TestArnes_EntornoLimpio`
  arranca con `WAPP_DB_HOST=trampa` exportado en el shell y el servidor igualmente usa el contenedor.
- **R9.1.f** · **MIENTRAS** dos procesos corren en paralelo (`t.Parallel()`), **EL** arnés **DEBERÁ**
  darles puertos y bases disjuntos. — Verifica: `make test-procesos` con `-parallel 4` sin
  `address already in use` en los logs (`grep -c 'address already in use'` = 0).

## H9.2 · El candado: nunca un Postgres vivo

> Como **Jhoan**, quiero que el gate falle si alguien apunta un test de proceso a un Postgres vivo o
> esconde un rojo, para que el vicio de `WAPP_TEST_DB_DSN` y la deuda DT-52 no vuelvan.

- **R9.2.a** · **SI** algún fichero de `test/procesos/` contiene `WAPP_TEST_DB_DSN`,
  `WithReuseByName`, `:5432`, `localhost:5432`, `os.Environ()` o `t.Skip`, **ENTONCES EL** test
  `TestSinBDViva` **DEBERÁ** fallar nombrando fichero y línea. — Verifica: `GOWORK=off go test -run
  TestSinBDViva ./test/procesos/; echo rc=$?` con un fichero trampa → `rc≠0`.
- **R9.2.b** · **EL** candado **DEBERÁ** correr en `make ci-local` **sin Docker** (fichero sin
  etiqueta de build; el resto del paquete lleva `//go:build integracion`). — Verifica:
  `GOWORK=off go test -v ./test/procesos/ 2>&1 | grep -c -- '--- PASS: TestSinBDViva'` = 1 sin Docker.
- **R9.2.c** · **EL** candado **DEBERÁ** probar su propio detector con casos sintéticos (cadenas en
  memoria), para que un detector roto no dé verde. — Verifica: `TestSinBDViva_DetectaCadaPatron`.

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
  `WAPP_PROCESOS_BINARIO=otro go test -tags integracion ./test/procesos/; echo rc=$?` → `rc≠0`.
- **R9.4.b** · **CUANDO** se escribe un proceso nuevo, **EL** proceso **DEBERÁ** pasar contra `viejo`
  antes de su commit `procesos(<proceso>)`. — Verifica: el traspaso del bloque cita el log con
  `RC=0` contra `viejo` y el conteo `--- PASS`.
- **R9.4.c** · **SI** un proceso falla contra `nuevo` y pasa contra `viejo`, **ENTONCES** la sesión
  **DEBERÁ** registrarlo como hallazgo de la reconstrucción (no se toca el test). — Verifica: sección
  §7 del traspaso.
- **R9.4.d** · **EL** proceso **DEBERÁ** entrar solo por las puertas reales (HTTP `:8100`/`:8103`,
  gRPC `:8101`/`:8102`) y leer Postgres por SQL; **NO DEBERÁ** importar paquetes de dominio. —
  Verifica: `GOWORK=off go list -tags integracion -deps ./test/procesos | grep 'wapp-cloud-platform/internal/' | grep -v '/internal/modulos/.*test$\|/internal/nucleo/.*test$'`
  vacío (solo se admiten los paquetes `…test` de suites de contrato, H9.5).
- **R9.4.e** · **MIENTRAS** corre la suite, **EL** gate **DEBERÁ** leer el `rc` del log y contar
  `--- SKIP` = 0 y `--- FAIL` = 0 con `-v`. — Verifica: bloque «Antes de dar un proceso por bueno»
  de la skill `procesos-testcontainers`.

## H9.5 · Las suites de contrato contra Postgres

> Como **la sesión web**, quiero que las suites de contrato de cada puerto (E-6) corran también
> contra su adaptador Postgres, para que el SQL de los 20 adaptadores quede probado sin tests de
> integración por fichero.

- **R9.5.a** · **CUANDO** un módulo conmuta (ola 9C) — o en 9D si D-F9-1 se rechaza —, **EL** fichero
  `test/procesos/suites_<modulo>_test.go` **DEBERÁ** ejecutar `…test.Contrato(t, nuevo)` de cada
  puerto del módulo con un `nuevo` que abre el adaptador Postgres sobre **una base clonada propia**.
  — Verifica: `go test -tags integracion -v -run 'TestSuites_<Modulo>' ./test/procesos/` con un
  `--- PASS` por puerto (tabla de `diseno.md` §5).
- **R9.5.b** · **AL** cerrar F9, **EL** conjunto de suites **DEBERÁ** cubrir los **20** paquetes con
  adaptador Postgres de `05` E-6. — Verifica: la tabla de `diseno.md` §5 con 20 filas marcadas y su
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
