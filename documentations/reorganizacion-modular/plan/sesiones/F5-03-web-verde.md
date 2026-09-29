# F5-03 · F5 · C · verde, fichero a fichero · 🌐 web

| | |
|---|---|
| Fase · bloque | [F5 · catalogo](../F5-catalogo/README.md) · C · verde, fichero a fichero (🌐) |
| Tareas | T5.9–T5.18 |
| Depende de | F5-02 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F5 bloque C» |
| Se para cuando | los 11 ficheros sin etiqueta, ≥ 80 % cada uno; gate ci-local rc=0. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F5-02) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones que bloquean «F5 bloque C» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F5-03 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F5 · catalogo → documentations/reorganizacion-modular/plan/F5-catalogo/
- Bloque(s): C · verde, fichero a fichero
- Tareas: T5.9–T5.18 de plan/F5-catalogo/tareas.md
- Te paras cuando: los 11 ficheros sin etiqueta, ≥ 80 % cada uno; gate ci-local rc=0.
- Decisiones: las que en plan/DECISIONES.md bloquean «F5 bloque C» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd (pasada de verde), validar-antes-de-cerrar.

Orquesta con sub-agentes (por paquete en contratos, por fichero en verde) y protege tu contexto.
Al terminar: tareas [x] con SHA, ESTADO.md, traspaso si algo lo cierra la local, push de TU rama y `gh pr create --base dev` con «integrar SIN squash». No empieces el bloque siguiente.
```

## Al terminar debe existir

- Commits con los prefijos de la plantilla (`rojo(<m>)`, `verde(<m>)`, `conmutar(<m>)`, `andamiaje(f0)`, `procesos(<p>)`, `relevo`, `docs(reorganizacion-modular)`, `chore(deps)`), empujados a la rama de la sesión.
- En [`../F5-catalogo/tareas.md`](../F5-catalogo/tareas.md): T5.9–T5.18 `[x]` con SHA (o `[~]` con lo que falta).
- `ESTADO.md` de la reorganización al día.
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash».
- Si el bloque es 🌐→💻: el traspaso en `documentations/reorganizacion-modular/traspasos/`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
