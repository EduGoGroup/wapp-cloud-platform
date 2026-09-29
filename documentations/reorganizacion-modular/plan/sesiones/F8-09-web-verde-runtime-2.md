# F8-09 · F8 · I · verde del runtime (II) · 🌐 web

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · I · verde del runtime (II) (🌐) |
| Tareas | T8.27–T8.28 |
| Depende de | F8-08 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F8 bloque I» |
| Se para cuando | `grep -rn 'pendiente.Implementar' --include='*.go' C | wc -l` → **0**. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-08) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones que bloquean «F8 bloque I» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F8-09 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque(s): I · verde del runtime (II)
- Tareas: T8.27–T8.28 de plan/F8-conversacion/tareas.md
- Te paras cuando: `grep -rn 'pendiente.Implementar' --include='*.go' C | wc -l` → **0**.
- Decisiones: las que en plan/DECISIONES.md bloquean «F8 bloque I» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd (pasada de verde), validar-antes-de-cerrar.

Orquesta con sub-agentes (por paquete en contratos, por fichero en verde) y protege tu contexto.
Al terminar: tareas [x] con SHA, ESTADO.md, traspaso si algo lo cierra la local, push de TU rama y `gh pr create --base dev` con «integrar SIN squash». No empieces el bloque siguiente.
```

## Al terminar debe existir

- Commits con los prefijos de la plantilla (`rojo(<m>)`, `verde(<m>)`, `conmutar(<m>)`, `andamiaje(f0)`, `procesos(<p>)`, `relevo`, `docs(reorganizacion-modular)`, `chore(deps)`), empujados a la rama de la sesión.
- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.27–T8.28 `[x]` con SHA (o `[~]` con lo que falta).
- `ESTADO.md` de la reorganización al día.
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash».
- Si el bloque es 🌐→💻: el traspaso en `documentations/reorganizacion-modular/traspasos/`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
