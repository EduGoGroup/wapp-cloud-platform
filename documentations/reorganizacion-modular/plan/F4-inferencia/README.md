# F4 · `inferencia` — el LLM: vía, prompts, credencial del tenant y degradación

> **Estado**: spec escrita el 2026-09-28 sobre `dev` @ `1b18932` (código) / `bad573a` (plan). Sin
> una línea de código. Marco común: [`../00-marco/`](../00-marco/README.md) (no se repite aquí).
> Norma: [`05`](../../05-metodo-contratos-y-tdd.md). Rutas: autoridad
> [`../FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) (filas F1–F4).
>
> ✎ **D-F1-10 (Jhoan, 2026-10-02)**: los paquetes de suite de contrato y de dobles llevan el sufijo compuesto
> **`helpertest`**, el único que los candados de fichero eximen ([`DECISIONES.md`](../DECISIONES.md) §2). Esta spec los
> nombraba con `…test` (`degradationtest`, `tenantllmtest`): se actualizó el sufijo, nada más.
>
> Recalibrado el 2026-10-03 tras la parada de F1 (`05` E-12, §4.2, E-9, E-4; `plan/DECISIONES.md` §3).
>
> ✎ **Estado al 2026-10-07 (F45-02): CONMUTADA, sin cerrar.** `internal/modulos/inferencia` entero en verde (F45-01); el
> arranque nuevo construye el selector, los almacenes y la carga de prompts nuevos y la cara `apipublica` sirve las 4
> rutas F1–F4 (`98b24c5`); `FaseActual = 4` (hoy `5`, por F5). El adaptador `internal/arranque/bridge_inferencia.go`
> vive hasta **F8** (`llmConfigBridge` muere en F7; `turneroBridge`, y con él el fichero, en F8), así que `inferencia`
> sigue fuera de `Conmutados`. `bridge_gateway.go` de F3 está borrado. **Queda el cierre F45-03** (T4.29–T4.31 = T9.25).

## Objetivo en tres líneas

Reconstruir por contrato → rojo → verde los **10 ficheros de producción** (3.072 líneas) de
`internal/{llmvia,llmvia/local,prompts,tenantllm,degradation}` en `internal/modulos/inferencia/`,
con dobles en memoria para los dos puertos que no los tienen, y **conmutar**: el arranque nuevo
construye el selector de vía, los almacenes y la carga de prompts nuevos, y la cara `apipublica`
sirve las 4 rutas de la fase. Hacia fuera no cambia nada (ni una ruta, métrica, texto ni tabla).

## Qué es este módulo (lo mínimo para no perderse)

- 🔴 **ADR-0045** (fuera de este repo, vigente desde 2026-08-23): *«El Cloud es el **único**
  orquestador LLM: construye los prompts, trocea, ordena las llamadas y valida. El Edge se comporta
  como un servidor de inferencia estilo Ollama — prompt entra → JSON sale — y **no interpreta
  nada**.»* Aquí se ve en `llmvia/local`: arma el prompt con los `Build…Prompt` de
  `wapp-shared/llm`, lo empuja por el frame `InferenceRequest` del gateway y aísla el JSON con
  `llm.ExtractJSON`; **no tiene un solo prompt propio** (`internal/llmvia/local/local.go:10-23`).
- **ADR-0030 + E-3** (fuera): dos vías que **no se mezclan** — `local` (el Ollama del Edge del
  tenant, por frame) y `api` (proveedor externo con la credencial del tenant cifrada aquí). Si la vía
  falla se degrada al **Nivel A con aviso al dueño**, nunca a la otra vía. **El Cloud nunca tiene
  Ollama propio.**
- 🔒 **Cero gasto** (decisión D-11 de Jhoan, 2026-08-29, fuera de este repo): *«No se compra una
  credencial de pago para demostrar la vía `api`»*. La vía `api` **nunca corrió en campo con
  credencial real**; su única implementación cableada es `anthropic` (`gemini` es un stub que falla
  nombrado, `internal/tenantllm/tenantllm.go:36-43`). ⇒ En esta fase la vía `api` se prueba **solo**
  con dobles; ninguna tarea pide una clave real, y ningún test llama a un proveedor.
- **P1 no vive aquí**: el prompt de clasificación lo gobierna el catálogo de intenciones
  (`internal/intentcfg`, módulo `captacion`, F7). Este módulo solo ajusta por fichero **P2–P5**.
- **No hay `//go:embed` de plantillas** en este módulo: el texto compilado de P2–P5 vive en
  `wapp-shared/llm@v0.4.5` (`plantilla.go:97 PlantillaPorDefecto`). La excepción de `embed.go` de
  E-3 **no aplica** (ver «Contradicciones»).

## Entradas (tiene que ser cierto para empezar)

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | F0 cerrado: `internal/pendiente`, `make test-pendiente`, `make cobertura-ficheros`, los tres candados de `internal/modulos/`, la huella y `apipublica` vacía | `ls internal/pendiente internal/arranque internal/apipublica` · `make -n test-pendiente cobertura-ficheros` |
| E2 | **F3 conmutado**: `internal/modulos/edge/grpc` existe en verde y el arranque nuevo usa su `*Server`, con el **adaptador de `local.Frame`** de F3 que hace hablar al `llmvia` viejo con el gw nuevo (`../FX-cara-http/arquitectura.md` §4.1) | `grep -rn 'modulos/edge/grpc' internal/arranque/*.go` no vacío |
| E3 | F2 conmutado (el gate `entitlements.RequireFeature` que usan las 4 rutas vive en `modulos/acceso/entitlements`) | `ls internal/modulos/acceso/entitlements` |
| E4 | 🔴 **D-F4-1 aplicada en F0** (T0.27: los candados viejos que barren todo `internal/` no ven el árbol nuevo). Sin ella, el primer `verde` de `tenantllm.go` pone rojo `make ci-local` | [`reglas.md`](reglas.md) T-1 |
| E5 | `dev` verde y al día | skill `validar-antes-de-cerrar` |

## Salidas (es cierto al cerrar)

| # | Condición | Verifica |
|---|---|---|
| S1 | 26 ficheros nuevos bajo `internal/modulos/inferencia/` (10 de producción + 4 de dobles/suites + 12 de test) en verde, sin etiqueta `pendiente` | `grep -rln 'go:build pendiente' internal/modulos/inferencia` vacío |
| S2 | Un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9. Sin umbral de cobertura (P2): `make cobertura-ficheros` es informe (la tabla va al PR; no bloquea). Los dos `postgres.go`: su verdad la da la suite contra Postgres (P4) y F9 | revisión del PR · T4.31 |
| S3 | El arranque nuevo construye `tenantllm`, `degradation`, `prompts` y `llmvia` **nuevos**; los viejos solo los usa el adaptador `bridge_inferencia.go` hasta F7/F8, con su test de cableado completo | `grep -n 'internal/llmvia\|internal/tenantllm"' internal/arranque/*.go` → solo `bridge_inferencia.go` |
| S4 | Las 4 rutas F1–F4 las sirve `apipublica`; en la vieja `TenantLLM` y `DegradationNotices` = `nil`; `FaseActual = 4` | TX.14 · huella idéntica |
| S5 | El candado C2 nuevo (`modulos/inferencia/llmvia/c2_via_test.go`) en verde, y el viejo sigue verde | `make ci-local` rc=0 |
| S6 | 0 `t.Skip`, 0 `pendiente.Implementar` en el módulo | `grep -rn 't.Skip\|pendiente.Implementar' internal/modulos/inferencia` vacío |

## Orden de lectura

[`requisitos.md`](requisitos.md) → [`arquitectura.md`](arquitectura.md) → [`diseno.md`](diseno.md)
→ [`reglas.md`](reglas.md) → [`tareas.md`](tareas.md).

## Bloques de sesión

F4 y F5 **comparten sesiones** (son pequeñas: ≈ 10 archivos de producción cada una). Cada sesión: 45–90 min y cierre
de tres cosas (tareas `[x]` con SHA, bloque en `ESTADO.md`, hallazgos en este README).

| Sesión | Entorno | Tareas de F4 | Punto de parada |
|---|---|---|---|
| [`F45-01`](../sesiones/F45-01-web-inventario-e-inferencia.md) · inventario E-12 (de F4 y de F5) + F4 entero en verde | 💻 | T4.1–T4.9, T4.11–T4.23 | Jhoan aprobó la tabla de niveles; `internal/modulos/inferencia` sin etiqueta `pendiente`; C2 nuevo y viejo verdes; `ci-local` rc=0 con 0 SKIP |
| [`F45-02`](../sesiones/F45-02-web-conmutar-inferencia-y-catalogo.md) · adaptador, conmutar y 4 rutas (y F5 entero) | 💻 | T4.10, T4.24–T4.28 (+ TX.12–TX.14) | huella idéntica con `FaseActual = 4`; test de cableado de `bridge_inferencia.go` verde |
| [`F45-03`](../sesiones/F45-03-cli-cierre.md) · cierre local de las dos fases | 💻 | T4.29–T4.31 (+ T9.25) | definición de hecho de [`reglas.md`](reglas.md) §4; suites contra Postgres y procesos de F9 contra el binario nuevo |

✎ **2026-10-07**: F45-01 y F45-02 figuraban como 🌐; las dos se hicieron en local (💻, D-R-8: desde el 2026-10-04 no hay
sesiones web). El «web» del nombre de sus fichas es histórico.

## Decisiones que necesita (con recomendación)

| Id | Pregunta | Recomendación | Bloquea |
|---|---|---|---|
| 🔴 **D-F4-1** | Dos candados **viejos** barren **todo** `internal/` y se ponen rojos con ficheros **nuevos**: `internal/llmvia/c2_via_test.go:117` (`WalkDir("..")`, lista exacta de ficheros que comparan por vía) y `internal/iam/infra/postgres/membresia_unica_ast_test.go:74` (`"../../.."`, lista exacta de escritores de `tenant_members`, F2). E-1 prohíbe tocarlos | **Segunda excepción a E-1, en F0** (T0.27, según `DECISIONES.md`): los dos barridos saltan los directorios nuevos de primer nivel (`internal/modulos`, `internal/nucleo`, `internal/arranque`, `internal/apipublica`, `internal/pendiente`, y `internal/candados` de F0) con **una línea** cada uno, comparando la **ruta relativa a `internal/`** (no `d.Name()`: `internal/bootstrap/arranque` también se llama `arranque` y debe seguir barriéndose); el árbol nuevo tiene su propio C2. Alternativa peor: ampliar la lista vieja fase a fase (toca código viejo 3 veces: F4, TX.13, F7). Prohibido: renombrar la variable `via` para esquivar el barrido | **F4** (y F2) |
| **D-F4-2** | El candado C2 (I-CP-3, `constitucion.md`) y el de vocabulario Go ↔ `.sql` (`degradation_test.go:170`) **no están** en la tabla de `05` §3.2, y E-7 prohíbe tests que lean código como texto salvo esos | Añadirlos a `05` §3.2: C2 → contrato de `inferencia/llmvia` (AST); vocabulario → contrato de `inferencia/degradation` (lee la `0075`) | T4.9 |
| **D-F4-3** | ¿Se parten las fases del arranque nuevo (`fase4_inferencia.go` de `04` §3) al conmutar? | **No en F4**: se sustituye dentro de las copias de `fase3_almacenes.go`, `fase5_captacion.go` y `prompts.go`; el orden de fases y el primer error visible no cambian. El reparto por módulo, en F10 o en un `refactor(arranque)` aparte | T4.24 |
| **D-F4-4** | Los dos adaptadores de transición (`turnoacotado` viejo necesita `llmvia.TurnoRequest` y `ErrViaSinTurnoAcotado` **viejos**; `reanalisis` viejo necesita `tenantllm.Config` **viejo**) | En `internal/arranque/bridge_inferencia.go` (`05` §4.2; tipos `llmConfigBridge` y `turneroBridge`), con test de equivalencia y de cableado. Mueren en F7 (`reanalisis`) y F8 (`turnoacotado`) | T4.25 |

## Contradicciones encontradas (con `04`/`05`/el encargo)

1. **No hay `embed` de plantillas en F4** (`grep -rn 'go:embed' internal cmd` → solo
   `internal/platform/storage/postgres/migrations/embed.go:15`). El texto compilado de P2–P5 está en
   `wapp-shared/llm@v0.4.5/plantilla.go`. La excepción E-3 para `embed.go` no tiene aplicación aquí.
2. **`05` §3.2 no lista el candado C2** ni el de vocabulario de `degradation` (D-F4-2).
3. **`04` §3 da `cmd/prompts/main.go` como «no se toca»**, pero importa `internal/prompts`
   (`cmd/prompts/main.go:28`): en **F10**, al borrar el paquete viejo, ese import cambia a
   `internal/modulos/inferencia/prompts`. En F4 **no** se toca (E-1): el CLI sigue con el viejo, que
   produce los mismos ficheros byte a byte (lo fija el test de ida y vuelta, R4.3.a).
4. **El comentario de paquete viejo nombra mal la variable**: `internal/prompts/prompts.go:9` y `:58`
   dicen `WAPP_PROMPTS_DIR`; el nombre efectivo es **`WAPP_LLM_PROMPTS_DIR`** (loader con prefijo
   `WAPP_` + `LLM_PROMPTS_DIR`, `internal/platform/config/config.go:826`). El contrato nuevo lo
   escribe bien; el viejo no se corrige (E-1).
5. **`04` §2.3 (sustituida) y `04` §3 suponían que F4 era «pequeña y valida el script»**: con el
   método de `05` es una fase **con conmutación delicada** (tres consumidores viejos con tipos del
   paquete viejo, [`arquitectura.md`](arquitectura.md) §4).

## Hallazgos

### F45-01 (2026-10-06/07; rama `reorg/f45-01-inventario-inferencia` desde `dev` @ `ebf4eb7`)

1. **La spec nombraba símbolos de `edge/grpc` antes de que existieran.** `ClaseInteractivo` / `ClaseLote` son
   `edgegrpc.ClassInteractive` / `ClassBatch` (`internal/modulos/edge/grpc/inference.go:281-292`) y los `Motivo*` del
   transporte son `Reason*`; el método `Motivo()` de `*InferError` sí se llama así. El código usa los reales
   (`diseno.md` §6.1).
2. **E-13 no estaba en la spec de F4 y partió dos ficheros.** `llmvia.go` (656 l. viejo) nació en `llmvia.go` (575) +
   `llmvia_turno.go` (231); `local.go` (533 l. viejo), en `local.go` (400) + `local_budget.go` (253). El árbol tiene 45
   ficheros `.go`, no los 26 de `diseno.md` §1 (los tests también se parten por tema).
3. 🟡 **E-13 choca con C2 en `llmvia.go`.** El viejo prohíbe sacar `Turno` a otro fichero porque ampliaría la lista de
   permitidos (`internal/llmvia/llmvia.go:473-477`). Resuelto sin ampliarla: la pregunta por la vía del turno vive en
   `llmvia.go` (`turnRoute`) y `llmvia_turno.go` la recibe resuelta. Coste: `llmvia.go` queda en 575 líneas, dentro
   de la tolerancia de 600 pero sobre el objetivo de 500, y no se puede partir más sin tocar la lista del C2.
4. **La vía local nueva habla con el gateway nuevo sin adaptador.** `var _ local.Frame = (*edgegrpc.Server)(nil)`
   (`llmvia/local/local_test.go:23`): cada inferencia entra en `s.infers`, que es lo que cuenta `InFlight()`
   (D-F3-13). `bridge_gateway.go` puede morir en T4.24 sin que la parada corte inferencias en vuelo. `Frame` solo
   tiene `Infer` (como el viejo, `internal/llmvia/local/local.go:270-272`); `PlazaDe` es un puerto aparte del selector.
5. **T-8 es más estrecho de lo que dice `reglas.md`.** Sin notificador el provider no se envuelve
   (`internal/llmvia/notify.go:108-111`): el observador cuenta en la selección y en el turno, **no** los fallos del
   pipeline. El contrato fija la conducta vieja.
6. **La tabla de `motivoDe` tiene 8 filas, no 14** (T4.8); el test viejo la recorre con 17 casos, que son los
   portados. `motivoDe` se llama `reasonOf` en lo nuevo (E-11).
7. 🟡 **Cuatro sub-agentes en paralelo chocan en el lint.** `golangci-lint` usa un candado global de máquina
   («parallel golangci-lint is running»): tres `make ci-local` de sub-agente dieron `GATE_RC=2` sin culpa del código
   y hubo que reintentar. Además el harness dejó **bloqueado** el *worktree* de un sub-agente ya integrado, y
   `make test-pendiente` lo cuenta (`PENDIENTES=33 · ROJOS=17` con 0 en el árbol; es el hallazgo 23 de F2 otra vez).
   El gate que cuenta lo repite el agente principal sobre su rama, a solas.
8. **Quitar la etiqueta `pendiente` enseña al lint tests que no había visto.** En el verde aparecieron `gocyclo`,
   `errorlint`, `gosec` (G304 sobre `t.TempDir`) y `ST1018` en tests escritos en el rojo. Se resolvieron sin tocar
   ninguna aserción (auxiliares extraídos y `//nolint` con motivo). Conviene pasar el lint también con la etiqueta puesta ya en el rojo (hoy no hay target para eso).
9. **Mutantes: 181 a mano, 5 vivos equivalentes.** `tenantllm/postgres.go` 53 (2 vivos: intercambiar dos guardas de
   `APIKey` que devuelven el mismo centinela; devolver `plain` junto al error de descifrado, que ya es `""`),
   `notify.go` 41 (2: quitar `err == nil`, que cae igual en el `default`), `llmvia.go` 57 (1: devolver `prov` cuando
   falla el constructor, que ya es `nil`), `llmvia_turno.go` 30 (0). Cuatro vivos no equivalentes pidieron test
   (`21e8a5a`, `c5b4b74`): `api.New` sin `Model` o sin la clave, opciones aplicadas al revés, y texto junto al error
   en `Turno`. Los dos mutantes de T-4 (copia de opciones) solo mueren con `-race`.
10. **Lo que la suite de `tenantllm` deja para F9**: `TestCheck_LaViaApiIncompletaLaRechazaPostgres`
    (`internal/tenantllm/postgres_integration_test.go:515`) es SQL crudo contra los `CHECK`, no conducta del puerto; y
    con T-16 (cero gasto) el **éxito** de `For` por la vía `api` no tiene test unitario (solo `api.New` fallando).
11. **Para F45-02**: T4.24 no lista `internal/arranque/gateway_wiring_test.go`, que exige `*gatewayBridge` por
    reflexión (`:161-173`); T4.7 ya no tiene nada que añadir a `fronteras_test.go` (`inferencia → edge` estaba);
    T4.28 parte de que `edge/grpc` importa `degradation`, y F3 lo resolvió con una lista escrita a mano
    (`internal/modulos/edge/grpc/inference_test.go:20-24`): queda decidir si pasa a importar el paquete nuevo.
    Otras del código viejo que el contrato escribe tal cual: un marcador repetido dentro de su sección de plantilla no
    da error (`internal/prompts/prompts.go:278-287`); `Volcar` no es atómico (`volcar.go:55-70`); la rama «cerrar
    filas» de `degradation.List` solo es alcanzable con un driver que deja otro conjunto de resultados pendiente.

### F45-02 (2026-10-07; rama `reorg/f45-02-conmutar-inferencia-catalogo` desde `dev` @ `3c74b80`)

12. **T4.24 no es separable de TX.14.** Al cambiar los tipos del contenedor, `internal/arranque/fase8_transporte.go`
    (los campos `TenantLLM` y `DegradationNotices` de `publicapi.Deps`) deja de compilar, y ponerlos a `nil` sin montar
    F1–F4 en la cara nueva rompe `TestHuella`. Se fundieron en `98b24c5`, con `FaseActual = 4` (lo exige `mudanzas.go`:
    la fase sube en el commit que monta las rutas). La lista de ficheros de T4.24 omite `fase8_transporte.go`, `http.go`
    y `mudanzas*.go`.
13. **T4.28 quedó sin contenido propio.** `internal/modulos/edge/grpc/inference_test.go` ya no importa `degradation`
    (vocabulario escrito a mano); no se crea la arista `edge → inferencia`; el `grep` de su «Hecho cuando» solo casa la
    entrada del `Mapa` en `fronteras_test.go`. Su `FaseActual = 4` viajó en `98b24c5`.
14. **`gateway_wiring_test.go` no estaba en la lista de T4.24** y se rompía al borrar `bridge_gateway.go` (exigía
    `*gatewayBridge` por reflexión; lo avisaba el hallazgo 11): reescrito en `98b24c5`; añade el campo `router` del
    selector al test de identidad.
15. 🟡 **`edge` no entra en `Conmutados`** (decisión (a) de Jhoan, 2026-10-07: entraba en T4.24 **salvo** que obligara a
    tocar aserciones de `session_identity_test.go`; la salvedad se cumplió). `internal/arranque/session_identity_test.go:7`
    importa `internal/gateway/session` viejo y 3 de sus 4 aserciones nombran el centinela viejo `ErrSessionOffline`: la
    regla 3 lo pondría rojo. `Conmutados` sigue `{"acceso"}`. El comentario de `internal/modulos/fronteras_test.go`
    (antes «edge no entra hasta F4») se actualizó. Contradice [`../F3-edge/reglas.md`](../F3-edge/reglas.md) §4.7
    (`:83-85`) — corregido el 2026-10-07 (nota fechada en ese §4.7). **Pendiente de Jhoan**: qué se hace con ese test
    (muere, se mueve o excepción).
16. **Forma del adaptador.** `llmConfigBridge` guarda una interfaz mínima no exportada (`llmConfigReader`, solo `Get`)
    para probarse sin BD; `inference_wiring_test.go` afirma por reflexión que lo cableado es el `*tenantllm.Postgres`
    nuevo. `turneroBridge` guarda el `*llmvia.Selector` concreto. El centinela se traduce reutilizando `bridgeError` de
    `bridge_contact.go`. Dos alias de import en el adaptador (`legacyllm`, `legacytenantllm`).
17. **Test de cableado completo** en `internal/arranque/inference_wiring_test.go` (nombres `TestCableado_…` /
    `TestIdentidad_…`, seleccionables por el gate estrecho). Comprobado por mutación a mano por el orquestador: turnero
    sin selector, adaptador de config sin almacén, selector sin `WithFrame` y `FaseActual` a 3 dan rojo las cuatro
    (`TestIdentidad_TheBridgesWrapTheContainerInstances`, `TestIdentidad_EveryConsumerSharesTheOneSelector`,
    `TestTurnoAcotadoCableado`, `TestIdentidad_EveryConsumerSharesTheOneGateway`, `TestMudanzas_FaseActual`,
    `TestMudanzas_HuellaPorElCompuesto`).
18. **`internal/arranque/prompts.go` no tenía test** (nace `prompts_test.go` en `0e532e4`). `prompts.Volcar` escribe en
    el preámbulo el texto `"package_size": 0` (`internal/modulos/inferencia/prompts/volcar.go:138`): para fabricar la
    plantilla inválida se reemplaza `"package_size": 30`.
19. **De los tests de cableado copiados (T4.27) solo cambió `turno_acotado_cableado_test.go`.**
    `pipeline_captacion_cableado_test.go:156` es la única aserción copiada sobre el selector compartido y es por nombre;
    la identidad real la cubre ahora `inference_wiring_test.go`.
20. **El `grep` literal de [`reglas.md`](reglas.md) §4.7 casa también constantes de texto en tests**: en
    `inference_wiring_test.go` las rutas viejas se componen con un prefijo.
21. **`apipublica`**: `tenantllm.go` nace partido (`tenantllm_body.go` + gemelo) por E-13; entrada nueva en el C2
    (`internal/modulos/inferencia/llmvia/c2_via_test.go`); primer uso de `entitlements.RequireFeature` en la cara nueva
    (el resolver llega por `Deps`, no por `Common`); orden de cadena reproducido: Authenticate → RequirePermission →
    (escrituras) auditoría → gate de feature → handler, así que en PUT/DELETE el 403 del gate deja registro de auditoría
    `failure` y en los GET no. `upsertDesde` → `upsertFrom` (E-11).
22. 🟡 **Conservado del viejo y fijado por test, candidato a decisión**: el plazo vencido en
    `GET /degradation-notices` responde 500, no 504 (`internal/publicapi/degradationnotices.go:150-153`);
    PUT/DELETE/relectura de `tenant-llm` sin plazo de BD (`internal/publicapi/tenantllm.go:188,252,259,319`).
23. **No portado en `apipublica` (ramas inalcanzables)**: `ts == nil ⇒ 500` (`publicapi/tenantllm.go:184,237,315`),
    `lister == nil ⇒ 500` (`degradationnotices.go:142`), `if offset < 0` (`:191-193`).
24. **Restos en el arranque.** `internal/arranque/fase8_transporte.go:1-3`: la cabecera sigue diciendo «salvo acceso y
    edge» (no actualizada para inferencia) — corregido el 2026-10-07. `internal/arranque/http.go:47`: `buildPublicAPIServer` va por 9 parámetros,
    uno más por fase que muda rutas: conviene un struct antes de F6–F8.
25. **Lint.** `make lint` no ve los tests tras `//go:build pendiente`: los avisos aparecen al quitar la etiqueta
    (hallazgo 8). Para verlos en rojo: `.bin/golangci-lint run --build-tags pendiente` (solo informativo). Y el candado
    global de `golangci-lint` dio un `rc=2` espurio («parallel golangci-lint is running») con sub-agentes en paralelo:
    reintentar (hallazgo 7).
26. **Huella con `FaseActual = 4` y `5`: idéntica sin tocar la dorada**; perfiles `minimo` y `con-m2m`:
    `:8100=22 :8103=73 rpc=2 metricas=11`. La cara nueva sirve 33 rutas (23 de acceso + 6 de edge + 4 de inferencia).

**Decisiones de Jhoan en la sesión (2026-10-07)**: (a) `edge` y `Conmutados`, hallazgo 15; (b) `catalogo` no entra en
`Conmutados` hasta F7 y (c) D-F5-2 sin ampliar el motor de fronteras, en el [README de F5](../F5-catalogo/README.md).
Las tres, en [`DECISIONES.md`](../DECISIONES.md) §5 y §7.
