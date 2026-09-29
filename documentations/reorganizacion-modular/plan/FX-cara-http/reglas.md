# FX · Reglas — lo que no se toca, las trampas y la definición de hecho

## 1 · Lo que no se toca

- `internal/publicapi/**` **no se edita** en todo FX (E-1): es la referencia y lo que sirve UAT
  hasta el relevo. Se **borra** en F10, entero, con sus 64 tests.
- `internal/bootstrap/**` y `cmd/server` tampoco: la cara nueva solo la monta
  `cmd/server-modular` → `internal/arranque`.
- El texto de cada patrón (método, camino, **nombre de cada comodín**): la etiqueta `route` de
  `wapp_http_requests_total` y `wapp_http_request_duration_seconds` es `r.Pattern`
  (`internal/platform/metrics/metrics.go:214`).
- Los permisos, los recursos de auditoría, los gates y su orden, las condiciones de montaje y los
  textos de error (mapa §2; `03` §1).

## 2 · Trampas conocidas

| # | Trampa | Dónde | Cómo se evita |
|---|---|---|---|
| T-1 | **Un comodín en la cara nueva tapa un literal de la vieja**: la precedencia por especificidad de Go vale dentro de un mux, no entre dos | `GET /api/v1/intakes/{id}` frente a `…/export` y `…/summary.json` (`publicapi.go:658,788,790`); `DELETE /api/v1/invitations/{id}` frente a `POST …/accept` (`roleplane.go:160`, `arranque/http.go:115`) | Mudar juntos (mapa §4.2); candado de mudanzas (b) |
| T-2 | **Familia partida → 405 falso o 404** | cualquier camino con varios métodos (p. ej. `tenant-variables`, `publicapi.go:881-883`) | Todos los métodos de un camino en la misma fase; candado (c); paso (3) del estrangulador |
| T-3 | **Un `"/"` en el mux nuevo como catch-all** mata el 405 de toda la cara | — (tentación de diseño) | Despachador con `ServeMux.Handler`, [`diseno.md`](diseno.md) §2 |
| T-4 | **Copiar `r` en el estrangulador** (`r.WithContext`, `r.Clone`) antes de servir: `r.Pattern` se escribe en la copia y la métrica queda `unmatched` | `metrics.go:209-220` | El estrangulador pasa el **mismo** `r`; test `TestComponer_MismaPeticion` |
| T-5 | **Envolver dos veces** rate-limit o métricas (una por cara) | `arranque/http.go:193-195` | Se envuelve el **compuesto**, una vez; aserción de cableado |
| T-6 | **El plazo de G7 dentro de `protectRead`**: el `ResponseWriter` de `accessLog` corta la cadena del `ResponseController` y el plazo no llega a la conexión | `publicapi.go:766-768`, `plazoescritura.go:125-136` | `conPlazoDeRedacción(… protectRead(…))`, por fuera; test F6 |
| T-7 | **El callback CRM bajo `protect`** lo dejaría inalcanzable (no trae JWT) | `publicapi.go:1037-1056` | Solo `accessLog`; RX.2.g |
| T-8 | **Poner el gate del re-análisis en la cadena** invierte el orden 400→403 del contrato §8.1 | `publicapi.go:709-731` | H1 sin gate en la cadena; los gates viven en el servicio |
| T-9 | **Centinela viejo frente a gw nuevo** (F3→F8) | `publicapi/flows.go:235`, `flujos/admin/handlers.go:326`, `gateway/session/registry.go:22` | D-F3-2 (identidad vía `platform`, sin puente; alternativa D-FX-3); test de identidad hasta F8 |
| T-10 | **Encender una sola de las dos vías del perfil de sesión** la deja muda sin rojo | `fase8_transporte.go:81-85,133-138` | D3 y J16 pasan al handler portado en el **mismo** commit |
| T-11 | **Montar lo que no debe existir**: una ruta condicional montada sin su dependencia responde 500 en vez de 404 | los `if d.X != nil` de `publicapi.go` y `roleplane.go` | La condición viaja dentro del `Montar`; test «sin dependencia → 404» |
| T-12 | **Desmontar el alta de miembros sin M2M** (404) cuando el contrato es 503 | `roleplane.go:128-131`, `arranque/http.go:63-66` | RX.2.e |
| T-13 | **Una segunda instancia del gw** para la cara vieja (o para un servicio viejo): el envío sale por un gateway sin conexiones y se pierde | `fase4_gateway.go:45` | RX.4.a: aserción de identidad de puntero |
| T-14 | **Contar líneas en vez de registros**: `POST /api/v1/signup` aparece en dos líneas y es un patrón | `arranque/http.go:165,168` | Regla de conteo del mapa §0 |
| T-15 | **`writeError` y `parseIntQuery` viven en ficheros de área** que se mudan tarde, pero los usan áreas tempranas | `messages.go:322` (F3) lo usa `entitlements.go` (F2); `formatInstant` en `conversationevents.go:215` (F8) lo usa `intakes.go` (F6) | Nacen en ficheros comunes con su **primer** consumidor ([`diseno.md`](diseno.md) §1) |
| T-16 | **El detector de I-CP-5 lee el texto `platformadmin.` inline** en cada `adminHandler(…)` | `rutas_admin.go:13-28`, `platform_permissions_test.go` | En F2, `rutas_admin.go` conserva la construcción **inline** de J4–J11 |

## 3 · Prohibiciones

- Ni `git mv` ni copia por script de `publicapi` a `apipublica`: cada fichero se **crea** (E-1) por
  contrato → rojo → verde, leyendo antes los tests viejos de [`diseno.md`](diseno.md) §5.
- `t.Skip` en `internal/apipublica` (E-5). Postgres en un test de `apipublica` (E-6): el único
  adaptador SQL (`eventstelemetry_store.go`) tiene test unitario de mapeo y su SQL lo cubre F9.
- Mudar una ruta fuera de su fila del mapa sin corregir el mapa **y** `testdata/mapa.tsv` en el
  mismo commit.
- Un import de `apipublica` a código viejo que no esté en la lista de puentes.

## 4 · Definición de hecho

**De un tramo de fase Fn** (lo cierra la tarea `conmutar(<m>)` de Fn):

1. Las filas del mapa con «Muda en» = Fn se sirven por la cara nueva; `FaseActual = Fn`;
   `go test ./internal/arranque -run 'Mudanzas|Huella' -v` → PASS, **0 SKIP** (contados con `-v`).
2. En la cara vieja, los campos de `Deps` de esas rutas van a `nil` (tabla de
   [`arquitectura.md`](arquitectura.md) §4).
3. Cada fichero nuevo de `apipublica` en verde, ≥ 80 % de sentencias (salvo
   `eventstelemetry_store.go`), con su cabecera `Porta …`.
4. `make ci-local; echo rc=$?` → `rc=0` (sin pipe), con la toolchain fijada.
5. Si Fn cambia handlers de `:8100` (F2, F3, F8), el test de registro del mux admin del arranque
   nuevo pasa y la huella de `:8100` es igual.

**De FX entero** (F8 + F10): RX.3.d, RX.6.b y RX.6.c verdaderos.
