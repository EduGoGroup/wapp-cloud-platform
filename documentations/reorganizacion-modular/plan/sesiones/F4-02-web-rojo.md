# F4-02 · F4 · B · contratos y rojo de todo el módulo · 🌐 web

| | |
|---|---|
| Fase · bloque | [F4 · inferencia](../F4-inferencia/README.md) · B · contratos y rojo de todo el módulo (🌐) |
| Tareas | T4.3–T4.10 |
| Depende de | F4-01 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F4 bloque B» |
| Se para cuando | `make test-pendiente` cuenta **≈54** llamadas en `internal/modulos/inferencia` + `puente_inferencia.go` (la cifra exacta se anota aquí al cerrar T4.10), gate rojo rc=0, gate ci-local rc=0, `make lint` sin `unused`. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F4-01) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones que bloquean «F4 bloque B» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F4-02 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F4 · inferencia → documentations/reorganizacion-modular/plan/F4-inferencia/
- Bloque(s): B · contratos y rojo de todo el módulo
- Tareas: T4.3–T4.10 de plan/F4-inferencia/tareas.md
- Te paras cuando: `make test-pendiente` cuenta **≈54** llamadas en `internal/modulos/inferencia` + `puente_inferencia.go` (la cifra exacta se anota aquí al cerrar T4.10), gate rojo rc=0, gate ci-local rc=0, `make lint` sin `unused`.
- Decisiones: las que en plan/DECISIONES.md bloquean «F4 bloque B» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd (pasada de contrato y rojo), validar-antes-de-cerrar.

Orquesta con sub-agentes (por paquete en contratos, por fichero en verde) y protege tu contexto.
Al terminar: tareas [x] con SHA, ESTADO.md, traspaso si algo lo cierra la local, push de TU rama y `gh pr create --base dev` con «integrar SIN squash». No empieces el bloque siguiente.
```

## Al terminar debe existir

- Commits con los prefijos de la plantilla (`rojo(<m>)`, `verde(<m>)`, `conmutar(<m>)`, `andamiaje(f0)`, `procesos(<p>)`, `relevo`, `docs(reorganizacion-modular)`, `chore(deps)`), empujados a la rama de la sesión.
- En [`../F4-inferencia/tareas.md`](../F4-inferencia/tareas.md): T4.3–T4.10 `[x]` con SHA (o `[~]` con lo que falta).
- `ESTADO.md` de la reorganización al día.
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash».
- Si el bloque es 🌐→💻: el traspaso en `documentations/reorganizacion-modular/traspasos/`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
