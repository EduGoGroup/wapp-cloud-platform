# F9-01 · F9 · A · el arnés · 🌐 web

| | |
|---|---|
| Fase · bloque | [F9 · procesos (testcontainers)](../F9-procesos/README.md) · A · el arnés (🌐→💻) |
| Tareas | T9.1–T9.12 🕐 |
| Depende de | F0-06 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F9 bloque A» |
| Se para cuando | P0 verde contra los dos binarios **en local** y `ci-local` rc=0 con el candado ampliado. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F0-06) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones que bloquean «F9 bloque A» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F9-01 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F9 · procesos (testcontainers) → documentations/reorganizacion-modular/plan/F9-procesos/
- Bloque(s): A · el arnés
- Tareas: T9.1–T9.12 🕐 de plan/F9-procesos/tareas.md
- Entrada: F0 cerrada. Puede correr en paralelo con F1 (otra sesión)
- Te paras cuando: P0 verde contra los dos binarios **en local** y `ci-local` rc=0 con el candado ampliado.
- Decisiones: las que en plan/DECISIONES.md bloquean «F9 bloque A» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: procesos-testcontainers, validar-antes-de-cerrar.

F9 va ADELANTADO (D-F9-1): el arnés nace justo después de F0. Escribe el arnés y el proceso P0 y comprueba que compilan (`go vet -tags integracion ./test/procesos/...`). Si la sesión F0-01 dejó anotado en `06-entorno-web.md` §5 que testcontainers funciona en la web, córrelo aquí como PRE-chequeo (no cierra nada). Si D-F9-1 = no, esta sesión se salta y F9 se hace entero tras F8 (T9.34).

Orquesta con sub-agentes (por paquete en contratos, por fichero en verde) y protege tu contexto.
Al terminar: tareas [x] con SHA, ESTADO.md, traspaso si algo lo cierra la local, push de TU rama y `gh pr create --base dev` con «integrar SIN squash». No empieces el bloque siguiente.
```

## Al terminar debe existir

- Commits con los prefijos de la plantilla (`rojo(<m>)`, `verde(<m>)`, `conmutar(<m>)`, `andamiaje(f0)`, `procesos(<p>)`, `relevo`, `docs(reorganizacion-modular)`, `chore(deps)`), empujados a la rama de la sesión.
- En [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md): T9.1–T9.12 🕐 `[x]` con SHA (o `[~]` con lo que falta).
- `ESTADO.md` de la reorganización al día.
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash».
- Si el bloque es 🌐→💻: el traspaso en `documentations/reorganizacion-modular/traspasos/`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
