# Contratos de `wapp-cloud-platform`

> Todo lo que otros consumen o le pasan. El esquema de PostgreSQL vive en el fichero hermano
> [`esquema-postgres.md`](esquema-postgres.md) porque no cabía aquí.

## Cómo se contó (léelo antes de fiarte de un número)

Todas las cifras de este documento se midieron el **2026-10-11** contra el código, y cada una
lleva su regla y su comando. Este documento es el **dueño** de cuatro cifras —rutas HTTP, rpc,
comandos CLI y variables de entorno— y de la lista de métricas: el resto de la documentación
las enlaza, no las repite.

**Rutas HTTP: 95 = 22 en `:8100` + 73 en `:8103`.**

**La regla**: se cuenta cada **patrón registrado en ejecución** en el `http.ServeMux` de cada
listener, una vez. No se cuentan líneas de código, ni comentarios, ni registros que solo alcanza
un test. La verdad de campo es la dorada del arranque,
`internal/arranque/testdata/huella-vieja.golden`: una línea por patrón, medida arrancando el
proceso en dos perfiles (`con-m2m` y `minimo`), que dan **las mismas 95**.

```bash
# Lo que el proceso registra, por listener (la dorada):
awk -F'\t' '$1=="con-m2m" && $2=="ruta" {print $3}' \
  internal/arranque/testdata/huella-vieja.golden | sort | uniq -c        # 22 :8100 · 73 :8103

# Lo que el código registra, para cruzarlo:
grep -oE 'mux\.Handle\("[^"]+"' internal/arranque/rutas_admin.go | sort -u | wc -l          # 22
grep -hoE 'c\.Handle\("[^"]+"' $(ls internal/apipublica/*.go | grep -v _test) | sort -u | wc -l  # 73
```

Los dos conjuntos coinciden patrón a patrón (comprobado con `diff`), y lo vigilan tres tests de
`internal/arranque`: `TestHuella` (la huella entera contra la dorada),
`TestNewFace_RegistersExactlyTheGoldenRoutes` (los `Patrones()` de la cara real son las 73 del
`:8103`, ni una más ni una menos) y `TestMuxRegistration_NoPanic` (el registro del `:8100` no
choca). **Añadir o quitar una ruta rompe la dorada**: regenerarla es cambiar un contrato hacia
fuera y pide commit propio.

Se registran en **dos sitios**, uno por listener:

| Origen | Patrones | Listener |
|---|---|---|
| `internal/apipublica/` — 25 ficheros, cada uno con su función `Mount…`; las llama `caraNueva` (`internal/arranque/mudanzas.go:130`) | 73 | público `:8103` |
| `internal/arranque/rutas_admin.go` → `registerAdminRoutes` (`:68`) | 22 | admin `:8100` |

Las 73 del `:8103`, por módulo dueño: 23 de acceso · 6 de edge · 4 de inferencia · 18 de
solicitudes · 3 de captación · 19 de conversación.

⚠️ **Dos trampas al grepear**:

- En `internal/apipublica/` el registro es `c.Handle(…)` sobre la cara (`Cara.Handle`,
  `internal/apipublica/apipublica.go:50`), no `mux.Handle`. Un `grep 'mux\.Handle'` ahí solo
  encuentra el interior de ese método.
- `Register` de `internal/modulos/acceso/iam/transport/http/auth.go:60` registra
  `/api/v1/auth/verify` y `/api/v1/auth/exchange` en un `*http.ServeMux`, y **producción no lo
  llama**: esas dos rutas las monta `MountAuth` (`internal/apipublica/auth.go:102-103`).
  Contarlo las duplicaría.

🔴 **Cada `Mount…` es condicional por dependencia.** Si la dependencia que necesita es `nil`,
vuelve sin registrar y la ruta responde un **404 de ruta inexistente**, indistinguible del 404 de
un recurso ajeno. En el arranque real las 73 se montan en los dos perfiles de la dorada. Dos
excepciones deliberadas, que se montan y degradan a **503** en vez de desaparecer:
`POST /api/v1/members` (sin credencial M2M de identity; `internal/apipublica/roleplane.go:170`)
y `POST /api/v1/signup` (ídem; `internal/apipublica/auth.go:155`).

---

## 1 · gRPC — 2 servicios, 2 métodos

Contrato: `github.com/EduGoGroup/wapp-cloudlink v0.17.0` (`go.mod`), paquete Go `cloudlinkv1`,
servicio proto `wapp.cloudlink.v1`.

**La regla**: se cuentan los métodos de los servicios registrados en ejecución; los fija la
dorada (`awk -F'\t' '$1=="con-m2m" && $2=="rpc"' internal/arranque/testdata/huella-vieja.golden`
→ 2 líneas).

| RPC | Tipo | Listener | Dónde se sirve |
|---|---|---|---|
| `wapp.cloudlink.v1.Enrollment/EnrollEdge` | unario | `:8102`, **TLS solo de servidor** | `internal/modulos/edge/enroll/server.go:84` |
| `wapp.cloudlink.v1.CloudLink/Connect` | **bidi-stream** | `:8101`, **mTLS estricto** | `internal/modulos/edge/grpc/connect.go:59` |

Keepalive del servidor CloudLink (`internal/arranque/fase8_transporte.go:70`): `Time=30s`,
`Timeout=10s`, `MinTime=15s`, `PermitWithoutStream`.

**Mensajes `EdgeToCloud` que el servidor atiende** — 10, enumeración cerrada en el `switch` de
`route` (`internal/modulos/edge/grpc/connect_route.go:46`):
`Incoming` · `Ack` · `InferenceResult` · `Heartbeat` · `Pong` · `Receipt` ·
`DiagnosticsBundle` · `UserLogin` · `UserRefresh` · `UserLogout`.

`Ack` e `InferenceResult` se resuelven **inline** en el bucle `Recv`; `Receipt`,
`DiagnosticsBundle` y los tres `User…` se sueltan a un carril por sesión.

---

## 2 · HTTP — listener público `:8103` (73 rutas)

Todas se registran en `internal/apipublica/`. La columna «Registro» da el fichero de ese
directorio y la línea del `c.Handle`. Dos cadenas comunes (`internal/apipublica/chain.go`):
`protect` (autentica, exige el scope y audita la escritura) y `protectRead` (autentica y exige
el scope).

### 2.1 · Autenticación y empresa (7 rutas)

Un token **sin empresa** las atraviesa: la cadena es `Authenticate` a secas, sin scope. Las
monta `MountAuth` (`auth.go`).

| Método + ruta | Registro (`auth.go`) | Nota |
|---|---|---|
| `/api/v1/auth/verify` | `:102` | sin verbo → cualquier método |
| `/api/v1/auth/exchange` | `:103` | canje Identity Token → Context Token; sin verbo |
| `/api/v1/auth/whoami` | `:106` | sin verbo |
| `POST /api/v1/invitations/accept` | `:126` | canje de invitación de un solo uso |
| `POST /api/v1/auth/active-tenant` | `:142` | elegir empresa |
| `GET /api/v1/auth/tenants` | `:143` | listar las empresas propias |
| `POST /api/v1/signup` | `:146` | **público**. Sin `WAPP_IDENTITY_API_KEY` se cablea a un **503 fijo**; con ella, limitador propio de 1 cada 60 s con ráfaga 5 (`:159`) |

### 2.2 · Mensajería y flujos (8 rutas)

| Ruta | Scope | Registro |
|---|---|---|
| `POST /api/v1/messages` | `messages.send` | `messages.go` `:125` |
| `POST /api/v1/flows` | `flows.create` | `flows.go` `:209` |
| `GET /api/v1/flows` · `GET /api/v1/flows/{id}` | `flows.read` | `flows.go` `:213` `:214` |
| `POST /api/v1/flows/{id}/start` | `flows.start` | `flows.go` `:219` |
| `POST /api/v1/triggers` · `GET /api/v1/triggers` · `DELETE /api/v1/triggers/{id}` | `triggers.{create,read,delete}` | `flows.go` `:227` `:229` `:231` |

### 2.3 · Sesiones, flota y diagnóstico (5 rutas)

| Ruta | Scope | Registro |
|---|---|---|
| `GET /api/v1/sessions` | `sessions.read` | `sessions.go` `:117` |
| `POST /api/v1/sessions/{id}/profile` | `sessions.write` | `sessions.go` `:127` — empuja el perfil a la sesión viva del Edge |
| `POST /api/v1/sessions/{id}/status` | `sessions.write` | `sessions.go` `:135` |
| `POST /api/v1/sessions/{id}/diagnostics` | `diagnostics.request` | `diagnostics.go` `:163` |
| `GET /api/v1/diagnostics/{command_id}` | `diagnostics.request` | `diagnostics.go` `:165` |

### 2.4 · Contenido, catálogo y variables (12 rutas)

| Ruta | Scope / gate | Registro |
|---|---|---|
| `PUT /api/v1/tenant-content/{ref}` · `POST /api/v1/tenant-content/{ref}` · `DELETE /api/v1/tenant-content/{ref}` | `content.write` | `tenantcontent.go` `:133` `:135` `:137` |
| `GET /api/v1/tenant-content` · `GET /api/v1/tenant-content/{ref}` | `content.read` | `tenantcontent.go` `:139` `:141` |
| `POST /api/v1/media/upload-url` | `media.upload` | `media.go` `:120` |
| `POST /api/v1/catalog/import` | `content.write` + feature `catalog_import` | `catalogimport.go` `:219` |
| `POST /api/v1/catalog/import/tabular` | `content.write` + `catalog_import` | `catalogtabular.go` `:145` |
| `GET /api/v1/catalog/import/template` · `GET /api/v1/catalog/import/prompt` | `content.read` + `catalog_import` | `catalogtemplate.go` `:107` `:108` |
| `GET /api/v1/tenant-variables` · `PUT /api/v1/tenant-variables` | `content.read` / `content.write` | `tenantvariables.go` `:144` `:146` |

### 2.5 · La bandeja (solicitudes) y sus eventos (14 rutas)

| Ruta | Scope + feature | Registro |
|---|---|---|
| `GET /api/v1/intakes` · `GET /api/v1/intakes/{id}` | `intakes.read` + `cart_basic` | `intakes.go` `:294` `:296` |
| `POST /api/v1/intakes/{id}/status` | `intakes.write` + `cart_basic` | `intakes.go` `:298` |
| `PUT /api/v1/intakes/{id}/items` | `intakes.write` + `cart_basic` | `intakes.go` `:308` |
| `POST /api/v1/intakes/{id}/approve` | `intakes.write` + `cart_basic` | `intakes.go` `:324` |
| `POST /api/v1/intakes/{id}/request-info` | `intakes.write` + `cart_basic` | `intakes.go` `:339` |
| `POST /api/v1/intakes/discard` | `intakes.write` + `cart_basic` | `intakes.go` `:352` |
| `POST /api/v1/intakes/{id}/reanalyze` | `intakes.write`; **sin gate en la ruta** (los dos gates van dentro del caso de uso) | `reanalyze.go` `:151` |
| **`POST /api/v1/intakes/{id}/quote-suggestion`** | `intakes.read` + `cart_basic` + **`llm_intake`** | `intakereports.go` `:231` |
| `GET /api/v1/intakes/export` · `GET /api/v1/intakes/summary.json` | `intakes.read` + `intakes_export` | `intakereports.go` `:195` `:197` |
| `GET /api/v1/conversation-events` | `intakes.read` + **cualquiera** de las features de tipo de evento | `conversationevents.go` `:167` |
| `POST /api/v1/conversation-events/{id}/cancel` | `intakes.write` + cualquiera de las features de tipo de evento | `conversationeventcancel.go` `:155` |
| `GET /api/v1/events/telemetry` | `events_telemetry.read`, sin feature | `eventstelemetry.go` `:165` |

🔴 **`quote-suggestion` es la ÚNICA ruta con plazo de escritura propio** (`writeDeadline`,
`internal/apipublica/writedeadline.go:102`) porque **espera al modelo dentro de la petición**. El
`WriteTimeout` del servidor es de 10 s (`internal/arranque/http.go:26`); el plazo de esta ruta es
`quoteWriteDeadline` = suelo por llamada del pipeline (48 s) + margen de 12 s, **derivado, no
copiado** (`internal/arranque/fase5_captacion.go:54`). Si tocas el timeout global, mira antes
esta ruta.

⚠️ `GET /api/v1/intakes/{id}`, `…/export` y `…/summary.json` van **siempre juntas**: son el
comodín y los dos literales que lo solapan (`internal/arranque/mudanzas.go:141`).

### 2.6 · Plano de roles y miembros (14 rutas, `roleplane.go`)

| Ruta | Scope | Registro (`roleplane.go`) |
|---|---|---|
| `GET /api/v1/roles` · `POST /api/v1/roles` | `roles.read` / `roles.write` | `:132` `:133` |
| `POST /api/v1/roles/{id}/grants` · `DELETE /api/v1/roles/{id}/grants` | `roles.write` | `:138` `:139` |
| `POST /api/v1/members/{user_id}/roles` · `DELETE /api/v1/members/{user_id}/roles/{role_id}` | `roles.write` | `:144` `:145` |
| `POST /api/v1/members/{user_id}/grants` · `DELETE /api/v1/members/{user_id}/grants` | `roles.write` | `:151` `:152` |
| `GET /api/v1/members` | `members.read` | `:165` |
| `POST /api/v1/members` | `members.write` | `:170` — **sin M2M responde 503, nunca 404** |
| `DELETE /api/v1/members/{user_id}` | `members.write` | `:171` |
| `GET /api/v1/invitations` · `POST /api/v1/invitations` · `DELETE /api/v1/invitations/{id}` | `members.read` / `members.write` | `:193` `:194` `:197` |

### 2.7 · Integraciones, LLM del tenant, intents, entitlements y auditoría (13 rutas)

| Ruta | Scope / gate | Registro |
|---|---|---|
| `GET /api/v1/integrations` · `GET /api/v1/integrations/outbox` | `integrations.read` + `crm_bridge` | `integrations.go` `:214` `:220` |
| `PUT /api/v1/integrations` · `DELETE /api/v1/integrations` | `integrations.write` + `crm_bridge` | `integrations.go` `:222` `:224` |
| **`POST /api/v1/integrations/callback`** | 🔴 **SIN JWT** | `crmcallback.go` `:240` |
| `GET /api/v1/tenant-llm` · `PUT /api/v1/tenant-llm` · `DELETE /api/v1/tenant-llm` | `llm.read` / `llm.write` + `api_llm` | `tenantllm.go` `:182` `:183` `:184` |
| `GET /api/v1/degradation-notices` | `llm.read` + `llm_intake` | `degradationnotices.go` `:153` |
| `GET /api/v1/intents` · `PUT /api/v1/intents` | `intents.read` / `intents.write` + `llm_intent` (solo el `PUT`, y **dentro** del handler; el `GET` no lleva gate). ⚠️ Su 403 es prosa, no `feature_not_enabled` (D-F7-13, [D-30](deuda.md)) | `intents.go` `:157` `:159` |
| `GET /api/v1/entitlements` | `entitlements.read` | `entitlements.go` `:55` |
| `GET /api/v1/audit` | `audit.read` | `audit.go` `:62` |

🔴 **El callback del CRM no lleva JWT y no es un descuido**: se autentica por **HMAC-SHA256
sobre el cuerpo crudo** con el secreto del tenant, ventana anti-replay ±300 s
(`crmCallbackWindow`, `internal/apipublica/crmcallback.go:58`), comparación en tiempo constante
(`Verify`, `internal/modulos/solicitudes/integrations/sigv1/sigv1.go:53`) y cuerpo con tope de
64 KiB (`internal/apipublica/crmcallback.go:64`). Cabeceras: `X-Wapp-Tenant`,
`X-Wapp-Timestamp`, `X-Wapp-Signature: v1=…` (`internal/apipublica/crmcallback.go:44`).

---

## 3 · HTTP — listener admin `:8100` (22 rutas)

Cadena: `Authenticate → RequirePermission → AuditMiddleware → h`
(`adminHandler`, `internal/arranque/http.go:201`). Todas se registran en `registerAdminRoutes`
(`internal/arranque/rutas_admin.go:68`), que es la **única** función que hace ese cableado.

| Ruta | Permiso | Plano |
|---|---|---|
| `/healthz` · `/metrics` | — (sin auth) | — |
| `/admin/leases/revoke` | `leases.revoke` | tenant |
| `GET /admin/tenants` · `POST /admin/tenants` | `tenants.read.any` / `tenants.create.any` | **plataforma** |
| `GET /admin/tenants/{id}` | `tenants.read.any` | **plataforma** |
| `GET /admin/tenants/{id}/installations` | `fleet.read.any` | **plataforma** |
| `POST /admin/tenants/{id}/enrollment-codes` | `enrollment.issue.any` | **plataforma** |
| `POST /admin/tenants/revoke` · `POST /admin/tenants/restore` | `tenants.revoke.any` / `tenants.restore.any` | **plataforma** |
| `GET /admin/access-requests` · `POST /admin/access-requests/{id}/approve` · `POST /admin/access-requests/{id}/reject` | `users.provision.any` | **plataforma** |
| `/admin/messages/send` | `messages.send` | tenant |
| `/admin/crypto/rekey` | `crypto.rekey` | tenant |
| `/admin/flows` · `/admin/flows/start` | `flows.create` / `flows.start` | tenant |
| `POST /admin/triggers` · `GET /admin/triggers` · `DELETE /admin/triggers/{id}` | `triggers.{create,read,delete}` | tenant |
| `POST /admin/sessions/{id}/profile` · `POST /admin/sessions/{id}/status` | `sessions.write` | tenant |

Siete de las 22 se registran **sin verbo** (`/healthz`, `/metrics`, `/admin/leases/revoke`,
`/admin/messages/send`, `/admin/crypto/rekey`, `/admin/flows`, `/admin/flows/start`): el patrón
acepta cualquier método y es el handler quien decide.

🔴 El sufijo **`.any`** es lo único que separa los dos planos. Ver I-CP-5 en
[`constitucion.md`](constitucion.md).

⚠️ `/healthz` **solo existe aquí**. El listener público `:8103` **no tiene sonda de salud**: no
hay ningún patrón `/healthz` entre las 73 de la dorada, así que responde 404.

⚠️ **El scrape de `/metrics` no es inocuo**: barre de paso las rachas de auto-respuesta
vencidas. Si nadie raspa, esos episodios no se cierran nunca
(`internal/arranque/fase7_flujos.go:135`).

---

## 4 · Comandos CLI (6)

**La regla**: un comando por directorio de `cmd/` con un `main.go` (`ls -d cmd/*/ | wc -l` → 6).
Los flags son los que declara cada `main.go` (`grep -n 'flag\.' cmd/*/main.go`). Solo
`cmd/server` es un binario de servicio.

| Comando | Flags | Qué hace |
|---|---|---|
| `go run ./cmd/server` | — (no lee argumentos) | El proceso. Migra al arrancar y levanta los 4 listeners |
| `go run ./cmd/migrate` | `-status` | Aplica las migraciones pendientes y sale. `-status` solo consulta el estado registrado, sin escribir |
| `go run ./cmd/prompts` | `-volcar <dir>` · `-comprobar <dir>` | Vuelca / valida los cuatro `.tmpl` de P2–P5 |
| `go run ./cmd/casebank` | `-tenant <id>` · `-consentido` | Siembra un caso en `intake_case_bank`. **Sin `-consentido` se niega**. No migra |
| `go run ./cmd/cobertura-ficheros` | `-perfil <fichero>` · `-dirs <d1,d2>` · `-bridges <d,…>` · `-umbral 80` · `-raiz .` | Informe de cobertura por fichero a partir de un perfil de `go test -coverprofile`. **No bloquea** (rc=0 con ficheros por debajo). Lo invoca `make cobertura-ficheros` |
| `go run ./cmd/debug_inferencia` | `-model` · `-ollama <url>` · `-force-pipeline` · `[frase…]` | Depurador local de P1–P4 contra un Ollama. Con una frase como argumento la analiza y sale; sin argumentos entra en modo interactivo. **No toca la base ni el Edge** |

`server`, `migrate` y `casebank` toman la conexión de las **mismas** variables `WAPP_DB_*`
(`config.Load()`), nunca de un argumento. `prompts`, `cobertura-ficheros` y `debug_inferencia`
no leen configuración del entorno.

---

## 5 · Variables de entorno (71)

**La regla**: se cuentan las claves que el código de producción **lee de verdad**, por su nombre
efectivo en la máquina. `config.Load()` (`internal/platform/config/config.go:730`) lee **70
claves** por el loader —69 con `loader.Get…` y una, `WAPP_FLOW_REPLY_RATE`, por el ayudante
`getFloat` (`:818`)— **más** `WAPP_CONFIG_FILE`, que va por `os.Getenv` (`:735`) y no pasa por
el loader: **71**.

```bash
awk '/^func Load\(\)/,/^}/' internal/platform/config/config.go \
  | grep -oE '(loader\.Get[A-Za-z0-9]+\(|getFloat\(loader, )"[A-Z_0-9]+"' | sort -u | wc -l   # 70
grep -rnE 'os\.(Getenv|LookupEnv|Environ)\(' --include='*.go' cmd internal \
  | grep -v _test.go | grep -v testdata | grep -vE '^[^:]+:[0-9]+:[[:space:]]*//'          # 1 línea: config.go:735
```

`config.Load()` es el **único** punto de producción que lee el entorno: `TestHuella` lo vigila
sobre `cmd/server`, `internal/arranque`, `internal/apipublica`, `internal/modulos` e
`internal/nucleo`. **Precedencia: defaults → YAML → entorno.**

⚠️ Un `grep` que solo busque `loader.Get…` da **69** y se deja `WAPP_FLOW_REPLY_RATE`: por eso
el comando de arriba lleva las dos formas.

🔴 **Nombre efectivo**: el loader compone el prefijo con `WithEnvPrefix(EnvPrefix)` y
`EnvPrefix = "WAPP_"` (`internal/platform/config/config.go:24`). El literal del código es
`HTTP_ADDR`; en el entorno es **`WAPP_HTTP_ADDR`**. Abajo van todas con su nombre efectivo: un
`grep` del nombre entero en el código no encuentra nada y **no** prueba que esté muerta.

⚠️ **El proceso NO lee ningún `.env` por sí mismo** (cero `godotenv` en `go.mod`). En UAT lo
inyecta systemd con `EnvironmentFile`; en local lo exportas tú.

### Con default (49) — `defaults()` en `internal/platform/config/config.go:621`

| Variable | Default | Qué gobierna |
|---|---|---|
| `WAPP_APP_ENV` | `dev` | ambiente declarado |
| `WAPP_HTTP_ADDR` · `WAPP_PUBLIC_HTTP_ADDR` · `WAPP_GRPC_ENROLL_ADDR` · `WAPP_GRPC_CONNECT_ADDR` | `:8100` · `:8103` · `:8102` · `:8101` | los cuatro listeners |
| `WAPP_GRPC_PUSH_TIMEOUT` · `WAPP_GRPC_ACK_TIMEOUT` | `10s` · `8s` | empuje al Edge y espera del Ack |
| `WAPP_GATEWAY_WORK_QUEUE` · `WAPP_GATEWAY_WORK_TIMEOUT` | `64` · `5s` | el carril por sesión del gateway |
| `WAPP_PUBLICAPI_DB_TIMEOUT` | `1.5s` | plazo de base de datos de la cara pública. Calibrado por la **suma**: 1,5 s + 8 s del Ack = 9,5 s, y cabe en el `WriteTimeout` de 10 s |
| `WAPP_LOG_LEVEL` · `WAPP_LOG_JSON` | `info` · `false` | |
| `WAPP_PLATFORM_TENANT_ID` | el UUID fijo de `defaults()` | el tenant del plano de plataforma; sin él quedaría cerrado a cal y canto |
| `WAPP_DB_HOST` · `WAPP_DB_PORT` · `WAPP_DB_USER` · `WAPP_DB_NAME` · `WAPP_DB_SSLMODE` | `localhost` · `5432` · `wapp` · `wapp_cloud` · `disable` | |
| `WAPP_DB_PASSWORD` | uno de desarrollo, en `defaults()` | en un entorno real se inyecta siempre |
| `WAPP_DB_MAX_OPEN_CONNS` · `WAPP_DB_MAX_IDLE_CONNS` · `WAPP_DB_CONN_MAX_LIFETIME` · `WAPP_DB_CONN_MAX_IDLE_TIME` | las constantes `Default…` del paquete `internal/platform/storage/postgres` | referenciadas, no copiadas |
| `WAPP_PKI_SERVER_CERT_FILE` · `WAPP_PKI_SERVER_KEY_FILE` · `WAPP_PKI_CA_CERT_FILE` · `WAPP_PKI_CA_KEY_FILE` | `certs/server.crt` · `certs/server.key` · `certs/ca.crt` · `certs/ca.key` | el certificado del servidor y la CA de enrolamiento |
| `WAPP_KEK_PROVIDER` | **`env`** | `env` \| `kms`. El KMS está **construido y apagado** a propósito |
| `WAPP_RATELIMIT_PUBLIC_RPS` · `WAPP_RATELIMIT_PUBLIC_BURST` · `WAPP_RATELIMIT_TRUST_PROXY` | `20` · `40` · `false` | rate-limit de la cara pública entera |
| `WAPP_FLOW_REPLY_RATE` · `WAPP_FLOW_REPLY_BURST` · `WAPP_FLOW_INCOMING_TIMEOUT` · `WAPP_FLOW_MAX_CONCURRENT_INCOMING` | `0.5` · `3` · `30s` · `64` | |
| `WAPP_LLM_WARMUP_ENABLED` · `WAPP_LLM_MAX_OUTPUT_TOKENS_ENABLED` | `true` · `true` | nacen **encendidos**: el apagado existe para el A/B de campo |
| `WAPP_HEALTH_DEGRADED_AFTER` · `WAPP_HEALTH_STALE_AFTER` | `5m` · `2m` | |
| `WAPP_DIAGNOSTICS_BUNDLE_TTL` | `30m` | |
| `WAPP_TENANT_CONTENT_MAX_BYTES` | `1 MiB` | |
| `WAPP_IMPORT_MAX_ITEMS` | `500` | |
| `WAPP_WEBHOOK_POLL_INTERVAL` · `WAPP_WEBHOOK_MAX_ATTEMPTS` · `WAPP_WEBHOOK_TIMEOUT` | `5s` · `10` · `10s` | el outbox del CRM |
| `WAPP_STORAGE_S3_REGION` · `WAPP_STORAGE_S3_BUCKET` · `WAPP_STORAGE_S3_PRESIGN_EXPIRY` | `us-east-1` · `edugo-materials` · `15m` | |
| `WAPP_JWT_ISSUER` | `wapp-cloud` | |

### Sin default (22) — secretos, rutas y palancas; si faltan, el arranque falla o la función queda apagada

| Variable | Qué pasa si falta |
|---|---|
| `WAPP_CONFIG_FILE` | no se lee ningún YAML: quedan defaults y entorno |
| `WAPP_LEASE_PRIVATE_KEY_FILE` · `WAPP_LEASE_PRIVATE_KEY_B64` | ⚠️ **sin ninguna de las dos, la plataforma genera una clave de lease EFÍMERA en cada arranque e invalida a todos los Edges al reiniciar**; en prod el arranque se niega (`internal/arranque/lease.go:28`) |
| `WAPP_LEASE_TTL_MINUTES` | `<= 0` ⇒ **15 min** (`DefaultTTL`, `internal/modulos/edge/lease/lease.go:55`). Vigencia del lease, renovada en cada Heartbeat |
| **`WAPP_LLM_PROMPTS_DIR`** | vacío ⇒ corren los prompts **compilados**. Ver I-CP-1 en [`constitucion.md`](constitucion.md) |
| `WAPP_KEK_KEYRING` · `WAPP_KEK_CURRENT` · `WAPP_KEK_MASTER_B64` · `WAPP_KEK_INDEX_B64` | las KEK del proveedor `env` |
| `WAPP_KEK_KMS_KEY` · `WAPP_KEK_KMS_KEYRING` · `WAPP_KEK_KMS_INDEX_B64` | solo con `WAPP_KEK_PROVIDER=kms`, y entonces obligatorias |
| `WAPP_CLOUD_ENC_PRIVKEY_B64` | la clave privada de cifrado del Cloud |
| `WAPP_STORAGE_S3_ACCESS_KEY_ID` · `WAPP_STORAGE_S3_SECRET_ACCESS_KEY` · `WAPP_STORAGE_S3_ENDPOINT` | credenciales y endpoint del almacén de media |
| `WAPP_JWT_EC_PRIVATE_KEY_FILE` · `WAPP_JWT_KID` | la clave EC del emisor de Context Tokens |
| `WAPP_IDENTITY_JWKS_URL` · `WAPP_IDENTITY_URL` · `WAPP_IDENTITY_TIMEOUT` | el SSO del grupo |
| `WAPP_IDENTITY_API_KEY` | sin ella no hay cliente M2M: `POST /api/v1/signup` y `POST /api/v1/members` responden **503** |

De cada secreto se dice aquí dónde entra; el valor vive en el `EnvironmentFile` de UAT, nunca
en este repo.

### Solo de tests (2; no entran en las 71)

`WAPP_PROCESOS_EDGE_DIR` — el repo del Edge en disco, para `make test-procesos-edge-real`
(`test/procesos/edge_real_test.go:48`) · `WAPP_PROCESOS_COVERDIR` — dónde vuelca el servidor
bajo prueba sus contadores de cobertura (`test/procesos/servidor_test.go:280`); la fija
`make cobertura-combinada`.

Los tests de proceso arrancan su propio Postgres con testcontainers: **ningún test lee una
cadena de conexión del entorno**, y el candado `internal/candados/sinbdviva.go` lo impide.

### 🔴 Configuración MUERTA

**`WAPP_STORAGE_S3_PREFIX`** está en `.env.example`, y **no la lee nadie**: no está entre las 70
claves del loader y `StorageConfig` no tiene campo de prefijo. El prefijo real es la constante
compilada `mediaKeyPrefix = "wapp/media"` (`internal/apipublica/media.go:127`). Ver
[`deuda.md`](deuda.md).

⚠️ El `.env.example` asigna 47 claves (`grep -cE '^WAPP_[A-Z0-9_]+=' .env.example`) y `Load()`
lee 71. No lo tomes como inventario.

---

## 6 · Ficheros que lee y escribe

**Escribe: solo el CLI.** `cmd/prompts -volcar` crea el directorio con
`os.MkdirAll(dir, 0o750)` y los cuatro ficheros con `os.WriteFile(path, …, 0o600)`
(`internal/modulos/inferencia/prompts/volcar.go:66` y `:81`). **El servidor no escribe ningún
fichero**:

```bash
grep -rn 'os\.WriteFile\|os\.Create\|os\.OpenFile\|os\.MkdirAll' --include='*.go' internal cmd \
  | grep -v _test.go | grep -v testdata
```

da esas dos líneas y una tercera, `internal/arranque/huellatest/golden.go:78`, que es el
ayudante de test que regenera la dorada: no está en el camino del servidor.

**Lee**: los cuatro `.tmpl` de `WAPP_LLM_PROMPTS_DIR` (opcional), el certificado y la clave
del servidor y de la CA (`WAPP_PKI_*`), la clave privada del lease
(`WAPP_LEASE_PRIVATE_KEY_FILE`), la clave EC del emisor de JWT
(`WAPP_JWT_EC_PRIVATE_KEY_FILE`) y, si existe, el YAML de `WAPP_CONFIG_FILE`.

**Embebido** (`go:embed`, no es I/O; `internal/platform/storage/postgres/migrations/embed.go:15`):
los `.sql` de `internal/platform/storage/postgres/migrations/structure/`. Cuántos son y qué
esquema dejan lo dice [`esquema-postgres.md`](esquema-postgres.md).

---

## 7 · Contrato `wapp-crm-v1`

Vive congelado en `docs/contracts/wapp-crm-v1/` (tres esquemas JSON, su prosa y un ejemplo por
verbo). Sus ejemplos se validan contra su esquema en `TestContractExamplesMatchSchemas`
(`internal/modulos/solicitudes/integrations/contrato_wapp_crm_v1_test.go:76`), y el cuerpo que
acepta el callback se cruza con `intake.status.schema.json` en
`internal/apipublica/crmcallback_schema_test.go`.

Tres verbos, **dos implementados**: `intake.push` (wApp → puente, outbox durable + HMAC),
`intake.status` (puente → wApp, el callback de §2.7) y `catalog.pull`, diferido: configurar el
adaptador de catálogo `http` en `PUT /api/v1/integrations` **responde 422** con el texto
«catalog.pull diferido…» (`internal/apipublica/integrations_validate.go:100`).

---

## 8 · Métricas Prometheus (`/metrics` en `:8100`)

**22 nombres declarados**: 17 estáticos más 5 descriptores dinámicos.

**La regla**: se cuentan los **nombres declarados** en el código de producción. **No** se
cuentan las series que Prometheus deriva solo de un histograma (`_bucket` / `_sum` / `_count`):
existen en el cuerpo de `/metrics` pero no están declaradas.

```bash
grep -rnE 'Name:[[:space:]]*"wapp_' --include='*.go' internal/platform/metrics | grep -v _test | wc -l   # 17
grep -cE '^[[:space:]]*"wapp_[a-z_]+",$' internal/platform/metrics/inferstats.go                          # 5
```

⚠️ El primer comando va acotado a `internal/platform/metrics` a propósito: sobre todo
`internal/` daría una línea de más, `internal/platform/config/config.go:698`, que es el nombre
por defecto de la base de datos y no una métrica.

Los 17 estáticos, todos en `internal/platform/metrics/metrics.go`:

| Métrica | Línea |
|---|---|
| `wapp_http_requests_total` · `wapp_http_request_duration_seconds` | `:72` `:76` |
| `wapp_ratelimit_hits_total` | `:81` |
| `wapp_receipts_total` | `:85` |
| `wapp_flow_reactive_blocked_total` | `:89` |
| `wapp_webhook_deliveries_total` | `:93` |
| `wapp_flow_event_lifecycle_total` | `:97` |
| `wapp_cart_match_total` | `:101` |
| `wapp_llm_degradacion_total` | `:105` |
| `wapp_flow_autoreply_streak` (histograma) | `:109` |
| `wapp_flow_autoreply_streak_max` (gauge que se calcula en el scrape) | `:132` |
| `wapp_db_wait_count` · `wapp_db_wait_duration_seconds` · `wapp_db_max_idle_closed` · `wapp_db_in_use` · `wapp_db_idle` · `wapp_db_max_open` | `:482`–`:502` |

Los 5 por `NewDesc` (`internal/platform/metrics/inferstats.go:69`):
`wapp_edge_inference_by_regime_total` · `wapp_edge_inference_by_class_total` ·
`wapp_edge_intent_omitted_total` · `wapp_edge_inference_samples_total` ·
`wapp_edge_inference_reporting_edges`.

🔴 **Declaradas no es lo mismo que visibles.** Un `CounterVec` **no aparece en `/metrics` hasta
su primer incremento**, y el reinicio lo borra: su ausencia tras desplegar no prueba nada. La
dorada fija las **11** que un proceso recién arrancado ya publica (líneas `# TYPE`; perfil
`con-m2m`, clase `metrica`): las seis `wapp_db_*`, `wapp_http_requests_total`,
`wapp_http_request_duration_seconds`, `wapp_flow_autoreply_streak`,
`wapp_flow_autoreply_streak_max` y `wapp_edge_inference_reporting_edges`. Las otras 11 aparecen
con su primer evento.

🔴 **`wapp_auth_logins_total` NO EXISTE** y no debe entrar en esta lista: wApp no valida
credenciales, lo dice `internal/platform/metrics/metrics.go:201` y hay un test que **falla si la
cadena aparece** en el cuerpo de `/metrics` (`internal/platform/metrics/metrics_test.go:48`).

---

## 9 · Eventos de telemetría escritos en `flow_events`

Cinco, y la lista está cerrada por diseño:

| Evento | Constante | Dónde |
|---|---|---|
| `intake_draft_created` | `EventDraftCreated` | `internal/modulos/captacion/stages/draft_events.go:80` |
| `intake_reanalyzed` | `EventReanalyzed` | `internal/modulos/captacion/stages/draft_events.go:88` |
| `intake_line_corrected` | `EventLineCorrected` | `internal/modulos/solicitudes/intakes/metricas.go:71` |
| `intake_approved` | `EventApproved` | `internal/modulos/solicitudes/intakes/metricas.go:74` |
| `intake_info_requested` | `EventInfoRequested` | `internal/modulos/solicitudes/intakes/metricas.go:77` |

Los lee `GET /api/v1/events/telemetry` (§2.5).
