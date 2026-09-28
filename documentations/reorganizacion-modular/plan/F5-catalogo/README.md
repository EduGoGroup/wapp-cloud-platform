# F5 · `catalogo` — el modelo del catálogo, su índice de búsqueda y el importador

> **Estado**: spec escrita el 2026-09-28 sobre `dev` @ `1b18932` (código) / `bad573a` (plan). Sin
> código. Marco: [`../00-marco/`](../00-marco/README.md). Norma: [`05`](../../05-metodo-contratos-y-tdd.md).
> Rutas: [`../FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) — **F5 no muda
> ninguna** (TX.15).

## Objetivo en tres líneas

Crear el paquete nuevo `internal/modulos/catalogo` con el **modelo del catálogo extraído del
carrito** (`internal/flujos/modules/cart/catalog.go`), su índice en memoria (`internal/intake/catalogo`
→ `catalogo/indice`, el **único renombre** del árbol) y el importador (`internal/catalogimport`), por
contrato → rojo → verde. **F5 no conmuta nada en el arranque**: sus consumidores (pipeline, carrito y
rutas de import) se reconstruyen en F7 y F8, y son ellos quienes lo cablearán.

## Entradas

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | F0 cerrado (`pendiente`, `make test-pendiente`, `cobertura-ficheros`, `fronteras_test.go`) | `ls internal/pendiente internal/modulos` |
| E2 | F4 cerrado (orden de `05` §6; F5 no depende de F4 en código, pero comparte gate) | `ESTADO.md` |
| E3 | 🔴 **D-F5-1 decidida** (¿`conversacion/model` se reconstruye aquí o se tiende un puente?) | [`arquitectura.md`](arquitectura.md) §3 |
| E4 | `dev` verde | skill `validar-antes-de-cerrar` |

## Salidas

| # | Condición | Verifica |
|---|---|---|
| S1 | 11 ficheros de producción (10 + `conversacion/model/model.go` si D-F5-1 = B) y 11 tests en verde, más `testdata/` con los goldens | `grep -rln 'go:build pendiente' internal/modulos/catalogo internal/modulos/conversacion/model` vacío |
| S2 | ≥ 80 % por fichero (no hay adaptador Postgres en F5) | `make cobertura-ficheros` rc=0 |
| S3 | `fronteras_test.go` prohíbe `conversacion/** → catalogo/indice` y permite `catalogo → conversacion/model` | T5.6 |
| S4 | El arranque nuevo **no** cambia; `FaseActual = 5`; huella idéntica (TX.15) | T5.19 |
| S5 | 0 `t.Skip`, 0 `pendiente.Implementar` | `grep` |

## Bloques de sesión

| Bloque | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| **A** · inventario y D-F5-1 | 🌐 | T5.1 | números reconfirmados; decisión anotada |
| **B1** · rojo de `model`, `catalog.go` e `indice` | 🌐 | T5.2–T5.6 | `vet -tags pendiente` rc=0; ci-local rc=0 |
| **B2** · rojo de `catalogimport` | 🌐 | T5.7–T5.8 | `make test-pendiente` ≈ 37 (cifra exacta anotada en T5.8) |
| **C** · verde fichero a fichero | 🌐 | T5.9–T5.18 | todo sin etiqueta, ≥ 80 % |
| **D** · «conmutar» (solo TX.15) y cierre | 🌐 | T5.19–T5.21 | definición de hecho de [`reglas.md`](reglas.md) §4 |

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
