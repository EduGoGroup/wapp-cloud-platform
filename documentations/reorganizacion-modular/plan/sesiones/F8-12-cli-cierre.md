# F8-12 · F8 · K · conmutar y retirar todos los puentes + L · cierre · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · K · conmutar y retirar todos los puentes + L · cierre (🌐→💻, 💻) |
| Tareas | T8.30–T8.35 + T8.36–T8.38 |
| Depende de | F8-11 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F8 bloque K/L» |
| Se para cuando | la definición de hecho de [`reglas.md`](../F8-conversacion/reglas.md) §4 entera. |

> Cierra la parte 💻 de los bloques K y L.

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión web que cierra (F8-11) terminó y dejó PR y, si aplica, traspaso.
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-12 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque(s): K · conmutar y retirar todos los puentes + L · cierre
- Tareas: T8.30–T8.35 + T8.36–T8.38 de plan/F8-conversacion/tareas.md
- Te paras cuando: la definición de hecho de plan/F8-conversacion/reglas.md §4 entera.
- Decisiones: las que en plan/DECISIONES.md bloquean «F8 bloque K/L» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, traspaso-web-local, procesos-testcontainers.

Si Jhoan aceptó adelantar F9 (D-F9-1 = sí en `plan/DECISIONES.md`), este cierre incluye la pasada de los procesos del módulo contra el binario NUEVO (la tarea 🕐 del bloque y la del bloque C de `plan/F9-procesos/tareas.md` que corresponda a F8).

Al terminar: traspaso con «CERRADO <fecha>», tareas [x] con SHA, ESTADO.md, `git push origin dev` (rc sin pipe). No toques `main`.
```

## Al terminar debe existir

- La rama de la web integrada en `dev` **sin squash** y `dev` empujado.
- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): las tareas 💻 y 🌐→💻 de T8.30–T8.35 + T8.36–T8.38 `[x]` con SHA.
- El traspaso con su sección `CERRADO <fecha>` (qué se refutó de su §7).
- `ESTADO.md` de la reorganización al día.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
