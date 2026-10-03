# F8-07 · F8 · cierre · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 7 · cierre |
| Entorno | 💻 solo local |
| Nivel (E-12) | no aplica (no se escribe código de producción) |
| Duración objetivo | 45–90 min |
| Tareas | T8.36–T8.38 (T8.37 = T9.29) |
| Depende de | F8-06 |
| Decisiones | D-F9-1 (adelantar F9) |
| Se para cuando | la definición de hecho de [`reglas.md`](../F8-conversacion/reglas.md) §4 entera: suites de los puertos con BD verdes contra Postgres · `cmd/server-modular` arranca solo y recorre una conversación · `make test-procesos` y `make ci-local` rc=0 con 0 SKIP · PR a `dev` abierto desde la rama de la sesión |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-06) está integrada y empujada en `dev`.
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
- Tareas: T8.36–T8.38 (T8.37 = T9.29 de plan/F9-procesos/tareas.md) de plan/F8-conversacion/tareas.md
- Te paras cuando: se cumple entera la definición de hecho de plan/F8-conversacion/reglas.md §4: las suites de los puertos con BD pasan contra Postgres, `cmd/server-modular` arranca solo y recorre una conversación, `make test-procesos` y `make ci-local` dan rc=0 con 0 SKIP, y `dev` está empujado.
- Decisiones: D-F9-1 (adelantar F9) en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, procesos-testcontainers.

No hay rama web que integrar ni traspaso que cerrar: creas TU rama desde `dev` (`git checkout -b <rama> dev`) y trabajas en ella, nunca sobre `dev` (regla 6 del `CLAUDE.md`).
Corre las suites `Contrato` de `store`, `trigger`, `events.Store`, `self_numbers` y `tenant_resolver` contra Postgres con el mismo `Montaje` que en memoria (P4) y los procesos del módulo (T9.29: «Entrante a respuesta», «De mensaje a borrador», «Re-análisis») contra el binario NUEVO. Nunca contra un Postgres vivo: testcontainers. Nunca `cmd/server` y `cmd/server-modular` a la vez.
Refuta lo que T8.35 dejó anotado en ESTADO.md (identidad de `entResolver`/`kp`/`gw`, barrido del agregador con BD real, golden).

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.36–T8.38 (T8.37 = T9.29) `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md).
- `ESTADO.md` y el README de la fase con F8 «cerrada» y los SHA.
- Un PR con `--base dev` desde la rama de la sesión (push de la rama, rc sin pipe), con «integrar SIN squash»; nada commiteado directamente en `dev` (regla 6 del `CLAUDE.md`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo entonces se escribe un traspaso.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
