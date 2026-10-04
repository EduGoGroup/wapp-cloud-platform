# F6-04 · F6 · `quotetext`, `telemetria`, `integrations`, `crmpush` · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F6 · solicitudes](../F6-solicitudes/README.md) · F6-04 · `quotetext`, `telemetria`, `integrations`, `crmpush` |
| Entorno | 💻 local (era 🌐 web: desde el 2026-10-04 todo es local, D-R-8) |
| Nivel (E-12) | Provisional: simple (`telemetria`) · medio (`quotetext`, `crmpush`, `integrations/{store,gate,crud,outbox_stats}.go`) · complejo (`integrations/{worker,postgres}.go`). Lo fija el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T6.10–T6.13, T6.19–T6.21 |
| Depende de | F6-03 |
| Decisiones | D-F6-3, D-F6-6, D-F6-7 (en [`../DECISIONES.md`](../DECISIONES.md), «F6 bloque C» y «F6 bloque B») |
| Se para cuando | `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/solicitudes | wc -l` → 0 · puente (import) `telemetria → internal/flujos/store` declarado · suite `integrationshelpertest` verde con el doble · candado R-12 y esquema `wapp-crm-v1` verdes · `ci-local` rc=0 con 0 SKIP · PR |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F6-03) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones de la tabla están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`, con `dev` al día.

## Prompt

```text
Sesión F6-04 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F6 · solicitudes → documentations/reorganizacion-modular/plan/F6-solicitudes/
- Bloque: F6-04 · `quotetext`, `telemetria`, `integrations`, `crmpush`
- Tareas: T6.10–T6.13, T6.19–T6.21 de plan/F6-solicitudes/tareas.md
- Te paras cuando: `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/solicitudes | wc -l` → 0 · puente (import) `telemetria → internal/flujos/store` declarado · suite `integrationshelpertest` verde con el doble · candado R-12 y esquema `wapp-crm-v1` verdes · `ci-local` rc=0 con 0 SKIP · PR.
- Decisiones: D-F6-3, D-F6-6, D-F6-7 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.
- Por paquete, rojo y verde seguidos: `telemetria` → `quotetext` → `crmpush` → `integrations` (con su doble `Memoria` y la promesa del worker de D-F6-7).

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.
Orquesta con sub-agentes (por paquete) y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama y `gh pr create --base dev` con «integrar SIN squash». Trabaja en TU rama partida de `dev` y con PR (`gh pr create --base dev`, «integrar SIN squash»), leyendo el rc sin pipe. Nunca directo a `dev` (regla 6 del `CLAUDE.md`).
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F6-solicitudes/tareas.md`](../F6-solicitudes/tareas.md): T6.10–T6.13, T6.19–T6.21 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [`README.md`](../F6-solicitudes/README.md) de la fase (o «ninguno», dicho).
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash» (rama y PR; nunca directo a `dev`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el README de la fase), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
