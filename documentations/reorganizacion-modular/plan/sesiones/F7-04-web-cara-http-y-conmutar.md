# F7-04 · F7 · cara HTTP y conmutar · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F7 · captacion](../F7-captacion/README.md) · D · cara HTTP y conmutar |
| Entorno | 💻 local (era 🌐 web: desde el 2026-10-04 todo es local, D-R-8) |
| Nivel (E-12) | `apipublica/{reanalyze,intents}.go` medio · `bridge_captacion.go` simple — provisional; manda el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T7.21–T7.26 (= TX.19–TX.21) |
| Depende de | F7-03 |
| Decisiones | D-F7-1, D-F7-2, D-F7-4 / D-FX-1 |
| Se para cuando | `reanalyze.go` e `intents.go` en verde · `bridge_captacion.go` y su test de cableado completo en verde (objetos nuevos por tipo + grep de imports viejos fuera del adaptador) · H1, E1, E2 por la cara nueva · huella igual · INV-1 con la lista de F7 · `FaseActual = 7` · `make ci-local` rc=0 con 0 SKIP |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F7-03) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones de la cabecera están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`, con `dev` al día.

## Prompt

```text
Sesión F7-04 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F7 · captacion → documentations/reorganizacion-modular/plan/F7-captacion/
- Bloque: D · cara HTTP y conmutar
- Tareas: T7.21–T7.26 (= TX.19–TX.21) de plan/F7-captacion/tareas.md
- Te paras cuando: `reanalyze.go` e `intents.go` en verde · `bridge_captacion.go` y su test de cableado completo en verde (objetos nuevos por tipo + grep de imports viejos fuera del adaptador) · H1, E1, E2 por la cara nueva · huella igual · INV-1 con la lista de F7 · `FaseActual = 7` · `make ci-local` rc=0 con 0 SKIP
- Decisiones: D-F7-1, D-F7-2, D-F7-4 / D-FX-1. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

El adaptador es nivel simple (una pasada) y construye dentro la segunda instancia vieja de `intake.Postgres`. Retira `llmConfigBridge` de `bridge_inferencia.go`. NO añadas `captacion` a `Conmutados`: entra en F8, cuando muera `bridge_captacion.go`.
Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Orquesta con sub-agentes por paquete y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama y `gh pr create --base dev` con «integrar SIN squash». Trabaja en TU rama partida de `dev` y con PR (`gh pr create --base dev`, «integrar SIN squash»), leyendo el rc sin pipe. Nunca directo a `dev` (regla 6 del `CLAUDE.md`).
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F7-captacion/tareas.md`](../F7-captacion/tareas.md): T7.21–T7.26 (= TX.19–TX.21) `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F7-captacion/README.md).
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash» (rama y PR; nunca directo a `dev`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el README de la fase), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
