# F6-06 · F6 · cierre local · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F6 · solicitudes](../F6-solicitudes/README.md) · F6-06 · cierre local |
| Entorno | 💻 solo local |
| Nivel (E-12) | Provisional: — (no escribe ficheros de producción). Lo fija el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T6.27–T6.29 |
| Depende de | F6-05 |
| Decisiones | D-F9-1, D-F6-7 y la pregunta abierta D-F9-10 (en [`../DECISIONES.md`](../DECISIONES.md)) |
| Se para cuando | suites `intakeshelpertest`, `integrationshelpertest` y `tenantvarshelpertest` verdes en memoria **y** contra Postgres (testcontainers) · P5 y P6 verdes con `WAPP_PROCESOS_BINARIO=viejo` y `=nuevo`, 0 SKIP · `make test-procesos` rc=0 leído del log · `dev` empujado |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F6-05) terminó y está integrada en `dev` (o dejó PR y, si hubo dos entornos, traspaso).
- [ ] Las decisiones de la tabla están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F6-06 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F6 · solicitudes → documentations/reorganizacion-modular/plan/F6-solicitudes/
- Bloque: F6-06 · cierre local
- Tareas: T6.27–T6.29 de plan/F6-solicitudes/tareas.md
- Te paras cuando: suites `intakeshelpertest`, `integrationshelpertest` y `tenantvarshelpertest` verdes en memoria y contra Postgres (testcontainers) · P5 y P6 verdes con `WAPP_PROCESOS_BINARIO=viejo` y `=nuevo`, 0 SKIP · `make test-procesos` rc=0 leído del log · `dev` empujado.
- Decisiones: D-F9-1, D-F6-7 y la pregunta abierta D-F9-10 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, procesos-testcontainers, traspaso-web-local.
- Incluye los procesos de F9 del módulo (T9.27 de `plan/F9-procesos/tareas.md`) contra el binario NUEVO, si D-F9-1 = sí. Nunca contra un Postgres vivo.

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. `git push origin dev` (rc sin pipe). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F6-solicitudes/tareas.md`](../F6-solicitudes/tareas.md): T6.27–T6.29 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [`README.md`](../F6-solicitudes/README.md) de la fase (o «ninguno», dicho).
- `dev` integrado **sin squash** y empujado; si hubo traspaso, su sección `CERRADO <fecha>`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
