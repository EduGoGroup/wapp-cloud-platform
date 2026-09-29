# F9-02 · F9 · A · el arnés · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F9 · procesos (testcontainers)](../F9-procesos/README.md) · A · el arnés (🌐→💻) |
| Tareas | T9.1–T9.12 🕐 |
| Depende de | F9-01 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F9 bloque A» |
| Se para cuando | P0 verde contra los dos binarios **en local** y `ci-local` rc=0 con el candado ampliado. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión web que cierra (F9-01) terminó y dejó PR y, si aplica, traspaso.
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F9-02 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F9 · procesos (testcontainers) → documentations/reorganizacion-modular/plan/F9-procesos/
- Bloque(s): A · el arnés
- Tareas: T9.1–T9.12 🕐 de plan/F9-procesos/tareas.md
- Entrada: F0 cerrada. Puede correr en paralelo con F1 (otra sesión)
- Te paras cuando: P0 verde contra los dos binarios **en local** y `ci-local` rc=0 con el candado ampliado.
- Decisiones: las que en plan/DECISIONES.md bloquean «F9 bloque A» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, traspaso-web-local, procesos-testcontainers.

Corre P0 contra los DOS binarios (`cmd/server` y `cmd/server-modular`) con testcontainers; `go.sum` con red real si cambió `go.mod` (decisión T-2: commit `chore(deps)` aislado).

Al terminar: traspaso con «CERRADO <fecha>», tareas [x] con SHA, ESTADO.md, `git push origin dev` (rc sin pipe). No toques `main`.
```

## Al terminar debe existir

- La rama de la web integrada en `dev` **sin squash** y `dev` empujado.
- En [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md): las tareas 💻 y 🌐→💻 de T9.1–T9.12 🕐 `[x]` con SHA.
- El traspaso con su sección `CERRADO <fecha>` (qué se refutó de su §7).
- `ESTADO.md` de la reorganización al día.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
