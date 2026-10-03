# F5 · `catalogo` — el modelo del catálogo, su índice de búsqueda y el importador

> **Estado**: spec escrita el 2026-09-28 sobre `dev` @ `1b18932` (código) / `bad573a` (plan). Sin
> código. Marco: [`../00-marco/`](../00-marco/README.md). Norma: [`05`](../../05-metodo-contratos-y-tdd.md).
> Rutas: [`../FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) — **F5 no muda
> ninguna** (TX.15).
>
> Recalibrado el 2026-10-03 tras la parada de F1 (`05` E-12, §4.2, E-9, E-4; `plan/DECISIONES.md` §3).

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
| [`F45-01`](../sesiones/F45-01-web-inventario-e-inferencia.md) · inventario E-12 (de F4 y de F5) + F4 entero en verde | 🌐 | T5.1 | Jhoan aprobó la tabla de niveles de F5 y D-F5-1 está anotada |
| [`F45-02`](../sesiones/F45-02-web-conmutar-inferencia-y-catalogo.md) · (conmutar F4 y) F5 entero + conmutación nominal | 🌐 | T5.2–T5.19 (+ TX.15) | todo sin etiqueta `pendiente`; huella idéntica con `FaseActual = 5`; `ci-local` rc=0 con 0 SKIP |
| [`F45-03`](../sesiones/F45-03-cli-cierre.md) · cierre local de las dos fases | 💻 | T5.20–T5.21 (+ T9.26) | definición de hecho de [`reglas.md`](reglas.md) §4; procesos de F9 contra el binario nuevo |

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
