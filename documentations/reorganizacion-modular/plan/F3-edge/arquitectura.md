# F3 · Arquitectura — la vista macro de `edge`

> Medido el 2026-09-28 sobre `dev` @ `1b18932` (sin cambios en `bad573a`). `E` = `internal/modulos/edge`.

## 1 · Paquetes viejos → nuevos (aplanado: `gateway/x` → `edge/x`)

| Paquete viejo (referencia) | Nuevo | Prod. | Líneas | Export. | Tests viejos · `Test*` (BD) | Puerto · adaptador Postgres · gemelo |
|---|---|---:|---:|---:|---|---|
| `internal/gateway/grpc` | `E/grpc` | 13 | 4.566 | 60 | 37 · 144 (7) | puertos consumidos: `ConfigProvider`, `ReceiptSink` (`types.go`, `receipt_sink.go`) |
| `internal/gateway/enroll` | `E/enroll` | 7 | 673 | 46 | 3 · 13 (3) | `CodeStore` · `store_postgres.go` · `MemoryStore` (`store.go:48`); `EdgeCertRepository` · Postgres y `MemoryEdgeCertRepository` en `edgecert.go` |
| `internal/gateway/lease` 🔒 | `E/lease` | 4 | 611 | 39 | 3 · 26 (8) | `Repository` · `repository_postgres.go` · `MemoryRepository` (`repository.go:62`) |
| `internal/gateway/session` | `E/session` | 1 | 228 | 14 | 1 · 7 (0) | `Sender` |
| `internal/gateway/fleet` | `E/fleet` | 2 | 1.665 | 48 | 6 · 33 (13) | `Repository` · `repository_postgres.go` · `MemoryRepository` (`fleet.go:380`) |
| `internal/gateway/fleet/fleettest` | `E/fleet/fleethelpertest` (D-F1-10) | 1 | 177 | 13 | 0 | doble `SlowRepository` |
| `internal/diagnostics` | `E/diagnostics` | 2 | 353 | 23 | 2 · 9 (3) | `Store`, `BundleReceiver` · `postgres.go` · `MemoryStore` (`diagnostics.go:107`) |
| `internal/inferstats` | `E/inferstats` | 1 | 233 | 7 | 1 · 7 (0) | — (memoria pura) |
| `internal/receipts` | `E/receipts` | 4 | 305 | 17 | 2 · 4 (1) | `Store` · `postgres.go` · `memory.go` |
| `internal/ingest` | `E/ingest` | 2 (+1 ✚) | 173 | 9 | 3 · 4 (2) | **sin puerto** (D-F3-3) · `postgres.go` · `MemoryDeduper` (`dedupe.go:25`) |
| `internal/filtercfg` | `E/filtercfg` | 1 | 180 | 10 | 2 · 11 (3) | `Source`, `ConfigPusher` |
| **Total** | | **38** (+✚) | **9.164** | **286** | **60 · 258 (40)** | |

Comandos: los de F2 (`ls`/`wc -l`/recorrido `go/ast`/`grep -c '^func Test'`; BD = alcanza
`WAPP_TEST_DB_DSN`). Total: `cat $(for d in …; do ls $d/*.go | grep -v _test.go; done) | wc -l` → 9.164.

**Ficheros más grandes**: `grpc/connect.go` 1.143 · `fleet/repository_postgres.go` 921 ·
`fleet/fleet.go` 744 · `grpc/inference.go` 697 · `grpc/send.go` 578 · `grpc/worklane.go` 428 ·
`grpc/auth.go` 378 · `grpc/server.go` 364. `grpc` sola es la mitad del módulo: se verdea en tres
tandas (T3.21–T3.23).

### 1.1 · Nivel provisional por paquete (`05` E-12)

**Provisional, sin medir: la fija el inventario E-12 (T3.1), archivo a archivo, y la aprueba Jhoan.** Deducida de las
tablas de §1 y §5. El nº de consumidores por paquete está **sin medir** (solo se conoce el de `grpc.Server`, §4).

| Paquete nuevo | Estado en memoria | Concurrencia | BD | Nivel provisional |
|---|---|---|---|---|
| `E/session` | sí (registro de sesiones vivas) | sí (envíos serializados, `Send` acotado) | no | **complejo** |
| `E/inferstats` | sí (último parte por Edge) | sí | no | **complejo** (pequeño: 233 l) |
| `E/receipts` | no | no | sí (`postgres.go`) | **medio**; el adaptador Postgres, complejo |
| `E/ingest` | caché de poda | sin medir | sí (`postgres.go`) | **medio**; el adaptador Postgres, complejo; `deduper.go` ✚ (solo interfaz), simple |
| `E/diagnostics` | no | no | sí (`postgres.go`) | **medio**; el adaptador Postgres, complejo |
| `E/lease` 🔒 | no | sin medir | sí (y escribe en `public.tenants`) | **complejo** (kill-switch, ADR-0007) |
| `E/enroll` | no | sin medir | sí (consumo atómico del código) | **medio**; `store_postgres.go`, complejo; `doc.go` sin test |
| `E/fleet` | no | sin medir | sí (921 l de SQL, cifrado e índice ciego) | **complejo** |
| `E/fleet/fleethelpertest` | doble | — | no | **medio** (`slowrepo.go` tiene lógica) |
| `E/filtercfg` | no | no | no (lee por `Source`) | **medio** (reglas R-C1…R-C5) |
| `E/grpc` | sí (acks, inferencias, readiness) | sí (carril por sesión, fan-out) | vía puertos | **complejo** (ADR-0048) |
| `internal/arranque/bridge_gateway.go` | no | no | no | **simple** (adaptador, `05` §4.2) |

#### 1.1.a · Inventario E-12 de las hojas, fichero a fichero (medido el 2026-10-04 sobre `dev` @ `8896f13`; **aprobado por Jhoan** en F3-01)

Manda sobre la tabla provisional de arriba para estos 7 paquetes. Consumidores = paquetes de producción distintos, fuera
del propio, que usan un símbolo de ese fichero (los dos `arranque`, viejo y nuevo, cuentan 2). Líneas = `wc -l` del
fichero viejo; «→» = lo que queda al mudar el doble en memoria a `<paq>helpertest` (D-F3-1).

| Fichero nuevo (`E/…`) | L. viejo | Estado en memoria | Concurrencia | BD / tx | Cons. | Nivel |
|---|---:|---|---|---|---:|---|
| `session/registry.go` | 230 | `map` de sesiones vivas | `Mutex`, goroutine + timer + `select` | no | 5 | **complejo** (mutantes) |
| `inferstats/inferstats.go` | 223 | `map[Clave]Parte` | `RWMutex` | no | 3 | **complejo** (pequeño) |
| `receipts/receipts.go` | 59 | no | no | no | 0 | **simple** |
| `receipts/sink.go` | 81 | no | no | vía `Store` (N `Save` sin tx) | 2 | **medio** |
| `receipts/postgres.go` | 84 | no | no | 2 sentencias sueltas, `ON CONFLICT DO UPDATE` | 2 | **medio** (D-F3-7) |
| `receipts/receiptshelpertest` (suite + doble ← `memory.go`) | 81 | mapa + `seq` | `Mutex` | no | — | **medio** |
| `ingest/deduper.go` ✚ | — | no | no | no | — | **simple** (solo interfaz) |
| `ingest/postgres.go` | 125 | contador `atomic` de poda | `atomic.Uint64` | INSERT + DELETE de poda **sin tx**, reloj real | 2 | **complejo** |
| `ingest/ingesthelpertest` (suite + doble ← `dedupe.go`) | 48 | mapa | `Mutex` | no | — | **medio** |
| `diagnostics/diagnostics.go` | 203 → ~95 | no (el doble se va) | no | no | 2 | **simple** |
| `diagnostics/postgres.go` | 150 | no | no | 7 sentencias; `CreateRequest` y `GetBundle` son 2 sin tx; dos relojes para el TTL | 2 | **complejo** |
| `diagnostics/diagnosticshelpertest` | ~110 | 2 mapas | `Mutex`, reloj privado | no | — | **medio** |
| 🔒 `lease/lease.go` | 236 | no | no (delega) | vía repo: SELECT + SELECT + UPSERT **sin tx** | 3 | **complejo** (mutantes; kill-switch) |
| 🔒 `lease/repository.go` | 160 → ~60 | no (el doble se va) | no | no | 0 | **simple** (interfaz + `State`; lleva el contrato de `Upsert`) |
| 🔒 `lease/repository_postgres.go` | 123 | no | no | 6 sentencias sueltas; escribe `public.tenants.revoked_at` (D-9) | 2 | **complejo** (SQL literal, driver falso) |
| `lease/signingkey.go` | 92 | no | no | no (lee disco) | 2 | **medio** |
| `lease/leasehelpertest` | ~100 | 2 mapas | `Mutex` | no | — | **medio** |
| `enroll/ca.go` | 254 | no | no | no; 3 `time.Now` sin reloj inyectable | 2 | **medio** |
| `enroll/doc.go` | 18 | — | — | — | 0 | sin test |
| `enroll/edgecert.go` | 87 → ~50 | no (el doble se va) | no | 1 INSERT | 2 | **medio** (D-F3-7) |
| `enroll/server.go` | 103 | no | no | no | 2 | **medio** |
| `enroll/service.go` | 57 | no | no | 2 escrituras **sin tx** (consume, luego certificado) | 2 | **medio** |
| `enroll/store.go` | 89 → ~40 | no (el doble se va) | no | no | 0 | **simple** |
| `enroll/store_postgres.go` | 65 | no | no | `UPDATE … RETURNING` atómico (un solo uso) | 2 | **complejo** |
| `enroll/enrollhelpertest` (2 suites + 2 dobles) | ~90 | mapa / slice | `Mutex` | no | — | **medio** |

- **Adaptadores `bridge_<x>.go` en F3-01: ninguno** nace ni muere. Hoy viven `bridge_contact.go` (F1) y `bridge_iam.go` (F2).
- **Puertos de entrada sin suite (`05` E-3): ninguno.** Los 7 paquetes solo declaran interfaces de repositorio/salida; sus
  caras de entrada son tipos concretos. Nada que añadir a `candados.InboundPortDirsWithoutSuite`.
- **`ingest/dedupe.go` y `receipts/memory.go` no nacen** (hallazgo 1 del README): su único contenido era el doble.
- `fleet`, `filtercfg` y `grpc` se inventarían fichero a fichero al abrir F3-02 y F3-03.

#### 1.1.b · Inventario E-12 de `fleet` y `filtercfg`, fichero a fichero (medido el 2026-10-04 sobre `dev` @ `809345b`; **aprobado por Jhoan** en F3-02)

Manda sobre la tabla provisional de §1.1 para estos paquetes. Mismas columnas y misma regla de conteo que §1.1.a.

| Fichero nuevo (`E/…`) | L. viejo | Estado en memoria | Concurrencia | BD / tx | Cons. | Nivel |
|---|---:|---|---|---|---:|---|
| `fleet/fleet.go` (modelo, `Repository`, validadores, 2 centinelas, `DeviceLimit`, `HealthSnapshot`) | 744 → ~390 | no (el doble se va) | no | no | 6 | **medio** (+ corpus de equivalencia de `self_pn`) |
| `fleet/repository_postgres.go` (constructor, `Logger`/`Option`, `Mark*`, `SetState`, `Get`, `List`, escaneo) | 921 → ~350 | no | no | 6 sentencias sueltas, 1 `ON CONFLICT`; sin tx | 2 | **medio** (D-F3-7) |
| `fleet/repository_postgres_selfpn.go` (sobre, índice ciego, `SetSelfPn`, `CountLiveBySelfPn`, tally) | ~255 | no | no | 2 sentencias; guarda de re-cifrado en el `WHERE` | 1 | **complejo** (mutantes) |
| `fleet/repository_postgres_greeting.go` (`PendingGreeting`, `MarkGreeted`) | ~145 | no | no | 2 sentencias; CAS `greeted_at IS NULL` | 1 | **complejo** (mutantes) |
| `fleet/repository_postgres_profile.go` (`SetProfile`, `ProfilesByTenant`) | ~125 | no | no | 2 sentencias sueltas | 3 | **medio** (D-F3-7) |
| `fleet/repository_postgres_health.go` (`SaveHealth`, `nullText`, `nullInt64`) | ~90 | no | no | 1 sentencia, 19 parámetros | 1 | **medio** (D-F3-7) |
| `fleet/fleethelpertest` suite (`contrato.go` + `<tema>_contrato.go`) | — | — | — | no | — | **medio** |
| `fleet/fleethelpertest/memoria.go` (← `MemoryRepository`, `fleet.go:378-732`) | ~345 | 2 mapas + reloj de perfil | `Mutex` | no | — | **medio** (test propio) |
| `fleet/fleethelpertest/slowrepo.go` | 177 | `atomic.Int64` | espera cancelable | no | — | **medio** (test propio) |
| `filtercfg/filtercfg.go` | 180 | no | no | no (lee por `Source`) | 2 | **medio** (R-C1…R-C5) |

- **Medido en `repository_postgres.go`**: 442 líneas de código y 444 de comentario; 11 métodos, **todos de una sentencia,
  sin `BeginTx`, sin cerrojo advisory, sin `FOR UPDATE`**; `database/sql` puro. Por la letra de D-F3-7 sería medio entero;
  sube a complejo lo que lleva cifrado, índice ciego y compare-and-set (decisión de Jhoan: **mixto por trozo**). No hay
  carrera que forzar dentro del adaptador (D-F2-12 no aplica): la única ventana —el saludo— la cierra el CAS de `MarkGreeted`.
- **E-13**: al irse el doble, `fleet.go` queda bajo 500 y no se parte. `repository_postgres.go` nace en **5 trozos por
  tema**, solo moviendo declaraciones, cada uno con su gemelo `_test.go`.
- **Adaptadores `bridge_<x>.go` en F3-02: ninguno. Puertos de entrada sin suite: ninguno.**
- **E-11**: los exportados de `fleet`, `fleettest` y `filtercfg` ya estaban en inglés. Única correspondencia:
  `fleet.MemoryRepository` / `NewMemoryRepository` → `fleethelpertest.Memoria` / `NewMemoria` (D-F3-1).
- **Valor cero (D-F2-10), dicho y no cambiado**: `Profile ""` → `passive` (`defaultProfile`; el cero es el caso seguro, y
  solo convierte el vacío: un desconocido pasa intacto) · `State ""` cuenta como **vivo** en `CountLiveBySelfPn` ·
  `WhatsappState ""` (aún sin salud) → `Degraded() == false`: **aquí el cero sí es «sano»** · `*filtercfg.Pusher` nil o sin
  gateway → `nil`, y una sesión ausente del mapa el Edge la asume `active` (fail-open del contrato externo).
- **Tipos nominales** (como el hallazgo 11): `fleet.Profile` y `fleet.TenantProfiles` nuevos no encajan en los
  consumidores viejos (`flowadmin.ProfilePusher`, el `Source` viejo). En F3-02 no se cablea nada: lo resuelve F3-04 (T3.28).
- **Decidido por Jhoan al aprobarlo**: driver falso = variante **local** en `fleet` (`repository_postgres_fakedb_test.go`;
  7.º `*fakedb*`) · `fleethelpertest.Memoria` **porta tal cual** al doble viejo (crea la fila en `MarkOffline` /
  `MarkLoggedOut` de una sesión desconocida; Postgres no): la suite afirma solo lo común.

**Adaptadores de arranque**: nace **1** (`bridge_gateway.go`, muere en F4) y muere **1** (`bridge_iam.go`, nacido en F2).

## 2 · Grafo interno y orden

`GOWORK=off go list -f '{{.ImportPath}} {{.Imports}}' ./internal/gateway/... ./internal/{diagnostics,inferstats,receipts,ingest,filtercfg}`:

```
session, lease, enroll, diagnostics, inferstats, receipts, ingest   → nada interno   ← hojas
fleet        → flujos/contact (= nucleo/contact desde F1), platform/crypto
fleettest    → fleet
filtercfg    → fleet
grpc         → session, lease, fleet, diagnostics, inferstats, flujos/contact (nucleo), iam/{domain,ports/in} (acceso)
```

Orden de contratos y de verde: hojas (`session`, `inferstats`, `receipts`, `ingest`, `diagnostics`,
`lease`, `enroll`) → `fleet` (+`fleethelpertest`) → `filtercfg` → `grpc`.

## 3 · Imports hacia fuera

| Destino | Quién | Clase |
|---|---|---|
| `internal/nucleo/contact` | `fleet`, `grpc` | reconstruido en F1 — permitido |
| `internal/modulos/acceso/iam/{domain,ports/in}` (`in.Authenticator`, `in.Auditor`, 4 centinelas, `AuthResult`) | `grpc/auth.go`, `grpc/server.go` | reconstruido en F2 — permitido (`edge → acceso` en la lista blanca) |
| `internal/platform/crypto` (`FieldCipher`, `KeyProvider`) | `fleet/repository_postgres.go:73` | `platform` |
| `wapp-cloudlink v0.17.0` (`gen/wapp/cloudlink/v1`, `transport`, `lease`) | `grpc`, `lease`, `session`, `enroll`, `receipts` | 🔒 externa, **versión fija** |
| `wapp-shared/{envelope,logger}`, `grpc`, `protobuf` | `grpc`, `enroll` | externas |

**Puentes (imports) al código viejo: cero** (contradicción 2 del README). Lista blanca en
`fronteras_test.go`: `edge → {platform, nucleo, acceso}`. Comprobación:
`go list -deps ./internal/modulos/edge/... | grep -E 'internal/(gateway|flujos|iam|entitlements|intake|llmvia)'` → vacío.

## 4 · El gateway nuevo y el código viejo que lo sigue usando (F3 → F4…F8)

Consumidores del `*gatewaygrpc.Server` hoy (`grep -n 'c\.gw' internal/bootstrap/arranque/*.go`):

| Consumidor viejo | Por dónde | Tipos | En el binario nuevo tras F3 |
|---|---|---|---|
| `flujos/runtime` (F8) | `flowruntime.New(…, c.gw, …)` (`fase7_flujos.go:228`): `Sender` = `SendText`, `SendMedia` | stdlib + `*cloudlinkv1.Ack` | **estructural**: gw nuevo tal cual |
| hooks `c.gw.OnIncoming/OnHeartbeat/OnWarmup/OnEdgeReady` (`fase7_flujos.go:127-222`) | campos `func` | stdlib + `cloudlinkv1` | **estructural** |
| `intakes.Notifier` (F6) | `intakes.NewNotifier(c.gw, …)` (`fase6_solicitudes.go:42`), `MessageSender.SendText` | ídem; lee `CommandID()` por duck-typing (`intakes/notifier.go:141`) | **estructural** |
| `llmvia.Selector` + `llmvia/local.Provider` (F4) | `llmvia.WithFrame(c.gw)` (`fase5_captacion.go:92`): `local.Frame.Infer(ctx, tenantID, gatewaygrpc.InferRequest)` (`llmvia/local/local.go:270-272`); capacidad **opcional** `PlazaDe` por aserción de tipo (`llmvia.go:163`, `:411-419`) | `InferRequest` **viejo** (nominal); `*InferError` se lee por `Motivo()` duck-typed (`llmvia/notify.go:57`) | **`internal/arranque/bridge_gateway.go`** (nace F3, muere F4): convierte `InferRequest` viejo → nuevo campo a campo y expone `PlazaDe`. `Clase*` son `string` sin tipo: mismos valores |
| `platform/httpapi` (J12–J15: `RevokeLease`, `RevokeTenant`, `RestoreTenant`, `SendText`) | interfaces de `admin.go` | stdlib; errores por `errors.Is(ErrSessionOffline)` (tras F0, el de `platform`) y duck-typing `StreamCaido()`, `CommandID()` (`admin.go:343-352`) | **estructural** |
| `publicapi` viejo (D1, D5, E2) | `Deps.Sender/DiagnosticsRequester/ConfigPush` | `ConfigPusher` estructural | `Sender`/`DiagnosticsRequester` **`nil`** (rutas mudadas, FX TX.11); `ConfigPush` = **gw nuevo** hasta F7 (E2 sigue en la vieja, D-FX-1/D-F7-4) |
| `flujos/admin` + `publicapi/flows.go` (I4, J19 hasta F8) | comparan `session.ErrSessionOffline` (`flujos/admin/handlers.go:326`, `publicapi/flows.go:235`) | centinela | identidad compartida vía `platform` (D-F3-2, recomendación; alternativa: puente (import) de FX D-FX-3) |
| `ConfigProvider` del arranque (`auth.go:457-638`: jwks, intents, filters) | `WithConfigProvider` | `[]gatewaygrpc.ConfigPayload` | el arranque (que puede importar viejo y nuevo) devuelve el tipo **nuevo** |

Otros adaptadores del arranque cuyo tipo cambia en F3: `receipts.NewSink(…, c.mtx.Receipt)`
(`fase3_almacenes.go:67`), `diagnostics.NewPostgres` (`:85`), `fleet.NewPostgresRepository` (`:98`),
`inferstats.New()` + `mtx.RegisterInferenceStats(c.inferStats.Agrega)` (`fase4_gateway.go:36-41`,
con `Agregado` = alias de `platform/metrics` desde F0), `buildLeaseManager` (`lease.go:18-51`),
`buildEnrollServer`/`loadPKI` (`pki.go:31-81`), `enroll.NewPostgresCodeStore` para
`platformadmin.CodeIssuer` (`fase8_transporte.go:104`, estructural), `ingest.NewPostgresDeduper`
para `flowruntime.WithIngestDeduper` (`fase7_flujos.go:330`; `IngestDeduper.Seen(ctx, sessionID, waMessageID)`
es estructural, `flujos/runtime/runtime.go:116`), `filtercfg.NewPusher(c.fleetRepo, c.gw)` (`fase8_transporte.go:85`).

## 5 · Estado en memoria, goroutines, métricas y relojes

| Estado | Dónde | Por qué obliga a **una** instancia |
|---|---|---|
| `session.Registry`: sesiones vivas, última-gana, `map[session_id]` **sin tenant** | `session/registry.go:74-125` | el Edge está conectado a **un** registro |
| `acks` (envíos en vuelo), `infers` (inferencias en vuelo), `edgeSessions` y `edgeReadiness` por (tenant, edge) | `grpc/server.go:~255-270` | un Ack/InferenceResult solo encuentra su espera en la instancia que la creó |
| Carril por sesión (cola con tope, `sync.Cond`) | `grpc/worklane.go` | serializa por sesión |
| `inferstats.Store` (último parte por (tenant, edge), no olvida al desconectar) | `inferstats/inferstats.go:111-117` | lo escribe el gw y lo lee `/metrics` |
| Caché de poda del deduper (estado de barrido) | `ingest/postgres.go:29-63` | inocuo, pero una instancia |

- **Goroutines** (producción): `grpc/worklane.go:248` (un worker por sesión) y `:391` (drenaje),
  `grpc/send.go:90,153` (fan-out de revocación a cada sesión viva), `grpc/config_push.go:52` (fan-out
  de config), `session/registry.go:202` (el `Send` acotado). Todas **mueren con su stream o su
  llamada**; `edge` no lanza goroutines de fondo (las cinco de `fase9_fondo.go` no son de `edge`).
  Los servidores gRPC los sirve el arranque (`servir.go:56-59`).
- **Métricas**: `edge` no registra ninguna. `wapp_receipts_total` la incrementa `receipts.Sink` por
  callback (`func(status string)`, `platform/metrics/metrics.go:85,232`); los 5 descriptores
  `wapp_edge_inference_*` los publica `platform/metrics/inferstats.go:69` leyendo `Store.Agrega`.
- **Relojes reales** en producción: `grpc` 0 `time.Now()` (usa temporizadores y `context`),
  `enroll` 3, `diagnostics` 1, `receipts` 1, `ingest` 1. Tests nuevos con reloj inyectado o con
  canales; la lógica de plazos se prueba con `ctx` cancelados, no con esperas.

## 6 · Cableado y conmutación

Fases del arranque viejo que tocan `edge`: 1 `infraestructura` (PKI, lease, enrolamiento), 3
`almacenes` (receipts, diagnostics, fleet), 4 `gateway` (inferstats + `gatewaygrpc.New` con 12
opciones, `fase4_gateway.go:45-84`), 7 `flujos` (hooks, deduper), 8 `transporte` (listeners `:8101`
con `mtls.ServerCreds` de `wapp-cloudlink` —TLS 1.3 y `RequireAndVerifyClientCert`, `pki.go:21-24`—,
`:8102` con `EnrollServerCreds` solo servidor, rutas). `conmutar(edge)` cambia **solo la copia** de
`internal/arranque`: imports a `internal/modulos/edge/...`, un `grpc.New(session.NewRegistry(session.WithSendTimeout(cfg.GRPCPushTimeout)), …)`
con las mismas 12 opciones y los mismos valores (`WAPP_GRPC_PUSH_TIMEOUT` 10 s, `WAPP_GRPC_ACK_TIMEOUT`
8 s, `WAPP_GATEWAY_WORK_QUEUE` 64, `WAPP_GATEWAY_WORK_TIMEOUT` 5 s), `WithAuthenticator`/`WithAuthAuditor`
con los de `acceso` **nuevos** (se borra `bridge_iam.go` y `acceso` entra en `Conmutados`), el adaptador
`bridge_gateway.go` para el selector (`edge` entra en `Conmutados` cuando muera, en F4).
**Cómo se prueba que usa lo nuevo**: `go list -deps ./cmd/server-modular | grep internal/gateway/`
→ solo `internal/gateway/grpc` (lo arrastra `llmvia` viejo hasta F4); el test de cableado de `bridge_gateway.go`
(ninguna fase importa el gw viejo fuera del adaptador, grep por ruta de import); aserciones de identidad de FX
TX.11; huella igual (2 rpc, rutas, métricas, goroutines).

## 7 · Rutas (autoridad: FX `mapa-de-rutas.md` §2.4, §2.5, §3)

| Listener | Filas | Nº | En F3 |
|---|---|---:|---|
| `:8103` | D1 `POST /api/v1/messages` · D2 `GET /api/v1/sessions` · D3 `POST …/sessions/{id}/profile` · D4 `…/status` · D5 `…/diagnostics` · D6 `GET /api/v1/diagnostics/{command_id}` (E1–E2 `GET/PUT /api/v1/intents` **no**: se quedan en la vieja con el gw nuevo y se mudan en F7, D-FX-1/D-F7-4) | **6** | a `apipublica` (FX TX.8–TX.11); D3/D4 desde `apipublica/sessionadmin.go` (D-FX-2) |
| `:8100` | J12 `/admin/leases/revoke` · J13 `POST /admin/tenants/revoke` · J14 `…/restore` · J15 `/admin/messages/send` · J16–J17 `POST /admin/sessions/{id}/{profile,status}` | **6** | J12–J15 reciben el gw **nuevo**; J16–J17 los constructores exportados de `sessionadmin.go`, **en el mismo commit que D3–D4** |

## 8 · Lo que no cambia hacia fuera

Los 2 rpc y el proto (`wapp-cloudlink v0.17.0`); mTLS estricto en `:8101` y TLS de servidor en
`:8102`; el literal `AVISO_SESION_PASIVA_V1` y su `.md`; los 10 tipos de `EdgeToCloud` y el reparto
inline/carril; los motivos de degradación (vocabulario = `degradation.Reason`); `DefaultTTL` 15 min,
`initialCounter` 1, revocación pegajosa y la escritura de `public.tenants.revoked_at` (deuda D-9,
D-F3-4); el `kind` `"filters"`; las tablas `leases`, `edge_certs`, `activation_codes`,
`fleet_sessions`, `diagnostics_*`, `message_receipts`, `ingest_dedupe` y su SQL; las variables
`WAPP_LEASE_*`, `WAPP_PKI_*`, `WAPP_GRPC_*`, `WAPP_GATEWAY_*`, `WAPP_DIAGNOSTICS_BUNDLE_TTL`,
`WAPP_CLOUD_ENC_PRIVKEY_B64`. `cmd/server` no cambia un byte.
