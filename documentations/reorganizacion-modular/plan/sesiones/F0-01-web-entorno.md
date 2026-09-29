# F0-01 · F0 · A · el entorno web · 🌐 web

| | |
|---|---|
| Fase · bloque | [F0 · Andamiaje](../F0-andamiaje/README.md) · A · el entorno web (🌐) |
| Tareas | T0.0–T0.1 |
| Depende de | 00-02 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F0 bloque A» |
| Se para cuando | `06-entorno-web.md` §5 tiene el resultado de la primera sesión web, y el hook de `SessionStart` está commiteado y probado en sus dos ramas. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (00-02) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones que bloquean «F0 bloque A» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F0-01 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F0 · Andamiaje → documentations/reorganizacion-modular/plan/F0-andamiaje/
- Bloque(s): A · el entorno web
- Tareas: T0.0–T0.1 de plan/F0-andamiaje/tareas.md
- Te paras cuando: `06-entorno-web.md` §5 tiene el resultado de la primera sesión web, y el hook de `SessionStart` está commiteado y probado en sus dos ramas.
- Decisiones: las que en plan/DECISIONES.md bloquean «F0 bloque A» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo (§F0), validar-antes-de-cerrar.

Eres la PRIMERA sesión web del plan: además de T0.0–T0.1, haz la prueba de Docker + testcontainers de `plan/00-marco/flujo-web-local.md` §5 en un directorio temporal FUERA del árbol, sin commitearla, y anota en `documentations/reorganizacion-modular/06-entorno-web.md` §5 lo que viste (versiones, tiempos de `make ci-local`, si Docker y testcontainers funcionan, a qué rama empujas y si el proxy acepta `--force-with-lease`).

Orquesta con sub-agentes (por paquete en contratos, por fichero en verde) y protege tu contexto.
Al terminar: tareas [x] con SHA, ESTADO.md, traspaso si algo lo cierra la local, push de TU rama y `gh pr create --base dev` con «integrar SIN squash». No empieces el bloque siguiente.
```

## Al terminar debe existir

- Commits con los prefijos de la plantilla (`rojo(<m>)`, `verde(<m>)`, `conmutar(<m>)`, `andamiaje(f0)`, `procesos(<p>)`, `relevo`, `docs(reorganizacion-modular)`, `chore(deps)`), empujados a la rama de la sesión.
- En [`../F0-andamiaje/tareas.md`](../F0-andamiaje/tareas.md): T0.0–T0.1 `[x]` con SHA (o `[~]` con lo que falta).
- `ESTADO.md` de la reorganización al día.
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash».
- Si el bloque es 🌐→💻: el traspaso en `documentations/reorganizacion-modular/traspasos/`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
