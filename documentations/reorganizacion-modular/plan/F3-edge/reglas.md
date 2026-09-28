# F3 · Reglas — lo que no se toca, las trampas y la definición de hecho

> Comunes en [`00-marco/`](../00-marco/README.md). Aquí lo de `edge`, con `fichero:línea` sobre
> `dev` @ `1b18932`.

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
| T-1 | **El aforo por Edge se apaga en silencio** si el adaptador del selector no implementa `PlazaDe`: la aserción de tipo da `false` y solo sale un `Warn` | `internal/llmvia/llmvia.go:163,411-419` | `puente_gateway.go` implementa `Infer` **y** `PlazaDe`; test con aserción de tipo (R3.6.b) |
| T-2 | **`InferRequest` es nominal**: `local.Frame` pide el tipo **viejo** | `internal/llmvia/local/local.go:270-272` | `puente_gateway.go` convierte campo a campo; `InferError` pasa tal cual (duck-typing `Motivo()`, `llmvia/notify.go:57-70`) |
| T-3 | **Centinelas por identidad**: I4/J19 (hasta F8) comparan `session.ErrSessionOffline` viejo | `publicapi/flows.go:235`, `flujos/admin/handlers.go:326` | `ErrSessionOffline` nuevo = el de `platform` (D-F3-2) o el puente D-FX-3; test `errors.Is(nuevo, viejo)` |
| T-4 | **Dos `grpc.Server` en el proceso** = Edge conectado a uno y envíos buscándolo en otro; acks e inferencias sin respuesta | `grpc/server.go:~255-270` | una instancia; aserciones de identidad de FX TX.11 |
| T-5 | **`__wapp_control__` como clave** del registro filtró tokens y configs entre inquilinos (HS-14/HS-15) | `grpc/connect.go:109`, `:710`; `readiness.go:190`; ADR-0048 | tests de R3.4.a–c; `pushConfigsInBand`; **sin** fallback a `registry.Push` |
| T-6 | **«Filtrar por `READY` obligatorio»** está **refutado**: apaga la inferencia de la flota que no reporta el campo (la mutación puso 9 tests en rojo) | `grpc/readiness.go:132-160`; ADR-0048 alternativas | `UNSPECIFIED` elegible; excluir solo `DOWN` |
| T-7 | **`Send` de gRPC no se desbloquea al cancelar el ctx**: enviar a pelo cuelga la goroutine del carril | ADR-0048 alternativas | siempre `session.SendAcotado` (dos relojes) |
| T-8 | **La revocación es pegajosa por dos guardas**: `wasRevoked` antes de emitir **y** `Upsert` que nunca escribe `revoked` | `lease/lease.go:98-156`; `repository.go` contrato de `Upsert` | suite `leasetest` con el caso «Upsert no resucita» en memoria **y** en Postgres |
| T-9 | **`lease` escribe `public.tenants.revoked_at`** (tabla de `platform`) sin API interna | `lease/repository_postgres.go:106,117`; deuda D-9 | copiar el SQL literal (D-F3-4) |
| T-10 | **Sin clave de lease configurada**, la plataforma genera una **efímera** e invalida a todos los Edge al reiniciar | `bootstrap/arranque/lease.go:47`; `contratos.md` §5 | mismo aviso en el log del arranque nuevo; nunca «arreglarlo» con una clave por defecto |
| T-11 | **El literal se lee por ruta relativa**: con un nivel más (`modulos/edge/grpc`) son **cuatro** `../` | `greeting_internal_test.go:327` | `../../../../documentations/literal-aviso-sesion-pasiva.md` y el test falla si no lo encuentra |
| T-12 | **Tres teléfonos de un Edge triplican** la inferencia si la clave es la sesión | `inferstats/inferstats.go`, `inferstats_test.go:23` | clave (tenant, **edge**) |
| T-13 | **`OnWarmup`/`OnEdgeReady` se llaman inline en el `Recv`**: tienen que volver en el acto | `grpc/server.go` (comentarios 🔴 de los hooks) | contrato lo dice; test con hook que bloquea detecta el cuelgue por `ctx` |
| T-14 | **D3/J16 son el mismo handler por dos vías**: encender una sola deja la otra muda sin ningún rojo | `bootstrap/arranque/fase8_transporte.go:77-85,133-138` | las dos en el **mismo** commit (FX TX.11) |
| T-15 | **`InferError.Error()` lleva el prefijo `gatewaygrpc:`** aunque el paquete nuevo se llame `grpc` | `grpc/inference.go:193` | texto literal (diseño §5) |
| T-16 | **Tests viejos con `time.Sleep`** como sincronización en carril/acks | `worklane_internal_test.go`, `send_cancel_internal_test.go` (sin medir cuántos) | en lo nuevo, canales y `ctx`; ni `Sleep` ni reloj real |
| T-17 | **`unused` en rojo** (T-1 de F1) | — | solo exportados; `var _ viejo.X = (*y)(nil)` en los puentes |

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
3. `make cobertura-ficheros` ≥ 80 % salvo `*postgres*.go`.
4. Las 7 suites verdes contra sus dobles; `go vet -tags integracion ./...` rc=0.
5. Literal verde contra golden **y** `.md`; la pareja ADR-0048 verde.
6. Un solo `grpc.Server`; `puente_gateway.go` verde; `puente_iam.go` borrado; huella igual;
   `go.mod` sin cambio en `wapp-cloudlink`; `cmd/server` intacto.
7. e2e local con mTLS real (T3.29); el proceso de enrolamiento corrido o anotado «no corrido» (T3.30).
8. `ESTADO.md`, README de F3 y traspaso con `CERRADO`.
