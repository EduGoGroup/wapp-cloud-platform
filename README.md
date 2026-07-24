# wapp-cloud-platform (Piezas 03 y 05)

Plataforma cloud modular de wApp en Go: el **monolito modular** (ADR-0010) que
aloja todo lo que gestiona el equipo de wApp. Agrupa el IAM, el dominio de
negocio, el Motor de Flujos conversacionales y el gateway CloudLink que habla con
cada Edge. La nube **piensa** y arma el payload completo; el Edge despacha
(ADR-0005).

- Módulo Go: `github.com/EduGoGroup/wapp-cloud-platform`
- Go **1.26**
- Binario único: `cmd/server`
- SchemaVersion **0.21.0** (última migración `0036_diagnostics.sql`)

## Estado

**Implementada y operando.** No es un scaffold: cubre desde el IAM y la API
pública hasta el Motor de Flujos con sus módulos y la operabilidad de flota
(planes 005–031). Se despliega hoy contra Neon en el piloto multi-Edge.

## Módulos (`internal/`)

Layout **modular por capacidad**, no una hexagonal global. Cada módulo tiene su
frontera, sus tablas y su API.

| Módulo | Responsabilidad |
|---|---|
| `iam` | Autenticación (JWT) y autorización (RBAC glob multi-tenant): usuarios, roles, grants, refresh tokens, api-keys. Capas `domain`/`usecase`/`ports`/`infra`/`transport`. |
| `publicapi` | API pública `/api/v1` para terceros: sesiones, mensajes, flows, triggers, media, intents, diagnostics, health, audit. Protegida con JWT ES256 + RBAC. |
| `flujos` | Motor de Flujos (Pieza 05): motor dinámico por puertos `ContentSource` + `EventSink` con un `Registry` de módulos enchufables (`modules/menu`, `modules/survey`, `modules/cart`, `modules/media`) más `engine`, `runtime`, `trigger`, `content`, `contact`, `store`, `admin`. |
| `gateway` | Terminación gRPC de los Edges (Pieza 02): `grpc` (CloudLink bidi), `enroll` (CA + EnrollEdge), `lease` (kill-switch ADR-0007), `fleet` (online/offline por sesión), `session` (registro de streams vivos). |
| `entitlements` | Derechos/capacidades habilitadas por tenant (gate de features, p. ej. el clasificador LLM). |
| `intentcfg` | Almacén de la configuración de intenciones por tenant (contrato del módulo `intents` de wapp-shared) que alimenta al clasificador. |
| `ingest` | Ingesta de eventos entrantes del Edge con **dedupe** por `(session_id, wa_message_id)` (Plan 028, migración 0031, tabla `ingest_dedupe`). |
| `receipts` | Acuses de mensaje (`delivered`/`read`) extremo a extremo edge→nube. |
| `diagnostics` | Diagnóstico remoto bajo demanda de la flota (Plan 031, ADR-0023): bundle con retención (TTL). |
| `platform` | Plataforma de soporte transversal: `config`, `logging`, `httpapi` (health/admin), `storage/postgres` (runner de migraciones) y `storage/objectstore` (R2). No es un módulo de dominio. |

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
`0036_diagnostics.sql`). El runner de `platform/storage/postgres` los aplica al
arranque y valida `SchemaVersion` (`migrations/version.go`, hoy **0.21.0**) contra
`public.schema_version`; además calcula un hash de los archivos para detectar
cambios aunque no se haya subido la versión. **Obligatorio incrementar
`SchemaVersion` al tocar cualquier `structure/*.sql`.**

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
| `WAPP_JWT_EC_PRIVATE_KEY_FILE` · `WAPP_JWT_KID` · `WAPP_JWT_ISSUER` · `WAPP_SERVICE_JWT_AUDIENCE` · `WAPP_JWT_SECRET` | Firma/validación de tokens (ES256 usuario, HS256 M2M) |
| `WAPP_KEK_KEYRING` · `WAPP_KEK_CURRENT` · `WAPP_KEK_MASTER_B64` · `WAPP_KEK_INDEX_B64` · `WAPP_CLOUD_ENC_PRIVKEY_B64` | Cifrado de PII (KEK versionada, ADR-0017) y clave de tránsito de la nube |
| `WAPP_LEASE_PRIVATE_KEY_FILE` · `WAPP_LEASE_PRIVATE_KEY_B64` · `WAPP_LEASE_TTL_MINUTES` | Firma y vigencia del lease (kill-switch, ADR-0007) |
| `WAPP_PKI_*` | Rutas de la CA y el cert de servidor de la PKI del gateway |
| `WAPP_STORAGE_S3_*` (`BUCKET`, `ENDPOINT`, `ACCESS_KEY_ID`, `SECRET_ACCESS_KEY`, `PRESIGN_EXPIRY`) | Almacén de objetos R2 (media/PDF) |
| `WAPP_RATELIMIT_*` · `WAPP_FLOW_*` · `WAPP_HEALTH_*` · `WAPP_DIAGNOSTICS_*` | Rate-limit de la API pública, runtime del Motor de Flujos, derivación de salud de flota y retención de diagnósticos |

Los secretos (JWT, KEK, lease, credenciales R2) **nunca** se hardcodean ni se
loguean; en `dev`, si faltan, se generan valores efímeros con warning.

## Cómo correr

```bash
# PKI de dev (genera certs/{ca,server}.{crt,key})
scripts/gen-dev-certs.sh

# Arranque
go run ./cmd/server
```

Requisitos locales:

- **PostgreSQL** accesible (en local se comparte el `edugo-postgres`; base
  `wapp_cloud`), o un DSN a Neon vía `WAPP_DB_*`.
- Puertos libres en la **banda 81xx** (8100–8103); coexiste con EduGo en 80xx.
- Config opcional por YAML apuntado con `WAPP_CONFIG_FILE`; lo no cubierto cae a
  los defaults de `config.go`.

## Seguridad

- **Tokens de usuario en ES256** (ECDSA P-256) con `kid` + `MultiVerifier` para la
  coexistencia de algoritmos (ADR-0019, Plan 028); la clave EC privada se lee de un
  PEM con permisos `0600`.
- **Service tokens M2M en HS256** con audiencia acotada (`aud`), separando las
  rutas de máquina de las de usuario.
- **RBAC por grants glob** multi-tenant; el tenant se deriva del token.
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
