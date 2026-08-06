# wapp-cloud-platform (Piezas 03 y 05)

Plataforma cloud modular de wApp en Go: el **monolito modular** (ADR-0010) que
aloja todo lo que gestiona el equipo de wApp. Agrupa el contexto de identidad y
su RBAC, el dominio de negocio, el Motor de Flujos conversacionales y el gateway
CloudLink que habla con cada Edge. La nube **piensa** y arma el payload completo;
el Edge despacha (ADR-0005).

**Quién autentica**: desde el Plan 003 de identity, las credenciales de las
personas las valida **identity-core**, el SSO del grupo. wApp no tiene padrón de
usuarios ni valida contraseñas: recibe un Identity Token, lo **canjea** por un
Context Token propio con el tenant y los grants de negocio, y eso es lo único que
firma. Ver `docs/adr/` y el ADR local de adopción.

- Módulo Go: `github.com/EduGoGroup/wapp-cloud-platform`
- Go **1.26**
- Binarios: `cmd/server` (la plataforma) y `cmd/migrate` (aplica el esquema y sale)
- SchemaVersion **0.24.0** (última migración `0040_entitlements_read_grant.sql`)

## Estado

**Implementada y operando.** No es un scaffold: cubre desde el IAM y la API
pública hasta el Motor de Flujos con sus módulos y la operabilidad de flota
(planes 005–031, 033 y 040). Se despliega hoy contra Neon en el piloto multi-Edge.

## Módulos (`internal/`)

Layout **modular por capacidad**, no una hexagonal global. Cada módulo tiene su
frontera, sus tablas y su API.

| Módulo | Responsabilidad |
|---|---|
| `iam` | Canje de Identity Token → Context Token, verificación del Context Token propio y autorización (RBAC glob multi-tenant: roles, grants, membresías). Ya NO hay usuarios, contraseñas, refresh tokens ni api-keys: viven en identity-core. Capas `domain`/`usecase`/`ports`/`infra`/`transport`. |
| `publicapi` | API pública `/api/v1` para terceros: sesiones, mensajes, flows, triggers, media, intents, entitlements, diagnostics, health, audit. Protegida con JWT ES256 + RBAC. |
| `flujos` | Motor de Flujos (Pieza 05): motor dinámico por puertos `ContentSource` + `EventSink` con un `Registry` de módulos enchufables (`modules/menu`, `modules/survey`, `modules/cart`, `modules/media`) más `engine`, `runtime`, `trigger`, `content`, `contact`, `store`, `admin`. |
| `gateway` | Terminación gRPC de los Edges (Pieza 02): `grpc` (CloudLink bidi), `enroll` (CA + EnrollEdge), `lease` (kill-switch ADR-0007), `fleet` (online/offline por sesión), `session` (registro de streams vivos). |
| `entitlements` | Derechos/capacidades habilitadas por tenant. `Resolver` con `Has` / `ListEffective` / `CacheTTL` (`postgres.go:94/120/89`, caché en memoria de 60 s por defecto), el middleware `RequireFeature` que gatea rutas (ver *Seguridad*) y la taxonomía comercial sembrada en la migración `0039`. |
| `intentcfg` | Almacén de la configuración de intenciones por tenant (contrato del módulo `intents` de wapp-shared) que alimenta al clasificador. |
| `ingest` | Ingesta de eventos entrantes del Edge con **dedupe** por `(session_id, wa_message_id)` (Plan 028, migración 0031, tabla `ingest_dedupe`). |
| `receipts` | Acuses de mensaje (`delivered`/`read`) extremo a extremo edge→nube. |
| `diagnostics` | Diagnóstico remoto bajo demanda de la flota (Plan 031, ADR-0023): bundle con retención (TTL). |
| `platform` | Plataforma de soporte transversal: `config`, `logging`, `httpapi` (health/admin), `storage/postgres` (runner de migraciones) y `storage/objectstore` (R2). No es un módulo de dominio. |

### Planes y features (Plan 040)

**5 planes × 12 features**, sembrados en `0039_seed_plan_taxonomy.sql`: `basic`, `commerce`,
`advisor_ai` y `advisor_ai_pro` son los paquetes comerciales, y `pro` es el plan **interno de
laboratorio** con las 12 claves. `basic` y `pro` venían de la `0032` y se reutilizan; la `0039` solo
crea los tres nuevos. La composición está **denormalizada** —cada plan lista todas sus features— y
no hay herencia en BD: la notación «Comercio + …» del design es documental, el lookup es un JOIN
plano (`postgres.go:235`). `passive_profiles` y `multi_empresa` no entran en ningún paquete
comercial: son **add-on** por tenant.

Resolución (ADR-0022): el override de `tenant_features` **gana**; sin override mandan las del plan,
y un tenant sin `plan_id` se trata como `basic` (`COALESCE`, `postgres.go:151`). Quien quiera leer
el plan efectivo usa `GET /api/v1/entitlements` (scope `entitlements.read`, `publicapi.go:270`),
acotado al tenant del token (INV-8): el tenant no viaja en la URL y no hay consulta cross-tenant.

## Almacenes

| Almacén | Qué guarda | Qué NUNCA guarda |
|---|---|---|
| **PostgreSQL** (Neon en el piloto) | Tenants, IAM/RBAC, contactos (PII cifrada), flujos y su estado, órdenes, fleet de Edges, leases, acuses, dedupe de ingesta, entitlements, configs de intención, diagnósticos. | DEK, store cifrado del Edge, llaves privadas |
| **S3 / R2** (Cloudflare R2, S3-compatible) | Media y PDFs; se sirven al Edge por **URL prefirmada** de corta vida. En alpha comparte bucket con EduGo (`edugo-materials`, prefijo `wapp/`). | Credenciales del cliente |

**No hay MongoDB** (todo el estado conversacional vive en PostgreSQL) y **no hay
broker**: el fan-out de campañas se hace por **concurrencia Go** (goroutines +
channels), nunca RabbitMQ ni Redis (ADR-0003).

## Migraciones y SchemaVersion

Los scripts SQL embebidos viven en
`internal/platform/storage/postgres/migrations/structure/` (`0001_*.sql` …
`0040_entitlements_read_grant.sql`). El runner de `platform/storage/postgres` los aplica
al arranque y valida `SchemaVersion` (`migrations/version.go`, hoy **0.24.0**)
contra `public.schema_version`; además calcula un hash de los archivos para
detectar cambios aunque no se haya subido la versión. **Obligatorio incrementar
`SchemaVersion` al tocar cualquier `structure/*.sql`.**

El runner es **full-replay**: cuando detecta cambio de hash reejecuta TODOS los
`structure/*.sql`, no solo los nuevos. Por eso el DDL es idempotente
(`CREATE ... IF NOT EXISTS`) y lo destructivo va con guard.

### Aplicar migraciones sin levantar la plataforma

```bash
make migrate         # aplica y sale
make migrate-status  # solo consulta (no escribe)
```

`cmd/migrate` usa la MISMA config que el servidor (`WAPP_DB_*`) e imprime a qué
base se ha conectado antes de tocarla. Existe porque aplicar DDL no debería exigir
abrir listeners HTTP, gRPC y el plano de control del Edge.

⚠️ **Contra Neon, apunta al host DIRECTO, nunca al `-pooler`**: el runner
serializa con `pg_advisory_lock` sobre una conexión dedicada, y un pooler en modo
transacción puede repartir el lock y el unlock en sesiones distintas.

## Listeners y puertos (banda 81xx)

El binario `cmd/server` levanta cuatro listeners en un solo proceso. Defaults en
`internal/platform/config/config.go`, override por variables `WAPP_*`.

| Listener | Env | Default | Qué expone |
|---|---|---|---|
| HTTP admin/health (interno) | `WAPP_HTTP_ADDR` | `:8100` | `/healthz`, `/admin/*` (kill-switch de leases, envío de mensajes) |
| HTTP API pública | `WAPP_PUBLIC_HTTP_ADDR` | `:8103` | `/api/v1` para terceros (JWT usuario ES256 + RBAC) |
| gRPC Enrollment | `WAPP_GRPC_ENROLL_ADDR` | `:8102` | `EnrollEdge` (TLS de servidor solamente: el Edge enrola sin cert de cliente) |
| gRPC CloudLink | `WAPP_GRPC_CONNECT_ADDR` | `:8101` | `Connect` bidi-stream con **mTLS estricto** (el Edge conecta con el cert emitido) |

## Variables de entorno (prefijo `WAPP_`)

Precedencia: defaults → YAML (`WAPP_CONFIG_FILE`) → variables de entorno.
Principales (ver `config.go` para el conjunto completo):

| Variable | Para qué |
|---|---|
| `WAPP_APP_ENV` | `dev` o `prod`; en `prod` exige material de clave explícito (fail-fast) |
| `WAPP_HTTP_ADDR` · `WAPP_PUBLIC_HTTP_ADDR` · `WAPP_GRPC_ENROLL_ADDR` · `WAPP_GRPC_CONNECT_ADDR` | Direcciones de los cuatro listeners |
| `WAPP_DB_HOST` · `WAPP_DB_PORT` · `WAPP_DB_USER` · `WAPP_DB_PASSWORD` · `WAPP_DB_NAME` · `WAPP_DB_SSLMODE` | Conexión a PostgreSQL |
| `WAPP_JWT_EC_PRIVATE_KEY_FILE` · `WAPP_JWT_KID` · `WAPP_JWT_ISSUER` | Firma/validación del **Context Token** de wApp (ES256) |
| `WAPP_IDENTITY_URL` · `WAPP_IDENTITY_JWKS_URL` · `WAPP_IDENTITY_TIMEOUT` | El SSO del grupo: a quién preguntar por las credenciales y con qué claves verificar sus Identity Tokens |
| `WAPP_KEK_KEYRING` · `WAPP_KEK_CURRENT` · `WAPP_KEK_MASTER_B64` · `WAPP_KEK_INDEX_B64` · `WAPP_CLOUD_ENC_PRIVKEY_B64` | Cifrado de PII (KEK versionada, ADR-0017) y clave de tránsito de la nube |
| `WAPP_LEASE_PRIVATE_KEY_FILE` · `WAPP_LEASE_PRIVATE_KEY_B64` · `WAPP_LEASE_TTL_MINUTES` | Firma y vigencia del lease (kill-switch, ADR-0007) |
| `WAPP_PKI_*` | Rutas de la CA y el cert de servidor de la PKI del gateway |
| `WAPP_STORAGE_S3_*` (`BUCKET`, `ENDPOINT`, `ACCESS_KEY_ID`, `SECRET_ACCESS_KEY`, `PRESIGN_EXPIRY`) | Almacén de objetos R2 (media/PDF) |
| `WAPP_RATELIMIT_*` · `WAPP_FLOW_*` · `WAPP_HEALTH_*` · `WAPP_DIAGNOSTICS_*` | Rate-limit de la API pública, runtime del Motor de Flujos, derivación de salud de flota y retención de diagnósticos |
| `WAPP_TENANT_CONTENT_MAX_BYTES` | Peso máximo de un blob de `tenant_content` (1 MiB), aplicado **antes de deserializar**. Cuelga de la tabla y no del camino: gobierna **igual** al import de catálogo y a `PUT /api/v1/tenant-content`, porque dos techos distintos dejarían un blob importable que el PUT rechaza |
| `WAPP_IMPORT_MAX_ITEMS` | Artículos máximos por importación de catálogo (500). Este sí es propio del import: el `PUT` genérico no cuenta artículos |

Los secretos (JWT, KEK, lease, credenciales R2) **nunca** se hardcodean ni se
loguean; en `dev`, si faltan, se generan valores efímeros con warning.

## Cómo correr

```bash
# PKI de dev (genera certs/{ca,server}.{crt,key})
scripts/gen-dev-certs.sh

# Arranque
go run ./cmd/server

# Solo migrar (sin levantar listeners)
go run ./cmd/migrate
```

Requisitos locales:

- **PostgreSQL** accesible (en local se comparte el `edugo-postgres`; base
  `wapp_cloud`), o un DSN a Neon vía `WAPP_DB_*`.
- Puertos libres en la **banda 81xx** (8100–8103); coexiste con EduGo en 80xx.
- Config opcional por YAML apuntado con `WAPP_CONFIG_FILE`; lo no cubierto cae a
  los defaults de `config.go`.

## Seguridad

- **Las credenciales no se validan aquí.** Login, refresh, logout y revocación
  global son de identity-core; wApp canjea el Identity Token resultante. No queda
  ni un `password_hash` en esta base.
- **Context Tokens en ES256** (ECDSA P-256) con `kid` + `MultiVerifier`
  (ADR-0019, Plan 028); la clave EC privada se lee de un PEM con permisos `0600`.
  Es el único algoritmo de firma que le queda a wApp: el HS256 del plano M2M se
  retiró con él.
- **Sin plano M2M.** `X-API-Key` y los service tokens se retiraron (nadie tenía
  credencial). Cuando haga falta, se construye por el modelo del ADR-0025 de
  identity: la credencial de máquina **se canjea, no se presenta**.
- **RBAC por grants glob** multi-tenant; el tenant se deriva del token.
- **Gate de capacidades** (`entitlements.RequireFeature`, `internal/entitlements/middleware.go:33`):
  el grant dice «puedes operar esto», la feature dice «tu plan lo incluye» — dos preguntas
  distintas, ninguna sustituye a la otra. Sin la feature corta con `403` y
  `{"error":"feature_not_enabled","feature":"<clave>"}`. Es **fail-closed** en los tres modos de
  no-resolución (sin identidad o sin tenant, resolver caído, resolver `nil`): los tres dan 403 y no
  500, porque un 5xx invitaría a reintentar hasta colarse.
- **Dedupe de ingesta** por `(session_id, wa_message_id)` para no reprocesar entrantes repetidos.
- **PII cifrada en reposo** con KEK versionada y rotación sin re-cifrar (ADR-0017,
  Plan 012), más índice ciego HMAC con `indexKey` independiente.
- **Zero-knowledge**: la DEK y el store cifrado del Edge nunca llegan a la nube; el
  gateway solo emite y revoca leases (ADR-0007).

## Decisiones (ADRs)

- **ADR-0009** — Datos de negocio en la nube; DEK y store solo en el Edge.
- **ADR-0010** — Monolito modular; extraer a servicio solo cuando duela.
- **ADR-0017** — Cifrado de PII en reposo con KEK (rotación en el Plan 012).
- **ADR-0019** — JWT de usuario HS256 → **ES256** (`kid` + `MultiVerifier`).
- **ADR-0020 / 0021 / 0022** — Clasificador de intenciones LLM (entitlements,
  contrato `intents`, config por tenant e ingesta de la señal).
- **ADR-0023** — Operabilidad de flota (salud derivada y diagnóstico remoto).

## Referencias

- Pieza 03: `../../docs/piezas/03-plataforma-cloud.md`
- Pieza 05: `../../docs/piezas/05-motor-flujos-modulos.md`
- CloudLink (conducto edge↔cloud): `../../docs/piezas/02-cloudlink.md`
- ADRs: `../../docs/adr/`
- Contexto para agentes: `./CLAUDE.md` · raíz del ecosistema: `../../CLAUDE.md`
