# F5 · Requisitos — historias y criterios EARS

> Roles y patrones: [`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §2.
> `N` = `internal/modulos/catalogo` · `V` = paquetes viejos de referencia.

## H5.1 · El módulo nace con contrato y test, sin lógica

> Como **la sesión web**, quiero **crear `catalogo`, `catalogo/indice` y `catalogo/catalogimport`
> (y `conversacion/model` si D-F5-1 = B) con solo su contrato y un test desde él**, para **que la
> lógica llegue validada**.

- **R5.1.a** · **EL** contrato de cada fichero **DEBERÁ** tener cuerpos `panic(pendiente.Implementar(…))`, sin valores cero. — Verifica: revisión de T5.2–T5.8 y `make test-pendiente`.
- **R5.1.b** · **EL** rojo **DEBERÁ** compilar con `-tags pendiente` y declarar solo exportados (el test de un auxiliar no exportado nace en el verde, y solo si lleva regla de negocio o ramas no triviales, `05` E-4). — Verifica: `GOWORK=off go vet -tags pendiente ./internal/modulos/catalogo/...; echo rc=$?` → 0 y `make lint` rc=0.
- **R5.1.c** · **SI** un test de `N` llama a `t.Skip`, **ENTONCES EL** gate **DEBERÁ** fallar. — Verifica: `grep -rn 't.Skip' internal/modulos/catalogo` vacío.

## H5.2 · El parser de runtime tolera el v2 y no deja a nadie sin catálogo

> Como **la dueña del negocio**, quiero **que un catálogo con un campo v2 mal escrito siga vendiendo**,
> para **no quedarme sin carrito por una etiqueta**.

- **R5.2.a** · **CUANDO** `ParseCatalog` recibe un blob v1, **EL** árbol serializado **DEBERÁ** ser
  byte a byte el golden `catalog_v1_parsed.golden.json`, sin avisos. — Verifica: `catalog_test.go` + `testdata/`.
- **R5.2.b** · **SI** `Raw` es nil, el blob no cuadra con el v1 (p. ej. `price` no numérico) o no hay
  categorías, **ENTONCES** `ParseCatalog` **DEBERÁ** devolver un error que satisface
  `errors.Is(err, model.ErrInvalidFlow)`. — Verifica: tabla de casos.
- **R5.2.c** · **SI** un campo v2 (subcategorías, subcategoría, tags, atributos, variantes,
  componentes) viene mal formado, **ENTONCES** `ParseCatalog` **DEBERÁ** descartarlo con un
  `CatalogWarning{Category, SKU, Field, Reason}` y **no** fallar. — Verifica: `TestParseCatalogToleraCamposV2Malformados`-equivalente.
- **R5.2.d** · **SI** un artículo usa un SKU con prefijo `_` (`SystemSKUPrefix`), **ENTONCES** **DEBERÁ** descartarse con aviso. — Verifica: caso de test.
- **R5.2.e** · **SI** un artículo trae variantes **y** componentes, **ENTONCES** **DEBERÁ** conservar las variantes y descartar los componentes con aviso. — Verifica: caso de test.
- **R5.2.f** · **CUANDO** hay más de 50 avisos, **EL** parser **DEBERÁ** guardar 50 y uno final `Field: "(varios)"` con `se omitieron N avisos más del mismo parseo`. — Verifica: caso de test.

## H5.3 · El importador es estricto y dice todo lo que falla de una vez

> Como **la dueña del negocio**, quiero **subir mi catálogo (JSON o planilla) y ver todos sus errores
> en una pasada, en palabras que entiendo**, para **corregirlo sin ir y volver veinte veces**.

- **R5.3.a** · **CUANDO** `Validate` recibe un documento con varios defectos, **EL** validador **DEBERÁ** devolverlos todos (hasta su tope, avisando del resto), cada uno con su ubicación y su motivo **literal** de `V/catalogimport/validator.go`. — Verifica: tests de `validator_test.go`.
- **R5.3.b** · **SI** la cabecera (`format` ≠ `wapp.catalog_import` o `version` ≠ 1) no se reconoce, **ENTONCES** **NO DEBERÁ** interpretar el cuerpo. — Verifica: caso de test.
- **R5.3.c** · **LO** que `Validate` acepta, `catalogo.ParseCatalog` **DEBERÁ** parsearlo **sin un solo aviso**; y la plantilla (`BuildTemplate`) **DEBERÁ** pasar su propio validador. — Verifica: dos tests cruzados.
- **R5.3.d** · **EL** prefijo reservado que rechaza el validador **DEBERÁ** salir de `catalogo.SystemSKUPrefix`, no de una copia. — Verifica: test que compara con la constante.
- **R5.3.e** · **CUANDO** el documento mide exactamente el techo, `ReadLimited` **DEBERÁ** aceptarlo; **SI** mide un byte más, **ENTONCES** **DEBERÁ** devolver `ErrDocumentTooLarge`. `Limits` ≤ 0 caen a 1 MiB y 500 artículos. — Verifica: `contract_test.go`/`validator_test.go`.
- **R5.3.f** · **EL** default de artículos (500) **DEBERÁ** coincidir con el de `WAPP_IMPORT_MAX_ITEMS` en `platform/config`. — Verifica: test que importa `internal/platform/config`.
- **R5.3.g** · **CUANDO** `ParseTabular` recibe la planilla de la plantilla, **DEBERÁ** devolver el mismo catálogo que `BuildTemplate`; cada defecto **DEBERÁ** citar su fila, en orden de fila. — Verifica: `tabular_test.go`.

## H5.4 · El índice busca por ítem sin leer ni parsear

> Como **el pipeline de captación** (consumidor en F7), quiero **un índice en memoria por tenant que
> solo se reconstruye cuando cambia el contenido**, para **no pagar un parseo por ítem**.

- **R5.4.a** · **CUANDO** un job de N ítems consulta un catálogo, **LA** caché **DEBERÁ** hacer 1 lectura y como mucho 1 construcción; con el mismo contenido, 0 construcciones. — Verifica: `cache_test.go` con una `Fuente` que cuenta.
- **R5.4.b** · **CUANDO** cambian los bytes del documento (por import o por `PUT` a mano), **LA** caché **DEBERÁ** reconstruir (huella SHA-256 de los bytes ya leídos). — Verifica: caso de test.
- **R5.4.c** · **CUANDO** se supera `MaxTenantsEnCache` (64), **LA** caché **DEBERÁ** desalojar el menos usado. — Verifica: caso de test.
- **R5.4.d** · **SI** el documento está roto, **ENTONCES LA** caché **NO DEBERÁ** guardar un índice a medias. — Verifica: caso de test.
- **R5.4.e** · **EL** `*Indice` **NO DEBERÁ** satisfacer `Fuente` (no puede leer). — Verifica: aserción de tipo en `indice_test.go`.
- **R5.4.f** · **SI** el catálogo tiene más de 2.000 artículos (sumando **todas** las categorías), **ENTONCES** `Construir` **DEBERÁ** devolver `ErrCatalogoDemasiadoGrande`; con 2.000, construye. — Verifica: `indice_test.go`.
- **R5.4.g** · **SI** falta el normalizador o no cumple el contrato, **ENTONCES** `NewCache`/`Construir` **DEBERÁN** fallar al construir. — Verifica: casos de test.
- **R5.4.h** · **EL** índice **DEBERÁ** devolver lo mismo que una búsqueda lineal sobre el corpus de prueba, y su P99 por ítem **DEBERÁ** ser ≤ 5 ms con 2.000 artículos. — Verifica: `indice_test.go` (diferencial) y el test de rendimiento (D-F5-3).

## H5.5 · Las fronteras quedan escritas

> Como **Jhoan**, quiero **que el índice no pueda entrar en el turno conversacional**, para **que el
> entrante no espere a parsear catálogos** (INV-02 / T1.5).

- **R5.5.a** · **SI** un paquete bajo `internal/modulos/conversacion/**` importa `internal/modulos/catalogo/indice`, **ENTONCES** `fronteras_test.go` **DEBERÁ** fallar. — Verifica: mutación en T5.6.
- **R5.5.b** · **EL** único import de `catalogo` hacia otro módulo **DEBERÁ** ser `conversacion/model` (o el puente de import declarado a `internal/flujos/model` si D-F5-1 = A). — Verifica: `fronteras_test.go`.

## H5.6 · Hacia fuera no cambia nada

> Como **la operación de UAT**, quiero **que F5 no altere el binario nuevo**, para **no tener nada que
> probar en UAT por esta fase**.

- **R5.6.a** · **CUANDO** F5 cierra, **LA** huella del arranque nuevo **DEBERÁ** ser idéntica y `FaseActual = 5` (TX.15). — Verifica: `huella_test.go`.
- **R5.6.b** · **LOS** textos observables de [`diseno.md`](diseno.md) §4 **NO DEBERÁN** cambiar (en particular los `"catalogo: …"` del índice renombrado, `04` §5.1). — Verifica: aserciones literales en los tests.
