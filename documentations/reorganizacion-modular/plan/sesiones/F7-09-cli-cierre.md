# F7-09 · F7 · I · cierre local · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F7 · captacion](../F7-captacion/README.md) · I · cierre local (💻) |
| Tareas | T7.27–T7.29 |
| Depende de | F7-08 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F7 bloque I» |
| Se para cuando | todas las tareas del bloque `[x]` con SHA y los gates en verde |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión web que cierra (F7-08) terminó y dejó PR y, si aplica, traspaso.
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F7-09 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F7 · captacion → documentations/reorganizacion-modular/plan/F7-captacion/
- Bloque(s): I · cierre local
- Tareas: T7.27–T7.29 de plan/F7-captacion/tareas.md
- Te paras cuando: todas las tareas del bloque `[x]` con SHA, los gates del bloque en verde y (web) el PR abierto hacia `dev`.
- Decisiones: las que en plan/DECISIONES.md bloquean «F7 bloque I» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, traspaso-web-local, procesos-testcontainers.

Si Jhoan aceptó adelantar F9 (D-F9-1 = sí en `plan/DECISIONES.md`), este cierre incluye la pasada de los procesos del módulo contra el binario NUEVO (la tarea 🕐 del bloque y la del bloque C de `plan/F9-procesos/tareas.md` que corresponda a F7).

Al terminar: traspaso con «CERRADO <fecha>», tareas [x] con SHA, ESTADO.md, `git push origin dev` (rc sin pipe). No toques `main`.
```

## Al terminar debe existir

- La rama de la web integrada en `dev` **sin squash** y `dev` empujado.
- En [`../F7-captacion/tareas.md`](../F7-captacion/tareas.md): las tareas 💻 y 🌐→💻 de T7.27–T7.29 `[x]` con SHA.
- El traspaso con su sección `CERRADO <fecha>` (qué se refutó de su §7).
- `ESTADO.md` de la reorganización al día.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
