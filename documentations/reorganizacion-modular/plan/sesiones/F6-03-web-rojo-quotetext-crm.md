# F6-03 · F6 · C · contratos y rojo de `quotetext`, `telemetria`, `integrations`, `crmpush` · 🌐 web

| | |
|---|---|
| Fase · bloque | [F6 · solicitudes](../F6-solicitudes/README.md) · C · contratos y rojo de `quotetext`, `telemetria`, `integrations`, `crmpush` (🌐) |
| Tareas | T6.10–T6.13 |
| Depende de | F6-02 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F6 bloque C» |
| Se para cuando | todo el módulo en rojo · puente declarado · `make test-pendiente` = N anotado · PR. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F6-02) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones que bloquean «F6 bloque C» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F6-03 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F6 · solicitudes → documentations/reorganizacion-modular/plan/F6-solicitudes/
- Bloque(s): C · contratos y rojo de `quotetext`, `telemetria`, `integrations`, `crmpush`
- Tareas: T6.10–T6.13 de plan/F6-solicitudes/tareas.md
- Te paras cuando: todo el módulo en rojo · puente declarado · `make test-pendiente` = N anotado · PR.
- Decisiones: las que en plan/DECISIONES.md bloquean «F6 bloque C» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd (pasada de contrato y rojo), validar-antes-de-cerrar.

Orquesta con sub-agentes (por paquete en contratos, por fichero en verde) y protege tu contexto.
Al terminar: tareas [x] con SHA, ESTADO.md, traspaso si algo lo cierra la local, push de TU rama y `gh pr create --base dev` con «integrar SIN squash». No empieces el bloque siguiente.
```

## Al terminar debe existir

- Commits con los prefijos de la plantilla (`rojo(<m>)`, `verde(<m>)`, `conmutar(<m>)`, `andamiaje(f0)`, `procesos(<p>)`, `relevo`, `docs(reorganizacion-modular)`, `chore(deps)`), empujados a la rama de la sesión.
- En [`../F6-solicitudes/tareas.md`](../F6-solicitudes/tareas.md): T6.10–T6.13 `[x]` con SHA (o `[~]` con lo que falta).
- `ESTADO.md` de la reorganización al día.
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash».
- Si el bloque es 🌐→💻: el traspaso en `documentations/reorganizacion-modular/traspasos/`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
