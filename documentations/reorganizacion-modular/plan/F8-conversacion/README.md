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
| F8-02 | motor | medio · `menu`/`media` simple | T8.9–T8.11, T8.14, T8.23 | `engine`·`menu`·`survey`·`media`·`turnoacotado` verdes |
| F8-03 | `events` y `cart` | medio · `events/store` y `thread_reader` complejo | T8.12, T8.15–T8.17, T8.24, T8.25 | `events` (7) y `cart` (14) verdes; goldens idénticos; candado de orden verde y mutado |
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
