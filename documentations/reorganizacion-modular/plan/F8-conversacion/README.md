# F8 · `conversacion` — el Motor de Flujos, su runtime y el cierre de todos los puentes y adaptadores

> **Estado: en curso** desde el 2026-10-09 (F8-01, rama `reorg/f8-01-inventario-y-hojas` partida de `dev` @ `c0c0c03`; las 5 entradas, comprobadas: abajo). Spec escrita el 2026-09-28 sobre `dev` @ `1b18932`. Forma:
> [`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md). Norma:
> [`05`](../../05-metodo-contratos-y-tdd.md). Rutas: **autoridad**
> [`../FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) (filas I1–I19 · J18–J22).
>
> ✎ **D-F1-10 (Jhoan, 2026-10-02)**: los paquetes de suite de contrato y de dobles llevan el sufijo compuesto
> **`helpertest`**, el único que los candados de fichero eximen ([`DECISIONES.md`](../DECISIONES.md) §2). Esta spec los
> nombraba con `…test` (`triggertest`, `storetest`, `contenttest`, `eventstest`, `runtimetest`): se actualizó el sufijo, nada más.
>
> Recalibrado el 2026-10-03 tras la parada de F1 (`05` E-12, §4.2, E-9, E-4; `plan/DECISIONES.md` §3).

## Objetivo en tres líneas

1. Reconstruir `internal/flujos/**` (menos `contact`, `modules/cart/catalog.go`, `note.go` y
   `admin/sessions.go`) e `internal/turnoacotado` en `internal/modulos/conversacion/…`, aplanado.
2. Conmutar **de una vez** el singleton `*runtime.Runtime` (y su `IntakeAggregator`) y mudar a
   `internal/apipublica` las **19 + 5** rutas que lo usan o escriben por su almacén.
3. Dejar el binario nuevo con **cero puentes (import)** al código viejo y **cero adaptadores**
   `internal/arranque/bridge_*.go`: es la última fase de módulo. F8 **no crea** adaptadores: retira
   todos los que queden, y con cada muerte su módulo dueño entra en `Conmutados` (`05` §4.2).

## Tamaño medido (2026-09-28, `1b18932`)

| | Valor | Comando |
|---|---:|---|
| Ficheros de producción de la fase | **75** (74 si F5 ya reconstruyó `model`) | `ls internal/flujos/{admin,content,engine,events,model,modules,modules/*,runtime,store,trigger}/*.go internal/turnoacotado/*.go \| grep -v _test \| wc -l` → 78 (2026-09-29; el plan decía 79), menos `catalog.go`, `note.go` (F5/F6), `sessions.go` (D-FX-2) y los 0 de `contact` (F1) |
| Líneas de producción | **23.901** | `wc -l` por fichero (tabla en [`arquitectura.md`](arquitectura.md) §1) |
| Solo `runtime` | **23** ficheros · **8.640** líneas | ídem — confirma `05` §6 |
| Exportados | **633** (648 − 11 de `catalog.go` − 4 de `note.go`; `sessions.go` resta 5 más → **628**) | contador AST sobre `go/parser` (script en [`diseno.md`](diseno.md) §0) |
| Tests viejos (a leer, no a portar) | **167** ficheros · **1.001** `func Test*` · **47.057** líneas | `ls …/*_test.go`, `grep -c '^func Test'`, `wc -l` |
| El test más grande del repo | `internal/flujos/runtime/aggregator_test.go`, 1.993 l | `wc -l` |
| Ficheros de producción > 800 l | `runtime/events.go` 1.501 · `runtime/incoming.go` 1.328 · `store/repository_postgres.go` 1.074 · `events/store.go` 1.060 · `runtime/aggregator.go` 987 · `store/store.go` 875 · `cart/cart.go` 849 · `store/repository_memory.go` 817 | `wc -l \| sort -rn` |

## Entradas (tiene que ser cierto para empezar)

| # | Entrada | Cómo se comprueba |
|---|---|---|
| E1 | F0 cerrado: `internal/pendiente`, `make test-pendiente`, `make cobertura-ficheros` (informe, no bloquea), candados de `05` §5, `internal/apipublica` con el estrangulador | `ls internal/pendiente internal/apipublica internal/modulos/fronteras_test.go` |
| E2 | F1–F7 **cerrados y conmutados** (`nucleo/contact`, `acceso`, `edge`, `inferencia`, `catalogo`, `solicitudes`, `captacion`) | `ls internal/modulos` → 6 módulos · `ESTADO.md` |
| E3 | Las rutas de F2–F7 ya viven en `apipublica` (54 de 73) | `grep -c 'Handle(' internal/apipublica/*.go` contra el mapa §5 |
| E4 | `grep -rn 'pendiente.Implementar' --include='*.go' internal/ \| wc -l` → **0** (nada a medias de otra fase) | el comando |
| E5 | Jhoan decidió D-F8-1 (¿`model` en F5?) | [`../F5-catalogo/`](../F5-catalogo/README.md) o este README |

**Comprobadas el 2026-10-09 (T8.1, `c0c0c03`)**: E1 ✔ (`internal/pendiente`, `internal/apipublica`, `fronteras_test.go` existen) ·
E2 ✔ (`go list ./internal/modulos/...` → `acceso`, `captacion`, `catalogo`, `conversacion` (solo `model`), `edge`, `inferencia`,
`solicitudes`; F7 cerrada en `ESTADO.md`) · E3 ✔ (`FaseActual = 7`, 54 rutas; ojo: `grep -c 'Handle('` da 85 porque cuenta
líneas, no rutas — regla 6 del ecosistema: la cifra buena es la de la huella) · E4 ✔ con matiz: el `grep` literal da **11**, todas
en `internal/candados` (tests, `testdata` y comentarios); contratos de producción en rojo, **0** · E5 ✔ (D-F8-1 = D-F5-1, «sí, en
F5»: `C/model` existe y está verde; **T8.3 se tacha**).

## Salidas (es cierto al cerrar)

- `internal/modulos/conversacion/` con 74–75 ficheros, cada uno con su `_test.go` en verde y
  **0** `t.Skip`. Sin umbral de cobertura (P2): un test por promesa del contrato; mutantes en el
  nivel complejo; procesos de F9. La verdad de los adaptadores Postgres la da la suite contra
  Postgres (P4) y F9.
- `internal/arranque/fase7_flujos.go` cablea **solo** paquetes nuevos; `huella_test.go`
  igual para el módulo (rutas, rpc, métricas, goroutines).
- `apipublica` sirve las **73** rutas públicas; el `publicapi` viejo **no se construye** en el
  binario nuevo (TX.24).
- `grep -rlE 'internal/(flujos|turnoacotado|intake|intakes|gateway|iam|…)/' internal/modulos internal/nucleo internal/arranque internal/apipublica` → **vacío**: ni un import viejo en el binario
  nuevo (la lista exacta del patrón, en [`reglas.md`](reglas.md) §4).
- `ls internal/arranque/bridge_*.go` → **vacío**; la lista de puentes (import) de `fronteras_test.go` → **vacía**;
  `Conmutados` → **completa** (todos los módulos).

## Inventario E-12 — aprobado por Jhoan el 2026-10-09 (T8.2)

> Medido sobre `dev` @ `c0c0c03` por lectura del código viejo. **Manda sobre** el provisional de
> [`diseno.md`](diseno.md) §0.1 y sobre la columna «Nivel E-12 (provisional)» de la tabla de sesiones. Si un archivo
> sale peor al portarlo, sube de nivel y se anota aquí.

Reglas de conteo: **líneas** `wc -l`; **exportados** = declaraciones de primer nivel exportadas + métodos exportados de
tipos exportados (la regla de `candados.ExportadosCubiertos`; contado por grep/awk, coincide con `diseno.md` salvo
`runtime/webhook_sink.go`: 5, no 6); **consumidores** = directorios distintos bajo `internal`/`cmd` con un `.go` de
producción que importa el paquete (por paquete, no por fichero). Tamaño re-medido: **75 ficheros · 23.901 líneas**
(74 por hacer: `model` ya está verde desde F5) — igual que la spec.

### Hojas (F8-01)

| Fichero | Lín. | Exp. | Estado en memoria | Concurrencia | BD / tx | Cons. (paq.) | Nivel |
|---|---:|---:|---|---|---|---:|---|
| `model/model.go` | 406 | 24 | no | no | no | 12 | **hecho en F5** (verde, 441 l; T8.3 se tacha) |
| `trigger/trigger.go` | 257 | 33 | no | no | no | 5 | **simple** (tipos, constantes, `NoopResolver`) |
| `trigger/config_resolver.go` | 375 | 5 | no (lee por puerto) | no | no | 5 | **medio** (prioridad, normalización NFC; corpus adversario) |
| `trigger/store.go` | 36 | 2 | no | no | no | 5 | **simple** (puerto + centinela; necesita `store_test.go`: lleva un `var`) |
| `trigger/store_memory.go` | 91 | 7 | `map` de reglas | `sync.Mutex` | no | 5 | **medio** (lo cubre la suite; + `-race`) |
| `trigger/store_postgres.go` | 172 | 7 | no | no | 1 tabla (`flow_triggers`), sin tx | 5 | **complejo sin mutantes** (suite contra Postgres obligada; no hay CAS que mutar) |
| `content/{content,static,json,router}.go` | 179 | 11 | no | no | no (puerto) | 3 | **simple** (una pasada) |
| `store/store.go` | 875 | 42 | no | no | no | 11 | **medio** (13 interfaces, tipos, centinelas; el riesgo es partirlo) |
| `store/repository_memory.go` | 817 | 37 | 10 mapas | `sync.Mutex`; 8 `time.Now()` | no | 11 | **complejo** (reloj inyectable) |
| `store/repository_postgres.go` | 1.074 | 28 | no | no | 10 tablas; 3 `WithTx`; 2 `FOR UPDATE`; 5 `ON CONFLICT` | 11 | **complejo con mutantes** (`ReplaceTenantContentVersioned`, `CloseIntake`, `TouchContact`/`MarkWelcomed`) |
| `modules/registry.go` | 346 | 19 | `map` de módulos | `sync.RWMutex` | no | 9 | **medio** + test `-race` (se escribe en el arranque, luego solo se lee) |
| `modules/numbered.go` | 261 | 18 | no | no | no | 9 | **medio** |
| `modules/consulta.go` | 244 | 16 | no | no | no | 9 | **medio** |
| `modules/{ports,coerce}.go` | 103 | 6 | no | no | no | 9 | **simple** (`ports_test.go` con aserciones de compilación) |

Cambios frente al provisional (`diseno.md` §0.1): `trigger.go` y `trigger/store.go` bajan a simple; `store_memory` de
trigger baja a medio; `store/store.go` baja a medio; `registry.go` **no** sube a complejo.

**Particiones obligadas (E-13, tope 600)**: los tres ficheros de `store` nacen partidos por tema (sufijo del origen,
solo moviendo declaraciones): `store.go` → 2–3, `repository_memory.go` → 2–3, `repository_postgres.go` → 3–4. Cada
trozo lleva su `_test.go`. `store (3)` serán ≈ 8–10 ficheros de producción.

**Los gemelos en memoria se quedan en su paquete** (`C/store`, `C/trigger`), como dice el diseño: fases siguientes
(`cart`, `runtime`) los usarán como dobles. `MigrateContactID` del gemelo viejo: se mira quién lo llama al escribir el
contrato; si solo tests, se dice en el commit (E-8).

### Resto del módulo (F8-02…F8-06; nivel por fichero)

| Paquete (cons.) | simple | medio | complejo |
|---|---|---|---|
| `engine` (3) | — | `engine`, `consulta` | — |
| `menu` (2), `media` (2), `survey` (2) | `menu`, `media`, `survey`, `survey/projection` | — | — |
| `turnoacotado` (2) | `prompt` | `turnoacotado`, `troceado` | — |
| `events` (6) | `events`, `kinds` | `summary`, `dispatcher`, `menu` | `store` (1.060 l, 4 tablas, CAS y reintento por único → suite con casos de carrera), `thread_reader` |
| `cart` (3 reales, 8 nominales) | `effects`, `variants`, `resume`, `validate` | `cart`, `troceo`, `preresolutor`, `screens`, `prime`, `state`, `revalidate`, `buyer`, `consulta` | `projection` (escrituras encadenadas sin tx + índice único parcial) |
| `admin` (3) | `doc`, `durable_flow` | `triggers`, `handlers` | — |
| `runtime` (5) | `runtime`, `event_sink`, `log_sink`, `event_effects`, `summary_sources` | `exit_menu`, `thread`, `welcome`, `webhook_sink`, `persist_sink`, `send` | `keyedmutex`, `streak`, `runtime_engine`, `start`, `resume`, `event_lifecycle`, `events`, `incoming`, `aggregator`, `source_composer`, `tenant_resolver`, `self_numbers` |

Cambios frente al provisional: `survey` baja a simple; `cart/projection.go` sube a complejo; `runtime` deja de ser
«complejo en bloque» (12 complejos · 6 medios · 5 simples; mutantes en los 12). Nacen partidos además:
`events/store.go`, `cart/cart.go` (re-anclar el candado AST de `Step`), `runtime/{events,incoming,aggregator}.go`.
En tolerancia (500–600): `events/summary.go`, `admin/triggers.go`, `cart/troceo.go`, `cart/projection.go`.

### Adaptadores `bridge_<x>.go`: F8 crea 0 y retira todos (T8.32)

| Adaptador | Qué envuelve | Entra en `Conmutados` |
|---|---|---|
| `bridge_contact.go` (construido en `flows.go:90`) | `flujos/contact.Resolver` viejo sobre `nucleo/contact` | `nucleo` |
| `bridge_inferencia.go` (solo queda `turneroBridge`) | `turnoacotado.Turnero` viejo sobre `inferencia/llmvia` | `inferencia` |
| `bridge_captacion.go` (`aheadBridge`, `composerBridge`, `classifiedSink`, + la 2.ª instancia de `intake.Postgres` y `legacyThreadLimit`) | tres sentidos entre `captacion` nuevo y agregador/compositor viejos | `captacion` |
| 2.ª instancia vieja de `intakes.Postgres` (`fase3_almacenes.go:157`, no es fichero) | `cart.NewProjector` viejo | `solicitudes` |

`Conmutados` hoy: `acceso`, `edge`, `catalogo`. `conversacion` entra con su `conmutar`.

**Puentes de import (T8.31)** en `fronteras_test.go:120-150`, los tres con `Muere: "F8"`: `solicitudes/intakes/telemetria → flujos/store`,
`captacion/stages → flujos/store`, `captacion/reanalisis → flujos/events` (5 ficheros de producción + 6 de test, no «~6»).
Las dos filas de F5 (`catalogo → flujos/model`) de `arquitectura.md` §5.1 están caducadas: ya importan `C/model`.

**`send_budget_cableado_test.go`**: es de F3 (edge / cara de mensajes), ya adaptado; F8 no lo toca.

## Hallazgos

1. **(F8-01, inventario) El hallazgo 60 de F7, matizado leyendo el código; no reproducido.** Un job con sobre vacío **no**
   llega a `Release` ni a `Retry`: `literalDe` (`captacion/pipeline/pipeline_chain.go:30,153-163`) corre antes que la plaza
   y que la cadena y lo manda a `Fail` (`pipeline/backoff.go:105`). Lo alcanzable por esa vía es **literal perdido**: un job
   viejo con sobre **lleno** recibe `updated_at = now()` (`intake/machine_postgres.go:272-276,309-316`) entre el
   `CloseWindow` y el `PutSourceText` de otra ventana de la misma tupla; la subconsulta de `putSourceTextSQL`
   (`intake/postgres.go:155-169`) lo elige y el `source_text_enc IS NULL` de fuera escribe 0 filas — misma firma de log que
   el hallazgo 17. La **marca cruzada** (literal de A en B) exige una `pending` con sobre `NULL` de otro origen, y hay dos:
   un flush con hilo vacío o composición fallida (`flujos/runtime/source_composer.go:364-367`, `aggregator.go:906-909`), y
   el job de **re-análisis**, que nace `pending` sin sobre y lo recibe en una segunda sentencia
   (`captacion/reanalisis/reanalisis.go:377` → `:399`) y ya está portado (F7). El arreglo de D-F7-9 en el agregador **no lo
   cubre por sí solo**. Para F8-04/F8-05; aquí no se toca.
2. **(F8-01) [`arquitectura.md`](arquitectura.md) §5 estaba caducada**: 3 adaptadores vivos, no 1; los puentes de F5 hacia
   `flujos/model` ya no existen; `captacion/reanalisis` no importa `flujos/runtime`; y a la lista de ficheros de arranque a
   re-cablear le faltaban `contenedor.go`, `fase3_almacenes.go`, `flows.go`, `flowforkind.go` y `http.go`. Corregido con ✎.
3. **(F8-01) `Capas["conversacion"]` no incluye `catalogo`** (`fronteras_test.go:50-56`): lo necesitará `cart` (F8-03), y la
   prohibición `conversacion → catalogo/indice` es por subpaquete, que el motor de fronteras no sabe expresar (aviso en
   `:28-36`). Decisión para F8-03.
4. **(F8-01) La definición de hecho §4.5 no ve los tests**: `go list -deps` ignora los cuatro tests de cableado de
   `internal/arranque` que importan paquetes viejos (`iam`, `platformadmin`, `entitlements`, `gateway`, `publicapi`,
   `flujos/runtime`). Para F8-07.
5. **(F8-01) `bridge_contact_test.go` tiene 809 líneas**, sobre el tope de E-13; muere con el adaptador (T8.32).

6. **(F8-01, `store`) Tres divergencias deliberadas del gemelo en memoria: ahora imita a Postgres** (cada una con su comentario
   «Divergencia deliberada del viejo, F8-01» y su caso en la suite; ✅ **aceptadas por Jhoan, D-F8-7**): (a) `UpsertIntake` ya no escribe `CustomerNote`
   (el viejo, `flujos/store/repository_memory.go:518`, guardaba la del argumento y borraba la de una cerrada; el SQL,
   `repository_postgres.go:581-594`, ni nombra la columna); (b) `GetOpenIntake` y `GetIntakeByEvent` devuelven `CustomerNote ""`,
   como la proyección de cabecera (`:759`); (c) con varias `open` del mismo contacto, `GetOpenIntake` y `CloseIntake` eligen la
   más reciente por `created_at`, como el `ORDER BY` de Postgres, y no «la primera del recorrido del mapa» (`:613`, `:666`),
   que era no determinista. 🟡 Ojo en F8-03…F8-05: un test viejo de `cart` o `runtime` que dependiera de (a) o (b) con el
   doble en memoria estaba probando algo que producción no hace.
7. **(F8-01, `store`) Conductas del viejo que se portan tal cual y NO se arreglan**: `ListResults` de Postgres no lee
   `event_id` y devuelve `EventID ""` (memoria sí lo devuelve; `repository_postgres.go:346`); `UpsertIntake` con `EventID ""`
   falla siempre en Postgres (NOT NULL de la 0055), así que el «ni con NULL lo pisa» del `COALESCE` es inalcanzable;
   reescribir una línea `_shipping` ya existente viola `intake_items_shipping_uniq` en Postgres y memoria la duplica; y el
   comentario de `CloseIntake` («el segundo la ve ya closed y no crea otra», `:673-675`) no es lo que hace el código: N
   cierres con N eventos dan N solicitudes `closed` (la suite fija lo real). ✅ **Decidido por Jhoan (D-F8-8, 2026-10-09)**: `ListResults` de Postgres ya lee `event_id` (`03d82e9`) y el gemelo rechaza la segunda `_shipping` (`2677cb8`), cada uno con su caso (la suite pasa de 61 a **62** casos) y comprobado que el caso mata el código de antes; los otros dos no llevan código.
8. **(F8-01, `store`) Los 18 tests viejos no fijaban ninguna carrera** (ni una goroutine). La suite añade tres (versionado,
   cierre, bienvenida), y el montaje de Postgres **precalienta el pool** (16 conexiones): sin eso las llamadas salían
   escalonadas y el mutante «`CloseIntake` sin `FOR UPDATE`» sobrevivía.
9. **(F8-01, `store`) Los 9 métodos del gemelo que Postgres no tiene son solo de tests.** `MigrateContactID` lo llama solo
   `contact.MemoryResolver` (`nucleo/contact/repository_memory.go:243`), que solo se construye con un `MemoryRepository` en
   tests del runtime. Se portan los nueve: son los observadores del `Montaje` y los dobles de F8-03…F8-05.
10. **(F8-01, `trigger`) [`diseno.md`](diseno.md) §1.2 no casaba con el código**: la normalización no es «NFC» ni vive en
    `trigger.go`: es `ToLower` → NFD → descarte de marcas `Mn` → `strings.Fields`, en `config_resolver.go:355-375`; y a
    `ConfigResolver` le faltaba `ResolveLive`. Corregido con ✎. Conductas fijadas en el corpus adversario y **no** arregladas:
    «año» casa «ano»; dos bytes UTF-8 inválidos distintos casan entre sí; un emoji casa con y sin selector de variación; la İ
    turca baja a «i» pero la ı sin punto no es «i»; `IsEscape` no desempata por `priority` sino por `trigger_id` menor
    (`:233-243`). Equivalencia viejo ↔ nuevo: 67 filas de corpus, 1.005 comparaciones y 20.000 rondas aleatorias, 0 divergencias.
11. **(F8-01, `trigger`) `flow_triggers.tenant_id` no tiene clave foránea a `tenants`** (`0023_flow_triggers.sql`), y el gemelo
    en memoria no valida forma de UUID (un `trigger_id` que no lo es da `ErrTriggerNotFound`; Postgres, error de sintaxis). Se
    porta tal cual y está escrito en el contrato.
12. **(F8-01, `modules`) Un comentario del viejo no casaba con su código** (`numbered.go:92-106`: «un sello ilegible vale 0»; un
    sello que no es string se lee como ausente). Se porta el comportamiento, se corrige el comentario y se fija con dos casos.
    `AsInt` no acepta `float32` y `AsFloat` sí (`coerce.go:10-37`): asimetría portada y fijada. Los tests viejos de
    `exit_menu_test.go:197-283` (prueban `menu.New()`/`survey.New()`) son de F8-02.
13. **(F8-01, E-11) `modules/consulta.go` pasa sus exportados al inglés** (`Consulta` → `Query`, `Veredicto` → `Verdict`…):
    tabla en [`tareas.md`](tareas.md), antes del bloque 2. ✅ **Se quedan en inglés (D-F8-9, 2026-10-09).**

14. **(F8-01, gates) El binario viejo necesitó cuatro pasadas de `make test-procesos`**; el nuevo pasó a la primera. Las tres rojas,
    por intermitencias ya conocidas (hallazgos 17 y 62 de F7), ninguna en las suites nuevas: (1) `TestP6_CRMBridge/callback_body_adversarial`,
    `p6_crm_test.go:270: … write: broken pipe`; (2) `TestP4_MessageToDraft` (`adversarios`, `otro_borrador`, `cierre`), con
    `p4_borrador_test.go:319: línea ERROR inesperada … pipeline: job FAILED · causa=job_invalido · «stages: el job no trae literal que
    analizar (el compositor del flush no llegó a escribir el sobre)»`; (3) `TestP6_CRMBridge` (`push_first_attempt_fails`, `push_delivered`,
    `push_exhausted`…), misma firma en `p6_crm_test.go:379`. 🔴 **La carrera de D-F7-9 ya no es «vista una vez contra el nuevo»**: dos
    veces seguidas contra el **viejo**, en P4 y en P6, con la máquina cargada (load ≈ 11–13, ajeno a los gates). No reproducida a
    propósito ni arreglada aquí; es de F8-04/F8-05. Sin medir: si las dos suites nuevas (≈ 9 s más de Postgres por binario) la hacen
    más probable.
    ✎ En la ampliación (sobre `2677cb8`) el `broken pipe` de `callback_body_adversarial` volvió a salir, esta vez contra el
    **nuevo**: 2 de 7 pasadas en el día, una por binario. La carrera de D-F7-9 no volvió a aparecer (0 de 3).

15. **(F8-02, `engine`) Un comentario del viejo no casa con su código, y cinco conductas se portan tal cual, cada una con su caso**:
    `flujos/engine/engine.go:272` dice «No muta el estado recibido», pero `Step` escribe `VarContentRaw` en el mismo mapa de `Vars`
    del llamante (`:305-309`) y `SetOutcome` sella en el mapa que devolvió el módulo (`:350`): el contrato nuevo lo dice. Portado y
    fijado, **no** arreglado: un módulo que devuelve `Result{}` vacía las `Vars` de la conversación (`:324`); si el render del destino
    falla, los efectos del módulo se devuelven igualmente junto al error, con el estado ya en el nodo roto (`:355-356`); con
    `handled=true`, `EnterPrimed` no barre la señal de intención (`:204-207`: si un `Primer` consume y no limpia, `intent_params` llega
    al `Save`); el veredicto que acompaña a un error del resolutor se descarta, y con trozos y un solo `Code` el desenlace es
    `parcial` (`consulta.go:167-190`). Sin caso: la rama `if err != nil` de `EnterPrimed` (`:202-203`) es **muerta** (`tryPrime`
    devuelve siempre `nil`), y `"message"` es inline en el render pero los predicados de durabilidad sí consultan el `Registry`
    (`:383` frente a `:124`).
16. **(F8-02, `menu`/`survey`/`media`) Rarezas del viejo portadas tal cual y fijadas con caso**: el reprompt, la ayuda y el menú de
    salida re-emiten `node.Prompt` y no el `content.Prompt` que emitió `Render` (`menu.go:46,62`, `survey.go:53,74`): con contenido
    no estático el cliente vería dos textos distintos; `survey.Step` no rechaza un `QuestionID` vacío (`survey.go:77`) y descarta en
    silencio las respuestas previas que no son string (`:101-107`); `Projector.Project` no mira `eff.Name` ni `eff.Kind` y escribe
    la fila aunque `meta.EventID` esté vacío (`projection.go:42-55`; el gemelo la acepta y en Postgres la rechazaría el CHECK de la
    0054: no comprobado contra Postgres aquí); `NewProjector(nil)` se acepta y haría panic al proyectar (`:28`, dicho en el contrato,
    sin test); `media.Step` devuelve el mismo mapa `conv.Vars` sin clonar (`media.go:67-69`; hoy inocuo, el engine no lo invoca) y
    su validación es sensible a mayúsculas y no cruza `kind` con `mime` (`:84-103`). **Ningún test viejo de `survey` dependía de
    D-F8-7/D-F8-8**: el proyector solo usa `InsertResults`.
17. **(F8-02, `turnoacotado`) Encaja con el `llmvia` nuevo sin adaptador, y tres rarezas portadas con su caso.** El puerto `Turner`
    lo satisface `*llmvia.Selector` tal cual (aserción de compilación): F8-06 cablea `turnoacotado.New(<selector>)` y `turneroBridge`
    muere. Portado, no arreglado: una consulta de **cantidad** con trozos se trocea como elección y gasta hasta 3 inferencias sin
    poder resolver ninguna (`turnoacotado.go:123`, `troceado.go:118`); la vía API a mitad del troceado **descarta lo ya resuelto**
    (`troceado.go:149-153` devuelve solo `sin_resolutor`; su comentario dice «nunca hay nada parcial» y el código no lo garantiza);
    el texto del cliente y los rótulos entran en el prompt **sin escapar** (`prompt.go:189-203`; la validación de rango en Go acota
    el daño a elegir una opción válida equivocada). 🟡 Las tres son candidatas a decisión de Jhoan; ninguna bloquea.
18. **(F8-02, método) `engine` y `turnoacotado` nacieron en paralelo y coincidieron en el puerto** (`ResolveQuery`, misma firma) sin
    que nadie se lo dijera a los dos: fue suerte. Queda atado con una aserción de compilación (`4ce435f`). Para F8-03…F8-05: cuando
    dos paquetes en paralelo comparten un puerto, el nombre se fija en el prompt de los dos sub-agentes.
19. **(F8-02, E-13 y gates) `engine/engine.go` nace con 520 líneas** (viejo: 443; la diferencia son los comentarios de contrato), en
    la tolerancia 500–600; no se parte. Los verdes de `engine` (2 ficheros) y de `turnoacotado` (3) van **en un commit por paquete**,
    no por fichero: se llaman entre sí y ningún orden parcial pasa el lint `unused`. El `G` se corrió una vez sobre la cabeza
    integrada, no por commit; cada sub-agente corrió `test -race`, vet, lint y candados de su paquete. Los tests de reloj de
    `troceado` usan `testing/synctest` en vez del `time.Sleep` real de 400 ms del viejo.
20. **(F8-02) Tres documentos de la fase nombraban el engine con los nombres viejos** (`diseno.md` §2.1, `arquitectura.md` §4,
    `reglas.md` T-6: `WithConsultaResolver`, `ObservadorConsulta`, `Desenlace…`): corregidos con ✎ al cerrar la sesión; la
    correspondencia completa está en [`tareas.md`](tareas.md), antes del bloque 3.

21. **(F8-03, frontera; ✅ D-F8-10, Jhoan, 2026-10-09) El hallazgo 3, medido y decidido.** El `cart` viejo no importa ningún
    paquete de catálogo: el catálogo vivía **dentro** de él (`cart/catalog.go`, hoy `modulos/catalogo`, F5). El nuevo usa solo el
    paquete raíz (`Catalog`, `Category`, `Article`, `Variant`, `ParseCatalog`, `HasVariants`, en 9 ficheros) y nunca
    `catalogo/indice`. Queda: `"catalogo"` en `Capas["conversacion"]` y un test propio en `fronteras_test.go`,
    `TestConversacionDoesNotImportCatalogIndex`, sin ampliar el motor; comprobado por mutación (`fronteras_test.go:213`).
    `Capas` tiene ahora `conversacion → catalogo` y `catalogo → conversacion` a la vez: no hay ciclo de paquetes.
22. **(F8-03, `events`, E-13) Dos particiones más de las previstas.** `store.go` nace en cuatro (`store`, `store_list`,
    `store_filter`, `store_append`) y `summary.go`, que con su lógica medía 646 líneas, en dos (`summary.go` 498 +
    `summary_render.go`: `Encode` y `Render`). `events` son **10** ficheros de producción, no 7; los 114 exportados no cambian.
    Por el lint `unused`: `menu` y `dispatcher` van verdes en un commit; `eventColumnsE`, `scanRescuable` y `nullableID` nacen en
    `store_list.go`, no en `store.go`.
23. **(F8-03, `events`) Comentarios del viejo que no casaban con su código, corregidos en el contrato, y rarezas portadas tal
    cual.** `flujos/events/menu.go:268` dice que cada cierre va separado por una línea en blanco y solo el primero la lleva (con
    más de cinco rescatables, «…y N más» queda pegado al cierre; fijado en `BuildRescue`); `summary.go:365,546` nombran un
    `BuildSummary` que no existe (es `LoadSummary`); `events.go:144` dice «hoy nadie filtra `entry_kind = 'message'`» y lo hace
    `ListPastedByOwner` (`thread_reader.go:204`). Portado y fijado, **no** arreglado: `ListRescuable` y `ListEvents` fallan con el
    texto «events: recorrer eventos **vivos**» (`store.go:718`, el `collect` compartido); un id inexistente en `TransitionEvent`
    da `ErrNotOpen`, no `ErrEventMissing`; `Touch` refresca también un evento terminal; `AppendDecision` no devuelve el `seq`.
24. **(F8-03, `events`) La suite y sus mutantes.** `eventshelpertest.Contrato`: 51 casos (4 de carrera: único parcial, CAS de
    estado, `Touch` contra transición, numeración del historial con 4 escritores porque el adaptador reintenta 5 veces). 56
    mutantes del adaptador contra Postgres: 55 muertos y **1 equivalente** (quitar `entry_kind='message'` de `ListPastedByOwner`:
    la única puerta que escribe `origin='owner_pasted'` clava ese grado). No hay mutante de «quitar el único parcial»: vive en la
    migración 0051; lo mutado es su traducción a `ErrAliveExists`. 🟡 El test viejo
    `TestIntegration_MarcaVencidoSinFilaUsaElMismoDefaultQueLaBD` comparaba el 7200 del SQL con el `DEFAULT` de la columna; el caso
    nuevo (`ListRescuable_StaleIsAMarkNotAFilter`) fija el 7200 pero ya no lo compara con el esquema: candidato a caso de P10.
    `test/procesos` solo puede nombrar `events.New…` (candado `ProcessImports`): de ahí los alias `eventshelpertest.Event` y
    `ClockOption`.
25. **(F8-03, `cart`, rojo) Desvíos del reparto y rarezas que el verde tiene que portar.** `cart.go` entra en el commit de T8.15
    (no en el de T8.16) y nace en cuatro (`cart` con `Step`, `cart_levels`, `cart_notes`, `cart_navigation`). De los cuatro
    «simples» solo `validate.go` nace verde: `resume` lee el estado de `state.go`, `variants` no tiene quien lo llame y los
    auxiliares de `effects` solo se ven por `Step`. Rarezas del viejo, cada una con su caso ya escrito: una cantidad inválida no
    cuenta como inválido de opción y reinicia el contador (`cart.go:284`, `:497-500`); «2postres» no es número y el fuzzy lo
    perdona como errata (`preresolutor.go:173`); en la cantidad, un veredicto «0» pasa la aduana y significa volver
    (`consulta.go:164`, `cart.go:485`); el aviso de largo del checklist habla de «indicación» (`buyer.go:113`). 🟡 **Para Jhoan,
    sin bloquear**: un `item_added` reentregado tras el cierre reusa la solicitud **cerrada** de su evento y le reescribe las
    líneas (`projection.go:218-228`: la segunda pregunta no filtra por estado). Sin llamante de producción, ni viejo ni nuevo:
    `PriceListOf` y `RevalidationMessage` (`revalidate.go:16`). `projection.go:314,433` dicen que el dispatcher «loguea sin
    abortar» y `:253` que ya no es cierto desde D-054.4: no se afirma en el contrato; se comprueba al portar `runtime`
    (F8-04/05). Ningún test nuevo de `cart` depende de D-F8-7/D-F8-8. El `summary_test.go` viejo de `events` conducía el `cart`
    real para fijar la clave `"cart"`/`"level"` de `Vars`; el nuevo la fija con un literal, y el renombre al otro lado lo tiene
    que cazar el test de `cart`.
26. **(F8-03, método) No cupo en una sesión: 21 ficheros con dos complejos son dos bloques.** El rojo de `events` (con doble y
    suite) llevó ≈ 35 min y su verde con mutantes ≈ 26; el rojo de `cart`, ≈ 41 en paralelo. Los dos rojos validaron sus tests
    contra la lógica vieja como oráculo temporal (sin commitear) antes de portar, que es lo que hizo fiable el verde de `events`.
    El verde de `cart` (T8.25) queda para el relanzamiento.

## Orden de lectura

`README` → [`arquitectura.md`](arquitectura.md) (sobre todo §4 singleton y §5 puentes y adaptadores) →
[`reglas.md`](reglas.md) (niveles E-12 y trampas) → [`diseno.md`](diseno.md) §0.1 (niveles) y el paquete que toque →
[`requisitos.md`](requisitos.md) → [`tareas.md`](tareas.md).

## Bloques de sesión

Siete sesiones, **todas 💻**: cada una en **su rama partida de `dev`** y con PR a `dev` sin squash (regla 6 del `CLAUDE.md`, 2026-10-03; ✎ corregido en F8-01: aquí decía «sin PR, `git push origin dev`»).
Cada una es un bloque de 45–90 min (objetivo, **sin medir**) y cierra con las tres cosas: tareas `[x]` con SHA,
bloque en `ESTADO.md`, hallazgos nuevos aquí. Fichas en [`../sesiones/`](../sesiones/README.md).

| Sesión | Bloque | Nivel E-12 (provisional) | Tareas | Punto de parada |
|---|---|---|---|---|
| F8-01 | inventario E-12 y hojas | medio · `store` complejo · `content` simple | T8.1–T8.8, T8.22 | inventario **aprobado por Jhoan** (antes no se escribe código); `model`·`trigger`·`content`·`store`·`modules` verdes; suites `Contrato` en memoria y en Postgres |
| F8-02 ✅ | motor | medio · `menu`/`media`/`survey` simple | T8.9–T8.11, T8.14, T8.23 | `engine`·`menu`·`survey`·`media`·`turnoacotado` verdes (2026-10-09, rama `reorg/f8-02-motor`, `d8fd4ac` … `4ce435f`, PR #61 en `dev`, merge `df340a6`) |
| F8-03 🔧 | `events` y `cart` | medio · `events/store` y `thread_reader` complejo | T8.12, T8.15–T8.17, T8.24, T8.25 | `events` (7) y `cart` (14) verdes; goldens idénticos; candado de orden verde y mutado — **a medias (2026-10-09, rama `reorg/f8-03-events-cart`, `8c819b40` … `790ca522`)**: `events` verde, `cart` en rojo; falta T8.25, se relanza |
| F8-04 | `runtime` (1): contratos de los 23 y soporte | complejo | T8.18–T8.21, T8.26 | 23 contratos en rojo, candado de rachas escrito, los 12 de soporte verdes |
| F8-05 | `runtime` (2): núcleo | complejo, con mutantes | T8.27, T8.28 | los 11 del núcleo verdes, mutantes muertos, `pendiente` del runtime = 0 |
| F8-06 | la cara HTTP y conmutar | medio (`admin`, `apipublica`) | T8.13, T8.29–T8.35 | `admin` y handlers I1–I19 verdes; huella igual; 0 puentes (import), 0 adaptadores, `Conmutados` completo |
| F8-07 | cierre | — | T8.36–T8.38 | definición de hecho de [`reglas.md`](reglas.md) §4 entera |

Dos ajustes sobre el reparto por paquetes, por dependencias de compilación (medido en el código viejo):
`admin` importa `runtime` (`handlers.go:24,306,308`), así que no puede nacer antes que sus contratos y va con la cara
(F8-06); y `send.go`, `thread.go` y `welcome.go` cuelgan de `*Runtime`, así que F8-04 escribe los contratos de los
**23** antes de poner verdes los 12 de soporte.

## Decisiones que necesita (de Jhoan)

| # | Pregunta | Recomendación |
|---|---|---|
| **D-F8-1** | ¿`conversacion/model` (hoja, 1 fichero, 406 l, 24 exportados, **cero** imports internos) se reconstruye en **F5** o aquí? | **En F5.** Evita el puente `catalogo → flujos/model` y su re-toque aquí (2 ficheros: `catalogo/catalog.go`, `catalogo/indice/cache.go`). Si F5 ya lo dejó como puente, T8.3 lo reconstruye y T8.31 re-toca esos dos |
| **D-F8-2** | `flujos/admin.Register` (`handlers.go:344`) solo lo llaman tests (deuda D-7). ¿Se reconstruye? | **No.** Las dos rutas se montan inline en el arranque (`rutas_admin.go:100,102`); `handlers.go` nuevo exporta solo lo que usan `apipublica` y `rutas_admin.go`. Se dice en el commit (E-8) |
| **D-F8-3** | Los goldens `modules/cart/testdata/cart_v{1,2}_transcript.golden.txt` (285 + 136 l) fijan byte a byte las pantallas del carrito. ¿Se copian como **fixture** del test nuevo de `screens.go`/`cart.go`? | **Sí**: son datos observables, no tests. Copiados tal cual, sin `-update` en el primer verde |
| **D-F8-4** | La regla «`events` no depende del clasificador» (`events/summary_test.go:784`, REQ-21) es un test de imports. ¿Dónde queda? | En **`internal/modulos/fronteras_test.go`** (es una regla de frontera: `conversacion/events` no importa `inferencia/**` ni `captacion/intentcfg`), no como AST en `events` |
| **D-F8-5** | El ciclo de negocio `conversacion ↔ captacion` y `conversacion → solicitudes`, `solicitudes/…/telemetria → conversacion` (D-7, congelado) queda como **aristas permitidas** entre módulos nuevos | **Sí**, en la lista blanca de `fronteras_test.go`, arista a arista (§5 de [`arquitectura.md`](arquitectura.md)). Cortarlo es otro plan |
| **D-F8-6** | Deudas D-16 (`writeJSON` ×5) y D-17 (`rows.Close` ×41, 4 en `store/repository_postgres.go`) | **Se portan como están** en F8. Arreglarlas es cambiar dos cosas a la vez |

## Heredado de otras fases

- **D-F7-9 (Jhoan, 2026-10-08; hallazgo 17 de [F7](../F7-captacion/README.md))**: el agregador pone el job en `pending`
  (`CloseWindow`, `aggregator.go:892`) y solo después compone y escribe el literal (`ComposeAtFlush` → `PutSourceText`,
  `aggregator.go:906`, `source_composer.go:347-377`), en dos sentencias sin atomicidad. Si el worker del pipeline reclama
  el job en ese hueco, queda `failed` sin reintento. F7 portó el worker **tal cual**; **el arreglo es de esta fase**, al
  reconstruir el agregador y el compositor: que cierre y sobre sean un solo acto, o que el sobre preceda a la
  visibilidad (hoy `PutSourceText` exige `status='pending'`, así que invertir el orden toca esa guarda). Es una
  divergencia deliberada del viejo: va en su propio commit y con su caso en el proceso P4 de F9.

## Contradicciones encontradas (medidas contra el código)

1. **`04` §2.2 y `05` §4 («dos agregadores con ventanas en memoria partirían las ráfagas»)**: las
   ventanas **no** viven en memoria: son filas `intake_jobs` en `aggregating`, y el cierre es un
   `UPDATE … WHERE status='aggregating'` idempotente (`runtime/aggregator.go:674-681`, «este
   agregador no tiene un mapa de ventanas en memoria que salvar»). Lo que sí vive en memoria y se
   parte con dos instancias: el `keyedMutex` por conversación, el limitador de respuestas, el
   contador de rachas, el semáforo de entrantes, las **pistas** `dueNow` y el `seen` del agregador
   ([`arquitectura.md`](arquitectura.md) §4). La conclusión (un solo `Runtime`) se sostiene; el
   motivo escrito no.
2. **`05` E-6** lista 12 paquetes con Postgres sin gemelo en memoria y **omite** los de este módulo:
   `events` (`events/store.go`, 1.060 l, más `thread_reader.go`, solo Postgres) y dos adaptadores
   del runtime (`runtime/self_numbers.go`, `runtime/tenant_resolver.go`). Necesitan doble nuevo
   (§3 de [`diseno.md`](diseno.md)). El conteo de `05` parece hecho por nombre de fichero
   (`*postgres*.go`).
3. **`04` §3 `conversacion/admin/sessions.go`**: no se reconstruye aquí; nace en F3 como
   `apipublica/sessionadmin.go` (D-FX-2 de FX).
4. **`05` §3.2** trata `events/summary_test.go` como candado: de sus 26 `Test*` solo **uno** es
   estructural (`TestPaqueteEventsNoDependeDelClasificador`, `:784`); el resto son de conducta
   (INV-13, resumen durable) y van al test normal de `summary.go`.
5. **`04` §2.3 F8** («~25k l de prod, ~48k de test»): medido **23.901** (sin `catalog`, `note`,
   `sessions`, `contact`) y **47.057**. Coincide en orden.
6. **FX** asigna a F8 las 4 rutas de catálogo (I14–I17) aunque su dominio es F5: escriben por
   `flujos/store` (`publicapi/catalogimport.go:36-38`). Coherente con este análisis.
7. ~~**`F9-procesos`**: T9.22–T9.29 frente a T9.34~~ — **no era contradicción** (validación de
   coherencia, 2026-09-29): F9 `arquitectura.md` §5.3 anula T9.22–T9.29 **solo si D-F9-1 = no**.
   Con la recomendación (D-F9-1 = sí) la pasada de esta fase es **T9.29** (9C de `conversacion`) y
   T9.34 es la alternativa.
