# F3 · Tareas

> Formato de [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills:
> `reconstruir-modulo`, `contrato-tdd`, `validar-antes-de-cerrar`, `traspaso-web-local` (F3-04 → F3-05, solo mientras
> existan los dos entornos), `procesos-testcontainers` (T3.30). `E` = `internal/modulos/edge`. PR de la web con
> `--base dev`, sin squash. **G-rojo**: `GOWORK=off go vet -tags pendiente ./internal/modulos/edge/...; echo rc=$?` → 0.
> **G-verde**: `GOWORK=off go test -race ./<paquete>/ > "$TMPDIR/t.log" 2>&1; echo rc=$? >> "$TMPDIR/t.log"; tail -1 "$TMPDIR/t.log"` → `rc=0`,
> con **un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9**. `make cobertura-ficheros` es
> un **informe**: la tabla va al PR; no bloquea (sin umbral de cobertura, P2). Todo `rc` sin pipe.
>
> **Ceremonia por nivel** ([`05`](../../05-metodo-contratos-y-tdd.md) E-12, [`reglas.md`](reglas.md) §0): las tareas
> conservan su número y su pareja `rojo`/`verde`, pero se ejecutan **por paquete**: en nivel **simple**, la `rojo` y su
> `verde` son una sola pasada (las dos se marcan `[x]` con el mismo SHA); en **medio**, rojo y verde por archivo dentro
> del paquete; en **complejo**, el esquema completo E-2…E-9. El nivel lo fija el inventario aprobado (T3.1).
>
> **Correspondencias de F3-01 (E-11)**: `session.SendAcotado` → `BoundedSend`, `ErrPushAbandonado` → `ErrPushAbandoned`;
> `inferstats`: `Parte` → `Report`, `Clave` → `Key`, `Agregado` → `Aggregate` (sigue siendo alias de `platform/metrics/inferencia.Agregado`),
> `Observa` → `Observe`, `Agrega` → `Aggregated`, y los campos `PorRegimen`/`PorClase`/`OmitidasPorMotivo`/`MuestrasPrefill`/`MuestrasGeneracion`
> → `ByRegime`/`ByClass`/`SkippedByReason`/`PrefillSamples`/`GenerationSamples`. Los dobles: `Memory…` → `<paq>helpertest.Memoria…` (D-F3-1).
> F3-03 (`grpc`) y F3-04 (arranque) usan estos nombres.
>
> **Correspondencias de F3-02 (E-11)**: los exportados de `fleet`, `fleettest` y `filtercfg` ya estaban en inglés y conservan
> su nombre. Única mudanza: `fleet.MemoryRepository` / `NewMemoryRepository` → `fleethelpertest.Memoria` / `NewMemoria` (D-F3-1);
> `fleettest.SlowRepository` / `NewSlow` → `fleethelpertest.SlowRepository` / `NewSlow`. `repository_postgres.go` nace partido en
> `repository_postgres{,_selfpn,_greeting,_profile,_health}.go` (E-13).
>
> **Nombres (E-11)**: el adaptador de arranque que esta spec llamaba «puente gateway» (tipo `puenteGateway`) es
> `bridge_gateway.go` / `gatewayBridge`; el de F2 es `bridge_iam.go`. «Puente» a secas queda para los imports
> nuevo → viejo de `05` §4.1.

## Bloque F3-01 · inventario E-12 y hojas · 🌐 · T3.1–T3.9, T3.15–T3.18
Para cuando: 0 pendientes en `session`, `inferstats`, `receipts`, `ingest`, `diagnostics`, `lease`, `enroll`; sus
suites verdes contra los dobles · PR. Puntos limpios si no cabe en ~90 min: tras T3.1, tras T3.9, tras T3.17.

- [x] (`3ae565c`) **T3.1 · Inventario E-12, verdad de campo y entradas** · 🌐 · dep. F2 cerrado · cumple R3.1.a
  - **Ficheros**: `plan/F3-edge/README.md` (estado, SHA, respuestas D-F3-* y D-FX-1/2/3), `plan/F3-edge/arquitectura.md` §1.1 (la tabla de niveles, ya medida)
  - **Produce**: la tabla `archivo · estado en memoria · concurrencia · BD/transacciones · nº de consumidores · nivel (simple/medio/complejo)` de los 38 (+✚) ficheros, y la **lista de adaptadores**: nace `bridge_gateway.go` (muere en F4), muere `bridge_iam.go` (nació en F2). Si un archivo sale peor de lo previsto, **sube de nivel**.
  - **Además**: las 6 entradas comprobadas; tabla §1 de arquitectura re-medida; confirmado con `go doc` que `session.ErrSessionOffline` viejo **es** el de `platform` (E3) — si F0 no lo hizo así, D-F3-2 cae y se aplica el puente (import) de FX D-FX-3; contados los `time.Sleep` de los tests viejos de `grpc` (T-16).
  - **Hecho cuando**: **Jhoan aprueba la tabla. Antes de eso no se escribe código.**
  - **Gate**: `make ci-local` → `GATE_RC=0` · **Commit**: `docs(reorganizacion-modular): F3 arranca — inventario E-12 y entradas verificadas`
- [x] (`bcd0f4e`) **T3.2 · rojo(edge): `session/registry.go`** · 🌐 · dep. T3.1 · cumple R3.1.a–b · R-S1…R-S4; `ErrSessionOffline` = el de `platform` · **Gate**: G-rojo · **Commit**: `rojo(edge): contrato de session/registry`
- [x] (`6ece585`) **T3.3 · rojo(edge): `inferstats/inferstats.go`** · 🌐 · `Agregado` alias de `platform/metrics` · **Gate**: G-rojo · **Commit**: `rojo(edge): contrato de inferstats`
- [x] (`79d83c8`) **T3.4 · rojo(edge): `receipts` (3) + `receiptshelpertest`** · 🌐 · cumple R3.2.a–b · doble nace completo y verde contra la suite (D-F3-1) · **Gate**: G-rojo · `go test -race ./internal/modulos/edge/receipts/receiptshelpertest/` rc=0 · **Commit**: `rojo(edge): contratos de receipts y su suite`
- [x] (`76c760f`) **T3.5 · rojo(edge): `ingest` (2 + `deduper.go` ✚) + `ingesthelpertest`** · 🌐 · cumple R3.2.a · D-F3-3 · **Gate**: igual · **Commit**: `rojo(edge): contratos de ingest y su suite`
- [x] (`03db7b8`) **T3.6 · rojo(edge): `diagnostics` (2) + `diagnosticshelpertest`** · 🌐 · cumple R3.2.a · **Gate**: igual · **Commit**: `rojo(edge): contratos de diagnostics y su suite`
- [x] (`f783e76` (sin `repository_integracion_test.go`: D-F3-8)) **T3.7 · rojo(edge): 🔒 `lease` (4) + `leasehelpertest`** · 🌐 · cumple R3.3.a–e
  - **Ficheros**: `E/lease/{lease,repository,repository_postgres,signingkey}.go` y 4 `_test.go`, `E/lease/leasehelpertest/{suite,memoria,memoria_test}.go`, `…/repository_integracion_test.go` (`integracion`)
  - **Hecho cuando**: R-L1…R-L8 en los comentarios, con la cita de ADR-0007 en el de paquete; suite con `Montaje` (P4), con «Upsert no resucita» y los dos sujetos de corte; SQL de `repository_postgres.go` **pendiente de copiar literal** en el verde (T-9); los tests usan claves Ed25519 generadas en el test, nunca un fichero.
  - **Gate**: G-rojo · **Commit**: `rojo(edge): contrato del lease (mitad servidora de la doble llave)`
- [x] (`a2a5259`) **T3.8 · rojo(edge): `enroll` (7, `doc.go` sin test) + `enrollhelpertest`** · 🌐 · cumple R3.1.d, R3.2.a · CA de prueba con `NewDevCA`; `EnrollEdge` por `bufconn` · **Gate**: G-rojo · **Commit**: `rojo(edge): contratos de enroll y sus suites`
- [x] (gates sobre `8a4dd34`: `ci-local` rc=0, `PENDIENTES=0`, `ROJOS=0`) **T3.9 · Punto limpio de las hojas** · 🌐 · ningún paquete a medias; pendientes de `edge` anotados = `make test-pendiente`; `ci-local` rc=0. Si la sesión se corta aquí, cierra con las tres cosas y se relanza · **Gate**: `validar-antes-de-cerrar`
- [x] (`84c6352`, `b7d5e0b`) **T3.15 · verde(edge): `session`, `inferstats`** · 🌐 · dep. T3.2, T3.3 · 2 commits `verde(edge): <paq>/<f>` · **Gate**: G-verde
- [x] (`178bbe7`, `9dc9045`, `84e0789` · `70e4605`, `9170205` · `832ffd8`, `b086329`, `67129db` (mata un mutante)) **T3.16 · verde(edge): `receipts`, `ingest`, `diagnostics`** · 🌐 · dep. T3.4–T3.6 · 7 commits · **Gate**: G-verde
- [x] (`99f7e75`, `10b8d4e`, `36a054d`, `9b57756`, `715cfcf` (reparto de tests); `lease.go` contra el viejo: 0 líneas de lógica distintas) **T3.17 · verde(edge): 🔒 `lease`** · 🌐 · dep. T3.7 · 4 commits; SQL literal; `git diff --no-index` de la lógica de `Manager` contra la vieja sin cambios de comportamiento (revisión explícita en el PR) · **Gate**: G-verde
- [x] (`17dd728`, `87767bb`, `bb9fd66`, `4decfc1`, `2880255`, `096fb3b`, `8a4dd34`) **T3.18 · verde(edge): `enroll`** · 🌐 · dep. T3.8 · 6 commits · cierre de la sesión con las tres cosas y PR · ✅ `b86dd62` · **Gate**: G-verde

## Bloque F3-02 · `fleet` y `filtercfg` · 🌐 · T3.10, T3.11, T3.19, T3.20
Para cuando: 0 pendientes en `fleet`, `fleethelpertest` y `filtercfg`; suite de `fleet` verde contra su doble · PR.

- [x] **T3.10 · rojo(edge): `fleet` (2) + `fleethelpertest` (suite, memoria, `slowrepo.go`)** · 🌐 · dep. T3.18 · cumple R3.2.a · suite de diseño §2 completa, con `Montaje` (P4); `slowrepo.go` con test propio (tiene lógica) · **Gate**: G-rojo · **Commit**: `rojo(edge): contratos de fleet y su suite` · ✅ `532e62f` (modelo, suite, doble) y `607434c` (los 5 trozos de `repository_postgres`)
- [x] **T3.11 · rojo(edge): `filtercfg/filtercfg.go`** · 🌐 · R-C1…R-C5 · **Gate**: G-rojo · **Commit**: `rojo(edge): contrato de filtercfg` · ✅ `8fa8f44`
- [x] **T3.19 · verde(edge): `fleet/fleet.go`, `fleet/repository_postgres.go`, `fleethelpertest/slowrepo.go`** · 🌐 · dep. T3.10 · el índice ciego usa `nucleo/contact.Normalize`; corpus de equivalencia con casos adversarios (reglas §0) · **Gate**: G-verde · ✅ `d9641b1` (`fleet.go`, corpus de 26 entradas sin divergencia), `792af20`, `147d178` (`slowrepo.go`), `e106e6d` · `70e55d7` · `4b4006b` · `eead41e` · `3b9c91e` (los 5 trozos de `repository_postgres`)
- [x] **T3.20 · verde(edge): `filtercfg`** · 🌐 · dep. T3.11, T3.19 · **Gate**: G-verde · cierre de la sesión con las tres cosas y PR

## Bloque F3-03 · `grpc` (complejo; ADR-0048) · 🌐 · T3.12–T3.14, T3.21–T3.23
Para cuando: 0 pendientes en `edge`; literal y pareja ADR-0048 verdes · PR. Puntos limpios si no cabe en ~90 min: tras
cada tanda (T3.12+T3.21 · T3.13+T3.22 · T3.14+T3.23).

- [ ] **T3.12 · rojo(edge): `grpc` — tipos, servidor y envío** (`types`, `server`, `send`, `receipt_sink`, `worklane`) · 🌐 · dep. T3.20 · cumple R3.5.d, R3.6.a · R-G3, R-G11, R-G12 · **Gate**: G-rojo · **Commit**: `rojo(edge): contrato de grpc/<fichero>` — uno por fichero
- [ ] **T3.13 · rojo(edge): `grpc` — conexión, auth, config, readiness** (`connect`, `auth`, `config_push`, `readiness`, `diagnostics`) · 🌐 · cumple R3.4.a–c, R3.5.d · R-G1…R-G10, R-G16, R-G17, R-G19…R-G21 · **Gate**: G-rojo
- [ ] **T3.14 · rojo(edge): `grpc` — inferencia, plaza y aviso** (`inference`, `plaza`, `greeting`) · 🌐 · cumple R3.4.d, R3.5.b · R-G13…R-G15, R-G23…R-G30; el test del literal lee el `.md` por la ruta de T-11 y **falla** si no lo encuentra · **Gate**: G-rojo · `validar-antes-de-cerrar`
- [ ] **T3.21 · verde(edge): `types`, `session`-dependientes y `worklane`, `send`, `receipt_sink`, `server`** · 🌐 · dep. T3.12 · 5 commits · mutantes en `worklane` y `send` (carril y acks) · **Gate**: G-verde
- [ ] **T3.22 · verde(edge): `connect`, `auth`, `config_push`, `readiness`, `diagnostics`** · 🌐 · dep. T3.13 · 5 commits; `connect.go` (1.143 l) puede partirse en dos commits `verde` si pasa de una sesión · mutantes en `connect` y `readiness` (canal de control, ADR-0048) · **Gate**: G-verde
- [ ] **T3.23 · verde(edge): `inference`, `plaza`, `greeting`** · 🌐 · dep. T3.14 · 3 commits; `grep -rn 'pendiente.Implementar' internal/modulos/edge | wc -l` → 0 · **Gate**: G-verde · `make ci-local` rc=0 · cierre de la sesión con las tres cosas y PR

## Bloque F3-04 · adaptador, cara nueva y conmutación · 🌐 (TX.10 🌐→💻) · T3.24–T3.28
Para cuando: huella igual · un gw · 6 + 6 rutas · `bridge_gateway.go` con su test de cableado en verde ·
`bridge_iam.go` borrado y `acceso` en `Conmutados` · PR (y traspaso, mientras existan los dos entornos).

- [ ] **T3.24 · `bridge_gateway.go` en una pasada (nivel simple)** · 🌐 · dep. T3.23 · cumple R3.6.b
  - **Ficheros**: `internal/arranque/bridge_gateway.go`, `…/bridge_gateway_test.go`
  - **Hecho cuando**: `gatewayBridge` no exportado, **sin estado** (solo el campo `gw *grpc.Server`), con `Infer(ctx, tenantID, viejo.InferRequest)` (conversión campo a campo) y `PlazaDe`; `var _ local.Frame = (*gatewayBridge)(nil)`; el test afirma la conversión de **todos** los campos de `InferRequest` y que `any(p).(interface{ PlazaDe(string, string) (string, bool) })` da `true`. Contrato, test y lógica en la misma pasada (`05` §4.2).
  - **Gate**: G-verde sobre `./internal/arranque/...` · **Commit**: `verde(arranque): bridge_gateway`
- [ ] **T3.25 · Test de cableado completo de `bridge_gateway` (hallazgo 39 de F1)** · 🌐 · dep. T3.24 · cumple R3.6.f
  - **Hecho cuando**: el test afirma que el arranque construye el `edge/grpc.Server` **nuevo** *y* que ninguna fase de `internal/arranque` importa `internal/gateway/grpc` fuera de `bridge_gateway.go` (grep por ruta de import), no solo el campo del contenedor. Se escribe aquí y da verde con T3.28 (va en ese commit o en el siguiente).
  - **Gate**: el de T3.28 (`-run Cableado`) · **Commit**: `verde(arranque): cableado de bridge_gateway`
- [ ] **T3.26 · FX TX.8 + TX.9 (cara nueva: edge)** · 🌐 · dep. T3.23 · cumple R3.6.e
  - **Ficheros**: los de [`FX-cara-http/tareas.md`](../FX-cara-http/tareas.md) TX.8 (`apipublica/{plazos,limits,messages,sessions,health,sessionadmin,diagnostics}.go`)
  - **Hecho cuando**: lo que dicen TX.8 y TX.9; `sessionadmin.go` **exporta** los dos constructores (D-FX-2); `messages.go` mapea los centinelas de `E/session`; **ningún** puente (import) desde `apipublica` (E1–E2 se mudan en F7, D-FX-1/D-F7-4; *alternativa literal*: `intents.go` aquí con puente a `internal/intentcfg`, retira TX.21). Se marcan TX.8 y TX.9.
- [ ] **T3.27 · FX TX.10: identidad de `ErrSessionOffline`** · 🌐→💻 · dep. T3.26 · cumple R3.6.a
  - **Hecho cuando**: con D-F3-2, `E/session.ErrSessionOffline` **es** el centinela de `platform` y `errors.Is(nuevo, viejo)` es `true` **sin** puente (import) en `fronteras_test.go`; si D-F3-2 se rechaza, se aplica D-FX-3 literal (puente declarado, «retira: TX.24»). La parte 💻 (F3-05) confirma con el e2e: `/admin/flows/start` a una sesión offline da el mismo código que el binario viejo.
  - **Gate**: `go test -count=1 -v ./internal/arranque/... > "$TMPDIR/a.log" 2>&1; echo rc=$? >> "$TMPDIR/a.log"` → `rc=0`
  - **Commit**: `refactor(edge): el centinela de sesión offline conserva su identidad`
- [ ] **T3.28 · conmutar(edge): un solo gw, 6 + 6 rutas (= FX TX.11)** · 🌐 · dep. T3.24, T3.26, T3.27 · cumple R3.5.a, R3.5.c, R3.6.a–f
  - **Ficheros**: en `internal/arranque`: copias de `fase4_gateway.go`, `fase3_almacenes.go`, `fase5_captacion.go` (`WithFrame(gatewayBridge)`), `fase6_solicitudes.go`, `fase7_flujos.go`, `fase8_transporte.go`, `pki.go`, `lease.go`, `edge_config.go` (config providers con `ConfigPayload` nuevo; partido de `auth.go` en T2.34), `http.go`, `rutas_admin.go`, `contenedor.go`; **borrar** `bridge_iam.go` y su test; `internal/modulos/fronteras_test.go` (`Conmutados`); `traspasos/TRASPASO-F3-edge.md` (solo mientras existan los dos entornos)
  - **Hecho cuando**: **un** `grpc.New` con las 12 opciones y los valores de siempre, `WithAuthenticator`/`WithAuthAuditor` de `acceso` nuevo; el **mismo** gw en runtime viejo (`Sender` + 4 hooks), `intakes.NewNotifier`, `filtercfg.NewPusher`, J12–J15, el `ConfigPush` de la cara vieja (E1–E2 hasta F7) y (vía el adaptador) el selector; aserciones de identidad; `Deps` viejos `Sender`, `DiagnosticsRequester`, `Sessions`, `SessionProfiles`, `SessionStatus`, `ProfilePush`, `Diagnostics` = `nil` (`ConfigPush` = gw nuevo, `Intents` = el viejo); D3/D4 y J16/J17 en este mismo commit (T-14); `grep -rn 'gatewaygrpc.New(' internal/arranque` vacío; `go.mod` intacto. **`Conmutados`**: al morir `bridge_iam.go`, **`acceso` entra**; `edge` **no** entra aún (su adaptador `bridge_gateway.go` vive hasta F4). `FaseActual` no cambia de mecanismo.
  - **Gate**: `GOWORK=off go test -count=1 -v -run 'Mudanzas|Huella|Cableado|Identidad' ./internal/arranque > "$TMPDIR/m.log" 2>&1; echo rc=$? >> "$TMPDIR/m.log"; tail -1 "$TMPDIR/m.log"` → `rc=0`, 0 SKIP · `make ci-local` rc=0
  - **Commit**: `conmutar(edge): el arranque nuevo cablea edge con un solo gateway y muda 6 rutas`

## Bloque F3-05 · cierre local con mTLS · 💻 · T3.29–T3.30 (y la parte 💻 de T3.27)
Para cuando: e2e gRPC con mTLS real verde · las 7 suites verdes contra Postgres con el arnés · proceso de
enrolamiento verde contra los dos binarios, 0 SKIP · `dev` empujado.

- [ ] **T3.29 · Cierre de F3 con mTLS real** · 🌐→💻 · dep. T3.28
  - **Hecho cuando**: la local repite `validar-antes-de-cerrar`; e2e de `cmd/server-modular` (F0) verde con **enrolamiento real** en `:8102` (código de un solo uso → certificado), `Connect` en `:8101` con ese certificado (y rechazo de uno ajeno), lease inicial recibido, `POST /admin/leases/revoke` → `LeaseUpdate` revocado y el Edge de prueba deja de poder operar; login de operador por el canal de control con **dos** Edge de prueba a la vez (cada uno recibe el suyo); `ESTADO.md` y README → «cerrada» con SHA; traspaso con `CERRADO` si lo hubo.
  - **Gate**: `make ci-local` rc=0 en local · **Commit**: `docs(reorganizacion-modular): F3 cerrada`
- [ ] **T3.30 · Proceso «Enrolamiento de un Edge y su lease» contra el binario nuevo (= T9.24, 9C de `edge`)** · 🌐→💻 · con **D-F9-1 = sí** (recomendación) · cumple R3.2.b, R3.7.a–b
  - **Hecho cuando**: el proceso incluye lo de [`diseno.md`](diseno.md) §6 y pasa contra `cmd/server` y `cmd/server-modular`; las **7 suites** de puerto con BD (`lease.Repository`, `enroll.CodeStore`, `enroll.EdgeCertRepository`, `fleet.Repository`, `diagnostics.Store`, `receipts.Store`, `ingest.Deduper`) corren **en memoria y en Postgres** con el arnés de F9-A (P4); si D-F9-1 = no, la lista se entrega a `plan/F9-procesos/` (T9.34) y la tarea se tacha con ese motivo.
  - **Gate**: `make test-procesos > "$TMPDIR/p.log" 2>&1; echo rc=$? >> "$TMPDIR/p.log"` → `rc=0`, 0 SKIP · **Commit**: `procesos(enrolamiento-lease): …`
