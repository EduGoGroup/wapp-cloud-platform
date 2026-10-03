# F8-04 · F8 · `runtime` (1): contratos de los 23 y soporte · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 4 · `runtime` (1): contratos de los 23 y soporte |
| Entorno | 💻 solo local |
| Nivel (E-12) | complejo (estado en memoria y concurrencia): esquema completo E-2…E-9 |
| Duración objetivo | 45–90 min |
| Tareas | T8.18–T8.21, T8.26 |
| Depende de | F8-03 |
| Decisiones | D-F8-4 (regla «`events` sin clasificador»), D-F8-5 (lista blanca) |
| Se para cuando | los 23 contratos de `runtime` en rojo y `runtimehelpertest` verde; candado de rachas escrito; lista blanca de `conversacion` completa; los 12 de soporte verdes con `go test -race` rc=0; `make ci-local` rc=0 con 0 SKIP; PR a `dev` abierto desde la rama de la sesión |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-03) está integrada y empujada en `dev`.
- [ ] Decisiones rellenas en [`../DECISIONES.md`](../DECISIONES.md): D-F8-4 (regla «`events` sin clasificador»), D-F8-5 (lista blanca).
- [ ] `go1.26.5` y `golangci-lint v2.12.2` disponibles (`make toolchain`).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-04 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque: 4 · `runtime` (1): contratos de los 23 y soporte
- Tareas: T8.18–T8.21 y T8.26 de plan/F8-conversacion/tareas.md
- Te paras cuando: los 23 contratos de `runtime` están en rojo y `runtimehelpertest` en verde; el candado de rachas está escrito; la lista blanca de `conversacion` está completa; los 12 de soporte están verdes con `go test -race` rc=0; `make ci-local` rc=0 con 0 SKIP; PR a `dev` abierto desde la rama de la sesión.
- Decisiones: D-F8-4 (regla «`events` sin clasificador»), D-F8-5 (lista blanca) en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

No hay rama web que integrar ni traspaso que cerrar: creas TU rama desde `dev` (`git checkout -b <rama> dev`) y trabajas en ella, nunca sobre `dev` (regla 6 del `CLAUDE.md`).
Orden: primero los contratos de los 23 (T8.18–T8.21), después el verde de los 12 de soporte (T8.26): `send.go`, `thread.go` y `welcome.go` cuelgan de `*Runtime`.
Reloj siempre inyectado; prohibido `time.Sleep`. Mutantes donde haya estado o concurrencia (`keyedmutex`, `streak`).

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.18–T8.21, T8.26 `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md).
- Un PR con `--base dev` desde la rama de la sesión (push de la rama, rc sin pipe), con «integrar SIN squash»; nada commiteado directamente en `dev` (regla 6 del `CLAUDE.md`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo entonces se escribe un traspaso.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
