# F3 · Reglas — lo que no se toca, las trampas y la definición de hecho

> Comunes en [`00-marco/`](../00-marco/README.md). Aquí lo de `edge`, con `fichero:línea` sobre
> `dev` @ `1b18932`.

## 0 · Nivel de ceremonia (`05` E-12) y lo que no se relaja

El nivel de cada archivo lo fija el **inventario E-12** (T3.1), que aprueba Jhoan; la clasificación provisional por
paquete está en [`arquitectura.md`](arquitectura.md) §1.1. Si un archivo sale peor de lo previsto, sube de nivel.

- **Simple**: contrato, test y lógica en **una pasada**; varios archivos por sesión. Aquí entran los adaptadores
  `bridge_<x>.go`.
- **Medio**: rojo y verde por archivo, **agrupados por paquete**; un test por promesa del contrato.
- **Complejo**: el esquema completo E-2…E-9, con **mutantes** donde haga falta.
- 🔴 **No se relaja en ningún nivel**: la equivalencia viejo ↔ nuevo, `make ci-local` rc=0 con **0 SKIP** y los
  procesos de F9.
- **Sin umbral de cobertura (P2)**: un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9.
  `make cobertura-ficheros` es un informe: la tabla va al PR; no bloquea.
- **Auxiliares no exportados (P6, `05` E-4)**: nacen con el verde, y su test también, **solo si llevan regla de
  negocio o ramas no triviales**. No se testea fontanería ni `if err != nil`; el resto lo cubre F9.
- **Puertos con BD (P4)**: los 7 de `edge` tienen su suite `Contrato(t, func(t) Montaje)` corrida **en memoria y en
  Postgres** con el arnés de F9-A: es lo que garantiza que las dos implementaciones se comportan igual. La marca de
  estado de la suite vigila **todas** las columnas que la operación puede tocar, no una sola (hallazgo 35 de F1).
- **Corpus de equivalencia** viejo ↔ nuevo (índice ciego de `fleet`, `self_pn`): con casos **adversarios**
  (separadores repetidos como `a@@b`, dígitos no ASCII, espacios Unicode), no solo casos felices (hallazgo 40 de F1).
- **Adaptadores (`05` §4.2)**: `bridge_gateway.go` es nivel simple y lleva **test de cableado obligatorio y completo**
  (hallazgo 39 de F1). `un_fichero_un_test` y el informe de cobertura incluyen los `bridge_*.go`.

## 1 · Lo que no se toca

- El código viejo (`internal/gateway/**`, `internal/{diagnostics,inferstats,receipts,ingest,filtercfg}`,
  `internal/bootstrap/**`) — E-1, sin excepciones en F3.
- 🔒 `documentations/literal-aviso-sesion-pasiva.md`: contrato congelado; ni se edita ni se duplica.
- 🔒 `go.mod`: `github.com/EduGoGroup/wapp-cloudlink v0.17.0` no cambia (el proto vive allí).
- 🔒 La doble llave (ADR-0007, fuera de este repo: *«la DEK la custodia el cliente y la nube nunca la
  ve; el lease lo emite y revoca el servidor»*): ni un campo, ni una columna, ni un log con la DEK del
  almacén de `whatsmeow`. 🔴 **Homónimo**: la «DEK» que aparece en este repo es la del envelope de PII
  (`internal/intakes/buyerdata.go:11`), **no** esta.
- Los listeners y su seguridad: `:8101` mTLS estricto, `:8102` TLS solo servidor (el Edge enrola
  antes de tener certificado); keepalive `30s/10s/15s` (`bootstrap/arranque/fase8_transporte.go:58`).

## 2 · Trampas conocidas

| # | Trampa | Dónde | Qué hacer |
|---|---|---|---|
| T-1 | **El aforo por Edge se apaga en silencio** si el adaptador del selector no implementa `PlazaDe`: la aserción de tipo da `false` y solo sale un `Warn` | `internal/llmvia/llmvia.go:163,411-419` | `bridge_gateway.go` implementa `Infer` **y** `PlazaDe`; test con aserción de tipo (R3.6.b) |
| T-2 | **`InferRequest` es nominal**: `local.Frame` pide el tipo **viejo** | `internal/llmvia/local/local.go:270-272` | `bridge_gateway.go` convierte campo a campo; `InferError` pasa tal cual (duck-typing `Motivo()`, `llmvia/notify.go:57-70`) |
| T-3 | **Centinelas por identidad**: I4/J19 (hasta F8) comparan `session.ErrSessionOffline` viejo | `publicapi/flows.go:235`, `flujos/admin/handlers.go:326` | `ErrSessionOffline` nuevo = el de `platform` (D-F3-2) o el puente (import) D-FX-3; test `errors.Is(nuevo, viejo)`. ✎ 2026-10-07 (D-F3-14): ese test (`internal/arranque/session_identity_test.go`) se borró al cierre de F45-02; la identidad la cubren `TestErrSessionOfflineIsThePlatformSentinel` (`internal/modulos/edge/session`), `TestSendMessageHandler_Offline` (`internal/platform/httpapi`) y `TestP1_EdgeFaceOverTheWire/offline_session_is_502` (§4.7) |
| T-4 | **Dos `grpc.Server` en el proceso** = Edge conectado a uno y envíos buscándolo en otro; acks e inferencias sin respuesta | `grpc/server.go:~255-270` | una instancia; aserciones de identidad de FX TX.11 |
| T-5 | **`__wapp_control__` como clave** del registro filtró tokens y configs entre inquilinos (HS-14/HS-15) | `grpc/connect.go:109`, `:710`; `readiness.go:190`; ADR-0048 | tests de R3.4.a–c; `pushConfigsInBand`; **sin** fallback a `registry.Push` |
| T-6 | **«Filtrar por `READY` obligatorio»** está **refutado**: apaga la inferencia de la flota que no reporta el campo (la mutación puso 9 tests en rojo) | `grpc/readiness.go:132-160`; ADR-0048 alternativas | `UNSPECIFIED` elegible; excluir solo `DOWN` |
| T-7 | **`Send` de gRPC no se desbloquea al cancelar el ctx**: enviar a pelo cuelga la goroutine del carril | ADR-0048 alternativas | siempre `session.SendAcotado` (dos relojes) |
| T-8 | **La revocación es pegajosa por dos guardas**: `wasRevoked` antes de emitir **y** `Upsert` que nunca escribe `revoked` | `lease/lease.go:98-156`; `repository.go` contrato de `Upsert` | suite `leasehelpertest` con el caso «Upsert no resucita» en memoria **y** en Postgres |
| T-9 | **`lease` escribe `public.tenants.revoked_at`** (tabla de `platform`) sin API interna | `lease/repository_postgres.go:106,117`; deuda D-9 | copiar el SQL literal (D-F3-4) |
| T-10 | **Sin clave de lease configurada**, la plataforma genera una **efímera** e invalida a todos los Edge al reiniciar | `bootstrap/arranque/lease.go:47`; `contratos.md` §5 | mismo aviso en el log del arranque nuevo; nunca «arreglarlo» con una clave por defecto |
| T-11 | **El literal se lee por ruta relativa**: con un nivel más (`modulos/edge/grpc`) son **cuatro** `../` | `greeting_internal_test.go:327` | `../../../../documentations/literal-aviso-sesion-pasiva.md` y el test falla si no lo encuentra |
| T-12 | **Tres teléfonos de un Edge triplican** la inferencia si la clave es la sesión | `inferstats/inferstats.go`, `inferstats_test.go:23` | clave (tenant, **edge**) |
| T-13 | **`OnWarmup`/`OnEdgeReady` se llaman inline en el `Recv`**: tienen que volver en el acto | `grpc/server.go` (comentarios 🔴 de los hooks) | contrato lo dice; test con hook que bloquea detecta el cuelgue por `ctx` |
| T-14 | **D3/J16 son el mismo handler por dos vías**: encender una sola deja la otra muda sin ningún rojo | `bootstrap/arranque/fase8_transporte.go:77-85,133-138` | las dos en el **mismo** commit (FX TX.11) |
| T-15 | **`InferError.Error()` lleva el prefijo `gatewaygrpc:`** aunque el paquete nuevo se llame `grpc` | `grpc/inference.go:193` | texto literal (diseño §5) |
| T-16 | **Tests viejos con `time.Sleep`** como sincronización en carril/acks | `worklane_internal_test.go`, `send_cancel_internal_test.go` (sin medir cuántos) | en lo nuevo, canales y `ctx`; ni `Sleep` ni reloj real |
| T-17 | **`unused` en rojo** (T-1 de F1) | — | solo exportados; `var _ viejo.X = (*y)(nil)` en los adaptadores `bridge_<x>.go`; los auxiliares no exportados nacen con el verde (§0, P6) |

## 3 · Prohibiciones

- 🚫 `t.Skip`; 🚫 Postgres en tests de fichero; 🚫 red real (gRPC por `bufconn`; identity no aplica).
- 🚫 Un segundo `grpc.New(…)` o `session.NewRegistry(…)` en `internal/arranque`.
- 🚫 Tocar `lease` más allá de su ruta y su forma de test (`05` §6: «sin cambiar comportamiento»).
- 🚫 Importar `internal/gateway/**`, `internal/flujos/**` o `internal/llmvia/**` desde `edge`: no hace
  falta ningún puente (arquitectura §3).
- 🚫 Portar los 7 tests de carga/pool (D-F3-5).

## 4 · Definición de hecho de F3

1. `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/edge | wc -l` → **0**.
2. `make ci-local` → `GATE_RC=0` leído del log; `go test -v ./internal/modulos/edge/... | grep -c -- '--- SKIP'` → 0.
3. Un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9. La tabla de
   `make cobertura-ficheros` va al PR como informe; no bloquea.
4. Las 7 suites verdes contra sus dobles **y** contra Postgres con el arnés (P4); `go vet -tags integracion ./...` rc=0.
5. Literal verde contra golden **y** `.md`; la pareja ADR-0048 verde.
6. Un solo `grpc.Server`; `bridge_gateway.go` verde con su test de cableado completo; `bridge_iam.go` borrado;
   huella igual; `go.mod` sin cambio en `wapp-cloudlink`; `cmd/server` intacto.
7. **`Conmutados`** (`internal/modulos/fronteras_test.go`): un módulo entra cuando **muere su último adaptador**, no al
   conmutar. En F3 **nace** `bridge_gateway.go` y **muere** `bridge_iam.go` (nacido en F2): **`acceso` entra** en
   `Conmutados` al cerrar F3; **`edge` no entra** hasta F4, cuando muere `bridge_gateway.go`. `FaseActual` sigue
   existiendo y no cambia.
   ✎ **2026-10-07 (F45-02)**: `bridge_gateway.go` murió en `98b24c5` (T4.24), pero **`edge` no entró**:
   `internal/arranque/session_identity_test.go:7` importa el `internal/gateway/session` viejo y la regla 3 lo pondría
   rojo. `Conmutados` sigue `{"acceso"}`. Cuándo entra `edge` depende de qué se haga con ese test: D-F3-14 en
   [`../DECISIONES.md`](../DECISIONES.md), pendiente de Jhoan.
   ✎ **2026-10-07 (cierre de F45-02, D-F3-14 cerrada por Jhoan)**: **`edge` entra**; `Conmutados` es
   `{"acceso","edge"}`. Se **borró** `internal/arranque/session_identity_test.go` (37 líneas, un test,
   `TestIdentidad_SessionOfflineIsOneSentinelAcrossBothTrees`, nacido en `dd4cbd2`: T3.27 = TX.10, D-F3-2). La nota
   anterior queda como historia.
   - **El invariante sigue vivo.** El arranque nuevo cablea handlers viejos (`internal/flujos/admin/handlers.go:326`,
     `internal/publicapi/flows.go:235`, `internal/platform/httpapi/admin.go:313`) con el gateway nuevo, y comparan el
     centinela `ErrSessionOffline`. Casan porque el nuevo (`internal/modulos/edge/session/registry.go:31`) y el viejo
     (`internal/gateway/session/registry.go:24`) son los dos alias de `httpapi.ErrSessionOffline`.
   - **El test era redundante** (mutaciones en una copia del árbol). Rompiendo el centinela **nuevo** dan rojo
     `TestErrSessionOfflineIsThePlatformSentinel` y `TestPushOfflineSession`
     (`internal/modulos/edge/session/registry_test.go:90-97`) y, contra el binario nuevo,
     `TestP1_EdgeFaceOverTheWire/offline_session_is_502` (HTTP 500 en las tres rutas). Rompiendo el **viejo** dan rojo
     `TestSendMessageHandler_Offline` y `TestSendMessageHandler_StreamCaido_NoPisaEl502DeOffline`
     (`internal/platform/httpapi`).
   - **Alternativas descartadas.** `Puentes` solo exime la regla 2, no la 3 (`internal/candados/fronteras.go:204-218`);
     moverlo junto al paquete viejo lo muerde la regla 5, y a `test/` esquivaría el candado por ubicación; reescribirlo
     sin lo viejo lo reduce a la aserción que ya existe; esperar a F8 deja `edge` varias fases fuera de la regla 3.
   - **Lo que se pierde.** La aserción directa `errors.Is(nuevo, viejo)`. La rotura del centinela **viejo** la detectan
     tests de comportamiento, no de identidad. P1 no corre en `make ci-local`.
   - **Lo que se gana.** La regla 3 vigila en `internal/arranque` todo lo viejo de `edge` (`gateway`, `diagnostics`,
     `inferstats`, `receipts`, `ingest`, `filtercfg`), no solo `internal/gateway`.
   - **Medido.** Con `edge` en `Conmutados` y el test presente, `TestFronteras` daba **una** violación (ese test).
     Borrado: `go test -count=1 -v ./internal/modulos/ ./internal/arranque/ ./internal/modulos/edge/session/
     ./internal/platform/httpapi/ ./internal/candados/` rc=0, 0 SKIP.
8. e2e local con mTLS real (T3.29); el proceso de enrolamiento corrido o anotado «no corrido» (T3.30).
9. `ESTADO.md` y README de F3 al día; traspaso con `CERRADO` si lo hubo.
