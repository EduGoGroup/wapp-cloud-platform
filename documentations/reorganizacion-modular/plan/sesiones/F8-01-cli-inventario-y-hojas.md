# F8-01 · F8 · inventario E-12 y hojas · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 1 · inventario E-12 y hojas |
| Entorno | 💻 solo local |
| Nivel (E-12) | medio · `store` complejo · `content` simple (provisional; lo fija el inventario de esta sesión) |
| Duración objetivo | 45–90 min |
| Tareas | T8.1–T8.8, T8.22 |
| Depende de | F7-05 |
| Decisiones | D-F8-1 (= D-F5-1), D-F8-6 |
| Se para cuando | inventario E-12 **aprobado por Jhoan**; `model`·`trigger`·`content`·`store`·`modules` verdes; `storehelpertest` y `triggerhelpertest` corren `Contrato` en memoria y en Postgres; `pendiente` del módulo = 0; `make ci-local` rc=0 con 0 SKIP; PR a `dev` abierto desde la rama de la sesión |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F7-05) está integrada y empujada en `dev`.
- [ ] Decisiones rellenas en [`../DECISIONES.md`](../DECISIONES.md): D-F8-1 (= D-F5-1), D-F8-6.
- [ ] El inventario E-12 lo apruebas tú **dentro** de la sesión: la sesión para y espera.
- [ ] `go1.26.5` y `golangci-lint v2.12.2` disponibles (`make toolchain`).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-01 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque: 1 · inventario E-12 y hojas
- Tareas: T8.1–T8.8 y T8.22 de plan/F8-conversacion/tareas.md
- Te paras cuando: el inventario E-12 está aprobado por Jhoan; `model`, `trigger`, `content`, `store` y `modules` están verdes; `storehelpertest` y `triggerhelpertest` corren `Contrato` en memoria y en Postgres; `pendiente` del módulo = 0; `make ci-local` rc=0 con 0 SKIP; PR a `dev` abierto desde la rama de la sesión.
- Decisiones: D-F8-1 (= D-F5-1), D-F8-6 en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar, procesos-testcontainers (arnés de las suites).

No hay rama web que integrar ni traspaso que cerrar: creas TU rama desde `dev` (`git checkout -b <rama> dev`) y trabajas en ella, nunca sobre `dev` (regla 6 del `CLAUDE.md`).
Empieza por el inventario E-12 (T8.2): tabla de niveles (archivo · estado en memoria · concurrencia · BD/transacciones · nº de consumidores · nivel) + adaptadores `bridge_<x>.go` (F8 crea 0 y retira todos los vivos: lista real con `ls internal/arranque/bridge_*.go`). Preséntaselo a Jhoan y **PARA hasta que lo apruebe**; luego sigue con las hojas.
Las suites de `store` y `trigger` son `Contrato(t, func(t) Montaje)`: en memoria y, con el arnés de F9-A, en Postgres (testcontainers, nunca un Postgres vivo). La marca de estado vigila todas las columnas que la operación puede tocar.

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.1–T8.8, T8.22 `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md).
- El inventario E-12 aprobado, en el [README de la fase](../F8-conversacion/README.md) (tabla de niveles y lista de adaptadores a retirar).
- Un PR con `--base dev` desde la rama de la sesión (push de la rama, rc sin pipe), con «integrar SIN squash»; nada commiteado directamente en `dev` (regla 6 del `CLAUDE.md`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo entonces se escribe un traspaso.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
