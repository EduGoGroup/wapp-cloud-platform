# FX · Diseño — los ficheros de `internal/apipublica`, sus contratos y cómo se prueban

> Nombres de fichero: se **conserva** el del fichero viejo que se porta (así la cabecera E-10
> `// Porta internal/publicapi/x.go @ <sha>` se lee sola). Los nuevos llevan nombre en español.
> Cada fichero sigue el ciclo de `05` §4 con la skill `contrato-tdd`.

## 1 · El árbol, y cuándo nace cada fichero

```
internal/apipublica/
├── apipublica.go            F0  ✚ Cara: el ServeMux propio + la lista de patrones registrados
├── estrangulador.go         F0  ✚ Componer / Compuesto / Resolver — se borra en F10
├── cadena.go                F2  ↦ publicapi.go:1126-1144 (protect, protectRead) + accesslog.go + Comun
├── respuesta.go             F2  ↦ writeJSON/writeJSONErr (publicapi.go:1149-1170), writeError (messages.go:322), parseIntQuery (audit.go:70)
├── autenticacion.go         F2  ↦ el montaje de A1–A7 que hoy hace internal/bootstrap/arranque/http.go:80-171
├── roleplane.go             F2  ↦ roleplane.go (B1–B14)
├── audit.go                 F2  ↦ audit.go (C1)
├── entitlements.go          F2  ↦ entitlements.go (C2)
├── apipublicatest/arnes.go  F2  ✚ el arnés de test: firma Context Tokens y llama a una Cara
├── plazos.go                F3  ↦ publicapi.go:289-428 (dbCtx, defaultDBTimeout, SendBudgetFrom, sendCtx, dbTimedOut504)
├── limits.go                F3  ↦ limits.go (tooLarge, writeTooLarge, errorBody)
├── messages.go              F3  ↦ messages.go (D1) + sessionBelongsToTenant, streamCaidoFrom, commandIDFrom
├── sessions.go              F3  ↦ sessions.go (D2)
├── health.go                F3  ↦ health.go (HealthRules, Alerter, NoopAlerter)
├── sessionadmin.go          F3  ↦ internal/flujos/admin/sessions.go (D3, D4 y J16–J17 de :8100) — D-FX-2
├── diagnostics.go           F3  ↦ diagnostics.go (D5, D6)
├── intents.go               F7  ↦ intents.go (E1, E2) — sobre captacion/intentcfg nuevo, sin puente (D-FX-1/D-F7-4)
├── tenantllm.go             F4  ↦ tenantllm.go (F1–F3)
├── degradationnotices.go    F4  ↦ degradationnotices.go (F4)
├── instantes.go             F6  ↦ formatInstant (conversationevents.go:215), que ya usa intakes.go
├── intakes.go               F6  ↦ intakes.go (G1–G6, G8) + registerIntakes (publicapi.go:648-792)
├── intakes_llm_gate.go      F6  ↦ intakes_llm_gate.go
├── export.go · summary.go   F6  ↦ export.go (G9) · summary.go (G10)
├── quotesuggestion.go       F6  ↦ quotesuggestion.go (G7)
├── plazoescritura.go        F6  ↦ plazoescritura.go — ✎ el plazo llega por parámetro (mapa §4.3)
├── tenantvariables.go       F6  ↦ tenantvariables.go (G11–G12)
├── integrations.go          F6  ↦ integrations.go (G13–G16)
├── crmcallback.go           F6  ↦ crmcallback.go (G17)
├── eventstelemetry.go       F6  ↦ eventstelemetry.go (G18)
├── eventstelemetry_store.go F6  ↦ eventstelemetry_store.go — adaptador Postgres (E-6: fuera del 80 %) — D-FX-4
├── reanalyze.go             F7  ↦ reanalyze.go (H1)
├── flows.go                 F8  ↦ flows.go (I2–I4) + el montaje de I1 e I11–I13 (handlers de conversacion/admin)
├── media.go                 F8  ↦ media.go (I5)
├── tenantcontent.go         F8  ↦ tenantcontent.go (I6–I10)
├── catalogimport.go         F8  ↦ catalogimport.go (I14) + registerCatalogImport
├── catalogtabular.go        F8  ↦ catalogtabular.go (I15)
├── catalogtemplate.go       F8  ↦ catalogtemplate.go (I16–I17)
├── conversationevents.go    F8  ↦ conversationevents.go (I18)
└── conversationeventcancel.go F8 ↦ conversationeventcancel.go (I19)
```

**39** ficheros de producción + `apipublicatest/arnes.go`, cada uno con su `x_test.go`
(`publicapi` tiene 33; la diferencia: el estrangulador, la `Cara`, `autenticacion.go`,
`sessionadmin.go`, y las utilidades partidas en `cadena`, `respuesta`, `plazos`, `instantes`).

**No hay un `Deps` único.** Cada fichero de área declara **sus** puertos (interfaces del lado del
consumidor, con tipos del módulo **nuevo**) y un `Deps<Área>` + `Montar<Área>(c *Cara, k Comun,
d Deps<Área>)`. Así cada fase toca solo sus ficheros y el arranque llama a un `Montar` más por fase.
La condición de montaje de cada ruta (mapa, «Monta si») viaja **dentro** de su `Montar`.

## 2 · F0 · `apipublica.go` y `estrangulador.go`

**`apipublica.go`** — comentario de paquete: *qué es la cara nueva, D-10, que convive con la vieja
por el estrangulador hasta F8 y que las cadenas por ruta viven aquí.*

| Exportado | Promete |
|---|---|
| `type Cara struct` | Un `*http.ServeMux` propio y la lista de patrones que se le registraron, en orden |
| `func Nueva() *Cara` | Una cara vacía: `Patrones()` vacío, y toda petición → 404 |
| `func (c *Cara) Handle(patron string, h http.Handler)` | Registra en el mux **y** anota el patrón. Un patrón en conflicto hace `panic` **igual** que `ServeMux.Handle` (no lo esconde) |
| `func (c *Cara) Patrones() []string` | Copia de la lista; mutarla no altera la cara |
| `func (c *Cara) Handler(r *http.Request) (http.Handler, string)` | Lo que diga `ServeMux.Handler`; no escribe ni muta `r` |
| `func (c *Cara) ServeHTTP(w, r)` | Delega en el mux |

**`estrangulador.go`** — comentario: *por qué dos muxes, por qué no un `"/"`, que gana la nueva, y
que se borra en F10.*

| Exportado | Promete |
|---|---|
| `func Componer(nueva *Cara, vieja *http.ServeMux) *Compuesto` | Un `nil` en cualquiera de los dos hace `panic` al construir (fallo de cableado, no de petición) |
| `func (x *Compuesto) ServeHTTP(w, r)` | (1) si `nueva.Handler(r)` da patrón ≠ `""` → `nueva.ServeHTTP(w, r)`; (2) si no, si `vieja.Handler(r)` da patrón ≠ `""` → `vieja.ServeHTTP(w, r)`; (3) si ninguna casa: si la nueva **conoce la ruta** con otro método → `nueva.ServeHTTP` (su 405 con su `Allow`); si no → `vieja.ServeHTTP` (su 404 o su 405). Siempre con **el mismo `r`**; no escribe nada propio |
| `func (x *Compuesto) Resolver(r *http.Request) (cara, patron string)` | `("nueva", p)`, `("vieja", p)` o `("", "")` con la misma lógica de (1)–(2), sin servir. Es lo que usan la huella y el candado de mudanzas |

«Conoce la ruta» = algún método de los que aparecen en `Patrones()` (más los patrones sin método)
casa con `r.URL.Path`; se prueba con una copia superficial de `r` con otro `Method`. Solo corre en
el camino de error (método equivocado). Coste **sin medir**; irrelevante fuera de ese camino.

Tests (`estrangulador_test.go`), uno por promesa: `TestComponer_GanaLaNueva` ·
`TestComponer_DelegaEnLaVieja` · `TestComponer_MismaPeticion` (el handler viejo ve el mismo
puntero y `r.Pattern` queda con el patrón viejo tras servir) · `TestComponer_405DeLaCaraQueConoce`
(familia en la nueva, método ajeno → 405 `Allow` de la nueva; familia en la vieja → 405 de la
vieja) · `TestComponer_404` · `TestComponer_NoEscribeNada` · `TestComponer_NilPanic` ·
`TestResolver`. Sin BD, sin red: `httptest`.

## 3 · F2 · lo común

- **`cadena.go`** — `type Comun struct { MW *httpapi.Middleware; Auditor httpapi.AuditRecorder;
  Log sharedlogger.Logger }` y los no exportados `protect`, `protectRead`, `accessLog`,
  `anotarTenant`, `respuestaObservada` (estos **nacen con el verde**: el rojo lleva solo exportados,
  [`../00-marco/tecnologia.md`](../00-marco/tecnologia.md) §3.1). Promesas que salen de los comentarios viejos
  (`publicapi.go:1126-1144`, `accesslog.go:12-126`) y que el test afirma: orden exacto de la
  cadena; `accessLog` por **fuera** de `Authenticate` (ve el 401); cero PII en el log (solo
  `r.URL.Path`, nunca la query); `write_error` a nivel error cuando el `Write` falla; logger `nil`
  ⇒ sin envolver.
- **`respuesta.go`** — `writeJSON` descarta el fallo de escritura **por contrato**; `writeJSONErr`
  lo devuelve (el incidente del 2026-08-06, `publicapi.go:1155-1159`); `writeError` con cuerpo
  `{"error": msg}`; `parseIntQuery` con su valor por defecto.
- **`apipublicatest/arnes.go`** — `Arnes` con `Llamar(cara, credencial, método, destino, cuerpo)
  *httptest.ResponseRecorder`, que firma un Context Token real con `sharedjwt` y grants dados (el
  patrón de `publicapi/publicapi_test.go:131-168`), un `AuditorDoble` que cuenta registros y un
  `Comun` de prueba. Tiene lógica (firma) → lleva su test.

## 4 · El patrón de test de un fichero de área (E-3, sin BD)

```go
// entitlements_test.go — cubre el contrato de entitlements.go
func TestMontarDerechos(t *testing.T) {
	a := apipublicatest.Nuevo(t)
	cara := apipublica.Nueva()
	apipublica.MontarDerechos(cara, a.Comun(), apipublica.DepsDerechos{
		Entitlements: resolverDoble{plan: "basic", features: []string{"cart_basic"}, ttl: time.Minute},
	})
	if got := a.Llamar(cara, "", "GET", "/api/v1/entitlements", "").Code; got != 401 { … }            // sin token
	if got := a.Llamar(cara, a.Con("tenantA"), "GET", "/api/v1/entitlements", "").Code; got != 403 { … } // sin grant
	rec := a.Llamar(cara, a.Con("tenantA", "entitlements.read"), "GET", "/api/v1/entitlements", "")
	// 200 · {"plan":"basic","features":["cart_basic"],"cache_ttl_seconds":60} · tenant del TOKEN (INV-8)
	vacia := apipublica.Nueva()
	apipublica.MontarDerechos(vacia, a.Comun(), apipublica.DepsDerechos{}) // sin dependencia
	// GET /api/v1/entitlements → 404 de ruta inexistente
}
```

Por cada ruta del fichero, como mínimo: **401** sin token (salvo A1, A2, A7, G17), **403** sin el
grant, **403 `feature_not_enabled`** sin la feature (si la hay), el **camino feliz** con su cuerpo,
**un registro** en el auditor doble solo si es W (y ninguno si es R), **404** sin dependencia (si es
condicional), y cada **código de error del dominio** que el handler traduce (centinelas → 404,
409, 410, 504…). Los dobles de los puertos van en el propio `x_test.go` o, si los comparten varios
ficheros, en `apipublicatest/`. `t.Skip` prohibido.

## 5 · Tests viejos que hay que LEER antes de cada contrato (E-8)

| Fase | Ficheros de `internal/publicapi/` (y vecinos) |
|---|---|
| F0 | `internal/bootstrap/arranque/mux_registration_test.go` (el conflicto de patrones panica al registrar) |
| F2 | `publicapi_test.go` (arnés), `roleplane_test.go`, `membersalta_test.go`, `invitations_test.go`, `tenantless_test.go`, `entitlements_test.go`; `internal/iam/transport/http/*_test.go` (6); `arranque/{roleplane,invitaciones}_cableado_test.go` |
| F3 | `messages_senderror_test.go`, `dbtimeout_o3_test.go`, `sessions_test.go`, `health_test.go`, `diagnostics_test.go`, `limits_test.go`; `internal/flujos/admin/sessions_test.go`; `arranque/send_budget_cableado_test.go`, `arranque/filters_config_test.go` |
| F4 | `tenantllm_test.go`, `tenantllm_gate_via_test.go`, `degradationnotices_test.go` |
| F6 | `intakes_test.go` y los 10 `intakes_*_test.go`, `buyer_data_leak_test.go`, `export_test.go`, `export_internal_test.go`, `summary_test.go`, `quotesuggestion_test.go`, `plazoescritura_test.go`, `integrations_test.go`, `crmcallback_test.go`, `crmcallback_schema_test.go`, `tenantvariables_test.go`, `eventstelemetry_test.go`, `eventstelemetry_internal_test.go`; `arranque/quotetext_cableado_test.go` |
| F7 | `reanalyze_test.go`, `intents_test.go`, `intents_aditividad_test.go` (E1–E2 se mudan aquí, D-FX-1/D-F7-4); `arranque/reanalisis_cableado_test.go` |
| F8 | `flows_durable_guard_test.go`, `flows_streamcaido_test.go`, `triggers_test.go`, `media_tenantcontent_test.go`, `catalogimport_test.go`, `catalogtabular_test.go`, `catalogtemplate_test.go`, `conversationevents_test.go`, `conversationeventcancel_test.go` |

Los **14** `*_integration_test.go` de `publicapi` no se leen para contratos de fichero: son
escenarios de proceso y alimentan la lista de F9 (`05` §7.4).

## 6 · Los candados de la cara

| Candado | Dónde | Qué hace fallar |
|---|---|---|
| Mudanzas | `internal/arranque/mudanzas_test.go` + `internal/arranque/testdata/mapa.tsv` (id · listener · patrón · fase) | (a) la cara nueva, montada con dobles de todas sus dependencias, no resuelve **exactamente** las filas con fase ≤ `FaseActual`; (b) una fila de la cara nueva con comodín solapa con un literal que queda en la vieja; (c) una familia partida entre las dos caras |
| Huella | `internal/arranque/huella_test.go` (lo escribe F0) | un patrón del arranque viejo que el compuesto no resuelve con el **mismo texto**, o al revés |
| Fronteras | `internal/modulos/fronteras_test.go` (F0) con `internal/apipublica` en su alcance | un import de `apipublica` a código viejo que no sea un puente declarado (§5 de [`arquitectura.md`](arquitectura.md)) |
| E-3/E-9 | `un_fichero_un_test_test.go` · `exportados_cubiertos_test.go` con alcance sobre `internal/apipublica` | un `x.go` sin test o un exportado sin mención |

`FaseActual` es una constante de `internal/arranque` que la tarea `conmutar(<m>)` de cada fase
incrementa **en el mismo commit** que muda las rutas: el candado obliga a que la tabla y el
cableado avancen juntos.

**Cómo se enumera una cara sin poder listar un `ServeMux`**: por cada fila de `mapa.tsv` se
sintetiza una petición (método del patrón o `GET` si no tiene; cada comodín → `x`) y se pregunta a
`Compuesto.Resolver`. Un patrón de más que no esté en la tabla no se detecta así; lo detecta
`Cara.Patrones()` para la nueva (debe ser ⊆ tabla) y el `panic` de conflicto para la vieja.
