# F7 · Reglas — lo que no se toca, las trampas y la definición de hecho

## 0 · Nivel de ceremonia (`05` E-12)

El nivel de cada archivo lo fija el **inventario E-12** (T7.1), aprobado por Jhoan; la clasificación provisional está en
[`diseno.md`](diseno.md) §1.1. Si un archivo sale peor de lo previsto, sube de nivel.

| Nivel | Qué se hace |
|---|---|
| **Simple** | Contrato, test y lógica en **una pasada**; varios archivos por sesión |
| **Medio** | Rojo y verde por archivo, **agrupados por paquete**; un test por promesa del contrato |
| **Complejo** | Esquema completo E-2…E-9, con **mutantes** donde haga falta |

No se relaja en ningún nivel: equivalencia viejo ↔ nuevo, `make ci-local` rc=0 con **0 SKIP**, procesos de F9.

- **Sin umbral de cobertura (P2)**: un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9.
  `make cobertura-ficheros` es un informe: la tabla va al PR; no bloquea.
- **Auxiliares no exportados (P6, E-4)**: su test nace en el **verde** y solo si llevan regla de negocio o ramas no
  triviales; no se testea fontanería ni `if err != nil`; el resto lo cubre F9. Aplica a `match_lineas.go` y `saneo.go`.
- **Puertos con BD (P4)**: `intake.JobStore`, `intake.PipelineStore`, `casebank.Store` e `intentcfg.Store` tienen su suite
  `Contrato…(t, func(t) Montaje)` corrida **en memoria y en Postgres** con el arnés de F9-A: es lo que garantiza que
  memoria y Postgres se comportan igual. La marca de estado de la suite vigila **todas** las columnas que la operación
  puede tocar, no una sola (hallazgo 35).
- **Corpus de equivalencia adversario (hallazgo 40)**: los corpus viejo ↔ nuevo (`evidence.Normalize`, `anclaje`,
  `anonimizar`, `fechas`, el match) llevan casos adversarios —separadores repetidos como `a@@b`, dígitos no ASCII,
  espacios Unicode—, no solo casos felices.
- **Adaptador de arranque (`05` §4.2)**: `bridge_captacion.go` es nivel **simple** y lleva **test de cableado
  obligatorio y completo** (hallazgo 39): que el arranque construye lo **nuevo** *y* que ninguna fase importa lo viejo
  fuera del adaptador (grep por ruta de import). `un_fichero_un_test` y el informe de cobertura incluyen los `bridge_*.go`.
  «Puente» queda para el **import** nuevo → viejo de `05` §4.1.

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
| T-2 | El agregador y el compositor **no** son de F7 (viven en `flujos/runtime`) | `fase7_flujos.go:208` · `fase5_captacion.go:62` (el compositor; su porqué en `:53-61`) | No reconstruirlos aquí; coserlos por el adaptador `bridge_captacion.go` |
| T-3 | Dos `WindowKey` distintos (viejo y nuevo) con los mismos campos: el compilador **no** los mezcla | `internal/intake/store.go:60-65` | Conversión explícita `intakeviejo.WindowKey(k)` **solo** en `internal/arranque/bridge_captacion.go`; el test del adaptador afirma ida y vuelta |
| T-4 | Duplicar la goroutine del worker **no da error** | `fase9_fondo.go:99` (I-CP-4) | El candado de cableado; nunca un segundo `Run` |
| T-5 | Dos compositores = dos `source_text` que divergen | `fase5_captacion.go:53-62` (el comentario «ES EL MISMO OBJETO que consume `/reanalyze`», `:60-61`) | El re-análisis usa el compositor viejo por adaptador, no uno nuevo |
| T-6 | Los candados de cableado copiados en F0 leen **texto** (`"pipeline.NewWorker"`…) | `pipeline_captacion_cableado_test.go:85-164` | Nombre corto para el paquete nuevo, alias para el viejo |
| T-7 | La guarda anti-hueco del candado INV-1 falla con un directorio inexistente | F6 `diseno.md` §6 | Re-tocar la lista en el mismo commit de la conmutación (T7.25) |
| T-8 | `casebank` es invisible para la huella y el arranque | `go list` (solo `cmd/casebank`) | Su verdad: suite + doble (y F9) |
| T-9 | `intentcfg` sí tiene gemelo (contra `05` E-6) | `intentcfg/store.go:47` | No crear un segundo doble en `intentcfghelpertest`: la suite la corre el `MemoryStore` |
| T-10 | Los tests viejos de `stages` y `pipeline` son **guiones** (ámbar, hamburguesas, T40, Ola 3), no por fichero | `V/pipeline/guion_*_test.go`, `V/stages/ambar_*_test.go` | Consultarlos (E-8); el comportamiento de extremo a extremo va al proceso P4 de F9, no a un test de fichero |
| T-11 | `match_lineas.go` no tiene exportados | medido (0) | No inventar exportados para «cumplir» el candado; su test llega con el verde y cubre sus reglas, no su fontanería (E-4) |
| T-12 | Los fallos de `intakeAhead.Run` y del worker son mudos (D-11) | `fase9_fondo.go:80,95` | No añadir supervisión de paso: es un frente con dueño |
| T-13 | El plazo de G7 (F6) se deriva de `PlazoPorLlamadaSuelo`; al conmutar F7 cambia de **dónde** se lee | FX §4.3, TX.21 | Aserción de igualdad con el valor que recibe `quotetext.ConPlazo` |

> ✎ **D-F9-10 (Jhoan, 2026-10-08), para la goroutine de fondo de esta fase**: al reconstruir el **pipeline** (`intake/pipeline/pipeline.go`, `Run` → `Drenar`, «pipeline: no se pudo reclamar trabajo»), su contrato promete «contexto cancelado → vuelve **sin** loguear a `ERROR`», con su caso, como el *webhook worker* de F6 (D-F6-7; `solicitudes/integrations/worker.go` y `TestRun_ContextCancelled_…` son el modelo). El test de P0 ya tolera esas líneas tras la parada; esto es para que el log de producción no las lleve.

## 3 · Prohibiciones

- Importar desde `C/**` un paquete viejo fuera de los puentes (import) declarados (`stages → flujos/store`,
  `reanalisis → flujos/events`, y `reanalisis → flujos/runtime` si no se aplica la alternativa de la
  constante inyectada).
- Una segunda instancia de `Pool`, worker o aforo; un segundo compositor.
- Cablear la zona gris del match, un interruptor de configuración del pipeline, o el `Repartir` de
  `anclaje` «ya que estamos».
- Importar en una fase de `internal/arranque` un paquete viejo de captación fuera de `bridge_captacion.go`.
- Añadir `captacion` a `Conmutados` en F7 (su adaptador sigue vivo hasta F8).
- `t.Skip`, Postgres vivo, `rojo`+`verde` en un commit, squash.

## 4 · Definición de hecho (F7)

1. 32 ficheros (o 31 + `pipelinehelpertest/memoria.go` según D-F7-3) con `x_test.go`; suites `intakehelpertest`,
   `casebankhelpertest` (con doble), `intentcfghelpertest`.
2. Pendientes en `internal/modulos/captacion` = 0; SKIP = 0 (gate con `-v`, sin pipe).
3. Un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9. Las **cinco** suites de puerto con BD (✎ F7-01: `ContratoQueue`, `ContratoMachine`, `ContratoReanalysis`, `intentcfg` y `casebank`)
   verdes **en memoria y en Postgres** con el mismo `Montaje`: es la verdad de `postgres.go`, `machine_postgres.go` y
   `store_postgres.go`. La tabla de `make cobertura-ficheros` va al PR; no bloquea.
4. `make ci-local` rc=0; `vet -tags pendiente` rc=0; `fronteras_test.go` con 2–3 puentes (import) de F7 + 1 de F6.
5. Huella igual; `go list -deps ./cmd/server-modular | grep -c modulos/captacion` > 0; `cmd/server` sin
   `modulos/captacion`; `captacion_cableado_test.go` (cableado completo, `05` §4.2) y `bridge_captacion_test.go` verdes.
6. H1, E1, E2 por `apipublica`; `FaseActual = 7`; candado de mudanzas verde; INV-1 con la lista de F7.
7. **Adaptadores y `Conmutados`**: un módulo entra en `Conmutados` cuando muere su último adaptador, no al conmutar.
   **Nace** `bridge_captacion.go` (`aheadBridge`, `composerBridge`, clausura del sink, segunda instancia vieja de
   `intake.Postgres`): muere en **F8**, y con él entra `captacion` en `Conmutados`. **Muere aquí** `llmConfigBridge` de
   `bridge_inferencia.go` (F4); el resto de ese fichero (`turneroBridge`) muere en F8. `FaseActual` no cambia de regla.
8. `ESTADO.md` al día y hallazgos en el README; si hubo dos entornos, traspaso `TRASPASO-F7-captacion.md` con `CERRADO`
   (P4, P8 y, si D-F9-1, T9.28).
