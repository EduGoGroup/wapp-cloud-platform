# F9-04 · F9 · B2 · procesos de negocio · 🌐 web

| | |
|---|---|
| Fase · bloque | [F9 · procesos (testcontainers)](../F9-procesos/README.md) · B2 · procesos de negocio (🌐→💻) |
| Tareas | T9.17–T9.21, T9.35 🕐 |
| Depende de | F9-03 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F9 bloque B2» |
| Se para cuando | P4–P8 (y P10 si D-F9-4) con `RC=0` contra viejo y nuevo, en local; traspaso `TRASPASO-F9-procesos-b2.md` con `CERRADO`. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F9-03) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones que bloquean «F9 bloque B2» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F9-04 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F9 · procesos (testcontainers) → documentations/reorganizacion-modular/plan/F9-procesos/
- Bloque(s): B2 · procesos de negocio
- Tareas: T9.17–T9.21, T9.35 🕐 de plan/F9-procesos/tareas.md
- Entrada: B1 `CERRADO`. Misma forma que B1
- Te paras cuando: P4–P8 (y P10 si D-F9-4) con `RC=0` contra viejo y nuevo, en local; traspaso `TRASPASO-F9-procesos-b2.md` con `CERRADO`.
- Decisiones: las que en plan/DECISIONES.md bloquean «F9 bloque B2» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: procesos-testcontainers, validar-antes-de-cerrar.

Orquesta con sub-agentes (por paquete en contratos, por fichero en verde) y protege tu contexto.
Al terminar: tareas [x] con SHA, ESTADO.md, traspaso si algo lo cierra la local, push de TU rama y `gh pr create --base dev` con «integrar SIN squash». No empieces el bloque siguiente.
```

## Al terminar debe existir

- Commits con los prefijos de la plantilla (`rojo(<m>)`, `verde(<m>)`, `conmutar(<m>)`, `andamiaje(f0)`, `procesos(<p>)`, `relevo`, `docs(reorganizacion-modular)`, `chore(deps)`), empujados a la rama de la sesión.
- En [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md): T9.17–T9.21, T9.35 🕐 `[x]` con SHA (o `[~]` con lo que falta).
- `ESTADO.md` de la reorganización al día.
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash».
- Si el bloque es 🌐→💻: el traspaso en `documentations/reorganizacion-modular/traspasos/`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
