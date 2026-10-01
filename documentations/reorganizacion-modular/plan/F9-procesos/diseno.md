# F9 · Diseño — `test/procesos/` fichero a fichero, los dobles y los procesos

> Todo fichero de `test/procesos/` lleva `//go:build integracion` en la primera línea **salvo**
> `doc.go` y `sin_bd_viva_test.go` (F0, T0.8), que corren en `ci-local` sin Docker. Todo es paquete
> `procesos`. Lo que aquí se llama «contrato» de un helper es su comentario: qué recibe, qué
> devuelve, cuándo falla (E-2 aplica al código nuevo de `internal/`; aquí se usa la misma disciplina
> de comentario, sin `pendiente`: el arnés es código de test).

## 1 · Los ficheros

| Fichero | Nace en | Contrato (lo que promete) |
|---|---|---|
| `doc.go` | F0 · T0.8 | Comentario del paquete: qué es, la regla «nunca un Postgres vivo», cómo se corre |
| `sin_bd_viva_test.go` | F0 · T0.8 · ampliado en T9.3 | Falla con `fichero:línea` si aparece un patrón prohibido (§6) |
| `main_test.go` | F1 · T1.13 (mínimo, si D-F1-2) · completado en T9.5 | `TestMain`: un contenedor, plantilla migrada con `cmd/migrate`, binarios compilados una vez en `os.MkdirTemp`; `Terminate` al salir; **falla** sin Docker. Expone `instancia`, `rutaBinario(b)`, `binarioElegido()` |
| `base_test.go` | T9.5 | `nuevaBase(t, nombre) (dsn string)`: `CREATE DATABASE <nombre> TEMPLATE plantilla` sobre la base de mantenimiento; nombre `[a-z0-9_]` validado; `DROP DATABASE … WITH (FORCE)` en `t.Cleanup`. `abrir(t, dsn) *sql.DB` con `pgx/v5/stdlib`. Las DSN se derivan de `ctr.ConnectionString(ctx, "sslmode=disable")` cambiando la base; **nunca** un literal |
| `pki_test.go` | T9.6 | `nuevaPKI(t) pki`: CA EC P-256 (SEC1, como `scripts/gen-dev-certs.sh`), certificado de servidor con SAN `localhost` e IP `127.0.0.1`, cuatro ficheros PEM en `t.TempDir()`; `pki.Pool()` para los clientes |
| `claves_test.go` | T9.6 | `nuevasClaves(t) claves`: semilla Ed25519 del lease (base64), privada X25519 de la nube (base64, 32 B), KEK e índice (base64, 32 B), clave ES256 del emisor en fichero `0600` y su `kid`. Aleatorias por corrida: **ni una en el repo** |
| `s3falso_test.go` | T9.7 | `nuevoS3Falso(t, bucket) *s3Falso`: `httptest.Server` en `127.0.0.1`; `HEAD /<bucket>` → 200, cualquier otra ruta → 404; registra las peticiones (`s3.Peticiones()`) |
| `identidad_test.go` | T9.7 | `nuevaIdentidad(t) *identidad`: JWKS ES256 en `http://127.0.0.1:<p>/.well-known/jwks.json` (formato: `kty=EC`, `crv=P-256`, `x`, `y`, `kid`, `alg=ES256`; precedente `internal/bootstrap/arranque/identity_verifier_test.go:17`); `TokenDe(usuario, system string) string` con `identity-shared/auth/jwt.NewManager(priv, "identity-core", kid)` (`manager.go:40,139`); variantes `TokenCaducado`, `TokenDeOtroEmisor` |
| `servidor_test.go` | T9.8 | `arrancar(t, opciones) *servidor`: base propia, cuatro puertos libres, entorno **desde cero** (§2), `exec.Cmd` del binario elegido, stdout+stderr a un búfer; espera `GET /healthz` = 200 y TCP a los otros tres (tope 30 s); `SIGTERM` + espera 15 s + `SIGKILL` en `t.Cleanup`; si el test falla, vuelca las últimas 200 líneas del log con `t.Log`. `servidor.Log()` para asertar líneas |
| `clientes_test.go` | T9.9 | `servidor.HTTP(token)` (JSON, `Authorization: Bearer`), `servidor.Admin(token)`, `canjear(t, s, identityToken) contextToken` por `POST /api/v1/auth/exchange`; rechaza en el cliente `PUT /api/v1/tenant-llm` con `via=api` (cero gasto, R9.3.e) |
| `fixtures_test.go` | T9.9 | SQL de arranque **solo donde no hay puerta HTTP**: `altaStaffPlataforma(db, usuario)` (`tenant_members` en `55550000-…0055` + `iam_user_roles` con el rol `10000000-…0004`, lo mismo que el runbook de alta de staff del ecosistema); `ventanaInmediata(db, tenant)` (`tenant_settings.aggregation_window_seconds = 0`, `aggregation_max_seconds = 0`); `envejecer(db, tabla, id, intervalo)` para el vencimiento. Cada fixture dice en su comentario **por qué** no hay puerta |
| `edge_falso_test.go` | T9.10 | §3.3 |
| `guion_test.go` | T9.17 | §3.4: las respuestas del «modelo» por etapa |
| `crmfalso_test.go` | T9.19 | §3.5 |
| `p0_arranque_test.go` … `p9_diagnostico_test.go` (`p10_plataforma_test.go` si D-F9-4) | T9.11, T9.13–T9.21, T9.35 | Un fichero por proceso (§4) |
| `<paquete>_contrato_test.go` | F1 · T1.13 (`contact`) · T9.22–T9.29 | Suites de contrato contra el adaptador Postgres (§5) |

## 2 · El entorno del servidor (construido desde cero)

`cmd.Env` = `PATH`, `HOME`, `TMPDIR` del proceso de test **más** esta lista. Nada más: ni
`os.Environ()` (un `set -a; . ./.env` del desarrollador, `documentations/operacion.md` §2.2, metería
`WAPP_DB_*` de otra base), ni `WAPP_CONFIG_FILE` (el YAML pisaría los defaults: `config.go:730`).

| Variable efectiva | Valor | Por qué |
|---|---|---|
| `WAPP_APP_ENV` | `dev` | Como UAT (`documentations/operacion.md` §6). En `prod` exigiría más claves y permisos de fichero |
| `WAPP_LOG_LEVEL` · `WAPP_LOG_JSON` | `debug` · `true` | Líneas parseables para las aserciones de log |
| `WAPP_HTTP_ADDR` · `WAPP_PUBLIC_HTTP_ADDR` · `WAPP_GRPC_ENROLL_ADDR` · `WAPP_GRPC_CONNECT_ADDR` | `127.0.0.1:<libre>` ×4 | El arnés abre `net.Listen("tcp","127.0.0.1:0")`, lee el puerto y lo cierra; si el servidor muere con `address already in use`, reintenta una vez con puertos nuevos |
| `WAPP_DB_HOST` · `…PORT` · `…USER` · `…PASSWORD` · `…NAME` · `…SSLMODE` | `ctr.Host`, `ctr.MappedPort("5432/tcp")`, `wapp`, (el del contenedor), `proc_<p>_<binario>`, `disable` | La única base válida es la del contenedor de la corrida |
| `WAPP_PKI_CA_CERT_FILE` · `…CA_KEY_FILE` · `…SERVER_CERT_FILE` · `…SERVER_KEY_FILE` | rutas de `nuevaPKI` | |
| `WAPP_LEASE_PRIVATE_KEY_B64` | semilla Ed25519 | Sin ella, clave efímera por arranque (`lease.go:30-45`) |
| `WAPP_CLOUD_ENC_PRIVKEY_B64` | X25519 | Sin ella, efímera (`pki.go:88-121`) |
| `WAPP_KEK_PROVIDER` · `WAPP_KEK_MASTER_B64` · `WAPP_KEK_INDEX_B64` | `env` · aleatoria · aleatoria | El KMS no se toca (UAT tampoco lo usa) |
| `WAPP_JWT_EC_PRIVATE_KEY_FILE` · `WAPP_JWT_KID` | fichero `0600` · `procesos-1` | |
| `WAPP_IDENTITY_JWKS_URL` | `http://127.0.0.1:<p>/.well-known/jwks.json` | Abre el canje; el fetch es eager (`auth.go:148-151`) |
| `WAPP_STORAGE_S3_ENDPOINT` · `…BUCKET` · `…REGION` · `…ACCESS_KEY_ID` · `…SECRET_ACCESS_KEY` | `http://127.0.0.1:<p>` · `procesos` · `us-east-1` · valores de relleno | Endpoint **IP** ⇒ path-style (D-F9-2) |
| `WAPP_WEBHOOK_POLL_INTERVAL` · `WAPP_WEBHOOK_MAX_ATTEMPTS` | `200ms` · `2` | Solo P6 (opción de `arrancar`) |
| **No se ponen** | `WAPP_IDENTITY_URL`, `WAPP_IDENTITY_API_KEY`, `WAPP_LLM_PROMPTS_DIR`, `WAPP_KEK_KMS_*`, `WAPP_CONFIG_FILE` | Sin identity-api: 503 en `signup` y `POST /members` (diseñado). Prompts compilados |

## 3 · Los dobles

### 3.1 · S3 (D-F9-2, D-F9-3)

El servidor hace **una** llamada de red a S3 en toda su vida: `HeadBucket` al arrancar
(`r2_factory.go:54` → `:63`). Las URLs prefirmadas (`POST /api/v1/media/upload-url`, nodos media) se
**firman** en local (`presign.go`), sin red. El doble responde `HEAD /procesos` → 200 y P0 comprueba
que llegó **por path-style** (`Host` = `127.0.0.1:<p>`, ruta `/procesos`). Si T9.11 ve que el SDK pide
`procesos.127.0.0.1` en vez de path-style, **se para** y vuelve a Jhoan (D-F9-2).

### 3.2 · Identity

JWKS en loopback por HTTP (identity-shared rechaza `http` fuera de loopback: `jwks.go:187`). Los
Identity Tokens llevan `iss=identity-core`, `sub=<uuid de usuario>`, `system` ∈ {`wapp.bff`,
`wapp.edge`, `wapp.platform`} (`exchange.go:24-33`). El canje (`POST /api/v1/auth/exchange`) los
cambia por un Context Token ES256 firmado por el servidor, que es lo que usan todas las rutas.

### 3.3 · El Edge de prueba (`edge_falso_test.go`)

Referencia de lectura (no se importa): `cmd/server/integration_test.go:185-360` (`enroll`,
`connect`, `edgeSim`). Contrato:

| Método | Qué hace | Frames |
|---|---|---|
| `enrolar(t, s, codigo) *edge` | Genera clave P-256 y CSR, llama `Enrollment/EnrollEdge` en `:8102` con TLS de servidor (raíz = CA de la PKI, `ServerName=localhost`) | Guarda `edge_cert_pem`, `ca_chain_pem`, `tenant_id`, `cloud_enc_pubkey`, `lease_pubkey` |
| `conectar(t, sesion)` | Abre `CloudLink/Connect` en `:8101` con `mtls.ClientCreds(cert, pool, "localhost")`; lanza el bucle `Recv` | — |
| `latir(n)` | `Heartbeat{lease_counter:n, state, inference_readiness: READY}` | La disponibilidad READY es condición para recibir inferencia (`internal/gateway/grpc/readiness.go:132-142`, `inference.go:439-450`) |
| bucle `Recv` | `LeaseUpdate` → `cllease.Validator.Apply`; `SendText` → `Ack{ok}` y lo publica en un canal; `InferenceRequest` → §3.4; `ConfigPush` y peticiones de diagnóstico → las registra (P9) | Serializa `Send` con un mutex (gRPC no admite `Send` concurrentes) |
| `entrante(de, texto, waID)` | `IncomingMessage` **sellado**: `SensitivePayload` marshalado y `envelope.SealFor(cloud_enc_pubkey)` en `enc_payload`, planos sensibles vacíos | Como el Edge real (proto: «si va, los planos sensibles viajan vacíos»). El camino en claro es compatibilidad (`connect.go:502`) y no se usa |
| `acuse(waID, delivered/read)` · `bundle(cmd)` | `Receipt` · `DiagnosticsBundle` | P3 · P9 |
| `puedeOperar()` · `revocado()` | Lo que dice el `Validator` | P1 |

### 3.4 · El «modelo»: el guion de inferencia (`guion_test.go`)

- El prompt llega **en claro** (`InferenceRequest.prompt`, `inference.go:472`); la salida vuelve
  **sellada**: `InferenceOutput{raw_json}` marshalado y `SealFor(cloud_enc_pubkey)` en `enc_output`
  (la nube abre con su privada: `inference.go:553-590`).
- `class` **no** dice la etapa: es un rótulo de telemetría (`interactivo`|`lote`,
  `inference.go:281-283`). El guion identifica P1–P5 por un **marcador del texto del prompt**
  construido por `wapp-shared/llm` (`Build…Prompt`, v0.4.5) o por el techo `max_output_tokens`, que
  es distinto por etapa (`internal/llmvia/local/local.go`, «techo por tarea»). **Sin medir** cuál es
  más estable: T9.17 lo decide leyendo `prompt.go` y lo deja escrito en el comentario del guion.
- Las respuestas se toman de los guiones de los tests viejos (E-8): `internal/intake/pipeline/guion_ambar_test.go`,
  `internal/intake/stages/p2_test.go`, `p4_test.go`, `internal/intakes/quotetext/dobles_test.go`. 🔴 Cada
  JSON debe pasar el validador de su etapa: el esquema nunca lleva un valor que su validador rechace
  (I-CP-1; P4 fue 0 de 14 en campo por un `"package_size": 0`).
- Modos por proceso: `guion.Responder(etapa, json)`, `guion.Fallar(etapa, INFERENCE_ERROR_…)` (P4
  degradación), `guion.Tardar(etapa, d)` (P5, plazo propio de `quote-suggestion`).

### 3.5 · El CRM (`crmfalso_test.go`)

`httptest.Server` en loopback (la URL del tenant admite `http`, `internal/publicapi/integrations.go:338`).
Verifica `X-Wapp-Signature: v1=…` con HMAC-SHA256 sobre el cuerpo crudo y el secreto del tenant
(`internal/integrations/sigv1/sigv1.go:39`), valida el JSON contra
`docs/contracts/wapp-crm-v1/` (leído del repo), y responde lo que el proceso le pida (2xx, 500).
Para el callback, **firma** como lo haría el puente (`X-Wapp-Tenant`, `X-Wapp-Timestamp`,
`X-Wapp-Signature`: `internal/publicapi/crmcallback.go:22`).

## 4 · Los procesos (propuesta cerrada para D-13)

Convenciones: «admin» = Context Token de `tenant_admin` del tenant del proceso; «staff» = de
`platform_admin` (fixture §1). Cada proceso crea **su** tenant por `POST /admin/tenants`. Todos
terminan comprobando que el log del servidor no tiene líneas `level=ERROR` inesperadas.

### P0 · Humo del arranque (`p0_arranque_test.go`) — T9.11

- **Entra por**: el arranque del binario; `GET :8100/healthz`, `GET :8100/metrics`, `GET :8103/healthz`.
- **Aserta**: `healthz` 200 con Postgres `healthy`; `:8103/healthz` **404** (no tiene sonda,
  `contratos.md` §3); las **9** líneas `arranque: fase completada` en orden (`orquestador.go:117-123`);
  `migraciones aplicadas … skipped=true` (la base clonada ya está al día); las dos líneas de clave
  (el chequeo §9 del despliegue de UAT): lease `key_source=base64` y nube `key_source=config` ✎ medido en F9-01,
  contradicción 11 del README; el doble de S3 recibió `HEAD /wapp-procesos` (el bucket que crea `arrancar`);
  los nombres `wapp_*` que salen sin tráfico ✎ **9**, no 17 (contradicción 12 del README: los `CounterVec`
  sin incremento **no** aparecen y la lista medida está en `p0MetricasSinTrafico`); `SIGTERM` → `servidor detenido
  limpiamente` y código 0.
- **Candados**: ninguno. **Suites**: ninguna. **Viejos (E-8)**: `internal/bootstrap/arranque/orquestador_test.go`,
  `pool_metrics_integration_test.go`, `internal/platform/metrics/metrics_test.go:48-49` (`wapp_auth_logins_total` no debe aparecer).

### P1 · Enrolamiento de un Edge y su lease (`p1_enrolamiento_test.go`) — T9.13

1. staff: `POST /admin/tenants` (`slug`, `display_name`, `plan_id`; `internal/platformadmin/handlers.go:176`) → id.
2. staff: `POST /admin/tenants/{id}/enrollment-codes` → `code`, `expires_at`.
3. Edge: `EnrollEdge(code, csr)` → certificado, cadena, `tenant_id`, `cloud_enc_pubkey`, `lease_pubkey`.
4. Edge: `Connect` + latido → `LeaseUpdate` que el `Validator` acepta (`puedeOperar()`); `ConfigPush`
   `kind=jwks` recibido al conectar (`fase2_autenticacion.go`, config global del emisor).
5. admin: `POST /admin/leases/revoke` (`leases.revoke`) → el Edge recibe revocación; `puedeOperar()` falso.
6. staff: `POST /admin/tenants/revoke` → corte comercial; `POST /admin/tenants/restore` → vuelve.
- **Negativos**: el mismo código dos veces → error; `Connect` con un certificado de **otra** CA → falla
  el handshake; admin de cliente contra `POST /admin/tenants/revoke` → 403.
- **Postgres**: `tenants` (`revoked_at`), `enrollment_codes`, `edge_certs`, `leases`, `fleet_sessions`, `audit_events`.
- **Candados**: 🔒 `lease` es la mitad servidora de la doble llave (ADR-0007, fuera del repo: *el Lease
  lo emite y revoca el servidor y es el kill-switch anti-clon; la DEK la custodia el cliente y nunca
  cruza*): el proceso comprueba emisión, renovación por latido y revocación, y que ningún frame del
  servidor pide ni transporta material de la DEK.
- **Suites (9C · edge)**: `enroll` (códigos, certificados), `lease` (repositorio), `fleet` (repositorio).
- **Viejos (E-8)**: `cmd/server/integration_test.go` (`TestInProcessEnrollConnectLeaseSendRecv`),
  `internal/gateway/enroll/integration_test.go`, `internal/gateway/lease/integration_test.go`,
  `internal/gateway/fleet/*integration_test.go` (4), `internal/bootstrap/arranque/lease_test.go`, `pki_test.go`.

### P2 · Canje de identidad y permisos (`p2_canje_test.go`) — T9.14

1. Usuario sin membresías: canje → token **sin empresa y sin grants**; cualquier ruta de negocio → 403.
2. Staff crea tenant; alta de su administradora (fixture de membresía + rol `tenant_admin`, o invitación
   si la crea el staff) → canje → token con empresa; `GET /api/v1/auth/whoami`, `GET /api/v1/auth/tenants`.
3. admin: `POST /api/v1/invitations` → invitado canjea `POST /api/v1/invitations/accept` → membresía.
   **Concurrencia**: 8 `accept` simultáneos de la misma invitación → **1** éxito, **1** fila.
4. Usuario con **dos** empresas y sin activa → token sin empresa (no elige por él); `POST
   /api/v1/auth/active-tenant` → nuevo canje con esa empresa.
5. RBAC: `viewer` → `POST /api/v1/roles` 403; admin → 201; grants por usuario
   (`POST /api/v1/members/{id}/grants`).
6. 🔴 I-CP-5: con token de admin **de cliente**, las **10** rutas de plataforma de
   `internal/bootstrap/arranque/rutas_admin.go:76-94` → **403**; con staff → no 403.
7. `POST /api/v1/members` → **503**, nunca 404 (sin M2M, `contratos.md` §2.6).
- **Negativos**: token caducado, de otro emisor, de `system` no aceptado → canje rechazado.
- **Postgres**: `tenant_members`, `iam_roles`, `iam_user_roles`, `iam_role_grants`, `iam_user_grants`,
  `tenant_invitations`, `user_active_tenant`, `audit_events`.
- **Candados §3.2**: `internal/iam/infra/postgres/{canje_orden,canje_una_consulta,membresia_unica}_ast_test.go`
  (se **leen** y su regla pasa a aserción: paso 3 y 4); `platform_permissions_test.go` (paso 6).
- **Suites (9C · acceso)**: repositorios de `iam/ports/out` contra `iam/infra/postgres`, `entitlements`, `platformadmin`.
- **Viejos (E-8)**: `internal/iam/infra/postgres/*_integration_test.go` (5), `internal/iam/usecase/membership_integration_test.go`,
  `internal/iam/transport/http/canje_internal_test.go`, `internal/platformadmin/*_test.go` (5 con BD),
  `internal/bootstrap/arranque/{invitaciones,roleplane}_cableado_test.go`.

### P3 · Del entrante a la respuesta (`p3_entrante_test.go`) — T9.15

1. Tenant + Edge conectado (helpers de P1); admin: `POST /api/v1/sessions/{id}/profile` (perfil activo;
   dispara push de filtros al Edge), `POST /api/v1/flows` (un menú), `POST /api/v1/triggers`.
2. Edge: `entrante` sellado → el servidor responde `SendText` con el menú → `Ack`; segundo entrante con
   la opción → `SendText` del nodo destino.
3. El **mismo** `wa_message_id` otra vez → **ninguna** respuesta (dedupe).
4. `acuse(read)` → fila en `message_receipts`; `wapp_receipts_total` sube en `/metrics`.
5. Perfil **pasivo**: entrante → **ninguna** auto-respuesta; el aviso que recibe el Edge es,
   byte a byte, el literal `AVISO_SESION_PASIVA_V1` leído de `documentations/literal-aviso-sesion-pasiva.md`
   (🔒 contrato congelado: se lee, no se copia al test).
- **Postgres**: `fleet_sessions`, `flow_definitions`, `flow_triggers`, `flow_state`, `contacts` (el teléfono
  **no** aparece en claro: se busca el literal en la fila y no está), `ingest_dedupe`, `message_receipts`,
  `flow_events`, `conversation_events`.
- **Candados §3.2**: el literal del aviso (`internal/gateway/grpc/greeting_internal_test.go`) como conducta observable.
- **Suites**: `nucleo/contact` (F1), `flujos/store`, `flujos/trigger`, `ingest`, `receipts`, `flujos/events`, `flujos/runtime` (resolver de tenant).
- **Viejos (E-8)**: `cmd/server/flows_integration_test.go`, `internal/publicapi/{terminalflow_ola6,passiveprofile_o5,conversationchain_w45}_e2e_integration_test.go`,
  `internal/flujos/runtime/exit_menu_e2e_test.go`, `internal/ingest/integration_test.go`, `internal/receipts/integration_test.go`,
  `internal/flujos/contact/*_integration_test.go` (3).

### P4 · De mensaje a borrador (`p4_borrador_test.go`) — T9.17

1. Tenant con un plan que incluya `llm_intake` y `cart_basic` (plan sembrado por migración: se elige en
   T9.17 leyendo `0039`/`0074`); catálogo por `POST /api/v1/catalog/import`; `ventanaInmediata`
   (fixture); Edge conectado y READY.
2. Edge: ráfaga de 3 entrantes → fila en `intake_jobs` → el barrido cierra la ventana (≤ 5 s) → el
   worker (≤ 5 s) pide P2, P3, P4 al Edge → el guion responde → match contra el catálogo → borrador.
3. `GET /api/v1/intakes` → la solicitud; `GET /api/v1/intakes/{id}` → líneas casadas con el catálogo.
4. Degradación: `guion.Fallar(P2, OLLAMA_DOWN)` → fila en `owner_degradation_notices`,
   `GET /api/v1/degradation-notices`, `wapp_llm_degradacion_total` sube.
- **Postgres**: `intake_jobs`, `intakes`, `intake_items`, `intake_revisions` (el literal del cliente **no**
  en claro: migración `0079`), `flow_events` (`intake_draft_created`), `owner_degradation_notices`.
- **Espera**: sondeo de Postgres cada 250 ms con tope de 60 s; nunca `time.Sleep` fijo.
- **Suites**: `intake` (cola y máquina), `intakes`, `degradation`, `tenantllm`, `casebank`, `intentcfg`.
- **Viejos (E-8)**: `internal/intake/*_integration_test.go` (5), `internal/intake/pipeline/guion_ambar_test.go`,
  `internal/intake/stages/{p2,p4,audio_jamas_al_llm}_test.go`, `internal/flujos/runtime/{source_composer,intent_signal_persist}_integration_test.go`,
  `internal/flujos/store/aggregation_window_integration_test.go`, `internal/flujos/runtime/inv10_worker_pipeline_test.go`.

### P5 · La bandeja de la dueña (`p5_bandeja_test.go`) — T9.18

Sobre un borrador creado con el helper de P4:
`PUT /api/v1/intakes/{id}/items` (revisión nueva, `intake_line_corrected`) ·
`POST …/request-info` (el Edge recibe el `SendText` al cliente; `intake_info_requested`) ·
`POST …/quote-suggestion` con `guion.Tardar(P5, 12s)` → responde **después** de los 10 s del
`WriteTimeout` global (su plazo propio, `contratos.md` §2.5) · `POST …/approve` **dos veces** → un solo
efecto (INV-1) · `POST /api/v1/intakes/discard` · `GET /api/v1/intakes/export` y `summary.json`
(feature `intakes_export`) · vencimiento: `envejecer` + lectura → recordatorio perezoso
(`intakes.expiry_reminded`, migración `0081`).
- **Candados §3.2**: `internal/intakes/{inv1_aprobar,inv1_pedirinfo,inv_vencimiento,sello_poda}_ast_test.go` (se leen; INV-1 y
  la poda de revisiones pasan a aserción).
- **Suites (9C · solicitudes)**: `intakes` (Postgres y memoria), `buyerdata`, `tenantvars`.
- **Viejos (E-8)**: `internal/intakes/*_integration_test.go` (14) y `approve_test.go`, `vencimiento_*_test.go`,
  `sello_poda_test.go`; `internal/publicapi/ownerclose_o5_e2e_integration_test.go`.

### P6 · CRM (`p6_crm_test.go`) — T9.19

`arrancar` con sondeo 200 ms y 2 intentos. admin: `PUT /api/v1/integrations` (`crm_bridge`;
`endpoint_url` = CRM falso; secreto) → aprobar una solicitud → fila en `webhook_outbox` → `intake.push`
llega firmado y valida contra el esquema → entregado; CRM responde 500 → reintento → agotado;
`GET /api/v1/integrations/outbox`. Callback: `POST /api/v1/integrations/callback` firmado → la solicitud
refleja el estado; firma mala → 401; timestamp a ±301 s → rechazo (ventana ±300 s); cuerpo > 64 KiB →
rechazo. `catalog.pull` → **422** «catalog.pull diferido» (`internal/publicapi/integrations.go:307`).
- **Postgres**: `tenant_integrations` (secreto **no** en claro), `webhook_outbox` (payload purgado tras
  entregar: `0050`), `intakes` (reflejo CRM, `0048`).
- **Candados §3.2**: `internal/integrations/crmpush/contrato_ast_test.go` (los campos del contrato como
  aserción sobre lo **recibido**).
- **Suites**: `integrations` (outbox, secretos).
- **Viejos (E-8)**: `internal/integrations/*_integration_test.go` (4), `crmpush/push_test.go`,
  `internal/publicapi/crmcallback_e2e_integration_test.go`, `internal/intakes/crm_integration_test.go`,
  `internal/contracts/contract_examples_test.go`, `internal/flujos/runtime/webhook_sink_integration_test.go`.

### P7 · Catálogo (`p7_catalogo_test.go`) — T9.20

`GET /api/v1/catalog/import/template` y `…/prompt` · `POST /api/v1/catalog/import` estricto (400/422 con
la línea que falla) · `POST …/tabular` · tope `WAPP_IMPORT_MAX_ITEMS` (500) superado → rechazo · sin
feature `catalog_import` → 403 · versión nueva en `tenant_content_versions` · la caché del índice ve el
catálogo nuevo: una ráfaga (helper de P4) casa contra la versión recién importada ·
`POST /api/v1/media/upload-url` → URL prefirmada **path-style** contra el endpoint del doble.
- **Suites**: `flujos/store` (contenido del tenant). **Viejos (E-8)**: `internal/catalogimport/*_test.go` (5),
  `internal/publicapi/catalog*_test.go`, `internal/flujos/store/tenant_content*_integration_test.go`,
  `internal/intake/catalogo/*_test.go`.

### P8 · Re-análisis (`p8_reanalisis_test.go`) — T9.21

Sobre un borrador de P4: `POST /api/v1/intakes/{id}/reanalyze` → fila en `intake_jobs` de tipo
re-análisis (`0080`) → el guion responde → **revisión 2** y `intake_reanalyzed` en `flow_events`; un
segundo `reanalyze` con el primero pendiente → rechazo (la puerta del re-análisis); sobre una aprobada →
rechazo.
- **Viejos (E-8)**: `internal/reanalisis/reanalisis_test.go`, `internal/intake/reanalisis_internal_test.go`,
  `internal/bootstrap/arranque/reanalisis_cableado_test.go`.

### P9 · Diagnóstico remoto y configuración empujada (`p9_diagnostico_test.go`) — T9.16

Consentimiento del tenant (`tenant_diagnostics_consent`; fixture si no hay puerta) →
`POST /api/v1/sessions/{id}/diagnostics` → el Edge recibe la petición y manda `DiagnosticsBundle` →
`GET /api/v1/diagnostics/{command_id}` lo devuelve; tras el TTL, no. `PUT /api/v1/intents` → el Edge
recibe `ConfigPush kind=intents`. Es el único tramo del gRPC (diagnóstico, push de config) que ningún
otro proceso recorre. **Suites**: `diagnostics`, `intentcfg`, `filtercfg`.
**Viejos (E-8)**: `internal/diagnostics/postgres_integration_test.go`, `internal/filtercfg/push_integration_test.go`,
`internal/intentcfg/store_integration_test.go`, `internal/gateway/grpc/config_push_internal_test.go`.

### P10 · Plataforma (solo si D-F9-4) (`p10_plataforma_test.go`) — T9.35

Re-expresa los 9 ficheros / 25 `Test*` de BD de `internal/platform/`: réplica completa de migraciones
sobre un clon (idempotente: `cmd/migrate` dos veces → `skipped=true`), grants de las migraciones
(`0060`, `0084`), rotación de KEK por `POST /admin/crypto/rekey` (`crypto.rekey`) y lectura posterior
de un contacto cifrado, y el colector de `flow_events` visto en `/metrics`. Es lo que permite a F10
borrar `make test-integration` y dejar el repo sin `WAPP_TEST_DB_DSN`.

## 5 · Las suites de contrato contra Postgres (H9.5)

Cada suite la crea **su fase** (E-3/E-6: `func Contrato(t *testing.T, nuevo func() Puerto)` en
`<paquete>test`); F9 solo la **ejecuta** contra el adaptador Postgres, con una base clonada por
subtest. «Memoria» = implementación en memoria hoy (medido con
`grep -rn '^func NewMemory' --include='*.go' internal | grep -v _test` y los `memory*.go`).

| Paquete viejo con SQL | Módulo (fase) | Memoria hoy | Tarea 9C |
|---|---|---|---|
| `flujos/contact` | `nucleo` (F1) | sí | T9.22 (o F1 · T1.13) |
| `iam/infra/postgres` | `acceso` (F2) | sí (`iam/infra/memory`) | T9.23 |
| `entitlements` · `platformadmin` | `acceso` (F2) | **sí** (`Fake`, `entitlements.go`; D-F2-4 lo muda a `entitlementstest`) · no | T9.23 |
| `gateway/enroll` · `gateway/fleet` · `gateway/lease` | `edge` (F3) | **sí** (`NewMemory…`) ×3 | T9.24 |
| `diagnostics` · `ingest` · `receipts` | `edge` (F3) | sí · sí · sí | T9.24 |
| `tenantllm` · `degradation` | `inferencia` (F4) | no · no | T9.25 |
| — (el catálogo no tiene SQL propio: vive en `flujos/store`) | `catalogo` (F5) | — | T9.26 (solo la suite entera contra el nuevo) |
| `intakes` · `integrations` · `tenantvars` | `solicitudes` (F6) | sí · no · sí | T9.27 |
| `intake` · `casebank` · `intentcfg` | `captacion` (F7) | sí · no · **sí** (`store.go:53`) | T9.28 |
| `flujos/store` · `flujos/trigger` · `flujos/events` · `flujos/runtime` | `conversacion` (F8) | sí · sí · no · no | T9.29 |

22 paquetes con SQL (`05` E-6 dice 20). Los 12 «sin gemelo» de `05` son en realidad **5**
(`platformadmin`, `tenantllm`, `degradation`, `integrations`, `casebank`) **más** los dos que `05` no
contó (`flujos/events`, `flujos/runtime`): **7**. La cifra y su comando, en
[`../00-marco/tecnologia.md`](../00-marco/tecnologia.md) §9.

## 6 · El candado `sin_bd_viva_test.go`

Nace en F0 (T0.8, patrones de su `diseno.md` §4.4: `WAPP_TEST_DB_DSN`, literales con `:5432`,
`WithReuseByName`, literales `postgres://`/`postgresql://`). F9 (T9.3) añade dos patrones, con su caso
`muerde` en `internal/candados/testdata/sinbdviva/`:

| Patrón nuevo | Motivo |
|---|---|
| `os.Environ()` | El servidor hereda el entorno del shell: un `.env` exportado apuntaría a otra base (§2) |
| `t.Skip` / `t.SkipNow` / `testing.Short()` | E-5: nunca un SKIP en código nuevo; un proceso que no puede correr **falla** |

## 7 · `make test-procesos` (T9.4)

```make
test-procesos: ## Procesos (F9) contra los DOS binarios, testcontainers; necesita Docker
	@fallo=0; for b in $${BINARIO:-viejo nuevo}; do \
	  L=/tmp/procesos-$$b.log; \
	  WAPP_PROCESOS_BINARIO=$$b $(GO) test -tags integracion -count=$${CUENTA:-1} -v -timeout 30m -parallel 4 ./test/procesos/... > $$L 2>&1; \
	  rc=$$?; echo "RC=$$rc" >> $$L; [ $$rc -eq 0 ] || fallo=1; \
	  echo "$$b: RC=$$rc · PASS=$$(grep -c -- '--- PASS' $$L) FAIL=$$(grep -c -- '--- FAIL' $$L) SKIP=$$(grep -c -- '--- SKIP' $$L) · $$L"; \
	done; exit $$fallo
```

(Forma orientativa: el `rc` de cada binario se escribe en su log **antes** de seguir, y el target
sale ≠ 0 si cualquiera falló; T9.4 la prueba con un proceso que falla adrede. `BINARIO=viejo` o
`CUENTA=3` la acotan.) Más `vet-integracion` (`$(GO) vet -tags integracion ./test/procesos/...`)
dentro de `ci-local`, y `run.build-tags: [integracion]` en `.golangci.yml`.
