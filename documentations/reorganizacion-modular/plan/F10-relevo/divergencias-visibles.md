# F10 · Inventario de las divergencias visibles desde fuera (T10.24, primera mitad de la deuda D-36)

> **Qué es.** La lista de las conductas en que el binario nuevo (`cmd/server-modular`) responde distinto del viejo
> (`cmd/server`) **a propósito**, separadas en **internas** y **visibles desde fuera**, con lo que respondía el viejo
> y lo que responde el nuevo. Es un inventario: no arregla nada ni abre ningún repo consumidor. La segunda mitad de
> [D-36](../../../deuda.md) —mirar cómo trata cada consumidor la respuesta nueva— va después del relevo.
>
> **Fecha**: 2026-10-11 · **Sobre**: `bf2d0ae9` (rama `reorg/f10-02-dorada`; el árbol de trabajo tenía cambios sin
> commitear solo en `internal/arranque/huellatest/`, que no contienen ninguna de las frases contadas).
>
> **Cómo se comprobó.** Cada visible se leyó en el código viejo y en el nuevo; **no se ejecutó ninguno de los dos
> binarios**. Lo que quedó sin verificar lo dice §8.

## 1 · Recuento, con su regla

| Regla | Comando | Resultado |
|---|---|---|
| **R1** · la del encargo: la frase «Divergencia deliberada», producción | `grep -rn 'Divergencia deliberada' --include='*.go' internal cmd test \| grep -v _test.go \| wc -l` | **27 líneas en 14 ficheros** (más 1 línea en un `_test.go`: `internal/modulos/conversacion/runtime/runtime_engine_options_test.go:5`) |
| **R1b** · la de D-36 el 2026-10-10: la frase con «del viejo» | el mismo, con `'Divergencia deliberada del viejo'` | 26 líneas en 14 ficheros |
| **R2** · otras fórmulas, solo árbol nuevo y sin `_test.go` ni paquetes `…helpertest` | `grep -rnE` sobre `internal/apipublica internal/arranque internal/modulos internal/nucleo cmd/server-modular` de: «se aparta», «divergencia con/consciente/deliberada», «diverge del», «el viejo / la (cara) vieja» + verbo en imperfecto o en presente, «a diferencia del viejo», «respecto del viejo», «no se porta», «no está en el viejo» | 155 líneas candidatas leídas una a una (dos pasadas, de 101 y 56 líneas, con 2 repetidas); de ellas salen **19 divergencias de conducta** que R1 no cuenta (§3, O1–O19) |

**La línea 27.** No es un comentario nuevo: es `internal/modulos/conversacion/store/repository_memory_intakes.go:14`,
que dice «Divergencia deliberada **con** el viejo» y la frase de D-36 («…del viejo») no la casaba. Con R1b siguen
siendo 26 en 14.

**Lo que R1 no ve.** Las 27 líneas están todas en `conversacion` y `captacion`. Los módulos `acceso`, `edge` y
`solicitudes` y la cara `apipublica` escriben sus divergencias con otras fórmulas («se APARTA del viejo», «SE APARTA
de la vieja a propósito», «⚠️ DIVERGENCIAS con la cara vieja», «el viejo no lo miraba»): **diez de las once
visibles de §5 no llevan la frase de R1** (`casebank` la escribe en minúsculas o en mayúsculas, y el `grep` distingue).

**Regla de clasificación.**

- **Visible**: para la misma petición o el mismo hecho, cambia el código de estado, el cuerpo o un campo de una
  respuesta HTTP, o qué tramas CloudLink recibe un Edge o cuándo, o la salida de un CLI.
- **Interna**: no cambia ningún contrato. Se subdivide para la segunda mitad:
  - **interna · datos** — el contrato es el mismo pero cambia qué filas acaban existiendo (una fila que ya no se
    duplica, un mensaje que antes se perdía), y eso se lee después por una ruta;
  - **interna · log** — solo cambia una línea del log del proceso (`cloud.log`), que no es la auditoría;
  - **interna** a secas — orden de pasos, dobles en memoria, guardas contra nil.

**Total: 33 entradas**, contando cada hecho una vez aunque lo nombren un comentario y una decisión: 13 de §2
(C1–C13), 19 de §3 (O1–O19) y 1 que solo está en `DECISIONES.md` (D-F6-8). **11 visibles** (V1–V11: C1 y O1–O10)
**y 22 internas** (6 «datos», 3 «log» y 13 a secas).

## 2 · Los 27 comentarios «Divergencia deliberada» (R1), agrupados por hecho

Las 27 líneas describen 13 hechos. Todas las rutas son de `internal/modulos/`.

| # | `fichero:línea` | Qué dice | Clase |
|---|---|---|---|
| C1 | `captacion/reanalisis/reanalisis.go:174`, `:229`, `:328`, `:370`, `:415` | El re-análisis compone el sobre **antes** de abrir el job; si componer falla no se abre job y la petición devuelve error (D-F7-9, D-F8-13, T8.40) | **visible → V1** |
| C2 | `captacion/intake/reanalysis.go:25`, `captacion/intake/postgres_reanalysis.go:90`, `captacion/intake/intakehelpertest/reanalysis_memory.go:43` | `OpenReanalysis` inserta el job con su sobre en una sentencia | interna (es el mecanismo de C1) |
| C3 | `conversacion/runtime/aggregator_sweep.go:297`, `captacion/intake/store.go:201`, `captacion/intake/postgres.go:141` | El barrido compone y cierra la ventana con el sobre en una sentencia (`CloseWithSourceText`); el viejo cerraba y luego escribía el sobre | interna · datos (I-d1) |
| C4 | `conversacion/runtime/aggregator_observe.go:87`, `:128` | Si `OpenOrAppend` falla, el `wa_message_id` se desanota y la re-entrega vuelve a intentarlo (D-F8-14) | interna · datos (I-d2) |
| C5 | `conversacion/runtime/resume.go:84`, `:390`, `conversacion/runtime/persist_sink.go:311` | El reintento del sink durable repite solo la proyección si fue la proyección lo que falló (D-F8-15) | interna · datos (I-d3) |
| C6 | `conversacion/runtime/aggregator_sweep.go:161` | Con el contexto cancelado, el barrido no registra a `ERROR` sus fallos (D-F9-10) | interna · log (I-l1) |
| C7 | `conversacion/runtime/runtime_engine.go:291`, `:442` | `WithEventSink(nil)` y `WithResumePolicy(tipo, nil)` se ignoran (D-F8-16) | interna |
| C8 | `conversacion/store/repository_memory_intakes.go:14`, `:34` | El gemelo en memoria elige, entre varias `open`, la más reciente (D-F8-7) | interna (doble de test: `internal/arranque` no construye ningún almacén en memoria) |
| C9 | `conversacion/store/repository_memory_intakes.go:59` | El gemelo devuelve `CustomerNote ""` en las lecturas, como Postgres (D-F8-7) | interna (doble) |
| C10 | `conversacion/store/repository_memory_intakes.go:100`, `:106` | El gemelo no escribe la nota en `UpsertIntake` (D-F8-7) | interna (doble) |
| C11 | `conversacion/store/repository_memory_intakes.go:246` | El gemelo rechaza una segunda línea `_shipping` (D-F8-8) | interna (doble) |
| C12 | `conversacion/store/repository_postgres.go:393`, `conversacion/store/store.go:131` | `ListResults` de Postgres lee `event_id` (D-F8-8) | interna (el único consumidor, `conversacion/runtime/summary_sources.go`, no usa el campo, según D-F8-8) |
| C13 | `captacion/reanalisis/reanalisis.go:370` (misma línea que C1) | Desaparece el `Error` «reanalisis: el job quedó abierto pero SIN literal; el worker lo matará al reclamarlo» | interna · log (I-l2) |

Comprobación comentario ↔ código viejo de las internas de esta tabla: C4, `internal/flujos/runtime/aggregator.go:511-525`
(no desanota); C5, `internal/flujos/runtime/resume.go:325` (`sink.Handle` siempre); C3, `internal/flujos/runtime/aggregator.go:892`
y `:906` (cierra y después compone); C6, `internal/flujos/runtime/aggregator.go:741` y `:894` (registra sin mirar el
contexto); C7, `internal/flujos/runtime/runtime_engine.go:292` y `:447` (guarda el nil). Coinciden.

## 3 · Divergencias escritas con otra fórmula (R2)

Rutas relativas a `internal/`.

| # | `fichero:línea` | Qué dice | Clase |
|---|---|---|---|
| O1 | `modulos/acceso/iam/domain/invitation.go:91` | El hash del token de invitación recorta U+200B y U+FEFF de los bordes (D-F2-11) | **visible → V2** |
| O2 | `modulos/acceso/platformadmin/signup.go:184` | El alta acepta solo la dirección pelada y recorta los dos invisibles de los bordes (D-F2-13) | **visible → V3** |
| O3 | `apipublica/crmcallback.go:213` | Callback firmado con el puente encendido y sin `endpoint_url`: 200 → 403 (D-F6-11) | **visible → V4** |
| O4 | `apipublica/crmcallback.go:217`, `:465` | Un timestamp a más de ~292 años en el futuro ya no pasa la ventana | **visible → V5** |
| O5 | `modulos/solicitudes/integrations/gate.go:53` | El gate del puente exige endpoint y secreto; sin ellos no se encola la entrega (D-F6-11) | **visible → V6** (y causa de V4) |
| O6 | `modulos/edge/grpc/send_revoke.go:83` | `RevokeTenant` avisa también a los Edge con sesión viva que `fleet` no lista (D-F3-10) | **visible → V7** |
| O7 | `modulos/edge/grpc/connect_session.go:328` | El cierre de un stream no deja de rastrear la sesión si el Edge ya reconectó por otro (D-F3-9) | **visible → V8** |
| O8 | `apipublica/intents.go:126`, `:285` | El push de la config de intents no usa el contexto de la petición (D-F7-12) | **visible → V9** |
| O9 | `arranque/servir.go:3`, `arranque/servir_cloudlink.go:51` | La parada del CloudLink no agota el plazo con un Edge conectado (D-F3-13) | **visible → V10** |
| O10 | `modulos/captacion/casebank/anonymize.go:128`, `modulos/captacion/casebank/anonymize_phones.go:56`, `:90` | El anonimizador cierra tres agujeros de PII (D-F7-8) | **visible → V11** (CLI, solo tras T10.7) |
| O11 | `apipublica/diagnostics.go:118` | El *rollback* de D5 va desenganchado de la cancelación de la petición (D-F3-11) | interna · datos (I-d4) |
| O12 | `modulos/edge/fleet/repository_postgres_selfpn.go:182` | `SetSelfPn` re-cifra también una fila con el sobre a medias (D-F3-12) | interna · datos (I-d5) |
| O13 | `modulos/captacion/pipeline/pipeline_outcome.go:108` | Si el worker se apaga dentro de una etapa, el job vuelve sin cobrar intento (D-F7-10) | interna · datos (I-d6) |
| O14 | `modulos/solicitudes/integrations/worker.go:334` | Con el contexto cancelado el *webhook worker* no registra a `ERROR` ni cuenta el intento (D-F6-7) | interna · log (I-l3) |
| O15 | `modulos/solicitudes/integrations/worker.go:273`, `modulos/solicitudes/integrations/worker_delivery.go:103` | Una plantilla de payload `null` es un fallo con texto «plantilla del payload no es un objeto JSON»; el viejo entraba en pánico | interna (el propio comentario la da por inalcanzable: la plantilla la escribe siempre `crmpush` como objeto) |
| O16 | `modulos/solicitudes/intakes/buyerdata_postgres.go:101`, `:261` | Un blob de datos del comprador que descifra a `null` devuelve error; el viejo entraba en pánico en `PutBuyerField` | interna (dato no producible por el propio código) |
| O17 | `modulos/captacion/intake/memory.go:82`, `:151` | El gemelo de `intake` valida la clave de ventana y no reordena `Jobs()` (D-F7-8, cuarto punto) | interna (doble) |
| O18 | `modulos/captacion/intakeahead/warmup.go:164` | `Warm` comprueba `log` (D-F7-11) | interna |
| O19 | `modulos/acceso/iam/domain/canje.go:58` | El valor cero de `RedemptionVerdict` es `RedemptionMissing` (D-F2-10) | interna |
| O20 | `apipublica/degradationnotices.go:283`, `apipublica/summary.go:165`, `apipublica/intents.go:176`, `:224`, `apipublica/catalogimport.go:302`, `apipublica/tenantllm.go:258`, `apipublica/tenantvariables.go:172`, `apipublica/entitlements.go:81`, `apipublica/integrations.go:307`, `apipublica/intakes_llm_gate.go:101` | Ramas de la cara vieja que «no se portan» (un `offset < 0`, los «store nil ⇒ 500», `summaryBound`) | interna: cada comentario las declara inalcanzables o sustituidas por una función equivalente; no cuentan como divergencia de conducta |

O1–O10 son las que añaden visibles; O11–O19 son 9 divergencias de conducta internas; O20 se lista para que conste que
se leyó, y no se cuenta. De O1–O19, dieciséis tienen decisión en `DECISIONES.md` y tres no: **O4**, **O15** y
**O16** (hallazgos de F6 aceptados en el propio código).

## 4 · Decisiones de `DECISIONES.md` que cambian conducta

Leído entero: **150 decisiones** (143 filas de tabla en §0–§2 y §4–§8, más P1–P7 de §3). Cambian conducta **22**;
las otras 128 son de método, de nombres, de candados, de calendario, o deciden **conservar** la conducta del viejo
(D-F3-4, D-F6-4, D-F7-9 «portar tal cual», D-F7-13, D-F8-6, D-F8-17…D-F8-20).

| Decisión | Qué cambia | Clase | Entrada |
|---|---|---|---|
| D-F2-10 | El veredicto cero del canje rechaza | interna | O19 |
| D-F2-11 | Token de invitación con invisibles en los bordes: canjea | **visible** | V2 |
| D-F2-13 | Alta pública: dirección pelada e invisibles de los bordes | **visible** | V3 |
| D-F3-9 | La sesión sigue rastreada tras una reconexión rápida | **visible** | V8 |
| D-F3-10 | `RevokeTenant` alcanza a los Edge vivos que `fleet` no lista | **visible** | V7 |
| D-F3-11 | *Rollback* de D5 con contexto propio | interna · datos | I-d4 |
| D-F3-12 | `SetSelfPn` sana el sobre a medias | interna · datos | I-d5 |
| D-F3-13 | La parada del CloudLink no agota los 10 s | **visible** | V10 |
| D-F6-7 | El *webhook worker* corta al apagarse, sin `ERROR` ni intento cobrado | interna · log | I-l3 |
| D-F6-8 | El `MemoryStore` de `intakes` refresca `UpdatedAt` en toda escritura | interna (doble; Postgres no cambia) | — |
| D-F6-11 | El gate del puente exige endpoint y secreto | **visible** | V4, V6 |
| D-F7-8 | Anonimizador de `casebank` (tres agujeros) y clave de ventana en el gemelo | **visible** (CLI) / interna (doble) | V11, O17 |
| D-F7-10 | Parada dentro de una etapa: `Release` sin castigo | interna · datos | I-d6 |
| D-F7-11 | Guarda de `log` en `Warm` | interna | O18 |
| D-F7-12 | Push de intents con `context.WithoutCancel` | **visible** | V9 |
| D-F8-7 | El gemelo de `conversacion/store` imita a Postgres | interna (doble) | C8–C10 |
| D-F8-8 | `ListResults` lee `event_id`; el gemelo rechaza la segunda `_shipping` | interna | C11, C12 |
| D-F8-13 | Sobre antes que el job, en ventana y en re-análisis | **visible** (re-análisis) / interna · datos (ventana) | V1, I-d1 |
| D-F8-14 | Desanotar el id si falla la sentencia | interna · datos | I-d2 |
| D-F8-15 | El reintento retoma donde falló | interna · datos | I-d3 |
| D-F8-16 | Opciones nil ignoradas | interna | C7 |
| D-F9-10 (c) | Las goroutines de fondo reconstruidas no dejan `ERROR` al cancelarse el contexto | interna · log | I-l1, I-l3, I-d6 |

Dos filas más cambian conducta observable **en el futuro** y hoy no son divergencia (las dos caras responden igual):
**D-F7-13** (deuda D-30: el 403 de `PUT /api/v1/intents` pasará de prosa a `feature_not_enabled` y de 500 a
*fail-closed* cuando muera el viejo o se haga el editor) y la deuda **D-44** (los dos fallos del canje sin línea de
bitácora), que `deuda.md` remite a D-36. **D-41** dice de sí misma que «cambia conducta visible desde fuera» y tampoco
está hecha. Ninguna de las tres entra en el total.

## 5 · Las visibles, una por una

Consumidor deducido de la ruta: `/admin/*` (`:8100`) → consola de plataforma o BFF; `/api/v1/*` (`:8103`) → consola
del cliente o BFF; CloudLink → Edge; webhook y su callback → CRM.

### V1 · `POST /api/v1/intakes/{id}/reanalyze` cuando falla la composición del literal

- **Viejo**: abre el job y después compone; el fallo de componer se registra y **no** tumba la petición
  (`internal/reanalisis/reanalisis.go:409-435`). Responde **200** con `{intake_id, revision_no, job_id, via, status}`
  y `status` «en curso» (`internal/reanalisis/reanalisis.go:446-452`, `internal/publicapi/reanalyze.go:199`). El job
  muere después en el worker.
- **Nuevo**: compone antes; si falla, devuelve el error envuelto y no abre job
  (`internal/modulos/captacion/reanalisis/reanalisis.go:419-422`). La cara lo traduce por el `default`: **500**
  `{"error":"no se pudo pedir el re-análisis de la solicitud"}` (`internal/apipublica/reanalyze.go:390`).
- **Nota**: en el nuevo, el texto pegado ya está guardado en el hilo cuando llega ese 500
  (`internal/modulos/captacion/reanalisis/reanalisis.go:402-406` va antes de `:419`). En el viejo también se guardaba
  antes de abrir el job.
- **Consumidor**: consola del cliente (lo dice D-36); si pasa por el BFF, por determinar en la segunda mitad.

### V2 · `POST /api/v1/invitations/accept` con un token que trae U+200B o U+FEFF en un borde

- **Viejo**: el hash se calcula con `strings.TrimSpace`, que no quita esos dos caracteres
  (`internal/iam/domain/invitation.go:86`); el digest no casa con ninguna invitación y el handler responde **404**
  `{"error":"esa invitación no se puede usar"}` (`internal/iam/transport/http/canje.go:134`, `:175-176`, texto en `:83`).
- **Nuevo**: recorta también esos dos de los bordes (`internal/modulos/acceso/iam/domain/invitation.go:97`, `:103-105`);
  el canje procede y responde **204** sin cuerpo (`internal/modulos/acceso/iam/transport/http/canje.go:133`).
- **Sin cambio**: un invisible **interior** sigue dando 404 en los dos.
- **Consumidor**: `/api/v1` en `:8103` → consola del cliente o BFF; cuál de los dos llama al canje, por determinar en
  la segunda mitad.

### V3 · `POST /api/v1/signup` con un correo adornado o con invisibles en los bordes

- **Viejo**: valida con `mail.ParseAddress` y nada más (`internal/platformadmin/signup.go:62`, `:70`). «Ana
  <ana@x.com>», «<ana@x.com>», «ana@x.com (Ana)» y «"Ana" <ana@x.com>» pasan: **202**, y la cadena entera (en
  minúsculas) se guarda y se manda a identity. «ana@x.com» seguido de U+200B, o precedido de U+FEFF, pasa con el
  invisible dentro.
- **Nuevo**: exige la dirección pelada y recorta los dos invisibles de los bordes
  (`internal/modulos/acceso/platformadmin/signup.go:189`, `:197-198`, `:171-175`). Los cuatro adornados: **400**, texto
  plano «cuerpo o campos de registro inválidos» (`internal/modulos/acceso/platformadmin/signup.go:121`), sin llamar a
  identity. Los de invisibles en el borde: **202**, y lo que se guarda y llega a identity es «ana@x.com».
- **Dónde está fijado caso a caso**: `internal/modulos/acceso/platformadmin/signup_test.go:346` (las filas con
  «D-F2-13» llevan al lado lo que hacía el viejo).
- **Consumidor**: ruta pública de `:8103`; el campo `origin` solo admite «bff» y «edge»
  (`internal/modulos/acceso/platformadmin/signup.go:205`), así que la llaman el BFF y el Edge. Qué pantalla pinta el
  400, por determinar en la segunda mitad. Lo que identity hace con las cadenas que ahora ya no le llegan no se
  comprobó (otro repo; lo dice D-F2-13).

### V4 · `POST /api/v1/integrations/callback` de un tenant con el puente encendido y sin `endpoint_url`

- **Viejo**: el gate abre con feature + fila + `enabled` + adaptador «webhook» (`internal/integrations/gate.go:50-53`);
  el callback bien firmado sigue y responde **200** (`internal/publicapi/crmcallback.go:158-166`, `:203`).
- **Nuevo**: el gate exige además endpoint y secreto (`internal/modulos/solicitudes/integrations/gate.go:80`); el mismo
  callback responde **403** `{"error":"el puente CRM no está activo para este tenant"}`
  (`internal/apipublica/crmcallback.go:328-336`).
- **Sin cambio**: sin secreto ya era 401 en los dos (no hay con qué verificar la firma).
- **Alcance**: las dos caras impiden **guardar** por la API un puente encendido sin endpoint
  (`internal/publicapi/integrations.go:355`, `internal/apipublica/integrations_validate.go:150`); el caso pide una
  fila dejada así por fuera de la API.
- **Consumidor**: el CRM (es quien llama).

### V5 · `POST /api/v1/integrations/callback` con `X-Wapp-Timestamp` a más de ~292 años en el futuro

- **Viejo**: `delta <= crmCallbackWindow` sin más; la resta satura en el mínimo de `time.Duration`, su negación sigue
  negativa y pasa la ventana (`internal/publicapi/crmcallback.go:289-293`). Con la firma buena, **200**.
- **Nuevo**: `delta >= 0 && delta <= crmCallbackWindow` (`internal/apipublica/crmcallback.go:470`) ⇒ **401**
  `{"error":"no autenticado"}` (`internal/apipublica/crmcallback.go:421-424`).
- **El caso real** que nombra el comentario: un puente que manda milisegundos en vez de segundos.
- **Consumidor**: el CRM. **Sin decisión en `DECISIONES.md`**: solo está en el comentario
  (`internal/apipublica/crmcallback.go:217`).

### V6 · Entregas del webhook al CRM de un puente encendido sin endpoint o sin secreto

- **Viejo**: el gate abre (`internal/integrations/gate.go:50-53`), la entrega se encola y el worker la falla hasta
  `dead`. Al CRM no le llega nada (no hay adónde).
- **Nuevo**: el gate no abre (`internal/modulos/solicitudes/integrations/gate.go:80`) y no se encola nada. Al CRM
  tampoco le llega nada.
- **Qué se ve**: los contadores de `GET /api/v1/integrations/outbox` de ese tenant dejan de acumular fallidas y
  muertas.
- **Consumidor**: `/api/v1` en `:8103` → consola del cliente o BFF, por determinar en la segunda mitad. Mismo alcance
  que V4 (una fila dejada a medias por fuera de la API).

### V7 · CloudLink · `LeaseUpdate(Revoked)` tras `POST /admin/tenants/revoke`

- **Viejo**: `RevokeTenant` empuja solo a los Edge que lista `fleet.List`, y a ninguno si no hay `fleet`
  (`internal/gateway/grpc/send.go:128-137`). Un Edge con sesión viva que `fleet` no lista (canal de control de un
  Edge sin teléfono emparejado) no recibe la trama y se entera en su siguiente `Renew`.
- **Nuevo**: une a esa lista los Edge con sesión viva del tenant
  (`internal/modulos/edge/grpc/send_revoke.go:125`); ese Edge recibe el `LeaseUpdate(Revoked)` en el acto.
- **Sin cambio**: la respuesta HTTP de la ruta (`internal/arranque/rutas_admin.go:97`,
  `internal/bootstrap/arranque/rutas_admin.go:92`).
- **Consumidor**: el Edge (recibe la trama antes). La ruta la llama la consola de plataforma o el BFF, que no ven
  diferencia.

### V8 · CloudLink · tramas hacia una sesión que reconectó antes de que cerrara su stream anterior

- **Viejo**: al cerrar el stream viejo, `untrackSession` borra la sesión del seguimiento sin mirar si sigue viva por
  otro stream (`internal/gateway/grpc/connect.go:998`, `:1090-1102`). Hasta su siguiente reconexión no la alcanzan
  `PushConfig`, `RevokeLease`, el calentamiento ni la elección de plaza: el Edge **no recibe** esas tramas.
- **Nuevo**: el cierre solo deja de rastrear si la sesión se quedó sin nadie
  (`internal/modulos/edge/grpc/connect_session.go:226`, `:336-343`); el Edge **sí las recibe**.
- **Consumidor**: el Edge. Las rutas HTTP que disparan esos empujes responden igual.

### V9 · CloudLink · `ConfigPush` de intents tras `PUT /api/v1/intents` cuando el cliente HTTP cuelga

- **Viejo**: el push usa `r.Context()` (`internal/publicapi/intents.go:167`): si el cliente se va durante el push, se
  cancela; la config queda guardada y el Edge sigue con el catálogo anterior hasta reconectar.
- **Nuevo**: `context.WithoutCancel` con plazo propio de 5 s (`internal/apipublica/intents.go:293-294`, `:211`); el
  Edge recibe el push igual.
- **Sin cambio**: la respuesta HTTP (200 con la versión) y el `Warn` si el push falla.
- **Consumidor**: el Edge. Según D-F7-13, ningún cliente del ecosistema llama hoy a esa ruta (se usa por `curl`); no
  se comprobó aquí.

### V10 · CloudLink · cuándo se corta el stream `Connect` al parar el servidor

- **Viejo**: `GracefulStop` y espera de `shutdownTimeout` antes de `Stop()` (`internal/bootstrap/arranque/servir.go:91-104`);
  con un Edge conectado agota siempre el plazo. Medido en D-F3-13: 10,014 s.
- **Nuevo**: para en cuanto no queda nada en vuelo, con el mismo plazo como tope (`internal/arranque/servir.go:120`,
  `internal/arranque/servir_cloudlink.go:103`). Medido en D-F3-13: 0,061 s.
- **Coste que la decisión acepta**: un entrante que aún se estaba procesando tenía hasta 10 s para llegar a su envío
  y ahora tiene ≈ 50 ms; su envío encuentra la sesión offline.
- **Consumidor**: el Edge (ve caer el stream antes en cada reinicio del Cloud) y, por el coste, el cliente final por
  WhatsApp (una respuesta que no sale en ese reinicio). Las mediciones son de la decisión; aquí no se repitieron.

### V11 · Salida de `cmd/casebank` (banco de casos anonimizado) — solo desde T10.7

- **Viejo**: el anonimizador deja en claro el segundo de dos JID pegados, dos teléfonos seguidos en una racha de más
  de 15 dígitos y los teléfonos escritos con dígitos no ASCII (lo describen
  `internal/modulos/captacion/casebank/anonymize.go:128` y `internal/modulos/captacion/casebank/anonymize_phones.go:56`, `:90`).
- **Nuevo**: los tres se redactan.
- **Hoy no diverge nada en ejecución**: el CLI sigue importando el paquete viejo (`cmd/casebank/main.go:47`, D-F7-2) y
  cambia en T10.7.
- **Consumidor**: el operador que corre el CLI y quien lea el fichero que produce. No es un consumidor de red.

## 6 · Las internas que cambian datos o logs (para la segunda mitad, por si alguna pantalla los lee)

| # | Qué cambia en lo guardado o en el log | Viejo | Nuevo | Por dónde podría verse |
|---|---|---|---|---|
| I-d1 | Un job de ventana ya no puede quedar `failed` por reclamarse entre el cierre y el sobre | `internal/flujos/runtime/aggregator.go:892`, `:906` | `internal/modulos/conversacion/runtime/aggregator_sweep.go:326` | `GET /api/v1/intakes` (aparece el borrador que antes no nacía) |
| I-d2 | Un mensaje cuyo guardado falló entra en la ventana al re-entregarse | `internal/flujos/runtime/aggregator.go:511-525` | `internal/modulos/conversacion/runtime/aggregator_observe.go:132` | el borrador de la solicitud |
| I-d3 | No se duplican la fila de `flow_events` ni la decisión del hilo en un reintento | `internal/flujos/runtime/resume.go:325` | `internal/modulos/conversacion/runtime/resume.go:413-417` | `GET /api/v1/events/telemetry` (conteos), por determinar |
| I-d4 | La solicitud de diagnóstico no queda pendiente hasta su TTL si el cliente se va | `internal/publicapi/diagnostics.go:178` | `internal/apipublica/diagnostics.go:339-341` | `GET /api/v1/diagnostics/{command_id}` |
| I-d5 | Una fila de flota con el sobre del número a medias se sana en el latido siguiente | `internal/gateway/fleet/repository_postgres.go:338-339` | `internal/modulos/edge/fleet/repository_postgres_selfpn.go:239-242` | `self_pn` en `GET /api/v1/sessions`; D-F3-12 dice que no hay vía de producción que deje la fila así |
| I-d6 | Un job de captación no pierde un intento (ni muere en el décimo, con su línea a `ERROR`) por una parada del proceso | `internal/intake/pipeline/pipeline.go:991-1017` | `internal/modulos/captacion/pipeline/pipeline_outcome.go:107-116` | el borrador de la solicitud tras un despliegue; `cloud.log` |
| I-l1 | Sin `ERROR` del barrido del agregador al apagarse | `internal/flujos/runtime/aggregator.go:741` | `internal/modulos/conversacion/runtime/aggregator_sweep.go:167-172` | `cloud.log` |
| I-l2 | Desaparece el `Error` «el job quedó abierto pero SIN literal» | `internal/reanalisis/reanalisis.go:432` | — | `cloud.log` |
| I-l3 | Sin `ERROR` del *webhook worker* al apagarse, y la entrega cortada no cuenta como intento | `internal/integrations/worker.go:209` | `internal/modulos/solicitudes/integrations/worker_failure.go:59` | `cloud.log`; `attempts` de `webhook_outbox` |

## 7 · Discrepancias comentario ↔ código

Ninguna de fondo: en las once visibles, el viejo y el nuevo hacen lo que el comentario dice.
Tres precisiones:

1. **`documentations/deuda.md` (D-36) y `tareas.md` (T10.24) cuentan 26; el encargo, 27.** Las dos cifras son ciertas,
   con frases distintas (§1).
2. **D-36 presenta el recuento de la frase como «punto de partida» del inventario**, pero esa frase solo cubre una de
   las visibles (V1). Las otras diez se encuentran por `DECISIONES.md` o por R2.
3. **`internal/modulos/captacion/reanalisis/reanalisis.go:337-338`** dice que una petición que acaba en error antes
   de abrir el job «no ha tocado una sola fila (ni texto pegado, ni job)». Desde T8.40 hay un error posterior al
   guardado del texto pegado: el de componer (`:402-406` antes de `:419-422`). El párrafo habla de «antes del 10» y
   sigue siendo cierto para los nueve escalones de lectura; lo que no dice es que el 500 de V1 llega con el texto
   pegado ya en el hilo.

## 8 · Lo que no se pudo verificar

- **Nada se ejecutó.** Los códigos y cuerpos de §5 salen de leer los dos árboles. El **404** del viejo en V2 supone
  que el digest distinto acaba en `domain.ErrNotFound`; no se siguió la consulta del repositorio viejo hasta el final.
- **V3, el viejo con invisibles**: que `mail.ParseAddress` dé por bueno «ana@x.com» + U+200B lo afirma la fila del
  test nuevo («el viejo: …»), no una corrida del handler viejo.
- **V6**: no se leyó el punto donde el sink del webhook consulta el gate antes de encolar ni la forma del cuerpo de
  `GET /api/v1/integrations/outbox`; la entrada se apoya en los dos `gate.go` y en
  `internal/apipublica/integrations_validate.go:142`.
- **V10**: los tiempos (10,014 s y 0,061 s) son los de D-F3-13.
- **I-d3**: no se comprobó que `GET /api/v1/events/telemetry` cuente filas de `flow_events`.
- **Consumidores**: salvo el CRM y el Edge, que salen del sentido de la llamada, ningún consumidor está confirmado.
  Para V1, V2, V3 y V6 falta saber si llama la consola del cliente directamente o a través del BFF.
- **Divergencias sin comentario ni decisión.** Este inventario parte de lo que alguien dejó escrito. Una diferencia
  que nadie anotó no aparece aquí; lo que la buscaría son la huella (T10.2) y los procesos de F9 contra los dos
  binarios, no este documento. Los hallazgos de los README de fase (F2–F9) no se leyeron uno a uno: solo los que una
  decisión o un comentario citan.
- **R2 no es exhaustiva**: es una lista de fórmulas. Un comentario que describa una divergencia sin nombrar «el
  viejo» o «la vieja» no la casa.
