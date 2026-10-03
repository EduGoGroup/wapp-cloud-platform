# F7-03 · F7 · `pipeline`, `intakeahead`, `reanalisis` · 🌐❓ web si queda saldo

| | |
|---|---|
| Fase · bloque | [F7 · captacion](../F7-captacion/README.md) · C · `pipeline`, `intakeahead`, `reanalisis` |
| Entorno | 🌐❓ web si queda saldo de la promoción; si no, local |
| Nivel (E-12) | `pipeline`, `intakeahead` complejo · `reanalisis` medio — provisional; manda el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T7.10–T7.13, T7.18–T7.20 |
| Depende de | F7-02 |
| Decisiones | D-F7-3 (resuelta en T7.1) |
| Se para cuando | pendientes de `internal/modulos/captacion` = 0 · puentes (import) de `reanalisis` declarados (2, o 3 si T7.12 no evita el de `runtime`) · mutantes pasados en lo que el inventario dejó en complejo · `make ci-local` rc=0 con 0 SKIP |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F7-02) está integrada en `dev` (PR fusionado **sin squash**, o empujada si corrió en local).
- [ ] Las decisiones de la cabecera están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md). Sin saldo web: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F7-03 del plan de reconstrucción modular de wapp-cloud-platform · 🌐❓ web si queda saldo.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F7 · captacion → documentations/reorganizacion-modular/plan/F7-captacion/
- Bloque: C · `pipeline`, `intakeahead`, `reanalisis`
- Tareas: T7.10–T7.13, T7.18–T7.20 de plan/F7-captacion/tareas.md
- Te paras cuando: pendientes de `internal/modulos/captacion` = 0 · puentes (import) de `reanalisis` declarados (2, o 3 si T7.12 no evita el de `runtime`) · mutantes pasados en lo que el inventario dejó en complejo · `make ci-local` rc=0 con 0 SKIP
- Decisiones: D-F7-3 (resuelta en T7.1). Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

El worker y el aforo se prueban con reloj falso, sin `sleep` real. La tabla de `make cobertura-ficheros` va al PR como informe; no bloquea.
Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Orquesta con sub-agentes por paquete y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama y `gh pr create --base dev` con «integrar SIN squash»; traspaso solo si algo lo cierra la local. Si corres esto en local (sin saldo web): mismo encargo, sin PR ni traspaso, `git push origin dev` leyendo el rc sin pipe.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F7-captacion/tareas.md`](../F7-captacion/tareas.md): T7.10–T7.13, T7.18–T7.20 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F7-captacion/README.md).
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash» (en local: `dev` empujado).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
