# F5 · Arquitectura — la vista macro

> Medido el 2026-09-28 sobre `dev` @ `1b18932` con `GOWORK=off go list`, `wc -l` y `grep -c '^func Test'`.

## 1 · Paquetes viejos → nuevos

| Viejo (referencia) | Nuevo | Prod · líneas | Tests viejos (ficheros · `Test*`) |
|---|---|---|---|
| `internal/flujos/modules/cart/catalog.go` (**solo este fichero**, sin `loadCatalog`) | `internal/modulos/catalogo/catalog.go` (paquete **`catalogo`**) | 1 · 582 | `catalog_test.go` 1 · `catalog_v2_test.go` 6 · `golden_test.go` 1 (+ `testdata/catalog_v{1,2}.json` y `…_parsed.golden.json`) |
| `internal/intake/catalogo` | `internal/modulos/catalogo/indice` ⚠️ **renombre** `catalogo → indice` | 3 · 795 (`cache` 307 · `indice` 377 · `normalizador` 111) | 5 · 22 (`cache` 9 · `indice` 10 · `frontera` 2 · `rendimiento` 1 · `dobles` 0) |
| `internal/catalogimport` | `internal/modulos/catalogo/catalogimport` | 6 · 2.412 (`contract` 226 · `diff` 314 · `prompt` 67 · `tabular` 609 · `template` 320 · `validator` 876) | 5 · 48 (`diff` 5 · `limits` 6 · `tabular` 16 · `template` 8 · `validator` 13) |
| `internal/flujos/model` (si **D-F5-1 = B**) | `internal/modulos/conversacion/model` | 1 · 406 | `model_test.go` · 4 |

Total (con B): **11 ficheros · 4.195 líneas** de producción; 12 ficheros · 82 `Test*` viejos que leer
(ninguno de integración: no hay SQL en F5). **No hay adaptador Postgres** ni puerto sin gemelo:
`05` E-6 no aplica. El único puerto (`indice.Fuente`, `cache.go:84`) tiene su adaptador puro
(`fuenteContenido`, sobre un `LectorContenido{GetTenantContent}` estructural) y dobles de test.

Lo que **no** entra en F5 (y por qué, `04` §5):
- `cart/revalidate.go` (195 l) **se queda en el carrito**: usa `cartLine`, `lineLabel`, `money`,
  `summaryWith`, `variantSKUSuffix` y `PriceListOf` devuelve un `intakes.PriceList`; nadie fuera del
  carrito la llama en producción (citado de `04` §5, no re-medido).
- `cart/note.go` (150 l) va a **`solicitudes/intakes/note.go` en F6**: es el contrato de las
  columnas `intake_items.customization` e `intakes.customer_note`, cuyo dueño es `intakes`.
- `loadCatalog` (`catalog.go:574-582`) se queda en el carrito (usa `catalogVarKey`, `state.go:101`).

## 2 · Grafo de imports y orden

Medido (`go list -f '{{.Imports}}'`, producción):

```
cart (catalog.go)    → flujos/model
intake/catalogo      → flujos/model · flujos/modules/cart
catalogimport        → flujos/modules/cart
flujos/model         → (nada interno)
```

En el árbol nuevo: `catalogimport → catalogo`; `indice → catalogo, conversacion/model`;
`catalogo → conversacion/model`. Orden de contratos y verde (hojas primero):
**`conversacion/model` → `catalogo/catalog.go` → (`indice` ∥ `catalogimport`)**. Dentro de `indice`:
`normalizador` → `indice` → `cache`. Dentro de `catalogimport`: `contract` primero; el resto
(`validator`, `diff`, `template`, `prompt`, `tabular`) depende de él (orden fino entre ellos: sin
medir; se resuelve compilando).

Tests viejos con imports de fuera: `catalogimport` tests → `flujos/model`, `cart`, `platform/config`
(`limits_test.go`, R5.3.f); `intake/catalogo` tests → `flujos/model`, `cart`.

## 3 · Imports hacia fuera del módulo, y D-F5-1

| Import nuevo | Clase | Estado |
|---|---|---|
| `wapp-shared/textmatch` (solo en tests y en quien construye la caché: el normalizador se **inyecta**) | externo | permitido |
| `internal/platform/config` (solo test de `contract.go`) | `platform` | permitido |
| `modulos/conversacion/model` | módulo, **arista medida** `catalogo → conversacion` (`02` §4, ciclo 2) | permitido con D-F5-1 = B |
| `internal/flujos/model` | **puente** al viejo | solo con D-F5-1 = A: nace en T5.2, muere en F8 re-tocando `catalog.go`, `indice/cache.go` y sus tests |

**Por qué la arista a `model` no forma ciclo**: `model` no importa nada interno. Lo que `catalogo`
usa de él: `model.Content{Raw map[string]any}` (entrada de `ParseCatalog`; `cache.go:306`
`parsear`) y `model.ErrInvalidFlow` (envuelto 6 veces en `catalog.go`). Cambiar la firma a
`map[string]any` rompería el `errors.Is(err, model.ErrInvalidFlow)` de quien consume el error (el
motor, F8): cambio de conducta, descartado (`04` §5.2).

**Consumidores de `ErrInvalidFlow` fuera de `model`** (`grep -rn ErrInvalidFlow internal | grep -v _test`):
`flujos/admin/handlers.go` (1), `flujos/content/{json,router}.go` (5+1), `flujos/engine/engine.go` (7),
`cart/catalog.go` (6), `flujos/modules/media/media.go` (7): **todos de F8**. Ninguno en `intake/**`.

## 4 · Estado en memoria, goroutines, métricas, relojes

| Qué | Dónde | Consecuencia |
|---|---|---|
| **`indice.Cache`**: `sync.Mutex`, `map[tenant]*entradaCache`, reloj **lógico** (`uint64`, no de pared) y `Estadisticas{Aciertos, Construcciones, Desalojos}` | `cache.go:161-170` | Estado de proceso: **una** caché por binario. En F5 **no se instancia** en producción (la vieja la sigue usando el pipeline viejo, `bootstrap/arranque/fase5_captacion.go:245`); en F7 la nueva la **sustituye** |
| Lectura fuera del candado, indexado dentro | `cache.go:198-227,229-232` | Contrato de concurrencia (test con `-race`) |
| Goroutines | ninguna (`grep -n 'go func' …` vacío) | — |
| Métricas | ninguna; las estadísticas van al log del worker | — |
| Relojes | `Documento.Sello` (`time.Time`, hoy siempre cero: `GetTenantContent` no lee `updated_at`, `cache.go:71-77`) | Sin reloj real en la lógica |
| Rendimiento | `PlazoPorItem = 5 ms` (`rendimiento_test.go:22`) | D-F5-3 |

## 5 · Cableado y conmutación

**F5 no toca `internal/arranque`.** Razón medida: el único constructor de producción del índice es
`catalogo.NewCache(catalogo.NewFuenteContenido(c.flowStore, ""), textmatch.Normalize, 0)`
(`fase5_captacion.go:245`) y su consumidor `pipeline.NewWorker` pide `Catalogos{Obtener(ctx, tenant)
(*catalogo.Indice, error)}` con el tipo **viejo** (`pipeline.go:137`, afirmado en `:158`); `stages/match.go:303`
también. `*Indice` tiene campos no exportados: no hay adaptador posible. ⇒ el índice nuevo lo cablea
**F7** al conmutar `captacion`; el modelo nuevo lo usan **F7** (`stages`, `pipeline`, `reanalisis`) y
**F8** (`cart`, rutas I14–I17).

`FaseActual = 5` (TX.15) y la huella idéntica son el único cambio observable del binario nuevo.

## 6 · Rutas

Ninguna se muda en F5 (autoridad FX, filas I14–I17): `POST /api/v1/catalog/import`,
`POST …/tabular`, `GET …/template`, `GET …/prompt` escriben/leen por `flujos/store`
(`publicapi/catalogimport.go:36-38`, `tenantcontent.go:35-39`) y montan juntas (`publicapi.go:1093-1124`):
se mudan en **F8**. En F8, `apipublica/catalog*.go` importará `modulos/catalogo/catalogimport` y
`modulos/catalogo` (hoy `publicapi/catalogimport.go:305` llama a `cart.ParseCatalog(model.Content{Raw: raw})`).

## 7 · Lo que no cambia hacia fuera

La forma del blob `tenant_content` (ref `catalogo`), el contrato `wapp.catalog_import` v1, los textos
de error del validador que ve la dueña en el 400, el techo de 1 MiB y de 500 artículos
(`WAPP_IMPORT_MAX_ITEMS`), la hoja `catalogo` de la planilla y sus separadores, y los goldens del
parser. Nada de esto viaja todavía por el binario nuevo en F5.
