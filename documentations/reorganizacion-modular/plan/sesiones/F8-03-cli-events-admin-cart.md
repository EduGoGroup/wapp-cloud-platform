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
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar, procesos-testcontainers (arnés de la suite de `events.Store`).

No hay rama web que integrar ni traspaso que cerrar: creas TU rama desde `dev` (`git checkout -b <rama> dev`) y trabajas en ella, nunca sobre `dev` (regla 6 del `CLAUDE.md`).
`admin` NO va en esta sesión aunque el nombre de la ficha lo diga: importa `runtime` y nace en F8-06 (T8.13).
`events.Store` es un puerto con BD sin gemelo: doble en memoria + `Contrato(t, func(t) Montaje)` contra el doble y contra Postgres. Los goldens del carrito se copian literales (D-F8-3).

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
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
