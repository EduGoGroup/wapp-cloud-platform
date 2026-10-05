# F3 · Diseño — contratos, suites, dobles y reglas que se llevan (E-8)

> `E` = `internal/modulos/edge` · `V` = paquete viejo. Cabecera `// Porta <V>/<f>.go @ <sha>`. En
> rojo **solo exportados** (T-1 de F1); los auxiliares no exportados nacen con el verde, y su test también, solo si
> llevan regla de negocio o ramas no triviales (P6, `05` E-4). Las reglas salen de los tests viejos (258 `Test*`) y de
> los comentarios-ADR, leídos el 2026-09-28. Nivel de ceremonia por paquete (provisional):
> [`arquitectura.md`](arquitectura.md) §1.1; lo fija el inventario E-12 (T3.1).

## 1 · Árbol nuevo (producción ↔ test)

```
E/session/      registry.go                                                         (+1)
E/inferstats/   inferstats.go                                                       (+1)
E/receipts/     receipts.go · sink.go · postgres.go · memory.go→receiptshelpertest (D-F3-1) (+3)
E/receipts/receiptshelpertest/      ContratoStore · Memoria
E/ingest/       dedupe.go · postgres.go · deduper.go ✚ (solo interfaz, D-F3-3)       (+2)
E/ingest/ingesthelpertest/          ContratoDeduper · Memoria (↦ MemoryDeduper)
E/diagnostics/  diagnostics.go · postgres.go                                         (+2)
E/diagnostics/diagnosticshelpertest/ ContratoStore · Memoria (↦ MemoryStore)
E/lease/ 🔒     lease.go · repository.go · repository_postgres.go · signingkey.go      (+4)
E/lease/leasehelpertest/            ContratoRepository · Memoria (↦ MemoryRepository)
E/enroll/       ca.go · doc.go · edgecert.go · server.go · service.go · store.go · store_postgres.go   (+6; doc.go sin test)
E/enroll/enrollhelpertest/          ContratoCodeStore · ContratoEdgeCertRepository · dobles
E/fleet/        fleet.go · repository_postgres.go                                    (+2)
E/fleet/fleethelpertest/            slowrepo.go (hoy, en fleettest) + ContratoRepository + Memoria (↦ MemoryRepository)
E/filtercfg/    filtercfg.go                                                         (+1)
E/grpc/         auth · config_push · connect · diagnostics · greeting · inference · plaza · readiness · receipt_sink · send · server · types · worklane   (+13)
internal/arranque/  bridge_gateway.go ✚ · bridge_gateway_test.go   (y se BORRA bridge_iam.go de F2)
```

## 2 · Suites de contrato (firma D-F1-1) y dobles

Los 7 puertos con BD: cada suite es `Contrato(t, func(t) Montaje)` y corre **en memoria y en Postgres** con el arnés de
F9-A (P4). La marca de estado vigila **todas** las columnas que la operación puede tocar (hallazgo 35 de F1).

| Suite | Casos mínimos (reglas de §4) | Doble | Postgres (arnés de F9-A) |
|---|---|---|---|
| `leasehelpertest.ContratoRepository` 🔒 | `Upsert` **nunca** escribe `revoked` ni resucita un revocado; `MarkRevoked` pegajoso; `Get` de un Edge nunca visto → `found=false`; `MarkTenantRevoked`/`TenantRevoked`/`RestoreTenant` independientes de las filas por Edge | `leasehelpertest.Memoria` | `lease.PostgresRepository` |
| `enrollhelpertest.ContratoCodeStore` | código de un solo uso: el segundo consumo falla (`ErrCodeUsed`/`ErrCodeNotFound`); consumo **atómico** | memoria | `PostgresCodeStore` |
| `enrollhelpertest.ContratoEdgeCertRepository` | guarda y recupera el registro del certificado | memoria | `PostgresEdgeCertRepository` |
| `fleethelpertest.ContratoRepository` | online→offline; offline de desconocida no es error; `SaveHealth` marca `degraded_since` al **entrar** y lo limpia al salir; perfil por defecto **pasivo** en los **tres** llamantes de `defaultProfile`; `SetProfile` sobrevive a la reconexión (activo y pasivo); perfil inválido → `ErrInvalidProfile` sin mutar; aislamiento por tenant; `MarkLoggedOut` ≠ offline; `SetState` solo `offline\|loggedout`; `CountLiveBySelfPn` excluye zombies; `ProfilesByTenant` foto completa, versión solo la mueve `SetProfile`, filas discordantes → gana `passive`, sin sesiones → mapa vacío; bloque del worker: desconocido ≠ cero, rancio limpia lo anterior | `fleethelpertest.Memoria` | `PostgresRepository` (+ self_pn cifrado y su índice ciego, solo en F9) |
| `diagnosticshelpertest.ContratoStore` | consentimiento por defecto **ON** (opt-out); solicitud ⇒ bundle ⇒ descarga correlada por `command_id` + (tenant, sesión); bundle huérfano o de otro tenant no rompe; vencido → `ErrExpired` y borrado perezoso; crear purga vencidas; borrar solicitud (rollback) | memoria | `diagnostics.Postgres` |
| `receiptshelpertest.ContratoStore` | idempotente por (sesión, mensaje, estado) | memoria | `PostgresStore` |
| `ingesthelpertest.ContratoDeduper` | primera vez `false`, segunda `true` para la **misma** (sesión, wa_message_id) | memoria | `PostgresDeduper` (poda perezosa por retención, solo F9) |

## 3 · Contratos por fichero — lo que promete cada uno

| Fichero | Exportados clave | Promesa (resumen del comentario) |
|---|---|---|
| `session/registry.go` | `Registry`, `NewRegistry`, `WithSendTimeout`, `Sender`, `SendAcotado`, `ErrSessionOffline`, `ErrPushTimeout`, `ErrPushAbandonado` | `Push` a sesión inexistente → `ErrSessionOffline` (gana a un ctx cancelado); Edge que no lee → `ErrPushTimeout`; ctx cancelado antes de enviar → `ErrPushAbandonado`, **no** timeout; doble registro **última-gana** y `release` compara identidad (el stream reemplazado no borra al nuevo); el registro es **seguro en concurrencia** y **no retiene su mutex durante el `Send`** — 🔴 **no serializa los envíos**: eso lo hace el envoltorio por stream del gateway (`grpc.streamSender`, R-G12; corregido en F3-03, hallazgo 10). `ErrSessionOffline` **es** el de `platform` (D-F3-2) |
| `inferstats/inferstats.go` | `Store`, `New`, `Parte`, `Clave`, `Agregado` (alias de `platform/metrics`, F0) | clave (tenant, **edge**): tres teléfonos de un Edge **no triplican**; el último parte **sustituye** (es acumulado, no delta); entre Edges se suma; un Edge que se va **no hace bajar** la suma; `nil` + `nil` = `nil` (no medible); copia los mapas; nil-safe y concurrente |
| `receipts/{receipts,sink}.go` | `Status` (`delivered`, `read`), `Receipt`, `Stored`, `Store`, `Sink`, `NewSink` | una fila por `message_id`; `UNSPECIFIED` no persiste nada; callback de métrica por fila |
| `ingest/{dedupe,postgres}.go` · `deduper.go` ✚ | `Deduper`, `MemoryDeduper`→`ingesthelpertest`, `PostgresDeduper`, `WithRetention`, `WithSweep` | idempotente; opciones ≤0 se ignoran |
| `diagnostics/diagnostics.go` | `Store`, `BundleReceiver`, `Bundle`, `Record`, `NewCommandID`, 3 centinelas | los de la suite; `NewCommandID` aleatorio |
| `lease/lease.go` 🔒 | `Manager`, `NewManager`, `Option`, `WithTTL`, `DefaultTTL` | ver R-L1…R-L8 |
| `lease/signingkey.go` | `ResolveSigningKey`, `KeySource*`, `ParsePrivateKeyBase64`, `LoadPrivateKeyPEM`, `GenerateDevKey` | fichero > base64 > **efímera** (`KeySourceGenerated`); una configurada es **estable** entre llamadas; la generada no |
| `enroll/*.go` | `CA`, `NewCA`, `NewDevCA`, `LoadCAFromPEM`, `ParseAndVerifyCSR`, `Service`, `Server`, `WithCloudEncPubkey`, `WithLeasePubKey`, `DefaultEdgeCertTTL` (90 días), centinelas | cert hoja con EKU **ClientAuth**, `Organization`=tenant, vida corta; código válido → cert; reutilizado/ausente/CSR inválido → error; la respuesta publica `cloud_enc_pubkey` y `lease_pubkey` **solo** si se configuraron |
| `fleet/fleet.go` | `Repository`, `Session`, `State*`, `Profile*`, `TenantProfiles`, `HealthSnapshot`, `DeviceLimit` (4), `ValidProfile`, `ValidAdminState`, 2 centinelas | los de la suite; `defaultProfile` = pasivo |
| `filtercfg/filtercfg.go` | `Kind` (`"filters"`), `Build`, `ForTenant`, `Pusher`, `NewPusher`, `Source`, `ConfigPusher`, `Payload`, `SessionFilter` | ver R-C1…R-C5 |
| `grpc/*.go` | `Server`, `New` y 12 `With*`, `ConfigProvider`, `ConfigPayload`, `ReceiptSink`, `LogReceiptSink`, `InferRequest`, `InferError`, `SendError`, `ErrStreamClosed`, `ErrInferenceSinClaveDeCifrado`, `Motivo*`, `Clase*`, `DefaultInferGrace`; métodos `Connect`, `Register`, `SendText`, `SendMedia`, `Ping`, `Infer`, `PlazaDe`, `PushConfig`, `RequestDiagnostics`, `RevokeLease`, `RevokeTenant`, `RestoreTenant`; hooks `OnIncoming`, `OnHeartbeat`, `OnWarmup`, `OnEdgeReady` | ver R-G1…R-G30 |

Tests de `grpc`: el Edge del otro lado es un doble de `session.Sender` / stream en memoria
(`bufconn` para el camino gRPC completo, sin red); el mTLS con certificados reales va al e2e local.

## 4 · Reglas que el contrato debe llevar (E-8)

**lease** 🔒 (26 `Test*`; ADR-0007, fuera de este repo: *«dos secretos disjuntos y hacen falta los
dos para despachar: la DEK la custodia el cliente y la nube nunca la ve; el lease lo emite y **revoca**
el servidor y es el kill-switch anti-clon. Un clon del `.db` sin lease es inútil»*)
- R-L1 `IssueInitial` → counter 1; `Renew(hb)` → hb+1 (monótono, anti-replay); TTL 15 min salvo `WithTTL(d>0)` (D-055.7).
- R-L2 Revocar y luego `IssueInitial`/`Renew` → **sigue revocado** (REQ-055.3); `Upsert` no resucita (T2.1, en memoria y en Postgres).
- R-L3 Tenant revocado **gana** sobre un Edge nunca visto (T3.2); tenant activo + Edge nuevo → vigente.
- R-L4 `RestoreTenant` desbloquea la emisión futura sin tocar `leases`; `SignTenantRevocation` firma sin persistir por Edge (dos sujetos de corte, D-055.2).
- R-L5 Fail-closed: error leyendo el estado → error, **ningún** lease.
- R-L6 `Revoke` no depende del counter: el kill-switch se dispara siempre.
- R-L7 `NewManager` con clave inválida o repo nil → error (`"lease: construir issuer: …"`, `"lease: repositorio nil"`).
- R-L8 El lease no contiene la DEK ni llaves privadas (`lease.go:12-13`). La revocación sobrevive al reinicio del `Manager` (REQ-055.4 → F9).

**session** (7) — R-S1…R-S4: las de §3 (`registry_test.go:36-238`). R-S4 es «seguro en concurrencia sin retener el mutex durante el `Send`», **no** «envíos serializados» (hallazgo 10): la serialización es R-G12.

**grpc** (144; ADR-0040, ADR-0045, ADR-0048)
- R-G1 `route` atiende los 10 frames; inline solo `Incoming`, `Ack`, `InferenceResult`, `Pong` y el handshake; el `Ack` y el `Incoming` se resuelven **con el carril tapado** (`connect_lane_internal_test.go:256-347`).
- R-G2 El `Heartbeat` llega a `fleet` con **su propio deadline**; el handshake trae su reloj y **se rinde** sin colgar el `Recv` si la base no contesta (`connect_handshake_reloj_internal_test.go`).
- R-G3 Carril: serial dentro de la sesión, paralelo entre sesiones; el heartbeat se **coalesce en sitio** y solo corre el último; receipts **no** se coalescen; un logout ni se coalesce ni lo borra un latido posterior; cola llena **frena** sin perder ni crecer; presupuesto por job cancela y el carril sigue; `seal` cierra y `drain` espera, y si se agota dice qué queda; submit sin trabajo es error; tope 64 y 5 s por defecto (`worklane_internal_test.go`, `work_config_internal_test.go`).
- R-G4 Reconexión rápida: el `MarkOffline` diferido del stream viejo **no** deja offline a la sesión que ya volvió; sin reconexión sí marca offline (DEUDA-050.1, `connect.go:917-940`).
- R-G5 Multi-sesión por stream: cada `session_id` recibe su lease; cada frame se despacha bajo **su** `session_id`; hot-join de una segunda sesión; reenvío del mismo id no re-registra; cierre del stream → todas offline.
- R-G6 Heartbeat `LOGGED_OUT` marca zombie; `UNSPECIFIED` sigue online; salud degradada se persiste y un Edge viejo sin `SessionHealth` no toca campos; el heartbeat renueva el counter del lease.
- R-G7 Foreign cert rechazado; revocación bloquea (`TestMTLSRejectsForeignCert`, `TestMTLSRevokeBlocks` → e2e local).
- R-G8 Canal de control (ADR-0048): R3.4.a–c; `__wapp_control__` no produce fila de flota (MP-11) y el control positivo sí.
- R-G9 Auth: tenant del `AuthResult` ≠ tenant del canal mTLS → `UserAuthError{tenant_mismatch}`; `ErrInvalidCredentials` → `invalid_credentials`; refresh rota; logout → `UserTokens` vacío. Dos Edge (mismo tenant **y** distinto) hacen login a la vez y cada uno recibe **su** respuesta; apagar uno no deja al otro sin auth.
- R-G10 Auditoría: la acción del operador lleva su `sub`; la del daemon, el `edge_id`; se distinguen en la misma bitácora.
- R-G11 `SendText`/`SendMedia`: `command_id` generado **dentro**; sesión offline → `SendError` con `command_id`; se rinde con **su** reloj aunque el llamante no traiga deadline; `awaitAck` no se come un Ack legítimo; `ackTimeout` (8 s) **por debajo** del `WriteTimeout` HTTP (10 s) para que el 504 llegue antes; los tres caminos de salida dejan el mapa de acks vacío; el cierre del stream despierta en el acto al envío en vuelo; el cierre del stream **viejo** no cancela si la sesión sigue online en otro; Ack tardío + cierre no escriben en canal cerrado; el cierre de una sesión no cancela envíos de otra; stream caído se distingue del timeout por `StreamCaido()`.
- R-G12 `streamSender` serializa `Send` concurrentes sobre un stream (ADR-0008).
- R-G13 Inferencia: prompt entra, JSON crudo sale; cada error del frame trae su motivo (1:1 con el enum del proto); sin sesión viva → `edge_offline`; el presupuesto del Cloud vence → motivo timeout; el llamante que se rinde **no** tiene motivo; stream caído despierta en el acto; la entrada pendiente no se fuga; con dos sesiones vivas decide el **origen**; el **destino** manda sobre el origen y no viaja en el payload; fallos de la nube (sin clave, sobre ilegible, oneof vacío) sin motivo; resultado huérfano no rompe; margen `DefaultInferGrace` 5 s siempre materializado.
- R-G14 Vocabulario: cada `Motivo*` es un `degradation.Reason` válido y las vías coinciden (test con strings literales, sin importar `degradation`: F4 aún viejo); el enum del proto no crece sin decidir su motivo.
- R-G15 Afinidad (ADR-0048 regla 3): R3.4.d, con los dos tests **en pareja**; `PlazaDe` es el mismo Edge que atiende la inferencia.
- R-G16 Readiness: solo el **flanco** a `READY` calienta y avisa al pipeline (`OnWarmup`, `OnEdgeReady`); `UNSPECIFIED` no es `DOWN` (ni calienta ni olvida); tras reinicio del Cloud, el primer latido visto calienta; se olvida el readiness solo con la **última** sesión del Edge; sin hook el gateway se comporta igual; orden: primer latido sin decir nada → calienta el registro; `DOWN` → cero calentamientos hasta `READY`; `READY` → lo dispara la transición, no el registro.
- R-G17 Config: `PushConfig` a **todas** las sesiones vivas del tenant y a ninguna más; calienta **una vez por Edge**, no por sesión; al conectar empuja los tres kinds (`jwks`, `intents`, `filters`) y sin `llm_intent` solo dos; `calientaPorRegistro` calienta salvo el canal de control.
- R-G18 Inferstats: el latido **alimenta** el almacén; tres teléfonos no triplican; no se cuelga de `fleet`; sin salud ni identidad no revienta.
- R-G19 Diagnóstico: `RequestDiagnostics` empuja el frame; el bundle se correla; huérfano no rompe; sin identidad es no-op.
- R-G20 Entrantes cifrados: `enc_payload` se abre y repuebla en memoria; corrupto → descarte **sin** loguear contenido; sin `enc_payload`, campos planos; sin clave privada, se descarta el sellado.
- R-G21 `RevokeTenant` notifica a **todos** los Edge vivos **de ese** tenant y a ninguno más.
- R-G22 Receipt: se rutea, se loguea correlado y va al sink.
- R-G23…R-G30 Aviso de sesión pasiva (greeting): idempotente entre latidos (consulta + marca con centinela `WHERE greeted_at IS NULL`); si el Edge **rechaza** el envío no se marca y se reintenta en el siguiente latido (MD-046.3); sin Ack no hay marca; sin `self_pn` o sin identidad no dispara; **el literal**: golden byte a byte, dice «las tres cosas y nada más», y coincide con `documentations/literal-aviso-sesion-pasiva.md` leído desde `../../../../documentations/…`. El `sessionGreeter` es un puerto **propio** del consumidor (no de `fleet.Repository`).

**fleet** (33) — los de la suite (§2), más: el `self_pn` se cifra con el `FieldCipher` y se busca por
índice ciego; dos grafías del mismo número colapsan en el mismo índice; pasiva y activa del mismo
número bloquean (F9). ⚠️ El índice ciego depende de `nucleo/contact.Normalize` (T-10 de F1).

**filtercfg** (11)
- R-C1 `Kind == "filters"` literal (el Edge se escribió en paralelo contra ese string).
- R-C2 `Build` incluye **todas** las sesiones, también las activas (fail-open del contrato); sin sesiones → `"sessions":{}`, no `null`; perfil desconocido degrada a `passive`.
- R-C3 `Pusher` empuja la foto del **tenant entero**, no la de la sesión disparadora; error de lectura se propaga (el handler lo loguea y **no** cambia el código); sin gateway es no-op.
- R-C4 `ForTenant` **no** consulta entitlements y siempre devuelve config (regla 1 de Plan 046 · T2.1).
- R-C5 Push fallido no cambia la respuesta HTTP (best-effort) → F9.

**enroll** (13) · **diagnostics** (9) · **receipts** (4) · **ingest** (4) · **inferstats** (7): los de §2–§3.

## 5 · Textos observables byte a byte

- 🔒 `avisoSesionPasivaID = "AVISO_SESION_PASIVA_V1"` y `avisoSesionPasivaV1` (`grpc/greeting.go:13,38`).
- Centinelas: `session`: `"sesión offline"`, `"timeout empujando comando al Edge"`, `"el llamante se rindió empujando el comando al Edge"`; `grpc`: `"gatewaygrpc: el stream de la sesión se cerró antes de que llegara el ack"`, `"gatewaygrpc: inferencia sellada pero la nube no tiene clave de cifrado"`, el formato de `InferError.Error()` `"gatewaygrpc: inferencia %s por la sesión %s: %s: %v"` (**el prefijo `gatewaygrpc:` se conserva** aunque el paquete se llame `grpc`); `fleet`: `"perfil de sesión inválido (usar active|passive)"`, `"estado de sesión inválido (usar offline|loggedout)"`; `diagnostics`: `"diagnóstico no encontrado"`, `"diagnóstico expirado"`, `"diagnóstico pendiente"`; `enroll`: `"enroll: código de activación desconocido"`, `"enroll: CSR inválido"` y hermanos; `lease`: `"lease: …"`.
- Motivos (`Motivo*`, p. ej. `"ollama_down"`) y clases `"interactivo"`, `"lote"`; `filtercfg.Kind` `"filters"`; los `kind` `"jwks"`, `"intents"`.

## 6 · Candados de invariante y lo que pasa a F9

| Candado | Regla | Dónde queda |
|---|---|---|
| `grpc/greeting_internal_test.go` (`TestGoldenDelLiteralDelAviso`, `…TresCosas…`, `…CoincideConElRunbook`) | 🔒 el literal byte a byte | `E/grpc/greeting_test.go`; solo cambia la ruta relativa al `.md` (`03` §1) |
| Pareja ADR-0048 (`…DOWN_NoRecibeElPrompt` + `…ElQueNoLoDice_SigueSiendoElegible`) | ninguna de las dos vale sola | `E/grpc/inference_test.go` y `plaza_test.go` |
| `TestAckTimeoutPorDebajoDelWriteTimeoutHTTP` | 8 s < 10 s | `E/grpc/send_test.go` (invariante numérica, sin AST) |

**Pasan a F9, proceso «Enrolamiento de un Edge y su lease»** (`05` §7.4: `EnrollEdge` → certificado
→ `Connect` → lease emitido → **revocación**): el SQL de las 7 suites; R-L2/R-L3/R-L8 con reinicio del
proceso; la migración `0058` y `0003` idempotentes; `fleet` self_pn cifrado e índice ciego; el push
de filtros extremo a extremo (R-C5); `mTLS` real (R-G7, R3.4.e); la revocación de tenant con dos Edge
vivos (R-G21). No pasan (mediciones, D-F3-5): `load_*`, `curva_pool_t55`, `deuda_050_2`.
