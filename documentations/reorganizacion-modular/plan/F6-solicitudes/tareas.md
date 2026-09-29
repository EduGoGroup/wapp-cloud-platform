# F6 · Tareas

> Formato de [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills:
> `reconstruir-modulo` (la fase), `contrato-tdd` (cada fichero), `validar-antes-de-cerrar` (cada
> gate), `traspaso-web-local` (T6.26→T6.27), `procesos-testcontainers` (T6.27). `S` =
> `internal/modulos/solicitudes`, `V` = paquete viejo. La web trabaja en su rama y abre PR hacia `dev`
> (`--base dev`), integrado **sin squash**. Gate de tests, siempre sin pipe:
> `GOWORK=off go test -count=1 -race -v ./internal/modulos/solicitudes/... > "$TMPDIR/s.log" 2>&1; echo rc=$?; grep -c -- '--- SKIP' "$TMPDIR/s.log"`.
> Gate de rojo: `GOWORK=off go vet -tags pendiente ./internal/modulos/solicitudes/...; echo rc=$?` → 0.
> **Patrón F1**: el rojo lleva **solo exportados** (el linter `unused` rompe el gate con no exportados sin uso).

## Bloque A · inventario verificado y hojas · 🌐 · T6.1–T6.5
Para cuando: inventario recontado igual que `diseno.md` §1 (o corregido en el mismo commit) · `sigv1`, `tenantvars`, `note.go` en rojo · `ci-local` rc=0 · PR.

- [ ] **T6.1 · Verdad de campo e inventario** · 🌐 · dep. F5 cerrado · cumple R6.1.a
  - **Ficheros**: `plan/F6-solicitudes/README.md` (estado, SHA), `diseno.md` §1 y §5 si difieren
  - **Hecho cuando**: las 5 entradas del README comprobadas; ficheros/líneas/exportados/tests recontados con los comandos de `diseno.md` §1; inventario de textos observables (`grep -rn 'errors.New("\|fmt.Errorf("' V`) anotado en `diseno.md` §5
  - **Gate**: `make ci-local` rc=0 · **Commit**: `docs(reorganizacion-modular): F6 arranca — inventario verificado`
- [ ] **T6.2 · rojo(solicitudes): contrato de `integrations/sigv1`** · 🌐 · dep. T6.1 · cumple R6.4.c
  - **Ficheros**: `S/integrations/sigv1/sigv1.go`, `sigv1_test.go` · **Hecho cuando**: 3 exportados con promesa (±300 s, tiempo constante, cuerpo crudo, reloj por parámetro); casos de `V/sigv1_test.go` (7) leídos
  - **Gate**: rojo · **Commit**: `rojo(solicitudes): contrato de integrations/sigv1`
- [ ] **T6.3 · rojo(solicitudes): `tenantvars` + `tenantvarstest`** · 🌐 · dep. T6.1 · cumple R6.3.c
  - **Ficheros**: `S/tenantvars/{tenantvars,memory,postgres}.go` + tests, `S/tenantvars/tenantvarstest/contrato.go`
  - **Hecho cuando**: 11 exportados; la suite escrita (sin `t.Skip`); `memory_test.go` la invoca · **Commit**: `rojo(solicitudes): contrato de tenantvars`
- [ ] **T6.4 · rojo(solicitudes): `intakes/note.go`** · 🌐 · dep. T6.1 · cumple R6.1.d
  - **Hecho cuando**: `MaxNoteRunes`, `NoteTooLongError` (+`Error`), `SanitizeNote` con R-08 en el comentario; texto `cart: …` asertado (D-F6-4); 14 casos de `cart/notes_test.go` leídos · **Commit**: `rojo(solicitudes): contrato de intakes/note`
- [ ] **T6.5 · Cierre del bloque A** · 🌐 · **Hecho cuando**: `ci-local` rc=0; `make test-pendiente` anotado; PR abierto

## Bloque B · contratos y rojo de `intakes` · 🌐 · T6.6–T6.9
Para cuando: 24 ficheros de `S/intakes` en rojo + `intakestest` + los 4 candados de invariante que pueden nacer en rojo · `vet -tags pendiente` rc=0 · PR.

- [ ] **T6.6 · rojo(solicitudes): tipos puros de `intakes`** · 🌐 · dep. T6.5 · cumple R6.1.a–c, R6.1.e
  - **Ficheros**: `status.go`, `intakes.go`, `revisions.go`, `shipping.go`, `literal.go`, `customernote.go`, `summary.go`, `crm.go`, `metricas.go`, `revalidate.go` + tests (10 + 10)
  - **Hecho cuando**: reglas R-06, R-09, R-10 y textos de §5 en los comentarios; 🔶 lectura E-8 de sus tests viejos (tabla §2.1) hecha y anotada en el commit · **Commit**: `rojo(solicitudes): contratos de los tipos de intakes`
- [ ] **T6.7 · rojo(solicitudes): acciones de la bandeja** · 🌐 · dep. T6.6 · cumple R6.2.a–b
  - **Ficheros**: `service.go`, `approve.go`, `aprobadas.go`, `edit.go`, `discard.go`, `deposit.go`, `requestinfo.go`, `vencimiento.go`, `reanalisis.go` + tests
  - **Hecho cuando**: R-01…R-06 en los contratos; reloj inyectado en `Summary` (D-F6-5); 🔶 `approve_contrato_test`, `correct_test`, `tres_puertas_crm_test`, `vencimiento_test` (16) leídos · **Commit**: `rojo(solicitudes): contratos de la bandeja`
- [ ] **T6.8 · rojo(solicitudes): almacenes, comprador y notificador + suite** · 🌐 · dep. T6.7 · cumple R6.3.a, R6.3.d
  - **Ficheros**: `memory.go`, `postgres.go`, `buyerdata.go` (+ `buyerdata_postgres.go` si D-F6-6), `notifier.go` + tests; `S/intakes/intakestest/contrato.go`
  - **Hecho cuando**: suite `Contrato(t, func(t) Montaje)` escrita; `memory_test.go` la invoca; T-1 (homónimo DEK) en el comentario de `buyerdata.go`; 🔶 plantillas de `notifier` inventariadas · **Commit**: `rojo(solicitudes): almacenes, comprador y notificador de intakes`
- [ ] **T6.9 · rojo(solicitudes): candados de invariante de `intakes`** · 🌐 · dep. T6.7 · cumple R6.2.a–c
  - **Ficheros**: `S/intakes/inv1_aprobar_test.go` (aprobar + pedir info), candados de vencimiento en `vencimiento_test.go`
  - **Hecho cuando**: listas de `diseno.md` §6 (versión F6); el control positivo **falla hasta TX.16/TX.18** (no hay `Approve` en `apipublica` todavía) → el test va con `//go:build pendiente` hasta T6.25, y se dice en el commit · **Commit**: `rojo(solicitudes): candados INV-1 y del plazo`

## Bloque C · contratos y rojo de `quotetext`, `telemetria`, `integrations`, `crmpush` · 🌐 · T6.10–T6.13
Para cuando: todo el módulo en rojo · puente declarado · `make test-pendiente` = N anotado · PR.

- [ ] **T6.10 · rojo(solicitudes): `intakes/telemetria` y su puente** · 🌐 · dep. T6.6 · cumple R6.6.d
  - **Ficheros**: `S/intakes/telemetria/telemetria.go` + test; `internal/modulos/fronteras_test.go` (puente `telemetria → internal/flujos/store`, «muere F8»)
  - **Commit**: `rojo(solicitudes): contrato de telemetria y su puente a flujos/store`
- [ ] **T6.11 · rojo(solicitudes): `intakes/quotetext` (P5)** · 🌐 · dep. T6.6
  - **Ficheros**: 4 + 4 tests · **Hecho cuando**: 47 exportados; puertos `:192,:202,:210,:219` estructurales; 🔶 los nueve motivos de `fallback_reason` y los 8 tests viejos leídos · **Commit**: `rojo(solicitudes): contrato de quotetext`
- [ ] **T6.12 · rojo(solicitudes): `integrations`, `crmpush`, suite y doble** · 🌐 · dep. T6.2, T6.3, T6.8 · cumple R6.3.b, R6.4.a–b, R6.4.d
  - **Ficheros**: `S/integrations/{store,gate,worker,crud,outbox_stats,postgres}.go` + tests; `integrationstest/{contrato,memoria}.go` (+ `memoria_test.go`: el doble tiene lógica); `integrations/contrato_wapp_crm_v1_test.go` (D-F6-3); `crmpush/{push,desde_intakes}.go` + tests; `crmpush/contrato_test.go` (candado R-12, dirs de `diseno.md` §6)
  - **Hecho cuando**: el doble `Memoria` nace **completo** y en verde (no es código de producción); reloj inyectado en el worker · **Commit**: `rojo(solicitudes): contratos del puente CRM`
- [ ] **T6.13 · Cierre del bloque C** · 🌐 · **Hecho cuando**: `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/solicitudes | wc -l` anotado (≈191 esperado) y coincide con `make test-pendiente`; `ci-local` rc=0; PR

## Bloque D · verde de hojas y de `intakes` (1/2) · 🌐 · T6.14–T6.16
Para cuando: `sigv1`, `tenantvars`, `note.go`, tipos puros y acciones en verde · cobertura ≥ 80 % · PR.

- [ ] **T6.14 · verde(solicitudes): `sigv1`, `tenantvars`** · 🌐 · un commit por fichero (`verde(solicitudes): <fichero>`) · **Gate**: tests + `make cobertura-ficheros`
- [ ] **T6.15 · verde(solicitudes): `note.go` y los 10 tipos puros de `intakes`** · 🌐 · un commit por fichero; cabecera `// Porta internal/… @ <sha>` (E-10)
- [ ] **T6.16 · verde(solicitudes): `memory.go` y las 9 acciones** · 🌐 · `memory.go` primero: la suite `intakestest` pasa entera con `-race`, 0 SKIP

## Bloque E · verde de `intakes` (2/2) · 🌐 · T6.17–T6.18
- [ ] **T6.17 · verde(solicitudes): `notifier.go`, `buyerdata(_postgres).go`** · 🌐 · textos de plantilla asertados byte a byte
- [ ] **T6.18 · verde(solicitudes): `postgres.go` + candado de la poda** · 🌐 · cumple R6.2.d
  - **Hecho cuando**: SQL copiado literal (`diff` de `grep -o 'public\.[a-z_]*'` viejo/nuevo vacío); funciones puras con test; candado AST `revisionsOf → ejecutarPoda → sellarPodada` añadido a `postgres_test.go`; fuera del umbral (E-6), cobertura anotada

## Bloque F · verde de `quotetext`, `telemetria`, `integrations`, `crmpush` · 🌐 · T6.19–T6.21
Para cuando: 0 pendientes en `S` · cobertura ≥ 80 % · `vet -tags integracion ./test/procesos/...` rc=0 · PR.

- [ ] **T6.19 · verde(solicitudes): `telemetria`, `quotetext`** · 🌐
- [ ] **T6.20 · verde(solicitudes): `crmpush`, `integrations`** · 🌐 · candado R-12 verde; esquema `wapp-crm-v1` verde
- [ ] **T6.21 · refactor(solicitudes) y medición** · 🌐 · pendientes = 0; tabla de cobertura por fichero en el PR

## Bloque G · la cara HTTP de solicitudes · 🌐 · T6.22–T6.23 (= **TX.16–TX.17** de FX)
- [ ] **T6.22 = TX.16 · rojo(apipublica): solicitudes** · 🌐 · dep. T6.21, TX.15 · cumple R6.5.a
  - **Ficheros** (autoridad FX `tareas.md` TX.16): `instantes.go`, `intakes.go`, `intakes_llm_gate.go`, `export.go`, `summary.go`, `quotesuggestion.go`, `plazoescritura.go`, `tenantvariables.go`, `integrations.go`, `crmcallback.go`, `eventstelemetry.go`, `eventstelemetry_store.go` y sus 12 tests
  - **Hecho cuando**: `plazoescritura.go` recibe el plazo por parámetro; G17 sin `Authenticate` y con access-log; el gate de G2 oculta los campos LLM igual que el viejo; `eventstelemetry_store.go` con test de mapeo; tests viejos de `arquitectura.md` §7 leídos (E-8) · **Commit**: `rojo(apipublica): solicitudes` (estado de la TX se lleva en FX)
- [ ] **T6.23 = TX.17 · verde(apipublica): solicitudes, fichero a fichero** · 🌐 · `verde(apipublica): <fichero>`

## Bloque H · conmutar y mudar G1–G18 · 🌐 · T6.24–T6.26 (incluye **TX.18**)
Para cuando: huella igual · `go list -deps` prueba lo nuevo · candados verdes · traspaso escrito · PR.

- [ ] **T6.24 · conmutar(solicitudes): el arranque nuevo cablea solicitudes** · 🌐 · dep. T6.23 · cumple R6.6.a–c
  - **Ficheros**: `internal/arranque/{fase3_almacenes,fase5_captacion,fase6_solicitudes,fase7_flujos,fase9_fondo}.go`, `internal/arranque/solicitudes_cableado_test.go` (nuevo)
  - **Hecho cuando**: tabla de `arquitectura.md` §4 y §6 aplicada: una sola instancia nueva de `Service`, notificador, recordatorios, worker; segunda instancia **vieja** de `intakes.Postgres` solo para el carrito (D-F6-1, comentario «muere F8»); alias `…viejo` en los imports viejos; test de cableado por tipo (`%T`)
  - **Gate**: `make ci-local` rc=0 · `huella_test.go` verde · **Commit**: `conmutar(solicitudes): el arranque nuevo cablea solicitudes`
- [ ] **T6.25 = TX.18 · conmutar(solicitudes): 18 rutas** · 🌐 · dep. T6.24 · cumple R6.5.a–c
  - **Hecho cuando**: G1–G18 por la nueva (G2·G9·G10 en el mismo commit); en la vieja `Intakes`, `QuoteSuggestions`, `TenantVariables`, `Integrations`, `CRM*`, `EventTelemetry` = `nil`; G7 con `pipeline.PlazoPorLlamadaSuelo + 12 s` del **mismo** valor que `quotetext.ConPlazo` (aserción); `FaseActual = 6`; `quotetext_cableado_test`/`reanalisis_cableado_test` de `internal/arranque` reajustados (T-6); los candados INV-1 pierden `//go:build pendiente` (su control positivo ya encuentra `Approve`/`RequestInfo` en `apipublica`)
  - **Gate**: el de TX.7 + `make ci-local` rc=0 · **Commit**: parte del `conmutar(solicitudes)`
- [ ] **T6.26 · Traspaso a la sesión local** · 🌐 · skill `traspaso-web-local`
  - **Ficheros**: `documentations/reorganizacion-modular/traspasos/TRASPASO-F6-solicitudes.md` · **Hecho cuando**: las 8 secciones; §7 con lo que no se comprobó (SQL nuevo no corrido contra Postgres; carrito viejo con segunda instancia)

## Bloque I · cierre local · 💻 · T6.27–T6.29
- [ ] **T6.27 · procesos(solicitudes): pasada 9C (= T9.27)** · 🌐→💻 · con **D-F9-1 = sí** (recomendación; si no, se tacha y lo cubre T9.34)
  - **Hecho cuando**: suites `intakestest`, `integrationstest`, `tenantvarstest` contra Postgres (testcontainers) verdes; P5 (bandeja) y P6 (CRM) verdes con `WAPP_PROCESOS_BINARIO=viejo` y `=nuevo`; 0 SKIP · **Gate**: `make test-procesos` rc=0 leído del log
- [ ] **T6.28 · Integración en `dev`** · 💻 · merge sin squash; `ci-local` rc=0 en local con lint v2.12.2
- [ ] **T6.29 · Cierre de F6** · 💻 · `ESTADO.md` y este `README.md` (estado «cerrada», SHA); `CERRADO <fecha>` en el traspaso · **Commit**: `docs(reorganizacion-modular): F6 cerrada`
