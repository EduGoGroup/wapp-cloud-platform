# F7-01 · F7 · inventario E-12 y hojas · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F7 · captacion](../F7-captacion/README.md) · A · inventario E-12 y hojas |
| Entorno | 💻 local (era 🌐 web: desde el 2026-10-04 todo es local, D-R-8) |
| Nivel (E-12) | `evidence` simple · `anclaje`, `casebank` medio · `intake`, `intentcfg` complejo — provisional; manda el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T7.1–T7.6, T7.14–T7.15 |
| Depende de | F6-06 |
| Decisiones | D-F7-3 (`memoria.go`: se verifica en T7.1) |
| Se para cuando | inventario E-12 aprobado por Jhoan · `evidence`, `intake`, `anclaje`, `intentcfg`, `casebank` en verde, con sus suites `Contrato…(t, func(t) Montaje)` pasando en memoria · `make ci-local` rc=0 con 0 SKIP |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F6-06) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones de la cabecera están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`, con `dev` al día.
- [ ] El inventario E-12 lo apruebas tú **dentro** de la sesión: quédate cerca al principio.

## Prompt

```text
Sesión F7-01 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F7 · captacion → documentations/reorganizacion-modular/plan/F7-captacion/
- Bloque: A · inventario E-12 y hojas
- Tareas: T7.1–T7.6, T7.14–T7.15 de plan/F7-captacion/tareas.md
- Te paras cuando: inventario E-12 aprobado por Jhoan · `evidence`, `intake`, `anclaje`, `intentcfg`, `casebank` en verde, con sus suites `Contrato…(t, func(t) Montaje)` pasando en memoria · `make ci-local` rc=0 con 0 SKIP
- Decisiones: D-F7-3 (`memoria.go`: se verifica en T7.1). Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

Empieza por el inventario E-12 (tabla de niveles + adaptadores `bridge_<x>.go`), preséntaselo a Jhoan y PARA hasta que lo apruebe; luego sigue.

Las suites de `intake` (dos), `casebank` e `intentcfg` reciben `Montaje` (P4); aquí corren en memoria, contra Postgres las corre F7-05.
Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Orquesta con sub-agentes por paquete y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama y `gh pr create --base dev` con «integrar SIN squash». Trabaja en TU rama partida de `dev` y con PR (`gh pr create --base dev`, «integrar SIN squash»), leyendo el rc sin pipe. Nunca directo a `dev` (regla 6 del `CLAUDE.md`).
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F7-captacion/tareas.md`](../F7-captacion/tareas.md): T7.1–T7.6, T7.14–T7.15 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F7-captacion/README.md).
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash» (rama y PR; nunca directo a `dev`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el README de la fase), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
