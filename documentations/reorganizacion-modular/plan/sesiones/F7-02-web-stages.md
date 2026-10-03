# F7-02 · F7 · `stages` · 🌐❓ web si queda saldo

| | |
|---|---|
| Fase · bloque | [F7 · captacion](../F7-captacion/README.md) · B · `stages` |
| Entorno | 🌐❓ web si queda saldo de la promoción; si no, local |
| Nivel (E-12) | medio (`plazo.go`, `tope.go` candidatos a simple) — provisional; manda el inventario aprobado |
| Duración objetivo | 45–90 min |
| Tareas | T7.7–T7.9, T7.16–T7.17 |
| Depende de | F7-01 |
| Decisiones | ninguna nueva |
| Se para cuando | los 10 ficheros de `stages` en verde · puente (import) `captacion/stages → internal/flujos/store` declarado en `fronteras_test.go` · `make ci-local` rc=0 con 0 SKIP |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F7-01) está integrada en `dev` (PR fusionado **sin squash**, o empujada si corrió en local).
- [ ] Las decisiones de la cabecera están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md). Sin saldo web: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F7-02 del plan de reconstrucción modular de wapp-cloud-platform · 🌐❓ web si queda saldo.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este):
- Fase: F7 · captacion → documentations/reorganizacion-modular/plan/F7-captacion/
- Bloque: B · `stages`
- Tareas: T7.7–T7.9, T7.16–T7.17 de plan/F7-captacion/tareas.md
- Te paras cuando: los 10 ficheros de `stages` en verde · puente (import) `captacion/stages → internal/flujos/store` declarado en `fronteras_test.go` · `make ci-local` rc=0 con 0 SKIP
- Decisiones: ninguna nueva. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

Lee antes los tests viejos marcados 🔶 en diseno.md §2.4 (E-8). `match_lineas.go` no tiene exportados: su test nace en el verde y solo por sus reglas (E-4).
Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Orquesta con sub-agentes por paquete y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama y `gh pr create --base dev` con «integrar SIN squash»; traspaso solo si algo lo cierra la local. Si corres esto en local (sin saldo web): mismo encargo, sin PR ni traspaso, `git push origin dev` leyendo el rc sin pipe.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F7-captacion/tareas.md`](../F7-captacion/tareas.md): T7.7–T7.9, T7.16–T7.17 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F7-captacion/README.md).
- Un PR con `--base dev`, con el informe de gates y «integrar SIN squash» (en local: `dev` empujado).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
