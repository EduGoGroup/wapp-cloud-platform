# F6-05 · F6 · cara HTTP, cableado y conmutar G1–G18 · 🌐❓ web si queda saldo

| | |
|---|---|
| Fase · bloque | [F6 · solicitudes](../F6-solicitudes/README.md) · F6-05 · cara HTTP, cableado y conmutar G1–G18 |
| Entorno | 🌐❓ web si queda saldo de la promoción; si no, local |
| Nivel (E-12) | Provisional: medio (los 12 ficheros de `apipublica`) · simple (`bridge_intakes.go`, si el inventario lo pide). Lo fija el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T6.22–T6.26 (= **TX.16–TX.18** de FX) |
| Depende de | F6-04 |
| Decisiones | D-F6-1, D-FX-4 (en [`../DECISIONES.md`](../DECISIONES.md), «F6 bloque H» y «F6 bloque G») |
| Se para cuando | los 12 ficheros de `apipublica` en verde · G1–G18 por la cara nueva y `FaseActual = 6` · huella igual · `go list -deps ./cmd/server-modular` contiene `modulos/solicitudes` y el de `./cmd/server` no · test de cableado completo verde · candados INV-1 sin `//go:build pendiente` y verdes · `ci-local` rc=0 con 0 SKIP · PR |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F6-04) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones de la tabla están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] Si queda saldo web: arrancar en claude.ai/code, repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md). Si no: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`, sobre `dev`.

## Prompt

```text
Sesión F6-05 del plan de reconstrucción modular de wapp-cloud-platform · 🌐❓ web si queda saldo.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F6 · solicitudes → documentations/reorganizacion-modular/plan/F6-solicitudes/
- Bloque: F6-05 · cara HTTP, cableado y conmutar G1–G18
- Tareas: T6.22–T6.26 (= TX.16–TX.18 de FX) de plan/F6-solicitudes/tareas.md
- Te paras cuando: los 12 ficheros de `apipublica` en verde · G1–G18 por la cara nueva y `FaseActual = 6` · huella igual · `go list -deps ./cmd/server-modular` contiene `modulos/solicitudes` y el de `./cmd/server` no · test de cableado completo verde · candados INV-1 sin `//go:build pendiente` y verdes · `ci-local` rc=0 con 0 SKIP · PR.
- Decisiones: D-F6-1, D-FX-4 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar, traspaso-web-local.
- Test de cableado obligatorio y completo (hallazgo 39): el arranque construye lo NUEVO y, por grep de ruta de import, ninguna fase importa `internal/intakes`, `internal/integrations` ni `internal/tenantvars` viejos fuera del sitio declarado para el carrito. G2 · G9 · G10 en el mismo commit. `solicitudes` NO entra en `Conmutados` (entra cuando muere su último adaptador, F8).

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.
Orquesta con sub-agentes (por paquete) y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama y `gh pr create --base dev` con «integrar SIN squash»; traspaso solo si algo lo cierra la local. Si corres esto en local (sin saldo web): mismo encargo, sin traspaso, pero también en TU rama partida de `dev` y con PR (`gh pr create --base dev`, «integrar SIN squash»), leyendo el rc sin pipe. Nunca directo a `dev` (regla 6 del `CLAUDE.md`).
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F6-solicitudes/tareas.md`](../F6-solicitudes/tareas.md): T6.22–T6.26 (= **TX.16–TX.18** de FX) `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [`README.md`](../F6-solicitudes/README.md) de la fase (o «ninguno», dicho).
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash» (si corrió en local: igual, rama y PR; nunca directo a `dev`).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
