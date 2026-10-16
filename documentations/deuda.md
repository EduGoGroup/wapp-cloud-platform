# Deuda viva de `wapp-cloud-platform`

> Revisada entera contra el árbol el **2026-10-11**. Cada entrada abierta trae `fichero:línea`
> (localizado por símbolo ese día), la **consecuencia** (qué pasa si nadie la toca) y **cómo se
> cerraría**. Lo que no verifiqué va marcado **NO VERIFICADO** o **sin medir**.
>
> **Ampliada el 2026-10-14** con D-51 a D-65: los hallazgos del laboratorio de flujos (Plan 058 de la
> raíz de wApp), localizados por símbolo ese día en la rama `laboratorio` (`e74925b3`). El código del
> laboratorio que los reproduce está en los PR #89, #90 y #92, abiertos, en revisión. D-63 a D-65 salen
> de E18, que corre el ecosistema real con la receta del nivel c de la raíz.
>
> **Recuento** (contando los títulos `### D-nn` de este fichero): **65** identificadores, del D-1
> al D-65; **56 abiertos** y **9 cerrados** (D-7, D-10, D-19, D-35, D-38, D-39, D-40, D-42 y
> D-49). Aparte, la tabla de §5 lleva seis marcas heredadas de los planes, una de ellas cerrada.
> Un identificador cerrado no se reutiliza ni se borra.
>
> **«El expediente»**: varias entradas dicen de qué hallazgo o decisión de la reconstrucción modular
> salieron (`plan/F8-conversacion/README.md`, D-F7-13…). Esas rutas `plan/…` son del expediente de la
> reconstrucción, que ya no está en este repo: dónde está archivado, en
> [`modular-reorganization.md`](modular-reorganization.md).

**Este repo no marca deuda con `TODO`.** El `grep -rnE '// TODO\b|FIXME|HACK\b|XXX'` sobre
`cmd` e `internal`, sin tests, devuelve **7** líneas y ninguna es una marca: seis son la palabra
española «TODO» en mayúsculas al empezar un comentario y una es el literal `U+XXXX`. La deuda se
marca con `DEUDA-NNN.N`, 🔴, ⚠️ y 🟡 en el propio comentario.

---

## 1 · Seguridad y datos — lo primero de la lista

### D-1 · 🔴 `conversation_event_messages` está cifrada y FUERA del censo de rotación

- **Dónde**: la tabla se escribe cifrada en
  `internal/modulos/conversacion/events/store_append.go:50` (`body_enc, body_dek, body_kek_id`);
  el censo es `rekeyTargets` en `internal/platform/crypto/rekey.go:56`, con **8 sobres en 7
  tablas** (contados por sus líneas `table:`), y esta **no está**. Su única aparición en ese
  fichero es un comentario (`rekey.go:226`).
- **Consecuencia**: una rotación de KEK se declararía **completa** dejando esas filas
  **ilegibles para siempre** al retirar la KEK vieja. Y no es un búfer en vuelo: es la **copia
  canónica** del literal del cliente, la que sostiene auditar y regenerar. El propio comentario
  del censo (`rekey.go:48`) describe ese daño… para otras tablas.
- **El candado no cubre esto**: `test/procesos/rekey_contrato_test.go` rota los ocho sobres
  **declarados** — es un test *de la lista*, no *de la ausencia*.
- **Cómo se cierra**: (a) añadir la entrada al censo; (b) escribir el aserto que falta —
  *«¿qué tablas tienen una columna `*_kek_id` y no están en `rekeyTargets`?»*, derivado del
  catálogo de columnas, **con guarda anti-cero** para que no salga verde midiendo cero tablas;
  (c) corregir los dos comentarios de `rekey.go` que aún dicen «siete sobres en seis tablas»
  (`rekey.go:23` y `rekey.go:369`; es la misma línea de limpieza que apunta D-45).

### D-2 · 🔴 `intake_jobs.artifacts` guarda literal del cliente EN CLARO

- **Dónde**: lo dice la propia migración —
  `internal/platform/storage/postgres/migrations/structure/0083_intake_jobs_retencion.sql:80`
  («`artifacts` guarda literal del cliente (punto 2b), EN CLARO») y el `COMMENT ON COLUMN` de la
  `:107`.
- **Consecuencia**: las `evidence` de P2 son **subcadenas del mensaje del cliente por
  contrato**. Están en claro, **fuera del censo de rotación** y **sin vaciado en estado
  terminal**, con retención indefinida y declarada. El desvío **no caduca solo**.
- **Estado**: **DECIDIDO, no cerrado.** La salida elegida es podar **solo la `evidence`**,
  conservando la parte estructurada, para que redelivery y auditoría sigan funcionando. Falta
  ejecutarlo: mecanismo, test sobre filas reales, **mutación en rojo**, `COMMENT ON COLUMN` y
  la nota en el inventario de minimización.
- **Residuo que no se puede limpiar**: el `COMMENT` de
  `internal/platform/storage/postgres/migrations/structure/0072_intake_jobs_y_ventana_agregacion.sql:741`
  **afirma lo contrario** y sigue en el árbol, porque las migraciones no se editan hacia atrás.
  En la base manda el de la `0083`, que se aplica después; quien lea el DDL por orden se topa
  primero con el falso.

### D-3 · ⚠️ El KMS está construido y APAGADO — es una decisión, no un olvido

- **Dónde**: `internal/platform/crypto/kms_gcp.go` y
  `internal/platform/crypto/keyprovider_kms.go` existen; el default de `WAPP_KEK_PROVIDER` es
  **`env`** (`internal/platform/config/config.go:653`).
- **Consecuencia**: la KEK vive en una variable de entorno, no en un KMS. Es el estado
  esperado hoy (coste cero); encenderlo es un **gate de negocio** con dueño, no un cambio de
  fichero. Se anota para que nadie lo lea como un descuido ni lo «arregle» de paso.

### D-4 · 🔴 En UAT los cuatro listeners bindean a `*` y el admin queda expuesto

- **Dónde**: no es código roto, es configuración: los defaults de las cuatro direcciones van
  **sin host** (`internal/platform/config/config.go:623` a `:627`; `WAPP_HTTP_ADDR=:8100` y las
  otras tres), así que el proceso escucha en todas las interfaces. En el VPS de UAT, con el
  cortafuegos apagado, **el listener admin `:8100` y el `5432` de Postgres respondían desde
  Internet** (verificado conectando desde fuera el 2026-08-29; **no se ha vuelto a medir**).
- **Consecuencia**: `:8100` es el plano de operadores. `/metrics` y `/healthz` no piden auth
  (`internal/arranque/rutas_admin.go:69` y `:70`).
- **Cómo se cierra**: fuera del código — cortafuegos y `WAPP_HTTP_ADDR=127.0.0.1:8100`. Se
  anota aquí porque el default de la variable **invita** al fallo.

### D-52 · 🔴 El reintento del sink durable duplica la solicitud del carrito (medido en memoria; en Postgres, por confirmar)

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E08). Sin
> corregir. **Medido con los gemelos en memoria. Falta confirmarlo contra Postgres** (escalón E16).

- **Dónde**: la proyección del cierre del carrito, `closeIntake`
  (`internal/modulos/conversacion/modules/cart/projection_close.go:33`), hace tres escrituras sin
  transacción común: `store.CloseIntake` (`:37`), la revisión (`InsertRevision`, `:76`) y la línea de
  envío (`EnsureShippingLine`, `:92`). Si falla una de las dos últimas, el despacho reintenta la
  proyección: `retryDurableSink` (`internal/modulos/conversacion/runtime/resume.go:402`) llama a
  `RetryProjection` (`internal/modulos/conversacion/runtime/persist_sink.go:328`), hasta
  `durableRetryAttempts = 2` veces (`internal/modulos/conversacion/runtime/resume.go:316`). Cada intento
  vuelve a llamar a `CloseIntake`, y su contrato dice que, sin solicitud `open`, «nace una fila 'closed'
  con id uuid nuevo» (`internal/modulos/conversacion/store/repository_postgres_intakes.go:356`).
- **Consecuencia, medida en memoria**: 1 + 2 = **3 solicitudes `closed · total 5`** por turno. El turno
  se corta (`internal/modulos/conversacion/runtime/incoming_advance.go:356`) y el cliente lee «No
  pudimos registrar tu pedido. Por favor, intenta de nuevo en unos minutos.»
  (`defaultDurableSinkFailureNotice`). Si obedece y vuelve a fallar, ya son **6**. Además el evento
  queda `closed` desde el primer intento: `closeIfFinished`
  (`internal/modulos/conversacion/runtime/incoming_advance.go:320`) es una escritura aparte que va antes
  del fan-out, y el corte no la deshace. Mientras, `flow_state` no se guarda y sigue apuntando al evento
  como activo.
- **Lo que cambia en Postgres (deducido leyendo el código, NO VERIFICADO)**: el gemelo en memoria
  declara que **no** imita el índice único parcial `intakes_event_id_uidx` (cabecera de
  `internal/modulos/conversacion/store/repository_memory_intakes.go`; el índice, en
  `internal/platform/storage/postgres/migrations/structure/0054_evento_contenido_invertido.sql:125`).
  En Postgres, la segunda `closed` lleva el mismo `event_id` que la primera, así que debería chocar con
  ese índice (23505). `postgres.IsPermanentFailure` trata la clase 23 como permanente y corta sin gastar
  más reintentos (`internal/modulos/conversacion/runtime/resume.go:405`). Si es así, en Postgres no hay
  triplicado. Lo que queda es otro daño: el pedido **sí** quedó registrado y el cliente lee que no. Si
  obedece, abre un pedido nuevo en otro evento, y ese duplicado lo fabrica el aviso.
- **Relación**: D-F8-15 hizo que el reintento no duplicara `flow_events` ni el hilo, pero la proyección
  del cierre sigue sin ser idempotente. Es vecina de D-34, que reescribe una solicitud ya cerrada.
- **Cómo se cierra**: hacer idempotente el reintento del cierre. Hay dos caminos: que `closeIntake`
  reconozca la solicitud que cerró en el intento anterior (por el `event_id`, que la fila ya lleva), o
  que las tres escrituras vayan en una transacción. Además hay que decidir si el corte deshace el cierre
  del evento, y qué aviso recibe el cliente cuando el pedido sí se guardó.
- **Lo reproduce**: laboratorio E08 con `-twin-shipping` (Plan 058), PR #89, abierto, en revisión. En
  el laboratorio el fallo se provoca porque los dos gemelos en memoria no comparten la tabla `intakes`.
  En Postgres haría falta un fallo real de `InsertRevision` o de `EnsureShippingLine` después del cierre.

---

## 2 · Ramas vivas que no puede alcanzar nadie

Un símbolo cuenta aquí como muerto si nadie fuera de su propio fichero y de los tests lo
referencia (`grep -rnw` sobre `cmd`, `internal` y `test`, medido el 2026-10-11).

### D-5 · 🔴 Las reglas `flow_triggers kind='llm'` NO PUEDEN DISPARAR

- **Dónde**: lo declara con todas las letras el comentario de `KindLLM`
  (`internal/modulos/conversacion/trigger/trigger.go:46`, la constante en la `:54`); la rama
  sobrevive en `internal/modulos/conversacion/trigger/config_resolver.go:84`, con la deuda
  escrita en la `:69`.
- **Por qué**: `Signal.Intent` **es hoy siempre `nil` en producción**. La clasificación pasó de
  *push* a *pull* (I-CP-2) y el campo del proto quedó `reserved`: no hay de dónde leerla.
- **Consecuencia**: hay **símbolo, rama y tests verdes** sobre algo que producción no alcanza.
  Un lector concluye que la funcionalidad existe; de hecho la API admite crear reglas de ese tipo
  (`internal/modulos/conversacion/admin/triggers.go:70`). **Nada vigila** que esa rama tenga
  productor.
- **Cómo se cierra**: no aquí. Es un frente de producto con dueño (el sustituto natural —pedir
  la clasificación en el turno— **no cabe**: una decisión de arranque se toma en el turno y una
  inferencia tarda segundos, p50 medido 8,1 s). Lo que sí procede: un test que exija
  *«`KindLLM` tiene productor de producción, o el símbolo se retira»*.

### D-6 · 🟡 `anclaje.Distribute` no tiene llamante de producción

- **Dónde**: el paquete `internal/modulos/captacion/anclaje` se importa por sus tipos en
  `internal/modulos/captacion/pipeline/pipeline_chain.go:11`,
  `internal/modulos/captacion/stages/draft.go:14` y
  `internal/modulos/captacion/stages/draft_revision.go:10`, pero su función de reparto
  (`internal/modulos/captacion/anclaje/anclaje.go:271`) **no se llama en ningún sitio de
  producción** (`grep -rn '\.Distribute(' --include='*.go' cmd internal test | grep -v _test`
  → vacío). El pipeline pasa el reparto en cero (`pipeline_chain.go:346`).
- **Consecuencia**: el borrador sale **sin adjuntos colgados de su línea**. Es un hueco
  **nombrado** (`internal/modulos/captacion/pipeline/pipeline.go:28`, y la etapa en
  `draft.go:58`), no un olvido: cablearlo pide un lector que hoy no existe
  (`events.ThreadEntry`, en `internal/modulos/conversacion/events/thread_reader.go:86`, no trae
  ni los *media refs* ni el instante de cada turno, que son las dos entradas del reparto).
- **Cómo se cierra**: ampliar `ThreadEntry` con esos dos datos y cablear la llamada en el
  pipeline.

### D-7 · ✅ CERRADA · Un registrador de rutas de administración de flujos sin llamante de producción

- Era una función que montaba dos rutas ya registradas por el arranque. No aplica desde el
  2026-10-11: la función no existe y las dos rutas tienen un único registro
  (`internal/arranque/rutas_admin.go:105` y `:107`).

### D-8 · 🟡 Dos features comerciales sin un solo consumidor

- **Dónde**: `stt_audio` y `owner_app` se siembran en
  `internal/platform/storage/postgres/migrations/structure/0039_seed_plan_taxonomy.sql`
  (planes `advisor_ai_pro` y `pro`);
  `grep -rn '"stt_audio"\|"owner_app"' --include='*.go' cmd internal test | grep -v _test`
  devuelve **cero**.
- **Consecuencia**: un tenant puede tener la feature encendida y **no significa nada**.
- **Asimetría de trato**: `passive_profiles` está en el mismo caso pero **sí está documentado**
  como no-gateante (`internal/apipublica/sessionadmin.go:155`,
  `internal/modulos/edge/filtercfg/filtercfg.go:20`, `internal/arranque/edge_config.go:105`).
  La diferencia entre las tres es arbitraria.
- **Cómo se cierra**: documentar las tres igual, o retirar las dos que no gatean.

### D-57 · 🟡 El `umbral_confianza` del catálogo de intenciones publicado no lo lee nadie

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E12). Sin
> corregir.

- **Dónde**: el campo es del contrato compartido (`UmbralConfianza` en `intents.Config` de
  `wapp-shared/intents` v0.1.0, que lo rellena con 0,6 si viene a 0). La API lo acepta y lo valida al
  publicar (`ParseAndValidate`, `internal/apipublica/intents.go:259`). El único lector del catálogo en
  el Cloud es el adelanto P1 (`internal/modulos/captacion/intakeahead/intakeahead_classify.go:93`), y de
  él solo saca las intenciones (`flatten`, `:121`) y el vocabulario. El adelanto lo decide el agregador
  con un 0,7 fijo: `defaultIntentConfidence`
  (`internal/modulos/conversacion/runtime/aggregator.go:78`), comparado en `intentTriggers`
  (`internal/modulos/conversacion/runtime/aggregator_hint.go:62`). La opción que lo cambiaría,
  `WithIntentConfidence` (`internal/modulos/conversacion/runtime/aggregator.go:318`), no tiene llamante
  de producción (`grep -rn WithIntentConfidence internal cmd`, sin tests). Fuera de este repo, el único
  lector es `wapp-edge-intent/classifier`, que no llama nadie.
- **Consecuencia**: un tenant que publique `umbral_confianza: 0.9` cree que filtra y no cambia nada. Con
  Gemma pesa más: «y eso cuánto sale?» sale `intake_request` con 0,90 y cierra la ventana al instante, y
  el 0,7 se calibró con `qwen3:1.7b`.
- **Cómo se cierra**: decidirlo. O el campo se declara sin efecto en la API (o se retira del contrato),
  o el umbral viaja con la clasificación. El adelanto ya lee el catálogo del tenant al clasificar, así
  que podría devolver su umbral junto con la intención sin añadir la consulta en línea que prohíbe
  D-044.26. Lo segundo es una hipótesis: **no verifiqué** si cabe en el presupuesto de esa decisión.
- **Lo reproduce**: laboratorio E12 (Plan 058), PR #90, abierto, en revisión: cambiar el umbral del
  catálogo de 0,6 a 0,99 no mueve ninguna escena.

---

## 3 · Fronteras rotas y concentración de riesgo

### D-9 · 🔴 Hay tablas compartidas entre módulos, y `edge/lease` ESCRIBE en `public.tenants`

- **Dónde**: `internal/modulos/edge/lease/repository_postgres.go:128`
  (`UPDATE public.tenants SET revoked_at = now() …`) y `:143` (el `… = NULL`), cuando la tabla
  de identidad de la empresa la da de alta el módulo `acceso`
  (`internal/modulos/acceso/platformadmin/postgres.go:279`).
- **Consecuencia**: el kill-switch comercial escribe en la tabla de identidad de otro módulo,
  **sin API interna de por medio**. Contra la disciplina «una tabla, un módulo». La lista de
  tablas compartidas es de [`arquitectura.md`](arquitectura.md) §4.
- **Cómo se cierra**: (a) mover esos dos `UPDATE` detrás de un puerto de `acceso`; (b) el
  candado — un test «¿qué módulo toca qué tabla?». **La herramienta ya existe y se usa para
  otras cosas**: `internal/candados` vigila los imports entre módulos
  (`internal/candados/fronteras.go`) y `internal/arranque` tiene **19** tests de cableado
  (15 ficheros `*cablead*_test.go` y 4 `*_wiring_test.go`, contados con `ls`). Nadie la apuntó
  a las tablas.

### D-10 · ✅ CERRADA · El arranque era una sola función imposible de revisar por partes

- No aplica: el arranque son nueve fases declaradas en `internal/arranque/orquestador.go`
  (`var fases`), cada una con su `requiere()` comprobado antes de ejecutarla. Queda la lección
  que lo acompañó: un test de cableado recorre el **paquete**, nunca un fichero por su nombre
  (`internal/arranque/astpaquete_test.go`), porque mover la línea vigilada a otro fichero lo
  dejaría en verde mirando donde ya no está.

### D-11 · 🟡 Cinco goroutines de fondo sin supervisión ni reinicio

- **Dónde**: `internal/arranque/fase9_fondo.go:60` (webhook), `:70` (colector de
  `flow_events`), `:82` (agregador), `:91` (adelanto de clasificaciones) y `:106` (pipeline) —
  todas `go X.Run(ctx)` a pelo.
- **Consecuencia, en palabras del propio código**: «El sistema seguiría funcionando […] y
  nadie vería un error; simplemente no habría adelanto nunca» (`:88`) y «las ventanas cerrarían,
  los jobs quedarían `pending` y nadie los reclamaría nunca. **Ni un error en el log**» (`:103`).
  Los cinco fallos son **mudos**.
- **La red actual** es un test de **cableado**
  (`internal/arranque/pipeline_captacion_cableado_test.go`), que comprueba que se lanzan, no
  que sigan vivas.
- **Cómo se cierra**: un supervisor que relance con backoff y **emita una métrica de
  reinicio**, o como mínimo un `defer` que registre la muerte con su causa.

### D-12 · 🟡 `W = 1` es un invariante que vive en una línea repetible

- **Dónde**: `internal/arranque/fase9_fondo.go:105`. El comentario lo dice (`:96`): «Duplicar
  esta línea no daría ningún error: dos workers reclamarían sin pisarse […] y se bloquearían el
  uno al otro en la única plaza del Edge».
- **Estado**: vigilado por `TestPipelineCaptacionCableado`, que **es lo correcto**. Se anota
  porque el candado es lo único que separa el diseño del fallo.

### D-13 · 🟡 El candado de rutas de plataforma lee TEXTO FUENTE

- **Dónde**: `TestINV056_1_PlatformPermissionsMustEndInDotAny` detecta una ruta de plataforma
  buscando la cadena `"platformadmin."` en el argumento de `adminHandler(...)`
  (`internal/arranque/platform_permissions_test.go:49`); la advertencia está escrita en
  `internal/arranque/rutas_admin.go:26`.
- **Consecuencia**: impone un estilo de escritura que **solo el comentario explica**: esos
  handlers deben construirse inline. Un refactor «de limpieza» que los mueva a campos de
  `adminRouteDeps` deja el detector **ciego sin ponerse rojo**.
- **Cómo se cierra**: derivar la clasificación del **permiso** (`.any`) en vez del texto del
  constructor.

### D-14 · 🟡 El arranque entero depende de R2/S3 vivo

- **Dónde**: `internal/arranque/flows.go:75` llama a `NewR2PresignClient`, que valida el bucket
  con `HeadBucket` (`internal/platform/storage/objectstore/r2_factory.go:64`), y **si falla el
  proceso no levanta**.
- **Consecuencia**: fail-fast a propósito, pero acopla el arranque del acceso, del gateway y
  del pipeline a un almacén que **solo usa el nodo `media`**.
- **Cómo se cierra**: degradar a «media apagado» con un aviso ruidoso, en vez de tumbar todo.

---

## 4 · Modos de fallo mudos y calidad de código

### D-15 · 🔴 No hay forma de medir el fallback de P5

- **Dónde**: `internal/modulos/solicitudes/intakes/quotetext/quotetext.go:65`, con la deuda
  escrita en el propio fichero.
- **Consecuencia**: cuando el texto del modelo se descarta, el motivo sale por un `log.Warn` y
  por `fallback_reason` en la respuesta HTTP — **y nada más**. Cero métrica, cero `flow_event`.
  Nadie puede responder «¿qué porcentaje de sugerencias las escribe de verdad el modelo?» ni
  «¿cuál de los motivos manda?». Modo de fallo **mudo declarado a propósito**, porque la lista
  de eventos de telemetría está **cerrada** por diseño.
- **Cómo se cierra**: un contador con etiqueta `motivo`, o un evento más — **decisión de la
  tarea dueña de la telemetría**, no de quien pase por aquí.

### D-16 · 🔴 `writeJSON` duplicado cinco veces, y cuatro con un `if` muerto

- **Dónde**: `internal/platform/httpapi/authmw.go:196`,
  `internal/modulos/acceso/iam/transport/http/http.go:59`,
  `internal/modulos/acceso/platformadmin/handlers.go:328`,
  `internal/modulos/conversacion/admin/handlers.go:386` e
  `internal/apipublica/response.go:21`.
- **Consecuencia**: las cuatro primeras terminan en
  `if _, werr := w.Write(body); werr != nil { return }` — un `if` **muerto**: el `return` es la
  última instrucción de todos modos, así que **el error de escritura se traga** y el compilador
  no puede avisar. La quinta ya aprendió la lección: delega en un `writeJSONErr` hermano que
  devuelve el fallo, documentado con el incidente (`response.go:39`), **pero ese aprendizaje no
  se propagó**.
- **Cómo se cierra**: un único helper en `internal/platform/httpapi` con la variante que
  devuelve error, y las cinco copias apuntando ahí.

### D-17 · 🔴 El ritual `if cerr := rows.Close(); cerr != nil { _ = cerr }`

- **Cuántos**: **14 ocurrencias en 8 ficheros de producción**. Regla: líneas
  `cerr := rows.Close(); cerr != nil` cuya línea siguiente es `_ = cerr`
  (`grep -rn -A1 … --include='*.go' cmd internal test`, sin tests). Con 3 cada uno:
  `internal/modulos/acceso/platformadmin/postgres.go`,
  `internal/modulos/acceso/iam/infra/postgres/roles.go` y
  `internal/modulos/acceso/iam/infra/postgres/memberships.go`; con 1:
  `internal/modulos/acceso/platformadmin/access_requests_postgres.go`,
  `internal/modulos/acceso/iam/infra/postgres/invitations.go`,
  `internal/modulos/acceso/iam/infra/postgres/audit.go`,
  `internal/modulos/edge/receipts/postgres.go` e
  `internal/modulos/conversacion/trigger/store_postgres.go`. El mismo `if` aparece otras 26
  veces (40 en 28 ficheros en total) **manejando** el error: devolviéndolo o registrándolo.
- **Consecuencia**: en esos 14 sitios es un **no-op que satisface a `errcheck`** sin manejar
  nada. Si un `rows.Close()` empieza a fallar ahí, **no hay una sola línea de log**.
- **Cómo se cierra**: hacer en los 14 lo que ya hacen los otros 26 (devolver el error envuelto),
  o un helper que registre.

### D-18 · 🟡 Las cachés de entitlements no desalojan nunca

- **Dónde**: `internal/modulos/acceso/entitlements/postgres.go`: `p.cache[k] = …` (`:155`) y
  `p.effective[tenantID] = …` (`:182`), y **ningún `delete(` en todo el fichero**.
- **Consecuencia**: las entradas caducan **lógicamente** pero nunca se borran; los mapas crecen
  monótonamente con cada par (tenant, feature) visto. Hoy es acotado (features × tenants).
- **Por qué es un olvido y no una decisión**: el limitador hermano **sí** tiene desalojo
  (`internal/platform/ratelimit/ratelimit.go:72`, `evictStaleLocked`). La asimetría no está
  justificada en ningún comentario.

### D-19 · ✅ CERRADA · Un paquete Go que solo contenía un test

- No aplica desde el 2026-10-11: la validación de los ejemplos del contrato `wapp-crm-v1` vive
  con el código que lo emite
  (`internal/modulos/solicitudes/integrations/contrato_wapp_crm_v1_test.go`).

### D-20 · 🟡 El prompt del import de catálogo nunca se probó contra un LLM real

- **Dónde**: `internal/modulos/catalogo/catalogimport/prompt.go:41`, con la deuda escrita y
  fechada (decidida el 2026-08-06).
- **Consecuencia**: «lo que hay aquí es el texto del design, no un texto verificado»: trátalo
  como **una hipótesis con formato de instrucción**.

### Conductas raras que están fijadas por test

Las que siguen (D-24 a D-30) no son accidentes sin vigilar: cada una tiene un test que afirma
la conducta de hoy. **Al corregir una, cambia también el test que la fija.** Las que cambian
algo que se ve desde fuera (código HTTP, cuerpo) se corrigen con D-36, no sueltas.

### D-24 · 🟡 `GET /api/v1/degradation-notices` responde 500, no 504, al vencer el plazo de BD

- **Dónde**: `internal/apipublica/degradationnotices.go:244` (no usa `dbTimedOut504`; lo dice
  su comentario, `:227`). Lo fija `TestMountDegradationNotices_DBTimeout`.
- **Consecuencia**: incoherente con las demás lecturas de la cara, que dan 504; quien consume la API
  no distingue «BD lenta» de «error interno».
- **Veredicto**: defecto, abierto. Cambia un código HTTP: va con D-36.

### D-25 · 🟡 PUT y DELETE de `/api/v1/tenant-llm` no tienen plazo de BD

- **Dónde**: `internal/apipublica/tenantllm.go:311` (el `Upsert`), `:321` (la relectura del PUT)
  y `:372` (el `Delete`) llaman al store con `r.Context()` a secas; por eso `TenantLLMDeps`
  (`:51`) no lleva plazo. El GET (`:268`) tampoco lo acota.
- **Consecuencia**: con la BD lenta la petición queda colgada hasta que corte el cliente o el servidor.
- **Veredicto**: defecto, abierto.

### D-26 · 🟡 El prefijo reservado de SKU se esquiva con un espacio por JSON, pero no por planilla

- **Dónde**: el validador estricto compara sin recortar
  (`internal/modulos/catalogo/catalogimport/validator_item.go:81`), la planilla recorta la celda
  antes (`internal/modulos/catalogo/catalogimport/tabular.go:292`), y el runtime descarta con
  `HasPrefix` sin recortar (`internal/modulos/catalogo/catalog.go:371`).
- **Consecuencia**: `" _shipping"` se acepta por JSON y se rechaza por planilla: los dos caminos de
  importación no dicen lo mismo. Hoy no hace daño, porque el runtime tampoco trata ese SKU como de
  sistema.
- **Veredicto**: defecto leve; **si se recorta o no es decisión de producto**.

### D-27 · 🟡 Una subcategoría declarada con espacio final no se puede referenciar

- **Dónde**: `internal/modulos/catalogo/catalogimport/validator_catalog.go:139` (el código
  declarado se guarda sin recortar) frente a
  `internal/modulos/catalogo/catalogimport/validator_item.go:97` (la referencia del artículo sí
  se recorta).
- **Consecuencia**: una subcategoría `"01a "` es irreferenciable aunque el artículo la escriba idéntica.
  En la misma familia: el `sku` del artículo viaja sin recortar (`validator_item.go:77`), así que
  `"CAFE"` y `"CAFE "` son dos SKU válidos.
- **Veredicto**: defecto, abierto.

### D-28 · 🟡 Los precios de planilla aceptan todo lo que lea `strconv.ParseFloat`

- **Dónde**: `internal/modulos/catalogo/catalogimport/tabular_cells.go:125` (y `:101`, la
  cantidad con `strconv.Atoi`).
- **Consecuencia**: `0x1p4` (16), `1_000`, `1e3`, `+5` y `.5` son precios válidos. Improbable que un
  cliente lo escriba.
- **Veredicto**: accidente, no defecto que duela. **Se deja**; se anota para que nadie lo descubra dos veces.

### D-29 · 🟡 El desalojo de la caché del índice puede elegir mal la víctima con un tenant de id vacío

- **Dónde**: `internal/modulos/catalogo/indice/cache.go:297` usa `victim == ""` como centinela
  de «aún no hay víctima», y `Obtener` (`:242`) no rechaza el id vacío.
- **Consecuencia**: teórica: no debería existir un tenant con id vacío.
- **Veredicto**: **se deja**; como mucho, rechazar el id vacío en `Obtener`.

### D-30 · 🟡 `PUT /api/v1/intents` responde prosa en vez de `feature_not_enabled`, y 500 en vez de *fail-closed*

- **Dónde**: `internal/apipublica/intents.go:237` a `:245` (el gate `llm_intent` está escrito a
  mano, no con `entitlements.RequireFeature`). Lo fija el proceso P9
  (`test/procesos/p9_diagnostico_config_push_test.go:245`). Decidida el 2026-10-09 (D-F7-13 de
  `plan/DECISIONES.md`).
- **Consecuencia**: sin la feature, el 403 es `{"error":"el plan del tenant no incluye la clasificación de
  intenciones"}`: es el **único** endpoint con gate de plan que no responde
  `{"error":"feature_not_enabled","feature":"<clave>"}`, e incumple la norma de la documentación funcional de la raíz
  (un 403 con `feature_not_enabled` es plan; cualquier otro es permiso). Un cliente que hiciera *upsell* leyendo ese
  cuerpo —`wapp-client-console` ya lo hace con las demás rutas— mostraría «sin permiso». Y si el resolver de derechos
  falla, responde **500** (`:240`) donde el resto de gates cierra con 403. **Hoy no duele a nadie**: ningún cliente del
  ecosistema llama a esta ruta (el editor de intents en consola no existe, DT-37 de la deuda de la raíz); se usa por
  curl.
- **Veredicto**: defecto, abierto. **Se paga con D-36 o al construir el editor de intents (DT-37), lo que llegue
  antes**: la ruta pasa a `RequireFeature`, que trae juntos el cuerpo canónico y el *fail-closed*. Al corregirlo
  cambian con él el aserto de P9 y los tests de `internal/apipublica` que fijan la prosa y el 500.

### D-31 · 🟡 45 `//nolint` y 17 `#nosec`: lo que queda silenciado; 40 aprobadas, el resto a revisar tras la migración

> Abierta el 2026-10-10 por decisión de Jhoan: **«limpio» es sin avisos silenciados**, no «el linter no
> informa de nada». **Revisada el 2026-10-11 en F10-04 (T10.23)**: de los 95 que quedaban se arreglaron 50.
> **Decidida el 2026-10-11 (Jhoan, D-F10-13)**: de los 45 restantes, 40 quedan aprobados y 5 de producción se
> revisan tras la migración, con los `#nosec` y `nolintlint`.

- **Regla**: un aviso se arregla. Una supresión solo se queda si arreglarla obliga a cambiar lo que se prueba o lo que
  el código promete; entonces lleva linter, regla y motivo **en su misma línea**. No se añade ninguna sin plantearlo.
- **Lo arreglado (50)**: 28 `errorlint` (los tests que afirman que un centinela sale sin envolver usan
  `errhelpertest.IsBare`, de `internal/nucleo/errhelpertest`), 11 `gosec` G304 en tests (lectura anclada con `os.DirFS`
  u `os.OpenRoot`), 2 G202 (las consultas de `internal/modulos/solicitudes/intakes/postgres_read.go` son constantes
  precompuestas), 2 `contextcheck` (`context.WithoutCancel` en el cierre del servidor y en el colector de
  `flow_events`), 1 `gocyclo`, 1 `nilerr` y 5 directivas que el linter ya no necesitaba.
- **Lo que queda (45: 11 en producción y 34 en tests)**, medido con `grep -rn '//nolint' --include='*.go' . | wc -l`.
  Sin motivo en la línea, **0**: `grep -rnE '//nolint(:[a-z,]+)?\s*$' --include='*.go' . | wc -l`.

  | Linter y regla | Cuántos | Prod | Qué son y por qué no se arreglan |
  |---|---|---|---|
  | `gosec` G101 | 22 | 5 | Falsos positivos por el nombre o la forma de un literal: una ruta HTTP, un código de error y un aviso público del contrato con el Edge, un prefijo público; en tests, credenciales de mentira. Callarlo sin directiva sería renombrar o partir el literal |
  | `gosec` G204 | 6 | 0 | El arnés de `test/procesos` lanza los binarios que él mismo compila |
  | `gosec` G304 | 4 | 2 | `internal/modulos/inferencia/prompts/prompts.go` (anclar la lectura cambia el texto del error, que es contrato), `cmd/cobertura-ficheros/main.go` (la ruta es el argumento del CLI) y dos lecturas de test con ruta absoluta |
  | `gosec` G104 | 1 | 0 | El cierre de una conexión TLS de prueba: comprobar su error haría caer casos que afirman otra cosa |
  | `nilerr` | 4 | 2 | Degradación prometida en `internal/modulos/conversacion/events/thread_reader.go` y `conversacion/engine/engine.go`; en tests, el doble que la imita y un falso positivo |
  | `contextcheck` | 2 | 1 | `internal/modulos/edge/grpc/worklane.go`: `context.Background()` es el valor por defecto documentado de un `base` nil; y el test que necesita dos contextos |
  | `errcheck` | 2 | 1 | `internal/apipublica/response.go`: descartar el fallo de escritura es el contrato de `writeJSON`; y un fichero que es dato de un detector |
  | `gocritic` `mapKey` | 2 | 0 | Claves con un blanco final a propósito: el blanco es el caso |
  | `nilnil` y `staticcheck` SA1012 | 2 | 0 | El aviso es el caso que se prueba |

- **Fuera de esa cuenta: 17 `#nosec`** (4 en producción), la directiva propia de `gosec`: la misma deuda con otra
  grafía, que el inventario de `//nolint` no veía (`grep -rn '#nosec' --include='*.go' . | wc -l`). En producción: la
  ruta de la clave JWT (`internal/arranque/auth_jwt.go`), la de la clave de firma del lease
  (`internal/modulos/edge/lease/signingkey.go`) y dos G202 de `internal/modulos/conversacion/store` que concatenan
  marcadores generados. En tests, 4 G304 que se arreglan con `os.DirFS` y 9 G101 de material de prueba. **No se
  tocaron en T10.23**: se revisan con el mismo criterio cuando Jhoan lo decida.
- **Candado que falta**: `nolintlint` no está activado en `.golangci.yml`, así que nada impide añadir una supresión sin
  linter ni motivo, ni avisa de las que ya no hacen falta (en esta revisión había 5).
- **Veredicto**: deuda de calidad, no defecto. **Decidido por Jhoan el 2026-10-11 (D-F10-13)**: quedan **aprobadas 40**
  de las 45. Lo demás va a una **revisión de lint tras la migración**, que es la que cierra esta deuda:
  - las **5 de producción que callan conducta prometida**, para ver si alguna debe dejar rastro (un log o una métrica)
    en vez de silencio: `inferencia/prompts/prompts.go` (G304), `conversacion/events/thread_reader.go` y
    `conversacion/engine/engine.go` (`nilerr`), `edge/grpc/worklane.go` (`contextcheck`) y `apipublica/response.go`
    (`errcheck`). Lo que cambie conducta visible desde fuera va con D-36;
  - los **17 `#nosec`**, con el criterio de arriba;
  - **activar `nolintlint`**, con los `#nosec` que sobrevivan reescritos como `//nolint:gosec`, para que un solo
    candado vea toda la deuda (`nolintlint` no ve `#nosec`).

### D-32 · 🟡 El sobre de P2 no escapa lo que escribe el cliente

> Abierta el 2026-10-10 por decisión de Jhoan (D-F8-18; hallazgo 34a de
> `plan/F8-conversacion/README.md`). **Deuda post-relevo.**

- **Dónde**: `ComposeSourceText` (`internal/modulos/conversacion/runtime/source_composer.go:217`). El sobre que
  recibe P2 lleva secciones con cabecera (contexto y mensajes) y el texto del cliente se pega tal cual en la de
  mensajes.
- **Consecuencia**: un cliente que teclea una línea idéntica a una cabecera deja dentro del bloque de mensajes lo que
  parece una sección nueva, y puede simular un bloque de contexto. Acotado: el modelo no ejecuta nada (solo extrae un
  borrador, y el Cloud valida la forma de la salida), el borrador lo revisa el dueño antes de ser presupuesto, y el recuento
  interno no se engaña (un bloque falsificado no cuenta como mensajes: caso del corpus adversario,
  `source_composer_test.go`). El daño realista es un borrador con datos inventados por el propio cliente.
- **Veredicto**: riesgo aceptado por ahora. Escapar **cambia la entrada del modelo**, y el prompt de P2 está calibrado
  en campo contra el formato actual: no se valida con tests unitarios, hay que medirlo contra el banco de casos reales.
  Pendiente: escapar (o reforzar el prompt de P2, que se ajusta por fichero y sin release) **y medir** antes de darlo
  por bueno. El corpus adversario fija la conducta de hoy; cambiará con el arreglo.

### D-33 · 🟡 El resolver de tenant no filtra por el estado de la sesión

> Abierta el 2026-10-10 por decisión de Jhoan (D-F8-19; hallazgo 34f de
> `plan/F8-conversacion/README.md`). **Deuda post-relevo.** No investigada a fondo.

- **Dónde**: `PostgresTenantResolver` (`internal/modulos/conversacion/runtime/tenant_resolver.go:24`). La consulta no
  mira `state` (lo dice su comentario, `:23`): una sesión `loggedout` resuelve su tenant y pesa en el perfil igual que
  una viva. Lo fija el caso `AnyState_ResolvesTheSame` de la suite de contrato
  (`internal/modulos/conversacion/runtime/runtimehelpertest/tenant_resolver_contrato.go:78`).
- **Consecuencia**: **sin medir**. Depende de dos cosas que nadie ha mirado: si puede llegar un entrante por una sesión
  cerrada (si el Edge ya no tiene socket, no debería), y si el perfil se agrega por tenant o por sesión (si es por tenant,
  una sesión muerta en pasiva podría volver pasivo a un tenant con otra viva y activa).
- **Veredicto**: pendiente. Leer la consulta, contar en UAT las sesiones `loggedout` que comparten tenant con una
  viva, y decidir si se filtra. Si se filtra, `AnyState_ResolvesTheSame` cambia con la decisión.

### D-34 · 🟡 Un `item_added` reentregado tras el cierre reescribe las líneas de la solicitud cerrada

> Abierta el 2026-10-10 por decisión de Jhoan (D-F8-20; hallazgos 25 y 27 de
> `plan/F8-conversacion/README.md`). **Deuda post-relevo.** La vía de entrada no está
> confirmada.

- **Dónde**: el proyector del carrito, `internal/modulos/conversacion/modules/cart/projection.go:160`: la segunda
  búsqueda de la solicitud del evento va **sin filtro de estado**, así que encuentra una `closed` y le reescribe las
  líneas.
- **Consecuencia**: si un `item_added` se vuelve a entregar después de que el cliente confirmó, el pedido que el dueño ya
  vio cambia sin que nadie lo toque. **Sin confirmar que hoy ocurra**: la fuente más obvia de re-entregas (el reintento del
  sink durable) ya no repite todo (D-F8-15); no se investigó si queda otra.
- **Veredicto**: pendiente. El arreglo es pequeño (no escribir sobre una cerrada), pero **qué hacer con el hecho
  tardío** (descartarlo, avisar, abrir otra solicitud) es una regla de negocio.

### D-35 · ✅ CERRADA · Dos operaciones del sobre de la ventana sin llamante de producción

- Cerrada el 2026-10-10 (commit `d01e23a0`): se borraron las dos operaciones y lo que solo ellas usaban. No aplica.

### D-36 · 🟡 Los cambios de conducta visibles del servidor, sin revisar contra los consumidores

> Abierta el 2026-10-10 por decisión de Jhoan: **sesión aparte, de varios repos**. Los consumidores
> (`wapp-client-console`, `wapp-guardian-bff`, `wapp-platform-console`, el Edge) no se han tocado.

- **Qué es**: el servidor responde hoy distinto, a propósito, en varios casos que un consumidor puede estar pintando
  mal o no tratando. El inventario está hecho, leyendo el código y **sin ejecutar el binario**:
  `plan/F10-relevo/divergencias-visibles.md` del expediente
  (las visibles, una por una, en su §5; el recuento y su regla, en su §1).
- **El caso que la abrió**: `POST …/reanalyze` devuelve **500** «no se pudo pedir el re-análisis de la solicitud»
  cuando falla la composición del literal (`internal/apipublica/reanalyze.go:387`). **No se miró** cómo lo muestra la
  consola del cliente.
- **Qué falta** (la segunda mitad): por cada cambio visible, mirar en el consumidor cómo trata la respuesta, y
  adaptarlo o anotar por qué se deja. El consumidor solo está confirmado para el CRM y el Edge. Los cambios internos que
  alteran datos guardados (§6 del inventario) se miran también, por si una pantalla los lee.
- **D-F10-11 (Jhoan, 2026-10-11)**: el callback del CRM con la hora en milisegundos responde **401** (V5 del
  inventario) y **se queda así**. Hay que mirar qué formato de hora manda cada CRM conectado: uno que mande
  milisegundos no funciona.
- **Lo que arrastra de otras entradas**: D-24, D-30, D-44 y lo que salga de la revisión de lint de D-31 cambian
  conducta visible y se corrigen aquí.
- **Veredicto**: abierta. Sesión propia.

### D-37 · 🟡 El hilo y la cola no se escriben en un solo acto: un mensaje puede entrar en dos ventanas

> Abierta el 2026-10-10 por decisión de Jhoan (hallazgo 59 (f) de
> `plan/F8-conversacion/README.md`). **Deuda post-relevo.** **Deducida leyendo el
> código, no vista ni reproducida.**

- **Dónde**: el camino del entrante escribe el mensaje en el hilo (`conversation_events`) y **después** lo apunta en
  la cola (`intake_jobs`), en dos pasos *best-effort* y sin transacción:
  `internal/modulos/conversacion/runtime/incoming_advance.go:365` (`persistTurnMessages`) y `:376`
  (`observeForAggregation`). El cierre de la ventana compone el literal leyendo el **hilo** y decide si la ventana
  cambió mirando la **cola**.
- **El hueco**: si la ventana vence con un mensaje ya en el hilo cuyo apunte en la cola aún no llegó, el sobre lo incluye,
  la guarda no lo ve, y el apunte abre después **otra ventana** solo con ese mensaje: se analizaría dos veces, la segunda
  como un borrador de un único mensaje. No se pierde nada.
- **Lo que no se sabe**: si ocurre alguna vez, y cuánto tiempo pasa entre las dos escrituras (**sin medir**). Es
  estrecho: la ventana tiene que vencer entre las dos escrituras de un mismo mensaje.
- **Veredicto**: se deja. Arreglarlo pide una regla (qué se hace con el mensaje tardío), no un parche.

### D-38 · ✅ CERRADA · No había prueba de integración con el Edge REAL sin WhatsApp

- Cerrada el 2026-10-11: `make test-procesos-edge-real` corre el carrito con el Cloud y el Edge reales y un WhatsApp
  falso (`TestEdgeReal_CartConversation`). Para hacerlo a mano y en UAT:
  `plan/FW-whatsapp-enchufable/receta-uat.md` del expediente. Lo que no cubre tiene su
  número: D-48.

---

### D-39 · ✅ CERRADA · Ningún proceso notaba que faltara la política de reanudación del carrito

- Cerrada el 2026-10-11: `TestEdgeReal_CartResumePolicy` (`make test-procesos-edge-real`) cae sin `WithResumePolicy`,
  y el candado estático sigue en `internal/arranque`
  (`TestCableado_TheRuntimeHasTheCartResumePolicy`). Lo que el caso destapó tiene su número: D-47.

---

### D-40 · ✅ CERRADA · El árbol de `04` §3 no estaba cotejado con el `internal/modulos` real

- Cerrada el 2026-10-11 (D-F10-8): el árbol de
  `04-estructura-final.md` §3 (del expediente) se regeneró desde el código con
  `go list`, en el mismo cambio que reescribe esta documentación contra el árbol.

---

### D-41 · 🟡 Un 404 del canje de la API key se lee como «la persona no está en el padrón»

> Abierta el 2026-10-11 (hallazgo 65 de `plan/F9-procesos/README.md`). **Deuda post-relevo.**

- **Qué hay hoy**: `mapExchangeError` (`internal/modulos/acceso/iam/infra/identity/m2m_wire.go:108`) devuelve el error
  crudo para los estados que no están en su tabla, y el traductor de la operación de negocio lo vuelve a traducir. Con
  un 404 en `/api/v1/auth/token`, `GetUserSystems`
  (`internal/modulos/acceso/iam/infra/identity/m2m.go:241`) devuelve `domain.ErrNotFound` sin haber preguntado por la
  persona. Por lectura, un 409 del canje se retraduciría igual en las rutas que mapean el 409.
- **Por qué no se arregló**: cambia conducta visible desde fuera; identity no devuelve hoy un 404 en esa ruta.
- **Qué pediría**: que el canje con estado sin mapear sea opaco **y terminal** (un error que los traductores de negocio
  no reinterpreten), con su test.
- **Veredicto**: pendiente.

---

### D-42 · ✅ CERRADA · Los 135 ficheros entre 90 y 99 % de cobertura combinada no estaban clasificados

- Cerrada el 2026-10-11, el mismo día que se abrió: los 135 ficheros leídos, 319 bloques → 85 promesas con su test,
  1 caso en su suite, 8 guardas ya decididas y 225 defensivas (hallazgos 71 y 72 de
  `plan/F9-procesos/README.md`).

---

### D-43 · 🟡 El arnés de procesos no arranca un servidor con otro entorno sobre una base existente

> Abierta el 2026-10-11 (hallazgos 57, 42 y 70 de `plan/F9-procesos/README.md`). Decisión de
> Jhoan: **no** se amplía ahora; se anota para hacerlo **si se le saca provecho más allá de un caso**.

- **Qué hay hoy**: `opcionesServidor` (`test/procesos/servidor_test.go:56`) no admite variables de entorno extra y
  `arrancar` (`:100`) clona siempre una base nueva. Por eso la rotación de KEK se prueba sin servidor
  (`test/procesos/rekey_contrato_test.go`), y la puerta de `POST /admin/crypto/rekey` solo con rotaciones que no
  mueven nada (`test/procesos/p10_plataforma_rekey_door_test.go`).
- **Qué dejaría probar**: la rotación por la puerta con `processed > 0` y su fila de auditoría; que el arranque lee de
  verdad `WAPP_KEK_KEYRING` / `WAPP_KEK_CURRENT`; la lectura por las puertas reales tras retirar la KEK vieja; «el
  colector no recuenta el histórico» (pide filas antes del arranque); y, en general, cualquier proceso de «reinicio con
  otra configuración».
- **Sin medir**: cuánto cuesta, ni cuántos procesos lo aprovecharían.
- **Veredicto**: pendiente.

---

### D-44 · 🟡 La bitácora del canje calla dos de sus fallos

> Abierta el 2026-10-11 (hallazgo 73 de `plan/F9-procesos/README.md`). **Deuda post-relevo.**

- **Qué hay hoy**: el rastro de `Exchange` (`internal/modulos/acceso/iam/usecase/exchange.go`) es una línea «ok» o
  «error». El fallo de `resolveGrants` (`:203` a `:206`) y el de firma (`:219` a `:222`) devuelven sin
  `record(…, "error")`, a diferencia de los fallos vecinos (`:199`, `:210`, `:215`).
- **Qué pediría**: que los dos fallos dejen su línea «error», con su test.
- **Veredicto**: pendiente. Cambia lo que se ve en auditoría: va con D-36.

---

### D-45 · Limpieza: comentarios que no dicen la verdad y ramas que no pueden ejecutarse

> Abierta el 2026-10-11 (hallazgo 73 de `plan/F9-procesos/README.md`). Ninguna cambia
> conducta. Las siete rutas existen hoy; **los comentarios y las ramas no se releyeron uno a uno en esta revisión**,
> salvo el de `rekey.go`.

- **Comentarios**: `solicitudes/intakes/memory_write.go` (el store no rechaza el texto vacío: lo hace el servicio);
  `conversacion/content/json.go` («si el unmarshal a map falla…»); `internal/platform/crypto/rekey.go:369` («siete
  sobres en seis tablas»: son ocho en siete; comprobado, y lo repite la `:23`).
- **Ramas muertas**: `conversacion/engine/engine.go` (error de `tryPrime`); `cart/buyer.go` y `cart/cart_notes.go` (el
  «otro error» de `SanitizeNote`); `acceso/iam/infra/identity/m2m_wire.go` (`out == nil`);
  `acceso/platformadmin/access_requests.go` (`operatorFrom` sin identidad).
- **Veredicto**: pendiente; un commit por paquete.

---

### D-46 · 🟡 El servidor sigue despachando a un Edge con el lease revocado, y da por mostrada la pantalla

> Abierta el 2026-10-11 (hallazgo 34 de `plan/FW-whatsapp-enchufable/README.md`). Decisión de
> Jhoan: **deuda post-relevo**.

- **Qué hay hoy**: con el lease de un Edge revocado, un entrante sigue subiendo (el lease no gobierna lo que entra) y
  el motor de flujos lo atiende: guarda el estado y ordena los envíos. El Edge los rechaza en su gate («lease no
  vigente») y no sale nada: la doble llave se cumple. Pero el servidor **da la pantalla por mostrada**. Medido con
  `TestEdgeReal_CartConversation`: un cliente nuevo escribe «carrito», quedan un carrito abierto y una racha de
  auto-respuesta, y las categorías no las vio nadie.
- **Qué se nota**: si el lease se restaura y ese cliente contesta «1», recibe la lista de un menú que nunca vio. La
  bienvenida sí se reintenta (el motor lo dice en su log); la pantalla del flujo, no.
- **Qué pediría**: decidir si un rechazo del Edge deja la conversación como estaba, o si el servidor ni siquiera
  atiende el entrante de un Edge revocado. Cambia conducta visible del motor.
- **Sin medir**: qué pasa con los demás motivos de rechazo del Edge (sesión caída, tiempo agotado), que recorren el
  mismo camino; y la inferencia con el lease revocado, que el recorrido del carrito no llega a pedir.
- **Veredicto**: pendiente. Solo ocurre con un Edge revocado.

---

### D-47 · 🟡 La primera pantalla del carrito no usa el `page_size` de la empresa

> Abierta el 2026-10-11 (hallazgo 35 de `plan/FW-whatsapp-enchufable/README.md`). Decisión de
> Jhoan: **deuda post-relevo**.

- **Qué hay hoy**: `tenant_settings.page_size` lo siembra la política de reanudación al llegar cada mensaje de un
  carrito **ya abierto**. La pantalla que abre el carrito (las categorías) la pinta el arranque del flujo, antes de
  sembrar nada, con el tamaño por defecto (5): `internal/modulos/conversacion/modules/cart/cart.go:308` lo dice en su
  comentario («`Render` … usa el default del Module»).
- **Qué se nota**: una empresa con `page_size` menor que su número de categorías las muestra todas al abrir, y pagina
  desde el segundo mensaje. Medido con `TestEdgeReal_CartResumePolicy` (`page_size = 1`): el estado del carrito recién
  abierto no trae el ajuste. No se pierde ningún dato.
- **Qué pediría**: sembrar los ajustes de la empresa antes de pintar la primera pantalla.
- **Sin medir**: si alguna empresa de UAT tiene un `page_size` menor que su número de categorías; y si el checklist de
  comprador (`buyer_fields`), que se siembra por el mismo camino, tiene un efecto parecido.
- **Veredicto**: pendiente.

---

### D-48 · 🟡 El carrito con Edge real es un recorrido paralelo al del Edge falso, no la misma función

> Abierta el 2026-10-11 (hallazgo 33 de `plan/FW-whatsapp-enchufable/README.md`). **Deuda de
> test.** Decisión de Jhoan: **deuda post-relevo**.

- **Qué hay hoy**: dos funciones recorren el mismo carrito: `TestP3_CartConversation`
  (`test/procesos/p3_entrante_cart_test.go:196`; Edge falso, `make test-procesos`) y `TestEdgeReal_CartConversation`
  (`test/procesos/edge_real_cart_test.go:52`; Edge real, `make test-procesos-edge-real`). Comparten las constantes de
  texto, el catálogo y las consultas de estado; **no** el guion.
- **Qué protege y qué no**: si cambia un texto, lo notan los dos. Si cambia el guion de uno (un paso nuevo), el otro se
  queda atrás sin ponerse rojo.
- **Qué comprueba de menos el del Edge real**: el hilo cifrado del evento, el detalle de `conversation_events`, la
  revisión de la solicitud y el mensaje posterior al cierre. Tampoco cuenta inferencias: solo mira la línea del servidor.
- **Por qué no se unificó**: el caso del Edge falso afirma cosas que solo existen en el doble (las inferencias que le
  bajaron, sus errores internos, unos `wa_message_id` que elige el test). Unificar pide una interfaz común y mover un
  caso que hoy pasa.
- **Qué pediría**: un solo guion con dos conductores (Edge falso y Edge real), y lo propio de cada uno aparte.
- **Veredicto**: pendiente. El Edge falso sigue siendo el oráculo fino.

---

### D-49 · ✅ CERRADA · Un test de `iam/infra/postgres` fallaba de forma intermitente y ponía rojo `make ci-local`

- Cerrada el 2026-10-11 (commit `56e699b1`, PR #80): la transacción del test se abre con un contexto que no muere con
  él (`internal/modulos/acceso/iam/infra/postgres/memberships_test.go:220`). Medido: 0 fallos en 3000 repeticiones
  con `-race`, donde antes eran 5 de 300.

### D-50 · 🟡 52 literales de test y de métricas siguen nombrando rutas del árbol que se borró

> Abierta el 2026-10-12, al limpiar los comentarios (PR #86). Decisión de Jhoan (D-F10-15): no se reescriben
> durante la migración; es deuda para después del relevo. Medido con un escáner de `go/scanner` sobre los `.go`.

- **Qué queda**: 24 líneas en `internal/candados/fronteras_test.go` y 15 en sus `testdata/` (los casos del candado de
  fronteras, que prueba sus reglas sobre un árbol de mentira con los nombres de antes); 7 en tests de cableado de
  `internal/arranque` (rutas de import prohibidas y mensajes de error); 2 en `processimports_test.go`; 1 en
  `responsecontroller_test.go`; y los 3 textos `Help` de `/metrics`.
- **Por qué no se tocó**: son código, no comentarios. Reescribirlos pide decidir antes qué vigila el candado de
  fronteras ahora que ese árbol no existe (su `Mapa` está vacío en `internal/modulos/fronteras_test.go`).
- **Fuera de ese recuento, y sin arreglo posible** (medido el 2026-10-12 con el patrón de rutas borradas de
  `scripts/check-docs-tree.sh` sobre todo el repo, no solo los `.go`): **26** líneas en **18** migraciones de
  `internal/platform/storage/postgres/migrations/structure/` (22 en comentarios `--` y 4 dentro de un
  `COMMENT ON`, que además vive en la base) y **11** en `CHANGELOG.md`. Las migraciones no se editan: el runner
  es full-replay por hash (I-CP-7) y cambiar un comentario cambia el hash. El `CHANGELOG.md` cuenta versiones ya
  publicadas. Y una línea más en un dato de prueba, `internal/candados/testdata/cobertura/pasa/perfil.out`.
- **Veredicto**: pendiente. Los 3 `Help` cambian texto observable: van con D-36. El resto, un commit por paquete.

### D-51 · 🔴 Con el limitador de respuestas de producción, el carrito avanza sin que el cliente lo vea

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E08). Sin
> corregir.

- **Dónde**: `advanceLiveStep` (`internal/modulos/conversacion/runtime/incoming_advance.go:285`) sigue
  este orden: el `Step` del motor, el fan-out de efectos (`:335`), el `Save` del estado (`:358`), el hilo
  del turno (`persistTurnMessages`, `:365`), la ventana de captación (`observeForAggregation`, `:374`) y,
  al final, `sendReply` (`:375`). El limitador solo se consulta dentro de `sendReply`
  (`internal/modulos/conversacion/runtime/send.go:293`, `replyAllowed`). Sin cupo, devuelve `nil` sin
  enviar nada. En producción el límite es de 0,5 respuestas/s con ráfaga de 3
  (`internal/platform/config/config.go:663` y `:664`), y se cablea en
  `internal/arranque/fase7_flujos.go:112`.
- **Consecuencia**: cuando el limitador corta, el estado ya avanzó. El carrito cambia de pantalla, la
  solicitud llega a `closed`, el hilo apunta como `business:message` pantallas que nunca salieron y, si
  hubo turno acotado, la inferencia ya se pagó. Medido: el cliente vio tres pantallas y tiene un pedido
  confirmado que no vio confirmar. Es coherente con RT-4 («Save antes de Send»,
  `internal/modulos/conversacion/runtime/runtime_engine.go:42`), que prefiere un texto perdido a un
  avance duplicado. Pero RT-4 piensa en un envío que **falla**, no en uno que se **suprime** a propósito.
- **Sin medir**: cuántas veces agota un cliente humano 3 respuestas a 0,5/s. En el laboratorio los
  mensajes entran seguidos, sin la pausa de una persona. El limitador es la red anti-loop (Plan 020), y
  frente a un peer automático no avanzar también sería lo correcto.
- **Cómo se cierra**: con una decisión, no con un parche. Una opción es preguntar al limitador **antes**
  del `Step` y, sin cupo, no avanzar (el turno se descarta como un duplicado). Otra es avanzar, pero no
  apuntar en el hilo lo que no salió y reenviar la pantalla pendiente en el siguiente turno con cupo. La
  primera cambia la conducta anti-loop; la segunda, el hilo.
- **Lo reproduce**: laboratorio E08 con `-limiter prod` (Plan 058), PR #89, abierto, en revisión. El
  comentario de ese arnés que dice «el carrito no avanza» es inexacto: lo que no avanza es lo que ve el
  cliente.

### D-53 · 🔴 Al cliente le llega «dejamos el encuesta por ahora»

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E07). Sin
> corregir.

- **Dónde**: `stopNotice` (`internal/modulos/conversacion/runtime/events_switch.go:386`) escribe el
  artículo fijo: «Listo, dejamos el %s por ahora…» (`:395`). El nombre sale de `events.KindName`
  (`internal/modulos/conversacion/events/menu.go:262`) y del vocabulario `kindVocabulary` (`:223`):
  `cart` → «pedido», `survey` → «encuesta», `media` → «documentos». Un tipo que no está en el
  vocabulario se nombra con su identificador (`:244`). Sale por `event_stop` y por la opción «2» del
  menú de salida.
- **Consecuencia**: con encuesta, el cliente lee «dejamos el encuesta»; con documentos, «dejamos el
  documentos». Solo concuerdan «pedido» y «menú». Es texto observable que llega a WhatsApp.
- **Por qué no es un parche**: el literal está declarado «byte a byte» en el contrato del runtime
  (`internal/modulos/conversacion/runtime/events.go:71`) y lo fija un test
  (`internal/modulos/conversacion/runtime/events_switch_test.go:20`, con «pedido», el único caso que ya
  concuerda).
- **Cómo se cierra**: que el vocabulario dé el nombre con su artículo («el pedido», «la encuesta», «los
  documentos»), y que el literal y su test cambien con él. Es un texto que se mide, así que lo decide
  Jhoan.
- **Lo reproduce**: laboratorio E07 (Plan 058), PR #89, abierto, en revisión: un cliente que falla tres
  veces y elige «2» en el menú de salida.

### D-54 · 🔴 Expediente: con `gemma4:e2b-it-qat`, P2 tumba 12 de las 14 frases del banco

> Abierta el 2026-10-14: hallazgos del laboratorio de flujos (Plan 058 de la raíz, escalones E02 y
> E10, ampliados con E14). **Decisión de Jhoan: todavía no se arregla.** Esta ficha es un expediente de análisis: lo que
> se midió, las hipótesis abiertas y lo que falta entender antes de tocar el prompt. Mientras tanto,
> el laboratorio sigue estudiando el flujo con un frame híbrido.
>
> ⚠️ **El modelo**: el producto usa `qwen3:1.7b` por defecto; Gemma es el modelo del laboratorio. Nada
> de lo de aquí es una conclusión sobre qwen: el banco **no** se ha pasado con qwen en este plan.

**Qué se midió**

- **E02** (`debug_inferencia`, cuatro frases sueltas): P1 acierta las cuatro. P2 falla las dos que
  bifurcan con «el campo obligatorio wants[0].evidence vino vacío». Gemma separa bien las ideas y copia
  bien la pista de entrega con su evidencia, pero en `wants[]` omite `evidence`. Con la ráfaga Ámbar
  (tres mensajes, más texto) sí la trae.
- **E10** (P2→P3→P4→Match→Draft reales, banco de 14 frases, `-today 2026-10-14`):

  | Frame | Intención | Líneas | Fecha | Los tres ejes | P2 pasa la calidad |
  |---|---|---|---|---|---|
  | guion (Ámbar fijo) | 10/14 | 5/14 | 10/14 | 1/14 | 14/14 |
  | Gemma, prompts compilados | 13/14 | 0/14 | 2/14 | **0/14** | **2/14** |
  | Gemma, texto pelado (`-bare`) | 13/14 | 2/14 | 3/14 | 2/14 | 3/14 |
  | Gemma, P2 editado (`-prompts`) | 13/14 | 3/14 | 5/14 | 3/14 | 5/14 |

- **Dos modos de fallo**, con los prompts compilados:

  | Modo | Casos | La salida de Gemma |
  |---|---|---|
  | `wants[0].evidence vino vacío` | 10 | `{"idea": "presupuesto para 30 empanadas de carne"}`, sin `evidence` |
  | `delivery_hint.text vino vacío` | 2 (`short-tequenos`, `chocolate-variant`) | ideas perfectas y con evidencia, y `"delivery_hint": {"text": null, "evidence": null}`, cuando el prompt dice «si el cliente no dice cuándo, omite delivery_hint» |

- **Los reintentos no cambian nada**: los tres intentos de cada caso dan la misma salida byte a byte
  (D-55).
- **El formato del literal importa, pero no arregla**: `e02-long` pasa con el literal del compositor y
  falla pelado, y `chocolate-variant` y `greeting` hacen lo contrario.
- **El P2 editado** (instrucción que explica qué es `evidence` y que sin fecha no se escribe la clave
  `delivery_hint`, «ni vacía ni con null») sube a 5/14 y elimina el fallo de `delivery_hint`… pero
  **rompe la ráfaga Ámbar**, que antes pasaba.

**Dónde vive cada pieza**

- **El prompt**: `plantillaP2`, de `wapp-shared/llm` en el tag `llm/v0.4.5` que fija `go.mod` (línea 130
  de `llm/prompt.go`). La instrucción no dice qué es «evidencia»: el esquema la muestra solo como
  `"evidence": "..."`, y la regla de omitir `delivery_hint` va en el bloque del esquema, no en la
  instrucción. Se ajusta por fichero con `WAPP_LLM_PROMPTS_DIR`, sin release.
- **El validador**: `ParseMainIdeas` del mismo módulo. Una sola idea sin `evidence` tumba la salida
  entera (`validarWants`). Y como `Hint.Text` es un `string`, un `"text": null` se lee como cadena vacía
  y `validarHint` la rechaza: una pista **opcional** mal escrita tumba ideas buenas. Cambiarlo pide un
  release de `wapp-shared/llm`.
- **El lado del Cloud**: `P2.Run` (`internal/modulos/captacion/stages/p2.go:159`) parsea y después
  ancla (`anchor`, `:255`). El anclaje descarta, **una a una**, las ideas cuya evidencia no aparece en
  el literal, y con una pista sin respaldo se cae solo la pista. El validador compartido es estricto con
  la salida entera; el anclaje del Cloud es tolerante, idea a idea.

**Hipótesis abiertas**

1. **El esquema no explica qué es `evidence`.** El modelo copia el ejemplo, y el ejemplo es `"..."`. Lo
   apoya que el P2 editado suba de 2 a 5. Lo contradice que la ráfaga Ámbar sí la traiga sin explicación.
2. **El validador es demasiado estricto con `delivery_hint` nulo.** Leer `{"text": null}` como «sin
   pista» habría salvado 2 de 14 sin tocar el prompt. Es coherente con lo que ya hace el anclaje
   («se cae solo la pista»), pero cambia el contrato compartido.
3. **Relajar `evidence` no salvaría nada.** Si la salida pasara la calidad, el anclaje descartaría igual
   esas ideas: `evidence.Contains` devuelve falso con una evidencia vacía
   (`internal/modulos/captacion/evidence/evidence.go:88`). El job dejaría de morir y daría un borrador
   sin líneas. Este modo solo se arregla consiguiendo que el modelo escriba la evidencia.
4. **El formato del literal.** El sobre del compositor cambia qué frases pasan, en los dos sentidos. Es
   la misma advertencia de D-32: el prompt de P2 está calibrado contra el formato actual del sobre.

**Lo que falta entender antes de tocar el prompt**

- Pasar el mismo banco con `qwen3:1.7b`, el modelo del producto, para separar lo que es del modelo de lo
  que es del prompt.
- Por qué la ráfaga Ámbar trae `evidence` y las frases cortas no: si es la longitud, el número de
  mensajes o las cabeceras del compositor.
- Qué salida exacta da Gemma con el P2 editado en la ráfaga Ámbar: por qué rompe la frase que antes
  pasaba.
- Si `{"text": null}` debe leerse como ausencia (hipótesis 2): es una decisión del contrato de
  `wapp-shared/llm`, no del prompt.
- Si los reintentos de calidad deberían cambiar de temperatura (D-55) antes de medir otra vez.
- **Medir siempre contra el banco entero**: el P2 editado demostró que arreglar una frase rompe otra.

**Ampliación (E14, 2026-10-14): con Gemma de punta a punta, el re-análisis empeora el borrador**

Va en este expediente y no en una ficha propia: es el mismo modelo, la misma decisión de no arreglar
todavía, y la causa no está aislada de la de arriba. Con `-frame ollama`, el borrador inicial de la
ráfaga Ámbar pasa (como en E10). Pero el re-análisis, con la transcripción pegada («La clienta llamó por
teléfono: la torta de chocolate que sea de 12 porciones.»), cambia lo que saca P2. La revisión 3 trae
«torta de bizcocho de vainilla» `unmatched`, los tequeños y el envío, y **la torta de chocolate, que era
justo la que la dueña quería corregir, desaparece**. Lo que falta entender: si el texto pegado debilita la
evidencia de las ideas de los mensajes originales (el sobre de un re-análisis mezcla mensajes y
transcripción, y vale lo dicho para la hipótesis 4), y cuánto pesa el match con nombres cortos (D-56).
Una idea que P3 aísla tampoco se ve en el borrador (D-60).

- **Lo reproduce**: laboratorio E02 (el script `probar-frase.sh` de la raíz) y E10
  (`laboratorio/e10-pipeline -all -frame ollama`, con `-bare` y `-prompts <dir>`) del Plan 058, PR #90,
  abierto, en revisión. La ampliación, E14 con `-frame ollama`, PR #92, abierto, en revisión.

### D-55 · 🟡 Los reintentos del worker no sirven en P2 ni en P4

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalones E10 y
> E11). Sin corregir.

- **Dónde**: P2 y P4 piden temperatura 0 en todas sus llamadas: `llm.TemperatureGreedy`
  (`internal/modulos/captacion/stages/p2.go:237` e `internal/modulos/captacion/stages/p4.go:253`; vale
  0,0 en `wapp-shared/llm` v0.4.5). Ninguna de las dos reintenta por su cuenta: lo hace el worker, hasta
  `DefaultMaxQualityAttempts = 3` (`internal/modulos/captacion/pipeline/backoff.go:148`), con el mismo
  literal y el mismo prompt. P3 sí reintenta a otra temperatura (`llm.TemperatureRetry`,
  `internal/modulos/captacion/stages/p3.go:262`).
- **Consecuencia**: con temperatura 0 el modelo responde siempre lo mismo. Medido con Gemma en P2: los
  tres intentos dan la salida idéntica byte a byte, y con `-attempts 1` el resumen del banco sale igual.
  Los dos intentos extra no rescatan nada y retrasan la muerte del job con el backoff (≈26 s y ≈67 s en
  una corrida de E11), además de ocupar el Edge.
- **NO VERIFICADO**: P4 se lee en el código, no se midió. Con `qwen3:1.7b` no se ha medido.
- **Cómo se cierra**: decidirlo. O los reintentos de calidad de P2 y P4 suben la temperatura como P3, o
  esas dos etapas no reintentan por calidad. Lo primero cambia la salida del modelo y habría que medirlo
  contra el banco (D-54).
- **Lo reproduce**: laboratorio E10 con `-all -frame ollama` y con `-attempts 1`, y E11 con `-break-p2`
  (Plan 058), PR #90, abierto, en revisión.

### D-56 · 🟡 El match casa poco con los nombres que da P3 con Gemma, y P4 baraja atributos entre ítems

> Abierta el 2026-10-14: hallazgos del laboratorio de flujos (Plan 058 de la raíz, escalones E10 y
> E11). Sin corregir. **Medido solo con Gemma.**

- **Dónde, el match**: `DefaultCascade`
  (`internal/modulos/captacion/stages/match_cascade.go:77`) es `Exact → Fuzzy(0,85)`, sin zona gris.
  Su propio comentario fija que el 0,85 no se toca (D-044.45). Con Gemma, P3 llama «torta» a las dos
  tortas (pierde el sabor) y «tequeños» al artículo «Tequeños congelados». Ninguno de los dos llega a
  0,85, así que salen `unmatched`.
- **Dónde, P4**: `merge` (`internal/modulos/captacion/stages/p4.go:283`) funde por **posición**, y
  `applyQuantities` (`:330`) copia cantidad, rango, unidad y paquete del ítem del modelo que está en el
  mismo puesto. `llm.ParseQuantities` valida cada campo por separado, pero nada mira si el renglón tiene
  sentido. La única señal de un reordenamiento es el aviso de producto renombrado. En la ráfaga Ámbar,
  P4 le puso a la torta de chocolate `qty 2` y `package_size 30`, y se lo quitó a los tequeños, que eran
  el paquete.
- **Consecuencia**: borradores con líneas sin casar y renglones con la cantidad o el paquete de otro
  ítem. El borrador queda en `pending_approval` y lo revisa la dueña, así que no llega al cliente sin
  pasar por ella. Pero el trabajo de casar y corregir le cae a ella.
- **Cómo se cierra**: con el match, cablear un tercer escalón (la zona gris existe en `textmatch` y aquí
  está apagada) o pedir en P3 que copie el nombre del catálogo. Con P4, un validador del renglón: por
  ejemplo, que un `package_size` aparezca en la evidencia de **su** ítem. Las dos son hipótesis, y
  cualquiera se mide contra el banco antes de darla por buena.
- **Lo reproduce**: laboratorio E10 (`-phrase amber-burst` y `-phrase e02-long` con `-frame ollama`) y
  E11 con `-frame ollama` (Plan 058), PR #90, abierto, en revisión.

### D-58 · 🟡 Tras morir un job por calidad, la fila dice `attempts = 2`

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E11). Sin
> corregir.

- **Dónde**: solo el reencolado suma el intento (`retrySQL`, `attempts = attempts + 1`, en
  `internal/modulos/captacion/intake/machine_postgres.go:307`). `Fail` (`:400`, con `failSQL` en `:388`)
  y `finishSQL` no lo tocan. El worker calcula el intento como `job.Attempts + 1`
  (`internal/modulos/captacion/pipeline/pipeline_outcome.go:118`) y lo escribe en el motivo de la muerte
  (`kill`, `:155`).
- **Consecuencia**: un job que muere al tercer intento queda con `attempts = 2` y un `error` que dice
  «agotados los 3 intentos». Uno que termina al primer intento queda con `attempts = 0`. El
  `COMMENT ON COLUMN` de la `0078` dice «Número de intentos de pipeline ya realizados», y lo que cuenta
  la columna son los **reintentos castigados**. Quien diagnostique desde la base lee un intento de menos.
- **Cómo se cierra**: o `Fail` y `Finish` suman el intento final en la misma sentencia, o se cambia la
  definición de la columna en una migración nueva (las viejas no se editan). Antes hay que mirar quién
  lee `attempts` fuera del worker.
- **Lo reproduce**: laboratorio E11 con `-break-p2` (Plan 058), PR #90, abierto, en revisión.

### D-59 · 🟡 Con una encuesta por `keyword` y sin oferta, el cliente recibe la bienvenida y después silencio

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E06). Sin
> corregir.

- **Dónde**: la bienvenida se manda **antes** que el disparo (`welcomeIfDue` y después `handleTrigger`,
  en `internal/modulos/conversacion/runtime/incoming.go:349` y `:350`). Un flujo durable no arranca sin
  evento (`ErrDurableFlowNeedsEvent`, `internal/modulos/conversacion/runtime/start.go:78`), y
  `degradeDurableStart` (`internal/modulos/conversacion/runtime/incoming_trigger.go:333`) lo degrada a
  la oferta del despachador. Si la oferta sale vacía, deja un `WARN` (`:355`) y no responde nada.
- **Por qué se puede llegar en producción**: la API corta el alta de un `keyword` durable con un 422,
  pero solo si el tenant no tiene **ninguna** regla `event_start` habilitada
  (`blocksStartWithoutEventStart`, `internal/modulos/conversacion/admin/triggers.go:345`). La oferta, en
  cambio, solo enseña los tipos cuya *feature* trae el plan. Así que un tenant con una `event_start`
  hacia un tipo que su plan no incluye pasa el 422 y se queda sin oferta. Es el paso 2 de E06. **NO
  VERIFICADO por la API**: el laboratorio escribe las reglas por debajo de ella.
- **Consecuencia**: el cliente lee «¡Hola! Recibimos tu mensaje y lo estamos procesando. Te respondemos
  en unos minutos.» y nadie le contesta.
- **La documentación de la raíz está desfasada**: el caso 14 de `operacion/troubleshooting.md` (en
  `documentations/` de wApp, no en este repo) describe el estado anterior, un pedido que se cerraba en
  el chat y se perdía en la base. Hoy lo cortan la API (422) y el runtime. Se corrige allí, no aquí.
- **Cómo se cierra**: que el 422 mire también si la oferta saldría vacía con las *features* del plan, o
  que la bienvenida no se mande cuando el turno no va a tener respuesta. Las dos cambian conducta
  visible.
- **Lo reproduce**: laboratorio E06, empresa C, pasos 1 y 2 (Plan 058), PR #89, abierto, en revisión.

### D-60 · 🔴 P3 aísla un ítem en silencio: el borrador sale con una línea menos y nadie lo dice

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E14). Sin
> corregir. Reproducido igual en dos corridas.

- **Dónde**: cuando P3 no consigue especificar una idea (dos fallos de calidad, a temperatura 0 y a
  0,3), o la evidencia no aparece en el literal, o el pedido pasa del tope, la idea va a
  `art.Isolated` (`internal/modulos/captacion/stages/p3.go:221`, y `internal/modulos/captacion/stages/cap.go:94`
  para el tope) y solo sigue lo de `art.Items`. La cadena pasa a P4 únicamente `specs.Items`
  (`internal/modulos/captacion/pipeline/pipeline_chain.go:192`). El comentario de `IsolatedItem`
  promete que el ítem «queda anotado para que la bandeja lo enseñe»
  (`internal/modulos/captacion/stages/p3.go:37`), pero nadie lo lee: no aparece `Isolated` en ningún
  `stages/draft*.go` ni `stages/match*.go`, y ni la API ni `solicitudes` leen los `artifacts` del job.
- **Consecuencia**: el borrador sale con una línea menos de las que pidió el cliente y la métrica
  `intake_draft_created` dice `matched:2 unmatched:0`, sin rastro del que falta (`tally`,
  `internal/modulos/captacion/stages/draft_events.go:225`). Solo queda en `artifacts.p3.isolated` del job
  y en un `Warn` del log. La dueña aprueba un presupuesto incompleto sin saberlo. Pasa con cualquier
  modelo: basta que P3 falle una idea.
- **Cómo se cierra**: que el borrador lleve una línea (o una pregunta sugerida) por cada ítem aislado,
  apuntando a su idea de P2, igual que ya hace con lo `unmatched`, y que la métrica cuente los aislados.
  El texto de la idea está en `artifacts.p2.wants[IdeaPos]`, así que no hace falta guardar nada nuevo.
- **Lo reproduce**: laboratorio E14 con `-frame hybrid -script-stages p2` (Plan 058), PR #92, abierto, en
  revisión: en el re-análisis, Gemma falla P3 con la torta de vainilla y la revisión 3 sale con 3 líneas
  en vez de 4.

### D-61 · 🔴 `intakes.MemoryStore` no implementa `ReanalysisTargetOf`: el gemelo está incompleto

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E14). Sin
> corregir. **Deuda de test**: no afecta a producción.

- **Dónde**: el puerto del re-análisis, `reanalisis.Intakes`
  (`internal/modulos/captacion/reanalisis/reanalisis.go:135`), pide solo `ReanalysisTargetOf`, y lo
  satisface únicamente `*intakes.Postgres`
  (`internal/modulos/solicitudes/intakes/postgres_reanalysis.go:52`). `intakes.MemoryStore`
  (`internal/modulos/solicitudes/intakes/memory.go:57`) se presenta como el gemelo que «reproduce las
  MISMAS semánticas que el store Postgres», pero no tiene ese método. Los tests del propio paquete usan
  un doble a mano (`fakeIntakes`, en `internal/modulos/captacion/reanalisis/doubles_test.go:92`).
- **Consecuencia**: quien quiera montar el re-análisis contra el gemelo, como hizo el laboratorio, tiene
  que reescribir la consulta en memoria. Esa copia no la vigila ninguna suite de contrato, así que
  puede divergir de Postgres sin que nada se ponga rojo. Las reglas de la lectura (estado normalizado,
  `EventID` vacío en una legada, `LastRevisionNo = 0` sin revisiones, `ErrNotFound` en otro tenant) solo
  las prueba el test de Postgres.
- **Cómo se cierra**: implementar `ReanalysisTargetOf` en `MemoryStore` y llevar sus casos a
  `intakeshelpertest.Contrato`, la suite que corren los dos stores.
- **Lo reproduce**: laboratorio E14 (Plan 058), PR #92, abierto, en revisión: el laboratorio necesitó su
  propia tabla en memoria para tener esa lectura.

### D-62 · 🟡 Tres detalles de la bandeja y del re-análisis

> Abierta el 2026-10-14: hallazgos del laboratorio de flujos (Plan 058 de la raíz, escalones E13 y
> E14). Sin corregir. Van juntos porque cada uno es pequeño.

- **(a) La línea de envío depende del orden de las acciones.** Solo la crea `UpdateStatus` al entrar en
  `pending_approval` (`ensureShippingTx` con `ShippingAlways`,
  `internal/modulos/solicitudes/intakes/postgres_status.go:92`). Un borrador del pipeline **nace** en ese
  estado sin pasar por ahí, así que no la tiene, y la corrección de la dueña tampoco la añade. Aparece
  cuando la dueña vuelve de `needs_info`, a precio 0. `Approve`
  (`internal/modulos/solicitudes/intakes/approve.go:400`) la deja pasar, porque `PendingPriceLines`
  (`:220`) solo mira la **última revisión**, que no la lleva. **Consecuencia**: el mismo pedido sale con
  línea de envío o sin ella según el camino que siguió la dueña. Aprobado justo después de corregir, la
  cotización no lleva envío. **Cómo se cierra**: decidir en qué momento nace la línea (al crear el
  borrador, o siempre antes de aprobar) y que `Approve` mire las líneas vigentes y no solo la revisión.
- **(b) El texto determinista empieza por «Hola!» sin «¡».** `renderGreeting`
  (`internal/modulos/solicitudes/intakes/quotetext/render.go:35`) es texto que lee el cliente cuando P5
  no contesta. Lo fijan sus tests (`render_test.go` y `fixtures_test.go` del mismo paquete). **Cómo se
  cierra**: cambiar el literal y sus tests. Es texto observable, así que lo decide Jhoan.
- **(c) `elapsed_ms` del re-análisis mide desde el primer mensaje del cliente**: 1 530 000 ms en E14. **No
  es un defecto**: está decidido y escrito (`internal/modulos/captacion/stages/draft_events.go:37`). El job
  hereda el `message_ts` original, y por eso el evento lleva `requested_by` (`:172`). Se anota para quien
  calcule el KPI «tiempo a primer borrador»: tiene que filtrar por esa clave o lo inflará.
- **No es deuda**: que `revenue` sume todos los estados, también un carrito abandonado, es una decisión
  escrita en `internal/modulos/solicitudes/intakes/summary.go:55` («filtrar por dentro sería una
  política escondida»). Se acota con `?status=`.
- **Lo reproduce**: laboratorio E13 (a) y E14 (b, c) (Plan 058), PR #92, abierto, en revisión.

### D-63 · 🔴 El carrito también abre ventana de captación cuando el plan trae `llm_intake`

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E18, el
> ecosistema real con Gemma). Sin corregir.

- **Dónde**: los tres llamantes de `observeForAggregation` (turno normal,
  `internal/modulos/conversacion/runtime/incoming_advance.go:374`; mensaje que arranca el evento,
  `internal/modulos/conversacion/runtime/incoming_trigger.go:227`; reinicio por reanudación,
  `internal/modulos/conversacion/runtime/resume.go:287`) mandan **todo** entrante con evento vivo. La
  puerta, `Observe` (`internal/modulos/conversacion/runtime/aggregator_observe.go:94`), solo mira que haya
  evento (`acceptable`, `:156`) y la *feature* `llm_intake` (`:102`). No mira el tipo de evento ni el
  módulo que lo atiende, y la clave de la ventana (`intake.WindowKey`) ni siquiera lo lleva.
- **Lo que pasa después, según quién llegó antes**: el borrador pregunta por la solicitud del evento
  antes de crear la suya (`header`, `internal/modulos/captacion/stages/draft.go:220`). Si la encuentra, le
  cuelga la revisión `interpreted` y **no toca su estado** (D-044.46, escrito en ese comentario).
- **Consecuencia, medida en E18** (la ventana del carrito lleva 7 referencias: `carrito`, `1`, `1`, `2`,
  `un par`, `2`, `1`):
  - **Carrito a medias sin línea añadida** (`carrito 1 1 2 abc`): no hay solicitud del carrito todavía,
    así que el pipeline crea una nueva. Queda en la bandeja un borrador `pending_approval` espurio, con
    «Café»×1 sin casar dos veces y el envío.
  - **Carrito completo, dos veces**: P2 falla (`delivery_hint.text vino vacío`) y el job muere sin
    superficie (D-65). Las 7 referencias se pagaron en inferencias de lote.
- **Deducido leyendo el código, NO VERIFICADO**: si P2 pasara con el carrito cerrado, no nacería una
  segunda solicitud. La revisión `interpreted` (con «Café» sin casar) se colgaría de la solicitud del
  carrito ya cerrada, y la dueña vería el mismo pedido con dos versiones distintas. No miré qué enseña la
  bandeja en ese caso. Con el carrito abierto y una línea ya añadida, la revisión se colgaría de la
  solicitud `open` y no se vería en la bandeja.
- **Cómo se cierra**: decidir qué eventos alimentan la ventana. El carrito ya materializa su contenido
  durable por su propio proyector: lo natural es que su evento no observe, o que solo observe lo que el
  carrito no entiende. Se filtraría en los llamantes, que conocen el tipo de evento, porque la clave de
  la ventana no lo lleva. Es una decisión de producto: cambia qué ve la dueña.
- **Lo reproduce**: laboratorio E18, arnés (a) del carrito con un plan que trae `llm_intake` (Plan 058,
  receta del nivel c de la raíz). Sin PR de código: E18 corre el ecosistema real.

### D-64 · 🔴 P2 lee el menú del negocio como si fuera el pedido

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E18). Sin
> corregir. Medido con Gemma.

- **Dónde**: el literal lo arma `ComposeSourceText`
  (`internal/modulos/conversacion/runtime/source_composer.go:217`). Las decisiones (`KindDecision`) no
  entran, pero los **mensajes del negocio en turno** sí, en el bloque de mensajes, con la etiqueta
  «negocio:» (`speakerOf`, `:114`). El texto no se recorta ni se escapa: un mensaje de varias líneas
  ocupa varias, y solo la primera lleva la etiqueta, como dice el propio contrato. El menú del bot llega
  así (sacado de `revisions[0].payload.source_text` del borrador de E18):

  ```text
  negocio: Cuéntanos qué necesitas y te pasamos el presupuesto.
  1) Hablar con una persona
  ```

  La opción va en una línea **sin quién la dijo**. El prompt de P2 (`wapp-shared/llm` v0.4.5,
  `llm/prompt.go`) presenta ese bloque como «Texto del cliente» y no explica las etiquetas «cliente:» y
  «negocio:» ni las cabeceras del sobre. Y el anclaje no lo para: busca la evidencia en el literal entero
  (`anchor`, `internal/modulos/captacion/stages/p2.go:255`), y «Hablar con una persona» está ahí.
- **Consecuencia**: «Hablar con una persona»×1 sale como línea del borrador en 2 de los 6 que pasaron P2
  (`e02-empanadas` y `chocolate-variant`). En la ráfaga Ámbar, el menú se repite tres veces dentro del
  literal, porque el bot contesta «Opción no válida» + menú a cada mensaje.
- **Cómo se cierra** (hipótesis; cualquiera se mide contra el banco, D-54): que el compositor ponga la
  etiqueta en cada línea de un mensaje de varias líneas, o que el texto del negocio vaya al bloque de
  contexto; que el prompt de P2 diga que solo lo de «cliente:» es pedido; que el anclaje busque la
  evidencia solo en las líneas del cliente. Ojo con D-32: el sobre es un contrato «byte a byte» con el
  prompt, y tocarlo cambia la entrada del modelo.
- **Lo reproduce**: laboratorio E18, arnés (b′) con el banco de frases por el ecosistema (Plan 058).

### D-65 · 🔴 Un fallo de P2 no lo ve nadie

> Abierta el 2026-10-14: hallazgo del laboratorio de flujos (Plan 058 de la raíz, escalón E18). Sin
> corregir.

- **Dónde**: al agotar los intentos, el worker llama a `kill`
  (`internal/modulos/captacion/pipeline/pipeline_outcome.go:155`), que deja el job `failed` (`Fail`,
  `internal/modulos/captacion/intake/machine_postgres.go:400`) y una línea `Error` «pipeline: job
  FAILED» (`pipeline_outcome.go:170`). Y nada más: ni solicitud, ni `flow_event`, ni métrica, ni aviso. La
  bandeja (`GET /api/v1/intakes`) lista solicitudes, y un job muerto antes del borrador no tiene ninguna.
- **Consecuencia**: el cliente recibió la bienvenida («…Te respondemos en unos minutos.») y el menú, y
  nadie le contesta. La dueña no sabe que hubo un pedido. En E18 murieron así 4 de las 10 frases del
  banco que se pasaron por el ecosistema, y los dos carritos completos de D-63, todos con
  `causa=calidad`. Es el mismo silencio que
  D-59, por otra puerta. Y el `attempts = 2` de la fila (D-58) despista a quien lo busque en la base.
- **Relación**: D-54 explica por qué P2 falla con Gemma; esta entrada es que **cuando** falla, con
  cualquier modelo, no queda rastro visible. D-15 es el mismo tipo de hueco en P5 (la lista de eventos
  de telemetría es cerrada por diseño).
- **Cómo se cierra**: decidirlo. Que un job muerto por calidad deje algo que la dueña vea (una solicitud
  con el literal y sin líneas, o un aviso en la bandeja), o al menos un contador por causa y etapa. Lo
  primero choca con INV-13 (`Fail` vacía el sobre del literal): habría que decidir qué se conserva.
- **Lo reproduce**: laboratorio E18, arnés (b′) (`two-packages`, `short-chocolate-saturday`,
  `bread-tomorrow` y `some-cakes-friday`) y los carritos completos del arnés (a) (Plan 058).

---

## 5 · Deudas con nombre heredadas de los planes

| Marca | Dónde | Qué significa |
|---|---|---|
| **DEUDA-044.10** | — | ✅ Cerrada: la marca no está en el árbol desde el 2026-10-11 (`grep -rn '044\.10' --include='*.go' cmd internal test` → vacío) |
| **DEUDA-044.11** (sin dueño) | `internal/arranque/fase5_captacion.go:219`, `internal/modulos/captacion/stages/p4.go:21` | Una decisión que hay que escribir explícitamente en vez de heredarla: el plazo por llamada y la zona horaria (UTC, porque no hay zona por tenant en ninguna parte). **Medido el 2026-10-14** (laboratorio E18, Plan 058): «en 5 días», escrito a las 22:47 −03 (ya el 15 en UTC), sale 2026-10-20, y en la hora de quien escribe sería el 19. La base de la fecha es `job.MessageTS` en la zona de la etapa (`internal/modulos/captacion/stages/p4.go:194`), y la zona es `stages.DefaultZone`, UTC (`internal/arranque/fase5_captacion.go:229`). Es el hueco que ya fija el comentario de `DefaultZone`, visto de punta a punta: no abre un `D-nn` nuevo |
| **DEUDA-044.16** | `internal/modulos/captacion/stages/match.go:37` y `:224`, `match_lines.go:65`, `draft.go:299`, `draft_revision.go:51` y `:150`, `internal/modulos/captacion/pipeline/pipeline_chain.go:293` | Un ítem malo **no** tira el borrador: se degrada y se anota como *warning* |
| **DEUDA-050.1** (cerrada con red) | `internal/modulos/edge/grpc/connect_session.go:230` | Carrera de la reconexión rápida (`MarkOffline` diferido, `MarkOnline` inmediato). Mitigada preguntando «¿sigue caída?» **al ejecutar** el job, no al encolarlo. El comentario declara qué **no** cubre la red |
| **DEUDA-050.2** | `internal/platform/metrics/metrics.go:483`, `internal/arranque/fase1_infraestructura.go:42` | El cuello mudado del head-of-line al pool; `wapp_db_wait_count` existe para decidirlo |
| **H-1** (2026-10-01) | `internal/platform/metrics/flowlifecycle/collector.go:252`, `:262`, `:347` y `:371` | El colector de `flow_events` loguea a `ERROR` sin mirar `ctx.Err()` cuando el contexto se cancela a mitad de una llamada a BD (SIGTERM justo tras arrancar). **Cosmético.** Las otras tres goroutines de fondo que tenían el patrón ya miran el contexto: el worker de webhooks (`internal/modulos/solicitudes/integrations/worker_failure.go:126`), el agregador (`internal/modulos/conversacion/runtime/aggregator_sweep.go:166`) y el pipeline (`internal/modulos/captacion/pipeline/pipeline_loop.go:311`). El subtest `sin_errores` de `TestP0_Arranque` tolera los `ERROR` de cancelación posteriores a la señal de parada (`p0CountedErrors`, `test/procesos/p0_arranque_test.go:384`; D-F9-10), así que el colector no lo pone rojo |

---

## 6 · Deuda documental (dentro de este repo)

El `README.md` de la raíz del repo se reescribió entero contra el árbol el 2026-10-11: no hay
afirmación caducada conocida en él. Lo que sigue abierto, comprobado ese día:

| Qué | Dónde | Estado |
|---|---|---|
| `[Unreleased]` **vacío** | `CHANGELOG.md:6` | **Abierto**: hay 1064 commits desde `v0.2.0`, el último tag (`git log --oneline v0.2.0..HEAD \| wc -l`), y ninguno está anotado |
| El runbook de R2 dice «sin MinIO local» | `docs/runbooks/configurar-r2.md:16` | **Abierto**: UAT corre MinIO |
| `.env.example` no nombra 11 claves que `Load()` sí lee | `.env.example`, `internal/platform/config/config.go:729` | **Abierto**. Regla: literales `loader.Get…("X")` del paquete `internal/platform/config` cuyo `WAPP_X` no aparece en `.env.example`. Son `WAPP_CLOUD_ENC_PRIVKEY_B64`, `WAPP_DIAGNOSTICS_BUNDLE_TTL`, `WAPP_FLOW_INCOMING_TIMEOUT`, `WAPP_FLOW_MAX_CONCURRENT_INCOMING`, `WAPP_FLOW_REPLY_BURST`, `WAPP_HEALTH_DEGRADED_AFTER`, `WAPP_HEALTH_STALE_AFTER`, `WAPP_LLM_PROMPTS_DIR`, `WAPP_WEBHOOK_MAX_ATTEMPTS`, `WAPP_WEBHOOK_POLL_INTERVAL` y `WAPP_WEBHOOK_TIMEOUT`. La lista completa de variables es de [`contratos.md`](contratos.md) |
| `.env.example` declara una clave que no lee nadie | `.env.example:272` | **Abierto**: es D-21 |

### D-21 · 🔴 `WAPP_STORAGE_S3_PREFIX` es configuración MUERTA

- **Dónde**: declarada en `.env.example:272`. **Pero `config.StorageConfig`
  (`internal/platform/config/config.go:470`) no tiene campo de prefijo** ni lo lee `Load()`, ni
  existe en `objectstore.R2Config` (`internal/platform/storage/objectstore/r2_factory.go:18`).
  El nombre no aparece en ningún `.go`, ni en el `Makefile`, ni en los scripts.
- **Consecuencia**: el aislamiento del bucket existe, pero **está compilado**:
  `const mediaKeyPrefix = "wapp/media"` en `internal/apipublica/media.go:123`. Quien cambie esa
  variable creyendo que mueve el prefijo **no moverá nada, y no habrá error**.
- **Cómo se cierra**: o se cablea de verdad, o se borra de `.env.example`.

### D-22 · 🟡 Faltan los números de migración `0020` y `0021`, y nadie sabe por qué

- **Dónde**: `internal/platform/storage/postgres/migrations/structure/` — 84 ficheros
  numerados `0001`…`0086`, sin `0020` ni `0021` (contados con `ls`). No aparecen borrados en el
  historial de git. Las dos menciones de pasada están en
  `internal/platform/storage/postgres/migrations/structure/0060_platform_console_grants.sql:64`
  y en `internal/platform/storage/postgres/migrations/version.go:190` («hoy ya falta la 0021 y
  el sistema arranca»).
- **Consecuencia**: un hueco sin explicar invita a reutilizar el número, que en un runner
  ordenado por nombre metería DDL en medio de una secuencia ya aplicada.
- **Cómo se cierra**: una línea en el comentario de `version.go` diciendo que nunca existieron.

### D-23 · 🟡 Tres tablas se crean y se borran en cada replay

- **Dónde**: `0014_iam_users.sql`, `0017_iam_refresh_tokens.sql` y `0018_iam_api_keys.sql`
  crean `iam_users`, `iam_refresh_tokens` e `iam_api_keys`; `0038_retiro_iam_propio.sql:59` a
  `:61` las dropea.
- **Consecuencia**: cada arranque recrea y destruye tres tablas muertas. Es **coherente** con
  append-only + full-replay (las migraciones no se editan hacia atrás), pero cuesta tiempo de
  arranque y confunde a quien lea el DDL.
- **Cómo se cierra**: no se cierra sin romper la regla. Se documenta, y ya está documentado
  aquí.

---

## 7 · Lo que está BIEN y conviene no romper

- **Cero secretos versionados.** De los ficheros de entorno, `git ls-files` solo devuelve
  `.env.example`, con placeholders. `.env`, `.env.neon`, `certs/` y `cloud.log` están en el
  árbol de trabajo pero **ignorados** (`git check-ignore`).
- **HMAC en tiempo constante** en el callback CRM
  (`internal/modulos/solicitudes/integrations/sigv1/sigv1.go:61`).
- **Fail-closed** en el gate de features: los tres modos de no-resolución dan **403**, no 500
  (`entitlements.RequireFeature`, `internal/modulos/acceso/entitlements/middleware.go:45`; la
  única ruta que no lo usa es la de D-30).
- La caché de entitlements **no sostiene el mutex** durante la consulta a BD
  (`internal/modulos/acceso/entitlements/postgres.go:145`).
- **Ningún test apunta a un Postgres vivo**: los de base de datos arrancan la suya con
  testcontainers (`test/procesos`), y un candado lo vigila (`internal/candados/sinbdviva.go`).
  Ningún test se salta solo: `grep -rn 't\.Skip' --include='*_test.go' cmd internal test`, fuera
  de los datos del propio candado, devuelve cero.
- En producción quedan **11 `//nolint`** y **4 `#nosec`**, todos con su motivo escrito en la línea; el detalle y lo que
  falta por aprobar, en la deuda **D-31**.
