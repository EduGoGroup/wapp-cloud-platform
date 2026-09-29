# F1-04 · F1 · D · cierre local e informe · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F1 · nucleo/contact — el piloto](../F1-nucleo-contact/README.md) · D · cierre local e informe (💻) |
| Tareas | T1.17–T1.19 |
| Depende de | F1-03 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F1 bloque D» |
| Se para cuando | suite contra Postgres corrida · todo en `dev` · `informe-piloto.md` escrito. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión web que cierra (F1-03) terminó y dejó PR y, si aplica, traspaso.
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F1-04 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F1 · nucleo/contact — el piloto → documentations/reorganizacion-modular/plan/F1-nucleo-contact/
- Bloque(s): D · cierre local e informe
- Tareas: T1.17–T1.19 de plan/F1-nucleo-contact/tareas.md
- Te paras cuando: suite contra Postgres corrida · todo en `dev` · `informe-piloto.md` escrito.
- Decisiones: las que en plan/DECISIONES.md bloquean «F1 bloque D» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, traspaso-web-local.

Al terminar: traspaso con «CERRADO <fecha>», tareas [x] con SHA, ESTADO.md, `git push origin dev` (rc sin pipe). No toques `main`.
```

## Al terminar debe existir

- La rama de la web integrada en `dev` **sin squash** y `dev` empujado.
- En [`../F1-nucleo-contact/tareas.md`](../F1-nucleo-contact/tareas.md): las tareas 💻 y 🌐→💻 de T1.17–T1.19 `[x]` con SHA.
- El traspaso con su sección `CERRADO <fecha>` (qué se refutó de su §7).
- `ESTADO.md` de la reorganización al día.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
