# FX · El mapa ruta a ruta — qué ruta se muda a `internal/apipublica`, y en qué fase

> **Autoridad para F2–F8.** Cada fase de módulo muda **exactamente** las filas que aquí llevan su
> número en la columna «Muda en». Una ruta que no está en su fila no se muda; una que falta en la
> huella al cerrar la fase es un defecto. Si el código dice otra cosa que esta tabla, **manda el
> código**: se corrige la tabla en el mismo commit y se avisa en «Contradicciones» del
> [`README.md`](README.md).
>
> Medido sobre `dev` @ `1b18932` el 2026-09-27/28. Rutas relativas a la raíz del repo.

## 0 · Cómo se contó

**Regla** (la del ecosistema): cuenta un **registro que ocurre en ejecución** con el cableado de
producción, **una vez por patrón**, aunque el patrón aparezca en dos ramas de un `if`. Un bucle
sobre N elementos contaría N (no hay ninguno: `grep -n 'for .*range'` sobre los cinco ficheros de
registro → vacío). Un comentario que contiene `mux.Handle` no cuenta. Un `Register` que solo llaman
tests no cuenta.

```bash
grep -c 'mux\.Handle(' internal/publicapi/publicapi.go internal/publicapi/roleplane.go \
  internal/publicapi/eventstelemetry.go internal/iam/transport/http/auth.go \
  internal/bootstrap/arranque/rutas_admin.go          # → 51 · 14 · 1 · 2 · 22
grep -c 'publicMux\.Handle(' internal/bootstrap/arranque/http.go   # → 6 líneas, 5 patrones
```

| Origen | Líneas | Patrones en ejecución | Listener |
|---|---:|---:|---|
| `internal/publicapi/publicapi.go` (`Register` y sus `register*`) | 51 | 51 | `:8103` |
| `internal/publicapi/roleplane.go` (`registerRolePlane`) | 14 | 14 | `:8103` |
| `internal/publicapi/eventstelemetry.go:260` | 1 | 1 | `:8103` |
| `internal/iam/transport/http/auth.go:49-50` (`iamhttp.Register`) | 2 | 2 | `:8103` |
| `internal/bootstrap/arranque/http.go:84-168` | 6 | **5** (`POST /api/v1/signup` en las dos ramas de `:163`) | `:8103` |
| `internal/bootstrap/arranque/rutas_admin.go:64-112` | 22 | 22 | `:8100` |
| **Total** | 96 | **95** (73 públicas + 22 admin) | |

**Contraste con `documentations/contratos.md` («95 rutas»)**: **el número coincide**, pero la
tabla de cómo-se-contó de `contratos.md` cita ficheros que ya no existen con ese nombre:
`internal/bootstrap/http.go` → hoy `internal/bootstrap/arranque/http.go`, y
`internal/bootstrap/bootstrap.go → registerAdminRoutes (:1425-1476)` → hoy
`internal/bootstrap/arranque/rutas_admin.go:63-114`. Las líneas de `publicapi.go` y `roleplane.go`
que cita sí coinciden con `1b18932`. Ninguna ruta de `internal/publicapi` se monta en `:8100`: el
listener admin lo cablea entero el arranque, aunque **reutiliza** handlers de
`internal/flujos/admin` que la API pública también monta (§3).

🔴 **Condicionales.** 55 de las 73 públicas (las 14 de `roleplane.go`, 40 de las 51 de `publicapi.go` y la de telemetría) se montan solo si su dependencia de `publicapi.Deps`
no es `nil` (la ausencia da **404 de ruta inexistente**, no error). Con el cableado de producción
(`internal/bootstrap/arranque/fase8_transporte.go:171-266` más `http.go:37-78`) **todas** llegan
no-nil, así que en UAT se registran las 73. La cara nueva **conserva cada condición de montaje tal
cual** (es comportamiento observable): columna «Monta si».

## 1 · Leyenda

- **Fase del módulo** (`05` §6): F2 `acceso` · F3 `edge` · F4 `inferencia` · F5 `catalogo` ·
  F6 `solicitudes` · F7 `captacion` · F8 `conversacion`. `platform` y `nucleo` (F1) no fuerzan
  nada: `platform` lo comparten los dos arranques y `nucleo/contact` solo lo usa una ruta de F8.
- **Mínima** = la fase del **último** módulo cuyo código usa el handler (tipos en su firma, errores
  centinela que compara, funciones que llama). **Muda en** = la fase planificada, que es la mínima
  salvo que un **singleton** obligue antes (regla de D-10) o que la **familia** la retrase (§4).
- **Singleton**: `gw` = `*gatewaygrpc.Server` (conexiones vivas de los Edge, registro de sesiones,
  aforo de plaza, inferencias pendientes; `internal/bootstrap/arranque/fase4_gateway.go:45`) ·
  `rt` = `*flowruntime.Runtime` (ventanas del agregador, candados por clave, rachas;
  `fase7_flujos.go:105`). **Directo** = entra en `publicapi.Deps` y el handler lo llama.
  **Transitivo** = lo guarda un servicio viejo que el handler llama (se resuelve inyectando el
  singleton nuevo en el servicio viejo, [`arquitectura.md`](arquitectura.md) §4).
- Cadena: **W** = `protect` (accessLog → Authenticate → anotarTenant → RequirePermission →
  AuditMiddleware) · **R** = `protectRead` (igual, sin auditoría) · **A** = `Authenticate` a secas
  · **—** = sin cadena de sesión. Entre corchetes, el gate de feature que va **dentro** de la
  cadena (`entitlements.RequireFeature`).
- Líneas: `publicapi.go:N` salvo que se diga otro fichero.

## 2 · Las 73 rutas del listener público `:8103`

### 2.1 · Autenticación y empresa — las 7 que registra el arranque (hoy fuera de `publicapi`)

| # | Método + patrón | Registro | Handler (fichero · función) | Cadena · permiso | Dominio (módulo) | Singleton | Mín. | **Muda en** |
|---|---|---|---|---|---|---|---|---|
| A1 | `/api/v1/auth/verify` (sin método) | `iam/transport/http/auth.go:49` | `auth.go` · `AuthHandler.Verify` | — pública | `iam/ports/in` (acceso) | — | F2 | **F2** |
| A2 | `/api/v1/auth/exchange` (sin método) | `auth.go:50` | `auth.go` · `AuthHandler.Exchange` | — pública | `iam/ports/in`, canje (acceso) | — | F2 | **F2** |
| A3 | `/api/v1/auth/whoami` (sin método) | `arranque/http.go:84` | `platform/httpapi/authmw.go:147` · `WhoAmIHandler` | A | solo `platform` | — | F0 | **F2** (va con su grupo) |
| A4 | `POST /api/v1/invitations/accept` | `http.go:115` | `iam/transport/http/canje.go:119` · `InvitationRedeemHandler.Accept` | A | iam canje + `entitlements` (acceso) | — | F2 | **F2** |
| A5 | `POST /api/v1/auth/active-tenant` | `http.go:144` | `iam/transport/http/active_tenant.go:141` · `Select` | A | iam (acceso) | — | F2 | **F2** |
| A6 | `GET /api/v1/auth/tenants` | `http.go:153` | `active_tenant.go:189` · `List` | A | iam (acceso) | — | F2 | **F2** |
| A7 | `POST /api/v1/signup` | `http.go:165` / `:168` | `platformadmin/signup.go` · `SignupHandler` · o 503 fijo sin M2M | — pública; limitador propio por IP 1/60 s, ráfaga 5 (`http.go:164`) | `platformadmin`, M2M de iam (acceso) | limitador (estado propio de la ruta) | F2 | **F2** |

Monta si: A1–A3, A5–A7 siempre; A4 y A5–A6 siempre que la BD esté (un fallo de construcción aborta
el arranque, `http.go:111-142`); A7 elige rama por `as.m2mClient != nil`.

### 2.2 · Roles, miembros e invitaciones (`roleplane.go`) — 14

Handlers en `internal/iam/transport/http/{roles,invitations}.go`; `roleplane.go` solo decide
patrón, scope y recurso de auditoría. Dominio: `iam/ports/in` (acceso). Sin singleton. **Mín. y
muda: F2** en las 14.

| # | Método + patrón | Línea | Handler | Cadena · permiso (recurso) | Monta si |
|---|---|---|---|---|---|
| B1 | `GET /api/v1/roles` | `roleplane.go:88` | `RoleAdminHandler.List` | R `roles.read` | `Roles` |
| B2 | `POST /api/v1/roles` | `:90` | `.Create` | W `roles.write` (`role`) | `Roles` |
| B3 | `POST /api/v1/roles/{id}/grants` | `:96` | `.AddRoleGrant` | W `roles.write` (`role_grant`) | `Roles` |
| B4 | `DELETE /api/v1/roles/{id}/grants` | `:98` | `.RemoveRoleGrant` | W `roles.write` (`role_grant`) | `Roles` |
| B5 | `POST /api/v1/members/{user_id}/roles` | `:104` | `.AssignRole` | W `roles.write` (`user_role`) | `Roles` |
| B6 | `DELETE /api/v1/members/{user_id}/roles/{role_id}` | `:106` | `.UnassignRole` | W `roles.write` (`user_role`) | `Roles` |
| B7 | `POST /api/v1/members/{user_id}/grants` | `:113` | `.AddUserGrant` | W `roles.write` (`user_grant`) | `Roles` |
| B8 | `DELETE /api/v1/members/{user_id}/grants` | `:115` | `.RemoveUserGrant` | W `roles.write` (`user_grant`) | `Roles` |
| B9 | `GET /api/v1/members` | `:127` | `MembershipHandler.List` | R `members.read` | `Members` |
| B10 | `POST /api/v1/members` | `:132` | `.Add` | W `members.write` (`member`) — **503 sin M2M, nunca 404** | `Members` |
| B11 | `DELETE /api/v1/members/{user_id}` | `:134` | `.Remove` | W `members.write` (`member`) | `Members` |
| B12 | `GET /api/v1/invitations` | `:154` | `InvitationHandler.List` | R `members.read` | `Invitations` |
| B13 | `POST /api/v1/invitations` | `:156` | `.Issue` | W `members.write` (`invitation`) | `Invitations` |
| B14 | `DELETE /api/v1/invitations/{id}` | `:160` | `.Revoke` | W `members.write` (`invitation`) | `Invitations` |

### 2.3 · Auditoría y derechos — 2

| # | Método + patrón | Línea | Handler | Cadena · permiso | Dominio | Singleton | Mín. | **Muda en** |
|---|---|---|---|---|---|---|---|---|
| C1 | `GET /api/v1/audit` | `:604` | `audit.go` · `listAuditHandler` | R `audit.read` | `iam/domain.AuditEvent` vía `AuditReader` = el auditor (acceso) | — | F2 | **F2** |
| C2 | `GET /api/v1/entitlements` | `:559` | `entitlements.go` · `listEntitlementsHandler` | R `entitlements.read` | `EntitlementsResolver` (acceso) — caché TTL 60 s (`entitlements/postgres.go:15`) | — | F2 | **F2** |

### 2.4 · Edge: mensajes, sesiones y diagnóstico — 6

| # | Método + patrón | Línea | Handler | Cadena · permiso (recurso) | Dominio (módulo) | Singleton | Mín. | **Muda en** |
|---|---|---|---|---|---|---|---|---|
| D1 | `POST /api/v1/messages` | `:443` | `messages.go` · `messagesHandler` | W `messages.send` (`message`) | `gateway/fleet` (`SessionLister`), `gateway/session` (compara 3 centinelas, `messages.go:212-221`) — edge | **gw directo** (`Sender`) | F3 | **F3** |
| D2 | `GET /api/v1/sessions` | `:508` | `sessions.go` · `listSessionsHandler` + `health.go` | R `sessions.read` | `fleet.Session` (edge) | — | F3 | **F3** |
| D3 | `POST /api/v1/sessions/{id}/profile` | `:518` | `flujos/admin/sessions.go:116` · `SetSessionProfileHandler` | W `sessions.write` (`session`) | `fleet.Profile` (edge); `filtercfg.Pusher` (edge) | **gw transitivo** vía `ProfilePush` (`fase8_transporte.go:85`) | F3 | **F3** — el handler se **porta** desde `flujos/admin` a `apipublica/sessionadmin.go` (D-FX-2) |
| D4 | `POST /api/v1/sessions/{id}/status` | `:547` | `flujos/admin/sessions.go:189` · `SetSessionStatusHandler` | W `sessions.write` (`session`) | `fleet.State` (edge) | — | F3 | **F3** — ídem |
| D5 | `POST /api/v1/sessions/{id}/diagnostics` | `:535` | `diagnostics.go` · `requestDiagnosticsHandler` | W `diagnostics.request` (`session`) | `diagnostics`, `fleet` (edge) | **gw directo** (`DiagnosticsRequester`) | F3 | **F3** |
| D6 | `GET /api/v1/diagnostics/{command_id}` | `:538` | `diagnostics.go` · `getDiagnosticsHandler` | **W** `diagnostics.request` (`diagnostics`) — lectura auditada a propósito | `diagnostics` (3 centinelas, `diagnostics.go:244-250`) | — | F3 | **F3** |

Monta si: D2 `Sessions` · D3 `SessionProfiles` · D4 `SessionStatus` · D5 y D6 los **tres**
`Diagnostics`, `DiagnosticsRequester` y `Sessions` (`:530`) · D1 siempre.

### 2.5 · Intenciones — 2 (el caso con conflicto)

| # | Método + patrón | Línea | Handler | Cadena · permiso | Dominio (módulo) | Singleton | Mín. | **Muda en** |
|---|---|---|---|---|---|---|---|---|
| E1 | `GET /api/v1/intents` | `:571` | `intents.go` · `getIntentsHandler` | R `intents.read` | `intentcfg` (captación) | — | F7 | **F3** (familia de E2) |
| E2 | `PUT /api/v1/intents` | `:573` | `intents.go` · `putIntentsHandler` | W `intents.write` (`intents`) + gate `llm_intent` **dentro** del handler | `intentcfg` (captación), `entitlements` (acceso), `wapp-shared/intents` | **gw directo** (`ConfigPush`, best-effort: su error solo se registra) | F7 | **F3**, con **puente declarado** `apipublica → internal/intentcfg` que se retira en F7 |

Monta si: `Intents` **y** `Entitlements` (`:570`). 🔶 **Decisión D-FX-1** del
[`README.md`](README.md): la alternativa es dejar E1–E2 en la cara vieja hasta F7 con el `gw`
nuevo inyectado por su puerto estructural. Mientras no se decida, **manda D-10 literal: F3**.

### 2.6 · Inferencia — 4

| # | Método + patrón | Línea | Handler | Cadena · permiso (recurso) [gate] | Dominio | Singleton | Mín. | **Muda en** |
|---|---|---|---|---|---|---|---|---|
| F1 | `GET /api/v1/tenant-llm` | `:986` | `tenantllm.go` · `getTenantLLMHandler` | R `llm.read` [`api_llm`] | `tenantllm` (inferencia) | — | F4 | **F4** |
| F2 | `PUT /api/v1/tenant-llm` | `:988` | `putTenantLLMHandler` | W `llm.write` (`tenant_llm`) [`api_llm`] | ídem | — | F4 | **F4** |
| F3 | `DELETE /api/v1/tenant-llm` | `:990` | `deleteTenantLLMHandler` | W `llm.write` (`tenant_llm`) [`api_llm`] | ídem | — | F4 | **F4** |
| F4 | `GET /api/v1/degradation-notices` | `:1030` | `degradationnotices.go` · `listDegradationNoticesHandler` | R `llm.read` [`llm_intake`] | `degradation` (inferencia) | — | F4 | **F4** |

Monta si: F1–F3 `TenantLLM` y `Entitlements` · F4 `DegradationNotices` y `Entitlements`.

### 2.7 · Solicitudes — 18

Gate de toda la bandeja: `cart_basic` (`:652`); el export, `intakes_export` (`:653`). Dominio
común: `intakes` (solicitudes) + `entitlements` (acceso). **gw transitivo** en las que avisan al
cliente o al dueño: el `intakes.Service` y el `CRMNotify` guardan un `intakes.Notifier` que envía
por el gateway (`fase6_solicitudes.go:42`).

| # | Método + patrón | Línea | Handler | Cadena · permiso (recurso) [gate] | Dominio extra | Mín. | **Muda en** |
|---|---|---|---|---|---|---|---|
| G1 | `GET /api/v1/intakes` | `:656` | `intakes.go` · `listIntakesHandler` | R `intakes.read` [`cart_basic`] | — (la lectura dispara recordatorios: gw transitivo) | F6 | **F6** |
| G2 | `GET /api/v1/intakes/{id}` | `:658` | `getIntakeHandler` + `intakes_llm_gate.go` · `aplicarGateLLMIntake` | R `intakes.read` [`cart_basic`] | — | F6 | **F6** |
| G3 | `POST /api/v1/intakes/{id}/status` | `:660` | `setIntakeStatusHandler` | W `intakes.write` (`intake`) [`cart_basic`] | — | F6 | **F6** |
| G4 | `PUT /api/v1/intakes/{id}/items` | `:670` | `putIntakeItemsHandler` | W `intakes.write` (`intake`) [`cart_basic`] | `cart.SanitizeNote`, `cart.MaxNoteRunes` (`intakes.go:666-677`) → `solicitudes/intakes/note.go` en F6 (`04` §5.3) | F6 | **F6** |
| G5 | `POST /api/v1/intakes/{id}/approve` | `:686` | `approveIntakeHandler` | W `intakes.write` (`intake`) [`cart_basic`] | — | F6 | **F6** |
| G6 | `POST /api/v1/intakes/{id}/request-info` | `:701` | `requestInfoIntakeHandler` | W `intakes.write` (`intake`) [`cart_basic`] | — | F6 | **F6** |
| G7 | `POST /api/v1/intakes/{id}/quote-suggestion` | `:770` | `quotesuggestion.go` · `quoteSuggestionHandler`, envuelto **por fuera** en `plazoescritura.go:125` · `conPlazoDeRedacción` | R `intakes.read` [`cart_basic` + `llm_intake`] | `intakes/quotetext` (solicitudes); la constante `pipeline.PlazoPorLlamadaSuelo` (captación, `plazoescritura.go:87`); gw transitivo por el selector LLM | F7 (por la constante) | **F6**, con el plazo **inyectado** por el arranque (§4.3) |
| G8 | `POST /api/v1/intakes/discard` | `:784` | `discardIntakesHandler` | W `intakes.write` (`intake`) [`cart_basic`] | — | F6 | **F6** |
| G9 | `GET /api/v1/intakes/export` | `:788` | `export.go` · `exportIntakesHandler` | R `intakes.read` [`intakes_export`] | — | F6 | **F6** |
| G10 | `GET /api/v1/intakes/summary.json` | `:790` | `summary.go` · `intakeSummaryHandler` | R `intakes.read` [`intakes_export`] | — | F6 | **F6** |
| G11 | `GET /api/v1/tenant-variables` | `:881` | `tenantvariables.go` · `getTenantVariablesHandler` | R `content.read` | `tenantvars` | F6 | **F6** |
| G12 | `PUT /api/v1/tenant-variables` | `:883` | `putTenantVariablesHandler` | W `content.write` (`tenant_variables`) | `tenantvars` | F6 | **F6** |
| G13 | `GET /api/v1/integrations` | `:923` | `integrations.go` · `getIntegrationHandler` | R `integrations.read` [`crm_bridge`] | `integrations` | F6 | **F6** |
| G14 | `GET /api/v1/integrations/outbox` | `:929` | `getOutboxHandler` | R `integrations.read` [`crm_bridge`] | `integrations` | F6 | **F6** |
| G15 | `PUT /api/v1/integrations` | `:931` | `putIntegrationHandler` | W `integrations.write` (`integration`) [`crm_bridge`] | `integrations` | F6 | **F6** |
| G16 | `DELETE /api/v1/integrations` | `:933` | `deleteIntegrationHandler` | W `integrations.write` (`integration`) [`crm_bridge`] | `integrations` | F6 | **F6** |
| G17 | `POST /api/v1/integrations/callback` | `:1055` | `crmcallback.go` · `crmCallbackHandler` | 🔴 **sin JWT**: solo `accessLog`; HMAC `sigv1`, ±300 s, cuerpo ≤ 64 KiB | `integrations`, `integrations/sigv1`, `intakes` (reflector y notificador: gw transitivo) | F6 | **F6** |
| G18 | `GET /api/v1/events/telemetry` | `eventstelemetry.go:260` | `eventTelemetryHandler` + su adaptador SQL `eventstelemetry_store.go` · `PostgresEventTelemetryStore` | R `events_telemetry.read` | `intakes.MaxExportIntakes` (`eventstelemetry.go:129`); lee `flow_events` por SQL propio | F6 | **F6** |

Monta si: G1–G6, G8–G10 `Intakes` y `Entitlements` (`:649`) · G7 además `QuoteSuggestions` ·
G11–G12 `TenantVariables` · G13–G16 `Integrations` y `Entitlements` · G17 `CRMSecrets`, `CRMGate` y
`CRMReflect` (`CRMNotify` opcional) · G18 `EventTelemetry`.

### 2.8 · Captación — 1

| # | Método + patrón | Línea | Handler | Cadena · permiso (recurso) | Dominio (módulo) | Singleton | Mín. | **Muda en** |
|---|---|---|---|---|---|---|---|---|
| H1 | `POST /api/v1/intakes/{id}/reanalyze` | `:742` | `reanalyze.go` · `reanalyzeIntakeHandler` | W `intakes.write` (`intake`) — 🔴 **sin gate en la cadena**: los dos gates viven dentro del servicio (el 400 de forma va primero, `:709-731`) | `reanalisis` (captación), `intakes`, `cart.NoteTooLongError` (→ `solicitudes/intakes/note.go`) | — | F7 | **F7** |

Monta si: `Reanalysis` (`:741`) — **no** depende de `Intakes`.

### 2.9 · Conversación — 19

| # | Método + patrón | Línea | Handler | Cadena · permiso (recurso) [gate] | Dominio (módulo) | Singleton | Mín. | **Muda en** |
|---|---|---|---|---|---|---|---|---|
| I1 | `POST /api/v1/flows` | `:448` | `flujos/admin/handlers.go:82` · `DefinitionHandler` | W `flows.create` (`flow`) | `flujos/admin`, `flujos/store`, `modules.Registry` | — | F8 | **F8** |
| I2 | `GET /api/v1/flows` | `:452` | `flows.go` · `listFlowsHandler` | R `flows.read` | `store.FlowSummary` | — | F8 | **F8** |
| I3 | `GET /api/v1/flows/{id}` | `:454` | `flows.go` · `getFlowHandler` | R `flows.read` | `model.Flow` | — | F8 | **F8** |
| I4 | `POST /api/v1/flows/{id}/start` | `:460` | `flows.go` · `startFlowHandler` | W `flows.start` (`flow`) | `flowadmin.Starter`, `runtime` (2 centinelas), `contact.Ref` (nucleo), **`gateway/session.ErrSessionOffline`** (`flows.go:235`) | **rt directo** (`Starter`) | F8 | **F8** — ⚠️ trampa de centinela F3→F8 (§4.4) |
| I5 | `POST /api/v1/media/upload-url` | `:467` | `media.go` · `uploadURLHandler` | W `media.upload` (`media`) | solo `platform/storage/objectstore` (presign); prefijo `wapp/media` compilado (`media.go:28`) | — | F0 | **F8** (con su grupo `MediaDeps`) |
| I6 | `PUT /api/v1/tenant-content/{ref}` | `:474` | `tenantcontent.go` · `upsertTenantContentHandler` | W `content.write` (`tenant_content`) | `flujos/store.TenantContentSummary`, `catalogimport` (techo por defecto) | — | F8 | **F8** |
| I7 | `POST /api/v1/tenant-content/{ref}` | `:476` | ídem | W `content.write` (`tenant_content`) | ídem | — | F8 | **F8** |
| I8 | `DELETE /api/v1/tenant-content/{ref}` | `:478` | `deleteTenantContentHandler` | W `content.write` (`tenant_content`) | ídem | — | F8 | **F8** |
| I9 | `GET /api/v1/tenant-content` | `:480` | `listTenantContentHandler` | R `content.read` | ídem | — | F8 | **F8** |
| I10 | `GET /api/v1/tenant-content/{ref}` | `:482` | `getTenantContentHandler` | R `content.read` | ídem | — | F8 | **F8** |
| I11 | `POST /api/v1/triggers` | `:490` | `flujos/admin/triggers.go:436` · `CreateTriggerHandler` | W `triggers.create` (`trigger`) | `flujos/admin`, `trigger`, `DurableFlowChecker` (motor) | — | F8 | **F8** |
| I12 | `GET /api/v1/triggers` | `:492` | `triggers.go:490` · `ListTriggersHandler` | R `triggers.read` | ídem | — | F8 | **F8** |
| I13 | `DELETE /api/v1/triggers/{id}` | `:494` | `triggers.go:533` · `DeleteTriggerHandler` | W `triggers.delete` (`trigger`) | ídem | — | F8 | **F8** |
| I14 | `POST /api/v1/catalog/import` | `:1102` | `catalogimport.go` · `catalogImportHandler` | W `content.write` (`catalog_import`) [`catalog_import`] | `catalogimport`, `cart.Catalog`/`ParseCatalog` (→ `catalogo`, F5), `flujos/model`, **`flujos/store`** | — | F8 | **F8** |
| I15 | `POST /api/v1/catalog/import/tabular` | `:1111` | `catalogtabular.go` · `catalogImportTabularHandler` | W `content.write` (`catalog_import`) [`catalog_import`] | ídem | — | F8 | **F8** |
| I16 | `GET /api/v1/catalog/import/template` | `:1120` | `catalogtemplate.go` · `catalogTemplateHandler` | R `content.read` [`catalog_import`] | solo `catalogimport` | — | F5 | **F8** (monta con I14: `:1115-1119`) |
| I17 | `GET /api/v1/catalog/import/prompt` | `:1122` | `catalogtemplate.go` · `catalogPromptHandler` | R `content.read` [`catalog_import`] | solo `catalogimport` | — | F5 | **F8** (ídem) |
| I18 | `GET /api/v1/conversation-events` | `:845` | `conversationevents.go` · `listConversationEventsHandler` | R `intakes.read` [**cualquiera** de `events.KindFeatures()`] | `flujos/events` (conversación), `entitlements` | — | F8 | **F8** |
| I19 | `POST /api/v1/conversation-events/{id}/cancel` | `:849` | `conversationeventcancel.go` · `cancelConversationEventHandler` | W `intakes.write` (`conversation_event`) [ídem] | `flujos/events` | **rt directo** (`EventCanceller`) | F8 | **F8** |

Monta si: I1–I10 siempre · I11–I13 `Triggers` · I14–I17 `Content`, `ContentVersions` y
`Entitlements` · I18 `Entitlements` y `ConversationEvents` · I19 `Entitlements` y `EventCanceller`.

## 3 · Las 22 rutas del listener admin `:8100` — no se estrangulan

El mux admin lo cablea **entero** el arranque (`registerAdminRoutes`, una sola función, la que
llama `mux_registration_test.go`). No hay cara vieja que delegar: el arranque nuevo
(`internal/arranque/transporte_rutas_admin.go`, `04` §3) **cambia el constructor del handler** en
la fase indicada. Cadena de todas salvo `/healthz` y `/metrics`: `adminHandler` =
Authenticate → RequirePermission → AuditMiddleware (`arranque/http.go:216`), sin access-log.

| # | Patrón | Línea (`rutas_admin.go`) | Handler de hoy | Permiso | Singleton | **Cambia en** |
|---|---|---|---|---|---|---|
| J1 | `/healthz` | `:64` | `httpapi.HealthHandler` + `postgres.NewHealthCheck` | — | — | no cambia (platform) |
| J2 | `/metrics` | `:65` | `mtx.PromHandler()` | — | — | no cambia (platform) |
| J3 | `/admin/crypto/rekey` | `:98` | `httpapi.CryptoRekeyHandler(crypto.Rekey…)` | `crypto.rekey` | — | no cambia (platform) |
| J4–J11 | `GET /admin/tenants` · `POST /admin/tenants` · `GET /admin/tenants/{id}` · `GET /admin/tenants/{id}/installations` · `POST /admin/tenants/{id}/enrollment-codes` · `GET /admin/access-requests` · `POST /admin/access-requests/{id}/approve` · `POST /admin/access-requests/{id}/reject` | `:76-91` | `platformadmin.*Handler`, construidos **inline** (lo exige el candado I-CP-5, `platform_permissions_test.go`) | `*.any` | — (`codeStore` de `enroll` entra por `platformadmin.CodeIssuer`, estructural) | **F2** |
| J12 | `/admin/leases/revoke` | `:66` | `httpapi.RevokeLeaseHandler(gw)` | `leases.revoke` | gw directo | **F3** (recibe el gw nuevo) |
| J13 | `POST /admin/tenants/revoke` | `:92` | `httpapi.RevokeTenantHandler(gw, …)` | `tenants.revoke.any` | gw directo | **F3** |
| J14 | `POST /admin/tenants/restore` | `:94` | `httpapi.RestoreTenantHandler(gw, …)` | `tenants.restore.any` | gw directo | **F3** |
| J15 | `/admin/messages/send` | `:96` | `httpapi.SendMessageHandler(gw, …)` | `messages.send` | gw directo | **F3** (su centinela lo arregla el ✎ de F0 en `platform/httpapi/admin.go:306`) |
| J16 | `POST /admin/sessions/{id}/profile` | `:110` | `flowadmin.SetSessionProfileHandler(fleetRepo, filtersPusher, log)` | `sessions.write` | gw transitivo | **F3** — usa el handler portado de D3 |
| J17 | `POST /admin/sessions/{id}/status` | `:112` | `flowadmin.SetSessionStatusHandler(fleetRepo)` | `sessions.write` | — | **F3** — usa el de D4 |
| J18 | `/admin/flows` | `:100` | `flowadmin.DefinitionHandler(flowStore, flowReg)` | `flows.create` | — | **F8** |
| J19 | `/admin/flows/start` | `:102` | `flowadmin.StartHandler(flowRuntime)` | `flows.start` | rt directo | **F8** — ⚠️ misma trampa de centinela que I4 (`flujos/admin/handlers.go:326`) |
| J20–J22 | `POST /admin/triggers` · `GET /admin/triggers` · `DELETE /admin/triggers/{id}` | `:104-108` | `flowadmin.*TriggerHandler` | `triggers.*` | — | **F8** |

🔴 **D3/J16 son el mismo handler por dos vías** (`fase8_transporte.go:81-85` y `:133-138`: «encender
solo una deja la otra muda sin dar ningún rojo»). En F3 **las dos** pasan al handler portado en el
mismo commit.

## 4 · Reglas que ajustan la columna «Muda en»

### 4.1 · Singletons: cuándo obligan

Regla de D-10: **si la cara vieja necesitaría una instancia VIEJA de un singleton ya conmutado, la
ruta se muda con el singleton.** Aplicada:

| Singleton | Rutas con uso **directo** | Muda forzada | Conflicto |
|---|---|---|---|
| gw (F3) | D1 `Sender` · D5 `DiagnosticsRequester` · E2 `ConfigPush` · (admin J12–J15) | F3 | **E1–E2**: su código es de captación (F7) → F3 con puente (D-FX-1) |
| gw (F3) vía `filtercfg.Pusher` | D3 (`ProfilePush`) · J16 | F3 | Ninguno: todo es edge; solo cambia dónde vive el handler ([`diseno.md`](diseno.md) §1, `sessionadmin.go`) |
| rt (F8) | I4 `Starter` · I19 `EventCanceller` · J19 | F8 | Ninguno |

Rutas con uso **transitivo** del gw que **no** pueden mudarse en F3 porque su dominio es posterior:
G1–G7 y G17 (vía `intakes.Notifier`, hasta F6), G7 (vía `llmvia.Selector`, hasta F4/F6), I4 (vía
`rt`, hasta F8). Siguen en la cara vieja y el arranque nuevo **inyecta el gw nuevo** en los
servicios viejos que las sirven. Es trabajo de F3, y no siempre es gratis: ver
[`arquitectura.md`](arquitectura.md) §4 (el `local.Frame` de `llmvia` pide un tipo viejo del gateway).

### 4.2 · Familias y solapes: lo que se muda junto

1. **Todos los métodos de un mismo camino se mudan juntos** (familia). Si no, un 405 cambia su
   cabecera `Allow` o pasa a 404 ([`diseno.md`](diseno.md) §2). Familias partidas por la tabla:
   ninguna (verificado fila a fila).
2. 🔴 **Un comodín en la cara nueva tapa un literal que se quede en la vieja.** La precedencia «el
   patrón más específico gana» de Go 1.22 vale **dentro** de un mux, no entre los dos. Solapes
   reales: `GET /api/v1/intakes/{id}` con `…/export` y `…/summary.json` (G2 · G9 · G10, todas F6) y
   `DELETE /api/v1/invitations/{id}` con `POST /api/v1/invitations/accept` (B14 · A4, las dos F2).
   Los dos grupos caen en la misma fase: la tabla no crea ningún solape roto. Una fila que se
   adelante o se atrase tiene que mirar esto.
3. Las rutas que **montan juntas** se mudan juntas: I16–I17 con I14; D6 con D5; E1 con E2.

### 4.3 · El plazo de G7

`conPlazoDeRedacción` suma `pipeline.PlazoPorLlamadaSuelo` (48 s, captación) + 12 s
(`plazoescritura.go:67,87`). Para mudar G7 en F6 sin importar el `pipeline` viejo, la cara nueva
**recibe el plazo como parámetro** y el arranque lo calcula con el mismo `PlazoPorLlamadaSuelo` que
ya le pasa a `quotetext.ConPlazo` (`fase5_captacion.go:342-344`): sigue siendo **derivado, no
copiado**. En F7 el arranque pasa a leerlo del `pipeline` nuevo.

### 4.4 · Centinelas: la identidad de un error no sobrevive al cambio de paquete

`errors.Is` compara **identidad**. Del cierre de F3 al de F8, I4 (y J19) siguen en código viejo que
compara `gateway/session.ErrSessionOffline` **viejo** (`publicapi/flows.go:235`,
`flujos/admin/handlers.go:326`), pero el runtime viejo enviará por el **gw nuevo**, que devolverá el
centinela **nuevo**. Sin remedio, «sesión offline» deja de mapearse y cambia el código HTTP.
Remedio propuesto (D-FX-3): en F3, `modulos/edge/session` declara
`var ErrSessionOffline = oldsession.ErrSessionOffline` como **puente de identidad** declarado, que
se retira al cerrar F8.

## 5 · Resumen: fase → rutas que se mudan (o cambian de handler)

| Fase | `:8103` a `apipublica` | `:8100` cambian de handler | Filas |
|---|---:|---:|---|
| F0 | 0 (nace la cara vacía y el estrangulador) | 0 | — |
| F1 | 0 | 0 | — |
| **F2** `acceso` | **23** | 8 | A1–A7, B1–B14, C1–C2 · J4–J11 |
| **F3** `edge` | **8** (6 + 2 de intenciones con puente) | 6 | D1–D6, E1–E2 · J12–J17 |
| **F4** `inferencia` | **4** | 0 | F1–F4 |
| **F5** `catalogo` | **0** (sus 4 rutas escriben por `flujos/store`: van en F8) | 0 | — |
| **F6** `solicitudes` | **18** | 0 | G1–G18 |
| **F7** `captacion` | **1** | 0 | H1 |
| **F8** `conversacion` | **19** | 5 | I1–I19 · J18–J22 |
| **Total** | **73** | 19 (+3 que no cambian) | |

Con D-FX-1 en su alternativa, F3 muda 6 y F7 muda 3. Al cerrar F8 la cara vieja no sirve
**ninguna** ruta en el binario nuevo; en F10 se borran `internal/publicapi` y el estrangulador.
