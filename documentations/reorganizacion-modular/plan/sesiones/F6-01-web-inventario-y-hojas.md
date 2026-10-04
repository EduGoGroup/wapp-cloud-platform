# F6-01 · F6 · inventario E-12 y hojas · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F6 · solicitudes](../F6-solicitudes/README.md) · F6-01 · inventario E-12 y hojas |
| Entorno | 💻 local (era 🌐 web: desde el 2026-10-04 todo es local, D-R-8) |
| Nivel (E-12) | Provisional: simple (`sigv1`, `note.go`, `tenantvars.go`) · complejo por criterio (`tenantvars/{memory,postgres}.go`). Lo fija el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T6.1–T6.5, T6.14 |
| Depende de | F45-03 |
| Decisiones | D-F6-4, D-F6-6; y D-F6-1 se confirma frente a P5 dentro de la sesión (en [`../DECISIONES.md`](../DECISIONES.md) figuran como «F6», «F6 bloque B» y «F6 bloque H») |
| Se para cuando | la tabla del inventario E-12 está **aprobada por Jhoan** · `sigv1` y `tenantvars` en verde (suite de `tenantvars` verde en memoria con `-race`) · `note.go` con contrato y test · `ci-local` rc=0 con 0 SKIP · PR |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F45-03) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones de la tabla están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] El inventario E-12 lo apruebas tú **dentro** de la sesión: quédate disponible al principio.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`, con `dev` al día.

## Prompt

```text
Sesión F6-01 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F6 · solicitudes → documentations/reorganizacion-modular/plan/F6-solicitudes/
- Bloque: F6-01 · inventario E-12 y hojas
- Tareas: T6.1–T6.5, T6.14 de plan/F6-solicitudes/tareas.md
- Te paras cuando: la tabla del inventario E-12 está aprobada por Jhoan · `sigv1` y `tenantvars` en verde (suite de `tenantvars` verde en memoria con `-race`) · `note.go` con contrato y test · `ci-local` rc=0 con 0 SKIP · PR.
- Decisiones: D-F6-4 y D-F6-6 deben estar rellenas en plan/DECISIONES.md (si falta alguna, PARA y dilo). D-F6-1 (segunda instancia vieja) choca con P5 (adaptador `bridge_<x>.go` estándar): pregúntaselo a Jhoan con el inventario; no lo decidas tú.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.
- Hojas: `integrations/sigv1`, `tenantvars` (+ suite `tenantvarshelpertest`) e `intakes/note.go`. Lo que el inventario fije como simple va en una pasada.

Empieza por el inventario E-12 (tabla de niveles + adaptadores `bridge_<x>.go`), preséntaselo a Jhoan y PARA hasta que lo apruebe; luego sigue.
Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.
Orquesta con sub-agentes (por paquete) y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama y `gh pr create --base dev` con «integrar SIN squash». Trabaja en TU rama partida de `dev` y con PR (`gh pr create --base dev`, «integrar SIN squash»), leyendo el rc sin pipe. Nunca directo a `dev` (regla 6 del `CLAUDE.md`).
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F6-solicitudes/tareas.md`](../F6-solicitudes/tareas.md): T6.1–T6.5, T6.14 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [`README.md`](../F6-solicitudes/README.md) de la fase (o «ninguno», dicho).
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash» (rama y PR; nunca directo a `dev`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el README de la fase), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
