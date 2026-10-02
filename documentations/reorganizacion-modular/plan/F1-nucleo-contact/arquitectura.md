# F1 · Arquitectura — la vista macro

> Medido sobre `dev` @ `1b18932` el 2026-09-28. `V` = `internal/flujos/contact` (viejo, referencia,
> **no se toca**) · `N` = `internal/nucleo/contact` (nuevo).

## 1 · Paquete viejo → nuevo

| Viejo (`V`) | Líneas (com./código) | Nuevo | Qué es |
|---|---|---|---|
| `contact.go` | 170 (59/99) | `N/contact.go` | Dominio puro: kinds, `Ref`, `Normalize`, `NewRef`, `ValidateKind` |
| `resolver.go` | 151 (47/92) | `N/resolver.go` | El **puerto** `Resolver` + `StateMigrator`, centinelas, `Ref.Sendable`, `RefsFrom`, auxiliares |
| `repository_memory.go` | 183 (33/134) | `N/repository_memory.go` | Gemelo en memoria. **0 usos en producción**; 89 líneas en 57 ficheros de test ajenos |
| `repository_postgres.go` | 460 (190/254) | `N/repository_postgres.go` | Adaptador `database/sql` sobre `public.contacts` (+ `public.flow_state` en la fusión) |
| — | — | ✚ `N/contacthelpertest/contrato.go` y 8 `*_contrato.go` | Suite `Contrato` del puerto (E-3), un fichero por tema (README hallazgo 20) |
| — | — | ✚ `N/contacthelpertest/estado.go` | Doble de `flow_state` en memoria (implementa `StateMigrator` y el observador de la suite) |
| — | — | ✚ `internal/arranque/bridge_contact.go` | Adaptador de tipos viejo ← nuevo (§3). Nace en F1, muere en F8 |

Contado con `wc -l` y `grep -cE '^\s*//'`. `go list -f '{{.GoFiles}}'` confirma **4** ficheros de
producción. `05` §6 acierta: **solo depende de `platform`**.

**Imports de `V`** (`go list -f '{{join .Imports "\n"}}'`): `context`, `database/sql`, `errors`,
`fmt`, `strings`, `sync`, `time`, `github.com/google/uuid`, `internal/platform/crypto`,
`internal/platform/storage/postgres`. `N` importa lo mismo. **Puentes (`05` §4.1): cero.**
`N/contacthelpertest` importa `testing` + `N` + `uuid`.

**Tablas**: `public.contacts` (PK `(tenant_id, kind, value_bidx)`, índice `(tenant_id, contact_id)`,
`0006_contacts_cifrado.sql:44-58`; sobre de `push_name` en `0069`; columnas en claro retiradas en
`0070`) y, solo en la fusión, `public.flow_state` (DELETE + UPDATE, `repository_postgres.go:391-415`).
`contacts` está **dos veces** en `rekeyTargets` (`platform/crypto/rekey.go:60` y `:161`): la
rotación de KEK es de `platform`; `N` solo tiene que leer cada fila con **su** `kek_id`.

## 2 · Los consumidores de hoy

Medido con `GOWORK=off go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./internal/...` (solo
producción) y `grep` del símbolo. **7 paquetes de producción** + 64 ficheros de test fuera de `V`.

| Consumidor (producción) | Qué usa | Fase que lo reconstruye | Tras F1 |
|---|---|---|---|
| `bootstrap/arranque/flows.go:20,35,88` | `Resolver`, `PostgresResolver`, `NewPostgresResolver` | — (viejo, oráculo) | **Sin cambio**. Su copia en `internal/arranque` conmuta (§4) |
| `flujos/runtime` · `runtime_engine.go:67,483` · `start.go:106-109` · `incoming.go:117-118,1292` · `send.go:238-242` | El **`Resolver` inyectado** (`Resolve`, `Destino`), `Ref`, `RefsFrom`, `Normalize`, `Sendable` | F8 | Recibe el adaptador; sigue llamando a las funciones puras de `V` |
| `intakes/notifier.go:114-115,434-440` | Interfaz propia `Destinations{Destino(…) (contact.Ref, error)}` + `Sendable` | F6 | Recibe el adaptador |
| `gateway/grpc/connect.go:753` | `Normalize(KindPhoneE164, …)` sobre `self_pn` | F3 | Sigue con `V` (puro) |
| `gateway/fleet/fleet.go:52` | `Normalize(KindPhoneE164, …)` | F3 | Sigue con `V` (puro) |
| `flujos/admin/handlers.go:50,166-175` | `Ref`, `NewRef`, `KindPhoneE164` | F8 | Sigue con `V` (puro) |
| `publicapi/flows.go:99-108` | `Ref`, `NewRef`, `KindPhoneE164` | F8 (ruta, D-10) | Sigue con `V` (puro) |

Implementa `StateMigrator` sin importarlo: `flujos/store/repository_memory.go:170`
(`MigrateContactID`), solo para tests. Solo **dos** puntos reciben la **instancia** del resolver:
`fase6_solicitudes.go:42` (`intakes.NewNotifier`) y `fase7_flujos.go:228` (`flowruntime.New`),
ambos con `c.flowDeps.contacts`, construido en `fase3_almacenes.go:57` → `flows.go:88`.

## 3 · El adaptador de tipos: por qué hace falta y qué hace

`N.Ref` y `V.Ref` son **tipos distintos** para Go aunque tengan los mismos campos. El
`*N.PostgresResolver` tiene `Resolve(ctx, t, []N.Ref, pn)`, así que **no** satisface `V.Resolver`
(que pide `[]V.Ref`), ni `intakes.Destinations` (que devuelve `V.Ref`). `runtime.New` e
`intakes.NewNotifier` son viejos y **no se tocan** (E-1). Alternativas medidas:

| Opción | Veredicto |
|---|---|
| Alias `type Ref = N.Ref` dentro de `V` | ❌ edita el código viejo (E-1) |
| `N` con alias hacia `V` | ❌ puente de `nucleo` hacia `flujos`: el revés de lo que se busca |
| **Adaptador en `internal/arranque`** | ✅ el único sitio que ve a los dos por diseño (F0 ya cablea paquetes viejos) |

`internal/arranque/bridge_contact.go` (sin exportados; ✎ D-F1-9, 2026-10-02: los nombres de esta sección eran `puente_contact.go`, `puenteContact` y `nuevoResolverDeContactos`):

- `type contactBridge struct{ nuevo *contact.PostgresResolver }` que implementa `viejo.Resolver`
  (y por tanto `intakes.Destinations`).
- `Resolve`: copia `[]viejo.Ref` → `[]contact.Ref` campo a campo y delega. `Destino`: delega y copia
  de vuelta. **No re-normaliza** (hoy `Resolve` tampoco: confía en refs de `NewRef`).
- **Errores**: devuelve un error cuyo `Error()` es **el mismo texto** y cuyo `Unwrap() []error`
  contiene el centinela nuevo **y** el viejo equivalente (`ErrNoRefs`, `ErrNoDestino`,
  `ErrContactNotFound`). Medido: ningún código de producción hace hoy `errors.Is` sobre un centinela
  de `contact` fuera del paquete (`grep -rn 'contact\.Err' --include='*.go' internal cmd | grep -v _test`
  → 0 fuera de `V`), así que es fidelidad barata, no una necesidad presente.
- **Vida**: nace en F1 · F6 lo deja de usar para el notificador (el `intakes` nuevo recibe tipos de
  `N`) · **muere en F8**, cuando el `runtime` nuevo recibe `N.Resolver`.

## 4 · Cableado en `internal/arranque`

- La copia de F0 de `bootstrap/arranque/flows.go` (`buildFlowRuntimeDeps`, llamada desde la copia de
  `fase3_almacenes.go:57`) es la que cambia. Si F0 renombró esos ficheros, **manda F0**.
- Se extrae una costura probable sin R2: `newContactResolver(db, cipher, kp) *contactBridge`,
  que construye `contact.NewPostgresResolver(db, cipher, kp)` **con el `cipher` y el `kp` que ya
  construyó la fase** (los mismos que usan `fleet`, `events`, `intakes`, `integrations`,
  `tenantllm`: `fase3_almacenes.go:98-190`). 🔴 Un segundo `KeyProvider` con otro índice haría que
  `value_bidx` no case y **duplicaría contactos en silencio**.
- El campo `contactsPG *contact.PostgresResolver` de la copia se **elimina**: está muerto (se escribe
  en `flows.go:91` y no se lee en ningún sitio; su motivo, el backfill `BackfillPushName`, murió en
  `58e92a2`, T5.4). `flowRuntimeDeps.contacts` sigue siendo `viejo.Resolver` (lo pide `runtime.New`).

## 5 · Estado en memoria, goroutines, métricas

| | Medido | Consecuencia |
|---|---|---|
| Estado de paquete en `V` | 4 centinelas `errors.New` + `destinoPref` (mapa de solo lectura, `resolver.go:62`) | Inmutable: que el binario nuevo enlace `V` y `N` a la vez no duplica nada |
| Estado de `PostgresResolver` | Solo `db`, `cipher`, `kp` (punteros compartidos); **sin caché** | Una sola instancia nueva; ninguna vieja en `server-modular` |
| Estado de `MemoryResolver` | Mapas + `sync.Mutex`; **0 usos en producción** | Irrelevante para el arranque |
| Goroutines · métricas | 0 · 0 (`grep -n 'go func\|prometheus\|metrics' V/*.go`, sin tests) | La huella no cambia… ni puede delatar un error de F1 |

## 6 · Rutas HTTP y rpc (D-10) — *a reconciliar con FX*

`plan/FX-cara-http/mapa-de-rutas.md` no existía al escribir esto: esta sección es **propuesta** y
**manda FX**. Rutas que tocan `contact` (contadas por registro en ejecución; cada patrón se registra
una vez, sin bucle):

| Ruta | Registro | Handler | Qué usa de `contact` | ¿Es de `contact`? |
|---|---|---|---|---|
| `POST /api/v1/flows/{id}/start` (`:8103`, permiso `flows.start`) | `publicapi/publicapi.go:460` | `publicapi/flows.go:131` | `NewRef`, `KindPhoneE164`, `Ref`; el texto `"contact_ref inválida: "+err.Error()` (`:159`) | **No**: es del motor (usa `flowadmin.Starter` = `*flowruntime.Runtime`, singleton con estado) → se muda en **F8** |
| `/admin/flows/start` (`:8100`) | `bootstrap/arranque/rutas_admin.go:102` | `flujos/admin/handlers.go:203` | Idem (el texto, en `:231`) | **No** → F8 (y es `:8100`, fuera de `publicapi`) |

**F1 muda cero rutas a `internal/apipublica`.** Ningún rpc usa el resolver; `gateway/grpc` solo
llama a `Normalize` (F3). Ojo: el 400 de esas rutas lleva hoy el prefijo **dos veces**
(`"contact_ref inválida: contact_ref inválida: kind desconocido \"x\""`): es observable y se conserva.

## 7 · Lo que no cambia hacia fuera

Tablas, columnas y SQL (84 migraciones) · textos de error · cifrado (mismo `FieldCipher`, DEK del
**envelope de PII**, no la del ADR-0007 — ver [`reglas.md`](reglas.md) R-homónimo) · rutas · rpc ·
variables `WAPP_*` · métricas · `cmd/server` entero.
