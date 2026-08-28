# CLAUDE.md — wapp-cloud-platform (Piezas 03 y 05)

> Orientado a LLM. Lee esto antes de tocar cualquier archivo.
> `README.md` (este repo) tiene el detalle operativo actual (módulos, listeners, env, migraciones).
> Especificación pieza 03: `../../docs/piezas/03-plataforma-cloud.md`
> Especificación pieza 05: `../../docs/piezas/05-motor-flujos-modulos.md`
> CLAUDE.md raíz del ecosistema: `../../CLAUDE.md`

---

## Qué es esta pieza

**Monolito modular Go** (ADR-0010) que aloja todo lo que gestiona el equipo de wApp
(plataforma SaaS). La nube **piensa**; el Edge despacha (ADR-0005).

**Estado: implementada y en piloto** (esquema en `SchemaVersion`, hoy `0.46.0` —
`internal/platform/storage/postgres/migrations/version.go`; no fijamos aquí un SHA porque
caduca al commit siguiente). NO es scaffold ni greenfield:
cubre IAM, API pública, Motor de Flujos con sus módulos, gateway CloudLink y la
operabilidad de flota; se despliega hoy contra Neon en el piloto multi-Edge
(planes 005–031, 033 y 040). Antes de afirmar que algo "no existe todavía", **verifica en
`internal/`**: casi todo el dominio ya está construido.

Binarios `cmd/server` (la plataforma) y `cmd/migrate` (aplica el esquema y sale),
Go **1.26**, module `github.com/EduGoGroup/wapp-cloud-platform`.

⚠️ **wApp NO autentica personas.** Desde la Ola 5 del Plan 003 de identity, las
credenciales las valida **identity-core** (el SSO del grupo) y aquí no queda
padrón, ni contraseñas, ni sesiones, ni credenciales M2M. Lo que wApp hace es
**canjear** el Identity Token por un **Context Token** propio con el tenant y los
grants de negocio. Si vas a proponer algo que valide una contraseña, resuelva un
usuario o emita un refresh en este repo, estás reintroduciendo lo que la Ola 5
eliminó.

---

## Responsabilidad en wApp

| Qué hace la Plataforma | Qué NO hace |
|---|---|
| Arma el payload completo (destino + contenido + media) | Tocar `whatsmeow` o el socket de WhatsApp |
| Empuja órdenes al Edge por CloudLink | Custodiar la DEK del cliente |
| Recibe eventos entrantes y los procesa (Motor de Flujos) | Depender de RabbitMQ, broker o `edugo-worker` |
| Emite y revoca leases (kill-switch anti-clon) | Guardar el store cifrado del Edge |
| Genera URLs prefirmadas de corta vida (R2 / S3) | Ejecutar lógica en el Edge |
| Fan-out de campañas con goroutines + channels | Usar fan-out por broker/worker externo |

---

## Arquitectura real (modular por capacidad)

> El layout es **modular por capacidad**, NO una hexagonal global domain/app/adapters.
> Cada módulo de `internal/` tiene su frontera, sus tablas y su API. Trabaja **dentro
> del módulo** que corresponda; no crees capas transversales nuevas sin consenso.

```
cmd/server/    → binario principal: orquesta arranque, CUATRO listeners (2 HTTP + 2 gRPC),
                 migraciones al arranque, lease pubkey en log, graceful shutdown.
cmd/migrate/   → aplica las migraciones y SALE (sin listeners). `make migrate` /
                 `make migrate-status`. Contra Neon: host DIRECTO, nunca el -pooler.
internal/
  iam/          → canje Identity Token → Context Token (exchange), delegación del relé del
                  Edge en identity, verificación del Context Token propio y RBAC glob
                  multi-tenant (roles, grants, membresías). NO hay usuarios ni contraseñas.
                  Capas domain/usecase/ports/infra/transport.
  publicapi/    → API pública /api/v1 para terceros (Context Token ES256 + RBAC): sesiones,
                  mensajes, flows, triggers, media, intents, entitlements, diagnostics, health, audit.
  flujos/       → Motor de Flujos (Pieza 05): motor dinámico por puertos ContentSource +
                  EventSink, Registry de módulos enchufables (modules/{menu,survey,cart,media}),
                  más engine, runtime, trigger, content, contact, store, admin.
  gateway/      → terminación gRPC de los Edges (Pieza 02): grpc (CloudLink bidi),
                  enroll (CA + EnrollEdge), lease (kill-switch ADR-0007),
                  fleet (online/offline por sesión), session (streams vivos).
  entitlements/ → derechos/capacidades por tenant: Resolver (Has/ListEffective/CacheTTL) + el
                  middleware RequireFeature que gatea rutas. Ver "Gate de capacidades" abajo.
  intentcfg/    → config de intenciones por tenant (contrato del módulo intents de wapp-shared).
  ingest/       → ingesta de entrantes con dedupe por (session_id, wa_message_id) (tabla ingest_dedupe).
  receipts/     → acuses delivered/read extremo a extremo edge→nube.
  diagnostics/  → diagnóstico remoto bajo demanda de la flota (Plan 031, ADR-0023) con TTL.
  platform/     → soporte transversal (NO es módulo de dominio): config, logging,
                  httpapi (health/admin), storage/postgres (runner de migraciones),
                  storage/objectstore (R2).
```

### Cuatro listeners (un solo proceso)

> **Puertos: banda wApp 81xx** (aparte de EduGo 80xx; ver `../../docs/CONVENCIONES.md`).
> Defaults en `internal/platform/config/config.go`; override por `WAPP_*`.

| Listener | Env | Default | Qué expone |
|---|---|---|---|
| HTTP admin/health (interno) | `WAPP_HTTP_ADDR` | `:8100` | `/healthz` (incluye check de BD), `/admin/*` (kill-switch de leases, envío de mensajes) |
| HTTP API pública | `WAPP_PUBLIC_HTTP_ADDR` | `:8103` | `/api/v1` para terceros (JWT usuario ES256 + RBAC) |
| gRPC Enrollment | `WAPP_GRPC_ENROLL_ADDR` | `:8102` | `EnrollEdge` — **TLS de servidor SOLAMENTE** (el Edge enrola antes de tener cert) |
| gRPC CloudLink | `WAPP_GRPC_CONNECT_ADDR` | `:8101` | `Connect` bidi-stream con **mTLS estricto** (el Edge conecta con el cert emitido) |

PKI de dev: `scripts/gen-dev-certs.sh` genera `certs/{ca,server}.{crt,key}`
(rutas en `WAPP_PKI_*`). `certs/` está fuera de git.

---

## Datos (SIN MongoDB)

**Todo el estado —incluido el conversacional del Motor de Flujos— vive en PostgreSQL**
(JSONB para grafos y estado). **No hay MongoDB.** El único otro almacén es **R2 / S3**
para media/PDF (URLs prefirmadas de corta vida).

| Almacén | Qué guarda | Qué NUNCA guarda |
|---|---|---|
| **PostgreSQL** (Neon en el piloto) | Tenants, IAM/RBAC, contactos (PII cifrada), flujos y su estado, órdenes, fleet de Edges, leases, acuses, dedupe de ingesta, entitlements, configs de intención, diagnósticos | DEK, store cifrado del Edge, llaves privadas |
| **R2 / S3** (Cloudflare R2, S3-compatible) | Media y PDF; se sirven al Edge por URL prefirmada. En alpha comparte bucket con EduGo (`edugo-materials`, prefijo `wapp/`) | Credenciales del cliente, DEK |

**No hay broker**: el fan-out de campañas se hace por **concurrencia Go** (goroutines +
channels), nunca RabbitMQ ni Redis (ADR-0003). La nube no tiene cola durable propia; la
durabilidad la da el `outbox` del Edge.

### Migraciones y SchemaVersion

Los scripts SQL embebidos viven en
`internal/platform/storage/postgres/migrations/structure/` (`0001_*.sql` … la última, que se pregunta al directorio).
El runner los aplica al arranque y valida `SchemaVersion` contra `public.schema_version`; además
hashea los archivos para detectar cambios aunque no se suba la versión. 🔴 **El valor NO se copia
aquí: caduca.** Pregúntalo al código —
`grep -oE 'SchemaVersion = "[0-9.]+"' internal/platform/storage/postgres/migrations/version.go` — y
la última migración con `ls .../migrations/structure/ | tail -1`. (Este párrafo dijo **0.45.0** y
**`0081_…`** hasta el 2026-08-28, cuando el real era **0.46.0** y **`0084_…`**: mismo antídoto que
aplicó `wapp-cloudlink/CLAUDE.md` con su tag.)

**Cuándo hay que subir `SchemaVersion` — la regla honesta.** El runner reaplica por **hash de
contenido**: `isUpToDate` exige versión **Y** hash (`migrations/schema.go:82`), así que tocar un
`structure/*.sql` ya dispara el replay aunque la constante no se mueva. Verificado contra Postgres
real (Plan 041 · T3.3): con la misma versión y el hash alterado, el runner reejecutó **todos** los
`structure/*.sql` sobre una BD **con datos** y las filas sobrevivieron. De ahí las dos mitades:

- Una **ola intermedia** de un plan puede añadir migraciones **sin bump**: es seguro y evita una
  ristra de versiones que no significan nada (el Plan 041 añadió `0041`–`0045` en cuatro olas con
  **un solo** incremento).
- **Publicar un plan sin su bump, no.** Cuando el trabajo sale a `dev`/`main`, `SchemaVersion` tiene
  que reflejar el esquema nuevo: es lo único que un operador puede comparar contra
  `public.schema_version` para saber qué esquema corre en esa base.

En la práctica: **un bump por plan**, en el commit que el plan designe, no uno por migración.

El runner es **full-replay**: al detectar cambio de hash reejecuta TODOS los
`structure/*.sql`, no solo los nuevos. Consecuencia que muerde: un `INSERT ... SELECT`
sobre una tabla que otra migración borra revienta el ARRANQUE del servidor en la
siguiente corrida. Por eso lo destructivo y lo que depende de tablas retiradas va con
guard `IF EXISTS` (ver `0037_tenant_members.sql` y `0038_retiro_iam_propio.sql`).

### Taxonomía de planes y features (Plan 040)

**5 planes × 14 features**, sembrados en `0039_seed_plan_taxonomy.sql` (las 12 originales) y
`0053_seed_survey_media_features.sql` (`survey` nace en `basic`, `media` en `commerce`; Plan 043 ·
Ola 2, porque el despachador filtra el menú por features y esos dos tipos de fábrica no tenían
clave): los comerciales `basic`, `commerce`, `advisor_ai`, `advisor_ai_pro`, más `pro` = plan
**interno de laboratorio** con las 14 claves. Los comentarios de la propia `0039` siguen diciendo
«12» a propósito: cambiarlos alteraría su hash y forzaría un full-replay del esquema en todos los
entornos por una línea de comentario. `basic` y `pro` ya existían desde la `0032` y se **reutilizan** (no se re-crean ni se
renombran); la `0039` solo inserta los tres nuevos.

Dos cosas que se malinterpretan al tocar esto:

- **No hay herencia en BD.** La composición está denormalizada: cada plan lista TODAS sus features,
  y la notación «Comercio + …» del design es documental. El lookup es un JOIN plano
  (`internal/entitlements/postgres.go:235`); una herencia implícita obligaría al Resolver a recursión.
- **El override manda** (ADR-0022): `tenant_features` gana sobre el plan; sin override mandan las del
  plan, y un tenant sin `plan_id` se trata como `basic` (`COALESCE`, `postgres.go:151`).
  `passive_profiles` y `multi_empresa` no están en ningún paquete comercial: son add-on por tenant.

El Resolver cachea en memoria (`Has` por (tenant,feature) y `ListEffective` por tenant), TTL 60 s por
defecto (`postgres.go:15`), y publica ese TTL por `CacheTTL()` para que los clientes lo respeten.

⚠️ Las tablas `iam_roles`, `iam_role_grants`, `iam_user_roles` e `iam_user_grants`
**NO son identidad**: son el RBAC de negocio de wApp y se quedan. Su prefijo `iam_` es
herencia del Plan 018 y hoy engaña. `iam_users`, `iam_refresh_tokens` e `iam_api_keys`
murieron en la 0038. Su `user_id` es un UUID SIN FK: la persona vive en otra base.

---

## Seguridad

- **Las credenciales las valida identity-core**, no wApp. Aquí no hay `password_hash`,
  ni refresh tokens, ni api-keys: el Plan 003 de identity se los llevó.
- **Context Tokens en ES256** (ECDSA P-256) con `kid` + `MultiVerifier` (ADR-0019, Plan
  028); la clave EC privada se lee de un PEM `0600`. Es el ÚNICO algoritmo de firma que
  le queda a wApp — el HS256 se retiró con el plano M2M.
- **Sin plano M2M**: `X-API-Key` y los service tokens se eliminaron (0 credenciales
  emitidas, 0 consumidores). Reconstruirlo es por el ADR-0025 de identity: la credencial
  de máquina **se canjea, no se presenta**.
- **RBAC por grants glob** multi-tenant; el tenant se deriva del token (INV-8).
- **Gate de capacidades — no lo confundas con el RBAC.** El grant dice "puedes operar esto"; la
  feature dice "tu plan lo incluye". Son dos preguntas distintas y **ninguna sustituye a la otra**:
  una ruta de pago lleva las dos. Se aplica con `entitlements.RequireFeature(resolver, feature)`
  (`internal/entitlements/middleware.go:33`), que sin la feature corta con `403` y
  `{"error":"feature_not_enabled","feature":"<clave>"}`. Es **FAIL-CLOSED** en los tres modos de
  no-resolución —sin identidad (o sin tenant en ella), resolver caído, resolver `nil`—: los tres dan
  403, nunca 500. No lo "arregles" devolviendo 5xx cuando el resolver falla: el llamante no debe
  poder distinguir "no lo tienes" de "no pude averiguarlo", y un 5xx invita a reintentar hasta
  colarse.
- **Dedupe de ingesta** por `(session_id, wa_message_id)` para no reprocesar entrantes repetidos.
- **PII cifrada en reposo** con KEK versionada y rotación sin re-cifrar (ADR-0017, Plan 012),
  más índice ciego HMAC con `indexKey` independiente.
- **Zero-knowledge** (ADR-0007): la DEK y el store cifrado del Edge nunca llegan a la nube;
  el gateway solo emite y revoca leases. No propongas persistir la DEK aquí.

---

## Motor de Flujos (Pieza 05) — resumen

Módulo `internal/flujos/`. **Motor dinámico hexagonal** (Plan 015) por puertos
`ContentSource` + `EventSink`, con un `Router` que elige adapter por `node.Content.Source`
y un `Registry` de módulos enchufables (`WaitsForInput`). El estado conversacional por
`(tenant, sesión, contacto)` vive en **PostgreSQL** (no MongoDB).

| Módulo (`flujos/modules/`) | Hace | Estado |
|---|---|---|
| `menu` | Lista numerada → rama por elección | Implementado |
| `survey` | Secuencia de preguntas, recolecta respuestas | Implementado |
| `cart` | Carrito sobre catálogo (sub-máquina jerárquica), proyecta a `orders` | Implementado |
| `media` | Entrega URL prefirmada (R2) al Edge | Implementado |
| Pago | Cobro y conciliación | Futuro |

Menús/encuestas se renderizan en texto numerado; el módulo decide el render según
capacidades de la sesión.

---

## Clasificador de intenciones (LLM) — quién hace qué

- El **clasificador LLM local corre en el Edge** (repo hermano `edge/wapp-edge-intent`,
  Ollama/`qwen3:1.7b`, ADR-0020/Plan 029), NO en esta plataforma.
- La nube aporta: `entitlements` (gate de la feature por tenant), `intentcfg` (catálogo de
  intenciones que se empuja al Edge por `ConfigUpdate kind:intents`, ADR-0021) e `ingest`
  del `ClassifiedIntent` que el Edge devuelve. La nube **decide el flujo**; el Edge **comprende**.

---

## Qué reutiliza de EduGo (copia-adaptación, ADR-0004)

Se **copia y adapta**, nunca se importa un paquete `edugo-*` como dependencia. Origen del
IAM/RBAC: patrón de `edugo-api-identity`; el `ProcessorRegistry` de `edugo-worker` se
reimplementó sobre concurrencia Go **sin RabbitMQ**. El código compartido **interno** de wApp
vive en `github.com/EduGoGroup/wapp-shared` (módulo propio, **no** `edugo-shared`).

---

## Decisiones (ADRs) que gobiernan este repo

| ADR | Decisión | Impacto |
|---|---|---|
| ADR-0003 | Sin Redis ni broker; fan-out por goroutines | `DispatchCampaign` usa worker pool acotado, no RabbitMQ |
| ADR-0005 | Edge = despachador; la nube arma el payload completo | Nunca dejar que el Edge llame endpoints de negocio |
| ADR-0007 | Zero-knowledge: nube emite lease, nunca la DEK | El gateway emite/revoca leases; la DEK jamás llega aquí |
| ADR-0009 | Datos de negocio en la nube; DEK y store solo en el Edge | PostgreSQL/R2 solo con metadatos y contenido de negocio |
| ADR-0010 | Monolito modular; extraer a servicio solo cuando duela | No partir módulos por adelantado |
| ADR-0017 | Cifrado de PII en reposo con KEK versionada | Rotación sin re-cifrar (Plan 012) |
| ADR-0019 | JWT de usuario HS256 → **ES256** (`kid` + `MultiVerifier`) | Hoy ES256 es el único: el HS256 se fue con el plano M2M |
| ADR-0020/0021/0022 | Clasificador LLM (entitlements, contrato `intents`, config e ingesta) | El clasificador vive en el Edge; aquí el soporte |
| ADR-0023 | Operabilidad de flota (salud derivada + diagnóstico remoto) | Módulo `diagnostics` con TTL (Plan 031) |

---

## Puntos abiertos (no implementar sin consenso)

- Cadencia de renovación del lease y operación offline con lease cacheado (ADR-0007).
- Corte exacto para extraer módulos a servicio aparte (ADR-0010).
- Fan-out: límite de paralelismo por tenant/Edge para no saturar ni provocar bloqueos.

---

## Referencias

- `README.md` (este repo) — módulos, listeners, env y migraciones al día.
- Pieza 03: `../../docs/piezas/03-plataforma-cloud.md` · Pieza 05: `../../docs/piezas/05-motor-flujos-modulos.md`
- CloudLink (conducto edge↔cloud): `../../docs/piezas/02-cloudlink.md` · Edge Agent: `../../docs/piezas/01-edge-agent.md`
- ADRs: `../../docs/adr/` · CLAUDE.md raíz: `../../CLAUDE.md`
