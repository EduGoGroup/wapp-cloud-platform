# F3-04 · F3 · adaptador, cara nueva y conmutación · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F3 · edge](../F3-edge/README.md) · bloque F3-04 de [`tareas.md`](../F3-edge/tareas.md) |
| Entorno | 💻 local (era 🌐 web: desde el 2026-10-04 todo es local, D-R-8) |
| Nivel (E-12) | simple el adaptador `bridge_gateway.go` (una pasada + test de cableado completo); la conmutación sigue T3.28 (provisional; manda el inventario aprobado) |
| Duración objetivo | 45–90 min |
| Tareas | T3.24–T3.28 |
| Depende de | F3-03 |
| Decisiones | D-F3-1…D-F3-6 y D-FX-1/2/3 de [`../DECISIONES.md`](../DECISIONES.md) |
| Se para cuando | `go test -run 'Mudanzas|Huella|Cableado|Identidad' ./internal/arranque` rc=0 con 0 SKIP · `ls internal/arranque/bridge_iam.go` → no existe · `acceso` en `Conmutados` (y `edge` no) · `grep -rn 'gatewaygrpc.New(' internal/arranque` vacío · `make ci-local` rc=0 · PR. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F3-03) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones D-F3-* y D-FX-1/2/3 de [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`, con `dev` al día.

## Prompt

```text
Sesión F3-04 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F3 · edge → documentations/reorganizacion-modular/plan/F3-edge/
- Bloque: F3-04 · adaptador, cara nueva y conmutación
- Tareas: T3.24–T3.28 de plan/F3-edge/tareas.md
- Qué: bridge_gateway.go con su test de cableado, rutas a apipublica (FX TX.8–TX.11), conmutar(edge) y borrar bridge_iam.go. La parte 💻 de T3.27 (e2e) la cierra F3-05.
- Te paras cuando: `go test -run 'Mudanzas|Huella|Cableado|Identidad' ./internal/arranque` rc=0 con 0 SKIP · `ls internal/arranque/bridge_iam.go` → no existe · `acceso` en `Conmutados` (y `edge` no) · `grep -rn 'gatewaygrpc.New(' internal/arranque` vacío · `make ci-local` rc=0 · PR.
- Decisiones: D-F3-* y D-FX-1/2/3 de plan/DECISIONES.md deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.
Trabaja por paquete (rojo y verde del paquete seguidos). Orquesta con sub-agentes y protege tu contexto.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase.
Push de TU rama y `gh pr create --base dev` con "integrar SIN squash".
Trabaja en TU rama partida de `dev` y con PR (`gh pr create --base dev`, «integrar SIN squash»), leyendo el rc sin pipe. Nunca directo a `dev` (regla 6 del `CLAUDE.md`).
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F3-edge/tareas.md`](../F3-edge/tareas.md): T3.24–T3.28 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de F3](../F3-edge/README.md).
- Un PR con `--base dev`, con el informe de gates (y la tabla de `make cobertura-ficheros` como informe) y «integrar SIN squash».

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el README de la fase), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio (tras T3.26 o tras T3.27), cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
