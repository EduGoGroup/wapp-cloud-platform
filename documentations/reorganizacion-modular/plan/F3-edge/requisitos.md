# F3 · Requisitos — historias y criterios EARS

> Formato de [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §2. `E` =
> `internal/modulos/edge`. Cada criterio nombra el comando o el test que lo verifica.

## H3.1 · Contratos y rojo de todo el módulo

> Como **la sesión web**, quiero el contrato y el test en rojo de cada fichero de `edge` antes que su
> lógica, para que cada fichero nazca con lo que lo valida.

- **R3.1.a** · **EL** contrato de cada fichero de `E/**` **DEBERÁ** llevar solo exportados, cabecera
  `// Porta <ruta vieja> @ <sha>` y cuerpos `panic(pendiente.Implementar(…))`. — Verifica:
  `golangci-lint run ./internal/modulos/edge/...` sin `unused`; ningún `return nil, nil` en rojo.
- **R3.1.b** · **EL** `x_test.go` **DEBERÁ** llevar `//go:build pendiente` en rojo y mencionar todo
  exportado de `x.go`. — Verifica: `exportados_cubiertos_test.go`.
- **R3.1.c** · **SI** un test de `E/**` llama a `t.Skip` o usa `time.Sleep` como sincronización,
  **ENTONCES EL** gate **DEBERÁ** fallar (el primero) o la revisión rechazarlo (el segundo). —
  Verifica: `grep -rn 't.Skip' internal/modulos/edge` vacío; `-v` sin `--- SKIP`.
- **R3.1.d** · **EL** doc.go de `E/enroll` **NO DEBERÁ** llevar test (excepción E-3). — Verifica:
  `un_fichero_un_test_test.go` verde.

## H3.2 · Los puertos del Edge nacen cubiertos

> Como **la sesión web**, quiero una suite por puerto con doble en memoria, para que lo que hoy
> prueban 40 tests de integración quede especificado antes de F9.

- **R3.2.a** · **CADA** puerto con implementación en memoria y en Postgres (`lease.Repository`,
  `enroll.CodeStore`, `enroll.EdgeCertRepository`, `fleet.Repository`, `diagnostics.Store`,
  `receipts.Store`, `ingest.Deduper` ✚) **DEBERÁ** tener `<paq>test.Contrato…` con la firma de D-F1-1
  y un doble que la pase con `-race`. — Verifica: `go test -race -v ./internal/modulos/edge/...` 0 FAIL, 0 SKIP.
- **R3.2.b** · **CADA** adaptador Postgres **DEBERÁ** correr su suite desde un test
  `//go:build integracion`. — Verifica: `go vet -tags integracion ./internal/modulos/edge/...` rc=0.

## H3.3 · 🔒 La doble llave no cambia (ADR-0007)

> Como **el Edge**, quiero que el lease se emita, renueve y revoque exactamente igual, para que el
> kill-switch anti-clon siga funcionando sin recompilarme.

- **R3.3.a** · **EL** `lease.Manager` nuevo **DEBERÁ** emitir con `counter=1` al conectar y
  `heartbeatCounter+1` al renovar, TTL `DefaultTTL = 15 * time.Minute` salvo `WithTTL(d>0)`. —
  Verifica: `lease_test.go` nuevo.
- **R3.3.b** · **SI** el tenant o el Edge están revocados en el estado persistido, **ENTONCES**
  `IssueInitial`/`Renew` **DEBERÁN** devolver el `LeaseUpdate` **de revocación** sin tocar `Upsert`
  (revocación pegajosa, D-055.1); **SI** leer el estado falla, **ENTONCES DEBERÁN** devolver error y
  ningún lease (fail-closed). — Verifica: suite `ContratoRepository` + `lease_test.go`.
- **R3.3.c** · **EL** tenant revocado **DEBERÁ** ganar sobre un `edge_id` nunca visto;
  `RestoreTenant` **DEBERÁ** desbloquear la emisión futura sin tocar `leases`;
  `SignTenantRevocation` **NO DEBERÁ** persistir por Edge. — Verifica: `lease_test.go`.
- **R3.3.d** · **EL** lease **NUNCA DEBERÁ** contener la DEK ni una llave privada, y ningún
  contrato nuevo de `edge` **DEBERÁ** tener un campo que la transporte. — Verifica:
  `grep -rni '\bdek\b' internal/modulos/edge --include='*.go' | grep -v '^\S*:\s*//'` vacío.
- **R3.3.e** · **SI** no hay `WAPP_LEASE_PRIVATE_KEY_FILE` ni `…_B64`, **ENTONCES**
  `ResolveSigningKey` **DEBERÁ** generar una clave **efímera** y decir `KeySourceGenerated`. —
  Verifica: `signingkey_test.go`.

## H3.4 · El canal de control no es una sesión (ADR-0048)

> Como **la dueña del negocio** que entra por la consola del Edge, quiero que mi login y la
> configuración de mi empresa nunca viajen al Edge de otra, para que no se repita HS-14/HS-15.

- **R3.4.a** · **CUANDO** llegue un `UserLogin/Refresh/Logout`, **EL** servidor **DEBERÁ** responder por
  el **mismo** stream (`cc.sender` con `session.SendAcotado`) y, **SI** falta el `sender`,
  **ENTONCES DEBERÁ** registrar un `Error` y **no** caer a `registry.Push` (INV-057.1, REQ-057.3). —
  Verifica: `TestPushAuthResponse_SalePorElStreamQuePregunto`, `…_SinSenderNoCaeAlRegistry` nuevos.
- **R3.4.b** · **MIENTRAS** el `session_id` sea `cltransport.ControlSessionID` (`__wapp_control__`),
  **EL** servidor **NO DEBERÁ** registrarlo en `session.Registry` ni en `edgeSessions`, ni emitirle
  lease, ni producir fila de flota (INV-057.2, REQ-057.4/8). — Verifica: `TestPushConfig_NoAlcanzaElCanalDeControl`,
  `TestRegisterSession_ElCanalDeControl_NO_ProduceFilaDeFlota` y su control positivo.
- **R3.4.c** · **CUANDO** un Edge sin teléfonos conecte, **EL** servidor **DEBERÁ** empujarle sus
  configs **por su propio stream** (`pushConfigsInBand`, REQ-057.7). — Verifica:
  `TestConfigAlConectar_LlegaAlEdgeSinNingunTelefono`.
- **R3.4.d** · **EL** enrutado de inferencia **DEBERÁ**: dar el stream del candidato vivo si lo hay;
  si no, elegir entre sesiones **reales** del tenant excluyendo los Edge `DOWN`, prefiriendo `READY`,
  y tratando `UNSPECIFIED` como elegible; y **SI** nadie puede, **ENTONCES** fallar con motivo
  `edge_offline` envolviendo `ErrSessionOffline`. `PlazaDe` **DEBERÁ** usar el mismo criterio (INV-057.3). —
  Verifica: los dos tests **en pareja** `…ElEdgeQueDijoDOWN_NoRecibeElPrompt` y `…ElQueNoLoDice_SigueSiendoElegible`.
- **R3.4.e** · **LA** identidad del Edge **DEBERÁ** salir del certificado mTLS verificado, nunca del
  payload (INV-057.4). — Verifica: `mtls` en el e2e local (T3.29) y los unitarios de `peerIdentity`.

## H3.5 · Los contratos hacia fuera no se mueven

> Como **el Edge**, quiero el mismo proto, el mismo literal y los mismos listeners.

- **R3.5.a** · **EL** `go.mod` **NO DEBERÁ** cambiar la línea `github.com/EduGoGroup/wapp-cloudlink v0.17.0`.
  — Verifica: `git diff <sha-inicio-F3> -- go.mod` sin esa línea.
- **R3.5.b** · **EL** literal `AVISO_SESION_PASIVA_V1` **DEBERÁ** coincidir byte a byte con el golden
  y con `documentations/literal-aviso-sesion-pasiva.md` (que **no** se edita). — Verifica:
  `TestGoldenDelLiteralDelAviso`, `TestElLiteralDiceLasTresCosasYNadaMas`, `TestElLiteralCoincideConElRunbook` nuevos.
- **R3.5.c** · **EL** binario nuevo **DEBERÁ** exponer `cloudlinkv1.CloudLink/Connect` en `:8101` con
  **mTLS estricto** (TLS 1.3, certificado de cliente exigido) y `Enrollment/EnrollEdge` en `:8102` con
  TLS **solo de servidor**, keepalive `30s/10s/15s` y `PermitWithoutStream`. — Verifica: `huella_test.go`
  (rpc) y el e2e local.
- **R3.5.d** · **EL** `route()` nuevo **DEBERÁ** atender los 10 tipos de `EdgeToCloud`; resolver inline
  `Incoming`, `Ack`, `InferenceResult`, `Pong` y el handshake; y soltar al carril por sesión
  `Heartbeat` (coalescido en sitio), `Receipt`, `DiagnosticsBundle`, `UserLogin/Refresh/Logout`
  (ADR-0040). — Verifica: `TestRouteSoloLasRamasPesadasEntranAlCarril` nuevo.
- **R3.5.e** · **LOS** textos de §5 de [`diseno.md`](diseno.md) **DEBERÁN** quedar byte a byte. —
  Verifica: los tests de cada fichero.

## H3.6 · Un solo gateway en el proceso

> Como **Jhoan**, quiero una sola instancia de `*grpc.Server` en el binario nuevo, porque guarda
> las conexiones vivas: dos serían un Edge conectado a uno y un envío buscándolo en el otro.

- **R3.6.a** · **EL** arranque nuevo **DEBERÁ** construir **un** `edge/grpc.Server` y pasarlo a: los
  hooks del runtime viejo (`OnIncoming`, `OnHeartbeat`), `OnWarmup`/`OnEdgeReady` de captación, el
  `Sender` del runtime viejo, el `MessageSender` del notificador viejo de solicitudes, el
  `ConfigPusher` de `filtercfg` nuevo, los handlers de `:8100` J12–J15 y (por `puente_gateway.go`)
  el `local.Frame` y el `enrutadorDeEdges` del selector LLM viejo. — Verifica: aserciones de
  identidad de FX TX.11 (`==` sobre el puntero o sobre `puenteGateway.gw`).
- **R3.6.b** · **EL** adaptador `puente_gateway.go` **DEBERÁ** implementar **también** `PlazaDe`:
  sin él, `llmvia.go:163` (`s.frame.(enrutadorDeEdges)`) da `false` y el aforo por Edge queda
  **inerte sin error**. — Verifica: `puente_gateway_test.go` con aserción de tipo sobre la interfaz.
- **R3.6.c** · **EL** binario nuevo **NO DEBERÁ** enlazar `internal/gateway/grpc` salvo por
  `puente_gateway.go` y `llmvia` viejo, ni instanciar un `gatewaygrpc.Server` viejo. — Verifica:
  `grep -rn 'gatewaygrpc.New(' internal/arranque` vacío.
- **R3.6.d** · **CUANDO** conmute `edge`, `puente_iam.go` (F2) **DEBERÁ** borrarse: el gw nuevo
  recibe el `in.Authenticator` e `in.Auditor` **nuevos** de `acceso`. — Verifica: `ls internal/arranque/puente_iam.go` → no existe.
- **R3.6.e** · **EL** binario nuevo **DEBERÁ** servir por `apipublica` D1–D6 y E1–E2 y por los
  handlers nuevos J12–J17 (D3/J16 y D4/J17 en el mismo commit). — Verifica: `huella_test.go` y FX TX.11.

## H3.7 · El kill-switch se prueba de punta a punta

> Como **la sesión local**, quiero correr enrolamiento → `Connect` → lease → revocación con mTLS real
> contra los dos binarios.

- **R3.7.a** · **EL** `diseno.md` §6 **DEBERÁ** listar las reglas que pasan al proceso
  «Enrolamiento de un Edge y su lease» (`05` §7.4). — Verifica: la lista y su referencia en `plan/F9-procesos/`.
- **R3.7.b** · **DONDE** F9 esté adelantado, **EL** cierre de F3 **DEBERÁ** correr ese proceso contra
  `cmd/server-modular`. — Verifica: `make test-procesos` (T3.30, condicionada).
