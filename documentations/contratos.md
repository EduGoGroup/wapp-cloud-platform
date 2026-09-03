# Contratos de `wapp-cloud-platform`

> Todo lo que otros consumen o le pasan. El esquema de PostgreSQL vive en el fichero hermano
> [`esquema-postgres.md`](esquema-postgres.md) porque no cabía aquí.

## Cómo se contó (léelo antes de fiarte de un número)

Las rutas HTTP **no están en un fichero**: están en **tres sitios**, y hay que mirar los tres.
El comando que se usa de arranque, sin tests (pero **no** produce la lista completa: lee el aviso de debajo):

```bash
grep -rn 'mux\.Handle' --include='*.go' internal/ | grep -v _test
```

🔴 **Ese comando no da el número de rutas, y le faltan cinco.** Devuelve **95 líneas**
(medido el 2026-08-30), pero coincidir con el total de patrones es casualidad: son dos conjuntos
distintos.

- **Sobran 5 de esas 95.** Tres son **comentarios** que contienen la cadena
  (`internal/bootstrap/bootstrap.go:1384`,
  `internal/platform/metrics/flowlifecycle/collector.go:8`,
  `internal/iam/transport/http/roles.go:28` — este último, en un fichero que la tabla ni
  menciona). Las otras dos son el `Register` de `internal/flujos/admin/handlers.go:345-346`, que
  **solo llaman tests**: esas dos rutas se montan inline en `bootstrap.go:1462,1464`, así que
  contarlas las duplicaría. Quedan **90 registros de producción**.
- **Faltan 5.** En `internal/bootstrap/http.go` la variable se llama `publicMux`, y
  `mux\.Handle` en minúscula **no acierta nunca ahí**
  (`grep -c 'mux\.Handle' internal/bootstrap/http.go` → **0**). Hay que grepear también
  `publicMux\.Handle`, que da **6 líneas** y **5 patrones**: `POST /api/v1/signup` se registra
  dos veces (`:165` y `:168`), en las dos ramas de un `if`.

**La regla para recontar**: un patrón cuenta si es un registro **real** (no un comentario), se
monta **en producción** (no solo desde un test) y se cuenta **una vez** aunque aparezca en dos
sitios. Con esa regla salen **95 patrones distintos**, repartidos así:

| Origen | Patrones | Listener |
|---|---|---|
| `internal/publicapi/publicapi.go` (51) + `roleplane.go` (14) + `eventstelemetry.go` (1) | 66 | público `:8103` |
| `internal/bootstrap/http.go` (5, por `publicMux.Handle`) + `internal/iam/transport/http/auth.go` (2) | **7** | público `:8103` |
| `internal/bootstrap/bootstrap.go` → `registerAdminRoutes` (`:1425-1476`) | 22 | admin `:8100` |

⚠️ En `bootstrap.go` el grep da **23** líneas, no 22: la de más es el comentario de `:1384`. Ver
`deuda.md`.

🔴 **Muchas rutas son CONDICIONALES.** Si su dependencia en `publicapi.Deps` es `nil`, la ruta
**no se monta** y responde un 404 de ruta inexistente (`internal/publicapi/roleplane.go:75`).
La única excepción deliberada es `POST /api/v1/members`, que se monta siempre y degrada a
**503**, nunca a 404.

---

## 1 · gRPC — 2 servicios, 2 métodos

Contrato: `github.com/EduGoGroup/wapp-cloudlink v0.17.0`, paquete `cloudlinkv1`.

| RPC | Tipo | Listener | Dónde se sirve |
|---|---|---|---|
| `cloudlinkv1.Enrollment/EnrollEdge` | unario | `:8102`, **TLS solo de servidor** | `internal/gateway/enroll/server.go:74` |
| `cloudlinkv1.CloudLink/Connect` | **bidi-stream** | `:8101`, **mTLS estricto** | `internal/gateway/grpc/server.go:362` |

Keepalive del servidor (`bootstrap.go:824`): `Time=30s`, `Timeout=10s`, `MinTime=15s`,
`PermitWithoutStream`.

**Mensajes `EdgeToCloud` que el servidor atiende** — 10, enumeración cerrada en
`internal/gateway/grpc/connect.go:211`:
`Incoming` · `Ack` · `InferenceResult` · `Heartbeat` · `Pong` · `Receipt` ·
`DiagnosticsBundle` · `UserLogin` · `UserRefresh` · `UserLogout`.

`Ack` e `InferenceResult` se resuelven **inline** en el bucle `Recv`; el resto se suelta a un
carril por sesión.

---

## 2 · HTTP — listener público `:8103`

### 2.1 · Autenticación y empresa (7 rutas)

Un token **sin empresa** las atraviesa: la cadena es `Authenticate` a secas, sin
`RequirePermission`. **Son las 7 que un inventario que solo mire `publicapi` se deja.**

| Método + ruta | Registro | Nota |
|---|---|---|
| `/api/v1/auth/verify` | `internal/iam/transport/http/auth.go:49` | sin verbo → cualquier método |
| `/api/v1/auth/exchange` | `auth.go:50` | canje Identity Token → Context Token |
| `/api/v1/auth/whoami` | `internal/bootstrap/http.go:84` | |
| `POST /api/v1/invitations/accept` | `http.go:115` | canje de invitación de un solo uso |
| `POST /api/v1/auth/active-tenant` | `http.go:144` | elegir empresa |
| `GET /api/v1/auth/tenants` | `http.go:153` | listar las empresas propias |
| `POST /api/v1/signup` | `http.go:165` / `:168` | **público**. Sin `WAPP_IDENTITY_API_KEY` se cablea a un **503 fijo**; con ella, rate-limit 1/60 s con ráfaga 5 |

### 2.2 · Mensajería y flujos

| Ruta | Scope | Línea (`publicapi.go`) |
|---|---|---|
| `POST /api/v1/messages` | `messages.send` | `:443` |
| `POST /api/v1/flows` | `flows.create` | `:448` |
| `GET /api/v1/flows` · `GET /api/v1/flows/{id}` | `flows.read` | `:452` `:454` |
| `POST /api/v1/flows/{id}/start` | `flows.start` | `:460` |
| `POST /api/v1/triggers` · `GET /api/v1/triggers` · `DELETE /api/v1/triggers/{id}` | `triggers.{create,read,delete}` | `:490` `:492` `:494` |

### 2.3 · Sesiones, flota y diagnóstico

| Ruta | Scope | Línea |
|---|---|---|
| `GET /api/v1/sessions` | `sessions.read` | `:508` |
| `POST /api/v1/sessions/{id}/profile` | `sessions.write` | `:518` — dispara push de filtros al Edge |
| `POST /api/v1/sessions/{id}/status` | `sessions.write` | `:547` |
| `POST /api/v1/sessions/{id}/diagnostics` | `diagnostics.request` | `:535` |
| `GET /api/v1/diagnostics/{command_id}` | `diagnostics.request` | `:538` |

### 2.4 · Contenido, catálogo y variables

| Ruta | Scope / gate | Línea |
|---|---|---|
| `PUT` · `POST` · `DELETE /api/v1/tenant-content/{ref}` | `content.write` | `:474` `:476` `:478` |
| `GET /api/v1/tenant-content` · `GET …/{ref}` | `content.read` | `:480` `:482` |
| `POST /api/v1/media/upload-url` | `media.upload` | `:467` |
| `POST /api/v1/catalog/import` · `…/tabular` | `content.write` + feature `catalog_import` | `:1102` `:1111` |
| `GET /api/v1/catalog/import/template` · `…/prompt` | `content.read` + `catalog_import` | `:1120` `:1122` |
| `GET` · `PUT /api/v1/tenant-variables` | `content.read` / `content.write` | `:881` `:883` |

### 2.5 · La bandeja (solicitudes) — todas con feature `cart_basic`

| Ruta | Scope | Línea |
|---|---|---|
| `GET /api/v1/intakes` · `GET /api/v1/intakes/{id}` | `intakes.read` | `:656` `:658` |
| `POST /api/v1/intakes/{id}/status` | `intakes.write` | `:660` |
| `PUT /api/v1/intakes/{id}/items` | `intakes.write` | `:670` |
| `POST /api/v1/intakes/{id}/approve` | `intakes.write` | `:686` |
| `POST /api/v1/intakes/{id}/request-info` | `intakes.write` | `:701` |
| `POST /api/v1/intakes/{id}/reanalyze` | `intakes.write` | `:742` |
| **`POST /api/v1/intakes/{id}/quote-suggestion`** | `intakes.read` + **`llm_intake`** | `:770` |
| `POST /api/v1/intakes/discard` | `intakes.write` | `:784` |
| `GET /api/v1/intakes/export` · `GET /api/v1/intakes/summary.json` | `intakes.read` + `intakes_export` | `:788` `:790` |
| `GET /api/v1/conversation-events` | `intakes.read` | `:845` |
| `POST /api/v1/conversation-events/{id}/cancel` | `intakes.write` | `:849` |
| `GET /api/v1/events/telemetry` | `events_telemetry.read` | `eventstelemetry.go:260` |

🔴 **`quote-suggestion` es la ÚNICA ruta con plazo de escritura propio** (`conPlazoDeRedacción`)
porque **espera al modelo dentro de la petición**: 24,8–35,5 s medidos en UAT, contra un
`WriteTimeout` global de 10 s. Si tocas el timeout global, mira antes esta ruta.

### 2.6 · Plano de roles y miembros (`roleplane.go`)

| Ruta | Scope | Línea |
|---|---|---|
| `GET /api/v1/roles` · `POST /api/v1/roles` | `roles.read` / `roles.write` | `:88` `:90` |
| `POST` · `DELETE /api/v1/roles/{id}/grants` | `roles.write` | `:96` `:98` |
| `POST /api/v1/members/{user_id}/roles` · `DELETE …/roles/{role_id}` | `roles.write` | `:104` `:106` |
| `POST` · `DELETE /api/v1/members/{user_id}/grants` | `roles.write` | `:113` `:115` |
| `GET /api/v1/members` | `members.read` | `:127` |
| `POST /api/v1/members` | `members.write` | `:132` — **sin M2M responde 503, nunca 404** |
| `DELETE /api/v1/members/{user_id}` | `members.write` | `:134` |
| `GET` · `POST /api/v1/invitations` · `DELETE /api/v1/invitations/{id}` | `members.read` / `members.write` | `:154` `:156` `:160` |

### 2.7 · Integraciones, LLM del tenant, intents, entitlements y auditoría

| Ruta | Scope / gate | Línea |
|---|---|---|
| `GET /api/v1/integrations` · `GET …/outbox` | `integrations.read` + `crm_bridge` | `:923` `:929` |
| `PUT` · `DELETE /api/v1/integrations` | `integrations.write` + `crm_bridge` | `:931` `:933` |
| **`POST /api/v1/integrations/callback`** | 🔴 **SIN JWT** | `:1055` |
| `GET` · `PUT` · `DELETE /api/v1/tenant-llm` | `llm.read` / `llm.write` + `api_llm` | `:986` `:988` `:990` |
| `GET /api/v1/degradation-notices` | `llm.read` + `llm_intake` | `:1030` |
| `GET` · `PUT /api/v1/intents` | `intents.read` / `intents.write` | `:571` `:573` |
| `GET /api/v1/entitlements` | `entitlements.read` | `:559` |
| `GET /api/v1/audit` | `audit.read` | `:604` |

🔴 **El callback del CRM no lleva JWT y no es un descuido**: se autentica por **HMAC-SHA256
sobre el cuerpo crudo** con el secreto del tenant, ventana anti-replay ±300 s, comparación en
tiempo constante (`internal/integrations/sigv1/sigv1.go:39`) y cuerpo con tope de 64 KiB.
Cabeceras: `X-Wapp-Tenant`, `X-Wapp-Timestamp`, `X-Wapp-Signature: v1=…`
(`internal/publicapi/crmcallback.go:22`).

---

## 3 · HTTP — listener admin `:8100` (22 rutas)

Cadena: `Authenticate → RequirePermission → AuditMiddleware → h`
(`adminHandler`, `internal/bootstrap/http.go:216`).

| Ruta | Permiso | Plano |
|---|---|---|
| `/healthz` · `/metrics` | — (sin auth) | — |
| `/admin/leases/revoke` | `leases.revoke` | tenant |
| `GET /admin/tenants` · `POST /admin/tenants` | `tenants.read.any` / `tenants.create.any` | **plataforma** |
| `GET /admin/tenants/{id}` | `tenants.read.any` | **plataforma** |
| `GET /admin/tenants/{id}/installations` | `fleet.read.any` | **plataforma** |
| `POST /admin/tenants/{id}/enrollment-codes` | `enrollment.issue.any` | **plataforma** |
| `POST /admin/tenants/revoke` · `POST /admin/tenants/restore` | `tenants.revoke.any` / `tenants.restore.any` | **plataforma** |
| `GET /admin/access-requests` · `POST …/{id}/approve` · `POST …/{id}/reject` | `users.provision.any` | **plataforma** |
| `/admin/messages/send` | `messages.send` | tenant |
| `/admin/crypto/rekey` | `crypto.rekey` | tenant |
| `/admin/flows` · `/admin/flows/start` | `flows.create` / `flows.start` | tenant |
| `POST /admin/triggers` · `GET /admin/triggers` · `DELETE /admin/triggers/{id}` | `triggers.{create,read,delete}` | tenant |
| `POST /admin/sessions/{id}/profile` · `POST …/{id}/status` | `sessions.write` | tenant |

🔴 El sufijo **`.any`** es lo único que separa los dos planos. Ver I-CP-5 en
[`constitucion.md`](constitucion.md).

⚠️ `/healthz` **solo existe aquí**. El listener público `:8103` **no tiene sonda de salud**
(verificado en UAT: 404).

⚠️ **El scrape de `/metrics` no es inocuo**: barre de paso las rachas de auto-respuesta
vencidas. Si nadie raspa, esos episodios no se cierran nunca (`bootstrap.go:790`).

---

## 4 · Comandos CLI

| Comando | Flags | Qué hace |
|---|---|---|
| `go run ./cmd/server` | — | El proceso. Migra al arrancar y levanta los 4 listeners |
| `go run ./cmd/migrate` | `-status` | Aplica el DDL y sale. `-status` solo consulta versión y hash |
| `go run ./cmd/prompts` | `-volcar <dir>` · `-comprobar <dir>` | Vuelca / valida los `.tmpl` de P2–P5 |
| `go run ./cmd/casebank` | `-tenant <id>` · `-consentido` | Siembra un caso en `intake_case_bank`. **Sin `-consentido` se niega** |

---

## 5 · Variables de entorno

`config.Load()` (`internal/platform/config/config.go:730`) lee **69 claves** por el loader
(`awk '/^func Load\(\)/,/^}/' internal/platform/config/config.go | grep -oE 'loader\.Get[A-Za-z]+\("[A-Z_0-9]+"' | sort -u | wc -l` → 69)
**más** `WAPP_CONFIG_FILE`, que va por `os.Getenv` (`:735`) y no pasa por el loader: **70 en
total**, que es la cifra que se cita en el resto de la documentación. **Precedencia: defaults →
YAML → entorno.**

🔴 **Nombre efectivo**: el loader compone el prefijo con `WithEnvPrefix(EnvPrefix)` y
`EnvPrefix = "WAPP_"` (`config.go:24`). El literal `HTTP_ADDR` del código es
**`WAPP_HTTP_ADDR`** en el entorno. Todas las de abajo van con ese prefijo.

⚠️ **El proceso NO lee ningún `.env` por sí mismo** (cero `godotenv` en el árbol). En UAT lo
inyecta systemd con `EnvironmentFile`; en local lo exportas tú.

### Con default (los que importan)

| Variable (`WAPP_…`) | Default | Qué gobierna |
|---|---|---|
| `APP_ENV` | `dev` | ambiente declarado |
| `HTTP_ADDR` · `PUBLIC_HTTP_ADDR` · `GRPC_ENROLL_ADDR` · `GRPC_CONNECT_ADDR` | `:8100` · `:8103` · `:8102` · `:8101` | los cuatro listeners |
| `GRPC_PUSH_TIMEOUT` · `GRPC_ACK_TIMEOUT` | `10s` · `8s` | empuje al Edge y espera del Ack |
| `GATEWAY_WORK_QUEUE` · `GATEWAY_WORK_TIMEOUT` | `64` · `5s` | el carril por sesión del gateway |
| `PUBLICAPI_DB_TIMEOUT` | `1.5s` | calibrado por la **suma**: 1,5 s + 8 s del Ack = 9,5 s, y cabe en el `WriteTimeout` de 10 s |
| `LOG_LEVEL` · `LOG_JSON` | `info` · `false` | |
| `PLATFORM_TENANT_ID` | `55550000-0000-0000-0000-000000000055` | el tenant del plano de plataforma; sin él quedaría cerrado a cal y canto |
| `DB_HOST` · `DB_PORT` · `DB_USER` · `DB_PASSWORD` · `DB_NAME` · `DB_SSLMODE` | `localhost` · `5432` · `wapp` · `wapp` · `wapp_cloud` · `disable` | |
| `DB_MAX_OPEN_CONNS` · `MAX_IDLE_CONNS` · `CONN_MAX_LIFETIME` · `CONN_MAX_IDLE_TIME` | `25` · `5` · `1h` · `10m` | referenciados de `postgres.Default*`, no copiados |
| `PKI_SERVER_CERT_FILE` · `…KEY_FILE` · `PKI_CA_CERT_FILE` · `…KEY_FILE` | `certs/server.crt` · `certs/server.key` · `certs/ca.crt` · `certs/ca.key` | la CA de enrolamiento |
| `KEK_PROVIDER` | **`env`** | `env` \| `kms`. El KMS está **construido y apagado** a propósito |
| `RATELIMIT_PUBLIC_RPS` · `PUBLIC_BURST` · `TRUST_PROXY` | `20` · `40` · `false` | |
| `FLOW_REPLY_RATE` · `REPLY_BURST` · `INCOMING_TIMEOUT` · `MAX_CONCURRENT_INCOMING` | `0.5` · `3` · `30s` · `64` | |
| `LLM_WARMUP_ENABLED` · `LLM_MAX_OUTPUT_TOKENS_ENABLED` | `true` · `true` | nacen **encendidos**: el apagado existe para el A/B de campo |
| **`LLM_PROMPTS_DIR`** | *(vacío)* | vacío ⇒ corren los prompts **compilados**. Ver I-CP-1 |
| `HEALTH_DEGRADED_AFTER` · `HEALTH_STALE_AFTER` | `5m` · `2m` | |
| `DIAGNOSTICS_BUNDLE_TTL` | `30m` | |
| `TENANT_CONTENT_MAX_BYTES` | `1 MiB` | |
| `IMPORT_MAX_ITEMS` | `500` | |
| `WEBHOOK_POLL_INTERVAL` · `MAX_ATTEMPTS` · `TIMEOUT` | `5s` · `10` · `10s` | el outbox del CRM |
| `STORAGE_S3_REGION` · `BUCKET` · `PRESIGN_EXPIRY` | `us-east-1` · `edugo-materials` · `15m` | |
| `JWT_ISSUER` | `wapp-cloud` | |
| `LEASE_TTL_MINUTES` | `<=0` ⇒ **15 min** (`internal/gateway/lease/lease.go:33 DefaultTTL`) | vigencia del lease, renovada en cada Heartbeat |

### Sin default (secretos y rutas; si faltan, el arranque falla o la función queda apagada)

`DB_PASSWORD` (tiene default de dev) · `LEASE_PRIVATE_KEY_FILE` · `LEASE_PRIVATE_KEY_B64`
(⚠️ **sin ninguna de las dos, la plataforma genera una clave de lease EFÍMERA en cada arranque
e invalida a todos los Edges al reiniciar**; no apta para producción) · `KEK_KMS_KEY` · `KEK_KMS_KEYRING` · `KEK_KMS_INDEX_B64` · `KEK_KEYRING` ·
`KEK_CURRENT` · `KEK_MASTER_B64` · `KEK_INDEX_B64` · `CLOUD_ENC_PRIVKEY_B64` ·
`STORAGE_S3_ACCESS_KEY_ID` · `STORAGE_S3_SECRET_ACCESS_KEY` · `STORAGE_S3_ENDPOINT` ·
`JWT_EC_PRIVATE_KEY_FILE` · `JWT_KID` · `IDENTITY_JWKS_URL` · `IDENTITY_URL` ·
`IDENTITY_TIMEOUT` · `IDENTITY_API_KEY` · `CONFIG_FILE`.

### Solo de tests

`WAPP_TEST_DB_DSN` · `WAPP_TEST_REQUIRE_DB` · `WAPP_PERF_ABSOLUTO`.

### 🔴 Configuración MUERTA

**`WAPP_STORAGE_S3_PREFIX`** está en `.env.example` y en los runbooks, y **no la lee nadie**:
`config.StorageConfig` no tiene campo `Prefix`. El prefijo real es la constante compilada
`mediaKeyPrefix = "wapp/media"` (`internal/publicapi/media.go:28`). Ver `deuda.md`.

⚠️ El `.env.example` declara 47 claves y `Load()` lee 70. No lo tomes como inventario.

---

## 6 · Ficheros que lee y escribe

**Escribe: uno solo, y solo el CLI.** `cmd/prompts -volcar` crea el directorio con
`os.MkdirAll(dir, 0o750)` y los ficheros con `os.WriteFile(ruta, …, 0o600)`
(`internal/prompts/volcar.go:52` y `:67`). **El servidor no escribe ningún fichero**:
`grep -rn 'os.WriteFile\|os.Create\|os.OpenFile' --include='*.go' internal/ cmd/` no devuelve
nada en el camino del servidor.

**Lee**: los cuatro `.tmpl` de `WAPP_LLM_PROMPTS_DIR` (opcional), el certificado y la clave
del servidor y de la CA (`WAPP_PKI_*`), la clave privada del lease
(`WAPP_LEASE_PRIVATE_KEY_FILE`), la clave EC del emisor de JWT
(`WAPP_JWT_EC_PRIVATE_KEY_FILE`) y, si existe, el YAML de `WAPP_CONFIG_FILE`.

**Embebido** (`go:embed`, no es I/O): los 84 `.sql` de
`internal/platform/storage/postgres/migrations/structure/`.

---

## 7 · Contrato `wapp-crm-v1`

Vive congelado en `docs/contracts/wapp-crm-v1/` (esquemas JSON + ejemplos). Sus ejemplos se
validan contra su schema en `internal/contracts/contract_examples_test.go` — un paquete Go que
**solo contiene ese test**.

Tres verbos, **dos implementados**: `intake.push` (wApp → puente, outbox durable + HMAC),
`intake.status` (puente → wApp, el callback de §2.7) y `catalog.pull`, que **responde 422**
con el texto «catalog.pull diferido» (`internal/publicapi/integrations.go:307`).

---

## 8 · Métricas Prometheus (`/metrics` en `:8100`)

17 nombres declarados estáticamente más 5 descriptores dinámicos.

**La regla para recontar**: se cuentan los **nombres declarados** en el código
(`grep -rnE 'Name:[[:space:]]*"wapp_' --include='*.go' internal/ | grep -v _test` → 18 líneas,
de las que **una no es una métrica**: `internal/platform/config/config.go:698` es el nombre por
defecto de la base de datos, `wapp_cloud`). **No** se cuentan las series que Prometheus deriva
solo de un histograma (`_bucket` / `_sum` / `_count`): existen en el cuerpo de `/metrics` pero
no están declaradas.

`wapp_http_requests_total` · `wapp_http_request_duration_seconds` · `wapp_ratelimit_hits_total` ·
`wapp_receipts_total` (`internal/platform/metrics/metrics.go:85`) ·
`wapp_webhook_deliveries_total` (`:93`) ·
`wapp_cart_match_total` · `wapp_flow_autoreply_streak` (histograma, `:109`) ·
`wapp_flow_autoreply_streak_max` (`:132`) ·
`wapp_flow_event_lifecycle_total` · `wapp_flow_reactive_blocked_total` ·
`wapp_llm_degradacion_total` ·
`wapp_db_{max_open,in_use,idle,wait_count,wait_duration_seconds,max_idle_closed}`.

🔴 **`wapp_auth_logins_total` YA NO EXISTE** y no debe volver a esta lista: se retiró con el
login (identity Plan 003 · Ola 5), lo dice `internal/platform/metrics/metrics.go:201` y hay un
test que **falla si la cadena aparece** en el cuerpo de `/metrics`
(`internal/platform/metrics/metrics_test.go:48-49`).

Por `NewDesc` (`internal/platform/metrics/inferstats.go:69`):
`wapp_edge_inference_by_regime_total` · `…by_class_total` · `wapp_edge_intent_omitted_total` ·
`wapp_edge_inference_samples_total` · `wapp_edge_inference_reporting_edges`.

⚠️ Un `CounterVec` **no aparece en `/metrics` hasta su primer incremento**, y el reinicio lo
borra. Su ausencia tras desplegar no prueba nada.

---

## 9 · Eventos de telemetría escritos en `flow_events`

Cinco, y la lista está cerrada por diseño: `intake_draft_created`
(`internal/intake/stages/draft.go:90`), `intake_reanalyzed` (`draft.go:107`),
`intake_line_corrected` · `intake_approved` · `intake_info_requested`
(`internal/intakes/metricas.go:55`, `:57`, `:59`).
