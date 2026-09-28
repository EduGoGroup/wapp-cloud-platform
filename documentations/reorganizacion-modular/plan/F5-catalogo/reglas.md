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
| T-12 | Con D-F5-1 = A, un test nuevo que importe `internal/flujos/model` es el **mismo** puente | `05` §4.1 | Declararlo una vez en `fronteras_test.go` y listarlo para su retirada en F8 |

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
4. `make cobertura-ficheros` → rc=0 (≥ 80 % en los 11 ficheros).
5. `GOWORK=off go test -v ./internal/modulos/catalogo/... 2>&1 | grep -c -- '--- SKIP'` → `0`.
6. `fronteras_test.go` con la arista prohibida y la mutación documentada.
7. Huella idéntica, `FaseActual = 5` (TX.15).
8. `ESTADO.md`, este README y `tareas.md` con los SHA.
9. **No aplica** el pre-chequeo de F9: F5 no tiene adaptador Postgres; el proceso «Catálogo»
   (`05` §7.4: importación estricta y tabular → caché → match) solo puede correr contra el binario
   nuevo cuando F7 y F8 hayan cableado índice y rutas.
