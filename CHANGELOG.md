# Changelog — wapp-cloud-platform

El formato sigue [Keep a Changelog](https://keepachangelog.com/es-ES/1.0.0/)
y [Versionado Semántico](https://semver.org/lang/es/).

## [Unreleased]

## [0.1.0] - 2026-08-13

Primer tag de este repositorio. `wapp-cloud-platform` viene operando desde su
origen **sin versionar** —cero tags, cero CHANGELOG—, así que esta entrada no
resume cambios frente a una versión previa: resume el **estado en que se
corta** la plataforma al estrenar versionado semántico, con `SchemaVersion` en
`0.33.0` (`internal/platform/storage/postgres/migrations/version.go`).

### Added

- **IAM y Context Token** (`internal/iam`). wApp no autentica personas: desde
  el Plan 003 de identity, las credenciales las valida identity-core (el SSO
  del grupo) y aquí no queda padrón, contraseñas ni refresh tokens. El módulo
  canjea el Identity Token por un Context Token propio firmado en **ES256**
  (ECDSA P-256, `kid` + `MultiVerifier`, ADR-0019) con el tenant y los grants
  de negocio, y autoriza por **RBAC de grants glob multi-tenant** (roles,
  grants, membresías), con el tenant siempre derivado del token (INV-8).
- **API pública `/api/v1`** (`internal/publicapi`), protegida con el Context
  Token + RBAC: sesiones, mensajes, flows, triggers, media, intents,
  entitlements, diagnostics, health y audit para terceros.
- **Motor de Flujos** (`internal/flujos`, Pieza 05): motor dinámico por
  puertos `ContentSource` + `EventSink`, con un `Router` y un `Registry` de
  módulos enchufables — `menu` (lista numerada → rama por elección), `survey`
  (secuencia de preguntas), `cart` (carrito sobre catálogo, proyecta a
  `orders`) y `media` (entrega de URL prefirmada R2). El estado conversacional
  por `(tenant, sesión, contacto)` vive entero en PostgreSQL.
- **Gateway CloudLink** (`internal/gateway`, Pieza 02): terminación gRPC de
  los Edges — `grpc` (stream bidi `Connect` con mTLS), `enroll` (CA +
  `EnrollEdge`), `lease` (kill-switch anti-clon, ADR-0007), `fleet`
  (online/offline por sesión) y `session` (registro de streams vivos).
- **Entitlements** (`internal/entitlements`): taxonomía comercial de **5
  planes × 14 features** (`basic`, `commerce`, `advisor_ai`, `advisor_ai_pro`
  y el `pro` interno de laboratorio), con override por tenant (ADR-0022) y el
  middleware `RequireFeature` que gatea rutas de pago **fail-closed** en los
  tres modos de no-resolución (sin identidad, resolver caído, resolver
  `nil`): siempre 403, nunca 500.
- **Clasificador de intenciones — soporte cloud** (`internal/intentcfg`,
  `internal/ingest`): catálogo de intenciones por tenant que se empuja al
  Edge por `ConfigUpdate` (ADR-0021) y la ingesta del `ClassifiedIntent` que
  el Edge devuelve; el clasificador en sí corre en `edge/wapp-edge-intent`
  (ADR-0020).
- **Recepción e ingesta**: dedupe de entrantes por `(session_id,
  wa_message_id)` (`internal/ingest`, tabla `ingest_dedupe`) y acuses
  `delivered`/`read` extremo a extremo (`internal/receipts`).
- **Operabilidad de flota** (`internal/diagnostics`, ADR-0023): salud de
  sesión derivada del heartbeat y diagnóstico remoto bajo demanda, con
  retención por TTL.
- **Almacenamiento**: todo el estado —incluido el conversacional— vive en
  **PostgreSQL** (Neon en el piloto; sin MongoDB); media y PDF se sirven por
  URL prefirmada de **R2/S3** de corta vida. Sin broker: el fan-out de
  campañas es concurrencia Go pura, nunca RabbitMQ ni Redis (ADR-0003).
- **PII cifrada en reposo** con KEK versionada y rotación sin re-cifrar
  (ADR-0017, Plan 012), más índice ciego HMAC con clave independiente.

### Plan 055 — kill-switch anti-clon (lo último que entra en este corte)

- **Clave de firma del lease estable entre reinicios**: el arranque en `prod`
  falla si falta `WAPP_LEASE_PRIVATE_KEY_FILE`/`_B64` en vez de generar una
  clave efímera en silencio, el mismo patrón que ya aplicaba la clave JWT; en
  `dev` sigue generando una efímera con warning.
- **La revocación pasa a ser estado persistido**, no el efecto de un solo
  ciclo: antes de emitir o renovar un lease, el gateway consulta si el
  `(tenant, edge)` ya está revocado y, de estarlo, no vuelve a emitir — el
  kill-switch deja de borrarse solo en el siguiente reinicio del Edge.
- **Dos sujetos de corte**: además de la instalación (`edge_id`), el corte
  alcanza al **tenant completo** (migración `0058_tenants_revocation.sql`,
  `tenants.revoked_at`), con superficie de administración para revocar de una
  vez todas las instalaciones de un tenant.
- **TTL del lease sube a 15 minutos** (D-055.7, decisión de Jhoan del
  2026-08-13), unificando la constante compilada con el valor operativo.
- **Plano de plataforma** (ADR-0039, migración `0059_platform_admin.sql`):
  wApp pasa a ser un tenant más (`wapp-platform`) con un permiso propio
  `*.any` que ningún rol de cliente tiene, para que la plataforma pueda cortar
  a una empresa tercera sin que esa empresa pueda desrevocarse a sí misma.
