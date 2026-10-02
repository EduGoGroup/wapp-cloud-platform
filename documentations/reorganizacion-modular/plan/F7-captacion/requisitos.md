# F7 · Requisitos — historias y criterios EARS

> Formato: [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §2. `C` =
> `internal/modulos/captacion`.

## H7.1 · La cola y sus etapas nacen por contrato

> Como **la sesión web**, quiero reconstruir la cola `intake_jobs`, las cinco etapas y el worker desde
> contratos que digan sus reglas (evidencia, topes, plazos, reintentos), para que la lógica llegue con
> su test hecho.

- **R7.1.a** · **EL** módulo `C` **DEBERÁ** tener los 32 ficheros de [`diseno.md`](diseno.md) §2 con
  su `x_test.go`. — Verifica: `un_fichero_un_test_test.go` verde.
- **R7.1.b** · **EL** contrato de `C/intake` **DEBERÁ** fijar los estados literales del job
  (`aggregating`, `pending`, `processing`, `done`, `failed`) y de etapa (`p2`, `p3`, `p4`, `match`,
  `draft`), y `WindowKey{TenantID, SessionID, ContactID, EventID}` con los mismos cuatro campos
  `string` y en el mismo orden que el viejo. — Verifica: `C/intake/{store,machine}_test.go`.
- **R7.1.c** · **CUANDO** una etapa LLM devuelva una frase que no está en el texto normalizado del
  cliente, **LA** etapa **DEBERÁ** tratarla como no-evidencia (`evidence.Contains` = false). —
  Verifica: `C/evidence/evidence_test.go` y los tests de `p2`/`p3`/`p4`.
- **R7.1.d** · **SI** P3 recibe más de 10 ítems, **ENTONCES** **DEBERÁ** aplicarse el tope de
  `tope.go`. — Verifica: `C/stages/tope_test.go`.
- **R7.1.e** · **MIENTRAS** un fichero esté en rojo, **SU** test **DEBERÁ** llevar
  `//go:build pendiente` y el contrato solo exportados con `panic(pendiente.Implementar(…))`. —
  Verifica: `go vet -tags pendiente ./internal/modulos/captacion/...` rc=0 y `make ci-local` rc=0.

## H7.2 · Una sola plaza, un solo worker

> Como **el Edge**, quiero que el Cloud nunca me pida dos cadenas de lote a la vez por mi única plaza
> de inferencia, para no bloquear los turnos interactivos.

- **R7.2.a** · **EL** arranque nuevo **DEBERÁ** lanzar **una** goroutine del worker del pipeline
  (W=1) y construir **un** aforo con `KPorPlaza = 1`. — Verifica: `pipeline_captacion_cableado_test.go`
  de `internal/arranque` (I-CP-4) y `C/pipeline/plaza_test.go`.
- **R7.2.b** · **CUANDO** un Edge anuncie que está listo (`gw.OnEdgeReady`), **EL** worker
  **DEBERÁ** despertar sin esperar a su cadencia (5 s). — Verifica: `pipeline_test.go` con reloj falso.
- **R7.2.c** · **DONDE** la vía del tenant sea `api`, **EL** aforo **NO DEBERÁ** exigir plaza. —
  Verifica: `plaza_test.go`.

## H7.3 · El adelanto de la ventana sigue siendo por pull

> Como **la dueña del negocio**, quiero que la ventana cierre antes cuando el modelo ya clasificó la
> intención, para ver antes el borrador, sin que un fallo del adelanto pierda el pedido.

- **R7.3.a** · **EL** `intakeahead.Pool` **DEBERÁ** atender con 4 workers y una cola de 64; **SI** la
  cola está llena, **ENTONCES** **DEBERÁ** soltar la marca de la clave y dejar que la ventana cierre por
  su reloj (log `adelanto: cola de clasificación llena; la ventana cerrará por su reloj`). —
  Verifica: `C/intakeahead/intakeahead_test.go`.
- **R7.3.b** · **EL** calentamiento **NO DEBERÁ** bloquear a quien lo pide y **DEBERÁ** poder
  apagarse con `WAPP_LLM_WARMUP_ENABLED=false`. — Verifica: `calentamiento_test.go` y
  `calentamiento_cableado_test.go`.

## H7.4 · El re-análisis no cambia la solicitud

> Como **la dueña del negocio**, quiero pedir un re-análisis sin que mi solicitud cambie de estado,
> y que me diga claramente si ya hay uno en curso, para corregir un borrador sin perder el control de
> la solicitud.

- **R7.4.a** · **EL** servicio `C/reanalisis` **NO DEBERÁ** poder transicionar, editar ni escribir
  revisiones: su puerto `Solicitudes` solo tiene `ReanalysisTargetOf` (INV-10). — Verifica: aserción de
  compilación del puerto en `reanalisis_test.go`.
- **R7.4.b** · **SI** el evento ya tiene un job vivo, **ENTONCES** **DEBERÁ** devolver `EnCursoError`
  con el texto `reanalisis: el evento ya tiene un job vivo (%s)`. — Verifica: `reanalisis_test.go`.
- **R7.4.c** · **SI** falta cualquiera de sus seis dependencias, **ENTONCES** `NewServicio`
  **DEBERÁ** devolver `ErrSinCablear` y el arranque abortar. — Verifica: ídem + `reanalisis_cableado_test`.
- **R7.4.d** · **CUANDO** la petición tenga mala forma, **LA** cara **DEBERÁ** responder 400 **antes**
  que los 403 de los gates del servicio (FX T-8). — Verifica: `apipublica/reanalyze_test.go` (TX.19).

## H7.5 · Los puertos nacen cubiertos sin BD

> Como **la sesión web**, quiero una suite por puerto corrida por su doble en memoria, para que lo que
> hoy prueban los tests de integración quede especificado antes de F9.

- **R7.5.a** · **LOS** puertos `intake.JobStore` y `intake.PipelineStore` **DEBERÁN** tener su suite en
  `intakehelpertest` corrida por `MemoryStore`. — Verifica: `go test ./internal/modulos/captacion/intake/`.
- **R7.5.b** · **EL** puerto `casebank.Store` **DEBERÁ** tener `casebankhelpertest.Contrato` y un doble
  nuevo `casebankhelpertest.Memoria` (idempotencia por literal exacto: `Existe`). — Verifica: ídem.
- **R7.5.c** · **EL** puerto `intentcfg.Store` **DEBERÁ** tener `intentcfghelpertest.Contrato`, que pasa el
  `MemoryStore` de producción. — Verifica: ídem.

## H7.6 · El ciclo de negocio queda congelado, no roto

> Como **Jhoan**, quiero que la captación nueva conviva con la conversación vieja por puentes
> declarados y con fecha de muerte, para cortar el ciclo 2 en F8 sin sorpresas (D-7).

- **R7.6.a** · **SI** `C/**` importa un paquete viejo fuera de los puentes declarados (dos, o tres si T7.12 no evita el de `flujos/runtime`), **ENTONCES
  EL** gate **DEBERÁ** fallar. — Verifica: `fronteras_test.go`.
- **R7.6.b** · **EL** agregador viejo **DEBERÁ** seguir pidiendo adelantos al `Pool` **nuevo** y
  recibiendo sus clasificaciones, y el re-análisis nuevo **DEBERÁ** usar el **mismo** compositor que
  el agregador (uno solo en el proceso). — Verifica: `internal/arranque/puente_captacion_test.go` y
  el test de cableado de captación.
- **R7.6.c** · **EL** candado INV-1 de `solicitudes/intakes` **DEBERÁ** vigilar los directorios
  **nuevos** de captación (`intake`, `pipeline`, `stages`) en vez de los viejos. — Verifica:
  `TestINV1_SoloElPOSTDelDueñoAprueba` con la lista de F7.

## H7.7 · Rutas y oráculo

> Como **Jhoan**, quiero que las rutas de captación se muden a la cara nueva y que sus procesos pasen
> contra los dos binarios, para saber que el binario nuevo hace lo mismo que el viejo.

- **R7.7.a** · **CUANDO** F7 conmute, **LA** cara nueva **DEBERÁ** servir H1
  (`POST /api/v1/intakes/{id}/reanalyze`, `intakes.write`, sin gate en la cadena) y E1–E2
  (`GET/PUT /api/v1/intents`, `intents.read`/`intents.write`, gate `llm_intent` dentro del handler,
  push al Edge best-effort) con el patrón byte a byte. — Verifica: huella + candado de mudanzas (TX.21).
- **R7.7.b** · **DONDE** D-F9-1 esté aceptada, **LA** sesión local **DEBERÁ** correr P4 y P8 contra
  `viejo` y `nuevo` y las suites de `intake`, `casebank`, `intentcfg` contra Postgres (T9.28). —
  Verifica: `TRASPASO-F7-captacion.md`, `CERRADO`.
- **R7.7.c** · **EL** binario `cmd/server` y `cmd/casebank` **NO DEBERÁN** cambiar en F7 (D-F7-2). —
  Verifica: `git diff <sha-inicio>..HEAD -- cmd/ internal/bootstrap internal/publicapi` vacío.
