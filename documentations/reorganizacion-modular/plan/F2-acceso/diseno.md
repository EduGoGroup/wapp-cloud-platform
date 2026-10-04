# F2 · Diseño — contratos por paquete, suites, dobles y reglas que se llevan (E-8)

> `A` = `internal/modulos/acceso` · `V` = el paquete viejo de referencia. Todo fichero nuevo lleva
> `// Porta <V>/<fichero>.go @ <sha>` (o `// Nuevo: …`). **En rojo, solo exportados** (T-1 de F1): los
> auxiliares no exportados nacen en el verde, y su test también, **solo si llevan regla de negocio o ramas no
> triviales**; no se testea fontanería ni `if err != nil`; el resto lo cubre F9 (`05` E-4, P6).
> Las reglas `R-xx` de §4 son lo que el comentario del contrato **tiene que decir** y el test
> **tiene que afirmar**; salen de los tests viejos y de los comentarios-ADR del código, leídos el
> 2026-09-28. Una regla que se decida no mantener se dice en el commit, con motivo (E-8).

## 1 · Árbol nuevo (producción ↔ test)

```
A/entitlements/          entitlements.go · middleware.go · postgres.go            (+3 _test.go)
A/entitlements/entitlementshelpertest/   suite.go (ContratoResolver) · fake.go (Fake ↦ de entitlements.go, D-F2-4)
A/iam/domain/            canje.go · entities.go · errors.go · invitation.go       (+4)
A/iam/ports/in/          active_tenant.go · canje.go · usecases.go                (+1: usecases_test.go; D-F2-5)
A/iam/ports/out/         active_tenant.go · canje.go · repos.go                   (solo interfaces: sin _test, E-3)
A/iam/ports/out/outhelpertest/ ✚ una suite por puerto persistente (7) + montaje común
A/iam/usecase/           active_tenant · audit · canje · config · context_token · delegated_auth · exchange · grants · invitations · memberships · roles   (+11)
A/iam/infra/memory/      active_tenant_store · audit_store · grant_store · invitation_store · membership_store · role_store · store · redeem_store ✚   (+8)
A/iam/infra/postgres/    active_tenant · audit · canje · grants · invitations · memberships · postgres · roles   (+8 unitarios, +3 candados AST, +1 `integracion`)
A/iam/infra/identity/    client.go · m2m.go                                       (+2, contra httptest)
A/iam/transport/http/    active_tenant · auth · canje · http · invitations · roles (+6)
A/platformadmin/         access_requests · access_requests_postgres ✚ · handlers · postgres · puertos ✚ · signup   (+5; puertos.go sin test, E-3)
A/platformadmin/platformadminhelpertest/ ✚ suite de los puertos + doble en memoria
internal/arranque/       bridge_iam.go ✚ · bridge_iam_test.go   (adaptador de arranque, `05` §4.2)
```

### 1.1 · Niveles de ceremonia (`05` E-12) — **definitivo**: inventario de T2.1, aprobado por Jhoan el 2026-10-04

Medido fichero a fichero sobre el código viejo en `dev` @ `9a77307` (sesión F2-01). **Consumidores** = paquetes viejos de
producción que importan el paquete (sin `_test.go`, sin el propio paquete y sin la copia `internal/arranque`). Comandos:
`wc -l`; exportados por recorrido de las declaraciones de primer nivel; estado = campos o mapas mutados tras construir;
concurrencia = `grep -nE 'sync\.|atomic\.|chan |<-|^\s*go |pg_advisory'`; BD = `database/sql`/`pgconn`/SQL/`BeginTx` en
código (no en comentarios); consumidores = `grep -rl '"github.com/EduGoGroup/wapp-cloud-platform/internal/<paq>"' --include='*.go' internal cmd | grep -v _test.go`.

| Fichero (viejo → `A/…`) | Líneas | Export. | Estado en memoria | Concurrencia | BD / transacciones | Consum. | Nivel | Sesión |
|---|---:|---:|---|---|---|---:|---|---|
| `entitlements/entitlements.go` (sin `Fake`) | 296 | 20 | no (el `Fake` sí: mapas `:213,:216`) | no | no | 7 | **simple** | F2-01 |
| ✚ `entitlements/entitlementshelpertest/` (suite + `Fake`, de `:203-296`, D-F2-4) | ~94 | — | doble | no | no | tests | **simple** (D-F1-3) | F2-01 |
| `entitlements/middleware.go` | 144 | 2 | no | no | no | 1 (`publicapi`) | **medio** (fail-closed, I-CP-6) | F2-01 |
| `entitlements/postgres.go` | 243 | 7 | caché TTL (`:34`, `:39`) | `sync.Mutex` `:32` | 4 consultas, sin Tx | 1 | **complejo** | F2-03 |
| `iam/domain/canje.go` | 110 | 7 | no | no | no | 2 | **simple** (P4) | F2-01 |
| `iam/domain/entities.go` | 208 | 14 | no | no | no | 9 | **simple** | F2-01 |
| `iam/domain/errors.go` (19 centinelas) | 171 | 19 | no | no | no | 7 | **simple** | F2-01 |
| `iam/domain/invitation.go` | 173 | 10 | no | no | no (`crypto/rand`, `sha256`) | 6 | **simple** (P4) | F2-01 |
| `iam/ports/in/{active_tenant,canje,usecases}.go` | 86 · 35 · 326 | 2 · 1 · 25 | no | no | no | 5 | **simple** | F2-01 |
| `iam/ports/out/{active_tenant,canje,repos}.go` | 43 · 49 · 321 | 1 · 1 · 8 | no | no | no | 6 | **simple** | F2-01 |
| ✚ `iam/ports/out/outhelpertest/` (7 suites + montaje) | — | — | — | — | — | tests | **simple** (D-F1-3) | F2-01 |
| `iam/infra/memory/{active_tenant,audit,grant,invitation,membership,role}_store.go` · `store.go` | 47 · 63 · 48 · 138 · 249 · 252 · 52 | 4 · 5 · 5 · 7 · 11 · 14 · 2 | **sí** (mapas) | `Mutex`/`RWMutex` | no | **0** en producción | **simple** (dobles, D-F1-3; P1) | F2-01 |
| ✚ `iam/infra/memory/redeem_store.go` | nuevo | — | sí | `Mutex` | no | 0 | **simple** (P1) | F2-01 |
| `iam/usecase/{config,audit,context_token}.go` | 36 · 64 · 82 | 2 · 4 · 4 | no | no | no | 1 | **simple** | F2-02 |
| `iam/usecase/{active_tenant,canje,delegated_auth,exchange,grants,invitations,memberships,roles}.go` | 198 · 99 · 204 · 378 · 79 · 230 · 254 · 264 | 4 · 3 · 6 · 7 · 0 · 5 · 5 · 10 | no | no | Tx solo **indirecta**, en el adaptador (`canje`, `memberships`) | 1 | **medio** | F2-02 |
| `iam/infra/identity/client.go` | 273 | 6 | no | no | no (HTTP) | 1 | **medio** | F2-02 |
| `iam/infra/identity/m2m.go` | 735 | 8 | caché del token (`:95-101`) | `RWMutex` + candado de canal (`:93`) | no (HTTP) | 1 | **complejo** | F2-02 |
| `iam/infra/postgres/postgres.go` | 56 | 0 | no | no | solo mapeo de `pgconn` | 2 | **simple** | F2-03 |
| `iam/infra/postgres/{active_tenant,audit,grants,invitations,roles}.go` | 77 · 85 · 53 · 218 · 319 | 4 · 4 · 5 · 5 · 12 | no | no | SQL, una sentencia por método, sin Tx | 2 | **medio** (P2) | F2-03 |
| `iam/infra/postgres/{memberships,canje}.go` | 426 · 295 | 10 · 3 | no | `pg_advisory_xact_lock` (`memberships.go:310`) | **Tx** | 2 | **complejo** (mutantes) | F2-03 |
| `iam/transport/http/*` (6) | 1.407 | 32 | no | no | no | 2 | **medio** | F2-03 |
| ✚ `platformadmin/puertos.go` | nuevo | — | no | no | no | — | **simple** | F2-03 |
| `platformadmin/{handlers,signup}.go` · `access_requests.go` (reglas + handlers) | 293 · 207 · ~400 | 11 · 4 · — | no | no | no (tras D-F2-3) | 1 | **medio** | F2-03 |
| `platformadmin/postgres.go` | 322 | 14 | no | no | SQL sin Tx | 1 | **medio** (P2) | F2-03 |
| ✚ `platformadmin/access_requests_postgres.go` (SQL de `access_requests.go:135-336,481-526`) | ~300 | — | no | lock indirecto (`GrantTenantAccess`) | **Tx** (`executeApprovalTx`) | 1 | **complejo** (mutantes) | F2-03 |
| ✚ `platformadmin/platformadminhelpertest/` | — | — | doble | — | — | tests | **simple** | F2-03 |
| ✚ `internal/arranque/bridge_iam.go` | nuevo | 0 | no | no | no | gateway viejo | **simple** (`05` §4.2) | F2-04 |
| ✚ `internal/apipublica/{chain,response,auth,roleplane,audit,entitlements}.go` + `apipublicahelpertest/harness.go` (FX TX.5–TX.6; añadida en F2-04, aprobada por Jhoan el 2026-10-04) | nuevo | 9 (+ `Harness` & co.) | no | no | no | arranque nuevo | **medio** (rojo + verde por fichero; el arnés nace verde) | F2-04 |

Goroutines en producción de `acceso`: **0**. Métricas propias: **0**.

**Adaptadores de arranque**: nace **1**, `internal/arranque/bridge_iam.go` (F2-04; muere en F3 con el gateway nuevo); se
retira **0**. `entitlements` no lo necesita (puerto estructural, constantes `string` sin tipo). Puentes de import: **0**.

**Respuestas de Jhoan al inventario (2026-10-04)** — donde el criterio literal de E-12 y la medida no casaban:
- **P1** · `iam/infra/memory` tiene estado y candados (E-12 diría «complejo»), pero son **dobles** con 0 consumidores en
  producción y su test es la suite de `outhelpertest`: **simple**, nacen completos en una pasada.
- **P2** · Los adaptadores Postgres **sin transacción** (una sentencia por método): **medio**; su verdad la da la suite
  compartida con memoria contra Postgres (P4, D-R-1) y F9. Mutantes solo en los de Tx/cerrojo (`memberships.go`,
  `canje.go`, `access_requests_postgres.go`) y en los de estado y concurrencia (`entitlements/postgres.go`, `m2m.go`).
- **P3** · T2.34: el candado de invitaciones parsea `auth_roleplane.go` (ver `tareas.md` T2.34).
- **P4** · Los cuatro de `iam/domain` son **simples** (puros, 1–3 funciones), con R-D1…R-D5 afirmadas y corpus adversario.

Cambios frente a la tabla provisional: bajan a **simple** `iam/usecase/{config,audit,context_token}.go`,
`iam/infra/postgres/postgres.go` e `iam/infra/memory`; bajan a **medio** los 5 adaptadores SQL sin Tx de
`iam/infra/postgres` y `platformadmin/postgres.go`. Ninguno sube.

Qué se hace en cada nivel: [`reglas.md`](reglas.md) §5.

## 2 · Suites de contrato y dobles (E-3, E-6)

Firma común (D-F1-1 de F1): `func ContratoX(t *testing.T, nuevo func(t *testing.T) MontajeX)`,
donde el montaje trae la implementación y los **dos tenants** con UUID sembrados (Postgres los exige
por FK). Cada suite: casos con nombre **en inglés** que digan la regla (`05` E-11, que rige lo nuevo desde el 2026-10-02; aquí decía
«en español»); nada de BD ni reloj real en la versión en
memoria. **Todo puerto con BD** corre su suite `Contrato(t, func(t) Montaje)` **en memoria y en Postgres** con el arnés
de F9-A (`//go:build integracion`, testcontainers; P4): es lo que garantiza que memoria y Postgres se comportan igual.
Vale para las 7 de `outhelpertest`, `ContratoResolver` y la de `platformadminhelpertest`. La marca de estado de cada
`Montaje` vigila **todas** las columnas que la operación puede tocar, no una sola (hallazgo 35 de F1).

| Suite (`outhelpertest`) | Casos mínimos (de las reglas de §4) | Doble | Postgres |
|---|---|---|---|
| `ContratoMembershipRepo` | `Add` idempotente; `Remove` acotado al tenant; `TenantsOfUser` y `UserTenants` **mismo orden** y lista **vacía no nil**; `MembersOf` solo del tenant; segunda empresa sin `multi_empresa` → `ErrConflict` idéntico; con `multi_empresa` escribe; resolver caído **mantiene** el rechazo | `memory.MembershipStore` | `iampostgres.MembershipRepo` |
| `ContratoRoleRepo` | `List` = propios + plantillas globales, nunca ajenos; `ParentOf`; `AssignToUser` de rol de empresa con tenant nil **o `""`** → `ErrRoleScopeInvalid`; rol transversal sí se asigna global | `RoleStore` | `RoleRepo` |
| `ContratoGrantRepo` | alta/baja de override por usuario; `GrantsOfUser` | `GrantStore` | `GrantRepo` |
| `ContratoAuditRepo` | `Record` append-only; `List` acotado al tenant con límite/desplazamiento | `AuditStore` | `AuditRepo` |
| `ContratoInvitationRepo` | nace pendiente; digest de 32 bytes (otro tamaño → error); digest **único**; `Revoke` marca y **no borra**; tres desenlaces de la revocación; orden `(created_at DESC, id DESC)`; no cruza empresas; rol que se borra deja la invitación viva sin rol | `InvitationStore` | `InvitationRepo` |
| `ContratoActiveTenantRepo` | ausencia → `ok=false`; alta; **reemplazo** (un valor por usuario) | `ActiveTenantStore` | `ActiveTenantRepo` |
| `ContratoInvitationRedeemRepo` ✚ | camino feliz: membresía en el tenant **de la invitación**; inexistente/caducada/canjeada/revocada **sin rastro escrito**; ya-miembro-de-otra → conflicto y la invitación **no se quema**; sin solicitud pendiente no es fallo; el `tenant_id` del cuerpo **se ignora** | `memory.RedeemStore` ✚ | `InvitationRedeemRepo` |
| `entitlementshelpertest.ContratoResolver` | override gana en los dos sentidos; plan nil ⇒ `basic`; `ListEffective` ordenada, sin las apagadas; tenant inexistente ⇒ `("", nil, nil)`; error de infraestructura se propaga | `entitlementshelpertest.Fake` | `entitlements.Postgres` |
| `platformadminhelpertest.Contrato…` ✚ (D-F2-3) | listado paginado estable con desempate; `GetTenant` inexistente → `ErrNotFound`; `CreateTenant` slug duplicado → `ErrConflict`; ciclo de una solicitud de acceso (pendiente → aprobada/rechazada); reaprobar con otro rol → `ErrRetryRoleMismatch` | doble ✚ | `Repository` |

Los tres clientes de identity (`IdentityClient`, `IdentityM2MClient`, `UserSystemsClient`) no llevan
suite en `outhelpertest`: su contrato es **traducir el protocolo de identity**, y se prueba en
`infra/identity` contra un `httptest.Server`; los usecases usan dobles propios en su `_test`.

## 3 · Contratos por fichero (lo que promete cada uno)

Exportados = los del viejo (§1 de arquitectura) salvo lo marcado. Se listan los que fijan conducta.

| Fichero nuevo | Exportados clave | Promesa (resumen del comentario de contrato) |
|---|---|---|
| `entitlements/entitlements.go` | 11 `Feature*`, `Resolver` | Constantes **byte a byte** (`"llm_intent"`, `"cart_basic"`, `"intakes_export"`, `"catalog_import"`, `"crm_bridge"`, `"menu"`, `"survey"`, `"media"`, `"llm_intake"`, `"api_llm"`, `"multi_empresa"`). `Has`: error solo por infraestructura, «no la tiene» es `false, nil`. `ListEffective`: plan + encendidas, alfabético, vacío no es error. `CacheTTL`: el TTL **real** del objeto |
| `entitlements/middleware.go` | `RequireFeature`, `RequireAnyFeature` | R-E1…R-E5 |
| `entitlements/postgres.go` | `Postgres`, `NewPostgres`, `Option`, `WithTTL`, `WithReloj` ✚ | caché por par y por tenant **separadas**; el `false` también se cachea; la lista devuelta es **copia**; el mutex **no** se sostiene durante la consulta; `WithTTL(<=0)` se ignora; tenant inexistente ⇒ sin derechos |
| `iam/domain/errors.go` | 19 centinelas | textos **literales** (`"iam: …"`, §5); `errors.Is` es la única forma de inspeccionarlos |
| `iam/domain/invitation.go` | `NewInvitationToken`, `HashInvitationToken`, `InvitationStatus`… | R-D1…R-D4 |
| `iam/domain/canje.go` | `EvaluarCanje` y sus 4 veredictos, `ErrInvitationExpired` | R-D5 |
| `iam/ports/in/usecases.go` | 25 (DTOs, interfaces, `CallerResolverFunc`) | `AuditInput` = **alias** del DTO de `platform/httpapi` (F0); `CallerResolverFunc.Caller` delega en la función |
| `iam/usecase/exchange.go` | `SystemWappBFF` & co., `ExchangeService`, `NewExchangeService`, `IdentityTokenVerifier`, `DefaultAccessTTL` | R-U1…R-U9 |
| `iam/usecase/delegated_auth.go` | `DelegatedAuthService`, constructor | R-U10…R-U14 |
| `iam/usecase/active_tenant.go` | `ActiveTenantService` | R-U15…R-U19 |
| `iam/usecase/{roles,memberships,invitations,canje,audit,context_token,config,grants}.go` | servicios y constructores | R-U20…R-U33; todos los constructores **fallan con nil** (fail-fast) con su texto `"iam: <Servicio> requiere …"` |
| `iam/infra/postgres/memberships.go` | `MembershipRepo`, `GrantTenantAccess`, `Executor`, `FeatureResolver` | R-P1…R-P4; **único** escritor de `tenant_members` (candado §6) |
| `iam/infra/postgres/canje.go` | `InvitationRedeemRepo`, `NewInvitationRedeemRepo` | R-P5…R-P8; lectura en **una** consulta (`leerInvitacion`) con `now()` de la base |
| `iam/infra/identity/{client,m2m}.go` | `Client`, `M2MClient`, constructores | R-I1…R-I9 |
| `iam/transport/http/*.go` | `Register`, handlers (`AuthHandler`, `InvitationRedeemHandler`, `ActiveTenantHandler`, `RoleAdminHandler`, `MembershipHandler`, `InvitationHandler`) | R-H1…R-H9; mapeo de errores de `http.go` **idéntico** (§5) |
| `platformadmin/puertos.go` ✚ | `TenantStore`, `AccessRequestStore` (solo interfaces) | lo que hoy son métodos de `*Repository` (`postgres.go:99-277`, `access_requests.go:136-485`) |
| `platformadmin/*.go` | handlers, DTOs, 9 centinelas, `CodeIssuer` | R-A1…R-A10; handlers reciben los **puertos**, no `*Repository` |

## 4 · Reglas que el contrato debe llevar (E-8), con su origen

**entitlements** (`V/entitlements/*_test.go`, 24 `Test*`)
- R-E1 Fail-closed: sin identidad o sin tenant, resolver caído, resolver nil → **403** y el mismo cuerpo; nunca 500 ni pase (`middleware.go:39-58`).
- R-E2 Override `enabled=false` cuenta como no tenerla, también para «alguna» (`middleware_any_test.go:80`).
- R-E3 `RequireAnyFeature`: con una basta; un error **corta en el acto** (no `continue`); lista vacía **no abre** (sin guarda `len==0`: lo sostiene el bucle) (`middleware.go:61-107`, `middleware_any_test.go:127,155`).
- R-E4 Cuerpos: `{"error":"feature_not_enabled","feature":"<k>"}` y el plural con `features`; `omitempty` en los dos para que el singular no cambie (`middleware.go:22-26`).
- R-E5 `api_llm` gatea la **vía**, no la capacidad; `llm_intake` se basta sola (ADR-0044, D-044.28, `entitlements.go:124-154`); `multi_empresa` gobierna el desenlace de un alta, **nunca** una ruta (`:163-175`).

**iam/domain** (`V/iam/domain/*_test.go`, 6)
- R-D1 Token de invitación: prefijo acordado + 16 bytes aleatorios en hex, largo **exacto** (`invitation_test.go:30`); dos emisiones no se repiten.
- R-D2 `HashInvitationToken` determinista, **32 bytes** (CHECK `tenant_invitations_token_hash_len_check`).
- R-D3 La normalización de lo que vuelve de WhatsApp vive **dentro** de `HashInvitationToken` (simetría escritor/lector, `:97`).
- R-D4 `InvitationStatus`: cuatro salidas con **precedencia** (lo que pasó gana a lo que dice el reloj después, `:137`).
- R-D5 `EvaluarCanje`: los cuatro veredictos (`canje_test.go:18`).

**iam/usecase** (93 `Test*`)
- R-U1 El Context Token **nunca** vence después que el Identity Token (REQ-A2, `exchange_test.go:207`); con identidad larga manda el TTL de contexto; identidad casi vencida no se canjea (`ErrIdentityTokenExpiring`).
- R-U2 Solo se canjean tokens de las aplicaciones de wApp (`SystemWappBFF`…); de otro ecosistema → rechazo (`:269,283`).
- R-U3 Sin membresía → token **sin empresa y sin grants** (D-056.12), aunque tenga roles asignados (`:329,376`).
- R-U4 Con una membresía el token sale idéntico campo a campo y la empresa activa **ni se mira** (`:449,713`).
- R-U5 Varias empresas sin elegir → sin empresa (no elige por ti) (`:578`); con elegida viva → acotado a ESA y sus grants (`:610`); elegida que ya no es suya → sin empresa (`:662`).
- R-U6 Un fallo leyendo la empresa activa **corta**; no degrada a «sin empresa» (`:755`).
- R-U7 El canje no abre sesión en wApp (sin refresh propio) (`:808`); identity caído ≠ credencial rechazada (`:843`).
- R-U8 Roles acotados por tenant (`:877`); herencia `parent_role_id` agrega; `deny` precede a `allow` (`rbac_test.go:60,84`).
- R-U9 Constructor exige verificador, repos y emisor (textos §5).
- R-U10 Login delegado: valida en identity y canjea en wApp; el rechazo de identity se propaga **sin** canjear; sujeto sin migrar no entra (`delegated_auth_test.go:116,220,247`).
- R-U11 Refresh rota en identity y re-canjea; logout cierra **solo** la sesión de esta aplicación; logout-all revoca sin transportar user_id y con refresh muerto no revoca nada.
- R-U12 Fallo parcial del logout-all: se cierra la sesión rotada (mitigación) y el error devuelto es el del logout-all; la mitigación **no depende del logger** (`:311,337,355`).
- R-U13 Verify valida Context Tokens **sin** salir a identity (`:258`).
- R-U14 El constructor exige una aplicación de wApp (`wapp.bff` o `wapp.edge`).
- R-U15 Elegir empresa: se guarda (se comprueba lo guardado, no el `nil`); reemplaza, no acumula; ajena e inexistente son **el mismo** `ErrNotFound` (anti-oráculo); entrada inválida es 400, no 401 (`active_tenant_test.go:56-144`).
- R-U16 Una sola membresía también se puede fijar (`:162`).
- R-U17 `TenantsOfCaller`: con nombre y la activa; sin empresas → lista **vacía, no error**; solo las suyas; con una sola marca esa aunque la guardada sea otra; la guardada que ya no es suya no se marca (`:195-318`).
- R-U18 **El selector y el canje nunca discrepan** en las cinco formas de estar (`:373`).
- R-U19 Constructores fallan con cualquiera de sus tres dependencias nil.
- R-U20 Roles: listado = propios + plantillas globales; crear nace en el tenant del contexto; padre de otra empresa → rechazo; asignar acota al tenant del contexto (INV-04), también con plantilla global (`roles_test.go:111-205`).
- R-U21 Rol ajeno → `ErrNotFound` opaco; usuario que no es miembro → rechazo; retirar en A no toca la fila de B.
- R-U22 Grants: la plantilla global es inmutable (`ErrGlobalRoleImmutable`); `iam_user_grants` no tiene tenant, así que el override **solo** sobre miembros del tenant; efecto vacío no vale como allow.
- R-U23 Sin tenant en el contexto (sin identidad **o** identidad sin empresa) no se ejecuta nada.
- R-U24 Alta de miembro: tenant del contexto; idempotente; segunda empresa sin `multi_empresa` → conflicto; con ella escribe; resolver caído **mantiene** el rechazo (fail-closed invertido) (`memberships_test.go:57-166`).
- R-U25 Acreditación antes de escribir: si identity falla no se escribe la membresía; el conjunto que viaja es la **unión** (PUT declarativo); si ya la tenía no se escribe en identity; sin M2M → `ErrIdentityNotConfigured` y cero escrituras (`membership_acreditacion_test.go:108-231`).
- R-U26 El log distingue credencial M2M rechazada del resto, al llamante se le contesta lo mismo (500); sin logger no revienta (`:276,341`).
- R-U27 Invitaciones: el tenant sale del contexto; se guarda **solo el digest**; sin empresa no emite; TTL con default y clamp en **un** sitio (el servicio); el rol prometido tiene que ser visible; sin rol es legítimo (`invitations_test.go:55-189`).
- R-U28 Listado solo de su empresa, más recientes primero; revocar deja la fila revocada (se lee la fila), idempotente sobre una revocada, **conflicto** sobre una canjeada, no alcanza a otra empresa (y la de B sigue viva), id no-UUID → `ErrNotFound` (`:203-360`).
- R-U29 Canje (`canje.go`): quien canjea sale del contexto (INV-04).
- R-U30…R-U33 `audit.go` límite por defecto; `context_token.go` exige validador; `config.go` y `grants.go` (conversión a `Allow[]/Deny[]` de identity-shared).

**iam/infra/postgres** (41 `Test*`: 37 BD + 4 AST — **no** se portan; lo que dicen va a las suites de §2 y a F9)
- R-P1 `GrantTenantAccess` es el caso de uso compartido de las dos vías de alta: membresía **y** rol en una llamada; la guarda corta **antes** del rol; sin rol no toca `iam_user_roles`; no asigna roles con ámbito global (`integration_test.go:302-376`, `rol_transversal_integration_test.go:134`).
- R-P2 Cerrojo `pg_advisory_xact_lock(user_id)` → guarda `countOtherMemberships` → `INSERT`, en ese orden y en el cuerpo (TOCTOU de T5.2; dos altas simultáneas, solo una escribe) (`multi_empresa_integration_test.go:167`).
- R-P3 Sin `multi_empresa` el 409 es **idéntico** al de siempre (mismo centinela, mismo cuerpo) (`:97`).
- R-P4 `MembersOf` lee el tenant pedido y solo ese (`integration_test.go:410`).
- R-P5 Canje: los cuatro pasos en **una** transacción; acceso **antes** de marcar la invitación (candado `canje_orden`).
- R-P6 `leerInvitacion` hace **una** consulta y la rama `sql.ErrNoRows` no llama a nada (candado `canje_una_consulta`).
- R-P7 El scan traslada las cuatro NULLables (`RoleID`, `RedeemedBy`, `RedeemedAt`, `RevokedAt`) → función pura `filaAInvitacion` con test de conducta (D-F2-1).
- R-P8 El `UPDATE` de canje lleva `redeemed_at IS NULL AND revoked_at IS NULL` (resuelve la carrera con la revocación).

**iam/infra/identity** (25)
- R-I1 Login manda el `system` en el cuerpo y lee `identity_token`; refresh **no** manda `system`; un refresh quemado no es credencial inválida; logout idempotente; logout-all presenta el Identity Token como portador (`client_test.go:91-195`).
- R-I2 Identity inalcanzable ≠ credencial rechazada (`ErrIdentityUnavailable`) (`:216`); URL vacía o inusable → error en el constructor.
- R-I3 M2M: canjea una vez y reutiliza el service token hasta que **expira**; un token rechazado se re-canjea una vez y se reintenta una vez (`m2m_test.go:164,199,433`).
- R-I4 La API key viaja en el **cuerpo** como `api_key`, nunca en `Authorization` (`:359`).
- R-I5 Una ráfaga fría produce **un** canje (candado serializado); un contexto cancelado no espera el canje (`:464,507`).
- R-I6 `ReplaceUserSystems` declara el conjunto (nil viaja como vacío) y lee el diff; traduce códigos.
- R-I7 `GetUserSystems` devuelve todas las claves, sin cuerpo; `null` → slice vacío **no nil**; identificador vacío no sale al cable; su mapeo es **propio** (403 `SYSTEM_ACCESS_DENIED` no significa lo mismo que en el PUT) (`:536-626`).
- R-I8 `Signup` no presenta el service token (`:296`).
- R-I9 Los fallos del canje M2M son de la credencial de **máquina**, nunca de la persona (`:389`).

**iam/transport/http** (27)
- R-H1 Elegir empresa: 204 **y** el dato llega al puerto; el 404 no es oráculo (`active_tenant_test.go:85-138`).
- R-H2 Listar empresas: forma exacta; sin empresas la cadena literal `"tenants":[]` (no `null`); sin elegida no marca; sin totales ni conteos; el listado no tiene 404 (400 cableado, 500 infra) (`:167-296`).
- R-H3 El IAM viejo **no está** en el cable (prueba por ausencia de las rutas de login/refresh propias) (`auth_test.go:102`); verify por cuerpo o `Bearer`; sin token 400.
- R-H4 Canje de invitación: «no existe» y «caducada» dan **bytes idénticos**; el 409 tiene voz propia; el token viaja **tal cual** al usecase (sin normalizar ni recortar) (`canje_test.go:65-157`).
- R-H5 El cuerpo del canje y el del exchange **no tienen dónde** aterrizar un `tenant_id` (`canje_internal_test.go:31,67`, `exchange_internal_test.go:33,70`).
- R-H6 Exchange: modo dual apagado → 503 con su texto; token no aceptable → 401; sin empresa → 200 con contexto sin tenant; varias empresas → 200 sin empresa (el 409 viejo **ya no existe**) (`exchange_test.go:105-272`).
- R-H7…R-H9 roles/invitations: mapeo de §5; `POST /api/v1/members` sin M2M → 503.

**platformadmin** (40: 33 BD)
- R-A1 `EnforcePlatformCaller` (401 sin token / 403 con tenant ajeno) corta **antes** de tocar el almacén (`handlers_test.go:72`).
- R-A2 {id} no-UUID → **404**, no 500 (tenants y solicitudes) (`:286`, `access_requests_test.go:715`); tenant inexistente en la aprobación → 404 (`:742`).
- R-A3 Paginación estable con desempate aunque compartan `created_at` (`postgres_test.go:214`).
- R-A4 Código de enrolamiento: TTL propagado a `expires_at` (`handlers_test.go:470`); `"WAPP-"` + 10 bytes aleatorios en hex (`generateEnrollmentCode`, `handlers.go:218-224`).
- R-A5 Aprobación: reintento **converge** tras fallo de identity; segunda aprobación **une** sistemas; sin poder **leer** el conjunto vigente no declara nada (`ErrSystemsUnionUnavailable`); sin M2M → 503 `ApprovePartialResult{local:"ok", identity:"skipped"}`; sin sistemas que conceder, M2M nil → 204 (`access_requests_test.go:274-574`).
- R-A6 Reaprobar con **otro rol** → rechazo (`ErrRetryRoleMismatch`) (`:625`); `wapp.platform` no se concede desde la bandeja; rechazar exige motivo.
- R-A7 La aprobación es atómica: si el INSERT del rol falla, la membresía tampoco queda (`executeapprovaltx_internal_test.go:66`) → F9.
- R-A8 Signup: 409 de identity **no adopta** la cuenta (`signup_test.go:142`); sin M2M 503 sin pánico; cuerpo por encima del límite → 400 antes de decodificar; correo sin `@` → 400 sin llamar a M2M; correo normalizado a minúsculas (misma fila); **ningún log lleva el correo** (A-12); el alta pública manda **una sola** aplicación (`:488`).
- R-A9 Rate-limit del signup: sin `trustProxy` la clave es la IP de socket, `X-Forwarded-For` no estrena cubo (`:224`).
- R-A10 Los handlers se construyen **inline** en el arranque (I-CP-5, trampa T-3 de [`reglas.md`](reglas.md)).

## 5 · Textos observables que quedan byte a byte

- **`iam/transport/http/http.go:88-121` (`writeDomainError`)**: `"entrada inválida"`, `"el token no trae empresa: no puede administrar roles ni miembros"`, `"recurso no encontrado"`, `"conflicto: el recurso ya existe o la persona ya pertenece a otra empresa"`, `"las plantillas de rol globales no se modifican desde una empresa"`, `"no autorizado"`, `"identity token inválido"`, `"al identity token le queda muy poca vida: refresca antes de canjearlo"`, `"usuario no migrado"`, `"identity no está disponible"`, `"identity_no_configurado"`, `"system_no_acreditable"`, `"error interno"`; más `"cuerpo JSON inválido"`, `"método no permitido"`, `"token requerido (cuerpo {token} o header Authorization)"`, `"modo dual apagado: identity no está configurado en este despliegue"`, `"no se pudo validar el token"`.
- **Canje** (`canje.go:134-156`): `mensajeInvitacionInservible` (el mismo para 404 y 410), `"esa invitación ya no está disponible, o esta cuenta ya pertenece a una empresa"`, `"falta el token de invitación"`.
- **Signup** (`platformadmin/signup.go:95-175`): `"la contraseña no cumple la política de seguridad (mínimo 12 caracteres)"`, `"ese correo ya tiene cuenta: entra con tu clave"`, `"servicio de identidad no disponible"`, `"error al procesar registro"`, `"demasiadas solicitudes desde esta IP"`, `"registro no disponible"`, `"cuerpo o campos de registro inválidos"`, `"error al configurar aplicaciones"`, `"error al registrar solicitud"` (el BFF y `wapp-ctl` los enseñan en texto plano).
- **platformadmin** `http.Error`: los 30 literales de `grep -hoE 'http\.Error\(w, "[^"]*"' internal/platformadmin/*.go` (p. ej. `"empresa no encontrada"`, `"la solicitud ya fue aprobada con un rol distinto; el reintento no converge"`).
- **Centinelas**: los 19 `"iam: …"` de `domain/errors.go` + `domain/canje.go:42`, los 9 `"platformadmin: …"` y los textos de los constructores `"iam: <Servicio> requiere …"` (`grep -rn 'errors.New(' internal/iam internal/platformadmin`).
- **entitlements**: `"feature_not_enabled"` y los prefijos `"entitlements: …"` de `postgres.go`.

## 6 · Candados de invariante que aterrizan en `acceso` (`05` §3.2)

| Candado viejo | Regla | Dónde queda |
|---|---|---|
| `internal/bootstrap/arranque/platform_permissions_test.go` (`TestINV056_1_*`, 2 tests) | 🔴 I-CP-5: toda ruta de plataforma exige permiso `.any`. Detecta por `"platformadmin."` en el 6.º argumento de `adminHandler` **o** por patrón `/admin/tenants`, `/admin/access-requests` (`:48-50`) | Su copia en `internal/arranque` (F0) sigue valiendo si el paquete nuevo se importa **como `platformadmin`**. Proceso F9 «canje y permisos»: un `tenant_admin` recibe 403 en una ruta `.any` |
| `iam/infra/postgres/canje_orden_ast_test.go` | acceso concedido antes de marcar la invitación | **Candado AST** en `A/iam/infra/postgres/` (D-F2-1) · F9 prueba la atomicidad |
| `iam/infra/postgres/canje_una_consulta_ast_test.go` (2) | una consulta en `leerInvitacion`; las 4 NULLables cableadas | el 1.º, candado AST; el 2.º, test de conducta de `filaAInvitacion` (R-P7) |
| `iam/infra/postgres/membresia_unica_ast_test.go` | un solo escritor de `tenant_members`, con cerrojo → guarda → INSERT en el cuerpo | candado AST en `A/iam/infra/postgres/`, **barriendo `internal/`** y esperando **los dos** escritores hasta F10; el viejo, ciego al árbol nuevo desde F0 (T0.27, D-F4-1) |
| (sin candado hoy) I-CP-6 fail-closed | 403 en los tres modos | `middleware_test.go` nuevo (R-E1) |

**Pasan a F9 («Canje de identidad y permisos», `05` §7.4)**: el SQL de las 7 suites contra Postgres;
R-P1…R-P4, R-P8; R-A5…R-A7 (aprobación, reintento, atomicidad); la migración 0038 (el IAM propio no
sobrevive: `integration_test.go:243`); I-CP-5 extremo a extremo.
