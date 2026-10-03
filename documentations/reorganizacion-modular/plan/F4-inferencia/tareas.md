# F4 · Tareas

> Formato y estados: [`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3.
> «**Gate ci-local**» = `GOWORK=off make ci-local > /tmp/g.log 2>&1; echo GATE_RC=$? >> /tmp/g.log; tail -1 /tmp/g.log` → `GATE_RC=0`
> (skill `validar-antes-de-cerrar`). «**Gate rojo**» = `GOWORK=off go vet -tags pendiente ./internal/modulos/inferencia/...; echo rc=$?` → `rc=0`
> **y** el test nuevo, corrido solo con `-tags pendiente -run '^TestX$'`, da rc≠0.
> «**Gate verde**» = `GOWORK=off go test -race ./<paquete>/; echo rc=$?` → `rc=0` + gate ci-local. `make cobertura-ficheros` es **informe**: la tabla va al PR; no bloquea (P2).
> Skills: `reconstruir-modulo` (la fase) y `contrato-tdd` (cada fichero).
>
> **Nivel de ceremonia** (`05` E-12): el del inventario aprobado en T4.1. En nivel **simple**, la tarea de rojo y la de
> verde del mismo archivo se hacen en una pasada y se marcan las dos; en **medio**, rojo y verde por archivo agrupados
> por paquete; en **complejo**, el esquema completo con mutantes. Sin umbral de cobertura: un test por promesa del
> contrato; mutantes en el nivel complejo; procesos de F9.
> **Auxiliares no exportados** (`05` E-4): su test nace en el verde y solo si llevan regla de negocio o ramas no triviales.
> **Adaptador** (E-11, `05` §4.2): `puente inferencia` → `bridge_inferencia.go` · `puenteTurnero` → `turneroBridge` ·
> `puenteConfigLLM` → `llmConfigBridge`. Los puentes de **import** (`05` §4.1) siguen llamándose «puente».
> **Sesiones**: F4 y F5 comparten las tres fichas `F45-*` de [`../sesiones/`](../sesiones/README.md).

## Sesión F45-01 · inventario E-12 y F4 entero en verde · 🌐 · T4.1–T4.9, T4.11–T4.23
Para cuando: Jhoan aprobó el inventario E-12; `internal/modulos/inferencia` entero sin etiqueta; C2 nuevo y viejo verdes; gate ci-local rc=0 con 0 SKIP.

### Inventario y candados viejos · T4.1–T4.2

- [ ] **T4.1 · docs: Inventario E-12 de F4** · 🌐 · dep. F3 cerrado · cumple —
  - **Ficheros**: este `tareas.md` y `diseno.md` §1 y §6 (los números, solo si cambiaron)
  - **Produce**: (1) los números reconfirmados sobre `dev`: `wc -l internal/{llmvia,llmvia/local,prompts,tenantllm,degradation}/*.go | grep -v _test` suma 3.072 en 10 ficheros (o se corrige aquí con fecha); `grep -c '^func Test'` sobre los 14 tests viejos suma 88; `GOWORK=off go list -f '{{.Imports}}'` de los 5 paquetes coincide con `arquitectura.md` §2; existe el adaptador de `local.Frame` de F3 (`grep -rln 'InferRequest' internal/arranque`). (2) la tabla `archivo · estado en memoria · concurrencia · BD/transacciones · nº de consumidores · nivel (simple/medio/complejo)` de los 10 ficheros, sus dobles y el adaptador. (3) la lista de adaptadores: **nace** `bridge_inferencia.go` (`llmConfigBridge`, `turneroBridge`); **se retira** `bridge_gateway.go` de F3
  - **Hecho cuando**: **Jhoan aprueba la tabla. Antes de eso no se escribe código.** Si un archivo sale peor, sube de nivel. Se presenta junto al inventario de F5 (T5.1)
  - **Gate**: ninguno de código · **Commit**: `docs(reorganizacion-modular): F4, inventario E-12`
- [ ] **T4.2 · verificar que los candados viejos están ciegos al árbol nuevo (D-F4-1, hecho en F0)** · 🌐 · dep. T4.1 · cumple R4.2.c
  - **Ficheros**: ninguno (solo lectura). La línea la puso **F0 · T0.27** en `internal/llmvia/c2_via_test.go` (`:117`) y en `internal/iam/infra/postgres/membresia_unica_ast_test.go` (`:97`); F4 **no** vuelve a tocar código viejo.
  - **Hecho cuando**: `git log --oneline origin/dev -- internal/llmvia/c2_via_test.go` muestra el commit de T0.27; los dos barridos saltan `modulos`, `nucleo`, `arranque`, `apipublica`, `pendiente`, `candados` **directamente bajo `internal/`** (por ruta, no por nombre) y siguen leyendo > 0 ficheros viejos; una sonda en el árbol, sin commitear (`internal/modulos/x/x.go` con `if via == "api"`), **no** los pone rojos y se borra.
  - **Gate**: gate ci-local · **Commit**: ninguno
  - Si falta (D-F4-1 = no, o F0 no la hizo): 🛑 parada; F4 no puede llegar al primer verde (T-1 de `reglas.md`) y se vuelve a Jhoan con la alternativa (ampliar la lista del C2 viejo en F4, TX.13 y F7)

### Contratos y rojo · T4.3–T4.9
Para cuando: `make test-pendiente` cuenta **≈54** llamadas en `internal/modulos/inferencia` (estimación previa al inventario, que incluía el adaptador; la cifra exacta se anota aquí al cerrar T4.9), gate rojo rc=0, gate ci-local rc=0, `make lint` sin `unused`.

- [ ] **T4.3 · rojo(inferencia): contratos de `prompts/prompts.go` y `prompts/volcar.go`** · 🌐 · dep. T4.1 · cumple R4.1.a–e, R4.3.a, R4.3.b
  - **Ficheros**: `internal/modulos/inferencia/prompts/{prompts,volcar}.go` y sus `_test.go`
  - **Hecho cuando**: los 13 exportados de `diseno.md` §2 existen con su comentario-promesa; `prompts_test.go` tiene la tabla de los 5 fallos de arranque con sus textos, «sin directorio ⇒ compiladas», «una etapa suelta», «ignora lo que no es plantilla», `P4-x.TMPL` carga (T-12), preámbulo fuera; `volcar_test.go` tiene ida y vuelta byte a byte sobre las 4 etapas contra `llm.PlantillaPorDefecto`, «no pisa», dir vacío; cabecera `// Porta internal/prompts/<f>.go @ <sha>`
  - **Gate**: gate rojo · **Commit**: `rojo(inferencia): contrato de prompts`
- [ ] **T4.4 · rojo(inferencia): contrato de `degradation/degradation.go`** · 🌐 · dep. T4.1 · cumple R4.5.a–c
  - **Ficheros**: `…/degradation/degradation.go`, `degradation_test.go`
  - **Hecho cuando**: vocabulario, errores, `Notice`, `ListFilter`, `Store`, `Notifier`, `VentanaDe`; el test lee `../../../platform/storage/postgres/migrations/structure/*.sql`, extrae la lista del `CHECK owner_degradation_notices_reason_check` y la compara como **conjunto** con `Reasons()` (candado D-F4-2), afirma `ViaLocal/ViaAPI` iguales a los de `tenantllm` **nuevo**, `Reasons()` es copia, `VentanaDe` pura (dos TZ ⇒ misma clave), motivo sano ⇒ `saves == 0` (usa `degradationhelpertest.Memoria`)
  - **Gate**: gate rojo · **Commit**: `rojo(inferencia): contrato de degradation`
- [ ] **T4.5 · rojo(inferencia): `degradation/postgres.go` y el doble `degradationhelpertest`** · 🌐 · dep. T4.4 · cumple R4.5.c
  - **Ficheros**: `…/degradation/postgres.go`, `postgres_test.go`, `…/degradationhelpertest/{contrato,memoria,memoria_test}.go`
  - **Hecho cuando**: `Contrato(t, func(t) Montaje)` escrita **entera** (es especificación: dedupe, ventana siguiente, `creado`, INV-7, orden y `[]`), con la marca de estado sobre todas las columnas que `Save` puede tocar; `memoria_test.go` la corre (la pasada contra Postgres, T4.31); `postgres_test.go` prueba las funciones puras de `diseno.md` §2 (acotar, `LastSeenAt` cero, `NULL read_at`) y el constructor
  - **Gate**: gate rojo · **Commit**: `rojo(inferencia): contrato de degradation/postgres y su doble`
- [ ] **T4.6 · rojo(inferencia): `tenantllm` (dominio, adaptador y doble)** · 🌐 · dep. T4.1 · cumple R4.4.a–e
  - **Ficheros**: `…/tenantllm/{tenantllm,postgres}.go` y tests, `…/tenantllmhelpertest/{contrato,memoria,memoria_test}.go`
  - **Hecho cuando**: `Config` sin campo de clave (test por reflexión); `Contrato(t, func(t) Montaje)` con los casos de conducta de los 15 tests de integración viejos salvo `TestBackfill0073_*` (van a F9); `postgres_test.go` prueba la validación previa al SQL con sus tres textos exactos
  - **Gate**: gate rojo · **Commit**: `rojo(inferencia): contrato de tenantllm y su doble`
- [ ] **T4.7 · rojo(inferencia): `llmvia/local`** · 🌐 · dep. T4.1 · cumple R4.6.a, R4.6.b, R4.6.d
  - **Ficheros**: `…/llmvia/local/{local,calentamiento}.go` y tests; `internal/modulos/fronteras_test.go` (lista blanca: `inferencia → edge`; **sin** puentes de import)
  - **Hecho cuando**: `Frame` sobre `modulos/edge/grpc.InferRequest`; tests con un `frameFalso` que guarda la última petición: los 5 métodos usan el `Build…Prompt` compartido (y el `…Con` con plantilla), `ExtractJSON`, error del transporte intacto (`errors.As` sobre un tipo con `Motivo()`), temperatura del llamante, plazo heredado (`D − 7 s`, por rango), sin deadline 30 s, sin presupuesto ⇒ `ErrSinPresupuesto` y **cero** llamadas al frame, tabla de techos y `class`, techo apagado ⇒ 0, `MargenVeredicto > DefaultInferGrace`; calentamiento: `"hola"`, 16, `lote`, `Warmup`, no interpreta la salida, mismo prefijo que la P1 real
  - **Gate**: gate rojo · **Commit**: `rojo(inferencia): contrato de llmvia/local`
- [ ] **T4.8 · rojo(inferencia): `llmvia/notify.go`** · 🌐 · dep. T4.4, T4.6, T4.7 · cumple R4.5.d, R4.5.e
  - **Ficheros**: `…/llmvia/notify.go`, `notify_test.go` (paquete `llmvia` interno: `motivoDe` nace en verde y su test también —lleva regla de negocio, T-6—; en rojo el test cubre los 5 exportados y la tabla por la conducta de `For` con un frame que falla)
  - **Hecho cuando**: la tabla de `diseno.md` §2 entera (14 filas) expresada como casos; «el observador cuenta aunque no haya notificador»; «lo que no tiene motivo no se cuenta»; «el aviso sobrevive al contexto muerto»
  - **Gate**: gate rojo · **Commit**: `rojo(inferencia): contrato de llmvia/notify`
- [ ] **T4.9 · rojo(inferencia): `llmvia/llmvia.go` y el candado C2** · 🌐 · dep. T4.8 · cumple R4.2.a, R4.2.b, R4.4.b, R4.6.c
  - **Ficheros**: `…/llmvia/llmvia.go`, `llmvia_test.go`, `c2_via_test.go` (con `//go:build pendiente` hasta T4.23)
  - **Hecho cuando**: tests de `For` (tres estados de fila; vía inventada; fallo de `Get` ≠ vía; sesión de origen en el frame, vacía viaja vacía, **se añade** a las opciones; credencial rota avisa al construir; éxito no avisa; calidad no avisa; sin notificador no envuelve; `NewSelector(nil)`), `WithLocalOptions` acumula (dos llamadas llegan juntas), `Warm` (api ⇒ centinela sin cable; no avisa), `PlazaDe` (5 casos de `plaza_test.go`), `Turno` (parámetros medidos, ctx de 19 s no se queda sin presupuesto, api sin cable, fallo avisa y cuenta `turno`, sin cable ⇒ `local.ErrSinTransporte`), carrera de dos `For` concurrentes con `-race`; C2 con la lista de permitidos de `diseno.md` §3 **vacía de las entradas futuras** (solo las del módulo)
  - **Gate**: gate rojo · **Commit**: `rojo(inferencia): contrato de llmvia y candado C2`
### Verde de las hojas · T4.11–T4.18
Para cuando: 8 ficheros sin etiqueta, un test por promesa de su contrato, gate ci-local rc=0 **con el C2 viejo en verde**.

Un commit por fichero (por paquete en nivel medio), `verde(inferencia): <fichero>`; se quita `//go:build pendiente` de **su**
test; el comentario-ADR del fichero viejo viaja con la lógica (E-10). Dentro de un paquete, en serie.

- [ ] **T4.11 · verde: `prompts/prompts.go`** · 🌐 · dep. T4.3 · cumple R4.3.b, R4.3.e
- [ ] **T4.12 · verde: `prompts/volcar.go`** · 🌐 · dep. T4.11 · cumple R4.3.a
- [ ] **T4.13 · verde: `degradation/degradation.go`** · 🌐 · dep. T4.4, **T4.2** · cumple R4.5.a–c — primera comparación por vía del árbol nuevo (`ValidVia`): si el C2 viejo se pone rojo, **T4.2 no está hecha**
- [ ] **T4.14 · verde: `degradationhelpertest/memoria.go`** · 🌐 · dep. T4.13
- [ ] **T4.15 · verde: `degradation/postgres.go`** · 🌐 · dep. T4.13 · SQL copiado literal; su verdad la da la suite contra Postgres (P4, T4.31) y F9
- [ ] **T4.16 · verde: `tenantllm/tenantllm.go`** · 🌐 · dep. T4.6, T4.2 · cumple R4.4.a
- [ ] **T4.17 · verde: `tenantllmhelpertest/memoria.go`** · 🌐 · dep. T4.16 · cumple R4.4.b–e
- [ ] **T4.18 · verde: `tenantllm/postgres.go`** · 🌐 · dep. T4.16 · SQL literal; su verdad la da la suite contra Postgres (P4, T4.31) y F9
  - **Gate de las ocho**: gate verde · **Commit**: `verde(inferencia): <fichero>`

### Verde de `llmvia` y el candado C2 · T4.19–T4.23
Para cuando: `internal/modulos/inferencia` entero sin etiqueta; C2 nuevo en verde; gate ci-local rc=0.

- [ ] **T4.19 · verde: `llmvia/local/local.go`** · 🌐 · dep. T4.7 · cumple R4.6.a, R4.6.b, R4.6.d
- [ ] **T4.20 · verde: `llmvia/local/calentamiento.go`** · 🌐 · dep. T4.19
- [ ] **T4.21 · verde: `llmvia/notify.go`** · 🌐 · dep. T4.8, T4.13, T4.16 · cumple R4.5.d, R4.5.e — mutantes sobre el orden de ramas de `motivoDe` (T-6), si el inventario lo deja en complejo
- [ ] **T4.22 · verde: `llmvia/llmvia.go`** · 🌐 · dep. T4.19–T4.21 · cumple R4.2.b, R4.4.b, R4.6.c
- [ ] **T4.23 · verde: candado C2 nuevo** · 🌐 · dep. T4.22, T4.13, T4.16–T4.18 · cumple R4.2.a
  - **Ficheros**: `…/llmvia/c2_via_test.go`
  - **Hecho cuando**: sin etiqueta; lista = los 5 permitidos del módulo; barre `internal/{modulos,nucleo,arranque,apipublica}` y afirma `recorridos > 0`; mutación documentada en el commit (un `if x.Via == "api"` en `…/prompts/prompts.go` ⇒ rojo; revertida)
  - **Gate**: gate ci-local · **Commit**: `verde(inferencia): candado C2`

## Sesión F45-02 · adaptador, conmutar y mudar las 4 rutas · 🌐 · T4.10, T4.24–T4.28 (+ TX.12–TX.14)
Para cuando: la huella del arranque nuevo es idéntica a la del viejo con `FaseActual = 4`; el test de cableado de `bridge_inferencia.go` verde; gate ci-local rc=0. (La misma sesión sigue con F5 entero.)

- [ ] **T4.10 · arranque: `bridge_inferencia.go` y su test de traducción** · 🌐 · dep. T4.18, T4.22 · cumple R4.7.c
  - **Ficheros**: `internal/arranque/bridge_inferencia.go`, `bridge_inferencia_test.go`
  - **Nivel**: simple (`05` §4.2): contrato, test y lógica en una pasada, sin estado
  - **Hecho cuando**: `var _ turnoacotado.Turnero = (*turneroBridge)(nil)` y `var _ reanalisis.ConfigLLM = (*llmConfigBridge)(nil)` compilan; el test afirma la traducción del centinela y la conversión campo a campo de `Config`
  - **Gate**: gate verde · **Commit**: `verde(arranque): bridge_inferencia`
- [ ] **T4.24 · conmutar(inferencia): el arranque nuevo construye los paquetes nuevos** · 🌐 · dep. T4.23, T4.10 · cumple R4.7.a, R4.7.b
  - **Ficheros**: en `internal/arranque/`: la copia de `fase3_almacenes.go` (stores nuevos), `prompts.go` (cargador nuevo), `fase5_captacion.go` (`construirSelectorDeVia` con el paquete nuevo y `c.gw` directo), `contenedor.go` (tipos de los campos), y **se borra** el adaptador de `local.Frame` de F3 (`bridge_gateway.go`) con su test
  - **Hecho cuando**: `arquitectura.md` §6 aplicado tal cual (orden de fases y `requiere()` sin cambio, D-F4-3); `grep -rn 'internal/llmvia\b\|internal/tenantllm"\|internal/degradation"\|internal/prompts"' internal/arranque --include='*.go'` solo en `bridge_inferencia*.go`
  - **Gate**: gate ci-local + huella · **Commit**: `conmutar(inferencia): el arranque nuevo cablea inferencia`
- [ ] **T4.25 · verde(arranque): cableado de `bridge_inferencia.go` y su test de cableado** · 🌐 · dep. T4.24 · cumple R4.7.b, R4.7.c
  - **Hecho cuando**: `turnoacotado.New(&turneroBridge{…})` y `reanalisis.NewServicio(…, &llmConfigBridge{…})` en la copia de `fase5_captacion.go`; el **test de cableado completo** (hallazgo 39) afirma que el arranque construye el selector y los almacenes **nuevos** *y* que ninguna fase importa `internal/{llmvia,tenantllm,degradation,prompts}` viejos fuera del adaptador (grep por ruta de import), no solo el campo del contenedor
  - **Gate**: gate verde · **Commit**: `verde(arranque): cableado de bridge_inferencia`
- [ ] **T4.26 · test del arranque: una plantilla inválida aborta y el log dice el origen** · 🌐 · dep. T4.24 · cumple R4.3.c, R4.3.d
  - **Hecho cuando**: el test de la copia de `prompts.go` monta un dir con `"package_size": 0` y afirma error con prefijo `prompts ajustables de P2-P5: ` que envuelve `prompts.ErrPromptsDir` nuevo; con dir válido, la línea `prompts: plantillas de las etapas ajustables` trae `p2…p5`
  - **Gate**: gate ci-local · **Commit**: `verde(arranque): prompts aborta el arranque`
- [ ] **T4.27 · los tests de cableado copiados apuntan al selector nuevo** · 🌐 · dep. T4.25 · cumple R4.7.b
  - **Ficheros**: las copias de F0 de `turno_acotado_cableado_test.go`, `calentamiento_cableado_test.go`, `quotetext_cableado_test.go`, `pipeline_captacion_cableado_test.go` (solo lo que nombra tipos o paquetes de inferencia)
  - **Hecho cuando**: siguen afirmando **un** selector compartido por etapas, aforo, `quotetext`, `intakeahead` y el turno; verdes
  - **Gate**: gate ci-local · **Commit**: `conmutar(inferencia): cableado del selector en los candados del arranque`
- [ ] **TX.12–TX.14** de [`../FX-cara-http/tareas.md`](../FX-cara-http/tareas.md): `apipublica/tenantllm.go` y `degradationnotices.go` (rojo, verde, conmutar las filas F1–F4). **En el verde de `apipublica/tenantllm.go`** se añade su entrada a la lista del C2 nuevo (compara por vía: `publicapi/tenantllm.go` es permitido hoy). Con D-F4-1 aplicada, el C2 viejo no lo ve.
- [ ] **T4.28 · el test de vocabulario de `edge/grpc` apunta al `degradation` nuevo; `FaseActual = 4`** · 🌐 · dep. TX.14 · cumple R4.7.a, R4.7.e
  - **Ficheros**: el test de F3 equivalente a `internal/gateway/grpc/inference_vocabulario_internal_test.go` (si importa el `degradation` viejo), `internal/arranque` (`FaseActual`)
  - **Hecho cuando**: `grep -rn 'internal/degradation"' internal/modulos` vacío; huella idéntica
  - **Gate**: gate ci-local + huella · **Commit**: `conmutar(inferencia): huella y vocabulario del transporte`

## Sesión F45-03 · cierre local · 💻 · T4.29–T4.31 (+ T9.25)
Para cuando: la definición de hecho de [`reglas.md`](reglas.md) §4 se cumple entera. (La misma sesión cierra F5.)

- [ ] **T4.29 · validar-antes-de-cerrar** · 💻 · dep. T4.28, T4.31
  - **Hecho cuando**: los 11 puntos de `reglas.md` §4, con números (informe de la skill)
  - **Commit**: ninguno (o `refactor(inferencia): …` si la limpieza con tests en verde lo pide)
- [ ] **T4.30 · docs: `ESTADO.md` y este README** · 💻 · dep. T4.29
  - **Hecho cuando**: estado «cerrada» (con `inferencia` **fuera** de `Conmutados` hasta F8, `reglas.md` §4.11), SHA por tarea, hallazgos nuevos en el README, cifra de coste (ficheros, commits, horas de sesión) para medir el ahorro de E-12
  - **Commit**: `docs(reorganizacion-modular): F4 cerrada`
- [ ] **T4.31 · suites contra Postgres (= T9.25, 9C de `inferencia`)** · 💻 · dep. T4.28
  - **Hecho cuando**: en `test/procesos/` están las dos pasadas (`tenantllmhelpertest.Contrato` y `degradationhelpertest.Contrato` con un `Montaje` sobre `NewPostgres` y la base clonada del arnés); `go vet -tags integracion ./test/procesos/...` rc=0; `make test-procesos` (testcontainers, `postgres:17-alpine`) contra el binario **nuevo**, contando PASS/FAIL/SKIP con `-v` (0 SKIP). Traspaso (skill `traspaso-web-local`) solo mientras existan los dos entornos, o si la sesión se corta
  - **Commit**: `procesos(inferencia): suites de tenantllm y degradation contra Postgres`
