# F8-07 · F8 · cierre · 💻 CLI

> ✎ **2026-10-10 (Jhoan, tras F8-06b)**: esta sesión se **partió en tres**. Lo que tenía delante va ahora en
> [`F8-07a`](F8-07a-cli-limpieza-d-f7-9.md) (T8.41–T8.43: limpieza tras D-F7-9) y en
> [`F8-07b`](F8-07b-cli-docker-y-arranque-real.md) (T8.36–T8.37: `ci-docker`, suites contra Postgres y arranque real).
> Aquí queda **solo cerrar la fase** (T8.38).

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 7 · cierre |
| Entorno | 💻 solo local |
| Nivel (E-12) | no aplica (no se escribe código de producción) |
| Duración objetivo | 45–90 min |
| Tareas | T8.38 (✎ 2026-10-10: T8.41–T8.43 pasan a F8-07a y T8.36–T8.37 a F8-07b) |
| Depende de | F8-07b (✎ 2026-10-10: antes F8-06b) |
| Decisiones | D-F9-1 (adelantar F9) |
| Se para cuando | la definición de hecho de [`reglas.md`](../F8-conversacion/reglas.md) §4 entera: suites de los puertos con BD verdes contra Postgres · `cmd/server-modular` arranca solo y recorre una conversación · `make test-procesos` y `make ci-local` rc=0 con 0 SKIP · PR a `dev` abierto desde la rama de la sesión |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-07b; ✎ 2026-10-10: antes F8-06b) está integrada y empujada en `dev`.
- [ ] Decisiones rellenas en [`../DECISIONES.md`](../DECISIONES.md): D-F9-1 (adelantar F9).
- [ ] Docker encendido en el Mac.
- [ ] `go1.26.5` y `golangci-lint v2.12.2` disponibles (`make toolchain`).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-07 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque: 7 · cierre
- Tareas: T8.38 de plan/F8-conversacion/tareas.md (T8.41–T8.43 son de F8-07a y T8.36–T8.37 de F8-07b: tienen que estar `[x]` con SHA; si no, PARA y dilo)
- Te paras cuando: se cumple entera la definición de hecho de plan/F8-conversacion/reglas.md §4: las suites de los puertos con BD pasan contra Postgres, `cmd/server-modular` arranca solo y recorre una conversación, `make test-procesos` y `make ci-local` dan rc=0 con 0 SKIP, y `dev` está empujado.
- Decisiones: D-F9-1 (adelantar F9) en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, procesos-testcontainers.

No hay rama web que integrar ni traspaso que cerrar: creas TU rama desde `dev` (`git checkout -b <rama> dev`) y trabajas en ella, nunca sobre `dev` (regla 6 del `CLAUDE.md`).
✎ 2026-10-10: las suites contra Postgres, los procesos, `make ci-docker`, el arranque real y la refutación del hallazgo 51 los hizo F8-07b. Aquí NO se repiten enteros: se comprueba que F8-07b los dejó escritos con su rc, se repite el gate corto sobre la cabeza de `dev` (`make ci-local` y `BINARIO=nuevo make test-procesos`, rc sin pipe, 0 SKIP) y se cierra la fase: los 10 puntos de plan/F8-conversacion/reglas.md §4, cada uno con su número y su evidencia, en ESTADO.md. Si alguno no se cumple, la fase NO se cierra: dilo.

✎ 2026-10-11 (tras F8-07b, PR #71 integrado): lee los hallazgos 64–67 del README de F8. Cuatro puntos del hallazgo 51 no se ven desde fuera (identidad del `entResolver`, mux de detrás vacío, receptor de `OnHeartbeat`, runtime de J19) y Jhoan los da por buenos con lo estático que ya los cubre: no los reabras. Lo que F8-07b dejó SIN hacer y te toca decir en el cierre (hecho, o deuda con su número): las cabeceras de `05`, `04` y la documentación de la pieza (`contratos.md`, `arquitectura.md`) que F8-06 no tocó; los 22 mutantes del candado de cableado de F8-06, no repetidos por un orquestador; el mutante `WithResumePolicy`, que sobrevive (hallazgo 66); y el golden en un Edge real (deuda D-38). `make test-procesos` da ya PASS=1291 por binario.

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.38 `[x]` con SHA (✎ 2026-10-10: T8.36–T8.37 y T8.41–T8.43 los cierran F8-07b y F8-07a).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md).
- `ESTADO.md` y el README de la fase con F8 «cerrada» y los SHA.
- Un PR con `--base dev` desde la rama de la sesión (push de la rama, rc sin pipe), con «integrar SIN squash»; nada commiteado directamente en `dev` (regla 6 del `CLAUDE.md`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo entonces se escribe un traspaso.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
