# F3 · Tareas

> Formato de [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills:
> `reconstruir-modulo`, `contrato-tdd`, `validar-antes-de-cerrar`, `traspaso-web-local` (G→H),
> `procesos-testcontainers` (T3.30). `E` = `internal/modulos/edge`. PR de la web con `--base dev`,
> sin squash. **G-rojo**: `GOWORK=off go vet -tags pendiente ./internal/modulos/edge/...; echo rc=$?` → 0.
> **G-verde**: `GOWORK=off go test -race ./<paquete>/ > "$TMPDIR/t.log" 2>&1; echo rc=$? >> "$TMPDIR/t.log"; tail -1 "$TMPDIR/t.log"` → `rc=0` ·
> `make cobertura-ficheros` ≥ 80 % (salvo Postgres). Todo `rc` sin pipe.

## Bloque A · inventario verificado · 🌐 · T3.1
- [ ] **T3.1 · Verdad de campo, entradas y re-medición** · 🌐 · dep. F2 cerrado · cumple R3.1.a
  - **Ficheros**: `plan/F3-edge/README.md` (estado, SHA, respuestas D-F3-* y D-FX-1/2/3)
  - **Hecho cuando**: las 6 entradas comprobadas; tabla §1 de arquitectura re-medida; confirmado con `go doc` que `session.ErrSessionOffline` viejo **es** el de `platform` (E3) — si F0 no lo hizo así, D-F3-2 cae y se aplica el puente de FX D-FX-3; contados los `time.Sleep` de los tests viejos de `grpc` (T-16).
  - **Gate**: `make ci-local` → `GATE_RC=0` · **Commit**: `docs(reorganizacion-modular): F3 arranca — entradas verificadas`

## Bloque B · rojo de las hojas · 🌐 · T3.2–T3.9
Para cuando: `session`, `inferstats`, `receipts`, `ingest`, `diagnostics`, `lease`, `enroll` en rojo con sus suites · PR.

- [ ] **T3.2 · rojo(edge): `session/registry.go`** · 🌐 · dep. T3.1 · cumple R3.1.a–b · R-S1…R-S4; `ErrSessionOffline` = el de `platform` · **Gate**: G-rojo · **Commit**: `rojo(edge): contrato de session/registry`
- [ ] **T3.3 · rojo(edge): `inferstats/inferstats.go`** · 🌐 · `Agregado` alias de `platform/metrics` · **Gate**: G-rojo · **Commit**: `rojo(edge): contrato de inferstats`
- [ ] **T3.4 · rojo(edge): `receipts` (3) + `receiptshelpertest`** · 🌐 · cumple R3.2.a–b · doble nace completo y verde contra la suite (D-F3-1) · **Gate**: G-rojo · `go test -race ./internal/modulos/edge/receipts/receiptshelpertest/` rc=0 · **Commit**: `rojo(edge): contratos de receipts y su suite`
- [ ] **T3.5 · rojo(edge): `ingest` (2 + `deduper.go` ✚) + `ingesthelpertest`** · 🌐 · cumple R3.2.a · D-F3-3 · **Gate**: igual · **Commit**: `rojo(edge): contratos de ingest y su suite`
- [ ] **T3.6 · rojo(edge): `diagnostics` (2) + `diagnosticshelpertest`** · 🌐 · cumple R3.2.a · **Gate**: igual · **Commit**: `rojo(edge): contratos de diagnostics y su suite`
- [ ] **T3.7 · rojo(edge): 🔒 `lease` (4) + `leasehelpertest`** · 🌐 · cumple R3.3.a–e
  - **Ficheros**: `E/lease/{lease,repository,repository_postgres,signingkey}.go` y 4 `_test.go`, `E/lease/leasehelpertest/{suite,memoria,memoria_test}.go`, `…/repository_integracion_test.go` (`integracion`)
  - **Hecho cuando**: R-L1…R-L8 en los comentarios, con la cita de ADR-0007 en el de paquete; suite con «Upsert no resucita» y los dos sujetos de corte; SQL de `repository_postgres.go` **pendiente de copiar literal** en el verde (T-9); los tests usan claves Ed25519 generadas en el test, nunca un fichero.
  - **Gate**: G-rojo · **Commit**: `rojo(edge): contrato del lease (mitad servidora de la doble llave)`
- [ ] **T3.8 · rojo(edge): `enroll` (7, `doc.go` sin test) + `enrollhelpertest`** · 🌐 · cumple R3.1.d, R3.2.a · CA de prueba con `NewDevCA`; `EnrollEdge` por `bufconn` · **Gate**: G-rojo · **Commit**: `rojo(edge): contratos de enroll y sus suites`
- [ ] **T3.9 · Cierre del bloque B** · 🌐 · pendientes de `edge` anotados = `make test-pendiente`; `ci-local` rc=0; PR · **Gate**: `validar-antes-de-cerrar`

## Bloque C · rojo de `fleet`, `filtercfg`, `grpc` · 🌐 · T3.10–T3.14
- [ ] **T3.10 · rojo(edge): `fleet` (2) + `fleethelpertest` (suite, memoria, `slowrepo.go`)** · 🌐 · dep. T3.9 · cumple R3.2.a · suite de diseño §2 completa; `slowrepo.go` con test propio (tiene lógica) · **Gate**: G-rojo · **Commit**: `rojo(edge): contratos de fleet y su suite`
- [ ] **T3.11 · rojo(edge): `filtercfg/filtercfg.go`** · 🌐 · R-C1…R-C5 · **Gate**: G-rojo · **Commit**: `rojo(edge): contrato de filtercfg`
- [ ] **T3.12 · rojo(edge): `grpc` — tipos, servidor y envío** (`types`, `server`, `send`, `receipt_sink`, `worklane`) · 🌐 · dep. T3.10 · cumple R3.5.d, R3.6.a · R-G3, R-G11, R-G12 · **Gate**: G-rojo · **Commit**: `rojo(edge): contrato de grpc/<fichero>` — uno por fichero
- [ ] **T3.13 · rojo(edge): `grpc` — conexión, auth, config, readiness** (`connect`, `auth`, `config_push`, `readiness`, `diagnostics`) · 🌐 · cumple R3.4.a–c, R3.5.d · R-G1…R-G10, R-G16, R-G17, R-G19…R-G21 · **Gate**: G-rojo
- [ ] **T3.14 · rojo(edge): `grpc` — inferencia, plaza y aviso** (`inference`, `plaza`, `greeting`) · 🌐 · cumple R3.4.d, R3.5.b · R-G13…R-G15, R-G23…R-G30; el test del literal lee el `.md` por la ruta de T-11 y **falla** si no lo encuentra · **Gate**: G-rojo · cierre del bloque con `validar-antes-de-cerrar` y PR

## Bloque D · verde de las hojas · 🌐 · T3.15–T3.18
- [ ] **T3.15 · verde(edge): `session`, `inferstats`** · 🌐 · dep. T3.14 · 2 commits `verde(edge): <paq>/<f>` · **Gate**: G-verde
- [ ] **T3.16 · verde(edge): `receipts`, `ingest`, `diagnostics`** · 🌐 · 7 commits · **Gate**: G-verde
- [ ] **T3.17 · verde(edge): 🔒 `lease`** · 🌐 · 4 commits; SQL literal; `git diff --no-index` de la lógica de `Manager` contra la vieja sin cambios de comportamiento (revisión explícita en el PR) · **Gate**: G-verde
- [ ] **T3.18 · verde(edge): `enroll`** · 🌐 · 6 commits · cierre del bloque con PR · **Gate**: G-verde

## Bloque E · verde de `fleet` y `filtercfg` · 🌐 · T3.19–T3.20
- [ ] **T3.19 · verde(edge): `fleet/fleet.go`, `fleet/repository_postgres.go`, `fleethelpertest/slowrepo.go`** · 🌐 · dep. T3.18 · el índice ciego usa `nucleo/contact.Normalize` · **Gate**: G-verde
- [ ] **T3.20 · verde(edge): `filtercfg`** · 🌐 · **Gate**: G-verde · PR

## Bloque F · verde de `grpc` · 🌐 · T3.21–T3.23
Para cuando: 0 pendientes en `edge`; literal y pareja ADR-0048 verdes; PR.
- [ ] **T3.21 · verde(edge): `types`, `session`-dependientes y `worklane`, `send`, `receipt_sink`, `server`** · 🌐 · dep. T3.20 · 5 commits · **Gate**: G-verde
- [ ] **T3.22 · verde(edge): `connect`, `auth`, `config_push`, `readiness`, `diagnostics`** · 🌐 · 5 commits; `connect.go` (1.143 l) puede partirse en dos commits `verde` si pasa de una sesión · **Gate**: G-verde
- [ ] **T3.23 · verde(edge): `inference`, `plaza`, `greeting`** · 🌐 · 3 commits; `grep -rn 'pendiente.Implementar' internal/modulos/edge | wc -l` → 0 · **Gate**: G-verde · `make ci-local` rc=0 · PR

## Bloque G · puente, cara nueva y conmutación · 🌐 (TX.10 🌐→💻) · T3.24–T3.28
Para cuando: huella igual · un gw · 6 + 6 rutas · `puente_iam.go` borrado · PR · traspaso.

- [ ] **T3.24 · rojo(arranque): `puente_gateway.go`** · 🌐 · dep. T3.23 · cumple R3.6.b
  - **Ficheros**: `internal/arranque/puente_gateway.go`, `…/puente_gateway_test.go`
  - **Hecho cuando**: `puenteGateway` no exportado, **sin campos en el rojo** (el campo `gw *grpc.Server` nace con el verde, T3.25: `unused` lo marcaría, [`../00-marco/tecnologia.md`](../00-marco/tecnologia.md) §3.1), con `Infer(ctx, tenantID, viejo.InferRequest)` (conversión campo a campo) y `PlazaDe` en `panic`; `var _ local.Frame = (*puenteGateway)(nil)`; el test afirma la conversión de **todos** los campos de `InferRequest` y que `any(p).(interface{ PlazaDe(string, string) (string, bool) })` da `true`.
  - **Gate**: G-rojo sobre `./internal/arranque/...` · **Commit**: `rojo(arranque): contrato de puente_gateway`
- [ ] **T3.25 · verde(arranque): `puente_gateway.go`** · 🌐 · **Gate**: G-verde · **Commit**: `verde(arranque): puente_gateway`
- [ ] **T3.26 · FX TX.8 + TX.9 (cara nueva: edge)** · 🌐 · dep. T3.23 · cumple R3.6.e
  - **Ficheros**: los de [`FX-cara-http/tareas.md`](../FX-cara-http/tareas.md) TX.8 (`apipublica/{plazos,limits,messages,sessions,health,sessionadmin,diagnostics}.go`)
  - **Hecho cuando**: lo que dicen TX.8 y TX.9; `sessionadmin.go` **exporta** los dos constructores (D-FX-2); `messages.go` mapea los centinelas de `E/session`; **ningún** puente desde `apipublica` (E1–E2 se mudan en F7, D-FX-1/D-F7-4; *alternativa literal*: `intents.go` aquí con puente a `internal/intentcfg`, retira TX.21). Se marcan TX.8 y TX.9.
- [ ] **T3.27 · FX TX.10: identidad de `ErrSessionOffline`** · 🌐→💻 · dep. T3.26 · cumple R3.6.a
  - **Hecho cuando**: con D-F3-2, `E/session.ErrSessionOffline` **es** el centinela de `platform` y `errors.Is(nuevo, viejo)` es `true` **sin** puente en `fronteras_test.go`; si D-F3-2 se rechaza, se aplica D-FX-3 literal (puente declarado, «retira: TX.24»). La local confirma con el e2e: `/admin/flows/start` a una sesión offline da el mismo código que el binario viejo.
  - **Gate**: `go test -count=1 -v ./internal/arranque/... > "$TMPDIR/a.log" 2>&1; echo rc=$? >> "$TMPDIR/a.log"` → `rc=0`
  - **Commit**: `refactor(edge): el centinela de sesión offline conserva su identidad`
- [ ] **T3.28 · conmutar(edge): un solo gw, 6 + 6 rutas (= FX TX.11)** · 🌐 · dep. T3.25–T3.27 · cumple R3.5.a, R3.5.c, R3.6.a–e
  - **Ficheros**: en `internal/arranque`: copias de `fase4_gateway.go`, `fase3_almacenes.go`, `fase5_captacion.go` (`WithFrame(puenteGateway)`), `fase6_solicitudes.go`, `fase7_flujos.go`, `fase8_transporte.go`, `pki.go`, `lease.go`, `edge_config.go` (config providers con `ConfigPayload` nuevo; partido de `auth.go` en T2.34), `http.go`, `rutas_admin.go`, `contenedor.go`; **borrar** `puente_iam.go` y su test; `traspasos/TRASPASO-F3-edge.md`
  - **Hecho cuando**: **un** `grpc.New` con las 12 opciones y los valores de siempre, `WithAuthenticator`/`WithAuthAuditor` de `acceso` nuevo; el **mismo** gw en runtime viejo (`Sender` + 4 hooks), `intakes.NewNotifier`, `filtercfg.NewPusher`, J12–J15, el `ConfigPush` de la cara vieja (E1–E2 hasta F7) y (vía puente) el selector; aserciones de identidad; `Deps` viejos `Sender`, `DiagnosticsRequester`, `Sessions`, `SessionProfiles`, `SessionStatus`, `ProfilePush`, `Diagnostics` = `nil` (`ConfigPush` = gw nuevo, `Intents` = el viejo); D3/D4 y J16/J17 en este mismo commit (T-14); `grep -rn 'gatewaygrpc.New(' internal/arranque` vacío; `go.mod` intacto.
  - **Gate**: `GOWORK=off go test -count=1 -v -run 'Mudanzas|Huella|Cableado|Identidad' ./internal/arranque > "$TMPDIR/m.log" 2>&1; echo rc=$? >> "$TMPDIR/m.log"; tail -1 "$TMPDIR/m.log"` → `rc=0`, 0 SKIP · `make ci-local` rc=0
  - **Commit**: `conmutar(edge): el arranque nuevo cablea edge con un solo gateway y muda 6 rutas`

## Bloque H · cierre local · 💻 (🌐→💻) · T3.29–T3.30
- [ ] **T3.29 · Cierre de F3 con mTLS real** · 🌐→💻 · dep. T3.28
  - **Hecho cuando**: la local repite `validar-antes-de-cerrar`; e2e de `cmd/server-modular` (F0) verde con **enrolamiento real** en `:8102` (código de un solo uso → certificado), `Connect` en `:8101` con ese certificado (y rechazo de uno ajeno), lease inicial recibido, `POST /admin/leases/revoke` → `LeaseUpdate` revocado y el Edge de prueba deja de poder operar; login de operador por el canal de control con **dos** Edge de prueba a la vez (cada uno recibe el suyo); `ESTADO.md` y README → «cerrada» con SHA; traspaso con `CERRADO`.
  - **Gate**: `make ci-local` rc=0 en local · **Commit**: `docs(reorganizacion-modular): F3 cerrada`
- [ ] **T3.30 · Proceso «Enrolamiento de un Edge y su lease» contra el binario nuevo (= T9.24, 9C de `edge`)** · 🌐→💻 · con **D-F9-1 = sí** (recomendación) · cumple R3.7.a–b
  - **Hecho cuando**: el proceso incluye lo de [`diseno.md`](diseno.md) §6 y pasa contra `cmd/server` y `cmd/server-modular`, con las suites de `enroll`, `fleet`, `lease`, `diagnostics`, `ingest`, `receipts` contra Postgres; si D-F9-1 = no, la lista se entrega a `plan/F9-procesos/` (T9.34) y la tarea se tacha con ese motivo.
  - **Gate**: `make test-procesos > "$TMPDIR/p.log" 2>&1; echo rc=$? >> "$TMPDIR/p.log"` → `rc=0`, 0 SKIP · **Commit**: `procesos(enrolamiento-lease): …`
