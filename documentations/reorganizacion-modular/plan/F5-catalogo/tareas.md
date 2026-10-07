# F5 · Tareas

> Formato: [`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Gates con la
> misma definición que en [`../F4-inferencia/tareas.md`](../F4-inferencia/tareas.md) (cabecera):
> «gate ci-local», «gate rojo» (sobre `./internal/modulos/catalogo/...` y
> `./internal/modulos/conversacion/model/...`), «gate verde». Skills: `reconstruir-modulo`, `contrato-tdd`.
>
> **Nivel de ceremonia** (`05` E-12): el del inventario aprobado en T5.1. En nivel **simple**, la tarea de rojo y la de
> verde del mismo archivo se hacen en una pasada y se marcan las dos; en **medio**, rojo y verde por archivo agrupados
> por paquete; en **complejo**, el esquema completo con mutantes. Sin umbral de cobertura: un test por promesa del
> contrato; mutantes en el nivel complejo; procesos de F9. `make cobertura-ficheros` es informe (la tabla va al PR).
> **Auxiliares no exportados** (`05` E-4): su test nace en el verde y solo si llevan regla de negocio o ramas no triviales.
> **Adaptadores `bridge_<x>.go`: ninguno. BD: ninguna.** El «puente» de D-F5-1 = A es de **import** (`05` §4.1).
> **Sesiones**: F4 y F5 comparten las tres fichas `F45-*` de [`../sesiones/`](../sesiones/README.md).
>
> ✅ **D-F5-1 = B** (Jhoan, 2026-09-30; anotada aquí el 2026-10-06 en F45-01): `internal/flujos/model` se reconstruye en
> F5 como `internal/modulos/conversacion/model`. No hay puente de import; T5.2 **se hace**. Inventario E-12 aprobado:
> [`diseno.md`](diseno.md) §6.1.

## Sesión F45-01 · inventario E-12 (junto al de F4) · 🌐 · T5.1
Para cuando: Jhoan aprobó la tabla de niveles de F5 y D-F5-1 está anotada aquí.

- [ ] **T5.1 · docs: Inventario E-12 de F5** · 🌐 · dep. F3 cerrado (se hace con T4.1) · cumple —
  - **Produce**: (1) los números reconfirmados: `wc -l` de los 10 (+1) ficheros suma 3.789 (+406); `grep -c '^func Test'` de los 12 tests viejos suma 78 (+4); `GOWORK=off go list -f '{{.Imports}}' ./internal/flujos/model` sin imports internos; `grep -n 'catalogVarKey' internal/flujos/modules/cart/state.go` sigue en `:101`. (2) la tabla `archivo · estado en memoria · concurrencia · BD/transacciones · nº de consumidores · nivel (simple/medio/complejo)` de los 10 (+1) ficheros. (3) la lista de adaptadores `bridge_<x>.go`: **ninguno nace, ninguno se retira**; y «sin BD». (4) la decisión D-F5-1 (A o B) escrita en la cabecera de este fichero con fecha
  - **Hecho cuando**: **Jhoan aprueba la tabla. Antes de eso no se escribe código.** Si un archivo sale peor, sube de nivel
  - **Commit**: `docs(reorganizacion-modular): F5, inventario E-12 y D-F5-1`

## Sesión F45-02 · F5 entero y conmutación nominal · 🌐 · T5.2–T5.19 (+ TX.15)
Para cuando: todo sin etiqueta `pendiente`; huella idéntica con `FaseActual = 5`; gate ci-local rc=0 con 0 SKIP. (La misma sesión conmuta antes F4.)

### Rojo de `model`, `catalog.go` e `indice` · T5.2–T5.6
Para cuando: gate rojo rc=0; gate ci-local rc=0; `make lint` sin `unused`.

- [ ] **T5.2 · rojo(conversacion): contrato de `model/model.go`** · 🌐 · dep. T5.1 (**solo si D-F5-1 = B**) · cumple R5.1.a–c
  - **Ficheros**: `internal/modulos/conversacion/model/model.go`, `model_test.go`
  - **Hecho cuando**: los 24 exportados de `diseno.md` §2 con su promesa; el test cubre `NodeTerminal` imprimible, las reglas de `Validate`/`ParseAndValidate` de los 4 tests viejos, `ErrInvalidFlow` literal y la ida y vuelta de `Marshal/UnmarshalDefinition`
  - **Gate**: gate rojo · **Commit**: `rojo(conversacion): contrato de model (adelantado a F5, D-F5-1)`
  - *Si D-F5-1 = A*: esta tarea se tacha y T5.6 declara el puente de import `catalogo → internal/flujos/model` (muere en F8)
- [ ] **T5.3 · rojo(catalogo): contrato de `catalog.go` y sus goldens** · 🌐 · dep. T5.2 · cumple R5.2.a–f
  - **Ficheros**: `internal/modulos/catalogo/catalog.go`, `catalog_test.go`, `testdata/` (los 4 JSON copiados de `internal/flujos/modules/cart/testdata/`)
  - **Hecho cuando**: tipos con **mismos nombres, orden y etiquetas** que `V` (T-1); el test cubre golden v1 y v2 byte a byte, v1 sin avisos, los errores sobre `ErrInvalidFlow`, las 10 reglas de `diseno.md` §3 con sus textos, el tope de 50; **sin** `loadCatalog`
  - **Gate**: gate rojo · **Commit**: `rojo(catalogo): contrato de catalog`
- [ ] **T5.4 · rojo(catalogo): contrato de `indice/normalizador.go` e `indice/indice.go`** · 🌐 · dep. T5.3 · cumple R5.4.e–h, R5.6.b
  - **Ficheros**: `…/catalogo/indice/{normalizador,indice}.go` y tests
  - **Hecho cuando**: paquete `indice`; textos `catalogo: …` exactos; `indice_test.go` con el diferencial contra búsqueda lineal (corpus de `V/intake/catalogo/indice_test.go`, ampliado con casos adversarios, `reglas.md` §5), variante con su precio, SKU repetido gana el primero, SKU sin normalizar, cota 2.000/2.001 sobre todas las categorías, normalizador obligatorio, `*Indice` no es `Fuente`, y el P99 ≤ 5 ms (D-F5-3)
  - **Gate**: gate rojo · **Commit**: `rojo(catalogo): contrato del índice`
- [ ] **T5.5 · rojo(catalogo): contrato de `indice/cache.go`** · 🌐 · dep. T5.4 · cumple R5.4.a–d, R5.4.g
  - **Ficheros**: `…/indice/cache.go`, `cache_test.go` (con las fuentes falsas de `V/…/dobles_test.go`, no exportadas)
  - **Hecho cuando**: 1 lectura y ≤ 1 construcción por job; 0 con el mismo contenido; huella sobre lo leído; import y `PUT` invalidan igual; aislamiento por tenant; desalojo LRU a 64; documento roto no deja índice; adaptador sobre `LectorContenido` con ref por defecto `catalogo`; `-race` con dos `Obtener` concurrentes del mismo tenant. Nivel complejo: mutantes en el verde (T5.13)
  - **Gate**: gate rojo · **Commit**: `rojo(catalogo): contrato de la caché del índice`
- [ ] **T5.6 · rojo: fronteras de `catalogo`** · 🌐 · dep. T5.5 · cumple R5.5.a, R5.5.b
  - **Ficheros**: `internal/modulos/fronteras_test.go` (tabla: permitido `catalogo → conversacion/model`; **prohibido** `conversacion/** → catalogo/indice`, producción **y** tests)
  - **Hecho cuando**: mutación documentada en el commit (un test temporal en `internal/modulos/conversacion/model/` que importe `…/catalogo/indice` ⇒ rojo; revertido)
  - **Gate**: gate ci-local · **Commit**: `rojo(catalogo): fronteras del índice`

### Rojo de `catalogimport` · T5.7–T5.8
Para cuando: `make test-pendiente` cuenta **≈37** en `catalogo` + `conversacion/model` (cifra exacta anotada en T5.8); gate ci-local rc=0.

- [ ] **T5.7 · rojo(catalogo): `catalogimport/contract.go` y `validator.go`** · 🌐 · dep. T5.3 · cumple R5.3.a, R5.3.b, R5.3.d–f
  - **Ficheros**: `…/catalogo/catalogimport/{contract,validator}.go` y tests
  - **Hecho cuando**: etiquetas JSON exactas; `contract_test.go` con `DefaultLimits`, ≤ 0 ⇒ default y `DefaultMaxItems` = default de `platform/config` (importa `internal/platform/config`); `validator_test.go` con los 13 casos viejos (acumula, mensajes legibles **literales**, cabecera desconocida, archivo inservible, reglas de campo, categorías, tope de artículos y de defectos, variants+components, SKU repetido, prefijo desde `catalogo.SystemSKUPrefix`, «lo que valida lo parsea el runtime sin avisos») y los de `ReadLimited` (justo en el techo pasa; un byte más no; techo propio)
  - **Gate**: gate rojo · **Commit**: `rojo(catalogo): contrato del importador estricto`
- [ ] **T5.8 · rojo(catalogo): `diff.go`, `template.go`, `prompt.go`, `tabular.go`** · 🌐 · dep. T5.7 · cumple R5.3.c, R5.3.g
  - **Ficheros**: los cuatro y sus tests
  - **Hecho cuando**: casos de `diff_test.go` (5), `template_test.go` (8), `tabular_test.go` (16) y el prompt dicta el contrato; plantilla y prompt comparados **byte a byte** con la salida del paquete viejo capturada **como fixture** en `testdata/` (no importando el paquete viejo: T-12)
  - **Gate**: gate rojo + gate ci-local · **Commit**: `rojo(catalogo): contrato de diff, plantilla, prompt y planilla`

### Verde · T5.9–T5.18
Para cuando: los 11 ficheros sin etiqueta, un test por promesa de su contrato; gate ci-local rc=0.

Un commit por fichero (por paquete en nivel medio): `verde(<módulo>): <fichero>`; se quita `//go:build pendiente` de su test; el
comentario-ADR viaja (E-10); cabecera `// Porta <ruta vieja> @ <sha>`.

- [ ] **T5.9 · verde: `conversacion/model/model.go`** · dep. T5.2 (si B)
- [ ] **T5.10 · verde: `catalogo/catalog.go`** · dep. T5.3, T5.9
- [ ] **T5.11 · verde: `indice/normalizador.go`** · dep. T5.4
- [ ] **T5.12 · verde: `indice/indice.go`** · dep. T5.10, T5.11
- [ ] **T5.13 · verde: `indice/cache.go`** · dep. T5.12, T5.5 — con mutantes (huella, desalojo, indexado dentro del candado)
- [ ] **T5.14 · el test de rendimiento en la VM web (D-F5-3)** · dep. T5.12 — correrlo 5 veces (`-count=5`) y anotar el P99 medido en el PR (y en el traspaso, si lo hay); si falla por la VM, parada y decisión, **nunca** `t.Skip`
- [ ] **T5.15 · verde: `catalogimport/contract.go`** · dep. T5.7, T5.10
- [ ] **T5.16 · verde: `catalogimport/validator.go`** · dep. T5.15
- [ ] **T5.17 · verde: `catalogimport/{diff,template,prompt}.go`** (tres commits) · dep. T5.16
- [ ] **T5.18 · verde: `catalogimport/tabular.go`** · dep. T5.17
  - **Gate de todas**: gate verde · **Commit**: `verde(catalogo): <fichero>` (o `verde(conversacion): model`)

### Conmutación nominal · T5.19

- [ ] **T5.19 · conmutar(catalogo): sin cableado, `FaseActual = 5` (TX.15 de FX)** · 🌐 · dep. T5.18 · cumple R5.6.a
  - **Hecho cuando**: [`../FX-cara-http/tareas.md`](../FX-cara-http/tareas.md) TX.15 cerrada (el candado del estrangulador verde **sin** mudar I14–I17); `git diff --stat` del commit no toca `internal/arranque` salvo `FaseActual`; huella idéntica
  - **Gate**: gate ci-local + huella · **Commit**: `conmutar(catalogo): ninguna ruta ni cableado; FaseActual 5`

## Sesión F45-03 · cierre local · 💻 · T5.20–T5.21 (+ T9.26)
Para cuando: la definición de hecho de [`reglas.md`](reglas.md) §4. (La misma sesión cierra F4.)

- [ ] **T5.20 · validar-antes-de-cerrar** · 💻 · dep. T5.19 — los 10 puntos de `reglas.md` §4 con números; incluye T9.26 (la suite entera de procesos de F9 contra el binario nuevo, 0 SKIP)
- [ ] **T5.21 · docs: `ESTADO.md`, README y SHA** · 💻 · dep. T5.20 · **Commit**: `docs(reorganizacion-modular): F5 cerrada` — incluir para F7 y F8 la lista de lo que heredan: F7 cablea `indice.NewCache(indice.NewFuenteContenido(<content store>, ""), textmatch.Normalize, 0)` y sus `stages`/`pipeline` nombran `*indice.Indice`; F8 reescribe `loadCatalog` en el carrito nuevo y muda I14–I17; si D-F5-1 = A, F8 retira el puente de import
