# F5 · `catalogo` — el modelo del catálogo, su índice de búsqueda y el importador

> **Estado**: spec escrita el 2026-09-28 sobre `dev` @ `1b18932` (código) / `bad573a` (plan). Sin
> código. Marco: [`../00-marco/`](../00-marco/README.md). Norma: [`05`](../../05-metodo-contratos-y-tdd.md).
> Rutas: [`../FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) — **F5 no muda
> ninguna** (TX.15).
>
> Recalibrado el 2026-10-03 tras la parada de F1 (`05` E-12, §4.2, E-9, E-4; `plan/DECISIONES.md` §3).
>
> ✅ **Estado al 2026-10-07 (F45-03): CERRADA.** La definición de hecho de [`reglas.md`](reglas.md) §4 se cumple en sus
> 10 puntos, con números (hallazgo 25); T9.26: la suite entera de procesos contra el binario nuevo da
> `RC=0 · PASS=869 · FAIL=0 · SKIP=0`. `catalogo` sigue **fuera** de `Conmutados` hasta que F7 conmute el índice (§4.10).
> Lo que heredan F7 y F8, al final de este fichero.
>
> ✎ **Estado anterior, al 2026-10-07 (F45-02): código entero en verde y conmutación nominal, sin cerrar.**
> `internal/modulos/catalogo` (`catalog.go`, `catalogimport`, `indice`) e `internal/modulos/conversacion/model` sin
> etiqueta `pendiente`; `FaseActual = 5` (`13e869f`), huella igual, ninguna ruta mudada y ningún cableado. `catalogo`
> **no** está en `Conmutados` (entra cuando F7 conmute el índice: [`reglas.md`](reglas.md) §4.10). **Queda F45-03**
> (T5.20–T5.21, con T9.26).

## Objetivo en tres líneas

Crear el paquete nuevo `internal/modulos/catalogo` con el **modelo del catálogo extraído del
carrito** (`internal/flujos/modules/cart/catalog.go`), su índice en memoria (`internal/intake/catalogo`
→ `catalogo/indice`, el **único renombre** del árbol) y el importador (`internal/catalogimport`), por
contrato → rojo → verde. **F5 no conmuta nada en el arranque**: sus consumidores (pipeline, carrito y
rutas de import) se reconstruyen en F7 y F8, y son ellos quienes lo cablearán.

**0 adaptadores `bridge_<x>.go` y sin BD**: F5 no crea ni retira ningún adaptador de arranque (`05` §4.2) y no tiene
ningún puerto con BD (no hay suite con `Montaje` que correr contra Postgres). La conmutación es **nominal**:
`FaseActual = 5`.

## Entradas

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | F0 cerrado (`pendiente`, `make test-pendiente`, `cobertura-ficheros`, `fronteras_test.go`) | `ls internal/pendiente internal/modulos` |
| E2 | F4 conmutado antes de escribir código de F5 (orden de `05` §6; F5 no depende de F4 en código, pero comparte gate y sesiones). El inventario E-12 (T5.1) se adelanta a la sesión F45-01, junto al de F4 | `ESTADO.md` |
| E3 | 🔴 **D-F5-1 decidida** (¿`conversacion/model` se reconstruye aquí o se tiende un puente?) | [`arquitectura.md`](arquitectura.md) §3 |
| E4 | `dev` verde | skill `validar-antes-de-cerrar` |

## Salidas

| # | Condición | Verifica |
|---|---|---|
| S1 | 11 ficheros de producción (10 + `conversacion/model/model.go` si D-F5-1 = B) y 11 tests en verde, más `testdata/` con los goldens | `grep -rln 'go:build pendiente' internal/modulos/catalogo internal/modulos/conversacion/model` vacío |
| S2 | Un test por promesa del contrato; mutantes en el nivel complejo (`indice/cache.go`); procesos de F9. Sin umbral de cobertura (P2): `make cobertura-ficheros` es informe (la tabla va al PR; no bloquea) | revisión del PR |
| S3 | `fronteras_test.go` prohíbe `conversacion/** → catalogo/indice` y permite `catalogo → conversacion/model` | T5.6 |
| S4 | El arranque nuevo **no** cambia; `FaseActual = 5`; huella idéntica (TX.15) | T5.19 |
| S5 | 0 `t.Skip`, 0 `pendiente.Implementar` | `grep` |

## Bloques de sesión

F4 y F5 **comparten sesiones** (son pequeñas: ≈ 10 archivos de producción cada una). Cada sesión: 45–90 min y cierre
de tres cosas (tareas `[x]` con SHA, bloque en `ESTADO.md`, hallazgos en este README).

| Sesión | Entorno | Tareas de F5 | Punto de parada |
|---|---|---|---|
| [`F45-01`](../sesiones/F45-01-web-inventario-e-inferencia.md) · inventario E-12 (de F4 y de F5) + F4 entero en verde | 💻 | T5.1 | Jhoan aprobó la tabla de niveles de F5 y D-F5-1 está anotada |
| [`F45-02`](../sesiones/F45-02-web-conmutar-inferencia-y-catalogo.md) · (conmutar F4 y) F5 entero + conmutación nominal | 💻 | T5.2–T5.19 (+ TX.15) | todo sin etiqueta `pendiente`; huella idéntica con `FaseActual = 5`; `ci-local` rc=0 con 0 SKIP |
| [`F45-03`](../sesiones/F45-03-cli-cierre.md) · cierre local de las dos fases | 💻 | T5.20–T5.21 (+ T9.26) | definición de hecho de [`reglas.md`](reglas.md) §4; procesos de F9 contra el binario nuevo |

✎ **2026-10-07**: F45-01 y F45-02 figuraban como 🌐; las dos se hicieron en local (💻, D-R-8: desde el 2026-10-04 no hay
sesiones web). El «web» del nombre de sus fichas es histórico.

## Decisiones que necesita (con recomendación)

| Id | Pregunta | Recomendación | Bloquea |
|---|---|---|---|
| 🔴 **D-F5-1** | `catalog.go` depende de `flujos/model` (`ParseCatalog(c model.Content)` y envuelve `model.ErrInvalidFlow`, `catalog.go:259-277`). ¿(A) **puente** `catalogo → internal/flujos/model` hasta F8, o (B) **reconstruir** `model` aquí como `internal/modulos/conversacion/model`? | **(B)**. Datos: `model` es **hoja** (`go list` → 0 imports internos), 1 fichero · 406 l · 24 exportados · 4 tests (274 l); **ningún** consumidor del `catalogo` nuevo existe en producción antes de F7, y los que comparan `ErrInvalidFlow` (`flujos/{admin,content,engine,modules/media}` y el carrito) son todos de F8 ⇒ con (B) no hay puente **ni re-toque**; con (A) F8 re-toca `catalog.go`, `indice/cache.go` y 3 tests, y declara/retira un puente. Coste de (B): +1 fichero de `conversacion` adelantado | T5.2 |
| **D-F5-2** | El candado `internal/intake/catalogo/frontera_test.go` (el índice no entra en el turno conversacional) | Su regla va a `internal/modulos/fronteras_test.go` como **arista prohibida** `conversacion/** → catalogo/indice` (así lo dice `05` §3.2) + la aserción de tipo «el `*Indice` no es una `Fuente`» en `indice_test.go` | T5.6 |
| **D-F5-3** | `rendimiento_test.go` (P99 por ítem ≤ 5 ms con 2.000 artículos, reloj real) en la VM web | Portarlo igual (umbral y corpus) y, si en la web resulta inestable, anotarlo en el traspaso — **sin** `t.Skip`. Sin medir en la VM | T5.14 |

## Contradicciones encontradas

1. **`04` §5 dice que `catalog.go` es «autocontenido» y no lo es del todo**: `loadCatalog`
   (`catalog.go:574-582`, no exportado) usa `catalogVarKey` (`cart/state.go:101`,
   `= modules.VarContentRaw`) y solo lo llama `cart.go:204`. ⇒ `loadCatalog` **no** va a `catalogo`:
   se queda en el carrito nuevo (F8), que llamará a `catalogo.ParseCatalog`.
2. **La técnica de alias temporales de `04` §5 no aplica** con el método de `05`: el carrito viejo no
   se toca (E-1), así que sigue con **su** `catalog.go`; los consumidores viejos (`stages`,
   `pipeline`, `catalogimport`, `publicapi`, `reanalisis`) siguen con el viejo; el
   `conversacion/cart` nuevo (F8) y la `captacion` nueva (F7) importan `modulos/catalogo`. No hay
   alias ni en lo viejo ni en lo nuevo.
3. **`05` §4 paso 5 (CONMUTAR) no tiene contenido en F5**: `pipeline.Catalogos` exige
   `*catalogo.Indice` **viejo** (`internal/intake/pipeline/pipeline.go:137,158`) y `stages/match.go:303`
   también; no hay adaptador posible (campos no exportados). El índice nuevo se cablea en F7; las 4
   rutas de import, en F8 (FX: escriben por `flujos/store`, `publicapi/catalogimport.go:36-38`).
4. `05` §6 fila F5 habla de «puente a `conversacion/model` hasta F8»: con D-F5-1 = B **no hay puente**.

## Hallazgos

### F45-01 (2026-10-06, inventario E-12; rama `reorg/f45-01-inventario-inferencia`)

1. **Los tests viejos son 13 + 1 ficheros, no 12**; los 78 + 4 `Test*` sí cuadran (`dobles_test.go` no tiene ningún `Test*`).
2. 🟡 **E-13 no está en esta spec y parte tres ficheros**: `validator.go` (876) y `tabular.go` (609) superan el tope de
   600, y `catalog_test.go` fusionaría 614 líneas de tres tests viejos. El árbol de `diseno.md` §1 crecerá.
3. 🟡 **`limits_test.go:150`** (`TestConfig_TechoDeTenantContent_SoloPorSuNombreNuevo`) prueba `platform/config`, no
   `Limits` ni `ReadLimited`: la spec reparte «sus 6 casos» sin decir dónde va este. ~~Por decidir en F45-02.~~
   ✎ **2026-10-07**: resuelto en F45-02 (hallazgo 14): **no** se porta; se queda en el viejo.
4. **El índice viejo se cablea en las dos copias del arranque** (`internal/arranque/fase5_captacion.go:249` e
   `internal/bootstrap/arranque/fase5_captacion.go:245`); la spec solo cita la segunda.
5. **Los ayudantes de test del carrito no viajan solos**: `rawFromFile`, `readTestdata`, `assertGolden`, `dumpCatalog`
   (`golden_test.go`) y `rawFromJSON` (`catalog_test.go:14`) hay que recrearlos en el `catalog_test.go` nuevo.

### F45-02 (2026-10-07; rama `reorg/f45-02-conmutar-inferencia-catalogo` desde `dev` @ `3c74b80`)

6. **`internal/modulos/catalogo/catalog.go` queda en 597 líneas** (a 3 del tope duro de 600, E-13): cualquier añadido
   obliga a partirlo. `conversacion/model/model_test.go`, 518 (en tolerancia).
7. **Sin tocar `Makefile` ni candados**: `PENDIENTE_DIRS` y `COBERTURA_DIRS` ya cubren `internal/modulos`.
8. **`loadCatalog` no se porta** (T-2); el flag `-update` del golden tampoco (los goldens son los del carrito). Los
   ayudantes del hallazgo 5 están recreados en `internal/modulos/catalogo/helpers_test.go`.
9. **Los tests viejos de `model` solo miraban `errors.Is`** (`internal/flujos/model/model_test.go:176-186`): los textos
   de rechazo de `Validate` y su orden ahora son literales. El tope de 50 avisos se cubre con 49/50/51/60/250.
10. **El texto «el carrito exige content.raw…»** (`internal/modulos/catalogo/catalog.go:281`) es literal observable
    aunque el paquete ya no sea del carrito.
11. **Comportamientos del viejo fijados tal cual por el corpus adversario (`catalog` / `model`)**: el prefijo reservado
    no recorta (`cart/catalog.go:342`: `" _shipping"`, NBSP o U+200B delante no se descartan; `＿shipping` tampoco); el
    artículo con SKU reservado se cae sin mirar sus campos v2; solo las etiquetas pasan por `TrimSpace` (`:408`; U+200B
    y BOM no son espacio); `qty` se trunca (`:528`: 2.9 → 2 sin aviso, 0.5 → 1 con aviso); dígitos no ASCII en `price`
    de artículo tumban el blob y en variante/componente tumban toda su lista; una variante descartada no gasta su code
    (`:470-474`); sin variantes utilizables sobreviven los `components` (`:362`); el v1 no valida negocio; `Validate` no
    recorta ni detecta ciclos (`flujos/model/model.go:329,351`); tipo de nodo `""` pasa si está en `moduleTypes`
    (`:383`); `Conversation.Outcome()` con un `Outcome` tipado en `Vars` devuelve «sin declarar» (`:253`).
12. **`catalogimport` partido por E-13** (resuelve el hallazgo 2): `validator.go` (876) → `validator.go` 379 +
    `validator_catalog.go` 181 + `validator_item.go` 267 + `validator_fields.go` 143; `tabular.go` (609) → `tabular.go`
    400 + `tabular_cells.go` 160 + `tabular_locate.go` 118. Solo se movieron declaraciones (multiconjunto de líneas
    viejo ↔ nuevo comparado). Los trozos de **producción** nacen en el verde, no en el rojo: solo contienen auxiliares
    no exportados (lint `unused`); en el rojo nacen sus gemelos de test.
13. **La spec no prevé tests de paquete interno; hicieron falta dos**: `contract_limits_test.go` y
    `tabular_locate_column_test.go`. «≤ 0 ⇒ default» solo es observable por `ReadLimited` / `Validate`. `868f8d8`
    (template) adelanta en `tabular.go` el bloque de constantes `col…`.
14. **`limits_test.go:150`** (`TestConfig_TechoDeTenantContent_SoloPorSuNombreNuevo`) prueba `platform/config`: **no** se
    porta (resuelve el hallazgo 3). [`diseno.md`](diseno.md) dice 6 casos de `limits_test.go`; son 5 + el no portado.
15. **Cifra de T5.8**: `catalogimport` medía 120 `Test*` pendientes en 12 ficheros etiquetados (`ROJOS=12`,
    `PENDIENTES=11`); el «≈37» de [`tareas.md`](tareas.md) no cuadra con lo medido — corregido el 2026-10-07 (nota
    fechada junto a la cifra).
16. 🟡 **Corpus adversario en `catalogimport` (fijado tal cual)**: el prefijo reservado no se recorta en el camino JSON
    (`internal/catalogimport/validator.go:558`) pero la planilla sí recorta la celda ⇒ `" _shipping "` se rechaza por
    planilla y se acepta por JSON (decisión de producto, no tocado); `sku` y `code` viajan sin recortar (`:749-759`);
    subcategoría asimétrica (referencia recortada `:572-575`, declaración no `:475`); U+200B no es espacio
    (`tabular.go:573-581`); precio en planilla = `strconv.ParseFloat` (`tabular.go:452`: acepta `1e3`, `0x1p4`,
    `1_000`, `+5`, `.5`); cantidad = `strconv.Atoi` (`:428`); `;;` se descarta y `||` rompe la entrada; atributo
    repetido en celda gana el último sin aviso (`:375`); el BOM solo se quita si es lo primero de la celda (`:201`);
    `null` como documento da dos defectos de cabecera. Rama inalcanzable desde `ParseTabular` en `tabularColumn`
    (`tabular.go:532-537`).
    — Registrado el 2026-10-07 como deuda **D-26**, **D-27** y **D-28** en [`deuda.md`](../../../deuda.md), con su veredicto.
17. **`indice`: 47 `Test*`. Mutantes de `cache.go` a mano: 42 aplicados, 41 muertos, 1 vivo equivalente** (`<=` por `<`
    al elegir víctima: el reloj no deja dos marcas iguales). Por grupo (aplicados/muertos): huella 8/8, reloj y
    contadores 5/5 (el «acierto que no avanza el reloj» estaba vivo y **no** era equivalente: lo mata `3a79ba8`),
    desalojo 10/9, candado 5/5 (`Estadisticas` y `Tamano` sin candado solo mueren con `-race`), tenant 2/2, documento
    roto 6/6, adaptador y constructor 6/6. Los tests viejos de desalojo no fijaban que el acierto avance el reloj lógico
    (`internal/intake/catalogo/cache_test.go:242-265`).
18. **T5.14 · P99 por ítem** (`-count=5`, sin `-race`, plazo 5 ms, rc=0): 56,125 µs · 62,375 µs · 56,125 µs · 41,25 µs ·
    76,917 µs. Con `-race`: 126 µs. El control «sin índice» del test de rendimiento cuesta ≈ 2 s sin `-race` y ≈ 24 s
    con `-race` (`indice_performance_test.go:78-93`): casi todo el tiempo del paquete en cualquier gate con `-race` (el
    viejo paga lo mismo).
19. 🟡 **`cache.go:249`** (viejo `internal/intake/catalogo/cache.go:253`): el desalojo usa `victim == ""` como centinela;
    con un tenant de id vacío la víctima puede no ser la menos usada, y `Obtener` no rechaza el id vacío. Portado tal
    cual.
    — Registrado el 2026-10-07 como deuda **D-29** en [`deuda.md`](../../../deuda.md) (se deja); la línea en el código nuevo es `cache.go:299`.
20. **D-F5-2 sin ampliar el motor de fronteras** (decisión (c) de Jhoan, 2026-10-07): la arista
    `conversacion → catalogo/indice` vive en la **regla 1**; comentario en la tabla y mutación documentada en `dec75e7`
    (test temporal en `conversacion/model` importando `catalogo/indice` ⇒
    `regla 1 … el módulo conversacion no puede importar catalogo (fuera de Capas)`, rc=1; revertido rc=0). 🟡 **F8 la
    romperá** al añadir `catalogo` a `Capas["conversacion"]` para el carrito: hará falta una prohibición por subpaquete
    que el motor no sabe expresar.
21. **Corpus adversario del índice (fijado)**: SKU opaco (`"CAFE "`, `" TORTA"` y fullwidth son claves distintas); el
    SKU vacío se indexa; label de solo espacios normaliza a `""`; separadores repetidos no se colapsan; dígitos no ASCII
    no se pliegan; un documento roto no borra el índice anterior del tenant; mismos bytes con otro `Sello` son acierto y
    conservan el sello antiguo; `null` y `{}` dan el error de `ParseCatalog` sobre `ErrInvalidFlow`.
22. **Erratas de la spec**: `diseno.md:18-20` sitúa `Normalizador` en `normalizador.go` (vive en `indice.go`, viejo y
    nuevo); `diseno.md:32` prevé 2 ficheros de test para dobles y rendimiento (han salido 8 por E-13) — las dos, corregidas el 2026-10-07 (nota fechada en
    `diseno.md` §1); `reglas.md:44`: el `grep` del gate §4.3 casa comentarios (motivo de `0977194`) — corregido el 2026-10-07 (nota fechada
    junto a la regla, en [`reglas.md`](reglas.md) §4.3, y la misma en la de F4; la regla no cambia). Los exportados en español del índice
    (`Construir`, `Obtener`…) se conservan (precedente de F4); dobles y auxiliares, en inglés (E-11; la correspondencia,
    en la cabecera de la sesión en [`tareas.md`](tareas.md)).
23. **`TestIndice_IsNotAFuente`** (sustituye la 2.ª mitad del `frontera_test.go` viejo) entró en `d84475c` porque
    `Fuente` nace con el contrato de la caché. `textmatch` ya tiene release (v0.1.0 en `go.mod`): el puerto inyectado se
    conserva para no tener dos normalizadores.
24. **T5.19**: el diff de `13e869f` solo toca `FaseActual` y su aserción; el mapa no tiene filas de fase F5 (I14–I17
    hasta F8) y la cara sigue sirviendo 33 rutas. **`catalogo` no entra en `Conmutados`** (decisión (b) de Jhoan,
    2026-10-07): el arranque nuevo sigue cableando el índice viejo (hallazgo 4); entra cuando F7 lo conmute
    ([`reglas.md`](reglas.md) §4.10).

**No corrido en F45-02 (es de F45-03)**: `make test-procesos`, la integración vieja contra Postgres y el arranque real de
`cmd/server-modular`. ✎ *(F45-03: aquí decía también «y UAT»; UAT no es de F45-03, es de **F10**,
[`plan/README.md`](../README.md).)*

**De la sesión F45-03 (2026-10-07, cierre local, rama `reorg/f45-03-cierre`):**

25. **Definición de hecho, punto a punto** (sobre `59a3e6b`, rc leído del log, sin pipe; `go1.26.5`, lint `v2.12.2`):
    (1) `GOWORK=off make ci-local` `GATE_RC=0`: 167 `ok`, 0 `FAIL`, lint `0 issues.`; (2) `make vet-pendiente` rc=0;
    (3) el `grep` da 0 líneas; `PENDIENTES=0 · ROJOS=0`; (4) cobertura (informe), los 15 ficheros de
    `internal/modulos/catalogo`: 10 al 100,0 % y `diff.go` 98,9 %, `tabular.go` 98,0 %, `template.go` 97,7 %,
    `validator_fields.go` 96,0 %, `normalizador.go` 98,9 %; ninguno entre los 7 `POR_DEBAJO`; (5) `go test -v` de
    `catalogo` y `conversacion/model` rc=0, 547 PASS, **0 SKIP**; (6) `fronteras_test.go` sin cambio desde `dec75e7`;
    (7) huella idéntica con `FaseActual = 5`; (8) esta documentación; (9) T9.26: `BINARIO=nuevo make test-procesos`
    `MAKE_RC=0`, `RC=0 · PASS=869 · FAIL=0 · SKIP=0`, `TestP7_Catalog` PASS; (10) `Conmutados` = `{"acceso","edge"}`.
26. 🟡 **T9.26 no prueba el código de F5, y hay que decirlo.** `internal/modulos/catalogo` no tiene un solo consumidor
    fuera de su árbol: el arranque nuevo cablea el índice **viejo** (`internal/arranque/fase5_captacion.go` importa
    `internal/intake/catalogo`) y `publicapi` usa el `internal/catalogimport` viejo. `TestP7_Catalog` pasa contra el
    binario nuevo ejercitando código viejo (`reglas.md` §4.9 ya lo avisa). Lo que sostiene a F5 hasta F7 y F8 son sus
    tests unitarios, los mutantes del índice y el corpus adversario; el primer proceso que toque el índice nuevo será el
    de F7.
27. 🟡 **Una pasada entera contra el nuevo dio rojo en `TestP7_Catalog/index_cache`**, y no es de `catalogo`: es la
    intermitencia del compositor del *flush* (el job de la ráfaga quedó `failed`, `el job no trae literal que
    analizar`), en el código viejo de captación. Repetido `-count=4` contra el nuevo, 4 de 4, y dos pasadas enteras
    verdes. Detalle y cuenta del día, en el hallazgo 30 del [README de F4](../F4-inferencia/README.md).

**Coste (D-R-6)**: F5 entera en tres sesiones compartidas con F4 (F45-01 el inventario, F45-02 el código, F45-03 el
cierre). En esta, ningún commit de código de `catalogo`. D-24…D-29 de `documentations/deuda.md`, sin tocar.

## Lo que heredan F7 y F8

- **F7 (captación) cablea el índice nuevo.** En el arranque:
  `indice.NewCache(indice.NewFuenteContenido(<content store>, ""), textmatch.Normalize, 0)`, y sus `stages` y
  `pipeline` nombran `*indice.Indice` (no el tipo de `internal/intake/catalogo`). Al conmutarlo deja de importarse
  `internal/intake/catalogo` desde `internal/arranque/fase5_captacion.go` y **ahí entra `catalogo` en `Conmutados`**
  ([`reglas.md`](reglas.md) §4.10; D-R-4). Es el primer momento en que un proceso de F9 ejercita código de F5: P7
  (`index_cache`) y P4 (el `match`) son su gate. Antes de portar `stages/p2.go`, la carrera del hallazgo 27.
- **F8 (conversación) reescribe `loadCatalog` en el carrito nuevo** (no se portó, hallazgo 8) y **muda I14–I17** (las
  rutas de importación de catálogo: hasta entonces las sirve `publicapi` con el `catalogimport` viejo, y el mapa no
  tiene filas de fase F5). **D-F5-1 = B**: `conversacion/model` se reconstruyó en F5, así que **no hay puente de import
  que retirar**. 🟡 Cuando el carrito nuevo añada `"catalogo"` a `Capas["conversacion"]`, la regla 1 de
  `fronteras_test.go` dejará de cubrir la arista prohibida `conversacion → catalogo/indice` y hará falta una
  prohibición por subpaquete (hallazgo 20, D-F5-2).
- **Los dos**: D-26…D-29 están fijadas por test en el código nuevo y no se corrigen antes de F10.
