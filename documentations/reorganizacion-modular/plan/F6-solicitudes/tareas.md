# F6 · Tareas

> Formato de [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills:
> `reconstruir-modulo` (la fase), `contrato-tdd` (cada fichero), `validar-antes-de-cerrar` (cada
> gate), `traspaso-web-local` (T6.26→T6.27), `procesos-testcontainers` (T6.27). `S` =
> `internal/modulos/solicitudes`, `V` = paquete viejo. La web trabaja en su rama y abre PR hacia `dev`
> (`--base dev`), integrado **sin squash**. Gate de tests, siempre sin pipe:
> `GOWORK=off go test -count=1 -race -v ./internal/modulos/solicitudes/... > "$TMPDIR/s.log" 2>&1; echo rc=$?; grep -c -- '--- SKIP' "$TMPDIR/s.log"`.
> Gate de rojo: `GOWORK=off go vet -tags pendiente ./internal/modulos/solicitudes/...; echo rc=$?` → 0.
> **Patrón F1**: el rojo lleva **solo exportados** (el linter `unused` rompe el gate con no exportados sin uso).
>
> **Ceremonia** (`05` E-12): la del inventario aprobado en T6.1. *Simple* = contrato, test y lógica en una pasada
> (un commit `verde(solicitudes): <fichero>`); *medio* = rojo y verde por fichero, agrupados por paquete; *complejo* =
> E-2…E-9 con mutantes. **Sin umbral de cobertura (P2)**: un test por promesa del contrato; mutantes en el nivel
> complejo; procesos de F9. `make cobertura-ficheros` es un informe: la tabla va al PR; no bloquea.
> **Auxiliares no exportados** (`05` E-4, P6): su test nace en el **verde** y solo si llevan regla de negocio o ramas no
> triviales; no se testea fontanería ni `if err != nil`; el resto lo cubre F9.
> **Cierre de cada sesión**, tres cosas: tareas `[x]` con SHA, un bloque en `ESTADO.md`, hallazgos nuevos en el
> [`README.md`](README.md) de la fase.
> **E-11**: el adaptador de arranque se llama `bridge_<x>.go` (antes «`puente` + `_<x>.go`»); tipos en inglés
> (`intakesBridge`). «Puente» queda solo para el **import** nuevo → viejo de `05` §4.1.
> Las letras A–I de los bloques anteriores a la recalibración (las cita `DECISIONES.md`, columna «Bloquea») equivalen a:
> A → F6-01 · B → F6-02/F6-03 · C → F6-04 · D → F6-01/F6-02/F6-03 · E → F6-03 · F → F6-04 · G/H → F6-05 · I → F6-06.

## Bloque F6-01 · inventario E-12 y hojas · 🌐 · T6.1–T6.5, T6.14
Para cuando: tabla del inventario E-12 **aprobada por Jhoan** · `sigv1` y `tenantvars` en verde (suite de `tenantvars` verde en memoria) · `note.go` con contrato y test · `ci-local` rc=0 con 0 SKIP · PR.

- [x] **T6.1 · Inventario E-12** — `27a7924` (aprobado por Jhoan el 2026-10-07) · 🌐 · dep. F5 cerrado · cumple R6.1.a
  - **Ficheros**: `plan/F6-solicitudes/README.md` (estado, SHA), `diseno.md` §1, §1.1 y §5 si difieren
  - **Produce**: (1) las 5 entradas del README comprobadas y ficheros/líneas/exportados/tests recontados con los comandos de `diseno.md` §1; (2) la tabla `archivo · estado en memoria · concurrencia · BD/transacciones · nº de consumidores · nivel (simple/medio/complejo)` de los 41 ficheros (parte de la clasificación provisional de `diseno.md` §1.1; si un archivo sale peor, sube de nivel); (3) la **lista de adaptadores `bridge_<x>.go`** que F6 crea y los que retira (`arquitectura.md` §4.1), con la pregunta abierta D-F6-1 ↔ P5 a la vista; (4) el inventario de textos observables (`grep -rn 'errors.New("\|fmt.Errorf("' V`) anotado en `diseno.md` §5
  - **Hecho cuando**: **Jhoan aprueba la tabla. Antes de eso no se escribe código.**
  - **Gate**: `make ci-local` rc=0 · **Commit**: `docs(reorganizacion-modular): F6 arranca — inventario E-12 aprobado`
- [x] **T6.2 · rojo(solicitudes): contrato de `integrations/sigv1`** — `98f9220` (simple, una pasada) · 🌐 · dep. T6.1 · cumple R6.4.c
  - **Ficheros**: `S/integrations/sigv1/sigv1.go`, `sigv1_test.go` · **Hecho cuando**: 3 exportados con promesa (tiempo constante, cuerpo crudo, timestamp por parámetro; ✎ 2026-10-07: la ventana ±300 s y el reloj **no** son de `sigv1` sino de la cara HTTP, hallazgo 1); casos de `V/sigv1_test.go` (7) leídos
  - **Gate**: rojo · **Commit**: `rojo(solicitudes): contrato de integrations/sigv1` (si el inventario lo fija *simple*: una pasada, y T6.14 se cierra con el mismo SHA)
- [x] **T6.3 · rojo(solicitudes): `tenantvars` + `tenantvarshelpertest`** — `8c7ce8b`; la invocación contra Postgres, escrita y sin correr, en `8cb2129` · 🌐 · dep. T6.1 · cumple R6.3.c
  - **Ficheros**: `S/tenantvars/{tenantvars,memory,postgres}.go` + tests, `S/tenantvars/tenantvarshelpertest/contrato.go`
  - **Hecho cuando**: 11 exportados; la suite `Contrato(t, func(t) Montaje)` escrita (sin `t.Skip`); `memory_test.go` la invoca; la invocación contra Postgres con el arnés queda escrita (P4; la corre T6.27) · **Commit**: `rojo(solicitudes): contrato de tenantvars`
- [x] **T6.4 · rojo(solicitudes): `intakes/note.go`** — `571482b` (simple, una pasada: ya en verde) · 🌐 · dep. T6.1 · cumple R6.1.d
  - **Hecho cuando**: `MaxNoteRunes`, `NoteTooLongError` (+`Error`), `SanitizeNote` con R-08 en el comentario; texto `cart: …` asertado (D-F6-4); 14 casos de `cart/notes_test.go` leídos; corpus con casos adversarios (`reglas.md` §2, T-15) · **Commit**: `rojo(solicitudes): contrato de intakes/note` (si el inventario lo fija *simple*: una pasada, y su verde no espera a T6.15)
- [x] **T6.14 · verde(solicitudes): `sigv1`, `tenantvars`** — `98f9220` (`sigv1`), `0108667` (`memory`), `177f529` (`postgres`); `tenantvars.go` nació en verde en `8c7ce8b` · 🌐 · un commit por fichero (`verde(solicitudes): <fichero>`) · **Gate**: tests rc=0 y 0 SKIP; la suite `tenantvarshelpertest` pasa en memoria con `-race`; `make cobertura-ficheros` como informe (no bloquea)
- [x] **T6.5 · Cierre de la sesión F6-01** — 2026-10-07: `ci-local` `GATE_RC=0`, `test-pendiente` `PENDIENTES=0 · ROJOS=0`, PR hacia `dev` · 🌐 · **Hecho cuando**: `ci-local` rc=0; `make test-pendiente` anotado; las tres cosas del cierre; PR abierto

## Bloque F6-02 · `intakes` (1/2): contratos del paquete y tipos puros en verde · 🌐 · T6.6–T6.8, T6.15
Para cuando: los 24 ficheros de `S/intakes` con contrato y test + `intakeshelpertest` escrita · `note.go` y los 10 tipos puros en verde · `vet -tags pendiente` rc=0 · `ci-local` rc=0 · PR.

> **Recuento real de F6-02 (2026-10-08)**: `S/intakes` tiene **40** ficheros de producción, no 24 (`ls S/intakes/*.go | grep -v _test | wc -l`):
> `note.go` + 9 tipos puros + 9 acciones + `service_metrics.go` + `service_revalidate.go` + `notifier.go` + `buyerdata.go` +
> `buyerdata_postgres.go` + 4 `memory*.go` + 12 `postgres*.go`; **56** ficheros de test y **10** de la suite `intakeshelpertest`.
> `PENDIENTES=108 · ROJOS=43` al cerrar (los pone en verde F6-03).
>
> **Correspondencias de nombres (E-11) de F6-02** (exportado viejo en español → nuevo; el valor de wire no cambia; los nombres
> de **fichero** se conservan como los nombra el inventario): `ClaveLineaUnitPrice`/`ClaveLineaLabel` → `LineKeyUnitPrice`/`LineKeyLabel` ·
> `TTLLiteralPorDefecto` → `DefaultLiteralTTL` · `ClavePayloadSourceText`/`ClavePayloadLines`/`ClaveLineaEvidence` →
> `PayloadKeySourceText`/`PayloadKeyLines`/`LineKeyEvidence` · `SobreLiteral` (`.Completo`, `.Vacio`) → `LiteralEnvelope` (`.Complete`, `.Empty`) ·
> `(LiteralRevision).Vacio` → `.Empty` · `PartirLiteral`/`FundirLiteral`/`LiteralVencido` → `SplitLiteral`/`MergeLiteral`/`LiteralExpired` ·
> `EventoLineaCorregida`/`EventoAprobado`/`EventoInfoPedida` → `EventLineCorrected`/`EventApproved`/`EventInfoRequested` ·
> `PublicadorDeMetricas` (`.PublicarMetrica`) → `MetricsPublisher` (`.PublishMetric`) · `ClaveAsCorrection`/`ClaveCorrectsRevisionNo`/`ClaveCorrectsKind` →
> `KeyAsCorrection`/`KeyCorrectsRevisionNo`/`KeyCorrectsKind` · `OpciónPostgres`/`ConCifraDeLiteral`/`ConLogDeRetencion` →
> `PostgresOption`/`WithLiteralCipher`/`WithRetentionLog` · `(*MemoryStore).SetLogDeRetencion`/`.RevisionesPersistidas` → `.SetRetentionLog`/`.PersistedRevisions`.
> No exportados: `comoObjeto`/`comoLista` → `asObject`/`asList` · `instanteDelBorrador` → `draftInstant` · `recuentoDeCorrección` → `correctionCount` ·
> `claveDeLínea`/`claveDe` → `lineKey`/`lineKeyOf` · `esLíneaDePlataforma` → `isPlatformLine`. **Nuevos**: `WithClock` (D-F6-5) y `(*MemoryStore).StoredStatus`.
> ⚠️ Las tareas de F6-03…F6-05 y `arquitectura.md` siguen citando los nombres viejos (`ConCifraDeLiteral`, `PublicadorDeMetricas`…): léanse con esta tabla.

- [x] **T6.6 · rojo(solicitudes): tipos puros de `intakes`** — `113912e` (son **9** ficheros puros, no 10: `customernote.go` no nace; + `edit.go`/`discard.go` mínimos, adelantados de T6.7 porque el puerto `Store` nombra `EditMode` y `DiscardOutcome`) · 🌐 · dep. T6.5 · cumple R6.1.a–c, R6.1.e
  - **Ficheros**: `status.go`, `intakes.go`, `revisions.go`, `shipping.go`, `literal.go`, `customernote.go`, `summary.go`, `crm.go`, `metricas.go`, `revalidate.go` + tests (10 + 10)
  - **Hecho cuando**: reglas R-06, R-09, R-10 y textos de §5 en los comentarios; 🔶 lectura E-8 de sus tests viejos (tabla §2.1) hecha y anotada en el commit · **Commit**: `rojo(solicitudes): contratos de los tipos de intakes`
- [x] **T6.15 · verde(solicitudes): `note.go` y los 10 tipos puros de `intakes`** — `b45ad8d` (`status`), `0698426` (`intakes`), `7634ba7` (`summary`, simple, una pasada), `0426da5` (`revisions`), `eb26402` (`literal`), `090ee38` (`shipping`), `743d85d` (`revalidate`), `909c967` (`metricas`), `c4420eb` (`crm`); `2b8bf7a` (dos avisos de lint en tests); `note.go` ya estaba (`571482b`). 126 PASS con `-race -v`, 0 SKIP; 2–7 mutantes a mano por fichero · 🌐 · un commit por fichero; cabecera `// Porta internal/… @ <sha>` (E-10); `note.go` solo si no quedó en verde en T6.4
- [x] **T6.7 · rojo(solicitudes): acciones de la bandeja** — `9f393c2` (las 9 + `service_metrics.go` y `service_revalidate.go`, que recogen lo de `*Service`/`Option` que el viejo tenía en `metricas.go` y `revalidate.go`; D-F6-5 = opción `WithClock`) · 🌐 · dep. T6.6 · cumple R6.2.a–b
  - **Ficheros**: `service.go`, `approve.go`, `aprobadas.go`, `edit.go`, `discard.go`, `deposit.go`, `requestinfo.go`, `vencimiento.go`, `reanalisis.go` + tests
  - **Hecho cuando**: R-01…R-06 en los contratos; reloj inyectado en `Summary` (D-F6-5); 🔶 `approve_contrato_test`, `correct_test`, `tres_puertas_crm_test`, `vencimiento_test` (16) leídos · **Commit**: `rojo(solicitudes): contratos de la bandeja`
- [x] **T6.8 · rojo(solicitudes): almacenes, comprador y notificador + suite** — `c366c68` (`memory.go` en 4 ficheros, `postgres.go` en 12, suite de 50 casos en 10 ficheros; la invocación contra Postgres, escrita y compilada, **sin correr**, en `test/procesos/intakes_contrato_test.go`) · 🌐 · dep. T6.7 · cumple R6.3.a, R6.3.d
  - **Ficheros**: `memory.go`, `postgres.go`, `buyerdata.go` + `buyerdata_postgres.go` (D-F6-6), `notifier.go` + tests; `S/intakes/intakeshelpertest/contrato.go`
  - **Hecho cuando**: suite `Contrato(t, func(t) Montaje)` escrita, con la marca de estado vigilando **todas** las columnas que cada operación puede tocar (hallazgo 35); `memory_test.go` la invoca y la invocación contra Postgres con el arnés queda escrita (P4; la corre T6.27); T-1 (homónimo DEK) en el comentario de `buyerdata.go`; 🔶 plantillas de `notifier` inventariadas · **Commit**: `rojo(solicitudes): almacenes, comprador y notificador de intakes`

## Bloque F6-03 · `intakes` (2/2): almacenes, acciones, notificador y candados · 🌐 · T6.9, T6.16–T6.18
Para cuando: `grep -rn 'pendiente.Implementar' internal/modulos/solicitudes/intakes/*.go | wc -l` → 0 · suite `intakeshelpertest` verde en memoria con `-race` y 0 SKIP · candados de vencimiento y de la poda verdes; los dos INV-1 escritos tras `//go:build pendiente` · `ci-local` rc=0 · PR.

> **Cierre de F6-03 (2026-10-08)**: `PENDIENTES=0 · ROJOS=1` (solo `inv1_aprobar_test.go`, hasta T6.25). `S/intakes` queda en **42** ficheros de
> producción (nacen `notifier_templates.go` y `postgres_revisions_read.go`) y **59** de test.
>
> **Correspondencias de nombres (E-11) de F6-03** (no exportados y tests; viejo → nuevo): `ejecutarPoda`/`sellarPodada`/`podarLiteralQuery`/`abrirLiteral`/`cifrarLiteral`/`revisionPodada` →
> `runPrune`/`sealPruned`/`pruneLiteralQuery`/`openLiteral`/`sealLiteral`/`prunedRevision` (el candado de la poda persigue `revisionsOf → runPrune → sealPruned`) ·
> `últimaRevisiónTx` → `lastRevisionTx` · `shippingZonesDe` → `shippingZonesOf` · `esUUID` → `isUUID` · `señalDeCorrección` → `correctionSignal` ·
> `guardarRevisiónLocked`/`leerRevisionesLocked`/`casaConElFiltroLocked`/`tieneEventoVivoLocked` → `saveRevisionLocked`/`readRevisionsLocked`/`matchesFilterLocked`/`hasLiveEventLocked` ·
> `publicarMetrica`/`métricaDeCorrección`/`métricaDeAprobación`/`métricaDeInformación`/`desdeElBorrador` → `publishMetric`/`publishCorrectionMetric`/`publishApprovalMetric`/`publishInfoRequestMetric`/`elapsedFromDraft` ·
> campos de `Service` `metricas`/`ahora` → `metrics`/`metricsNow` · `silencia` → `silences` · `contenerPánico`/`enviarComoElDueño` → `containPanic`/`sendAsOwner` ·
> `sinPlantillaAl{PedirSeña,Recordar,Aprobar}` → `noTemplateOn{DepositRequest,Reminder,Approve}` · `yaAvisado`/`cutoffDelPlazo` → `alreadyNotified`/`deadlineCutoff` ·
> `tienePrecio`/`etiquetaDeLínea`/`tieneLíneasDeCliente`/`conLaRevisión` → `hasPrice`/`lineLabel`/`hasCustomerLines`/`withRevision` ·
> tests `TestINV1_SoloElPOSTDelDueñoAprueba`/`…Pregunta` → `TestINV1_OnlyTheOwnersPOSTApproves`/`…Asks` · `TestPoda_ElInstanteSelladoNoSeDescarta` → `TestPrune_TheSealedInstantIsNotDiscarded`.

- [x] **T6.16 · verde(solicitudes): `memory.go` y las 9 acciones** — `4c93249` (`memory*.go`, los cuatro en un commit: comparten el struct) · `21438bb` (`service*.go`) · `80a8d59` `approve` · `abbdd91` `edit` · `99ad506` `discard` · `da756b7` `deposit` · `ff09931` `requestinfo` · `4c1258c` `vencimiento` · `0e330d2` `reanalisis` (`aprobadas.go` ya estaba en verde: su test pierde la etiqueta en `80a8d59`) · `141acfd` (una sola copia de los auxiliares compartidos por los dos almacenes) · 🌐 · `memory.go` primero: la suite `intakeshelpertest` pasa entera con `-race`, 0 SKIP
  - **D-F6-8 (2026-10-08)**: el `MemoryStore` nuevo refresca `UpdatedAt` en toda escritura de cabecera (el viejo solo en `AbandonByEvent`): aquí **no** se copia el doble viejo, manda la suite
- [x] **T6.17 · verde(solicitudes): `notifier.go`, `buyerdata.go`, `buyerdata_postgres.go`** — `cf61639` (`notifier.go` + `notifier_templates.go`, partido por E-13) · `e8f1e10` (`buyerdata_postgres.go`; `buyerdata.go` ya estaba en verde) · 🌐 · textos de plantilla asertados byte a byte
- [x] **T6.18 · verde(solicitudes): `postgres.go` + candado de la poda** — `e8126b5` `postgres` · `bb50152` `postgres_revisions_read` (fichero 13, no previsto) · `ced6fac` `read` · `7080179` `status` · `53a051c` `revisions` · `f0bc0d4` `items` · `c8a0fe9` `shipping` · `2534efe` `reminders` · `c4cf4fc` `approved`, `crm`, `customernote`, `reanalysis` · `09872e6` `discard` · candado de la poda `ab2e4bb` · suite contra Postgres destapada en `8d6cfa8` · 🌐 · cumple R6.2.d
  - **Hecho cuando**: SQL copiado literal (`diff` de `grep -o 'public\.[a-z_]*'` viejo/nuevo vacío); funciones puras con test; candado AST `revisionsOf → ejecutarPoda → sellarPodada` añadido a `postgres_test.go`; su verdad la da la suite `intakeshelpertest` contra Postgres (P4, T6.27) y F9
- [x] **T6.9 · rojo(solicitudes): candados de invariante de `intakes`** — `bd9ea5a` (los dos INV-1, tras `//go:build pendiente` hasta T6.25) · los dos del plazo, en verde, en `4c1258c` · 🌐 · dep. T6.7 · cumple R6.2.a–c
  - **Ficheros**: `S/intakes/inv1_aprobar_test.go` (aprobar + pedir info), candados de vencimiento en `vencimiento_test.go`
  - **Hecho cuando**: listas de `diseno.md` §6 (versión F6); el control positivo **falla hasta TX.16/TX.18** (no hay `Approve` en `apipublica` todavía) → el test va con `//go:build pendiente` hasta T6.25, y se dice en el commit · **Commit**: `rojo(solicitudes): candados INV-1 y del plazo`

## Bloque F6-04 · `quotetext`, `telemetria`, `integrations`, `crmpush` · 🌐❓ · T6.10–T6.13, T6.19–T6.21
Para cuando: `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/solicitudes | wc -l` → 0 · puente (import) de `telemetria` declarado · suite `integrationshelpertest` verde con el doble · candado R-12 y esquema `wapp-crm-v1` verdes · `vet -tags integracion ./test/procesos/...` rc=0 · `ci-local` rc=0 · PR.

> **Cierre de F6-04 (2026-10-08)**: `PENDIENTES=0 · ROJOS=1` (solo `inv1_aprobar_test.go`, hasta T6.25). Nacen **17** ficheros de producción, no 13:
> `telemetria` 1 · `quotetext` 6 (`precios_numbers.go` y `quotetext_fewshot.go` salen por E-13) · `integrations` 8 (`worker_delivery.go` y
> `worker_failure.go`, ídem) · `crmpush` 2 · más el doble `integrationshelpertest/memoria.go` y los 6 ficheros de la suite (61 casos).
> Los cuatro paquetes se escribieron en paralelo, uno por sub-agente, y commiteó solo el orquestador: los 14 commits compilan solos (medido).
>
> **Correspondencias de nombres (E-11) de F6-04** (exportado viejo → nuevo; el valor de wire y las etiquetas `json` no cambian; los ficheros del
> inventario conservan su nombre). `integrations` y `crmpush` ya estaban en inglés: no cambia ningún exportado; nuevos `WorkerOption` y `WithClock`.
> **`telemetria`**: `FlujoBandeja`/`VersionFlujoBandeja` → `InboxFlow`/`InboxFlowVersion` · `Publicador` (`.PublicarMetrica`) → `Publisher` (`.PublishMetric`).
> **`quotetext`**: `BorradorVersion` → `DraftVersion` · `Linea` (`.PorConfirmar`) → `Line` (`.PendingPrice`) · `Borrador` (`.Lineas`) → `Draft` (`.Lines`) ·
> `BorradorDe` → `DraftOf` · `TieneLineasDeCliente` → `HasCustomerLines` · `Importe` → `Amount` · `MaxRunasTexto` → `MaxTextRunes` ·
> `Veredicto` (`.Motivo`, `.Detalle`) → `Verdict` (`.Reason`, `.Detail`) · `ValidarSalida`/`Verificar`/`SecuenciaEsperada` → `ValidateOutput`/`Verify`/`ExpectedSequence` ·
> `EjemplosPorDefecto`/`MaxRunasEjemplo`/`MaxRunasFewShot` → `DefaultExamples`/`MaxExampleRunes`/`MaxFewShotRunes` · `RefEstiloSemilla` → `SeedStyleRef` ·
> `OrigenLLM`/`OrigenDeterminista` → `SourceLLM`/`SourceDeterministic` · `ErrSinCablear`/`ErrSinLineas` → `ErrNotWired`/`ErrNoLines` ·
> `Sugerencia` (`.Texto`, `.Origen`, `.Motivo`) → `Suggestion` (`.Text`, `.Source`, `.Reason`) · `LectorSolicitudes`/`LectorHistorial`/`LectorSemilla` →
> `IntakeReader`/`HistoryReader`/`SeedReader` · `Servicio`/`Opción`/`NewServicio`/`(*Servicio).Sugerir` → `Service`/`Option`/`NewService`/`(*Service).Suggest` ·
> `ConSemilla`/`ConEjemplos`/`ConPlazo` → `WithSeed`/`WithExamples`/`WithTimeout` · `ParseSemilla` → `ParseSeed`. `Render` y `ProviderSelector` se quedan.
> Los 13 motivos de `fallback_reason` (`Motivo…` → `Reason…`): `SinImportes` → `DraftWithoutAmounts` · `TextoIlegible` → `UnreadableText` ·
> `NumeroIlegible` → `UnreadableNumber` · `SinImportesEnTexto` → `TextWithoutAmounts` · `FaltaUnitario` → `MissingUnitPrice` · `FaltaTotal` → `MissingTotal` ·
> `ImporteAjeno` → `ForeignAmount` · `NumeroAjeno` → `ForeignNumber` · `ImportesFueraDeSitio` → `AmountsOutOfPlace` · `SinEjemplos` → `NoExamples` ·
> `ProveedorNoDisponible` → `ProviderUnavailable` · `LLMFallo` → `LLMFailed` · `SalidaIlegible` → `UnreadableOutput`.
> Candado R-12: `TestContrato_NingunCampoClaveEsConstante` → `TestContract_NoKeyFieldIsConstant` (`camposVigilados`/`directoriosVigilados` → `watchedFields`/`watchedDirs`).
> Los no exportados renombrados van en el mensaje del commit de cada fichero.
> ⚠️ T6.25, `arquitectura.md` y `diseno.md` siguen citando `quotetext.NewServicio`, `ConSemilla` y `ConPlazo`: léanse con esta tabla
> (y `internal/arranque/quotetext_cableado_test.go` busca hoy ese **texto**: se reajusta en T6.25, `reglas.md` T-6).

- [x] **T6.10 · rojo(solicitudes): `intakes/telemetria` y su puente (import)** — `31b9343` (nivel simple, una pasada: nace en verde, con el puente declarado en el mismo commit) · 🌐 · dep. T6.6 · cumple R6.6.d
  - **Ficheros**: `S/intakes/telemetria/telemetria.go` + test; `internal/modulos/fronteras_test.go` (puente de import `telemetria → internal/flujos/store`, «muere F8»)
  - **Commit**: `rojo(solicitudes): contrato de telemetria y su puente a flujos/store`
- [x] **T6.11 · rojo(solicitudes): `intakes/quotetext` (P5)** — `0159604` (`precios.go` y `quotetext.go`); `borrador.go` `0d245b7` y `render.go` `d87a9f9` son simples y nacen en verde. Los motivos de `fallback_reason` son **13**, no nueve · 🌐 · dep. T6.6
  - **Ficheros**: 4 + 4 tests · **Hecho cuando**: 47 exportados; puertos `:192,:202,:210,:219` estructurales; 🔶 los nueve motivos de `fallback_reason` y los 8 tests viejos leídos · **Commit**: `rojo(solicitudes): contrato de quotetext`
- [x] **T6.12 · rojo(solicitudes): `integrations`, `crmpush`, suite y doble** — `caa864a` (`crmpush` y el candado R-12) · `d42a5d6` (`integrations`: 4 contratos en rojo, `store.go` y `outbox_stats.go` en verde —solo tipos—, suite de 61 casos y `Memoria` en verde, esquema `wapp-crm-v1` en verde, invocación contra Postgres escrita **sin correr**) · 🌐 · dep. T6.2, T6.3, T6.8 · cumple R6.3.b, R6.4.a–b, R6.4.d
  - **Ficheros**: `S/integrations/{store,gate,worker,crud,outbox_stats,postgres}.go` + tests; `integrationshelpertest/{contrato,memoria}.go` (+ `memoria_test.go`: el doble tiene lógica); `integrations/contrato_wapp_crm_v1_test.go` (D-F6-3); `crmpush/{push,desde_intakes}.go` + tests; `crmpush/contrato_test.go` (candado R-12, dirs de `diseno.md` §6)
  - **Hecho cuando**: el doble `Memoria` nace **completo** y en verde (no es código de producción); la suite `Contrato(t, func(t) Montaje)` vigila todas las columnas que cada operación toca (hallazgo 35) y su invocación contra Postgres con el arnés queda escrita (P4; la corre T6.27); reloj inyectado en el worker · **Commit**: `rojo(solicitudes): contratos del puente CRM`
  - **Heredado de F9-02 (H-1, D-F6-7)**: el contrato de `integrations/worker.go` promete «contexto cancelado → vuelve **sin** loguear a `ERROR`» (hoy lo hacen `worker.go:209` y `:225`, y `TestP0_Arranque/sin_errores` falló por ello 1 de 161 veces); se escribe aquí, con su caso en `worker_test.go` (cancelar a mitad de la primera llamada), no al final
- [x] **T6.13 · Recuento de pendientes del módulo** — sobre `d42a5d6`: el `grep` da **19** (`gate` 2, `crud` 1, `worker` 3, `postgres` 13) y `make test-pendiente` lo mismo (`PENDIENTES=19`, `ROJOS=15`); `quotetext` y `crmpush` ya estaban en verde · 🌐 · **Hecho cuando**: `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/solicitudes | wc -l` anotado tras los rojos de esta sesión (cota ≈191 con todo en rojo; aquí será menor, porque `sigv1`, `tenantvars` e `intakes` ya están en verde) y coincide con `make test-pendiente`
- [x] **T6.19 · verde(solicitudes): `telemetria`, `quotetext`** — `31b9343` (`telemetria`) · `0d245b7` (`borrador`) · `d87a9f9` (`render`) · `6a202b0` (`precios` + `precios_numbers`) · `ccd24a7` (`quotetext` + `quotetext_fewshot`) · 🌐
- [x] **T6.20 · verde(solicitudes): `crmpush`, `integrations`** — `3a94b94` (`push`) · `b01d01c` (`desde_intakes`) · `afd3eed` (`crud`) · `cb9e36b` (`gate`) · `c2803b5` (`postgres`, con `SecretFingerprint` y `CountOutbox`) · `8ebc6eb` (`worker` + `worker_delivery` + `worker_failure`) · 🌐 · candado R-12 verde; esquema `wapp-crm-v1` verde
  - **Heredado de F9-02 (D-F6-7)**: el verde del worker cumple esa promesa; no se porta el `w.log.Error` ante `ctx.Err() != nil` — ✅ `8ebc6eb`: y **corta**, no solo calla (no cuenta el intento ni toca la métrica; hallazgo 37)
- [x] **T6.21 · refactor(solicitudes) y medición** — sin commit de `refactor` (nada que unificar: un dueño por paquete); pendientes = **0**; mutantes en lo complejo: `postgres.go` 37 (34 muertos, 3 equivalentes) y `worker*.go` 46 (46 muertos), más los de nivel medio; tras el cierre, `af68fe3` endurece la plantilla `null` del worker (hallazgo 38a); tabla de cobertura en el PR · 🌐 · pendientes = 0; mutantes en los ficheros de nivel complejo (inventario); tabla de cobertura por fichero en el PR (informe; no bloquea)

## Bloque F6-05 · cara HTTP, cableado y conmutar G1–G18 · 🌐❓ · T6.22–T6.26 (= **TX.16–TX.18** de FX)
Para cuando: 12 ficheros de `apipublica` en verde · huella igual · `go list -deps` prueba lo nuevo · test de cableado completo verde · candados INV-1 sin `//go:build pendiente` y verdes · `FaseActual = 6` · `ci-local` rc=0 · PR.

> **F6-05 se entrega en dos PR** (Jhoan, 2026-10-08): **F6-05a** = la cara en rojo y verde, **sin montar** (T6.22–T6.23, rama
> `reorg/f6-05a-cara-solicitudes`); **F6-05b** = cableado + conmutar + candados (T6.24–T6.25). Motivo: `intakes.go` viejo son 1073 líneas
> (5 ficheros por E-13) y el cableado y la conmutación no compilan por separado.
>
> **Cierre de F6-05a (2026-10-08)**: `PENDIENTES=0 · ROJOS=1` (solo `inv1_aprobar_test.go`, hasta T6.25; pasa ya con `-tags pendiente`).
> Nacen **19** ficheros de producción en `internal/apipublica`, no 12 (y 35 de test), y el paquete `eventstelemetryhelpertest` (suite de 13 casos + doble).
> Cuatro sub-agentes en *worktrees*, uno por familia de rutas; commitean en su *worktree* y el orquestador integra por `cherry-pick`
> (protocolo §2): los **23 commits compilan solos** (medido). Ningún `Mount*` está cableado todavía: las 18 rutas siguen en la cara vieja.
>
> **Correspondencias de nombres (E-11) de F6-05a** (spec → código): `instantes.go` → `instants.go` · `plazoescritura.go` → `writedeadline.go`
> (`conPlazoDeRedacción` → `writeDeadline`) · `QuoteSuggester.Sugerir` → `.Suggest` · `aplicarGateLLMIntake`/`ocultarCamposLLM`/`comoObjetoJSON`/`comoListaJSON`
> → `intakeApplyLLMGate`/`intakeHideLLMFields`/`intakeAsJSONObject`/`intakeAsJSONList` · `editModeDe` → `intakeEditModeOf` · goldens
> `intake_detail_{con,sin}_llm_intake` → `…_{with,without}_llm_intake` · `registerEventTelemetry` → `MountEventTelemetry`. Los no exportados
> ya en inglés ganan prefijo de área (`intake…`, `export…`, `summary…`, `quote…`, `tenantVar…`, `integration…`, `crm…`, `eventTelemetry…`):
> el paquete es uno y lo escribieron cuatro manos; la lista completa va en el mensaje de cada commit.
> **Ficheros fuera de los 12 de la spec**: `intakes_dto.go`, `intakes_filter.go`, `intakes_status.go`, `intakes_items.go`, `intakes_approve.go`
> (los cinco trozos de `intakes.go`, E-13) · `intakereports.go` (puerto, `IntakeReportsDeps` y `MountIntakeReports` de G7·G9·G10) ·
> `integrations_validate.go` (E-13).
> **Montajes que cablea F6-05b**: `MountIntakes` (G1–G6, G8) · `MountIntakeReports` (G7, G9, G10; campo `QuoteSuggestions` y
> `QuoteWriteDeadline`) · `MountTenantVariables` (G11–G12) · `MountIntegrations` (G13–G16) · `MountCRMCallback` (G17, no exige `MW`) ·
> `MountEventTelemetry` (G18).

- [x] **T6.22 = TX.16 · rojo(apipublica): solicitudes** — un rojo por familia: `6f40bb5` (bandeja) · `e71271f` (exportación, resumen y sugerencia) · `403ff97` (variables y puente CRM) · `3cddd86` (telemetría, con la suite y el doble) · 🌐 · dep. T6.21, TX.15 · cumple R6.5.a
  - **Ficheros** (autoridad FX `tareas.md` TX.16): `instantes.go`, `intakes.go`, `intakes_llm_gate.go`, `export.go`, `summary.go`, `quotesuggestion.go`, `plazoescritura.go`, `tenantvariables.go`, `integrations.go`, `crmcallback.go`, `eventstelemetry.go`, `eventstelemetry_store.go` y sus 12 tests
  - **Hecho cuando**: `plazoescritura.go` recibe el plazo por parámetro; G17 sin `Authenticate` y con access-log; el gate de G2 oculta los campos LLM igual que el viejo; `eventstelemetry_store.go` con test de mapeo; tests viejos de `arquitectura.md` §7 leídos (E-8) · **Commit**: `rojo(apipublica): solicitudes` (estado de la TX se lleva en FX)
- [x] **T6.23 = TX.17 · verde(apipublica): solicitudes, fichero a fichero** — `2cc4cde` `instants` · `2b9c44d` `intakes_filter` · `4737bfd` `intakes_dto` · `4694d93` `intakes_llm_gate` · `c6986b4` `intakes_status` · `c1d1e14` `intakes_items` · `86588cf` `intakes_approve` · `aeb1cf7` `intakes` · `06830c5` `writedeadline` · `61814fc` `export` · `a1e7791` `summary` · `a1a9223` `quotesuggestion` · `472d9b6` `intakereports` · `2b09c2f` `tenantvariables` · `baf2c74` `integrations` (+ `integrations_validate`) · `a60f074` `crmcallback` · `2d1dde8` `eventstelemetry` · `d9afa10` `eventstelemetry_store` · `6675927` (invocación contra Postgres, **sin correr**, y el candado que la admite, D-F6-12). Mutantes: `eventstelemetry_store.go` y su doble 76 (73 muertos, 3 equivalentes; 8 del texto del SQL solo los mata Postgres, F6-06) · 🌐 · `verde(apipublica): <fichero>`
> **Cierre de F6-05b (2026-10-08)**: `PENDIENTES=0 · ROJOS=0`. T6.24 y T6.25 van en **un** commit, `79f274e`: no compilan por separado
> (precedentes `98b24c5`, `0ebb743`). Ficheros tocados fuera de la lista de T6.24: `contenedor.go`, `fase8_transporte.go`, `mudanzas.go`,
> `flows.go`, `http.go` y `bridge_contact.go`. El test de cableado sale en dos por E-13: `solicitudes_cableado_test.go` y
> `solicitudes_cableado_identidad_test.go`. ⚠️ **D-F6-13 corrige la letra de T6.25**: en la vieja `Intakes` **no** es `nil`, es el centinela
> de montaje `oldFaceIntakesMountSentinel` (sin él desaparece H1, que es de F7); los otros ocho campos sí son `nil`. Nombres (E-11):
> `requestsFaceDeps`, `requestsDepsOfTheNewFace`, `intakeStoreViejo`, `quoteCallTimeout`/`quoteWriteMargin`/`quoteWriteDeadline`.

- [x] **T6.24 · conmutar(solicitudes): el arranque nuevo cablea solicitudes** — `79f274e` (segunda instancia vieja `intakeStoreViejo` en `fase3_almacenes.go`, D-F6-1: único import viejo de solicitudes que queda en el arranque, con alias `intakesviejo`, en `contenedor.go` y `fase3_almacenes.go`; ningún `bridge_intakes.go`) · 🌐 · dep. T6.23 · cumple R6.6.a–c
  - **Ficheros**: `internal/arranque/{fase3_almacenes,fase5_captacion,fase6_solicitudes,fase7_flujos,fase9_fondo}.go`, `internal/arranque/solicitudes_cableado_test.go` (nuevo); si el inventario aprobado lo pide, `internal/arranque/bridge_intakes.go` + `bridge_intakes_test.go` (nivel simple, una pasada)
  - **Hecho cuando**: tabla de `arquitectura.md` §4 y §6 aplicada: una sola instancia nueva de `Service`, notificador, recordatorios, worker; para el carrito, la salida que fije el inventario (segunda instancia **vieja** de `intakes.Postgres`, D-F6-1, o `bridge_intakes.go`), con el comentario «muere F8»; alias `…viejo` en los imports viejos; **test de cableado completo** (hallazgo 39): afirma por tipo (`%T`) que el arranque construye lo **nuevo** *y*, con un grep por ruta de import, que ninguna fase de `internal/arranque` importa `internal/intakes`, `internal/integrations` ni `internal/tenantvars` viejos fuera del sitio declarado para el carrito
  - **Gate**: `make ci-local` rc=0 · `huella_test.go` verde · **Commit**: `conmutar(solicitudes): el arranque nuevo cablea solicitudes`
- [x] **T6.25 = TX.18 · conmutar(solicitudes): 18 rutas** — `79f274e` (51 rutas en la cara nueva, `FaseActual = 6`, huella igual sin tocar `testdata/huella.json`, INV-1 sin etiqueta y verdes, `Conmutados` sigue `{"acceso","edge"}`; plazo de G7 derivado y con aserción, `TestCableado_TheQuoteWriteDeadlineIsDerived`; centinela de H1, D-F6-13) · 🌐 · dep. T6.24 · cumple R6.5.a–c
  - **Hecho cuando**: G1–G18 por la nueva (G2·G9·G10 en el mismo commit); en la vieja `Intakes`, `QuoteSuggestions`, `TenantVariables`, `Integrations`, `CRM*`, `EventTelemetry` = `nil`; G7 con `pipeline.PlazoPorLlamadaSuelo + 12 s` del **mismo** valor que `quotetext.ConPlazo` (aserción); `FaseActual = 6`; `solicitudes` **no** entra todavía en `Conmutados` (entra cuando muere su último adaptador, `reglas.md` §4); `quotetext_cableado_test`/`reanalisis_cableado_test` de `internal/arranque` reajustados (T-6); los candados INV-1 pierden `//go:build pendiente` (su control positivo ya encuentra `Approve`/`RequestInfo` en `apipublica`) ⚠️ *(D-F6-13, 2026-10-08)*: `Intakes` **no** queda en `nil` sino con el centinela de montaje `oldFaceIntakesMountSentinel` (sin él la vieja deja de registrar H1); los demás sí.
  - **Gate**: el de TX.7 + `make ci-local` rc=0 · **Commit**: parte del `conmutar(solicitudes)`
- [x] ~~**T6.26 · Traspaso a la sesión local**~~ — **tachada** (2026-10-08): F6-05 corre en local, no hay a quién traspasar · 🌐 · skill `traspaso-web-local` · **solo mientras existan los dos entornos** (si F6-05 corre en local, se tacha: no hay a quién traspasar)
  - **Ficheros**: `documentations/reorganizacion-modular/traspasos/TRASPASO-F6-solicitudes.md` · **Hecho cuando**: las 8 secciones; §7 con lo que no se comprobó (SQL nuevo no corrido contra Postgres; carrito viejo con su salida transitoria)

## Bloque F6-06 · cierre local · 💻 · T6.27–T6.29
Para cuando: las tres suites verdes en memoria **y** contra Postgres · P5/P6 verdes contra los dos binarios, 0 SKIP · `dev` integrado y empujado · `ESTADO.md` y README de la fase al día.

- [ ] **T6.27 · procesos(solicitudes): pasada 9C (= T9.27)** · 🌐→💻 · con **D-F9-1 = sí** (recomendación; si no, se tacha y lo cubre T9.34)
  - **Hecho cuando**: suites `intakeshelpertest`, `integrationshelpertest`, `tenantvarshelpertest` contra Postgres (testcontainers) verdes; P5 (bandeja) y P6 (CRM) verdes con `WAPP_PROCESOS_BINARIO=viejo` y `=nuevo`; 0 SKIP · **Gate**: `make test-procesos` rc=0 leído del log
  - **Heredado de F9-02 (H-1, D-F6-7)**: con el worker nuevo, se vuelve a medir `TestP0_Arranque/sin_errores` (`CUENTA=3`, arranques en frío, ambos binarios). Si deja de ser intermitente, se queda; si no, se **redefine el criterio** o lo sustituye un test más acorde, y se anota en el README de F9 (contradicción 19) y en `deuda.md` §5
  - ⚠️ **Revisión independiente (2026-10-01)** — hechos; el criterio de arriba **no se cambia** (es de Jhoan: pregunta abierta D-F9-10 del [README de F9](../F9-procesos/README.md)). (i) «Ambos binarios» no cuadra con D-F6-7: `viejo` (`cmd/server`) conserva el worker viejo hasta F10 y contra él la carrera **persiste**; en `nuevo` quedan además tres goroutines de fondo con el mismo patrón que F6 no reconstruye (el colector de `platform`, el agregador de F8 y el pipeline de F7; contradicción 19, hecho 1). (ii) La remedición con `CUENTA=3` **no discrimina**: son 2 arranques en frío expuestos (uno por binario) y, con 1/161 por arranque, da verde (160/161)² ≈ 98,8 % de las veces **sin arreglar nada**
  - ✎ **D-F9-10 decidida (2026-10-08, Jhoan): (a) + (c).** Lo «heredado» de arriba queda así: **no se remide con `CUENTA=3`**. `sin_errores` tolera ya los `ERROR` de cancelación posteriores a la señal de parada (`p0CountedErrors`), en los dos binarios; a esta tarea le basta `RC=0` de `make test-procesos`
- [ ] **T6.28 · Integración en `dev`** · 💻 · merge sin squash; `ci-local` rc=0 en local con lint v2.12.2
- [ ] **T6.29 · Cierre de F6** · 💻 · `ESTADO.md` y este `README.md` (estado «cerrada», SHA); `CERRADO <fecha>` en el traspaso, si lo hubo · **Commit**: `docs(reorganizacion-modular): F6 cerrada`
