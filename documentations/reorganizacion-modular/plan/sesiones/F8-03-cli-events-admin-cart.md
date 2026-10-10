# F8-03 · F8 · `events` y `cart` · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 3 · `events` y `cart` |
| Entorno | 💻 solo local |
| Nivel (E-12) | medio · `events/store.go` y `thread_reader.go` complejo (el del inventario aprobado) |
| Duración objetivo | 45–90 min |
| Tareas | T8.12, T8.15–T8.17, T8.24, T8.25 |
| Depende de | F8-02 |
| Decisiones | D-F8-3 (goldens), D-F8-6 |
| Se para cuando | `events` (7) y `cart` (14) verdes; `eventshelpertest` con doble y suite; goldens idénticos **sin** `-update`; candado de orden sin etiqueta, verde y comprobado por mutación; `make ci-local` rc=0 con 0 SKIP; PR a `dev` abierto desde la rama de la sesión |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-02) está integrada y empujada en `dev`.
- [ ] Decisiones rellenas en [`../DECISIONES.md`](../DECISIONES.md): D-F8-3 (goldens), D-F8-6.
- [ ] `go1.26.5` y `golangci-lint v2.12.2` disponibles (`make toolchain`).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-03 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque: 3 · `events` y `cart`
- Tareas: T8.12, T8.24, T8.15–T8.17 y T8.25 de plan/F8-conversacion/tareas.md
- Te paras cuando: `events` (7) y `cart` (14) están verdes; `eventshelpertest` tiene doble y suite; los goldens salen idénticos sin `-update`; el candado de orden está sin etiqueta, verde y comprobado por mutación; `make ci-local` rc=0 con 0 SKIP; PR a `dev` abierto desde la rama de la sesión.
- Decisiones: D-F8-3 (goldens), D-F8-6 en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar, procesos-testcontainers (arnés de la suite de `events.Store`), describir-pr.

- Base: F8-02 integrada en `dev` (PR #61). Trabaja en TU rama partida de `origin/dev` (p. ej. `reorg/f8-03-events-cart`), nunca en `dev` (regla 6 del `CLAUDE.md`).

Nivel de ceremonia: el del inventario E-12 aprobado, que está en el README de F8 (§ «Inventario E-12») y manda sobre `diseno.md` §0.1: `events` — `events`, `kinds` simple; `summary`, `dispatcher`, `menu` medio; `store` y `thread_reader` complejo (suite con casos de carrera, mutantes). `cart` — `effects`, `variants`, `resume`, `validate` simple; `cart`, `troceo`, `preresolutor`, `screens`, `prime`, `state`, `revalidate`, `buyer`, `consulta` medio; `projection` complejo. Nacen partidos (E-13): `events/store.go` y `cart/cart.go` (re-anclar el candado AST de `Step`). No hace falta volver a pedir aprobación.

Lo que F8-01 y F8-02 te dejan dicho (léelo antes de empezar): hallazgos 3, 6, 7, 13 y 15–19 del README de F8 y las DOS tablas de nombres de plan/F8-conversacion/tareas.md (antes del bloque 2 y antes del bloque 3).
- 🔴 Hallazgo 3, SIN decidir: `Capas["conversacion"]` de `internal/modulos/fronteras_test.go` no incluye `catalogo`, `cart` lo necesita (T8.15), `Capas["catalogo"]` ya apunta a `conversacion`, y la prohibición `conversacion → catalogo/indice` es por subpaquete, que el motor de fronteras no sabe expresar. Antes de escribir `cart`, mide qué importa el `cart` viejo de catálogo y propón a Jhoan cómo queda la frontera; si no hay decisión en la conversación, haz `events` entero y PARA ahí.
- Los exportados de `modules/consulta.go`, `engine/consulta.go` y `turnoacotado` están en inglés (`Query`, `Verdict`, `QueryResolver.ResolveQuery`, `WithQueryResolver`…). `cart/consulta.go` usa esos. Los valores observables no cambian.
- El gemelo en memoria de `store` imita a Postgres (D-F8-7, D-F8-8): no escribe ni devuelve `CustomerNote` en las lecturas de cabecera, elige la solicitud abierta más reciente y rechaza una segunda línea `_shipping`. Un test viejo de `cart` que dependiera de lo contrario probaba algo que producción no hace.
- Cuando dos paquetes que nacen en paralelo comparten un puerto, fija el nombre y la firma en el prompt de los DOS sub-agentes (hallazgo 18: en F8-02 coincidieron por suerte).
- Ficheros que se llaman entre sí no pasan el lint `unused` por separado: su verde va en un commit, y se dice (hallazgo 19).
- Sub-agentes en worktrees: nacen de `origin/main`; diles que se pongan en el SHA de tu rama y corran `make tools`. Sus commits entran en tu rama por `cherry-pick`. Borra los worktrees antes de `make test-pendiente`.
- Gates sin carga: no corras `make test-procesos` ni `make ci-docker` en paralelo con nada. Esta sesión SÍ corre contra Postgres (la suite de `events.Store`, en `test/procesos/…_contrato_test.go`, patrón de F9-A). Intermitencias conocidas, que se leen antes de repetir y no se arreglan aquí: `TestP6_CRMBridge/callback_body_adversarial` (`broken pipe`) y la carrera de D-F7-9 («el job no trae literal que analizar»), en `TestP4_MessageToDraft` y `TestP6_CRMBridge`. Si aparecen, anótalas con su firma y repite.
- Pendiente de Jhoan, sin bloquear: las 🟡 de los hallazgos 15 y 17 de F8 (rarezas del `engine` y de `turnoacotado`) y los 15 y 22 de F7. No los toques salvo que él lo decida en la conversación.

Orquesta con sub-agentes y protege tu contexto. Son 21 ficheros: si no cabe en 90 min, para en un punto limpio (lo natural: `events` verde y empujado), cierra con las tres cosas y dilo.
`admin` NO va en esta sesión aunque el nombre de la ficha lo diga: importa `runtime` y nace en F8-06 (T8.13).
`events.Store` es un puerto con BD sin gemelo: doble en memoria + `Contrato(t, func(t) Montaje)` contra el doble y contra Postgres. Los goldens del carrito se copian literales (D-F8-3).

Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md (con minutos, D-R-6), hallazgos en el README de la fase; y la fila de F8-03 en plan/sesiones/README.md y el resumen de plan/README.md. Push de TU rama (`git push -u origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev --body-file …`, cuerpo con `describir-pr`, «Integrar SIN squash»). Nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.12, T8.15–T8.17, T8.24, T8.25 `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md).
- Un PR con `--base dev` desde la rama de la sesión (push de la rama, rc sin pipe), con «integrar SIN squash»; nada commiteado directamente en `dev` (regla 6 del `CLAUDE.md`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo entonces se escribe un traspaso.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
