# FW · Diseño

> ✅ **Medido por FW-01 el 2026-10-11** sobre `edge/wapp-edge-agent` @ `38e5f91` (`main`, igual que `origin/dev`) y este
> repo @ `f84c9f49` (`dev`). Todo sale de **leer** código: no se ejecutó nada del Edge. Las rutas sin prefijo son del
> Edge; las de este repo llevan `C:`. ✅ **Decidido por Jhoan el 2026-10-11, al arrancar FW-02** (§3 y §5; D-FW-2, 4,
> 6, 7 y 8 en [`../DECISIONES.md`](../DECISIONES.md) §8). ✎ FW-02 construyó §3 en el Edge, rama
> `feat/fw-02-whatsapp-falso`: lo que cambió respecto a lo propuesto está marcado con ✎.
> Lo que solo se puede saber ejecutando está en §6, con su porqué y con la sesión que lo mide.

## 1 · Lo que hay hoy

| Pieza | Dónde | Qué promete hoy |
|---|---|---|
| `Connector` | `internal/app/pair.go:67` | `StartConnection(ctx, dek) (<-chan PairingSignal, error)`: señales QR, Connected, Error |
| `ListenGateway` | `internal/app/listen.go:80` | `Listen(ctx, dek) error`: conecta, anota los entrantes en la cola durable y bloquea hasta cancelar |
| `LiveSender` | `internal/app/listen.go:91` | Tres envíos por el cliente vivo. 🔴 **Sin consumidor de producción**: solo lo usa una aserción de test (`internal/adapters/whatsmeow/listen_gateway_test.go:11`); el envío real va por *closures* (fila siguiente) |
| Envío real | `internal/app/sessionmgr/listen.go:163-174` → `session.go:209-230` | `liveSend` y `liveMediaSend`: *closures* que el mux de CloudLink recibe en `cloudMux.Register` (`listen.go:288`) |
| Adaptador real | `internal/adapters/whatsmeow/` | Cumple los puertos; lo **construye `sessionmgr`**, no el arranque (§2.1) |
| Inyector (ADR-0041) | `internal/adapters/control/diag/inyector.go:70`, `internal/app/sessionmgr/inyector.go:57`, `internal/adapters/whatsmeow/inyector.go:270` | Ruta `POST /v1/diag/inbound/inject`, recurso `edge.diag.inbound.inject`, `409` sin escucha viva |
| Registro condicional | `internal/infra/daemon/daemon.go:495-510` | Con la palanca baja la ruta **no se registra**; con ella echada, `Warn` al arrancar y campo `inyector` en el latido (`internal/app/latencia/latido.go:168-174`) |

## 2 · Las seis preguntas de TW.1, contestadas

### 2.1 · ¿Qué tipos de `whatsmeow` cruzan los puertos, y cuáles estorban?

**Ninguno cruza un puerto.** Las firmas de `Connector`, `ListenGateway`, `LiveSender`, `LiveLogout` (`internal/app/logout.go:27`),
`PairingSignal`, `InboundSink` (`internal/app/listen.go:59`), `InyeccionEntrante` (`internal/app/inyector.go:41`) y
`domain.ReceiptEvent` (`internal/domain/receipt.go:25`) son tipos básicos. `internal/domain` **no importa** `go.mau.fi`
(lo de `internal/domain/jid.go:7` e `internal/adapters/cloudlink/adapter.go:628` son comentarios; ver hallazgo 1 del
[README](README.md)).

Fuera de `internal/adapters/whatsmeow/` importan `go.mau.fi/*` **9 ficheros de producción en 3 directorios**
(`grep -rn 'go.mau.fi/' --include='*.go' . | grep -v _test.go`, contando ficheros con `import`, no menciones).
El estorbo no está ahí: está en que **`sessionmgr` construye el adaptador concreto**.

| Qué | Dónde | ¿Estorba al falso? |
|---|---|---|
| `whatsmeow.NewListenGatewayForDevice(m.db, dialect, jid, log)` | `internal/app/sessionmgr/listen.go:110` | 🔴 **Estorba**: no hay costura pública para dar otro transporte |
| Diez métodos del tipo concreto `*whatsmeow.ListenGateway` que no están en ningún puerto (`SetHealthReporter`, `SetPushName`, `SetInboundMargin`, `SetCola`, `SetSesionPasiva`, `SetAviso`, `SetLatencia`, `InyectarEntrante`, `SetReceiptHandler` + `Correlator().Lookup`, `SetLoggedOutHandler`) | `internal/app/sessionmgr/listen.go:113-201` | 🔴 **Estorba**: el falso tiene que ofrecerlos tras una interfaz |
| `whatsmeow.NewConnector(storeDB, dialect, log)` | `internal/app/sessionmgr/pairing.go:55` | 🔴 **Estorba**: misma razón, en el emparejamiento |
| El centinela `whatsmeow.ErrSinEscuchaViva` | `internal/app/sessionmgr/inyector.go:96` (definido en `internal/adapters/whatsmeow/inyector.go:80`) | 🔴 **Estorba (leve)**: el `409` se decide por un error que vive en el paquete del adaptador real |
| `whatsmeow.Backoff` | `internal/app/sessionmgr/listen.go:316` | ⚪ No estorba: tipo puro (`backoff.go` solo importa `time`) |
| `types.ParseJID` antes de `cryptostore.DeleteDevice` | `internal/app/sessionmgr/pairing.go:181`, `unlink.go:262` | ⚪ No estorba **si el falso emite un JID bien formado**; `DeleteDevice` es idempotente sobre un device ausente (`internal/adapters/cryptostore/factory.go:108-115`). 🟡 Sin ejecutar: §6 |
| `types.ParseJID` | `internal/infra/edgemigrate/restore_active_sessions.go:238`, `:421` | ⚪ No estorba: migración de arranque, no pasa por los puertos |
| `internal/adapters/cryptostore/` (6 ficheros) | `upgrade.go`, `session.go`, `factory.go`, `cryptostore.go`, `device_anchor.go`, `container.go` | ⚪ No estorba: es el almacén de `whatsmeow`; el falso no lo abre |

**Las costuras ya existen, pero son privadas**: los campos `m.newPairer` y `m.newListener`
(`internal/app/sessionmgr/manager.go:77`, `:83`), que hoy solo sobrescriben los tests. La elección se activa en **un único
sitio** del arranque: `internal/infra/daemon/daemon.go:226-227` (`WithWhatsmeowListen`, `WithWhatsmeowPairing`).

### 2.2 · ¿Qué exige el arranque para dar una sesión por «escuchando»?

- **El daemon arranca sin ningún device pareado.** `Restore` lee las filas `devices` en estado `active`
  (`internal/app/sessionmgr/listen.go:233`, `internal/adapters/sessionstore/sessionstore.go:234-243`); con cero filas
  devuelve `nil` y el plano de control sube (`internal/infra/daemon/daemon.go:283-322`). Lo único fatal son las dos bases
  (`daemon.go:91-155`). Con cero sesiones, eso sí, el Cloud no tiene a quién mandar un `SendText`.
- **Una sesión es** un `session_id` (UUID, `internal/app/sessionmgr/pairing.go:80`) con su fila en `devices`: nace
  `pairing` con JID vacío (`pairing.go:92-97`) y pasa a `active` con el JID (`pairing.go:131-151`).
- **`Listen(ctx, dek)` exige las tres cosas** (`internal/adapters/whatsmeow/sender.go:131-153`): JID parseable, que la DEK
  abra el contenedor, y la fila del device en `msg_enc_device`. Si falta una, la sesión queda degradada y reintenta con
  *backoff* de 1 a 60 s (`internal/app/sessionmgr/listen.go:332-346`); nunca llega a `serve()`.
- **La DEK sale de la custodia de esa sesión** y la carga `app.Listen.Run` (`internal/app/listen.go:181`), que la borra al
  salir (`:200`). 🔴 **No sirve solo para el almacén de `whatsmeow`**: la **cola** de entrantes sella y abre con ella
  (`internal/infra/wiring/cloudlink.go:147-163`). Sin DEK no hay fila encolada ni despacho.
- **Con la sesión de WhatsApp caducada** (la duda que abría el README): si el rechazo llega con el proceso vivo, el
  `Listener` **sigue publicado** —se publica antes de `Connect()` (`internal/adapters/whatsmeow/listen_gateway.go:407-410`) y
  `LoggedOut` no lo retira (`internal/app/sessionmgr/failover.go:82-85`)—, pero la sesión ya salió del mux
  (`failover.go:110-112`). En un arranque **posterior** no hay escucha: `404` si la fila quedó `loggedout`, `409`
  perpetuo si quedó `active` con el device borrado. **Una sesión caducada no sirve de base**: hace falta el falso.

### 2.3 · ¿El inyector se puede reutilizar sobre el falso?

**Sí.** Del manejador HTTP al cable de la sesión, el contrato es una *closure* **sin tipos de `whatsmeow`**:

```go
// internal/adapters/control/diag/inyector.go:102
Inyectar func(ctx context.Context, sessionID string, p app.InyeccionEntrante) (acusar bool, err error)
// internal/app/sessionmgr/session.go:194
liveInyectar func(ctx context.Context, p app.InyeccionEntrante) (bool, error)
```

El primer tipo de la librería (`*events.Message`) aparece **dentro** del adaptador
(`internal/adapters/whatsmeow/inyector.go:152-202`), que entrega el evento a `handleEvent`
(`internal/adapters/whatsmeow/listener.go:465`). Ahí están **las cinco guardas de la puerta** (eco propio, perfil pasivo,
hora, ventana temporal, grupo: `listener.go:603-768`), el cronómetro, la doble marca y el encolado, que va por el puerto
`app.ColaEntrantes.Enqueue` (`internal/app/cola.go:344-351`, llamado en `listener.go:902`).

🔴 **La puerta es de `whatsmeow`, el transporte no.** `NewListener` es público y **no necesita cliente**
(`listener.go:331`); el cliente solo lo pide `Register` (`:421-430`). Un falso que **reutilice esa puerta** sin cliente ni
`Connect()` recorre literalmente el mismo código; uno que no la use tendría que reescribir guardas, meta y marca, y eso
es justo lo que el ADR-0041 descartó («no mide mal, mide otro tramo»). Decisión en §3.

### 2.4 · ¿Hay un doble de `LiveSender` que se pueda promover?

**No.** No hay dobles fuera de `_test.go` ni paquete `fake`. Los que existen son privados de su test: `fakeConnector`
(`internal/app/pair_test.go:13`), `fakeListenGateway` (`internal/app/listen_test.go:22`), `fakeFabric`
(`internal/app/sessionmgr/manager_listen_test.go:71`) y las capturas `sendCall` / `mediaCall` de
`internal/adapters/cloudlink/e2e_test.go:113-201`, que sí guardan todos los campos de un envío pero no devuelven
identificador de mensaje ni emiten acuse. Sirven de **molde**; el falso se escribe nuevo.

### 2.5 · ¿Qué necesita el Edge para arrancar contra un Cloud local?

- **El daemon es `agent serve`** (`cmd/agent/main.go:119-128`), sin *flags* y sin `wapp-ctl`. El plano de control es un
  **socket unix** `0600` (`internal/adapters/control/server/server.go:196-219`); el daemon no abre puerto de red.
- **El enrolamiento es no interactivo**: `agent enroll <código>` (`cmd/agent/main.go:176-195`) genera clave y CSR, llama a
  `EnrollEdge` en `:8102` y persiste certificado, clave, pública del lease, pública de cifrado y el *endpoint*
  (`internal/infra/enroll/enroll.go:94-147`, `:271-379`). Necesita de antemano la CA (`tls_ca`), el *endpoint* de
  enrolamiento y el código.
- **El arnés ya fabrica casi todo** (`C:test/procesos/`): la CA y el certificado de servidor (`pki_test.go:56-88`), las
  claves del lease y de cifrado (`claves_test.go:56-101`), el código de enrolamiento (`edgeEmitirCodigo`), el lanzador de
  procesos hijos (`iniciarProceso`, `servidor_process_test.go:75-108`) y el *login* de operador contra el doble de
  identidad (`identidad_login_test.go:94`). Su Edge falso hace **enrolamiento real** (`edge_falso_enrollment_test.go:53-108`).
- **Lo que falta para el modo «Edge real»** (lo construye FW-03, TW.7):
  1. **Obtener el binario.** `compilar` solo sabe `./cmd/…` de este módulo (`C:test/procesos/main_test.go:305-310`), el
     arnés exige `GOWORK=off` (`:136-142`) y **no hay precedente** de repo hermano ni de binario por variable. Decisión en §3.
  2. **Puertos.** El Edge deriva el *endpoint* de *runtime* con el puerto fijo `8101` (`internal/infra/enroll/enroll.go:328-362`)
     y el arnés usa puertos al azar: hay que darle `WAPP_AGENT_CLOUDLINK_ENDPOINT` explícito, que gana (`enroll.go:330-333`).
  3. **`WAPP_AGENT_CLOUDLINK_LEASE_PUBKEY_PATH` puesta.** 🔴 Sin ella no hay validador y el Edge **envía con el lease
     revocado** (`internal/infra/wiring/cloudlink.go:291-293`): el caso de RW.3.c sería verde sin medir nada.
  4. **`WAPP_AGENT_CLOUDLINK_CLOUD_ENC_PUBKEY_PATH` puesta**, o los entrantes suben en claro (`cloudlink.go:325-327`).
  5. **Rutas de socket cortas**: `t.TempDir()` no cabe en `sun_path` en macOS (`cmd/agent/serve_test.go:28-30`).
  6. **Activar la sesión**: nace pasiva y se descarta lo que entra; se activa por API con su `session_id` real
     (`C:test/procesos/p3_entrante_test.go:125-128`).
  7. **La custodia de la DEK**: ✎ D-FW-7 decidida, el Edge corre en un **contenedor Linux** (§5). 🔴 Trae dos
     trabajos que este inventario no midió: el plano de control es un socket unix **dentro** del contenedor (el arnés
     lo alcanza con `docker exec`, o montando el directorio del socket si el motor de Docker lo permite), y el
     contenedor tiene que llegar a los puertos gRPC del Cloud del arnés con un certificado cuyo nombre valga desde
     dentro. §6.
  8. ✎ FW-02: **`WAPP_AGENT_CLOUDLINK_LEASE_SHADOW_MODE` apagada** (es el default). Con ella encendida un lease
     revocado solo produce un `Warn` y el envío sale (`internal/adapters/cloudlink/adapter.go:133-139`): es la segunda
     forma de que RW.3.c salga verde sin medir nada, junto al punto 3.

### 2.6 · ¿Cómo se resuelve la inferencia en el caso de TW.8?

**El carrito no necesita modelo.** `TestP3_CartConversation` (`C:test/procesos/p3_entrante_cart_test.go:195-212`) es motor de
flujos: afirma 0 inferencias tras «8» (`:227-229`) y exactamente 1 tras «abc» (`:238-248`), que el Edge falso contesta con
`OLLAMA_DOWN` — y el flujo sigue. El Edge real, **sin Ollama y sin cajero**, contesta lo mismo: el cliente del socket del
cajero devuelve `ErrInferenciaOllamaCaido` por petición (`internal/infra/wiring/inferencia.go:20-33`). **TW.8 no lanza
Ollama ni `agent cajero`.**

Una diferencia a vigilar: el Edge falso late `READY` (`C:test/procesos/edge_falso_frames_test.go:47-55`) y el real, sin cajero,
`UNSPECIFIED` (`internal/adapters/cloudlink/readiness.go:132-141`). El Cloud trata el cero como «este Edge no lo dice»
(`C:internal/modulos/edge/grpc/readiness.go:139`), no como caído. 🟡 Si eso cambia el paso «abc» solo se ve corriendo: §6.

## 3 · Lo que se decide (TW.3) — ✅ decidido (Jhoan, 2026-10-11) y construido por FW-02

| Pregunta | Decisión propuesta | Por qué |
|---|---|---|
| **Palanca** | `WAPP_AGENT_WHATSAPP_FALSO` (campo `WhatsAppFalso bool`, yaml `whatsapp_falso`), default `false` **escrito** en `defaults()` | El molde es `InyectorEntrantes` (`internal/infra/config/config.go:513-533`, `:684`, `:771`) y la convención 6 de la constitución del Edge |
| **Paquete del falso** | `internal/adapters/whatsappfalso/` | El Edge nombra en español; es un adaptador hermano de `internal/adapters/whatsmeow/` |
| **La frontera (TW.4)** | `sessionmgr` deja de construir el adaptador: gana una interfaz con lo que hoy usa del tipo concreto (`listen.go:113-201`) y una opción pública que recibe el transporte; `ErrSinEscuchaViva` se muda a `internal/app`. El real y el falso se eligen en `internal/infra/daemon/daemon.go:226-227`. ✎ **Como quedó**: la interfaz `Escucha` y el struct `Transporte` viven en `internal/app/sessionmgr/transporte.go` (no en `internal/app`: nombra tipos de `health` y `latencia`, que importan `app`, y sería un ciclo); las opciones son `WithEscucha` y `WithEmparejamiento`; la cola entre cada adaptador y la frontera está en `internal/infra/wiring/transporte.go`; elige `elegirTransporte` (`internal/infra/daemon/daemon.go`). La frontera dice «un transporte», no «real o falso»: lo pidió Jhoan pensando en más transportes | Es lo listado como «estorba» en §2.1, y nada más (D-FW-4). Lo que queda fuera es la deuda **A-5** del `deuda.md` del Edge |
| **Cómo nace la sesión** | Por el **emparejamiento de siempre** (`internal/adapters/control/server/pair.go:80`), con un `Connector` falso que contesta «conectado» con un JID sintético bien formado | La DEK la sigue generando `app.Pair.Run` en el cliente (`internal/app/pair.go:143`) y sellándola en su custodia (`:175`): **la doble llave no tiene un camino aparte** |
| **Entrada** | Se **reutiliza** `POST /v1/diag/inbound/inject`, con su palanca `WAPP_AGENT_INYECTOR_ENTRANTES` también echada. El falso **reutiliza la puerta** del adaptador real (`NewListener` + `handleEvent`, sin cliente ni `Connect()`). ✎ **Como quedó**: `handleEvent` es privado, así que el adaptador real gana un fichero, `internal/adapters/whatsmeow/sin_transporte.go`, con lo justo para publicar ese mismo `Listener` sin socket; el falso lo envuelve y su `InyectarEntrante` **es** el del gateway real | §2.3. Misma ruta, misma marca `SINTETICO-`, mismo `409`; ninguna guarda reescrita. El binario enlaza `whatsmeow` igual: lo que se sustituye es el **transporte** |
| **Salida** | Ruta nueva `GET /v1/diag/outbound/captured?session_id=…`, registrada **solo** con la palanca echada, con el recurso `edge.diag.inbound.inject` | RW.2.c. Un recurso nuevo no se puede dar de alta desde aquí: los *grants* no viven en el código de producción de este repo (`grep -rn 'edge\.diag' --include='*.go' .` solo da tests). 🟡 Quién los concede al operador del arnés: §6 |
| **Dónde se guarda lo capturado** | **Memoria**, con tope (anillo); se pierde al reiniciar | Una tabla nueva en SQLite arrastra la trampa INV-E1 del Edge; el caso lee justo después de enviar. Sin Redis ni broker |
| **Qué se captura** | `command_id`, destinatario, texto; en media, además URL, nombre, MIME, clase y pie. **La media no se descarga** | RW.2.b. El real sí la descarga (`internal/adapters/whatsmeow/listen_gateway.go:581`): es una diferencia declarada, no escondida |
| **Acuses** | El `Ack` del comando es **el real** (`internal/adapters/cloudlink/adapter.go:841`, `:874`): el falso solo devuelve un id de mensaje con prefijo `SINTETICO-`. Además emite un acuse `delivered` por envío, por el manejador de acuses que ya existe (`internal/app/sessionmgr/listen.go:191-194`). `read`, no. ✎ **Como quedó**: el acuse entra por la puerta como un `events.Receipt` y sale 50 ms después del envío, para no fabricar el orden contrario al real (primero el `Ack`, luego el acuse); no es una garantía de orden | Con eso el Cloud recibe `Ack` y `MessageReceipt` como de un Edge real, sin inventar una lectura que nadie hizo |
| **Grito** | `Warn` al arrancar y campo propio en el latido, junto a `inyector` | RW.3.a; molde `daemon.go:503-507` y `latido.go:297-313` |
| **El binario en el arnés (TW.7)** | Variable `WAPP_PROCESOS_EDGE_DIR` con la ruta del repo del Edge; el arnés compila allí `./cmd/agent` con `GOWORK=off`, y el caso afirma ruta y revisión de git antes que nada. Etiqueta de build `edgereal` (además de `integracion`) y objetivo `make test-procesos-edge-real`, **fuera** de `make test-procesos` | Este repo se compila solo. Sin la variable el objetivo **falla**, no se salta: un SKIP no cuenta como verde |

## 4 · Tests

**En el Edge** (FW-02), con los nombres en español que usa ese repo:

| Requisito | Test | Tarea |
|---|---|---|
| RW.1.a | `TestPalancaWhatsAppFalso_ConLaPalancaECHADAElDaemonArrancaSinDeviceNiRed` | TW.5 |
| RW.1.b | `TestPalancaWhatsAppFalso_ConLaPalancaBAJADANadaCambia` (molde: `internal/infra/daemon/palanca_inyector_test.go:107`) | TW.5 |
| RW.1.c | `TestFronteraDelTransporte_SessionmgrNoConstruyeElAdaptadorReal` (sobre el AST, convención 3 del Edge) | TW.4 |
| RW.2.a | `TestWhatsAppFalso_ElEntranteInyectadoEntraPorLaPuertaRealYCaeEnLaCola` | TW.5 |
| RW.2.b | `TestWhatsAppFalso_SendTextYSendMediaQuedanCapturadosConSuCommandID` | TW.6 |
| RW.2.c | `TestCapturados_LaRutaDevuelveLoEnviadoYConLaPalancaBAJADANoExiste` | TW.6 |
| RW.3.a | `TestPalancaWhatsAppFalso_ElDaemonLoGRITAYElLatidoLoPublica` | TW.5 |
| RW.3.b | `TestWhatsAppFalso_TodoEntranteLlevaLasDosMarcas`, con su testigo real (`TestColaMeta_UnEntranteREAL_NoSeMarcaComoSintetico` sigue en verde) | TW.5 |
| RW.3.c | `TestWhatsAppFalso_ConElLeaseRevocadoNoSeCapturaNingunEnvio` | TW.6 |
| RW.3.d | `TestWhatsAppFalso_LaDEKNoSeGuardaNiSeRegistra`, más la revisión con `fichero:línea` | TW.5 |

**En este repo** (FW-03), tras las etiquetas `integracion` y `edgereal`: `TestEdgeReal_CartConversation` (TW.8, con el
caso de lease revocado dentro) y `TestEdgeReal_CartResumePolicy` (TW.11).

## 5 · Decisiones nuevas que salieron del inventario — ✅ decididas (Jhoan, 2026-10-11)

| # | Pregunta | Decisión |
|---|---|---|
| **D-FW-7** | La custodia de la DEK en macOS es el Keychain, **fijado en compilación**, y cada binario nuevo abre un diálogo en pantalla. ¿Cómo corre el caso sin nadie delante? | ✅ **El Edge corre en un contenedor Linux y la custodia no se toca.** Sin llavero, el Edge cae solo en el archivo `0600`, por el camino que ya existe (`internal/adapters/keycustody/secretservice_linux.go` degrada a `file.go`). **Descartado** el archivo en macOS tras la palanca, que era lo que FW-01 recomendaba: abría un segundo camino de custodia en el binario de producción, y el archivo en claro tiene un riesgo real (deuda **S-6** del Edge). ✎ **Corrige a FW-01**: en Linux la custodia es **Secret Service** y en Windows **DPAPI**; el archivo es solo el último recurso |
| **D-FW-8** | ¿El falso reutiliza la puerta del adaptador real (§2.3) o se escribe sin importar nada de `whatsmeow`? | ✅ **Reutiliza la puerta.** El paquete del falso depende del paquete del real. Cuando llegue otro transporte, la puerta se saca a un sitio neutral y todos pasan por ella (deuda A-5 del Edge) |

## 6 · Lo que no se pudo medir sin ejecutar

| Qué | Por qué | Quién lo mide |
|---|---|---|
| Si `Connect()` devuelve error síncrono con un device revocado | Depende del *handshake* real con WhatsApp | Nadie: deja de importar con el falso |
| ~~`cleanupPairing` y `unlink` con una sesión sin almacén de `whatsmeow`~~ | ✅ **Medido por FW-02**: desvincular una sesión falsa contra una base recién migrada no falla (`TestPalancaWhatsAppFalso_ConLaPalancaECHADAElDaemonArrancaSinDeviceNiRed`, paso 7). `cleanupPairing` no se ejercitó: el emparejamiento falso no falla | — |
| Si con *readiness* `UNSPECIFIED` el Cloud llega a pedir la inferencia de «abc» | Se ve en el log del servidor del arnés | FW-03, TW.8 |
| Quién concede `edge.sessions.pair` y `edge.diag.inbound.inject` al operador del arnés | El *login* se relaya al Cloud y este al doble de identidad; hay que verlo vivo | FW-03, TW.7 |
| ~~Si el archivo `0600` compila en darwin tal cual~~ | Ya no aplica: D-FW-7 salió (c), contenedor Linux | — |
| ✎ FW-02 · Cómo alcanza el arnés el plano de control del Edge en contenedor, y con qué nombre de certificado llega el contenedor al gRPC del Cloud | El plano de control es un socket unix dentro del contenedor; el certificado de servidor del arnés se emite para el anfitrión | FW-03, TW.7 |
| ✎ FW-02 · Si el Cloud acepta el número propio de una sesión falsa (`0000` + 8 cifras) que viaja en cada latido | Ningún prefijo internacional empieza por 0; si el Cloud valida el formato, se ve corriendo | FW-03, TW.7 |
