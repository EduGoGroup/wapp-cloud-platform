# Changelog — wapp-cloud-platform

El formato sigue [Keep a Changelog](https://keepachangelog.com/es-ES/1.0.0/)
y [Versionado Semántico](https://semver.org/lang/es/).

## [Unreleased]

## [0.3.0] - 2026-10-12

🧭 **Para quien despliega — lo que NO cambió respecto de `v0.2.0`:**

- **Puertos**: los mismos cuatro listeners (`:8100` admin · `:8103` API pública · `:8101` CloudLink
  · `:8102` enrolamiento), con los mismos defaults.
- **Variables de entorno**: ni una nueva ni una retirada. Las 70 claves que lee `config.Load()` son
  las mismas, nombre a nombre (más `WAPP_CONFIG_FILE`). `WAPP_TEST_DB_DSN` era solo de test.
- **Binario**: se sigue construyendo `./cmd/server`; el relevo en UAT no tocó ni la unidad systemd
  ni el `.env`. `go.mod` no mueve ninguna dependencia de producción.
- **Rutas y rpc**: la reconstrucción no añade ni quita ninguna. `TestHuella` compara lo que el
  proceso registra con una dorada del árbol anterior: 95 rutas (22 en `:8100`, 73 en `:8103`).
- ⚠️ **El esquema SÍ cambió, y no por la reconstrucción**: `SchemaVersion` pasa de **0.46.0** a
  **0.48.0** por dos migraciones aditivas del Plan 047 (`0085` y `0086`, en *Added*). La
  reconstrucción modular no trae ninguna migración. UAT ya está en `0.48.0`.

### Added

- **Invitaciones de un solo uso** (Plan 047 · Ola A): `GET`/`POST /api/v1/invitations`,
  `DELETE /api/v1/invitations/{id}` y `POST /api/v1/invitations/accept`. Migración
  **`0085_tenant_invitations.sql`** (`SchemaVersion` **0.47.0**): guarda el SHA-256 del token, nunca
  el token. El token viaja en el cuerpo, no en la URL; 404 y 410 responden el mismo cuerpo.
- **Una persona puede pertenecer a varias empresas** (Plan 047 · Ola 5). Migración
  **`0086_user_active_tenant.sql`** (`SchemaVersion` **0.48.0**). `POST /api/v1/auth/active-tenant`
  elige la empresa y `GET /api/v1/auth/tenants` lista las propias (`[]`, nunca `null`). La segunda
  membresía la gobierna el entitlement `multi_empresa`; sin él, el 409 es el de siempre.
- **Tests de proceso de caja negra** en `test/procesos/`: arrancan `cmd/server` contra un Postgres
  de testcontainers y lo ejercitan por sus puertos, con un Edge simulado y, aparte, con el binario
  real del Edge y un WhatsApp falso. Targets `make test-procesos` y `make test-procesos-edge-real`
  (piden Docker), más `vet-integracion`, `toolchain`, `tools` y `cobertura-ficheros`.

### Changed

- 🏗️ **`internal/` se reconstruyó por módulos de negocio** (2026-09-27 → 2026-10-11): el código vive
  en `internal/modulos/<módulo>/` — `acceso`, `captacion`, `catalogo`, `conversacion`, `edge`,
  `inferencia`, `solicitudes` —, con una sola cara HTTP pública (`internal/apipublica`) y un solo
  arranque (`internal/arranque`). UAT corre este binario desde el 2026-10-11.
- **El arranque son nueve fases**, legibles en `internal/arranque/orquestador.go`, donde antes había
  una sola función. El log deja una línea `arranque: fase completada` por cada una.
- 🔴 **Conducta visible que cambió a propósito.** Inventariada leyendo el código y **todavía sin
  revisar contra los consumidores** (deuda D-36 de `documentations/deuda.md`):
  - **Consolas y BFF** — `POST /api/v1/intakes/{id}/reanalyze`: si falla la composición del literal
    responde **500** `no se pudo pedir el re-análisis de la solicitud` y no abre job. Antes era un
    200 «en curso» cuyo job moría después en el worker.
  - **Consolas y BFF** — `POST /api/v1/signup`: un correo adornado (`Ana <ana@x.com>`,
    `ana@x.com (Ana)`) pasa de 202 a **400**; solo vale la dirección pelada. Los invisibles U+200B
    y U+FEFF de los bordes se recortan, aquí y en el token de `POST /api/v1/invitations/accept`,
    que con ellos pasa de 404 a **204**.
  - **CRM** — `POST /api/v1/integrations/callback`: con el puente encendido y sin `endpoint_url`
    pasa de 200 a **403**. Con `X-Wapp-Timestamp` muy en el futuro —el caso real es mandar
    **milisegundos** en vez de segundos— pasa de 200 a **401**, y se queda así por decisión.
  - **CRM y consola** — un puente sin endpoint o sin secreto ya no encola entregas: los contadores
    de `GET /api/v1/integrations/outbox` dejan de acumular fallidas y muertas para ese tenant.
  - **Edge** — `POST /admin/tenants/revoke` avisa en el acto también a los Edge con sesión viva que
    la flota no lista; una sesión que reconectó antes de cerrarse su stream anterior sigue
    recibiendo tramas; el `ConfigPush` de `PUT /api/v1/intents` sale aunque el cliente HTTP cuelgue.
  - **Edge y cliente final** — al parar, el servidor corta el CloudLink en cuanto no hay nada en
    vuelo, sin agotar el plazo: un entrante a medio procesar puede quedarse sin respuesta.
  - **Datos y log** — un job de captación no pierde un intento por una parada del proceso, y al
    apagar ya no salen los `ERROR` del barrido ni del worker del webhook.
- **Sin cambio, y conviene saberlo** — rarezas que se conservaron y siguen abiertas:
  `GET /api/v1/degradation-notices` da 500 y no 504 al vencer el plazo de BD (D-24); el 403 de plan
  de `PUT /api/v1/intents` es prosa y no `feature_not_enabled` (D-30); el canje calla dos de sus
  fallos en la bitácora (D-44); tres `Help` de `/metrics` nombran rutas ya borradas (D-50).

### Fixed

- **La inferencia ya no va al Edge que dijo que no puede** (Plan 057 · Ola 3): van primero los Edge
  `READY`, después los que no declaran nada, y los `DOWN` quedan fuera. Un Edge que no declara
  readiness no es un Edge caído. El origen vivo de la conversación sigue mandando sin condiciones.

### Removed

- 🔴 **El árbol anterior de `internal/`**: 28 directorios, todos menos `internal/platform`.
- **`make test-integration`, los 91 `*_integration_test.go` y `WAPP_TEST_DB_DSN`.** El gate con base
  de datos es `make test-procesos`; `make ci-local` sigue corriendo sin Postgres, y el job de
  integración de `ci.yml` ya no usa service container. Ningún test apunta a un Postgres vivo.

### Security

- 🔒 **Dos fugas entre inquilinos cerradas** (Plan 057 · Olas 1 y 2; HS-14 y HS-15, ADR-0048). El id
  de control `__wapp_control__`, idéntico en todos los Edge, se registraba como una sesión, sin
  tenant y con política última-gana. Con dos Edge conectados, la respuesta de login de un operador
  —con sus tokens dentro— podía escribirse en el socket de otra empresa, y el catálogo de
  intenciones de una podía aplicarse en el Edge de otra. Ahora la respuesta de auth vuelve por el
  stream que la pidió, y el canal de control no se registra, no se indexa y no recibe lease.
  ⚠️ La verificación en campo con dos Edge conectados a la vez está a medias (Plan 057 · T4.2).
- **`cmd/casebank` redacta tres casos de PII que dejaba en claro**: el segundo de dos JID pegados,
  dos teléfonos seguidos en una racha de dígitos y los teléfonos con dígitos no ASCII.
- **Un rol de empresa ya no puede asignarse con ámbito global** (Plan 047 · Ola 5): la guarda mira
  el id fijo de `platform_admin`, no su nombre, que una dueña puede duplicar en su empresa.

## [0.2.0] - 2026-08-28

### Added

- 🧭 **Los prompts de P2–P5 se ajustan por FICHERO, sin release** (`WAPP_LLM_PROMPTS_DIR`).
  Hasta ahora, cambiar una coma en un prompt costaba release de `llm` + subida de dependencia +
  release del cloud + despliegue. Ahora se edita un fichero y se reinicia. Guía completa en
  `docs/funcionalidades/36-ajuste-de-los-prompts-del-pipeline.md`.
  - `internal/prompts`: lee el directorio, parsea el formato y **valida cada plantilla contra el
    `Parse*` de su etapa** antes de servirla. Nombres `pN-<lo-que-quieras>.tmpl`, donde **solo el
    prefijo es contrato**; no hace falta poner las cuatro etapas.
  - `cmd/prompts`: `-volcar <dir>` escribe los cuatro ficheros con el texto que corre HOY —la
    única forma correcta de empezar, porque escribirlos a mano produce plantillas que nacen
    viejas— y `-comprobar <dir>` valida sin tocar el servicio.
  - `local.ConPlantillas` inyecta los textos en el proveedor; el arranque deja **una línea de
    log con el origen de cada etapa**, que es lo que contesta «¿qué prompt corrió de verdad?».
  - 🔴 **Todo fallo ABORTA EL ARRANQUE**: un prefijo desconocido, dos ficheros para la misma
    etapa, o un esquema que su propio validador rechaza. Nada se degrada a seguir con el texto
    compilado, porque el peor síntoma posible es «edité el prompt, reinicié y no cambió nada»,
    que no deja rastro en ningún log.
  - **Sin la variable no cambia NADA**: corre el texto compilado, y un test exige que volcar y
    cargar dé la plantilla compilada byte a byte —si no, encender la palanca alteraría los
    prompts sin que nadie hubiera editado nada—.
  - **P1 no entra**, a propósito: su prompt lo gobierna el catálogo de intenciones del tenant,
    que ya se edita por API. Meterlo aquí le daría dos fuentes de verdad al mismo texto.

### Fixed

- **`llm` sube a `v0.4.4`: un `qty` ausente CON rango vale 1, y deja de perderse el
  artefacto entero.** El prompt (v0.4.2) no bastó: ante «entre 10 y 12 kilos», `gemma4:e2b`
  emitía el ítem con su `range` correcto y **sin clave `qty`** —coherente con el contrato que
  él lee—, y como `Qty` es un `int`, la ausencia se leía como 0 y `validarNormalizedItem` la
  rechazaba. Los TRES ítems se perdían por esa clave (parseo todo-o-nada, DEUDA-044.16), y
  los otros dos eran impecables.
  - 🔴 **La decisión de `TestP4_CantidadOmitida_EsUnoYNuncaCero` sigue intacta**: sin `range`,
    una `qty` ausente se sigue rechazando y no se persiste nada. El `v0.4.3` la atropelló con
    un default incondicional, ese test se puso rojo al subir la dependencia —que es su
    función— y el `v0.4.4` estrechó el default al único caso donde el «cuánto» está
    demostrablemente en otro campo.

- **`llm` v0.4.2: P4 ya dice qué vale `qty` cuando la cantidad ES un rango.**
  Las reglas del prompt enunciaban «si el cliente no dijo cuántos, `qty` vale 1» y «los
  rangos se conservan como rango», pero ante «entre 10 y 12 kilos» el cliente **sí** dijo
  cuántos, así que el hueco quedaba a interpretación del modelo. En campo el 2026-08-26,
  `gemma4:e2b` y `gemma4:e4b` —dos modelos INDEPENDIENTES, mismo mensaje— lo rellenaron
  igual, `"qty": 0`, y `validarQuantities` lo rechaza. El artefacto P4 se perdía entero por
  ese único campo (parseo todo-o-nada, DEUDA-044.16). El cambio es solo texto del prompt:
  la superficie de `llm` no cambia.

### Added

- **Perfil de sesión: `POST /api/v1/sessions/{id}/profile`** (Plan 046 · T1.2,
  D-046.5). Cuerpo `{"profile":"active"|"passive"}`, respuesta
  `{"session_id","profile"}`. Mismo grant que la ruta que sucede
  (**`sessions.write`**), misma auditoría y mismo aislamiento al tenant del token
  (INV-8): una sesión de otro tenant devuelve **404**, nunca 403 — un 403
  confirmaría que existe. `{"profile":"bot"}` es **400**: `bot` es vocabulario de
  la ruta vieja y no se traduce en silencio.
  Registrada **en las dos vías**, igual que su predecesora: la pública y
  `POST /admin/sessions/{id}/profile`.
- **`GET /api/v1/sessions` publica `profile`** (`active`|`passive`). Va siempre
  presente (la columna es `NOT NULL DEFAULT 'passive'`).
- **Migración `0063_fleet_sessions_profile.sql`** (Plan 046 · T1.1): `fleet_sessions`
  gana la columna `profile TEXT NOT NULL DEFAULT 'passive'` con
  `CHECK (profile IN ('active','passive'))`, backfilleada desde `role`
  (`bot⇒active`, `passive⇒passive`). Aditiva e idempotente bajo el runner
  FULL-REPLAY: la columna nace **sin** default y el backfill lleva
  `WHERE profile IS NULL`, así que el replay del arranque siguiente es un no-op y
  **no vuelca a pasiva** las sesiones vivas. `SchemaVersion` sube a **0.37.0**.

### Removed

- 🔒 **MUEREN LAS DOS COLUMNAS DE PII EN CLARO** (migración
  **`0070_drop_self_pn_y_push_name_en_claro.sql`**, Plan 046 · T5.4, D-046.17;
  `SchemaVersion` **0.44.0**): `public.fleet_sessions.self_pn` y
  `public.contacts.push_name`, que quedaron vivas y vacías tras los backfills
  cifrados de T4.1 y T4.2. A partir de aquí **no hay en toda la base una sola
  columna donde quepa un teléfono o un nombre sin cifrar**.

  **La migración es la mitad pequeña.** En el mismo commit se retira el código Go
  que nombraba esas columnas en su SQL: los dos backfills de arranque
  (`fleet.BackfillSelfPn` y `contact.BackfillPushName`, con sus tests), el
  `self_pn = NULL` de `SetSelfPn` junto con su guarda `OR self_pn IS NOT NULL`, y
  el `push_name = NULL` de `resolveExisting`. Los backfills **bloquean el arranque**
  y abortan el proceso si su consulta falla, así que desplegar el DROP sin ellos
  dejaría la plataforma **sin arrancar** con «column does not exist».

  ⚠️ **El rollback repuebla las columnas, y no es un fallo**: un binario anterior
  aplica su propio directorio embebido —sin la 0070— y la `0028` (o la `0005`)
  las recrea vacías. Este DROP cierra la higiene del esquema, no la puerta del
  rollback.

  ⚠️ **Se perdió la consulta que acreditaba el saneo.** Mientras esas columnas
  existieron, `count(*) WHERE self_pn IS NOT NULL` y su gemela de `push_name` eran
  la prueba de que los backfills funcionaron. Ya no se pueden hacer; su última
  ejecución contra UAT quedó anotada en el journal.

- 🔴 **`POST /api/v1/sessions/{id}/role` y `POST /admin/sessions/{id}/role`:
  RETIRADAS**, junto con el campo `role` de `GET /api/v1/sessions` y la columna
  `fleet_sessions.role` (migración **`0064_drop_fleet_sessions_role.sql`**,
  `SchemaVersion` **0.38.0**).

  **Es un cambio de contrato incompatible, y se hace a propósito en alpha.** La
  0063 conservaba `role` «un ciclo como alias deprecado» para no romper a clientes
  que no se despliegan a la vez que la plataforma. Al comprobar esa premisa contra
  los seis repos del ecosistema, **ese cliente no existe**: el BFF llama a
  `/profile` y no conserva la ruta vieja ni en su propia consola; el proto de
  CloudLink no transporta `role`, así que el Edge nunca lo vio; y ningún otro repo
  lo menciona. Un ciclo de deprecación que no protege a nadie solo cuesta: código,
  tests y una micro-duda de producto (MD-046.2) que existía únicamente por esta
  columna. D-046.1 se revisó y la columna, su tipo Go, sus dos rutas y esa
  micro-duda mueren juntos. Balance: **−667 líneas netas**.

  🔴 **A partir de este despliegue el rollback al binario anterior NO es una
  opción**: leía `COALESCE(role,'bot')`. Volver atrás exige restaurar también la
  columna (la 0025 la recrea sola en el replay).

### Changed

- 🔴 **Una sesión recién emparejada nace PASIVA** (D-07). Es un cambio de
  comportamiento deliberado respecto de la 0025, que traía `DEFAULT 'bot'`: hasta
  que su dueño la active, la sesión **no dispara triggers ni auto-responde**. Las
  sesiones que ya existían **conservan su comportamiento exacto** por el backfill de
  la 0063 (REQ-15) — el default solo alcanza a las filas nuevas.
- **La decisión de negocio se lee de `profile`, y solo de `profile`.** Los cuatro
  lectores del runtime (`tenant_resolver`, `self_numbers`, las constantes de
  `runtime.go` y `reactiveBlocked`) migran a la columna nueva con **semántica byte a
  byte idéntica**. `role` se conserva un ciclo como alias deprecado y **sincronizado
  en escritura**: `SetRole` y `SetProfile` escriben las dos columnas en el **mismo**
  `UPDATE`, así que no hay instante en que se contradigan. Su `DROP` es de un plan
  futuro.

### Deprecated

- **`POST /api/v1/sessions/{id}/role` y `POST /admin/sessions/{id}/role`** quedan
  deprecadas **un ciclo** (D-046.5). **No cambia su contrato**: siguen devolviendo
  **200** con `{"session_id","role"}` y siguen aceptando `bot`|`passive`. Lo que
  se añade es el aviso, **por partida doble y en las CINCO respuestas** (no solo
  en el 200):
  - cabecera `Deprecation: true` (RFC 8594);
  - cabecera `Link: <…/{id}/profile>; rel="successor-version"` (RFC 8288), que
    apunta al sucesor **de la misma vía** — a un operador de `/admin` no se le
    manda a `/api/v1`, que puede no alcanzar;
  - campo `deprecation` en el cuerpo, para el cliente que solo mira el JSON.

  La traducción es `bot ⇒ active` y `passive ⇒ passive`, y ambas rutas escriben
  las dos columnas, así que **no hay ventana en la que `role` y `profile` se
  contradigan**. Conviven a propósito: el BFF y la plataforma no se despliegan a
  la vez, y romper `/role` hoy dejaría al BFF ya desplegado sin poder cambiar un
  perfil hasta su redespliegue. El `DROP` de `role` es de un plan futuro.
- **`sessionDTO.role` de `GET /api/v1/sessions`** queda deprecado por la misma
  razón y **no se retira** en este despliegue: los dos campos viajan juntos y
  dicen lo mismo.

### Notas

- 🔴 El push del cambio de perfil al Edge **nace apagado**: el puerto
  `ProfilePusher` está cableado a `nil` (no-op) en las dos vías. Hasta que **T2.1**
  lo enchufe, un cambio de perfil llega al Edge **en su siguiente conexión**, no al
  instante. Cuando se encienda será *best-effort*: un fallo de push se registra y
  **no** cambia el código de respuesta, porque el perfil ya quedó persistido.
- El entitlement `passive_profiles` **no gatea** estas rutas en v1.

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
