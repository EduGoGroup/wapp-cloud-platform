# F5 · Reglas de la fase

> Comunes: [`../00-marco/tecnologia.md`](../00-marco/tecnologia.md) y
> [`../00-marco/flujo-web-local.md`](../00-marco/flujo-web-local.md). Aquí solo lo de F5.

## 1 · Lo que no se toca

- `internal/flujos/modules/cart/**` (incluidos `catalog.go`, `note.go`, `revalidate.go` y
  `testdata/`), `internal/intake/catalogo`, `internal/catalogimport`, `internal/flujos/model` y sus
  tests (E-1). **No se ponen alias** en el carrito viejo (README, contradicción 2).
- `internal/arranque`: F5 no lo cambia salvo `FaseActual` (TX.15).
- `internal/publicapi` y las rutas I14–I17: se mudan en F8.

## 2 · Trampas conocidas

| Id | Trampa | Dónde | Cómo se evita |
|---|---|---|---|
| T-1 | **El golden serializa los nombres Go**: añadir, renombrar o reordenar un campo de `Catalog`/`Category`/`Article`, o quitar un `omitempty`, rompe la no-regresión v1 | `catalog.go:58-61`, `golden_test.go` | Copiar los `testdata/` y comparar byte a byte |
| T-2 | `loadCatalog` **no** es del catálogo (usa `catalogVarKey`) | `catalog.go:574-582`, `state.go:101` | No portarlo; lo reescribe F8 en el carrito |
| T-3 | Decodificar los campos v2 como tipados hace fallar el blob entero por una etiqueta mal escrita | `catalog.go:172-178` | `json.RawMessage` y decodificación uno a uno |
| T-4 | El renombre `catalogo → indice` tienta a cambiar los textos `"catalogo: …"` | `04` §5.1 | Aserción literal en los tests (R5.6.b) |
| T-5 | Un segundo normalizador (distinto del de `match`) haría que «Café» no case «cafe» **sin un error** | `fase5_captacion.go:238-243` | `VerificarNormalizador` al construir; el test usa `textmatch.Normalize` real |
| T-6 | Retener el candado durante la lectura bloquea a todos los tenants por el SELECT de uno | `cache.go:198-200` | Contrato: lectura fuera, indexado dentro; test con `-race` |
| T-7 | Una huella barata (longitud, CRC) daría por iguales dos catálogos que difieren en un precio | `cache.go:276-287` | SHA-256 de los bytes leídos |
| T-8 | Copiar `SystemSKUPrefix` en `catalogimport` en vez de importarlo | test viejo `TestValidate_PrefijoReservado_SaleDeLaConstanteDelCart` | Importar `catalogo.SystemSKUPrefix` |
| T-9 | El prompt del import es una **hipótesis** no probada contra un LLM (D-20) y no se va a probar (cero gasto) | `catalogimport/prompt.go:41` | Portar literal; no «mejorarlo» |
| T-10 | Test de rendimiento con reloj real en una VM compartida | `rendimiento_test.go:22,31` | D-F5-3; sin `t.Skip` |
| T-11 | Querer conmutar el índice en F5 | `pipeline.go:137,158` pide el tipo viejo | No hay conmutación hasta F7 (arquitectura §5) |
| T-12 | Con D-F5-1 = A, un test nuevo que importe `internal/flujos/model` es el **mismo** puente (import) | `05` §4.1 | Declararlo una vez en `fronteras_test.go` y listarlo para su retirada en F8 |

## 3 · Prohibiciones

- 🚫 `t.Skip`. 🚫 Alias de tipo en código viejo. 🚫 Cambiar la firma de `ParseCatalog` (rompería
  `errors.Is(…, model.ErrInvalidFlow)` aguas arriba, `04` §5.2).
- 🚫 Instanciar `indice.Cache` en el arranque nuevo en F5 (habría dos cachés con estado; la nueva
  sin consumidor).
- 🚫 Añadir reglas nuevas al validador o al parser «de paso»: cada regla es la del viejo o se
  justifica en el commit (E-8).

## 4 · Definición de hecho

1. `GOWORK=off make ci-local` → `GATE_RC=0`, leído del log.
2. `GOWORK=off go vet -tags pendiente ./...` → `rc=0`.
3. `grep -rn 'pendiente.Implementar\|go:build pendiente\|t.Skip' internal/modulos/catalogo internal/modulos/conversacion/model` → vacío.
   ✎ 2026-10-07 (F45-02): ese `grep` casa **también los comentarios**. Un comentario del test de rendimiento que
   nombraba `t.Skip` lo puso rojo y costó un commit solo para reescribirlo (`0977194`). La regla no cambia; en la
   práctica: no nombres ninguno de los tres textos **ni en comentarios** del árbol nuevo (di «el salto», «la etiqueta
   de pendientes»), o afina el `grep` para que descarte las líneas de comentario antes de leerlo.
4. Un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9. Sin umbral de cobertura (P2):
   `make cobertura-ficheros` es informe (la tabla de los 11 ficheros va al PR; no bloquea).
5. `GOWORK=off go test -v ./internal/modulos/catalogo/... 2>&1 | grep -c -- '--- SKIP'` → `0`.
6. `fronteras_test.go` con la arista prohibida y la mutación documentada.
7. Huella idéntica, `FaseActual = 5` (TX.15).
8. `ESTADO.md`, este README y `tareas.md` con los SHA.
9. **T9.26** (sesión F45-03): F5 no tiene SQL propio ni adaptador Postgres, así que no hay suite de contrato que
   correr contra Postgres; se corre la suite entera de procesos de F9 contra el binario nuevo, con 0 SKIP. El proceso
   «Catálogo» (`05` §7.4: importación estricta y tabular → caché → match) no ejercita todavía el código nuevo: eso
   llega cuando F7 y F8 cableen índice y rutas.
10. **`Conmutados`** (`internal/modulos/fronteras_test.go`): `catalogo` **no** entra al cerrar F5 (decisión de Jhoan,
    2026-10-07, F45-02; corrige lo que decía este punto y D-R-4 para `catalogo`). F5 no crea ni retira adaptadores
    `bridge_<x>.go`, pero el arranque nuevo sigue cableando el índice **viejo** (`internal/arranque/fase5_captacion.go`
    importa `internal/intake/catalogo`; T-11 prohíbe conmutarlo en F5), y con `catalogo` dentro la regla 3 daría rojo.
    Entra cuando **F7** conmute el índice. `FaseActual = 5` se fija en T5.19 (`13e869f`).

## 5 · Ceremonia y tests (`05` E-12, E-4)

- **Niveles** (los fija el inventario E-12, T5.1; provisional en [`diseno.md`](diseno.md) §6):
  simple = contrato, test y lógica en **una pasada**, varios archivos por sesión;
  medio = rojo y verde por archivo, **agrupados por paquete**, un test por promesa;
  complejo = esquema completo E-2…E-9, con **mutantes** donde haga falta (aquí, `indice/cache.go`).
- **No se relaja en ningún nivel**: equivalencia viejo ↔ nuevo, `make ci-local` rc=0 con **0 SKIP**, procesos de F9.
- **Auxiliares no exportados** (`warnBag`, `parseCategory`, `huella`…): nacen en el verde; su test, también en el verde
  y solo si llevan regla de negocio o ramas no triviales. No se testea fontanería ni `if err != nil`; el resto lo cubre F9.
- **Sin adaptadores y sin BD**: no hay `bridge_<x>.go` ni test de cableado, y no hay suite con `Montaje` contra Postgres.
- **Corpus de equivalencia adversario** (hallazgo 40): los corpus viejo ↔ nuevo (diferencial del índice, goldens,
  fixtures de plantilla y prompt, planilla) llevan casos adversarios, no solo felices: separadores repetidos (`a@@b`;
  aquí `;;` y `||` en la planilla), dígitos no ASCII (en precios y `qty`) y espacios Unicode (en SKU, etiquetas y tags).
  Lo que el viejo haga con ellos es lo que el nuevo debe hacer; no se «arregla» (§3).
