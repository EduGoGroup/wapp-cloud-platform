# F7 · Reglas — lo que no se toca, las trampas y la definición de hecho

## 1 · Lo que no se toca

- El código viejo: `internal/intake/**` (incluido `intake/catalogo`, que es de F5), `internal/intakeahead`,
  `internal/evidence`, `internal/reanalisis`, `internal/casebank`, `internal/intentcfg`,
  `internal/flujos/**`, `internal/publicapi/**`, `internal/bootstrap/**`.
- **`cmd/casebank` y `cmd/server`** (D-F7-2): cambian en F10.
- La lista `fases`, su orden y los `requiere()` del arranque.
- Migraciones y la tabla `intake_jobs` (el SQL se copia literal).

## 2 · Trampas conocidas

| # | Trampa | `fichero:línea` | Qué hacer |
|---|---|---|---|
| T-1 | 🔴 `intake` (cola, F7) ≠ `intakes` (solicitud, F6) | `internal/intake/store.go` («NO CONFUNDIR») | Nunca fundirlos; alias explícitos en el arranque |
| T-2 | El agregador y el compositor **no** son de F7 (viven en `flujos/runtime`) | `fase7_flujos.go:208` · `fase5_captacion.go:62` (el compositor; su porqué en `:53-61`) | No reconstruirlos aquí; coserlos por `puente_captacion.go` |
| T-3 | Dos `WindowKey` distintos (viejo y nuevo) con los mismos campos: el compilador **no** los mezcla | `internal/intake/store.go:60-65` | Conversión explícita `intakeviejo.WindowKey(k)` **solo** en `internal/arranque/puente_captacion.go`; el test del puente afirma ida y vuelta |
| T-4 | Duplicar la goroutine del worker **no da error** | `fase9_fondo.go:99` (I-CP-4) | El candado de cableado; nunca un segundo `Run` |
| T-5 | Dos compositores = dos `source_text` que divergen | `fase5_captacion.go:53-62` (el comentario «ES EL MISMO OBJETO que consume `/reanalyze`», `:60-61`) | El re-análisis usa el compositor viejo por adaptador, no uno nuevo |
| T-6 | Los candados de cableado copiados en F0 leen **texto** (`"pipeline.NewWorker"`…) | `pipeline_captacion_cableado_test.go:85-164` | Nombre corto para el paquete nuevo, alias para el viejo |
| T-7 | La guarda anti-hueco del candado INV-1 falla con un directorio inexistente | F6 `diseno.md` §6 | Re-tocar la lista en el mismo commit de la conmutación (T7.25) |
| T-8 | `casebank` es invisible para la huella y el arranque | `go list` (solo `cmd/casebank`) | Su verdad: suite + doble (y F9) |
| T-9 | `intentcfg` sí tiene gemelo (contra `05` E-6) | `intentcfg/store.go:47` | No crear un segundo doble en `intentcfgtest`: la suite la corre el `MemoryStore` |
| T-10 | Los tests viejos de `stages` y `pipeline` son **guiones** (ámbar, hamburguesas, T40, Ola 3), no por fichero | `V/pipeline/guion_*_test.go`, `V/stages/ambar_*_test.go` | Consultarlos (E-8); el comportamiento de extremo a extremo va al proceso P4 de F9, no a un test de fichero |
| T-11 | `match_lineas.go` no tiene exportados | medido (0) | No inventar exportados para «cumplir» el candado; su test llega con el verde |
| T-12 | Los fallos de `intakeAhead.Run` y del worker son mudos (D-11) | `fase9_fondo.go:80,95` | No añadir supervisión de paso: es un frente con dueño |
| T-13 | El plazo de G7 (F6) se deriva de `PlazoPorLlamadaSuelo`; al conmutar F7 cambia de **dónde** se lee | FX §4.3, TX.21 | Aserción de igualdad con el valor que recibe `quotetext.ConPlazo` |

## 3 · Prohibiciones

- Importar desde `C/**` un paquete viejo fuera de los puentes declarados (`stages → flujos/store`,
  `reanalisis → flujos/events`, y `reanalisis → flujos/runtime` si no se aplica la alternativa de la
  constante inyectada).
- Una segunda instancia de `Pool`, worker o aforo; un segundo compositor.
- Cablear la zona gris del match, un interruptor de configuración del pipeline, o el `Repartir` de
  `anclaje` «ya que estamos».
- `t.Skip`, Postgres vivo, `rojo`+`verde` en un commit, squash.

## 4 · Definición de hecho (F7)

1. 32 ficheros (o 31 + `pipelinetest/memoria.go` según D-F7-3) con `x_test.go`; suites `intaketest`,
   `casebanktest` (con doble), `intentcfgtest`.
2. Pendientes en `internal/modulos/captacion` = 0; SKIP = 0 (gate con `-v`, sin pipe).
3. Cobertura ≥ 80 % por fichero (fuera `postgres.go`, `machine_postgres.go`, `store_postgres.go`).
4. `make ci-local` rc=0; `vet -tags pendiente` rc=0; `fronteras_test.go` con 2–3 puentes de F7 + 1 de F6.
5. Huella igual; `go list -deps ./cmd/server-modular | grep -c modulos/captacion` > 0; `cmd/server` sin
   `modulos/captacion`; `captacion_cableado_test.go` y `puente_captacion_test.go` verdes.
6. H1, E1, E2 por `apipublica`; `FaseActual = 7`; candado de mudanzas verde; INV-1 con la lista de F7.
7. Traspaso `TRASPASO-F7-captacion.md` con `CERRADO` (P4, P8 y, si D-F9-1, T9.28); `ESTADO.md` al día.
