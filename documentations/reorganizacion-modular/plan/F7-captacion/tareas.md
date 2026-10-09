# F7 · Tareas

> Formato: [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills: `reconstruir-modulo`,
> `contrato-tdd`, `validar-antes-de-cerrar`, `traspaso-web-local` (T7.26→T7.27),
> `procesos-testcontainers` (T7.27). `C` = `internal/modulos/captacion`, `V` = paquete viejo. PR a
> `dev` (`--base dev`), sin squash. Gate de tests sin pipe:
> `GOWORK=off go test -count=1 -race -v ./internal/modulos/captacion/... > "$TMPDIR/c.log" 2>&1; echo rc=$?; grep -c -- '--- SKIP' "$TMPDIR/c.log"` → `rc=0` y `0`.
> Gate de rojo: `GOWORK=off go vet -tags pendiente ./internal/modulos/captacion/...; echo rc=$?` → 0.
> Patrón F1: el rojo lleva **solo exportados**; el test de un auxiliar no exportado nace en el verde y solo si lleva
> regla de negocio o ramas no triviales (`05` E-4).
>
> **Nivel de ceremonia** (`05` E-12, [`reglas.md`](reglas.md) §0): el que apruebe Jhoan en T7.1. Las tareas `rojo(…)` y
> su par `verde(…)` valen tal cual para medio y complejo; en un archivo **simple** se hacen en una pasada.
> **Sin umbral de cobertura (P2)**: un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9.
> **Bloque = sesión** (45–90 min). Las tareas no se renumeraron: se reagruparon por paquete.
> **Nombres (E-11, `05` §4.2)**: el adaptador de arranque es `bridge_captacion.go` (antes, prefijo «puente»); `adelantoViejo` → `aheadBridge`;
> `compositorViejo` → `composerBridge`; `puenteConfigLLM` (F4) → `llmConfigBridge`.

## Bloque A · inventario E-12 y hojas · 🌐❓ · sesión F7-01 · T7.1–T7.6, T7.14–T7.15
Para cuando: inventario **aprobado por Jhoan** · `evidence`, `intake`, `anclaje`, `intentcfg`, `casebank` en verde con sus suites en memoria · `ci-local` rc=0 · PR.

- [x] **T7.1 · Inventario E-12** — `6c461c6` (aprobado por Jhoan el 2026-10-08) · 🌐❓ · dep. F6 cerrada · cumple R7.1.a
  - **Produce**: (1) la tabla `archivo · estado en memoria · concurrencia · BD/transacciones · nº de consumidores · nivel (simple/medio/complejo)` de los 32 ficheros, más los 2 de la cara HTTP y el adaptador; (2) la **lista de adaptadores**: nace `bridge_captacion.go` (muere F8); muere `llmConfigBridge` de `bridge_inferencia.go` (F4). Parte de la clasificación provisional de [`diseno.md`](diseno.md) §1.1; si un archivo sale peor, sube de nivel
  - **Además**: entradas del README comprobadas; recuento de `diseno.md` §1; resueltos los 🔶 de inventario: D-F7-3 (`memoria.go`), `Plazas` estructural, reloj de `machine_postgres.go`, nombre de la tabla de intenciones, textos observables de `V` y de `publicapi/{reanalyze,intents}.go`
  - **Hecho cuando**: **Jhoan aprueba la tabla. Antes de eso no se escribe código.**
  - **Commit**: `docs(reorganizacion-modular): F7 arranca — inventario E-12 aprobado`
- [x] **T7.2 · rojo(captacion): contrato de `evidence`** — `06c7ffc` (nivel **simple**: contrato, test y lógica en una pasada, `verde(captacion): evidence.go`) · 🌐❓ · dep. T7.1 · cumple R7.1.c · el ejemplo de `05` §10 tal cual · **Commit**: `rojo(captacion): contrato de evidence`
- [x] **T7.3 · rojo(captacion): `intake` + `intakehelpertest`** — `eb20115` (tres suites: `ContratoQueue`, `ContratoMachine` y `ContratoReanalysis`) · 🌐❓ · dep. T7.1 · cumple R7.1.b, R7.5.a
  - **Ficheros**: 6 + 6 tests, `C/intake/intakehelpertest/{queue,machine,reanalysis}_contrato.go` (✎ F7-01: antes `{cola,maquina}.go`; E-11 y la tercera suite) · **Hecho cuando**: `WindowKey` idéntico (4 `string`, mismo orden); las dos suites escritas como `Contrato…(t, func(t) Montaje)`, con la marca de estado sobre **todas** las columnas que cada operación puede tocar; `memory_test.go` las invoca · **Commit**: `rojo(captacion): contrato de intake (la cola)`
- [x] **T7.4 · rojo(captacion): `anclaje`** — `e344915` · 🌐❓ · dep. T7.2 · 🔶 19 casos de `V/anclaje_test.go` · **Commit**: `rojo(captacion): contrato de anclaje`
- [x] **T7.5 · rojo(captacion): `intentcfg` + suite, `casebank` + suite y doble** — `5ca038e` (`intentcfg`) y `e4a0e97` (`casebank`): un commit de rojo por paquete · 🌐❓ · dep. T7.1 · cumple R7.5.b–c
  - **Hecho cuando**: `casebankhelpertest.Memoria` nace completo y en verde; `intentcfg.MemoryStore` corre `intentcfghelpertest.Contrato`; las dos suites reciben `Montaje` · **Commit**: `rojo(captacion): contratos de intentcfg y casebank`
- [x] **T7.6 · Punto de control del rojo de las hojas** — hecho por paquete (cada sub-agente comprobó su rojo antes de commitearlo); al cierre `make test-pendiente` rc=0, `PENDIENTES=0 · ROJOS=0` · 🌐❓ · `ci-local` rc=0 · `make test-pendiente` anotado (sin PR: la sesión sigue)
- [x] **T7.14 · verde(captacion): `evidence`, `anclaje`, `intentcfg`, `casebank`** — `06c7ffc` · `24bae38` · `c4f3f7c`, `75d456e` · `45d4efc`, `07d1326`, `1eda2f8`, `fc2588a` (+ `1d36dfe`, `f08e959`, `d88d200`, `cb41b42`: los tres agujeros de PII del anonimizador, D-F7-8) · 🌐❓ · un commit por fichero (`verde(captacion): <fichero>`), cabecera `// Porta … @ <sha>`
- [x] **T7.15 · verde(captacion): `intake`** — `10785c4`, `8acda6d`, `62f3728`, `5fc13a1`, `8305b3b`, `aa45d2d`, `7b25bb8`, `0508f52`, `6f37362` (+ `9bbc2dd`, lint de los tests; + `1db9266`, el gemelo rechaza la clave incompleta, D-F7-8) · orden real: `store`, `machine`, `reanalysis` **antes** que los dobles, que los usan (hallazgo 4) · 🌐❓ · `memory.go` primero (las dos suites en verde con `-race`), luego `store`, `machine`, `reanalisis`, y los dos adaptadores Postgres (`postgres.go`, `machine_postgres.go`: SQL literal, funciones puras; su suite contra Postgres la corre T7.27) · cierra la sesión: `ci-local` rc=0 · PR

### Correspondencia de nombres (E-11) — F7-01

La spec nombra en español lo que el código nuevo lleva en inglés. Textos observables, SQL y `WindowKey` no cambian.

| Paquete | Viejo / spec | Nuevo |
|---|---|---|
| `intake` | `ClaimNextIgnorandoBackoff` | `ClaimNextIgnoringBackoff` |
| `intake` | `Reanalisis` (tipo y campo) · `EsDelDueño` | `Reanalysis` · `IsFromOwner` |
| `intake` | `SolicitudReanalisis` (campo `Contexto`) | `ReanalysisRequest` (`Context`) |
| `intake` | `JobNoTerminalDeEvento` · `AbrirReanalisis` | `LiveJobOfEvent` · `OpenReanalysis` |
| `intake` | `reanalisis.go` | `reanalysis.go` (tipos) + `postgres_reanalysis.go` (SQL) |
| `intakehelpertest` | `ContratoCola` · `ContratoMaquina` (spec) | `ContratoQueue` · `ContratoMachine` (+ `ContratoReanalysis`, nueva) |
| `intakehelpertest` | `pipeline.Fila` · `StoreEnMemoria` · `NuevoStoreEnMemoria` | `Row` · `MachineMemory` · `NewMachineMemory` |
| `intakehelpertest` | `Sembrar` · `Ver` · `RomperElClaim` | `Seed` · `View` · `BreakClaim` (`Claims` igual) |
| `anclaje` | `Repartir` · `Reparto{PorLinea, Solicitud}` | `Distribute` · `Distribution{ByLine, Request}` |
| `anclaje` | `Turno{Seq, Texto, En}` · `Linea{Idx, Evidencia, Etiqueta}` | `Turn{Seq, Text, At}` · `Line{Idx, Evidence, Label}` |
| `anclaje` | `Opciones{MaxMensajesAtras, Ventana}` · `MediaRef.En` | `Options{MaxMessagesBack, Window}` · `MediaRef.At` |
| `anclaje` | `EtiquetaAudio` · `MaxMensajesAtrasPorDefecto` · `VentanaPorDefecto` | `AudioLabel` · `DefaultMaxMessagesBack` · `DefaultWindow` |
| `casebank` | `anonimizar.go` · `semilla.go` | `anonymize.go` · `seed.go` |
| `casebank` | `Caso` · `Store.Insertar`/`Existe` | `Case` · `Store.Insert`/`Exists` |
| `casebank` | `Servicio` · `NewServicio` · `Insertar` · `Sembrar` | `Service` · `NewService` · `Insert` · `Seed` |
| `casebank` | `ErrSinConsentimiento` · `ErrSinTenant` · `ErrSinTexto` | `ErrNoConsent` · `ErrNoTenant` · `ErrNoText` |
| `casebank` | `Marca{JID,Telefono,Nombre}` · `Clase…` · `Hallazgo{Clase, Texto, Ini, Fin}` | `Mark{JID,Phone,Name}` · `Class…` · `Finding{Class, Text, Start, End}` |
| `casebank` | `Anonimizador` · `NuevoAnonimizador` · `Anonimizar` · `Restos` · `Nombres` | `Anonymizer` · `NewAnonymizer` · `Anonymize` · `Remains` · `Names` |
| `casebank` | `TextoCasoAmbar` · `NombresDelCaso` · `EsperadoCasoAmbar` · `CasoAmbar` | `AmbarCaseText` · `CaseNames` · `AmbarCaseExpected` · `AmbarCase` |
| `casebankhelpertest` | `Memoria` (spec) | `Memory` |

`evidence` e `intentcfg` no renombran nada (ya estaban en inglés).

## Bloque B · `stages` · 🌐❓ · sesión F7-02 · T7.7–T7.9, T7.16–T7.17
Para cuando: 10 ficheros de `stages` en verde · puente (import) 1 declarado · `ci-local` rc=0 · PR.

- [x] **T7.7 · rojo(captacion): etapas LLM** — `1a5219e` (+ `7711a55`, `deadline.go`, nivel simple en una pasada; `tope.go` no pudo ir en una pasada: su lógica es de `P3`, hallazgo 18) · 🌐❓ · dep. T7.15 · `plazo.go`, `p2.go`, `p3.go`, `tope.go`, `p4.go`, `fechas.go` + tests · R-03, R-11 en los contratos; 🔶 lectura de `p2/p3/p4/tope/fechas/audio_jamas_al_llm_test` y del AST de `p4_test.go` · **Commit**: `rojo(captacion): contratos de P2, P3 y P4`
- [x] **T7.8 · rojo(captacion): match** — `3d31361` · 🌐❓ · dep. T7.7 · `match.go`, `match_cascada.go`, `match_lineas.go` (sin exportados: su test nace con el verde, E-4) · R-04, R-05; 🔶 `match_*_test` (cascada, cota, dobles, rendimiento) · **Commit**: `rojo(captacion): contratos del match`
- [x] **T7.9 · rojo(captacion): draft y puente (import) 1** — `3d380eb` (`draft` en cuatro ficheros por tema, D-F7-6) · 🌐❓ · dep. T7.8 · cumple R7.6.a
  - **Ficheros**: `draft.go` + test; `internal/modulos/fronteras_test.go` (`captacion/stages → internal/flujos/store`, «muere F8») · R-06, R-07; 🔶 `draft_*_test` y su AST · **Commit**: `rojo(captacion): contrato de draft y su puente a flujos/store`
- [x] **T7.16 · verde(captacion): etapas LLM** — `7711a55` (`deadline`), `bd25bf5` (`dates`), `864ca23` (`p2`), `0177f3d` (`p3` y `cap`, un commit: se necesitan mutuamente), `b41a6c7` (`p4`) · 🌐❓ · `plazo`, `tope`, `fechas`, `p2`, `p3`, `p4`
- [x] **T7.17 · verde(captacion): match y draft** — `c880cd5` (`match_cascade`, con `match_cascade_sweep.go` por E-13), `705bda7` (`match` y `match_lines`, un commit), `efe219d` (los cuatro de `draft`, un commit; hallazgo 19) · 🌐❓ · `match_cascada`, `match` (+ test de `match_lineas`: sus reglas de líneas, variantes y envío, no su fontanería), `draft` · cierra la sesión: PR

### Correspondencia de nombres (E-11) — F7-02 (`stages`)

Valores, textos de error y de log, claves JSON y nombres de evento no cambian. `ProviderSelector`, `StageStore`, `P2`/`P3`/`P4`,
`Match`, `Draft`, sus `New…`, `Run`, `Kind*` y `PushRevisionByID` conservan el nombre.

| Tema | Viejo / spec | Nuevo |
|---|---|---|
| ficheros | `plazo.go` · `tope.go` · `fechas.go` | `deadline.go` · `cap.go` · `dates.go` |
| ficheros | `match_cascada.go` · `match_lineas.go` | `match_cascade.go` + `match_cascade_sweep.go` (E-13) · `match_lines.go` |
| ficheros | `draft.go` (1.038) | `draft.go` · `draft_revision.go` · `draft_events.go` · `draft_push.go` (D-F7-6) |
| plazo | `Opción` · `ConPlazoPorLlamada` | `Option` · `WithCallTimeout` |
| P2–P4 | `ErrSinCablear` · `ErrSinLiteral` · `ErrJobFueraDeProcessing` | `ErrNotWired` · `ErrNoLiteral` · `ErrJobNotProcessing` |
| P3 | `MotivoCalidad` · `MotivoEvidencia` · `MotivoTope` | `ReasonQuality` · `ReasonEvidence` · `ReasonOverLimit` |
| P3 | `ItemAislado` · `ArtefactoP3` · `MaxItemsPorPedido` | `IsolatedItem` · `P3Artifact` · `MaxItemsPerOrder` |
| P4 | `ZonaPorDefecto` · `ErrSinZonaHoraria` · `ResolverFecha` | `DefaultZone` · `ErrNoTimeZone` · `ResolveDate` |
| match | `OpciónMatch` · `ConZonaGris` · `ConComparador` | `MatchOption` · `WithGrayZone` · `WithComparator` |
| match | `MotivoSinProducto` · `MotivoCantidadInvalida` · `MotivoIndicacionLarga` | `WarningNoProduct` · `WarningInvalidQty` · `WarningNoteTooLong` |
| match | `MotivoZonaGrisCaida` · `MotivoRangoSinVariante` | `WarningGrayZoneDown` · `WarningRangeWithoutVariant` |
| match | `NotaDePedido` · `SinNotaDePedido` | `OrderNote` · `NoOrderNote` |
| match | `ErrMatchSinCablear` · `ErrSinCatalogo` · `ErrSinCantidades` | `ErrMatchNotWired` · `ErrNoCatalog` · `ErrNoQuantities` |
| match | `OpcionVariante` · `ProcedenciaMatch` · `Linea` · `Aviso` | `VariantOption` · `MatchProvenance` · `Line` · `Warning` |
| match | `ArtefactoMatch` · `TotalParcial` · `EntradaMatch{Cantidades, Indice, Zonas, Nota}` | `MatchArtifact` · `PartialTotal` · `MatchInput{Quantities, Index, Zones, Note}` |
| match | `CascadaPorDefecto` · `MargenLongitud` · `MaxCandidatosZonaGris` · `Estrategia{SKU,Exacta,Variante,Tag,NGrama}` | `DefaultCascade` · `LengthMargin` · `MaxGrayZoneCandidates` · `Strategy{SKU,Exact,Variant,Tag,NGram}` |
| draft | `OpciónDraft` · `ConReloj` · `ConEmpujeCRM` | `DraftOption` · `WithClock` · `WithCRMPush` |
| draft | `AlmacenSolicitudes` · `EscritorRevision` · `EscritorEvento` · `EmpujadorCRM(Func)` | `IntakeStore` · `RevisionWriter` · `EventWriter` · `CRMPusher(Func)` |
| draft | `ErrDraftSinCablear` · `ErrSinMatch` · `ErrJobSinEvento` | `ErrDraftNotWired` · `ErrNoMatch` · `ErrJobWithoutEvent` |
| draft | `ArtefactoDraft` · `EntradaDraft{…, FechaEntrega, Analisis}` · `Analisis` | `DraftArtifact` · `DraftInput{…, DeliveryDate, Analysis}` · `Analysis` |
| draft | `PayloadRevision` · `LineaRevision` | `RevisionPayload` · `RevisionLine` |
| draft | `EventoBorradorCreado` · `EventoReanalizado` · `FlujoCaptacion` · `VersionFlujoCaptacion` | `EventDraftCreated` · `EventReanalyzed` · `IntakeFlowID` · `IntakeFlowVersion` |
| draft | `OrigenHiloDelEvento` · `OrigenTextoPegado` · `OrigenAmbos` | `SourceEventThread` · `SourcePastedText` · `SourceBoth` |

Lo que sigue en español porque ya estaba escrito: `indice.Indice`, `Coincidencia`, `Construir`, `VerificarNormalizador` (F5).

## Bloque C · `pipeline`, `intakeahead`, `reanalisis` · 🌐❓ · sesión F7-03 · T7.10–T7.13, T7.18–T7.20
Para cuando: pendientes del módulo = 0 · puentes (import) declarados · un test por promesa del contrato, mutantes en lo complejo · `ci-local` rc=0 · PR.

- [x] **T7.10 · rojo(captacion): `pipeline`** — `403f73d` (`verde(captacion): pipelinehelpertest`: el doble `CatalogMemory` nace completo y en verde, D-F7-5) y `1ff2012` (contratos de `pipeline.go`, `pipeline_loop.go`, `slot.go`, `backoff.go`; `pipeline_chain.go` y `pipeline_outcome.go` no tienen exportados y nacen en el verde, hallazgo 27) · 🌐❓ · dep. T7.17 · cumple R7.2.a–c · `backoff.go`, `plaza.go`, `pipeline.go` (+ `memoria.go` según D-F7-3) · R-01, R-02 · **Commit**: `rojo(captacion): contratos del worker y del aforo`
- [x] **T7.11 · rojo(captacion): `intakeahead`** — `aacc30e` (`intakeahead.go` y `warmup.go`; `sanitize.go` e `intakeahead_classify.go`, sin exportados, nacen en el verde) · 🌐❓ · dep. T7.14 · cumple R7.3.a–b · R-11, R-12 · **Commit**: `rojo(captacion): contrato de intakeahead`
- [x] **T7.12 · rojo(captacion): `reanalisis` y puentes (import) 2–3** — `7cb66e2` (puente `captacion/reanalisis → internal/flujos/events` declarado; el de `flujos/runtime` **evitado**: `threadLimit int` es el octavo parámetro de `NewService`, y un valor ≤ 0 se rechaza) · 🌐❓ · dep. T7.17 · cumple R7.4.a–c
  - **Hecho cuando**: seis puertos con sus firmas; decidido en el commit si `DefaultThreadLimit` entra por constructor (sin puente 3, recomendado) o por puente; puentes en `fronteras_test.go` · 🔶 `reanalisis_test` (33) y `dobles_test` (AST) · **Commit**: `rojo(captacion): contrato de reanalisis y sus puentes`
- [x] **T7.13 · Punto de control del rojo** — sobre `1ff2012`: `grep` = 15 = `make test-pendiente` (`PENDIENTES=15 · ROJOS=11`, rc=0), todos de `pipeline`; `reanalisis` e `intakeahead` ya estaban en verde (paquetes independientes, hallazgo 26) · 🌐❓ · pendientes contados (`grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/captacion | wc -l`) = `make test-pendiente` (sin PR: la sesión sigue)
- [x] **T7.18 · verde(captacion): `pipeline`** — `8d63643` (`slot.go`), `3b6dfd4` (`backoff.go`), `b6f55e3` (los cuatro del worker, un commit: métodos del mismo `Worker` que se llaman entre sí) y `8e23511` (tests que matan los mutantes vivos); 216 mutantes sobre los seis ficheros: 201 muertos, 0 vivos, 3 equivalentes, 12 que no compilan (hallazgo 36) · 🌐❓ · reloj y canal de despertar probados sin `sleep` real; mutantes sobre `pipeline.go` y `plaza.go` si el inventario los deja en complejo
- [x] **T7.19 · verde(captacion): `intakeahead`, `reanalisis`** — `reanalisis`: `e813c06` (`reanalisis_errors.go`), `5d3e1b1` (`reanalisis.go`, `reanalisis_checks.go`, `reanalisis_source.go`, un commit: se necesitan mutuamente) · `intakeahead`: `9d43c45` (los cuatro de producción, un commit) y `19a0c73` (test que mata el mutante vivo) · 🌐❓
- [x] **T7.20 · refactor(captacion) e informe** — sin commit de refactor (no hizo falta: la partición por tema se hizo al nacer, D-F7-6); la tabla de `make cobertura-ficheros` va en el PR · 🌐❓ · la tabla de `make cobertura-ficheros` va al PR; no bloquea · cierra la sesión: PR

### Correspondencia de nombres (E-11) — F7-03 (`pipeline`, `intakeahead`, `reanalisis`)

Valores (48 s, K = 1, 5 s / 30 s / 5 min / 10 / 3, 4 / 64 / 45 s / 110 s), textos de error y de log, claves de log y las causas
`calidad`·`infra`·`job_invalido` no cambian. Se conservan `NewWorker`, `Worker`, `Config`, `Run`, `Pool`, `New`, `Request`,
`Warm`, `Sink`, `SinkFunc`, `ConfigStore`, `ProviderSelector`, `WithWorkers`, `WithQueueSize`, `WithTimeout`,
`WithWarmTimeout`, `Jobs`, `Features`, y los métodos de puerto que satisfacen piezas ya escritas (`PlazaDe`, `Obtener`,
`Decrypt`, `ShippingZones`). El paquete `reanalisis` y sus ficheros conservan el nombre.

| Paquete | Viejo / spec | Nuevo |
|---|---|---|
| `pipeline` (ficheros) | `pipeline.go` (1.148) | `pipeline.go` · `pipeline_loop.go` · `pipeline_chain.go` · `pipeline_outcome.go` (D-F7-6) |
| `pipeline` (ficheros) | `plaza.go` · `memoria.go` (mitad catálogo) | `slot.go` · `pipelinehelpertest/catalog_memory.go` (D-F7-5) |
| `pipeline` | `Descifrador` · `Catalogos` · `ZonasDeEnvio` | `Decrypter` · `Catalogs` · `ShippingZones` |
| `pipeline` | `EtapaIdeas` · `EtapaEspecificaciones` · `EtapaNormalizacion` · `EtapaMatch` · `EtapaDraft` | `IdeasStage` · `SpecsStage` · `NormalizationStage` · `MatchStage` · `DraftStage` |
| `pipeline` | `ErrSinCablear` · `PlazoPorLlamadaSuelo` · `Opcion` | `ErrNotWired` · `CallTimeoutFloor` · `Option` |
| `pipeline` | `ConAforo` · `ConZonasDeEnvio` | `WithCapacity` · `WithShippingZones` |
| `pipeline` | `Despertar` · `DrenarDespierto` · `Drenar` · `UnaVuelta` | `Wake` · `DrainAwake` · `Drain` · `RunOnce` |
| `pipeline` | `Config{Cadencia, MaxIntentosCalidad, MaxIntentosInfra, BackoffTope}` | `Config{Cadence, MaxQualityAttempts, MaxInfraAttempts, BackoffCap}` (`BackoffBase` igual) |
| `pipeline` | `KPorPlaza` · `Plaza` · `Valida` · `Plazas` | `KPerSlot` · `Slot` · `Valid` · `Slots` |
| `pipeline` | `Aforo` · `NuevoAforo` · `Tomar` · `Esperando` | `Capacity` · `NewCapacity` · `Acquire` · `Waiting` |
| `pipeline` | `CausaCalidad` · `CausaInfra` · `CausaJobInvalido` | `CauseQuality` · `CauseInfra` · `CauseInvalidJob` |
| `pipeline` | `CadenciaPorDefecto` · `BackoffBasePorDefecto` · `BackoffTopePorDefecto` | `DefaultCadence` · `DefaultBackoffBase` · `DefaultBackoffCap` |
| `pipeline` | `MaxIntentosInfraPorDefecto` · `MaxIntentosCalidadPorDefecto` | `DefaultMaxInfraAttempts` · `DefaultMaxQualityAttempts` |
| `pipeline` | — (costuras nuevas de test) | `WithClock` · `WithTicker` |
| `pipelinehelpertest` | `CatalogoEnMemoria` · `NuevoCatalogoEnMemoria` · `RomperLaLectura` · `Lecturas` | `CatalogMemory` · `NewCatalogMemory` · `BreakRead` · `Reads` |
| `intakeahead` (ficheros) | `calentamiento.go` · `saneo.go` · `intakeahead.go` (592) | `warmup.go` · `sanitize.go` · `intakeahead.go` + `intakeahead_classify.go` (E-13) |
| `intakeahead` | `Calentador` · `WithCalentador` · `WithCalentamiento` | `Warmer` · `WithWarmer` · `WithWarmup` |
| `reanalisis` (ficheros) | `reanalisis.go` (772) | `reanalisis.go` · `reanalisis_errors.go` · `reanalisis_checks.go` · `reanalisis_source.go` (D-F7-6) |
| `reanalisis` | `Solicitud` · `Resultado` · `EstadoEnCurso` | `Request` · `Result` · `StatusInProgress` |
| `reanalisis` | `RazonPurgada` · `RazonNuncaGuardada` | `ReasonPurged` · `ReasonNeverStored` |
| `reanalisis` | `ViaInvalidaError{Via, Configurada}` · `FeatureAusenteError` · `CredencialAusenteError` | `InvalidViaError{Via, Configured}` · `FeatureMissingError` · `CredentialsMissingError` |
| `reanalisis` | `FuenteAusenteError` · `EnCursoError` · `ErrSinCablear` | `SourceUnavailableError` · `InProgressError` · `ErrNotWired` |
| `reanalisis` | `Solicitudes` · `Hilo` · `Compositor` · `ConfigLLM` | `Intakes` · `Thread` · `Composer` · `LLMConfig` |
| `reanalisis` | `Servicio` · `NewServicio(…6)` · `Reanalizar` | `Service` · `NewService(…6, threadLimit int)` · `Reanalyze` |

🔴 **Para F7-04 (T7.23, T7.24)**: tres candados de cableado copiados en F0 buscan **texto** con los nombres viejos y ya no
pueden quedar «verdes sin tocarlos» (hallazgo 28): `pipeline.ConAforo`, `pipeline.ConZonasDeEnvio`, `intakePipeline.Despertar`,
`intakeahead.WithCalentamiento`, `reanalisis.NewServicio`, `stages.ConEmpujeCRM`.

## Bloque D · cara HTTP y conmutar · 🌐❓ · sesión F7-04 · T7.21–T7.26 (= **TX.19–TX.21** + intenciones)
Para cuando: `reanalyze.go`, `intents.go` en verde · `bridge_captacion.go` con test de cableado completo · huella igual · candados verdes · PR (y traspaso si aplica).

- [ ] **T7.21 = TX.19 · rojo(apipublica): `reanalyze.go` e `intents.go`** · 🌐❓ · dep. T7.20 · cumple R7.4.d, R7.7.a
  - **Hecho cuando**: `reanalyze.go` sin gate en la cadena y con el 400 de forma antes de los 403 (T-8 de FX); `intents.go` con `C/intentcfg`, gate `llm_intent` dentro del handler y `ConfigPush` best-effort (D-FX-1 alternativa: **no** hay puente que retirar); tests viejos `reanalyze_test` (16), `intents_test` (6), `intents_aditividad_test` (2) leídos · **Commit**: `rojo(apipublica): re-análisis e intenciones`
- [ ] **T7.22 = TX.20 · verde(apipublica): `reanalyze`, `intents`** · 🌐❓ · un commit por fichero
- [ ] **T7.23 · conmutar(captacion): el arranque nuevo cablea captación** · 🌐❓ · dep. T7.22 · cumple R7.2.a, R7.6.b
  - **Ficheros**: `internal/arranque/{fase3_almacenes,fase5_captacion,fase7_flujos,fase9_fondo}.go`; `internal/arranque/bridge_captacion.go` + `bridge_captacion_test.go` (`aheadBridge`, `composerBridge`, la clausura del sink; ida y vuelta de `WindowKey`) — nivel **simple**, una pasada; `internal/arranque/captacion_cableado_test.go`; `bridge_inferencia.go` pierde `llmConfigBridge` (el `reanalisis` nuevo recibe el `tenantllm` nuevo)
  - **Hecho cuando**: tabla de `arquitectura.md` §4 y §6 aplicada; segunda instancia **vieja** de `intake.Postgres` solo para agregador y compositor («muere F8»); **test de cableado completo** (`05` §4.2): afirma que el arranque construye worker, aforo, `Pool`, servicio de re-análisis y store de intenciones **nuevos** *y* que ninguna fase de `internal/arranque` importa `internal/{intake,intakeahead,reanalisis,intentcfg}` viejos fuera de `bridge_captacion.go` (grep por ruta de import; por eso la segunda instancia vieja se construye **dentro** del adaptador), no solo el campo del contenedor; `pipeline_captacion_cableado_test` y `calentamiento_cableado_test` verdes sin tocarlos; `captacion` **no** se añade a `Conmutados` (entra en F8)
  - **Gate**: `make ci-local` rc=0 · huella verde · **Commit**: `conmutar(captacion): el arranque nuevo cablea captacion`
- [ ] **T7.24 = TX.21 · conmutar(captacion): H1, E1 y E2** · 🌐❓ · dep. T7.23 · cumple R7.7.a
  - **Hecho cuando**: H1, E1, E2 por la nueva; `Reanalysis` e `Intents` = `nil` en la vieja; G7 y `quotetext.ConPlazo` leen `PlazoPorLlamadaSuelo` del `pipeline` **nuevo** (aserción de igualdad); `reanalisis_cableado_test` mira la cara nueva; `ConfigPush` = `nil` en la vieja (E2 ya no la usa); `FaseActual = 7`. FX TX.21 ya está escrita así (reconciliada el 2026-09-29)
  - **Gate**: el de TX.7 · **Commit**: parte del `conmutar(captacion)`
- [ ] **T7.25 · refactor(solicitudes): INV-1 vigila la captación nueva** · 🌐❓ · dep. T7.23 · cumple R7.6.c · lista de `diseno.md` §6; guarda anti-hueco intacta · **Commit**: `refactor(solicitudes): el candado INV-1 mira captacion`
- [ ] **T7.26 · Traspaso** · 🌐❓ · solo mientras existan los dos entornos (si la sesión corrió en local, se tacha) · `traspasos/TRASPASO-F7-captacion.md`; §7: puentes (import) y adaptador con fecha de muerte, SQL no corrido, `casebank` sin prueba de extremo a extremo (D-F7-2)

## Bloque E · cierre local · 💻 · sesión F7-05 · T7.27–T7.29
Para cuando: suites contra Postgres y procesos verdes contra los dos binarios, 0 SKIP · `dev` empujado · `ESTADO.md`.

- [ ] **T7.27 · procesos(captacion): pasada 9C (= T9.28)** · 🌐→💻 · con **D-F9-1 = sí** (recomendación; si no, se tacha y lo cubre T9.34) · suites de `intake` (las **tres**: `ContratoQueue`, `ContratoMachine`, `ContratoReanalysis`; ✎ F7-01), `casebank`, `intentcfg` contra Postgres con el arnés y el mismo `Montaje` que en memoria (P4); P4 (mensaje a borrador) y P8 (re-análisis) verdes contra `viejo` y `nuevo`; 0 SKIP · **Gate**: `make test-procesos` rc=0 del log
- [ ] **T7.28 · Integración en `dev`** · 💻 · sin squash; `ci-local` con lint v2.12.2
- [ ] **T7.29 · Cierre de F7** · 💻 · `ESTADO.md`, este README («cerrada», SHA, hallazgos), `CERRADO` en el traspaso si lo hubo · **Commit**: `docs(reorganizacion-modular): F7 cerrada`
