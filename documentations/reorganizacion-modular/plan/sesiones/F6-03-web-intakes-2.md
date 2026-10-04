# F6-03 · F6 · `intakes` (2/2): almacenes, acciones, notificador y candados · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F6 · solicitudes](../F6-solicitudes/README.md) · F6-03 · `intakes` (2/2): almacenes, acciones, notificador y candados |
| Entorno | 💻 local (era 🌐 web: desde el 2026-10-04 todo es local, D-R-8) |
| Nivel (E-12) | Provisional: complejo (`memory.go`, `postgres.go`, `buyerdata_postgres.go`, `notifier.go`) · medio (las 9 acciones). Lo fija el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T6.9, T6.16–T6.18 |
| Depende de | F6-02 |
| Decisiones | D-F6-2 (en [`../DECISIONES.md`](../DECISIONES.md), «F6 bloque B») |
| Se para cuando | `grep -rn 'pendiente.Implementar' internal/modulos/solicitudes/intakes/*.go | wc -l` → 0 · suite `intakeshelpertest` verde en memoria con `-race` y 0 SKIP · candados del plazo y de la poda verdes; los dos INV-1 escritos tras `//go:build pendiente` · `ci-local` rc=0 · PR |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F6-02) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones de la tabla están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`, con `dev` al día.

## Prompt

```text
Sesión F6-03 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F6 · solicitudes → documentations/reorganizacion-modular/plan/F6-solicitudes/
- Bloque: F6-03 · `intakes` (2/2): almacenes, acciones, notificador y candados
- Tareas: T6.9, T6.16–T6.18 de plan/F6-solicitudes/tareas.md
- Te paras cuando: `grep -rn 'pendiente.Implementar' internal/modulos/solicitudes/intakes/*.go | wc -l` → 0 · suite `intakeshelpertest` verde en memoria con `-race` y 0 SKIP · candados del plazo y de la poda verdes; los dos INV-1 escritos tras `//go:build pendiente` · `ci-local` rc=0 · PR.
- Decisiones: D-F6-2 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.
- Orden: `memory.go` primero (la suite pasa entera), luego las 9 acciones, `notifier.go`, `buyerdata*.go`, `postgres.go` (SQL copiado literal) y los candados (INV-1, vencimiento, poda). La suite contra Postgres la deja escrita esta sesión y la corre F6-06.

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.
Orquesta con sub-agentes (por paquete) y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama y `gh pr create --base dev` con «integrar SIN squash». Trabaja en TU rama partida de `dev` y con PR (`gh pr create --base dev`, «integrar SIN squash»), leyendo el rc sin pipe. Nunca directo a `dev` (regla 6 del `CLAUDE.md`).
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F6-solicitudes/tareas.md`](../F6-solicitudes/tareas.md): T6.9, T6.16–T6.18 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [`README.md`](../F6-solicitudes/README.md) de la fase (o «ninguno», dicho).
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash» (rama y PR; nunca directo a `dev`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el README de la fase), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
