# F3-01 · F3 · inventario E-12 + hojas · 🌐 web

| | |
|---|---|
| Fase · bloque | [F3 · edge](../F3-edge/README.md) · bloque F3-01 de [`tareas.md`](../F3-edge/tareas.md) |
| Entorno | 🌐 web |
| Nivel (E-12) | mixto: complejo `session`, `inferstats`, `lease` 🔒 · medio `receipts`, `ingest`, `diagnostics`, `enroll` (sus adaptadores Postgres, complejo) (provisional; manda el inventario aprobado) |
| Duración objetivo | 45–90 min |
| Tareas | T3.1–T3.9, T3.15–T3.18 |
| Depende de | F2-05 |
| Decisiones | D-F3-1…D-F3-6 y D-FX-1/2/3 de [`../DECISIONES.md`](../DECISIONES.md) |
| Se para cuando | inventario E-12 aprobado por Jhoan · `grep -rn 'pendiente.Implementar' internal/modulos/edge/{session,inferstats,receipts,ingest,diagnostics,lease,enroll} | wc -l` → 0 · sus 6 suites verdes contra los dobles · `make ci-local` rc=0 con 0 SKIP · PR. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F2-05) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones D-F3-* y D-FX-1/2/3 de [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] El inventario E-12 lo apruebas tú **dentro** de la sesión: estate disponible al principio.
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F3-01 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F3 · edge → documentations/reorganizacion-modular/plan/F3-edge/
- Bloque: F3-01 · inventario E-12 + hojas
- Tareas: T3.1–T3.9, T3.15–T3.18 de plan/F3-edge/tareas.md
- Paquetes: session, inferstats, receipts, ingest, diagnostics, lease (🔒 doble llave: sin cambiar comportamiento, SQL literal), enroll.
- Te paras cuando: inventario E-12 aprobado por Jhoan · `grep -rn 'pendiente.Implementar' internal/modulos/edge/{session,inferstats,receipts,ingest,diagnostics,lease,enroll} | wc -l` → 0 · sus 6 suites verdes contra los dobles · `make ci-local` rc=0 con 0 SKIP · PR.
- Decisiones: D-F3-* y D-FX-1/2/3 de plan/DECISIONES.md deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

Empieza por el inventario E-12 (tabla de niveles + adaptadores `bridge_<x>.go`), preséntaselo a Jhoan y **PARA hasta que lo apruebe**; luego sigue.

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.
Trabaja por paquete (rojo y verde del paquete seguidos). Orquesta con sub-agentes y protege tu contexto.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase.
Push de TU rama y `gh pr create --base dev` con "integrar SIN squash"; traspaso solo si algo lo cierra la local.
Si corres esto en local (sin saldo web): mismo encargo, sin traspaso, pero también en TU rama partida de `dev` y con PR (`gh pr create --base dev`, «integrar SIN squash»), leyendo el rc sin pipe. Nunca directo a `dev` (regla 6 del `CLAUDE.md`).
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F3-edge/tareas.md`](../F3-edge/tareas.md): T3.1–T3.9, T3.15–T3.18 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de F3](../F3-edge/README.md).
- Un PR con `--base dev`, con el informe de gates (y la tabla de `make cobertura-ficheros` como informe) y «integrar SIN squash».

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio (tras T3.1, tras T3.9 o tras T3.17), cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
