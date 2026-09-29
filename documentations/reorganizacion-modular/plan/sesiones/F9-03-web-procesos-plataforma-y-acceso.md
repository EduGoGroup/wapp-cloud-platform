# F9-03 · F9 · B1 · procesos de plataforma y acceso · 🌐 web

| | |
|---|---|
| Fase · bloque | [F9 · procesos (testcontainers)](../F9-procesos/README.md) · B1 · procesos de plataforma y acceso (🌐→💻) |
| Tareas | T9.13–T9.16 🕐 |
| Depende de | F1-05 |
| Decisiones | Las de [`../DECISIONES.md`](../DECISIONES.md) que bloquean «F9 bloque B1» |
| Se para cuando | P1, P2, P3, P9 con `RC=0` contra viejo y nuevo, en local. Cada tarea: **Ficheros** el `p<n>_…_test.go` de `diseno.md` §4 · **Hecho cuando** la definición de hecho de un proceso (`reglas.md` §4) · **Gate** web: vet `-tags integracion` rc=0 (y pre-chequeo si hay Docker); local: `make test-procesos` RC=0 ×2 · **Commit** `procesos(<proceso>): …`. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F1-05) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones que bloquean «F9 bloque B1» en [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F9-03 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F9 · procesos (testcontainers) → documentations/reorganizacion-modular/plan/F9-procesos/
- Bloque(s): B1 · procesos de plataforma y acceso
- Tareas: T9.13–T9.16 🕐 de plan/F9-procesos/tareas.md
- Entrada: bloque A `CERRADO`
- Te paras cuando: P1, P2, P3, P9 con `RC=0` contra viejo y nuevo, en local. Cada tarea: **Ficheros** el `p<n>_…_test.go` de `diseno.md` §4 · **Hecho cuando** la definición de hecho de un proceso (`reglas.md` §4) · **Gate** web: vet `-tags integracion` rc=0 (y pre-chequeo si hay Docker); local: `make test-procesos` RC=0 ×2 · **Commit** `procesos(<proceso>): …`.
- Decisiones: las que en plan/DECISIONES.md bloquean «F9 bloque B1» deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: procesos-testcontainers, validar-antes-de-cerrar.

Orquesta con sub-agentes (por paquete en contratos, por fichero en verde) y protege tu contexto.
Al terminar: tareas [x] con SHA, ESTADO.md, traspaso si algo lo cierra la local, push de TU rama y `gh pr create --base dev` con «integrar SIN squash». No empieces el bloque siguiente.
```

## Al terminar debe existir

- Commits con los prefijos de la plantilla (`rojo(<m>)`, `verde(<m>)`, `conmutar(<m>)`, `andamiaje(f0)`, `procesos(<p>)`, `relevo`, `docs(reorganizacion-modular)`, `chore(deps)`), empujados a la rama de la sesión.
- En [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md): T9.13–T9.16 🕐 `[x]` con SHA (o `[~]` con lo que falta).
- `ESTADO.md` de la reorganización al día.
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash».
- Si el bloque es 🌐→💻: el traspaso en `documentations/reorganizacion-modular/traspasos/`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
