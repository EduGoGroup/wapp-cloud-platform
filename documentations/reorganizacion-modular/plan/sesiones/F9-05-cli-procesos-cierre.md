# F9-05 · F9 · B1 · procesos de plataforma y acceso + B2 · procesos de negocio · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F9 · procesos (testcontainers)](../F9-procesos/README.md) · B1 · procesos de plataforma y acceso + B2 · procesos de negocio (🌐→💻) |
| Tareas | T9.13–T9.16 🕐 + T9.17–T9.21, T9.35 🕐 |
| Depende de | F9-04 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F9 bloque B1/B2» |
| Se para cuando | P4–P8 (y P10 si D-F9-4) con `RC=0` contra viejo y nuevo, en local; traspaso `TRASPASO-F9-procesos-b2.md` con `CERRADO`. |

> Cierra la parte 💻 de los bloques B1 y B2.

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión web que cierra (F9-04) terminó y dejó PR y, si aplica, traspaso.
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F9-05 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F9 · procesos (testcontainers) → documentations/reorganizacion-modular/plan/F9-procesos/
- Bloque(s): B1 · procesos de plataforma y acceso + B2 · procesos de negocio
- Tareas: T9.13–T9.16 🕐 + T9.17–T9.21, T9.35 🕐 de plan/F9-procesos/tareas.md
- Entrada: bloque A `CERRADO`
- Te paras cuando: P4–P8 (y P10 si D-F9-4) con `RC=0` contra viejo y nuevo, en local; traspaso `TRASPASO-F9-procesos-b2.md` con `CERRADO`.
- Decisiones: las que en plan/DECISIONES.md bloquean «F9 bloque B1/B2» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, traspaso-web-local, procesos-testcontainers.

Corre los procesos de B1 y B2 contra el binario VIEJO (así se demuestra que el test es correcto) y contra el nuevo, SKIP = 0.

Al terminar: traspaso con «CERRADO <fecha>», tareas [x] con SHA, ESTADO.md, `git push origin dev` (rc sin pipe). No toques `main`.
```

## Al terminar debe existir

- La rama de la web integrada en `dev` **sin squash** y `dev` empujado.
- En [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md): las tareas 💻 y 🌐→💻 de T9.13–T9.16 🕐 + T9.17–T9.21, T9.35 🕐 `[x]` con SHA.
- El traspaso con su sección `CERRADO <fecha>` (qué se refutó de su §7).
- `ESTADO.md` de la reorganización al día.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
