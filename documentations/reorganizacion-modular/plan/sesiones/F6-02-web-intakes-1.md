# F6-02 · F6 · `intakes` (1/2): contratos del paquete y tipos puros · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F6 · solicitudes](../F6-solicitudes/README.md) · F6-02 · `intakes` (1/2): contratos del paquete y tipos puros |
| Entorno | 💻 local (era 🌐 web: desde el 2026-10-04 todo es local, D-R-8) |
| Nivel (E-12) | Provisional: medio (tipos puros y acciones) · complejo (contratos de `memory.go`, `postgres.go`, `buyerdata_postgres.go`, `notifier.go`). Lo fija el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T6.6–T6.8, T6.15 |
| Depende de | F6-01 |
| Decisiones | D-F6-5, D-F6-6 (en [`../DECISIONES.md`](../DECISIONES.md), «F6 bloque B») |
| Se para cuando | los 24 ficheros de `S/intakes` tienen contrato y test, y `intakeshelpertest` está escrita · `note.go` y los 10 tipos puros en verde · `go vet -tags pendiente` rc=0 · `ci-local` rc=0 con 0 SKIP · PR |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F6-01) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones de la tabla están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`, con `dev` al día.

## Prompt

```text
Sesión F6-02 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F6 · solicitudes → documentations/reorganizacion-modular/plan/F6-solicitudes/
- Bloque: F6-02 · `intakes` (1/2): contratos del paquete y tipos puros
- Tareas: T6.6–T6.8, T6.15 de plan/F6-solicitudes/tareas.md
- Te paras cuando: los 24 ficheros de `S/intakes` tienen contrato y test, y `intakeshelpertest` está escrita · `note.go` y los 10 tipos puros en verde · `go vet -tags pendiente` rc=0 · `ci-local` rc=0 con 0 SKIP · PR.
- Decisiones: D-F6-5, D-F6-6 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.
- Orden: T6.6 → T6.15 (tipos puros, rojo y verde por fichero) → T6.7 (contratos de las 9 acciones) → T6.8 (contratos de almacenes, comprador y notificador + suite con `Montaje`). 🔴 El «DEK» de `buyerdata.go` es el envelope de PII de negocio, no la DEK del ADR-0007 (trampa T-1).

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.
Orquesta con sub-agentes (por paquete) y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama y `gh pr create --base dev` con «integrar SIN squash». Trabaja en TU rama partida de `dev` y con PR (`gh pr create --base dev`, «integrar SIN squash»), leyendo el rc sin pipe. Nunca directo a `dev` (regla 6 del `CLAUDE.md`).
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F6-solicitudes/tareas.md`](../F6-solicitudes/tareas.md): T6.6–T6.8, T6.15 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [`README.md`](../F6-solicitudes/README.md) de la fase (o «ninguno», dicho).
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash» (rama y PR; nunca directo a `dev`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el README de la fase), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
