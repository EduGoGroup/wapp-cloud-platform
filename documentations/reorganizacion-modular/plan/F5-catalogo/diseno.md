# F5 · Diseño — la vista micro

> `V` = referencia @ `1b18932`. Exportados con la regla awk de F4 (`diseno.md` de F4, cabecera) —
> aprox. 🔴 En el rojo **solo exportados** (regla T-1 de F1); `warnBag`, `parseCategory`,
> `variantRejection`, `huella`, `parsear`, `fuenteContenido`, `entradaCache`… nacen en su verde. Su test nace también en
> el **verde** y **solo si llevan regla de negocio o ramas no triviales** (`05` E-4, P6): p. ej. `variantRejection` y
> `huella`; no se testea fontanería ni `if err != nil`; el resto lo cubre F9. Niveles de ceremonia por paquete: §6.

## 1 · El árbol que se crea

```
internal/modulos/conversacion/model/          (si D-F5-1 = B)
└── model.go            model_test.go           porta V=internal/flujos/model/model.go (24 exportados · 406 l)
internal/modulos/catalogo/
├── catalog.go          catalog_test.go         porta V=internal/flujos/modules/cart/catalog.go SIN loadCatalog (11 · 582 l)
├── testdata/           catalog_v1.json · catalog_v1_parsed.golden.json · catalog_v2.json · catalog_v2_parsed.golden.json (copiados de V/cart/testdata)
├── indice/             package indice  (⚠️ renombre; los textos siguen diciendo "catalogo: …")
│   ├── normalizador.go normalizador_test.go    porta V=internal/intake/catalogo/normalizador.go (1 · 111 l)
│   ├── indice.go       indice_test.go          porta …/indice.go (17 · 377 l) — incluye el diferencial y el de rendimiento
│   └── cache.go        cache_test.go           porta …/cache.go (14 · 307 l)
└── catalogimport/
    ├── contract.go     contract_test.go        porta V=internal/catalogimport/contract.go (17 · 226 l)
    ├── validator.go    validator_test.go       (3 · 876 l)
    ├── diff.go         diff_test.go            (5 · 314 l)
    ├── template.go     template_test.go        (6 · 320 l)
    ├── prompt.go       prompt_test.go          (2 · 67 l)
    └── tabular.go      tabular_test.go         (1 · 609 l)
```

`V/catalogimport/limits_test.go` no tiene fichero homónimo: sus 6 casos van a `contract_test.go`
(`Limits`, `DefaultLimits`) y `validator_test.go` (`ReadLimited`). `V/intake/catalogo/dobles_test.go`
(fuentes falsas) y `rendimiento_test.go` se reparten en `cache_test.go` e `indice_test.go` (E-3: un
fichero, un test). `frontera_test.go` → `fronteras_test.go` + aserción de tipo (D-F5-2).

## 2 · Contratos, fichero a fichero

### `conversacion/model/model.go` (si D-F5-1 = B)

Tipos `Flow`, `Node`, `Content{Raw map[string]any, …}`, `ContentItem`, `ContentRef`, `MediaRef`,
`Conversation` (+ `Finished`, `Outcome`, `SetOutcome`), `Outcome` y sus constantes, `NodeType*`,
`NodeTerminal = "__wapp_flow_end__"` (**imprimible**: PostgreSQL rechaza `0x00` en `flow_state.current_node`, `model.go:26-33`),
`VarOutcome = "flow_outcome"`, `ErrInvalidFlow = errors.New("definición de flujo inválida")` (texto exacto),
`MarshalDefinition`, `UnmarshalDefinition`, `ParseAndValidate(data, moduleTypes...)`, `Validate(f, moduleTypes...)`.
Leer `V/flujos/model/model_test.go` (4 tests: `TestNodeTerminalIsTextSafe`, `TestValidate`,
`TestValidate_ModuleType`, `TestParseAndValidate`) y llevar sus reglas al comentario (qué rechaza la
validación: nodo inicial ausente, destino colgante, `NodeTerminal` como id, tipos de módulo no
registrados…). Etiquetas JSON **idénticas** (el JSON de `flow_definitions` es contrato con la BD).

### `catalogo/catalog.go`

| Exportado | Promete |
|---|---|
| `SystemSKUPrefix = "_"` | Prefijo reservado de líneas del sistema (`_shipping`); el runtime descarta, el import rechaza |
| `Catalog{Categories; Warnings []CatalogWarning \`json:",omitempty"\`}` · `CatalogWarning{Category, SKU, Field, Reason}` · `Category{Code, Label, Items, Subcategories \`omitempty\`}` · `Subcategory{Code, Label}` · `Article{Code, SKU, Label, Price, Description, Subcategory, Tags, Attributes, Variants, Components}` (los 5 v2 con `omitempty`) · `Variant{Code, Label, Price}` · `Component{SKU, Qty}` | 🔴 **Mismos nombres, orden y etiquetas JSON**: el golden serializa el árbol entero con los nombres Go (sin `json:"x"`), y un campo de más o reordenado rompe la no-regresión v1 |
| `Article.HasVariants()` · `Article.IsCombo()` | `len > 0` |
| `ParseCatalog(c model.Content) (Catalog, error)` | Re-serializa `c.Raw` y decodifica sobre los blobs; **v1 estricto** (un `price` no numérico tumba el parseo), **v2 tolerante** (cada campo v2 como `json.RawMessage` y decodificado aparte). Errores envueltos sobre `model.ErrInvalidFlow` con los tres textos de `catalog.go:261,268,273,277`. Reglas en §3 |

### `catalogo/indice/normalizador.go` · `indice.go` · `cache.go`

| Exportado | Promete |
|---|---|
| `Normalizador func(string) string` · `VerificarNormalizador(n) error` | Verifica caso a caso el contrato (el de `wapp-shared/textmatch.Normalize`: pliega mayúsculas y tildes —«Café» casa «cafe»—); falla con `ErrNormalizadorInvalido` |
| `MaxArticulos = 2000` · `ErrSinNormalizador` · `ErrNormalizadorInvalido` · `ErrCatalogoDemasiadoGrande` | Textos `catalogo: …` **exactos** (`indice.go:80,86,96`) — el renombre de paquete **no** los cambia (`04` §5.1) |
| `Coincidencia{…}` · `Indice` · `Construir(cat catalogo.Catalog, n Normalizador) (*Indice, error)` | Tope contado sobre **todas** las categorías (2.000 entra, 2.001 no); SKU repetido ⇒ **gana el primero**; el SKU **no** se normaliza; la variante viaja con **su** precio |
| `(*Indice).Articulos · Etiqueta(n) · En(n) · Hash · Sello · PorSKU · PorEtiqueta · PorTag · PorVariante` | Búsquedas sin I/O; resultado igual al de una búsqueda lineal (test diferencial con el corpus de `V/intake/catalogo/indice_test.go`) |
| `RefCatalogo = "catalogo"` · `MaxTenantsEnCache = 64` · `Documento{Raw []byte; Sello time.Time}` · `Fuente{LeerCatalogo(ctx, tenant) (Documento, error)}` · `LectorContenido{GetTenantContent(ctx, tenant, ref) ([]byte, error)}` · `NewFuenteContenido(lector, ref)` (ref vacía ⇒ `catalogo`) · `ErrSinFuente` · `Estadisticas{Aciertos, Construcciones, Desalojos}` · `Cache` · `NewCache(fuente, n, max)` (max ≤ 0 ⇒ 64; falla sin fuente o con normalizador inválido) · `(*Cache).Obtener(ctx, tenant) (*Indice, error)` · `Estadisticas()` (copia) · `Tamano()` | Lectura **fuera** del candado; huella SHA-256 hex de los bytes **ya leídos**; mismo hash ⇒ acierto sin indexar; distinto ⇒ indexa **dentro** del candado (dos llamadas simultáneas no construyen dos veces); desaloja por reloj lógico; errores `catalogo: leer el documento del tenant %q: %w` y `catalogo: tenant %q: %w`; un documento roto no deja índice |

### `catalogo/catalogimport/*`

| Fichero | Exportados | Promete |
|---|---|---|
| `contract.go` | `ImportFormat = "wapp.catalog_import"` · `ImportVersion = 1` · `DefaultMaxJSONBytes int64 = 1 << 20` · `DefaultMaxItems = 500` · `Limits{MaxJSONBytes, MaxItems}` · `DefaultLimits()` · `CatalogImport` · `ImportSource` · `ImportBody` · `ImportCategory` · `ImportSubcategory` · `ImportItem` · `ImportVariant` · `ImportComponent` · `ImportFieldError{Field, Reason…}` · `ImportValidationError` + `Error()` | Contrato JSON del documento (etiquetas **exactas**: las lee la consola y el prompt); límites ≤ 0 caen al default; `DefaultMaxItems` = default de `WAPP_IMPORT_MAX_ITEMS` (`platform/config/config.go:836`) |
| `validator.go` | `ErrDocumentTooLarge` (`"el documento de import excede el tamaño máximo permitido"`) · `ReadLimited(r, limits)` · `Validate(raw, limits) (CatalogImport, *ImportValidationError)` | Estricto y **acumulativo** (todos los defectos en una pasada, con tope y aviso); cabecera desconocida ⇒ no interpreta el cuerpo; SKU repetido entre categorías; `variants` y `components` a la vez ⇒ rechazo; prefijo reservado **desde `catalogo.SystemSKUPrefix`**; motivos **literales** (los ve la dueña) |
| `diff.go` | `Diff` + `Empty()` · `PriceChange` · `ItemRef` · `DiffCatalog(current catalogo.Catalog, next ImportBody) Diff` | Sin catálogo vigente ⇒ todo alta; lo que el motor descartaría se avisa; cambio de detalle ≠ cambio de precio; `qty` implícita no es cambio |
| `template.go` | `BuildTemplate()` · `TabularSheetName = "catalogo"` · `TabularEntrySeparator = ";"` · `TabularFieldSeparator = "\|"` · `TabularColumns()` (copia) · `TemplateSheetRows()` | La plantilla trae los cuatro casos (simple, variantes, combo, subcategoría), pasa su propio validador y el runtime la lee **sin un aviso** |
| `prompt.go` | `PromptContractVersion = 1` · `ImportPrompt()` | El texto dicta el contrato que el validador exige. ⚠️ Deuda D-20: **nunca se probó contra un LLM real** (`prompt.go:41`); con **cero gasto** (D-11, 2026-08-29) sigue así: se porta literal |
| `tabular.go` | `ParseTabular(rows [][]string, limits) (CatalogImport, *ImportValidationError)` | Plantilla ⇒ mismo catálogo; defectos con su fila y en orden de fila; cabecera tal como la devuelve Excel; solo cabecera; mini-sintaxis de artículo y de combo; filas vacías no cuentan; precio vacío no se importa «regalado» |

## 3 · Reglas del parser tolerante (E-8, de `catalog_v2_test.go`, `golden_test.go` y `catalog.go`)

1. Un blob v1 es un v2 válido y produce **el mismo árbol** que antes del v2 (golden).
2. Subcategorías: lista mal formada ⇒ se ignora entera; sin `code` o `code` repetido ⇒ descarte.
3. `subcategory` del artículo debe apuntar a una subcategoría de **su** categoría; si no, se ignora.
4. `tags`: lista mal formada ⇒ fuera; etiqueta vacía (`TrimSpace`) ⇒ fuera.
5. `attributes`: escalares (texto, número con `FormatFloat(f,'f',-1,64)`, booleano) a texto; objetos, listas y `null` fuera; claves recorridas **ordenadas** (los avisos no bailan).
6. `variants`: exige `code`, `label` y `price` (puntero: ausente ≠ 0) y `price ≥ 0`, `code` único; si no queda ninguna, aviso «el artículo se vende sin variantes».
7. `components`: sin `sku` ⇒ fuera; `qty` ausente ⇒ 1; `qty < 1` ⇒ 1 con aviso.
8. Variants **y** components ⇒ quedan las variantes (las que cobran).
9. Tope de 50 avisos + uno de resumen.
10. Todos los textos de aviso, literales (`catalog.go:239,314,322,324,344,364,379,391,403,409,431,437,442,464,479,491-499,517,523,530`).

## 4 · Textos y literales observables

- `"catalogo: …"` del índice (4 errores + 2 envolturas) — **no** pasan a `"indice: …"`.
- `ErrInvalidFlow` = `definición de flujo inválida` y los tres envoltorios de `ParseCatalog`.
- Los motivos de `ImportFieldError` del validador y de la planilla (van en el 400 que ve la dueña);
  `ErrDocumentTooLarge`.
- `wapp.catalog_import`, versión 1, hoja `catalogo`, separadores `;` y `|`, ref `catalogo`, prefijo `_`.
- La plantilla y el prompt, **byte a byte** (se descargan tal cual por I16–I17 en F8).

## 5 · Candados de invariante

| Candado viejo | Regla | Dónde queda |
|---|---|---|
| `internal/intake/catalogo/frontera_test.go` · `TestFrontera_NingunFicheroDeFlujosImportaElIndice` (AST, barre `../../flujos`, exige > 100 ficheros) | El índice no entra en el turno conversacional (INV-02/T1.5) | `internal/modulos/fronteras_test.go`: arista **prohibida** `conversacion/** → catalogo/indice` (incluye tests de `conversacion`, como el viejo) |
| `…/frontera_test.go` · `TestFrontera_ElIndiceNoPuedeLeer` | `*Indice` no satisface `Fuente` | `indice_test.go`: `_, ok := any((*Indice)(nil)).(Fuente)` ⇒ `false` (conducta de tipos, no AST) |
| `TestValidate_PrefijoReservado_SaleDeLaConstanteDelCart` | Una sola fuente del prefijo | `validator_test.go` contra `catalogo.SystemSKUPrefix` |
| `TestParseCatalogGoldenV1` | No regresión del v1 | `catalog_test.go` + `testdata/` |

El viejo `frontera_test.go` sigue verde (barre solo `internal/flujos`, que F5 no toca).

## 6 · Clasificación provisional por paquete (`05` E-12)

> **Provisional, sin medir: la fija el inventario E-12** (T5.1), que la baja a archivo y la aprueba Jhoan. Deducida de
> `arquitectura.md` §1–§4. Si un archivo sale peor, sube de nivel. Consumidores: los que tendrá el paquete nuevo
> (F7/F8); hoy, en producción, ninguno.

| Paquete | Estado en memoria | Concurrencia | BD / transacciones | Consumidores | Nivel provisional |
|---|---|---|---|---|---|
| `conversacion/model` (1, si D-F5-1 = B) | no | no | no | `catalogo`, `indice` y todo `conversacion` (F8) | **medio** (reglas de `Validate`) |
| `catalogo/catalog.go` (1) | no | no | no | `indice`, `catalogimport`, `captacion` (F7), carrito (F8) | **medio** (parser tolerante, 10 reglas) |
| `catalogo/indice` · `normalizador.go` | no | no | no | `indice`, `cache` | **simple** |
| `catalogo/indice` · `indice.go` | no (inmutable tras `Construir`) | no | no | `cache`, `stages`/`pipeline` (F7) | **medio** |
| `catalogo/indice` · `cache.go` | **sí** (`sync.Mutex`, mapa por tenant, LRU) | **sí** (test con `-race`) | no | 1 (`pipeline`, F7) | **complejo** (mutantes) |
| `catalogo/catalogimport` (6) | no | no | no | 1 (rutas I14–I17, F8) | **medio**; `contract.go` y `prompt.go`, candidatos a **simple** |

**Adaptadores `bridge_<x>.go`**: nacen 0, mueren 0. **Puertos con BD**: ninguno.
