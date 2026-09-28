# F7 · Tareas

> Formato: [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills: `reconstruir-modulo`,
> `contrato-tdd`, `validar-antes-de-cerrar`, `traspaso-web-local` (T7.26→T7.27),
> `procesos-testcontainers` (T7.27). `C` = `internal/modulos/captacion`, `V` = paquete viejo. PR a
> `dev` (`--base dev`), sin squash. Gate de tests sin pipe:
> `GOWORK=off go test -count=1 -race -v ./internal/modulos/captacion/... > "$TMPDIR/c.log" 2>&1; echo rc=$?; grep -c -- '--- SKIP' "$TMPDIR/c.log"` → `rc=0` y `0`.
> Gate de rojo: `GOWORK=off go vet -tags pendiente ./internal/modulos/captacion/...; echo rc=$?` → 0.
> Patrón F1: el rojo lleva **solo exportados**.

## Bloque A · inventario verificado y hojas en rojo · 🌐 · T7.1–T7.6
Para cuando: inventario recontado · `evidence`, `intake`, `anclaje`, `intentcfg`, `casebank` en rojo con suites · `ci-local` rc=0 · PR.

- [ ] **T7.1 · Verdad de campo e inventario** · 🌐 · dep. F6 cerrada · cumple R7.1.a
  - **Hecho cuando**: entradas del README comprobadas; recuento de `diseno.md` §1; resueltos los 🔶 de inventario: D-F7-3 (`memoria.go`), `Plazas` estructural, reloj de `machine_postgres.go`, nombre de la tabla de intenciones, textos observables de `V` y de `publicapi/{reanalyze,intents}.go`
  - **Commit**: `docs(reorganizacion-modular): F7 arranca — inventario verificado`
- [ ] **T7.2 · rojo(captacion): contrato de `evidence`** · 🌐 · dep. T7.1 · cumple R7.1.c · el ejemplo de `05` §10 tal cual · **Commit**: `rojo(captacion): contrato de evidence`
- [ ] **T7.3 · rojo(captacion): `intake` + `intaketest`** · 🌐 · dep. T7.1 · cumple R7.1.b, R7.5.a
  - **Ficheros**: 6 + 6 tests, `C/intake/intaketest/{cola,maquina}.go` · **Hecho cuando**: `WindowKey` idéntico (4 `string`, mismo orden); las dos suites escritas; `memory_test.go` las invoca · **Commit**: `rojo(captacion): contrato de intake (la cola)`
- [ ] **T7.4 · rojo(captacion): `anclaje`** · 🌐 · dep. T7.2 · 🔶 19 casos de `V/anclaje_test.go` · **Commit**: `rojo(captacion): contrato de anclaje`
- [ ] **T7.5 · rojo(captacion): `intentcfg` + suite, `casebank` + suite y doble** · 🌐 · dep. T7.1 · cumple R7.5.b–c
  - **Hecho cuando**: `casebanktest.Memoria` nace completo y en verde; `intentcfg.MemoryStore` corre `intentcfgtest.Contrato` · **Commit**: `rojo(captacion): contratos de intentcfg y casebank`
- [ ] **T7.6 · Cierre del bloque A** · 🌐 · `ci-local` rc=0 · `make test-pendiente` anotado · PR

## Bloque B · rojo de `stages` · 🌐 · T7.7–T7.9
Para cuando: 10 ficheros de `stages` en rojo · puente 1 declarado · PR.

- [ ] **T7.7 · rojo(captacion): etapas LLM** · 🌐 · dep. T7.6 · `plazo.go`, `p2.go`, `p3.go`, `tope.go`, `p4.go`, `fechas.go` + tests · R-03, R-11 en los contratos; 🔶 lectura de `p2/p3/p4/tope/fechas/audio_jamas_al_llm_test` y del AST de `p4_test.go` · **Commit**: `rojo(captacion): contratos de P2, P3 y P4`
- [ ] **T7.8 · rojo(captacion): match** · 🌐 · dep. T7.7 · `match.go`, `match_cascada.go`, `match_lineas.go` (sin exportados: su test nace con el verde) · R-04, R-05; 🔶 `match_*_test` (cascada, cota, dobles, rendimiento) · **Commit**: `rojo(captacion): contratos del match`
- [ ] **T7.9 · rojo(captacion): draft y puente 1** · 🌐 · dep. T7.8 · cumple R7.6.a
  - **Ficheros**: `draft.go` + test; `internal/modulos/fronteras_test.go` (`captacion/stages → internal/flujos/store`, «muere F8») · R-06, R-07; 🔶 `draft_*_test` y su AST · **Commit**: `rojo(captacion): contrato de draft y su puente a flujos/store`

## Bloque C · rojo de `pipeline`, `intakeahead`, `reanalisis` · 🌐 · T7.10–T7.13
Para cuando: todo el módulo en rojo · puentes declarados · `make test-pendiente` = N (≈119) · PR.

- [ ] **T7.10 · rojo(captacion): `pipeline`** · 🌐 · dep. T7.9, T7.3 · cumple R7.2.a–c · `backoff.go`, `plaza.go`, `pipeline.go` (+ `memoria.go` según D-F7-3) · R-01, R-02 · **Commit**: `rojo(captacion): contratos del worker y del aforo`
- [ ] **T7.11 · rojo(captacion): `intakeahead`** · 🌐 · dep. T7.5 · cumple R7.3.a–b · R-11, R-12 · **Commit**: `rojo(captacion): contrato de intakeahead`
- [ ] **T7.12 · rojo(captacion): `reanalisis` y puentes 2–3** · 🌐 · dep. T7.9 · cumple R7.4.a–c
  - **Hecho cuando**: seis puertos con sus firmas; decidido en el commit si `DefaultThreadLimit` entra por constructor (sin puente 3, recomendado) o por puente; puentes en `fronteras_test.go` · 🔶 `reanalisis_test` (33) y `dobles_test` (AST) · **Commit**: `rojo(captacion): contrato de reanalisis y sus puentes`
- [ ] **T7.13 · Cierre del bloque C** · 🌐 · pendientes contados (`grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/captacion | wc -l`) = `make test-pendiente`; PR

## Bloque D · verde de las hojas · 🌐 · T7.14–T7.15
- [ ] **T7.14 · verde(captacion): `evidence`, `anclaje`, `intentcfg`, `casebank`** · 🌐 · un commit por fichero (`verde(captacion): <fichero>`), cabecera `// Porta … @ <sha>`
- [ ] **T7.15 · verde(captacion): `intake`** · 🌐 · `memory.go` primero (las dos suites en verde con `-race`), luego `store`, `machine`, `reanalisis`, y los dos adaptadores (SQL literal, funciones puras)

## Bloque E · verde de `stages` · 🌐 · T7.16–T7.17
- [ ] **T7.16 · verde(captacion): etapas LLM** · 🌐 · `plazo`, `tope`, `fechas`, `p2`, `p3`, `p4`
- [ ] **T7.17 · verde(captacion): match y draft** · 🌐 · `match_cascada`, `match` (+ test de `match_lineas`), `draft`

## Bloque F · verde de `pipeline`, `intakeahead`, `reanalisis` · 🌐 · T7.18–T7.20
Para cuando: pendientes = 0 · cobertura ≥ 80 % · PR.

- [ ] **T7.18 · verde(captacion): `pipeline`** · 🌐 · reloj y canal de despertar probados sin `sleep` real
- [ ] **T7.19 · verde(captacion): `intakeahead`, `reanalisis`** · 🌐
- [ ] **T7.20 · refactor(captacion) y medición** · 🌐 · tabla de cobertura en el PR

## Bloque G · la cara HTTP · 🌐 · T7.21–T7.22 (= **TX.19–TX.20** + intenciones)
- [ ] **T7.21 = TX.19 · rojo(apipublica): `reanalyze.go` e `intents.go`** · 🌐 · dep. T7.20 · cumple R7.4.d, R7.7.a
  - **Hecho cuando**: `reanalyze.go` sin gate en la cadena y con el 400 de forma antes de los 403 (T-8 de FX); `intents.go` con `C/intentcfg`, gate `llm_intent` dentro del handler y `ConfigPush` best-effort (D-FX-1 alternativa: **no** hay puente que retirar); tests viejos `reanalyze_test` (16), `intents_test` (6), `intents_aditividad_test` (2) leídos · **Commit**: `rojo(apipublica): re-análisis e intenciones`
- [ ] **T7.22 = TX.20 · verde(apipublica): `reanalyze`, `intents`** · 🌐 · un commit por fichero

## Bloque H · conmutar · 🌐 · T7.23–T7.26 (incluye **TX.21**)
Para cuando: huella igual · cableado por tipo · candados verdes · traspaso · PR.

- [ ] **T7.23 · conmutar(captacion): el arranque nuevo cablea captación** · 🌐 · dep. T7.22 · cumple R7.2.a, R7.6.b
  - **Ficheros**: `internal/arranque/{fase3_almacenes,fase5_captacion,fase7_flujos,fase9_fondo}.go`; `internal/arranque/puente_captacion.go` + test (`adelantoViejo`, `compositorViejo`, la clausura del sink; ida y vuelta de `WindowKey`); `internal/arranque/captacion_cableado_test.go`
  - **Hecho cuando**: tabla de `arquitectura.md` §4 y §6 aplicada; segunda instancia **vieja** de `intake.Postgres` solo para agregador y compositor («muere F8»); `pipeline_captacion_cableado_test` y `calentamiento_cableado_test` verdes sin tocarlos
  - **Gate**: `make ci-local` rc=0 · huella verde · **Commit**: `conmutar(captacion): el arranque nuevo cablea captacion`
- [ ] **T7.24 = TX.21 · conmutar(captacion): H1, E1 y E2** · 🌐 · dep. T7.23 · cumple R7.7.a
  - **Hecho cuando**: H1, E1, E2 por la nueva; `Reanalysis` e `Intents` = `nil` en la vieja; G7 y `quotetext.ConPlazo` leen `PlazoPorLlamadaSuelo` del `pipeline` **nuevo** (aserción de igualdad); `reanalisis_cableado_test` mira la cara nueva; `FaseActual = 7`. ⚠️ FX TX.21 dice «retirar el puente de intenciones» (D-FX-1 literal): con la alternativa no existe; reconciliar el texto de FX en este commit
  - **Gate**: el de TX.7 · **Commit**: parte del `conmutar(captacion)`
- [ ] **T7.25 · refactor(solicitudes): INV-1 vigila la captación nueva** · 🌐 · dep. T7.23 · cumple R7.6.c · lista de `diseno.md` §6; guarda anti-hueco intacta · **Commit**: `refactor(solicitudes): el candado INV-1 mira captacion`
- [ ] **T7.26 · Traspaso** · 🌐 · `traspasos/TRASPASO-F7-captacion.md`; §7: puentes y adaptadores con fecha de muerte, SQL no corrido, `casebank` sin prueba de extremo a extremo (D-F7-2)

## Bloque I · cierre local · 💻 · T7.27–T7.29
- [ ] **T7.27 · procesos(captacion): pasada 9C** · 🌐→💻 · **solo si D-F9-1 aceptada** (T9.28) · suites de `intake`, `casebank`, `intentcfg` contra Postgres; P4 (mensaje a borrador) y P8 (re-análisis) verdes contra `viejo` y `nuevo`; 0 SKIP · **Gate**: `make test-procesos` rc=0 del log
- [ ] **T7.28 · Integración en `dev`** · 💻 · sin squash; `ci-local` con lint v2.12.2
- [ ] **T7.29 · Cierre de F7** · 💻 · `ESTADO.md`, este README («cerrada», SHA), `CERRADO` en el traspaso · **Commit**: `docs(reorganizacion-modular): F7 cerrada`
