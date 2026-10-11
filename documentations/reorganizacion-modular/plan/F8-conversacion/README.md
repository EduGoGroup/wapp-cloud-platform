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
    `summary_render.go`: `Encode` y `Render`). `events` son **11** ficheros de producción (✎ 2026-10-10: aquí decía 10; el rojo `8c819b40` trajo 10 y `summary_render.go` nació en el verde `6590724e`), no 7; los 114 exportados no cambian.
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
    El verde de `cart` (T8.25) queda para el relanzamiento. ✎ Hecho el 2026-10-10: hallazgo 27.
27. **(F8-03, `cart`, verde) T8.25 cerrada en el relanzamiento: lo portado, lo decidido y los desvíos.** Rama
    `reorg/f8-03b-cart-verde` desde `dev` @ `8b841c8d`, `dd0771ce` … `44255a2b`. `cart` queda en **20** ficheros de producción
    (17 + 3: `projection.go` nace partido en cuatro por E-13, `projection` 268, `projection_lines` 222, `projection_close` 138,
    `projection_buyer` 68); `troceo.go` queda en 568 líneas, en la tolerancia, sin partir. Decidido al portar: `loadCatalog` nace
    no exportado en `cart/state.go` (`catalogo` no lo exporta); `cloneVars` se sustituye por `modules.CloneVars` (mismo cuerpo);
    las notas usan `SanitizeNote`, `NoteTooLongError` y `MaxNoteRunes` de `modulos/solicitudes/intakes`; los no exportados en
    español pasan al inglés por E-11 (`preresolveOConsulta` → `preresolveOrQuery`, `troceado` → `chunked`, `opcionesDelNivel` →
    `levelOptions`, `codeVolver` → `codeBack`) con los valores observables intactos (`"ninguno"`, `"troceo"`,
    `"troceo_perdido"`, `"producto"`, `"cantidad"`); el local `max` de `moreCode` (`screens.go`) pasa a `highest` por no sombrear
    el builtin. Desvíos del plan del verde: un commit previo de lint (hallazgo 28); `prime.go` entra en el primer commit verde
    (13 ficheros, no 12) porque `cart_levels_test.go`, `cart_notes_test.go` y `consulta_test.go` usan la constante
    `continueBebidas` de `prime_test.go`, y el traslado de `TestWithLogger_PrimeWarnsAboutDiscardedFields` a `prime_test.go` va
    ahí, cuerpo intacto; `projection.go` partido. Goldens `cart_v1` y `cart_v2` verdes **sin** `-update` y `cmp` rc=0 contra los
    del viejo; el candado `TestOrder_QueryIsRaisedBeforeAnyMutation`, sin etiqueta y verde, y rojo al mover el bloque
    `preresolveOrQuery` + `return` bajo `st.Started = true` en el `Step` real (deshecho sin commitear); mutantes de la
    proyección, declarados por el sub-agente: 84 válidos, 84 muertos, 0 equivalentes. 🟡 El `item_added` reentregado del
    hallazgo 25 está **portado tal cual**: sigue para Jhoan, sin decidir.
28. **(F8-03, método y gates) 🟡 El lint del repo no mira los tests que llevan `//go:build pendiente`.** Solo compila con la
    etiqueta `integracion`, así que los avisos de un test en rojo aparecen **de golpe** al quitarle la etiqueta en el verde: 17
    aquí (10 `errcheck` de aserción de tipo, 3 `ST1018`, 2 `gocritic` `mapKey`, 2 `gocyclo`, en 10 ficheros). Se silenciaron con
    `//nolint:<linter> // motivo` en un commit aparte y previo (`dd0771ce`), sin cambiar ninguna aserción ni dato. ✎ **Decidido por
    Jhoan el 2026-10-10: «limpio» es sin avisos silenciados.** Los 17 se arreglan de verdad en `d3f6c31b` (12 ficheros de test):
    dos helpers `mustBe[T]` e `itemsOf` comprueban las aserciones de tipo; los tres invisibles (U+200B, mirados con `xxd`) pasan
    a `\u200b`; los dos mapas con claves con espacios, a listas de casos; y los cuerpos de los subtests de los dos tests largos,
    a funciones con nombre. Mismos 187 tests y 470 PASS, con la lista de nombres idéntica antes y después; en `cart` queda un
    solo `//nolint` (`helpers_test.go:100`, `gosec` G304, anterior). Las demás supresiones del repo: deuda **D-31** de
    [`deuda.md`](../../../deuda.md) y T10.23 de F10. Para F8-04 y F8-05: correr el lint sin etiqueta sobre los tests antes del
    verde. Y al contar: `grep -rn 'pendiente.Implementar'` sobre `conversacion` da **4**, las cuatro en
    `orden_consulta_ast_test.go:109,181,201,220` (texto del propio candado), y **0** en producción.
29. **(F8-04, método; decisión de Jhoan, 2026-10-10) F8-04 se parte en dos sesiones antes de lanzarla (D-F8-11).** En vez de
    lanzarla sabiendo que no cabe y relanzarla, queda en **F8-04** (los contratos: T8.18–T8.21, todo el rojo de `runtime`) y
    **F8-04b** (el verde de los 12 de soporte: T8.26), por la frontera que ya tenía el bloque 4 de [`tareas.md`](tareas.md).
    Motivo medido: F8-03, con 21 ficheros y dos complejos, no cupo en un bloque de 90 min y se hizo en dos mitades (≈ 75 + ≈ 20
    min; hallazgo 26); F8-04 es de nivel complejo, con 23 contratos, dobles, dos suites contra Postgres y 12 ficheros en verde
    con mutantes. F8-05 pasa a depender de F8-04b. Las tareas no cambian de texto ni de número. Como F8-04 queda toda de rojo,
    el lint del hallazgo 28 se le pasa a los tests etiquetados antes de cerrarla, con `--build-tags pendiente` (la etiqueta se
    suma a la `integracion` del `.golangci.yml`; el comando, en la ficha).
30. **(F8-04, `runtime`, rojo) Lo que el diseño contaba mal, medido al escribir los 23 contratos.** Las `With*` del runtime son
    22, pero **21** viven en `runtime_engine.go` y la 22.ª, `WithWelcomeStore`, en `welcome.go`; `WithConsultaResolver` nunca fue
    del runtime: es del engine (ya `engine.WithQueryResolver`, F8-02), y T-6 queda escrita en el comentario de `New`. `resume.go`
    exporta **1** símbolo (`ErrTurnCutBySinkFailure`), no 0; `event_effects.go` son 7 efectos y 3 motivos de escape;
    `source_composer.go` exporta además `Composed`, `ComposeSourceText` y `WithThreadLimit`. La fila de `tenant_resolver.go`
    («perfil vacío/desconocido = activo») es la regla del **consumidor**: el adaptador, con un perfil fuera de dominio en la
    base, cae a **pasiva**, y la suite lo fija. [`diseno.md`](diseno.md) §4 corregido con ✎. Renombres por E-11:
    `ErrSelfNumbersSinKeyProvider` → `ErrSelfNumbersNoKeyProvider` (texto intacto) y el candado
    `TestRacha_TodoDeleteCierraElEpisodio` → `TestStreak_EveryDeleteClosesTheEpisode`; los demás exportados ya estaban en inglés.
31. **(F8-04, método) Cinco ficheros sin exportados y un ciclo de imports: cómo quedó el rojo.** `keyedmutex.go` y `streak.go`
    no tienen nada exportado ni se ven por la API pública sin el `Runtime`: su contrato es el comentario-promesa (KM-1…KM-6,
    ST-1…ST-29) y su `_test.go` es la lista de tests que trae el verde (F8-04b, con mutantes). `exit_menu.go`, `thread.go` y
    `send.go`, igual de vacíos, sí tienen tests: por `HandleIncoming` y `Start`. Los tests que montan un `Runtime` van en
    **`package runtime_test`** sobre un arnés común (`harness_test.go`, `harness_doubles_test.go`), porque `runtimehelpertest`
    importa `runtime` y un test interno que usara sus dobles cerraría un ciclo; el candado de exportados lo acepta. Dos tests
    nacen **sin** etiqueta (`runtime_test.go`, `event_effects_test.go`): sus ficheros son solo datos. Y el reparto de commits se
    desvía del de `tareas.md`: `welcome`, `thread` y `send` entran en el commit del núcleo (T8.19), no en el de soporte, porque
    su contrato cuelga de `*Runtime`.
32. **(F8-04, método) 🟡 Un rojo no se puede ejecutar: qué quedó validado contra el viejo y qué no.** Validados contra la lógica
    vieja como oráculo temporal, sin commitear: los 6 ficheros de soporte con exportados; las dos suites contra Postgres (33 PASS
    con testcontainers y 5 mutantes del SQL muertos); `persist_sink`, `source_composer` y `aggregator` (68 de 69 tests; fallan
    solo los 4 subcasos de D-F9-10, que es la divergencia buscada); y el candado de rachas (verde con 6 sobre una copia del viejo,
    rojo al quitarle un `Close`). **No ejecutados nunca contra lógica real**: los ≈ 260 tests que pasan por el arnés
    (`runtime_engine`, `start`, `resume`, `exit_menu`, `send`, `incoming`, `welcome`, `thread`, `events`, `event_lifecycle`);
    mueren en `runtime.WithClock` al montar. Compilan, pasan el lint con la etiqueta y están repasados a mano contra el viejo,
    nada más: F8-05 tiene que contar con corregir tests, no solo portar lógica. Al arnés le falta, y cada sub-agente lo resolvió
    en sus ficheros con un segundo `Runtime`: inyectar fallos en el almacén y en el de eventos, un `Ack` con `ok=false`, encender
    una feature antes del primer entrante y leer el cuerpo del hilo. Trampa: en cuanto se enciende una feature, el despachador
    real filtra `cart` por `cart_basic`; hay que encender las dos.
33. **(F8-04, `runtime`) El hallazgo 25, comprobado: las dos frases de `cart/projection.go` están caducadas.** El fan-out no
    «loguea sin abortar»: todo fallo de un sink se loguea a ERROR («runtime: sink de efecto falló»); si el efecto es de un módulo
    durable **y** el error lleva `ErrMaterializationFailed`, un error permanente de Postgres corta de inmediato y cualquier otro
    se reintenta 2 veces más con 25 ms; agotado, se corta el turno (`flujos/runtime/resume.go:268-339`, `persist_sink.go:164,188`):
    sin Save, sin hilo, sin ventana, y el cliente recibe el aviso de avería. Como el carrito es durable, un fallo de su
    proyección corta el turno. Escrito en el contrato de `resume.go` (FO-1…FO-7) y en RT-10. Más comentarios del viejo que no
    casan con su código, corregidos en el contrato nuevo: `event_lifecycle.go:380,387` (el fan-out va **antes** del Save);
    `runtime_engine.go:195,452` (los motivos de corte son cuatro, no tres); `incoming.go:85-92` (sin estado no «ignora»: consulta
    los disparos); `aggregator.go:457-460` (con `settings` nil `Observe` funciona; con `log` nil es no-op); `streak.go:107`;
    `tenant_resolver.go:18,34`; `persist_sink.go:125-131` (solo se llama al **primer** proyector que acepta el efecto).
34. **(F8-04, `runtime`) 🟡 Rarezas del viejo portadas tal cual al contrato, cada una con su test; candidatas a decisión de
    Jhoan, ninguna bloquea.** (a) El sobre de P2 **no se escapa**: un cliente que teclea las cuatro cabeceras las deja dentro del
    bloque de mensajes y puede simular un bloque de contexto (`source_composer.go:208-261`; fijado en el corpus adversario).
    (b) El agregador anota el id como visto **antes** de `OpenOrAppend`: si la sentencia falla, la re-entrega del mismo mensaje se
    descarta (`aggregator.go:511-525`). (c) El reintento del sink durable usa `time.After(25 ms)` **real**, no el reloj
    inyectado: nueve tests esperan 25–50 ms de verdad (`resume.go:351`); y repite el `Handle` entero, así que puede duplicar la
    fila de `flow_events` (`:304-315`). (d) `WebhookSink` no tiene por dónde inyectar reloj (`webhook_sink.go:66`). (e)
    `WithEventSink(nil)` y `WithResumePolicy(tipo, nil)` son un panic diferido; RT-12 no los exceptúa y no llevan test. (f) El
    resolver de tenant no filtra por `state`: una sesión `loggedout` resuelve y pesa en el perfil. (g) `Start` no cobra token del
    limitador pero sí cuenta en la racha (`start.go:321-331`). (h) Las claves `dispatcher_menu` y `dispatcher_menu_event_id` de
    `Vars` son estado persistido que el binario nuevo tiene que leer: hoy las fijan los tests de `events`, no el contrato.
    (i) Con dos proyectores para el mismo efecto gana el primero, en silencio.
35. **(F8-04, D-F9-10 y D-F7-9) Lo prometido y lo dejado al verde.** D-F9-10 está en el contrato del agregador con su caso:
    con el contexto cancelado, listar, cerrar y componer no loguean a ERROR (en `Sweep`, y por tanto en `RecoverAtBoot` y `Run`).
    No se prometió callar el WARN de los plazos, ni cortar el barrido a medias, ni un INFO de apagado. D-F7-9 **no** se arregla ni
    se promete aquí: el contrato anota la deuda en `Sweep` y en `ComposeAtFlush` y ningún test fija el orden cierre/sobre, para
    no bloquear el arreglo de F8-05; solo `ComposeAtFlush_DoesNotOverwrite/window_not_pending` cambiará si el verde invierte el
    orden. Los tests de `OnIncoming` y del semáforo usan `testing/synctest` con `<-time.After` dentro de la burbuja (no es el
    reloj del arnés: el plazo del entrante es un `context.WithTimeout`); ningún `time.Sleep`.
36. **(F8-04, método) Partida, cupo en un bloque: ≈ 80 min de pared** (✎ 2026-10-10: aquí decía ≈ 65; fueron 09:48–≈11:10)**.** Siete sub-agentes en tres olas dentro del mismo
    paquete y el mismo checkout, sin *worktrees*: soporte y dobles (≈ 17 min, en paralelo), contratos del `Runtime` y
    agregador (≈ 23), tests del `Runtime` y candado (≈ 21). Lo que lo hizo posible: escribir primero la producción de los 10
    ficheros que cuelgan de `*Runtime` y un arnés común, y repartir después sus tests por fichero con prefijos propios. Lo que
    costó: cada ola veía fallos de compilación y de lint de las otras mientras escribían, y un sub-agente ensució `go.sum` con
    `-mod=mod` (revertido). El paquete nace con 23 ficheros de producción, 52 de test y 391 `func Test`.
37. **(F8-04b; ✅ D-F8-12, Jhoan, 2026-10-10) El verde del soporte son 9, no 12, y `runtime.go` no lleva commit.** `welcome`,
    `thread` y `send` cuelgan de `*Runtime` y sus tests pasan por el arnés, que muere en `runtime.WithClock` con el núcleo en
    rojo: pasan a F8-05 (T8.27, que queda en 14 ficheros) con su etiqueta. `runtime.go` nació declarado y con su test sin
    etiqueta en F8-04: no había nada que portar. Siete commits verdes (`83565b4e` … `6216d1df`); `make test-pendiente`:
    `PENDIENTES=50 · ROJOS=40` (eran 60 y 50). Las 50 llamadas viven en **8** ficheros del núcleo (`aggregator` 12,
    `runtime_engine` 23, `source_composer` 5, `persist_sink` 3, `event_lifecycle` 2, `incoming` 2, `events` 1, `start` 1) y en
    `welcome.go` (1); `event_effects.go`, `resume.go` y `exit_menu.go` no tienen ninguna (datos, o sin exportados con cuerpo).
    Quedan 40 `_test.go` del paquete con `//go:build pendiente`.
38. **(F8-04b, `keyedmutex`, `streak`, `event_sink`) Los tres sin llamante de producción: el lint `unused` los da por usados
    con sus tests.** No hizo falta parar. `keyedmutex.go` (107 l, 6 tests), `streak.go` (445 l, 22 tests en dos ficheros:
    `streak_sweep_test.go` nace por E-13 con el tope y `Max`), `event_sink.go` (160 l; `sortSinksByPhase` y `phaseOf` con 3
    tests nuevos, más los 6 que ya pasaban). Mutantes: `keyedmutex` los 4 de la lista y 1 extra, muertos; `streak` 16 muertos
    (los de la lista y 7 extra: el `Before` del desalojo, el `delete` de `Close`, desalojar la más reciente…) y **1
    equivalente** (`e.n > longest` por `>=`); `event_sink` 3 de 3. Aprendido: (a) `testing/synctest` no sirve para el candado,
    porque un bloqueo en `sync.Mutex` no es «durable» para la burbuja: la espera se observa con el conteo `ref` bajo el candado
    global y canales, con un `time.After` de 10 s solo como vigilante (no es una pausa: con el código correcto nunca vence);
    (b) el mutante de ordenación **no estable** solo muere con más de 12 elementos (por debajo Go ordena por inserción, que es
    estable): el caso usa 60 sinks; (c) 🟡 el mutante extra «retener el candado global mientras se espera el de la clave»
    (KM-5) muere porque cuelga el binario y lo corta `-timeout`, no por una aserción con nombre; (d) KM-6 (unlock de un solo
    uso) no tiene test propio: la doble llamada es un fatal de Go; (e) cuatro de los mutantes extra de `streak` están en el
    bloque de `ESTADO.md` y no en el comentario-registro del `_test.go`. Tres avisos de lint en los tests, arreglados sin
    silenciar (`prealloc`, una asignación muerta, `string(rune(…))` → `strconv.Itoa`). Renombres E-11, solo no exportados de
    `streak.go`: `rachas` → `streaks`, `cerradas` → `closed`, `viva` → `alive`, `corte` → `cutoff`, `mayor` → `longest`,
    `masVieja` → `oldest`. 100 repeticiones con `-race` de los tests de los tres: rc=0.
39. **(F8-04b, sinks y adaptadores) Ningún test corregido; un doble que quedaba sin uso a mitad de camino; la rareza 34f,
    fijada por mutación.** Los tests de `log_sink`, `webhook_sink`, `summary_sources`, `tenant_resolver` y `self_numbers`
    pasaron contra la lógica portada a la primera (hallazgo 32: estaban validados contra el viejo). `log_sink` va antes que
    `webhook_sink`, y en ese commit intermedio el método `sinkLogRecorder.at` no tenía quien lo usara sin etiqueta (`unused`):
    se arregla con una aserción redundante en `TestLogSink_Handle_LogsOneInfoLineWithMetadata`, no moviendo el doble (lo usan
    también los tests del agregador). Los dos adaptadores van en **un** commit con `postgres_fakedb_test.go`, sus dos tests
    unitarios y las dos suites de `test/procesos` (ya solo `integracion`): 13 + 18 casos con testcontainers, rc=0, 0 SKIP.
    Nueve mutantes del SQL, nueve muertos; entre ellos, **añadir `AND state <> 'loggedout'` al resolver** (el «arreglo» del
    hallazgo 34f) lo mata `AnyState_ResolvesTheSame`: si Jhoan decide filtrar, ese caso cambia con la decisión. 34d sigue igual:
    `NewWebhookSink` no inyecta reloj. Comentario del viejo no portado: el del resolver nombra la columna legada `role` y un
    `DISTINCT tenant_id`; la consulta usa `GROUP BY` y la columna ya no existe. Las suites se corrieron con
    `WAPP_PROCESOS_BINARIO=nuevo` (no levantan servidor).
40. **(F8-04b, método) Tres sub-agentes en el mismo checkout sin commitear, y el «rc=0 por commit» medido después.** Cada uno
    con ficheros disjuntos (≈ 6, ≈ 7 y ≈ 12 min, en paralelo); el orquestador hizo los siete commits y comprobó **cada uno** en
    un *worktree* temporal fuera del repo: `go test -race`, `go vet -tags pendiente` y el lint sin etiqueta y con
    `--build-tags pendiente`, 28 de 28 en rc=0. 🟡 Lo que costó: los mutantes se aplicaron sobre el checkout compartido y otro
    sub-agente vio dos pasadas del paquete en rc=1 (los tests de `keyedmutex`, con el fichero mutado en ese momento); no era un
    fallo real. Para F8-05: los mutantes, en serie o sobre una copia, nunca mientras otro mide en el mismo árbol. Y el
    scratchpad compartido: cada sub-agente, su subdirectorio.
41. **(F8-05, método) El verde de los 14 son cuatro commits, no catorce, y `runtime` queda en 40 ficheros de producción.**
    `persist_sink` (`46b6a1ec`), `source_composer` (`0c1d8973`) y `aggregator` (`0aaa18cd`) van solos: no cuelgan de `*Runtime`.
    Los otros **11** (`runtime_engine`, `resume`, `start`, `exit_menu`, `event_lifecycle`, `events`, `incoming`, `event_effects`,
    `welcome`, `thread`, `send`) van en **un** commit (`1e0f6009`): se llaman entre sí, los campos del struct `Runtime` solo
    tienen lector en los demás ficheros (lint `unused`) y todos sus tests pasan por el mismo arnés, que no monta hasta que `New`
    y las `With*` tienen cuerpo. Particiones E-13, solo moviendo declaraciones: `aggregator` → 5 (`_observe`, `_hint`, `_sweep`
    con `RecoverAtBoot` porque el candado de exportados lo exige en el test homónimo, `_run`); `runtime_engine` → 3 (`_pool`,
    `_streak`; el principal queda en 553 líneas, en la tolerancia); `event_lifecycle` → 2 (`_cancel`); `events` → 6
    (`_start_new`, `_switch`, `_summary`, `_clock`, `_menu`); `incoming` → 5 (`_advance`, `_trigger`, `_guards`, `_limiter`).
    Dos cosas que el rojo no había repartido: `observeForAggregation` vivía en el `aggregator.go` viejo pero es un método de
    `*Runtime`: nace en `aggregator_bridge.go`, con su test, y entra con el núcleo; y a `event_effects.go` le faltaban los tres
    emisores (`emitEventEffect`, `emitEventEscaped`, `emitEventEffectWithReason`), que no podían existir sin llamante.
    `incoming_aggregation_test.go` e `incoming_doubles_test.go` quedan sin fichero de producción homónimo (el candado no lo
    exige en ese sentido). El «rc=0 por commit» se midió después, en un *worktree* temporal: 36 de 36.
42. **(F8-05) El hallazgo 32, medido: de los ≈ 260 tests nunca ejecutados contra lógica real, estaban mal dos.** Con la
    lógica portada, el paquete dio 423 PASS a la primera. (a) `TestOnIncoming_SameConversationIsProcessedOneAtATime` usaba
    `testing/synctest` y se colgaba: es el hallazgo 38 otra vez (un bloqueo en `sync.Mutex` no es durable para la burbuja).
    Ahora observa la espera con el conteo del candado, por `ConversationLockRefs` en un `export_test.go` nuevo (solo existe
    en el binario de test), y vive en `incoming_lock_test.go`; quitar el candado de `HandleIncoming` lo mata por aserción con
    nombre. (b) `TestWithClock_GovernsTheConversationTTL` usaba un TTL de 1 h, mayor que la ventana de inactividad de la racha
    (30 min, `streak.go`), que la cerraba sola y daba `[1]`: TTL de 10 min, expectativas intactas (el TTL no tiene mínimo ni
    saneo, solo se aparta el `<= 0`). Ningún fallo real del porte. Lo que el arnés no tenía sigue resuelto con un segundo
    `Runtime` por test; repasados siete de esos montajes, ninguno afirma algo vacuo (sin mirar a fondo: encender una feature
    antes del primer entrante y la lectura del cuerpo del hilo). Los avisos de lint que el hallazgo 28 hacía temer: **0**, con
    y sin etiqueta; ningún `//nolint` en `C/runtime`.
43. **(F8-05, mutantes) 362 válidos, 357 muertos, 4 equivalentes y 1 que solo muere por `-timeout`.** Corridos en serie, cada
    tanda en su *worktree* fuera del repo (hallazgo 40). **Núcleo**: 187 válidos, 185 muertos, 2 equivalentes (`started = true`
    con el arranque cortado en `events_switch.go`; `due` sin mirar `active`, que su único llamante ya filtra); 12 supervivientes
    cerrados con test (`a6fb0215`): el candado de `Start`, de `CancelEventForTenant` y de `repairCancelled` no tenía quien lo
    viera (tres tests `…WaitsForTheTurnThatHoldsTheConversation`), la guarda de `UpdatedAt` cero del TTL, el arranque cortado
    que no se ofrece al agregador, el no-op de `beginEvent` que solo vale para `gestureGoTo`, el testigo de `MarkWelcomed`, y dos
    del hilo. Cada uno de los seis `Close` muere además por un test de conducta, no solo por el candado AST. **Agregador, sink y
    compositor**: 175 válidos, 172 muertos, 2 equivalentes; 11 cerrados con test (`f5fd0374`). 🟡 Lo que conviene saber:
    (a) `ceiling <= 0` → `< 0` **no** es equivalente, aunque el comentario del viejo lo sugiere: las fechas de la fila las pone
    el reloj de la base y el barrido usa el del proceso (`TestSweep_ZeroDeadlineIsExpiredWhateverTheClocksSay`); (b) los
    mutantes que quitan el candado de `seen` y de las pistas solo mueren con `-race`; (c) «no volver tras el error del
    resolver» en `Observe` solo muere con un doble que devuelve `(true, err)`, que viola el contrato de `entitlements.Resolver`:
    el test fija el *fail-closed* y se puede quitar si se juzga excesivo; (d) **sin arreglar**: `Run` con `return` → `continue`
    en `ctx.Done()` solo muere por `-timeout` (un `Run` que gira impide que la burbuja de `synctest` quede quieta); (e) el
    sobre-candado (clave solo con tenant, o sin contacto) lo delata de rebote una aserción que mira la clave buena, y un
    `-timeout`: no hay test de «dos conversaciones distintas no se esperan»; (f) `StartNewOfKind` **no** toma candado: los
    sitios son cuatro (`HandleIncoming`, `Start`, `CancelEventForTenant`, `repairCancelled`).
44. **(F8-05, D-F7-9 y hallazgo 34) 🟡 Nada de lo pendiente de decisión se decidió aquí: todo portado tal cual.** D-F7-9 (cierre
    → sobre en dos sentencias sin atomicidad) **sigue sin arreglar**: el encargo de la sesión no lo pedía, el arreglo toca la
    guarda de `PutSourceText` en `captacion/intake` y pide su caso en P4. Los comentarios de `Sweep` y `ComposeAtFlush`, que
    decían «lo decide el verde (F8-05)», dicen ahora que sigue pendiente (`6c9bd1f1`). **Hace falta que Jhoan diga en qué
    sesión va** (F8-06 conmuta el runtime; F8-07 cierra). ✅ **Decidido (D-F8-13, Jhoan, 2026-10-10)**: sesión nueva
    **F8-06b** (T8.39), entre F8-06 y F8-07: el sobre primero, y cierre y sobre en una sola sentencia; leído el código, no hay
    que tocar la guarda de `PutSourceText`. El mismo hueco del re-análisis (hallazgo 1) entra en F8-06b como segunda tarea,
    T8.40: el job nace con su sobre. Las rarezas 34a–34i siguen portadas y con su test: el sobre de P2
    sin escapar (✎ **34a decidida**: se deja, deuda **D-32** de `deuda.md`, para después de F10 y con medición, D-F8-18; el corpus gana cuatro casos adversarios: bloque de mensajes falsificado, bytes de control y UTF-8 inválido,
    clases casi iguales, saltos al borde; el corpus entero pasó contra el `ComposeSourceText` viejo), el id visto antes de
    `OpenOrAppend` (✎ **34b ya no: arreglada**, D-F8-14, `c44c212f`: si la sentencia falla, el id se desanota y la re-entrega
    vuelve a intentarlo; divergencia deliberada del viejo, con dos casos y dos mutantes muertos), el `time.After(25 ms)` real del reintento
    (✎ **34c, la mitad que duplicaba, arreglada**: D-F8-15, `6eb31a3d`, el reintento retoma donde falló y ya no repite el outbox ni
    el hilo cuando lo que falló fue la proyección; los 25 ms de reloj real siguen), los dos panic diferidos (✎ **34e
    arreglada**: D-F8-16, `a9da0105`, el nil se ignora) y `Start` sin token pero contando racha (✎ **34g decidida**: se queda y es deliberado, D-F8-17)
    (cobrar token en `Start` lo matan `TestStart_SkipsTheIncomingMachinery` y `TestStart_DurableFlowIsRejectedWithoutATrace`).
    D-F9-10 está en `logSweepError` (`aggregator_sweep.go`), con cinco mutantes muertos por
    `TestRun_ContextCancelled_ReturnsWithoutLoggingAtError`.
45. **(F8-05, `runtime`) Comentarios del viejo que no casan con su código, y lo no portado.** `aggregator.go:695-697` dice que
    el plazo se recalcula desde `message_ts`; el código usa `LastActivity` y `CreatedAt` y `:824` lo niega. `source_composer.go:85-87`
    sugiere un sobre robusto y no escapa nada. La cabecera de `beginEvent` describe el no-op sin nombrar el gesto, y el código lo
    limita a `gestureGoTo` (`events.go:303` del viejo, `:302` del nuevo). El docstring viejo de `HandleIncoming` («sin estado lo
    ignora») ya estaba corregido en el contrato. Varias referencias `fichero:línea` de comentarios portados apuntan al
    *layout* viejo (`events.go:543-567`, `config_resolver.go:171-180`): se dejaron literales. No portado: la guarda
    `hint == nil` de `intentTriggers` (rama muerta: recibe el valor) y los docstrings viejos de los exportados, donde manda el
    contrato. Renombres E-11, solo no exportados: `debe` → `due`, `textoDeBienvenida` → `welcomeText`, `destino` →
    `destination`, `despertar` → `wake`, `plazosDeVentana` → `windowDeadlines`, `venceElSilencio`/`venceElTecho` →
    `silenceExpired`/`ceilingExpired`, `plazosFor` → `deadlinesFor`, `errHilo` → `threadErr`, y locales. Import nuevo fuera de
    `modulos/` y `nucleo/`: `internal/platform/storage/postgres` por `IsPermanentFailure`, el mismo que usa `captacion/pipeline`.
    `make cobertura-ficheros` (informe): de `C/runtime` solo `events_summary.go` queda por debajo (76,3 %).
46. **(F8-05, método) Cupo en un bloque: ≈ 65 min de pared (≈ 12:40–13:45).** Lo que lo hizo posible: portar la producción en
    cuatro sub-agentes a la vez en el mismo checkout con ficheros disjuntos (sink ≈ 2 min, agregador ≈ 7, y las dos mitades del
    `*Runtime` ≈ 13 cada una) con una **regla de nombres común escrita en los dos prompts** (los no exportados que ya están en
    inglés conservan su nombre; los campos de `Runtime`, los del viejo; las traducciones, fijadas de antemano): encajaron sin
    hablarse (hallazgo 18). Después, un sub-agente para los tests y las etiquetas (≈ 6 min) y dos de mutantes en paralelo, cada
    uno en su *worktree* (≈ 23 y ≈ 26 min), que devolvieron un commit solo de tests cada uno, integrado con `cherry-pick`.
    🟡 Lo que costó: un sub-agente lanzó `pkill -f runtime.test` para matar su propia ejecución colgada, con otros midiendo en
    la máquina (no hubo daño que se viera); y los mutantes fueron lo más largo, con diferencia.
47. **(F8-06, método) Cupo en un bloque: ≈ 60 min de pared (≈ 14:55–15:55), con hasta cuatro sub-agentes a la vez.** Lo que
    lo hizo posible: (a) rojo y verde con **el mismo** sub-agente, reanudado tras el commit del rojo (conserva lo leído);
    (b) ningún sub-agente commitea: devuelve, y el orquestador hace un commit por fichero con rutas explícitas; (c) a los
    agentes que comparten paquete (`apipublica`), la regla de nombres escrita en todos los prompts —todo símbolo nuevo lleva
    el tema de su fichero— y el único auxiliar compartido (`tenantContentBytes`) con dueño, nombre y firma fijados de
    antemano: encajaron sin hablarse; (d) el plan de la conmutación lo midió antes un agente de solo lectura (tabla de
    sustitución con `fichero:línea`), y el ensayo en seco (T8.30) fue en un *worktree* propio mientras los verdes seguían en
    el checkout principal; después se trajo como un solo diff. Desvíos: el rojo de `apipublica` son **dos** commits y no
    uno (`flows.go` esperaba al contrato de `admin`); un agente cambió la palabra «independientes» de dos comentarios de
    `tenantcontent.go` para que el `grep pendiente` del gate diera vacío (uno era comentario portado: ya no es literal en
    esa palabra); y `make test-pendiente` se corrió con el *worktree* del ensayo aún sin borrar (dio `0 · 0`; se borró
    después). El «rc=0 por commit» **no** se midió en un *worktree* para los ocho verdes de `apipublica`: lo simularon los
    agentes para `admin` y el catálogo; el resto, solo por lectura de dependencias.
48. **(F8-06, `admin`) 15 exportados, no 16, y dos desvíos de forma.** La cifra de T8.13 contaba `Register`, que no se
    reconstruye (D-F8-2, deuda D-7); tampoco su test `TestRegister_RoutesBothEndpoints`. `triggers.go` nace partido en tres
    (`triggers`, `triggers_list`, `triggers_delete`; E-13) y los tres van en un commit porque el contrato de rojo los tenía
    en un fichero. `durableContent` vive en `triggers.go` y no en `durable_flow.go` como en el viejo: allí quedaba sin uso
    (lint `unused`). El comentario de paquete pasa a `doc.go`. Rarezas del viejo portadas y fijadas con caso: el prefijo
    duplicado del 400 («definición de flujo inválida: definición de flujo inválida: …», y lo mismo con `contact_ref`);
    `Types()` leído una sola vez, al construir `DefinitionHandler`; los tres handlers de disparadores no miran el método;
    `flow_id` y `session_id` de start viajan sin recortar; el listado pregunta al checker con el tenant de la fila y no con
    el del token; la baja lista siempre antes de borrar; D-16 (`writeJSON`) tal cual. Antes de portar, los tests del rojo se
    corrieron contra la lógica **vieja** con `go test -overlay`: 173 PASS. Mutantes: 22 válidos, 22 muertos. Las
    referencias `fichero:línea` de los comentarios portados no se reverificaron contra el árbol nuevo.
49. **(F8-06, `apipublica`) Los ocho ficheros, sus 19 rutas y lo que el verde corrigió del rojo.** `flows.go` monta **siete**
    (I1–I4 sin condición; I11–I13 solo si `Triggers != nil`), `media.go` una, `tenantcontent.go` cinco, los tres del
    catálogo I14–I17 (con `Content`, `ContentVersions` y `Entitlements`), y los dos de eventos I18 e I19 (cada uno con su
    puerto y `Entitlements`). Decisiones de forma: **un solo `CatalogImportDeps`** para los tres `Mount…` del catálogo (la
    condición de I16–I17 no puede divergir de la de I14) con su auxiliar `catalogImportMountable`; un puerto de lectura de
    un método para el import en vez del almacén de cuatro; `MediaDeps` del viejo partido en una `Deps` por fichero; I4
    **no** usa `admin.StartHandler` (el viejo tiene handler propio, con el `flow_id` en la ruta). `catalogimport.go` nace
    partido en dos (`_apply`). Corregido del rojo, y eran los tests o el contrato, no la conducta: (a) los errores de I1 e
    I11–I13 salen en **texto plano** (`http.Error` de `admin`), no como `{"error":…}`; el 401 y el 403 de la cadena sí son
    JSON; (b) el GET por ref de contenido devuelve el blob compactado y con `<`, `>`, `&`: el literal del
    test estaba en claro. Los cinco supuestos del catálogo que el rojo no pudo ejecutar (dorados del diff, hoja
    `" catalogo "`, celda de moneda, comilla suelta del CSV, aviso por `_jugo`) se compararon **byte a byte** con la cara
    vieja lanzando la misma petición a las dos: ninguno desmentido. Mutantes (declarados por los sub-agentes): media,
    contenido y eventos 63 válidos, 61 muertos, 2 equivalentes; catálogo 67, 61 y 6 equivalentes o inalcanzables; `flows`
    39, 38 y 1 equivalente; tres supervivientes reales cerrados con caso (el borde `'9'` del saneo del nombre, el margen del
    sobre multipart, el BOM ante cabecera entrecomillada). Sin test, por inalcanzables desde fuera: tres 400 defensivos de
    la planilla, dos 500 de serialización, el techo de 32 MiB descomprimido del XLSX y la rama «store nil ⇒ 500», que no se
    porta (la ruta solo se monta con los puertos). Rarezas portadas: `content.write` se exige también en `mode=validate`;
    el 403 del gate de feature deja auditoría `failure` (va por dentro de `AuditMiddleware`) y el de permiso no; reaplicar
    el mismo documento no es un no-op; `stale` acepta todo lo de `strconv.ParseBool`; el cancel llama a
    `CancelEventForTenant` aunque el evento ya sea terminal; media sin techo de cuerpo.
50. **(F8-06, conmutación) Nada necesitó adaptador; lo que la spec decía mal y lo que no listaba.** `go build ./...` dio rc=0 a
    la primera en el ensayo: todos los tipos nuevos encajan tal cual (la tabla de sustitución era exacta). 🟡 Contradicciones
    de la spec, medidas: (a) [`reglas.md`](reglas.md) §4.6 y [`arquitectura.md`](arquitectura.md) §5 decían «`FaseActual`
    sigue existiendo y no cambia»; TX.24 dice `FaseActual = 8`, y con 7 falla el candado de mudanzas en las 19 rutas: mandó
    TX.24 (`internal/arranque/mudanzas.go`); (b) los `grep … → vacío` de T8.31 y T8.34 no pueden dar vacío: casan las
    cabeceras «Porta internal/flujos/…» (151 líneas en `internal/modulos`); se midió por línea de import y con
    `go list -deps` (0); (c) `tareas.md` no listaba: `internal/candados/filesize.go` y `filesize_test.go` (techo de
    `bridge_contact_test.go`), `internal/modulos/un_fichero_un_test_test.go` (testigo `bridge_contact.go`), el helper
    `sameInstance`, que vivía en `bridge_contact_test.go` y usan seis tests que sobreviven (pasa a
    `gateway_wiring_test.go`), y las cabeceras de 8 ficheros de producción y 10 de test que decían «cablea paquetes
    VIEJOS». `DBTimeout` ya no viaja a la cara de conversación: ningún handler I1–I19 lo leía. En `Capas` no hubo que tocar
    nada (`conversacion → edge` ya estaba). **Tests de cableado**: de «el adaptador envuelve a X» a identidad directa; se
    borraron solo las aserciones cuyo objeto murió (los tipos adaptador, `depsDeLaAPIPublica`, las dos segundas instancias,
    `exigeViejoANil`) y ocho tests cambian de nombre porque mentía (`OnlyTheBridgeImports…` → `NoBootFileImports…`); la
    lista completa, en el mensaje de `fe6305b9` y en el PR. El candado nuevo (`conversacion_cableado_test.go` y
    `conversacion_cableado_ast_test.go`, 18 tests) afirma por reflexión, sobre el perfil «minimo» y sin accesores nuevos,
    que los sinks y la política de reanudación están puestos de verdad (D-F8-16): 22 mutaciones de la producción, todas
    cazadas. Dos cosas de método: un `Mount…` con cuerpo `panic(pendiente…)` hace que staticcheck dé por muerto lo que
    sigue (SA4006 en `http.go` y en los tests): el rojo de la cara no puede convivir montado, que es justo lo que pide «aún
    sin montar»; y el lint con `--tests=false` saca tres `unused` en ficheros ajenos (`sqlStageArray`, `untrackSession`,
    `inferenceReasons`: solo los usan sus tests), que `make lint` no ve: **no medido contra la base**, queda anotado.
51. **(F8-06, T8.35) 🔴 Lo que la conmutación deja sin comprobar: lo que F8-07 tiene que refutar.** Esta sesión **no** corrió
    `make test-procesos`, `make ci-docker`, las suites contra Postgres, la integración vieja ni un arranque real de
    `cmd/server-modular`; el e2e de TX.24 tampoco. Lo afirmado es estático o sobre el perfil «minimo», sin BD: (a) **la
    identidad de `entResolver`, `kp` y `gw` en ejecución**: hoy la prueban la reflexión y el conteo de constructores, no un
    proceso; el guard anti-self-loop con un `kp` equivocado no da ni un error (T-3): hay que verlo bloquear con BD real;
    (b) **el barrido del agregador con BD real** (T-4): hoy solo consta por AST que `Run` se lanza una vez en la fase de
    fondo; falta ver una ventana abrirse y cerrarse, y `RecoverAtBoot`; (c) **el golden en un Edge real** y una
    conversación entera por el binario nuevo (R8.8.a); (d) **el receptor de los hooks** (`gw.OnIncoming`, `OnHeartbeat`,
    `OnWarmup`, `OnEdgeReady`), de J19 y de la fuente del gauge: un valor de método no se compara por receptor; consta por
    AST y por «no es nil»; (e) **que el mux de detrás esté vacío en ejecución**: un `ServeMux` no se enumera; lo cubren «nadie
    lo toca» (AST) y «las 73 filas resuelven en la nueva»; (f) **las tres divergencias deliberadas** (D-F8-14, D-F8-15,
    D-F8-16): ningún proceso corrió, así que no se sabe si alguno las nota; si lo hace, es un hallazgo; (g) los procesos
    P4–P8 contra el binario nuevo, que por primera vez ejercitan el runtime, el agregador y el compositor **nuevos**
    (D-F7-9 sigue sin arreglar: la carrera cierre → sobre puede asomar, hallazgo 62 de F7; se arregla en F8-06b); (h) los
    mutantes de esta sesión son los declarados por los sub-agentes, no repetidos por el orquestador; (i) las cabeceras de
    `05`, `04` y la documentación de la pieza (`contratos.md`, `arquitectura.md` del repo) no se tocaron.
52. **(F8-06b, línea base) Los procesos contra el binario nuevo, por primera vez con el runtime, el agregador y el
    compositor nuevos: verdes a la primera.** Antes de tocar nada, sobre `1b6bb209`: `BINARIO=nuevo make test-procesos` →
    `RC=0 · PASS=1283 · FAIL=0 · SKIP=0`; 0 apariciones de «no trae literal que analizar»; ningún proceso nota D-F8-14,
    D-F8-15 ni D-F8-16. Resuelve los puntos (f) y (g) del hallazgo 51 **en lo que un proceso puede ver**; el resto del 51
    —(a)–(e), (h), (i): identidad en ejecución, arranque real, Edge real, `ci-docker`— sigue siendo de F8-07. Matiz: mientras
    corría, un sub-agente empezó a escribir en el checkout el contrato rojo (un método nuevo que nadie llama); no cambia
    conducta. Contra el viejo no se midió línea base.
53. **(F8-06b, T8.39) El diseño que quedó, y por qué la guarda es `id` + `updated_at`.** No hay columna de versión, y un
    mensaje que entra en una ventana viva solo mueve `source_refs` y `updated_at` (`OpenOrAppend`); el barrido ya tiene las
    dos cosas en el `OpenJob` de `ListAggregating`. La quinta operación de `intake.JobStore`, `CloseWithSourceText(ctx, seen,
    env)`, es **un** `UPDATE` que cierra y escribe el sobre `WHERE id = $1 AND status = 'aggregating' AND updated_at = $2`:
    si un mensaje entró mientras se componía, devuelve `(false, nil)`, no toca la fila y la recoge el siguiente barrido. El
    sobre va completo o vacío entero (vacío → las tres columnas a NULL; `SourceText.Empty()` es nuevo); a medias es error
    antes de la base. Ni migración ni transacción. El compositor gana `Compose` (lee, compone y cifra, **sin escribir**);
    `ComposeAtFlush` se muda a `source_composer_flush.go` (E-13) como `Compose` + `PutSourceText`, con la conducta intacta.
    **Con duda mandó el viejo** en dos cosas: fallo al componer o cifrar → se cierra sin sobre con `CloseWindow` (que así
    conserva su llamante) y el mismo ERROR literal; hilo vacío → cierra con el sobre a NULL. D-F9-10 intacto. Los cuatro
    tests de cableado pasan sin tocarse, y `ComposeAtFlush_DoesNotOverwrite/window_not_pending` (hallazgo 35) no cambió.
54. **(F8-06b, T8.40) El job de re-análisis nace con su sobre, y un cambio observable.** `ReanalysisRequest` gana
    `SourceText` y el mismo `INSERT` de `OpenReanalysis` escribe las tres columnas (a medias → error, después de la petición
    incompleta). El puerto `reanalisis.Composer` pasa a `Compose`; el servicio persiste el texto pegado, compone y **después**
    abre. 🟡 **Cambia lo que ve el dueño cuando falla la composición**: antes, 200 y un job sin literal que el worker mataba;
    ahora no se abre job y la petición devuelve `reanalisis: componer el literal del evento <id>: <causa>`, que la cara saca
    como 500 «no se pudo pedir el re-análisis de la solicitud» (es lo que pedía T8.40). Desaparece el ERROR «el job quedó
    abierto pero SIN literal». ✎ Cómo lo muestra la consola del cliente **no se miró**: deuda **D-36** de `deuda.md`
    (Jhoan, 2026-10-10: sesión aparte al terminar el plan, para todos los cambios que afectan a consumidores). El sobre vacío tras componer no es alcanzable aquí (`sourceOfMaterial` usa el mismo criterio
    que el compositor): documentado en el puerto, sin test.
55. **(F8-06b, lo que queda sin llamante) 🟡 `PutSourceText` y `ComposeAtFlush` ya no los llama nadie en producción.** Tras
    T8.40 quedan: `(*SourceTextComposer).ComposeAtFlush`, `intake.JobStore.PutSourceText` con sus dos implementaciones, la
    interfaz `runtime.SourceTextWriter` y el campo `SourceTextComposer.jobs` (que `fase5_captacion.go` sigue cableando y los
    tests de cableado siguen afirmando), más sus tests y casos de suite. **Decisión de Jhoan (2026-10-10, al planificar): se
    dejan y se anota**; deuda **D-35** de `deuda.md`. ✎ **Decidido después (Jhoan, 2026-10-10): se borran en F8-07**, en su
    propio commit. `CloseWindow` sí conserva llamante. De paso, el hallazgo 1 (literal
    perdido o marca cruzada por la subconsulta de `PutSourceText`) queda **sin vía** en el árbol nuevo.
56. **(F8-06b, caso de proceso) 🟡 Una aserción igual para los dos binarios, sin ramificar; la ficha decía otra cosa.**
    `requireNoJobWithoutLiteral` (`test/procesos/p4_borrador_helpers_test.go`) exige 0 filas `failed` de `intake_jobs` del
    tenant con «no trae literal que analizar», en el cierre de `TestP4_MessageToDraft`, `TestP4_WindowRules`,
    `TestP8_Reanalysis` y `TestP6_CRMBridge` (no en `TestP4_AheadClassification`). La ficha de F8-06b pedía «distinguir
    binario (o tolerar el viejo), como ya hacen los procesos con otras divergencias deliberadas»: **ningún proceso
    ramifica** y R9.8.b de F9 lo prohíbe. **Decisión de Jhoan (2026-10-10)**: foco en el binario nuevo —el viejo está
    congelado, UAT no está activo y el código viejo se borra al acabar el plan—; si el viejo se pone rojo por esa aserción,
    es el fallo conocido que conserva (igual que ya lo hacía por la línea ERROR, hallazgo 14).
57. **(F8-06b, mutantes) 39, ninguno sobrevive.** En serie, por `go test -overlay` sobre copias fuera del repo. Store, 14 (7
    en el gemelo, 7 en el adaptador): en el adaptador, cuatro solo los mata el test del literal del SQL con el driver falso,
    así que dos se repitieron contra el **Postgres real** (la suite por overlay): sin `AND updated_at = $2` cae
    `CloseWithSourceText_MessageArrivedAfterTheRead_FalseAndUntouchedUntilReread`; sin `AND status = 'aggregating'` cae
    `CloseWithSourceText_AlreadyClosedOrUnknownID_FalseAndNothingTouched`. Agregador y compositor, 14: los cuatro pedidos
    (quitar la guarda, cerrar sin sobre, sobre sin cerrar, orden viejo) y diez más; «quitar la guarda» lo mata **un solo**
    test, `TestSweep_AMessageDuringTheCompositionKeepsTheWindowOpen`. Re-análisis, 11. Salvo los dos contra Postgres real,
    son los declarados por los sub-agentes, no repetidos por el orquestador. ✎ **Jhoan (2026-10-10)**: los siete que
    sostienen el arreglo se repiten en F8-07 (T8.43), por quien no escribió el código.
58. **(F8-06b, gates) Verdes sobre `42a06574`, con una intermitencia ajena por el camino.** `GOWORK=off make ci-local`
    `GATE_RC=0` (239 `ok`, lint `0 issues.`); `PENDIENTES=0 · ROJOS=0`; 0 `--- SKIP` (4.475 PASS de primer nivel);
    `make test-procesos` nuevo y viejo `RC=0 · PASS=1293 · FAIL=0 · SKIP=0`, sin la carrera en ninguno. Tras T8.39 (sobre
    `83254490`) la primera pasada contra el nuevo dio `RC=1 · PASS=1288 · FAIL=2`: `TestP6_CRMBridge/callback_body_adversarial`,
    `p6_crm_test.go:270`, «write: connection reset by peer» — la intermitencia del callback del CRM del hallazgo 14 (allí
    «broken pipe»), ajena al cambio; repetida sola, `RC=0 · PASS=1290`. **Sin correr**: `make ci-docker`, la integración
    vieja, el arranque real y el e2e de TX.24 (F8-07). La carrera no se reprodujo a propósito bajo carga. ✎ **Jhoan
    (2026-10-10): se da por bueno sin test de estrés**: con una sola sentencia no queda instante en que la fila esté
    `pending` sin sobre; el argumento es de construcción, no de estadística.
59. **(F8-06b, método y costes)** Tres sub-agentes en serie, rojo y verde con el mismo (reanudado), ninguno commitea.
    (a) El rojo de un cambio a una operación que ya existe es **por aserción**, no por `panic` (como en D-F8-15), y los casos
    de suite nuevos viven en una tabla aparte hasta el verde, porque la suite corre sin etiqueta. (b) Coste del orden nuevo:
    una lectura del hilo y un cifrado por cada intento que no llega a cerrar (carrera perdida, ventana cambiada, cierre
    fallido); ese sobre se tira. (c) Con el contexto cancelado y el compositor fallando por la parada se intenta el
    `CloseWindow` igualmente; en producción el `UPDATE` falla con el contexto cancelado, la ventana queda `aggregating` y la
    recoge `RecoverAtBoot`, sin ERROR. (d) El doble `aggregatorJobs` hace `ctxAware` también a los cierres. (e) Un
    `golangci-lint` a mano necesita `GOTOOLCHAIN=go1.26.5` delante (el `Makefile` lo exporta). (f) **Residual conocido, no
    cambiado**: el hilo y la cola no se escriben en un solo acto, así que un mensaje ya en el hilo cuya fila aún no movió
    `OpenOrAppend` puede entrar en el sobre y abrir después otra ventana; pasaba igual con el orden viejo y no es D-F7-9. ✎ Deuda **D-37** de `deuda.md` (Jhoan, 2026-10-10: post-migración;
    deducido leyendo, sin verificar el orden real de las dos escrituras).
60. **(F8-07a, T8.41) `PutSourceText` y `ComposeAtFlush`, borrados; arrastraban más de lo que decía la ficha.** `d01e23a0`:
    27 ficheros, 3 borrados enteros (`source_composer_flush.go`, su test y `intakehelpertest/queue_put_contrato.go`).
    `intake.JobStore` vuelve a **cuatro** operaciones y el compositor ya no conoce `intake_jobs`
    (`NewSourceTextComposer(log, thread, cipher, opts...)`). **No apareció ningún llamante de producción.** Lo que la
    ficha no listaba: (a) con las operaciones caen `putSourceTextSQL`, `Counters.PutSourceText`, `FailPutWith` y
    `lastPendingLocked`, que solo ellas usaban; (b) dos tests del **agregador** leían ese contador: pierden ese sumando y
    nada más; (c) **solo un** test de cableado miraba `composer.jobs` (`captacion_cableado_test.go`); en
    `conversacion_cableado_test.go` el `"jobs"` es el del agregador y no se tocó; (d) `source_composer_flush_test.go`
    alojaba el montaje que usa `source_composer_compose_test.go`: se movió tal cual (queda en 372 líneas); (e) cuatro
    conductas las custodiaba **solo** el fichero de flush y se portaron a `Compose` en vez de perderse —
    `TestWithThreadLimit`, `DefaultThreadLimit == 200`, «el constructor nunca devuelve nil» y el Warn del hilo con solo
    decisiones—. Se pierden a propósito, por ser conducta de lo borrado: el fallo al guardar el sobre, `DoesNotOverwrite`
    (hallazgo 35) y el caso «sin almacén». La suite `Contrato` de la cola baja en **6 casos**, y con ellos la única
    custodia en Postgres real del `ORDER BY` de `putSourceTextSQL`, que también se fue. 🟡 Quedan nueve comentarios que
    nombran `PutSourceText`/`ComposeAtFlush`: todos hablan del árbol **viejo** o en pasado; tres están en presente
    (`aggregator_sweep.go:298`, `intake/store.go:202`, `intake/postgres.go:142`) y pasarán a mentir cuando el viejo muera
    en F10. Deuda **D-35 cerrada**.
61. **(F8-07a, T8.42) 🟡 La causa de la intermitencia del callback del CRM no era el 413.** La ficha suponía «el servidor
    rechaza el cuerpo adversario mientras el test aún lo envía». El mecanismo es ese, **el culpable no**: (a) un **413 no
    puede provocarlo**, porque para darlo `MaxBytesReader` ya leyó el cuerpo hasta el techo; (b) lo provoca la respuesta
    que sale **sin leer el cuerpo** —el 401 «fuera de ventana», que va antes que el cuerpo, o un **429** del límite por IP
    (≈ 75 por pasada de `TestP6_CRMBridge`), que puede caer sobre cualquiera de los cuatro cuerpos de 64 KiB, también el
    que espera 200—: `net/http` cierra sin drenar lo que el handler no leyó y el cliente, a medio escribir, pierde a veces
    la carrera. Igual en los dos binarios. **Cómo se comprobó**: leyendo las dos caras y `net/http`, y con un programa
    aislado fuera del repo (401 sin leer un cuerpo de 64 KiB + 1: **10 cortes en 3.000**; 413 con `MaxBytesReader`: **0 en
    3.000**). 🟡 **En los procesos, antes del arreglo, no se reprodujo** (10 pasadas de
    `TestP6_CRMBridge` contra el nuevo, 0 fallos); después, en 40 pasadas más (20 por binario) **ocurrió una vez**, contra
    el nuevo, y el reintento lo absorbió (contador «callbacks reintentados por corte al enviar: 1»). Cuál de las dos
    respuestas fue —401 o 429— **no se sabe**, ni en esa ni en las de los hallazgos 14 y 58. De paso:
    `p6_crm_test.go:270` era el helper `post`, por donde pasan todos los callbacks, no el subcaso. **El arreglo**
    (`c7700226`, solo `test/procesos/`): `post` reintenta el corte dentro del tope con el que ya reintentaba el 429, así
    que el de 64 KiB justos sigue exigiendo su 200 y los 413 su código; los tres callbacks sobre el techo van por
    `postRejected`, que da por rechazo la respuesta de error o, si en todo el tope no llega ninguna, el corte. **Sigue
    mordiendo**: con `crmCallbackMaxBody` subido a 128 KiB (overlay, sin commit) el caso da rojo («HTTP 200 …; quería
    413»). Como la rama casi nunca se ejercita sola, nace `TestP6CutWhileSending`, que fija el clasificador contra un
    corte real; y encontró algo: el mismo corte sale con una **tercera cara**, «use of closed network connection» (6 de
    300 antes de incluirla; 1.000 de 1.000 después), que los hallazgos 14 y 58 no habían visto.
62. **(F8-07a, T8.43) Los siete mutantes, repetidos por el orquestador: ninguno sobrevive.** Sobre `c7700226`, en serie,
    `GOWORK=off GOTOOLCHAIN=go1.26.5 go test -count=1 -overlay <json> <paquete>` con el paquete entero y la copia mutada
    fuera del repo; antes, `go vet` con el mismo overlay: **los ocho compilan** (el 2 va en dos variantes). 🟡 Tras T8.41
    los mutantes **(1) y (3) ya no se pueden escribir al pie de la letra**: sin `PutSourceText` no hay operación que
    ponga un sobre en una fila ya `pending`; van en su variante más fiel.

    | # | Mutante | Dónde | Tests que caen |
    |---|---|---|---|
    | 1 | orden viejo: `CloseWindow`, después componer e intentar `CloseWithSourceText` (variante) | `runtime/aggregator_sweep.go` | 10 — `TestSweep_TheEnvelopeTravelsWithTheClose`, `TestSweep_ComposesOncePerWindowItClosed`, `TestSweep_TheClosedJobCarriesExactlyTheComposedEnvelope`, `TestSweep_AMessageDuringTheCompositionKeepsTheWindowOpen`, `TestSweep_EmptyThreadClosesWithAnEmptyEnvelope`, `TestSweep_CloseWithEnvelopeFailure_IsAnErrorOnlyWhileTheContextLives`, `TestSweep_StoreFailuresAreLoggedAndSkipped`, `TestWithSourceComposer_DefaultsToAnEmptyComposer`, `TestRun_ContextCancelled_ReturnsWithoutLoggingAtError`, `TestRun_AHintDuringASweepIsNotLost` |
    | 2a | cerrar sin sobre, por `CloseWindow` | ídem | 8 — los de (1) menos los dos `TestRun_…` |
    | 2b | cerrar con el sobre vacío | ídem | 5 — `…TravelsWithTheClose`, `…ComposesOncePerWindowItClosed`, `…CarriesExactlyTheComposedEnvelope`, `…AMessageDuringTheCompositionKeepsTheWindowOpen`, `…StoreFailuresAreLoggedAndSkipped` |
    | 3 | componer y no cerrar (variante) | ídem | 36 — casi todo test que espera un cierre; entre ellos `TestSweep_ComposesOncePerWindowItClosed` y `TestSweep_SilenceIsTheMainPath` |
    | 4 | sin la guarda `updated_at` | el gemelo `intake/memory.go`, con los tests de `runtime` | **1** — `TestSweep_AMessageDuringTheCompositionKeepsTheWindowOpen` |
    | 5 | abrir el job y componer después | `reanalisis/reanalisis.go` | 6 — `TestReanalyze_TheJobIsBornWithExactlyTheComposedEnvelope`, `TestReanalyze_FullStepSequenceIsTheContract`, `TestReanalyze_LocalVia_SequenceSkipsTheViaGate`, `TestReanalyze_ComposerDown_FailsTheRequestAndOpensNoJob`, `TestReanalyze_OpenJobFails_ErrorAsIsAndNoJob`, `TestReanalyze_TextThatSanitizesToEmpty_IsLikeNotSendingIt` |
    | 6 | abrir pese al fallo de composición | ídem | **1** — `TestReanalyze_ComposerDown_FailsTheRequestAndOpensNoJob` |
    | 7 | tragarse el fallo de composición | ídem | **1** — el mismo |

    Tres mutantes —el (4), el (6) y el (7)— cuelgan de **un solo test** cada uno, y el (6) y el (7) del mismo: confirma
    lo que el hallazgo 57 decía del (4) y lo extiende al re-análisis. No nació ningún caso nuevo.
63. **(F8-07a, gates y método) Verdes sobre `c7700226`.** `GOWORK=off make ci-local` rc=0 (239 `ok`, lint `0 issues.`,
    también con `--build-tags pendiente,integracion`); `PENDIENTES=0 · ROJOS=0`; 0 `--- SKIP` (4.462 PASS de primer nivel,
    eran 4.475); `BINARIO=nuevo make test-procesos` `RC=0 · PASS=1288 · FAIL=0 · SKIP=0` a la primera (eran 1.293: −6
    casos de `PutSourceText`, +1 `TestP6CutWhileSending`); `-run TestP6_CRMBridge -count=10` rc=0 contra los dos binarios.
    **Sin correr**: `make ci-docker`, la integración vieja, el arranque real (F8-07b) y los procesos **enteros** contra
    el viejo. Método: (a) una pasada de `TestP6_CRMBridge` cuesta ≈ 50 s, así que cada `-count=10` son ≈ 8,5 min y la
    sesión gastó ≈ 45 min solo en ellos; (b) el overlay llega al binario que compila el arnés con
    `GOFLAGS=-overlay=<json>`, porque `compilar` hereda el entorno: así se mutó el **servidor** sin tocar el checkout;
    (c) un selftest del clasificador de errores encontró en 300 pasadas lo que 30 pasadas del proceso no enseñaban.

64. **(F8-07b, T8.36) 🟡 El e2e de `cmd/server-modular` no existe, y el carrito no lo recorría ningún proceso: nacen tres
    casos.** `cmd/server-modular/` solo tiene `main.go`; F0 decidió no copiar el e2e del viejo
    ([`../F0-andamiaje/diseno.md`](../F0-andamiaje/diseno.md): «el oráculo del binario nuevo contra el viejo es F9»), y
    TX.24 y T8.36 lo nombran sin definirlo. Además, los flujos de `test/procesos` solo usaban nodos `menu` y `message`, y
    el Edge falso es código `_test.go`: fuera de `go test` no hay cliente CloudLink. «Arrancar solo» es, pues, lo que ya
    hace el arnés con `BINARIO=nuevo` (compila y lanza **solo** `cmd/server-modular`, un subproceso por proceso, con su
    base clonada y sus puertos). **Decisión de Jhoan (2026-10-10, en la sesión): casos de proceso nuevos, commiteados**,
    solo código de test, primero contra el viejo y después contra el nuevo, sin ramificar por binario:
    (a) `TestP3_CartConversation` (`ffba56e7`, `p3_entrante_cart_test.go`): «carrito» → categoría → artículo → cantidad →
    resumen → confirmar, todo dado de alta por HTTP (catálogo, flujo de un nodo `cart`, disparo `event_start` con
    `event_kind: cart`); afirma las pantallas literales, dos adversarios (opción inválida; cantidad ininteligible, que
    baja **una** inferencia al Edge), y al confirmar el evento `cart|closed`, la solicitud `order|closed` con su línea, seis
    `flow_events` en orden y el hilo de 19 filas con los 18 mensajes cifrados; (b) `TestP3_SelfLoopGuard` (`866e47a6`):
    dos Edges de la misma empresa, el número propio entra por un **latido real** del segundo —nada sembrado por SQL—; con
    la sesión pasiva no bloquea, con la activa corta las dos grafías y un extraño sí recibe respuesta;
    (c) `TestP4_BootRecoveryClosesExpiredWindow` (`02ad985a`): ventana `aggregating`, servidor parado, plazos vencidos por
    SQL **sin proceso**, y el rearranque la cierra (una línea «agregador: ventanas vencidas cerradas al arrancar»,
    `jobs`=1); el proceso muerto no cerró nada. Los tres, 1 y 5 pasadas por binario, rc=0 y 0 SKIP. Por el camino:
    la etiqueta de la métrica es `reason="self_loop"`, con guion bajo (la ficha y el encargo decían `self-loop`); un
    disparo `keyword` hacia un flujo con carrito no trae evento padre (tiene que ser `event_start`); tras confirmar, la
    fila de `flow_state` queda en el centinela hasta el siguiente entrante, así que la racha no llega al histograma hasta
    entonces; y el servidor arranca con el tope anti-bucle de producción (`WAPP_FLOW_REPLY_BURST=3`, `…_RATE=0.5`), por lo
    que el caso de carrito espera 2,1 s antes de cada mensaje desde el cuarto (≈ 15 s por pasada): una espera de reloj
    justificada, sin `time.Sleep`; la alternativa —una opción del arnés que suba la ráfaga— no se aplicó porque saca al
    servidor de los valores de producción.
65. **(F8-07b, T8.36) El hallazgo 51, punto por punto: qué se vio en ejecución y qué no.** Contra el binario nuevo, con
    Postgres de testcontainers y el Edge falso; «visto» quiere decir que un proceso lo afirma **y** que un mutante del
    servidor por overlay lo pone rojo.

    | Punto del 51 | Veredicto | Con qué |
    |---|---|---|
    | (a) identidad de `kp` | ✅ visto | `TestP3_SelfLoopGuard`: el índice ciego lo escribe `fleet` y lo lee el guard; con otro `KeyProvider` en el guard, o sin `WithSelfNumbers`, **no bloquea y no da error** (T-3, tal cual) y el test cae |
    | (a) identidad de `gw` | ✅ visto (ya lo estaba) | `TestP1_EdgeFaceOverTheWire`: lo que entra por `/api/v1/messages`, I4 y J19 llega al Edge conectado; sin stream, las cuatro puertas dan 502 (el centinela es uno). Sin mutante propio de esta sesión |
    | (a) identidad de `entResolver` | 🔴 **no visto** | Ningún proceso cambia un entitlement y mira a sus cuatro consumidores: no hay ruta que escriba `tenant_features` y la caché dura 60 s. Solo consta que runtime, agregador, despachador y cara leen **la misma respuesta** para una empresa (P4, carrito). Sigue en estático (`TestIdentidad_OneEntitlementsResolverForConversation`) |
    | (b) barrido del agregador | ✅ visto, con matiz | `TestP4_MessageToDraft/borrador` y `TestP4_WindowRules/anclas`: la ventana se abre con el entrante y la cierra el `Sweep` del `Run` real. 🟡 Con los plazos puestos a 0 por SQL: ningún proceso espera los 45 s de fábrica |
    | (b) `RecoverAtBoot` | ✅ visto | `TestP4_BootRecoveryClosesExpiredWindow`; sin la llamada en `aggregator_run.go` cae |
    | (c) conversación entera | ✅ visto | `TestP3_CartConversation` |
    | (c) golden en un Edge **real** | 🔴 no hecho | Fuera de la sesión por ficha: deuda D-38 |
    | (d) receptor de `OnIncoming` | ✅ visto | Sin `c.gw.OnIncoming = …` no llega ninguna pantalla; y el gauge se mueve con esos mismos entrantes, así que el receptor es el runtime de la fuente del gauge |
    | (d) fuente del gauge | ✅ visto | `wapp_flow_autoreply_streak_max` vale 8 en el resumen, 9 tras confirmar y 0 tras el mensaje siguiente (histograma `_count`=1, `_sum`=9); sin `SetFlowAutoreplyStreakMaxSource` o sin `WithAutoreplyStreakHook` cae |
    | (d) `OnWarmup`, `OnEdgeReady` | ✅ visto por su efecto (ya lo estaba) | P9 (`warmup=true` en el Edge) y `TestP4_MessageToDraft/degradacion`, `TestP4_WindowRules/flanco_por_empresa`. Sin mutante de esta sesión |
    | (d) `OnHeartbeat` | 🔴 **no visto** | Su único efecto es una línea Debug; ningún proceso la afirma |
    | (d) J19 | 🟡 a medias | Llega al Edge (P1); que su runtime sea **el mismo** que el de `OnIncoming` no se ve desde fuera |
    | (e) mux de detrás vacío | 🔴 **no se puede ver desde fuera** | Por diseño el 404 de ruta desconocida lo escribe el mux de detrás (`estrangulador.go`): por HTTP no se distingue de uno «de la nueva». Sigue en estático (`TestCableado_TheOldFaceServesNoRoute`) |
    | (f), (g) divergencias y P4–P8 | ✅ | Hallazgo 52, y otra vez aquí: 1.291 PASS por binario, 0 «no trae literal que analizar» |
    | (h) mutantes de F8-06 | 🟡 no repetidos | Los 22 del candado de cableado siguen siendo los declarados entonces. De los **12** de esta sesión, el orquestador repitió cuatro (`kp`, `RecoverAtBoot`, `OnIncoming`, fuente del gauge): los cuatro mueren |
    | (i) cabeceras de `05`, `04` y la documentación de la pieza | 🔴 sin tocar | No es de esta sesión: queda para F8-07 |
66. **(F8-07b, mutantes) Doce por overlay sobre el servidor, uno sobrevive.** `GOFLAGS=-overlay=<json>` con la copia
    mutada fuera del repo; el checkout no se tocó. Mueren: guard con otro `KeyProvider` (nuevo **y** viejo), sin
    `WithSelfNumbers`, sin `RecoverAtBoot` (nuevo y viejo), y en `internal/arranque/fase7_flujos.go` sin el registro del
    carrito (el alta del flujo da 400 «tipo desconocido "cart"»), sin `WithQueryResolver` (0 inferencias donde se espera
    1, `desenlace:sin_resolutor`: T-6 cazada por un proceso), sin `SetFlowAutoreplyStreakMaxSource`, sin `gw.OnIncoming`
    (T-5), sin `WithEventStore` y sin `WithAutoreplyStreakHook`. 🟡 **Sobrevive `WithResumePolicy`**: sin `page_size` ni
    `buyer_fields` en `tenant_settings`, los valores por defecto dan el mismo recorrido; matarlo pide una empresa con
    checklist de comprador o tamaño de página propio. No se escribió: queda anotado.
67. **(F8-07b, gates) `make ci-docker` verde por primera vez desde F8-01, y lo demás.** Sobre `fd3a3fde` (la base):
    `make ci-docker` **RC=0** (239 `ok`, lint `0 issues.`, ≈ 4 min; `TestRendimiento_P99PorItem` no asomó);
    `GOWORK=off make ci-local` `GATE_RC=0`; `PENDIENTES=0 · ROJOS=0`; 0 `--- SKIP` (4.462 PASS de primer nivel);
    `make test-procesos` viejo y nuevo `RC=0 · PASS=1288 · FAIL=0 · SKIP=0`. Repetidos sobre `ffba56e7` (con los tres
    casos): `ci-docker` **RC=0**, `ci-local` `GATE_RC=0` (239 `ok`, `0 issues.`), procesos viejo y nuevo
    `RC=0 · PASS=1291 · FAIL=0 · SKIP=0`. Las suites `Contrato` de `store`, `trigger`, `events.Store`, `self_numbers`,
    `tenant_resolver` e `intake` (cola, máquina y re-análisis), `PASS` contra Postgres en los dos binarios. Es la primera
    pasada **entera** contra el viejo desde F8-06b. 🟡 Dos cosas vistas y no afirmadas: tras un reinicio, el primer intento
    de P2 cae por `edge_offline` en los dos binarios (el worker reclama el job antes de que el Edge reconecte; sale al
    segundo); y un reinicio cuesta ≈ 10 s en el viejo y < 1 s en el nuevo (`TestP1_EdgeFaceOverTheWireRestart`, 10,8 s
    contra 0,8 s): no se localizó la causa (candidato sin comprobar: el tope de 10 s del `GracefulStop` viejo).
    **Sin correr**: la integración vieja (no se tocó código compartido) y el Edge real (D-38).

## Orden de lectura

`README` → [`arquitectura.md`](arquitectura.md) (sobre todo §4 singleton y §5 puentes y adaptadores) →
[`reglas.md`](reglas.md) (niveles E-12 y trampas) → [`diseno.md`](diseno.md) §0.1 (niveles) y el paquete que toque →
[`requisitos.md`](requisitos.md) → [`tareas.md`](tareas.md).

## Bloques de sesión

Once sesiones (✎ 2026-10-10, Jhoan tras F8-06b: eran nueve; F8-07 se parte en F8-07a, F8-07b y F8-07 · ✎ 2026-10-10, D-F8-13: eran ocho; nace F8-06b para D-F7-9, hallazgo 44 · ✎ D-F8-11: eran siete; F8-04 se parte en F8-04 y F8-04b, hallazgo 29), **todas 💻**: cada una en **su rama partida de `dev`** y con PR a `dev` sin squash (regla 6 del `CLAUDE.md`, 2026-10-03; ✎ corregido en F8-01: aquí decía «sin PR, `git push origin dev`»).
Cada una es un bloque de 45–90 min (objetivo, **sin medir**) y cierra con las tres cosas: tareas `[x]` con SHA,
bloque en `ESTADO.md`, hallazgos nuevos aquí. Fichas en [`../sesiones/`](../sesiones/README.md).

| Sesión | Bloque | Nivel E-12 (provisional) | Tareas | Punto de parada |
|---|---|---|---|---|
| F8-01 | inventario E-12 y hojas | medio · `store` complejo · `content` simple | T8.1–T8.8, T8.22 | inventario **aprobado por Jhoan** (antes no se escribe código); `model`·`trigger`·`content`·`store`·`modules` verdes; suites `Contrato` en memoria y en Postgres |
| F8-02 ✅ | motor | medio · `menu`/`media`/`survey` simple | T8.9–T8.11, T8.14, T8.23 | `engine`·`menu`·`survey`·`media`·`turnoacotado` verdes (2026-10-09, rama `reorg/f8-02-motor`, `d8fd4ac` … `4ce435f`, PR #61 en `dev`, merge `df340a6`) |
| F8-03 ✅ | `events` y `cart` | medio · `events/store` y `thread_reader` complejo | T8.12, T8.15–T8.17, T8.24, T8.25 | `events` (11) y `cart` (20) verdes; goldens idénticos; candado de orden verde y mutado (en dos mitades: 2026-10-09, rama `reorg/f8-03-events-cart`, `8c819b40` … `790ca522`, PR #62 en `dev`, merge `8b841c8d`; 2026-10-10, rama `reorg/f8-03b-cart-verde`, `dd0771ce` … `44255a2b`, PR #63 a `dev`, **integrado** por orden expresa de Jhoan (2026-10-10), sin squash) |
| F8-04 ✅ | `runtime` (1a): los contratos de los 23 | complejo | T8.18–T8.21 | 23 contratos en rojo, `runtimehelpertest` verde, candado de rachas escrito, lista blanca de `conversacion` completa; ningún verde de `runtime` (✎ 2026-10-10, D-F8-11: antes llevaba también T8.26) (2026-10-10, rama `reorg/f8-04-runtime-contratos`, `cf327ca5` … `de6a5411`, PR #65 a `dev`, **integrado** por orden expresa de Jhoan (2026-10-10), merge `cbebf10c`, sin squash) |
| F8-04b ✅ | `runtime` (1b): el verde del soporte | complejo | T8.26 | **9** de los 12 de soporte verdes (✎ 2026-10-10, D-F8-12: `welcome`, `thread` y `send` pasan a F8-05), mutantes de `keyedmutex` y `streak` muertos, `pendiente` del runtime solo en los 11 del núcleo y en `welcome.go` (2026-10-10, rama `reorg/f8-04b-runtime-soporte`, `83565b4e` … `6216d1df`, PR #66 a `dev`, **integrado** por orden expresa de Jhoan (2026-10-10), merge `d80e7f56`, sin squash) |
| F8-05 ✅ | `runtime` (2): núcleo | complejo, con mutantes | T8.27, T8.28 | los 11 del núcleo y `welcome`, `thread` y `send` verdes (✎ 2026-10-10, D-F8-12: 14, no 11), mutantes muertos, `pendiente` del runtime = 0 (2026-10-10, rama `reorg/f8-05-runtime-nucleo`, `46b6a1ec` … `f5fd0374` y tres arreglos decididos después (`c44c212f`, `6eb31a3d`, `a9da0105`), PR #67 a `dev`, **integrado** por orden expresa de Jhoan (2026-10-10), sin squash) |
| F8-06 ✅ | la cara HTTP y conmutar | medio (`admin`, `apipublica`) | T8.13, T8.29–T8.35 | `admin` y handlers I1–I19 verdes; huella igual; 0 puentes (import), 0 adaptadores, `Conmutados` completo (2026-10-10, rama `reorg/f8-06-cara-http-y-conmutar`, `5ba7f419` … `fe6305b9`; hallazgos 47–51) |
| F8-06b | D-F7-9: cierre y sobre en un solo acto (✎ 2026-10-10, D-F8-13: sesión nueva) | complejo | T8.39, T8.40 | el agregador nuevo compone antes de cerrar y cierra con el sobre en una sentencia; el job de re-análisis nace con su sobre; caso de P4 verde contra el binario nuevo. ✎ **hecha** (2026-10-10; `a0144628` … `42a06574`; hallazgos 52–59; PR #69 en `dev`, merge `2902ec40`) |
| F8-07a | limpieza tras D-F7-9 (✎ 2026-10-10, Jhoan: F8-07 se parte en tres) | medio | T8.41–T8.43 | `PutSourceText` y `ComposeAtFlush` borrados (D-35), tests de cableado al día sin debilitarse; el caso adversario del callback del CRM tolera el corte; los siete mutantes de D-F7-9 repetidos y muertos |
| F8-07b ✅ | Docker y arranque real (✎ 2026-10-10) | — | T8.36, T8.37 | `make ci-docker` rc=0 (no corre desde F8-01); suites `Contrato` contra Postgres; `cmd/server-modular` arranca solo y recorre una conversación con el Edge falso; hallazgo 51 refutado. ✎ **hecha** (2026-10-10; rama `reorg/f8-07b-docker-y-arranque-real`, `866e47a6`, `02ad985a`, `ffba56e7`, PR #71 a `dev`; `ci-docker` RC=0; tres casos de proceso nuevos; del hallazgo 51 quedan cuatro puntos sin ver desde fuera y T8.36 queda `[~]`: hallazgos 64–67) |
| F8-07 | cierre | — | T8.38 | definición de hecho de [`reglas.md`](reglas.md) §4 entera |

Dos ajustes sobre el reparto por paquetes, por dependencias de compilación (medido en el código viejo):
`admin` importa `runtime` (`handlers.go:24,306,308`), así que no puede nacer antes que sus contratos y va con la cara
(F8-06); y `send.go`, `thread.go` y `welcome.go` cuelgan de `*Runtime`, así que F8-04 escribe los contratos de los
**23** antes de que F8-04b ponga verdes los 12 de soporte (✎ 2026-10-10, D-F8-11: las dos cosas iban en F8-04).

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
  ✎ **2026-10-10 (D-F8-13, Jhoan)**: F8-05 lo portó tal cual; el arreglo va en la sesión **F8-06b** (T8.39), después de
  conmutar. Diseño decidido: componer primero y cerrar guardando el sobre en **una sola sentencia**; `PutSourceText` no se toca.
  ✎ **ARREGLADA en F8-06b (2026-10-10)**: la ventana, `ef480998` (el agregador compone antes y cierra con
  `CloseWithSourceText`, que solo cierra si la ventana no cambió); el re-análisis, `1aad38cd` (el job nace con su sobre en
  el mismo `INSERT`). El binario viejo conserva el fallo hasta F10. Hallazgos 53, 54 y 57.

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
