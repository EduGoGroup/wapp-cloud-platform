# F2-03 · F2 · `infra/postgres`, `transport/http` y `platformadmin` · 🌐 web

| | |
|---|---|
| Fase · bloque | [F2 · acceso](../F2-acceso/README.md) · sesión F2-03 (🌐) |
| Entorno | 🌐 web |
| Nivel (E-12) | complejo (`infra/postgres`, `entitlements/postgres.go`, los dos Postgres de `platformadmin`) · medio (`transport/http`, handlers) — provisional; manda el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T2.4, T2.12–T2.15, T2.24–T2.27 |
| Depende de | F2-02 |
| Decisiones | D-F2-1…D-F2-8 de [`../DECISIONES.md`](../DECISIONES.md) y su §3 (P1–P7) |
| Se para cuando | `grep -rn pendiente.Implementar internal/modulos/acceso` → 0 · los 3 candados AST verdes · `go vet -tags integracion ./...` rc=0 (las suites compilan con su `Montaje` de Postgres) · `ci-local` rc=0 con 0 SKIP · PR abierto. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F2-02) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] Las decisiones D-F2-* de [`../DECISIONES.md`](../DECISIONES.md) están rellenas.
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F2-03 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F2 · acceso → documentations/reorganizacion-modular/plan/F2-acceso/
- Sesión: F2-03 · `infra/postgres`, `transport/http` y `platformadmin`
- Tareas: T2.4, T2.12–T2.15, T2.24–T2.27 de plan/F2-acceso/tareas.md
- Todo puerto con BD lleva su suite `Contrato(t, func(t) Montaje)` con dos montajes: en memoria y en Postgres (arnés de F9-A). Si hay Docker, córrelas como pre-chequeo (no cierra: cierra F2-05).
- Te paras cuando: `grep -rn pendiente.Implementar internal/modulos/acceso` → 0 · los 3 candados AST verdes · `go vet -tags integracion ./...` rc=0 (las suites compilan con su `Montaje` de Postgres) · `ci-local` rc=0 con 0 SKIP · PR abierto.
- Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.
- Decisiones: D-F2-* de plan/DECISIONES.md deben estar rellenas. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, procesos-testcontainers, validar-antes-de-cerrar.

Orquesta con sub-agentes (por paquete) y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase.
Push de TU rama y `gh pr create --base dev` con «integrar SIN squash»; traspaso solo si algo lo cierra la local.
Si corres esto en local (sin saldo web): mismo encargo, sin PR ni traspaso, `git push origin dev` leyendo el rc sin pipe.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- Commits con los prefijos de la plantilla (`rojo(<m>)`, `verde(<m>)`, `conmutar(<m>)`, `refactor(<m>)`, `docs(reorganizacion-modular)`), empujados a la rama de la sesión.
- En [`../F2-acceso/tareas.md`](../F2-acceso/tareas.md): T2.4, T2.12–T2.15, T2.24–T2.27 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md`.
- Los hallazgos nuevos en el [README de F2](../F2-acceso/README.md).
- Un PR con `--base dev`, con el informe de gates (y la tabla de `make cobertura-ficheros` como informe) y «integrar SIN squash».

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si un archivo sale peor que su nivel del inventario: **sube de nivel**, se anota en `tareas.md` y se sigue con la ceremonia nueva.
- Si el bloque no cabe en ~90 min: para en un punto limpio (tras `iam/infra/postgres`, tras `transport/http` o tras `platformadmin`), cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
