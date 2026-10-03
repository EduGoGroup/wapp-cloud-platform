# F45-02 · F4+F5 · conmutar `inferencia` y `catalogo` entero · 🌐 web

| | |
|---|---|
| Fase · bloque | [F4 · inferencia](../F4-inferencia/README.md) · adaptador, conmutar y 4 rutas — [F5 · catalogo](../F5-catalogo/README.md) · entero y conmutación nominal (🌐) |
| Entorno | 🌐 web |
| Nivel (E-12) | Provisional: `bridge_inferencia.go` simple; F5 simple/medio salvo `indice/cache.go` (complejo). Manda el inventario aprobado en F45-01 |
| Duración objetivo | 45–90 min |
| Tareas | T4.10, T4.24–T4.28 (+ TX.12–TX.14) de F4 · T5.2–T5.19 (+ TX.15) de F5 |
| Depende de | F45-01 |
| Decisiones | D-F4-3, D-F4-4, D-F5-1, D-F5-2 y D-F5-3 en [`../DECISIONES.md`](../DECISIONES.md) |
| Se para cuando | huella del arranque nuevo idéntica a la del viejo con `FaseActual = 5`; test de cableado de `bridge_inferencia.go` verde; `grep -rln 'go:build pendiente' internal/modulos/catalogo internal/modulos/conversacion/model` vacío; `make ci-local` rc=0 con 0 SKIP. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F45-01) está integrada en `dev` (PR fusionado **sin squash**) y aprobaste los dos inventarios E-12.
- [ ] D-F4-3, D-F4-4, D-F5-1, D-F5-2 y D-F5-3 están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] Arrancar en claude.ai/code: repo `EduGoGroup/wapp-cloud-platform`, **rama base `dev`**, el entorno de [`00-01-jhoan-preparar-entorno-web.md`](00-01-jhoan-preparar-entorno-web.md).

## Prompt

```text
Sesión F45-02 del plan de reconstrucción modular de wapp-cloud-platform · 🌐 web.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md

Tu encargo (y solo este), en este orden:
1. F4 · inferencia → documentations/reorganizacion-modular/plan/F4-inferencia/
   - T4.10: internal/arranque/bridge_inferencia.go (tipos turneroBridge y llmConfigBridge), nivel simple, una pasada.
   - T4.24–T4.28: conmutar el arranque nuevo, borrar bridge_gateway.go de F3, el test de cableado COMPLETO del
     adaptador (el arranque construye lo nuevo Y ninguna fase importa lo viejo fuera del adaptador, grep por import).
   - TX.12–TX.14 de plan/FX-cara-http/tareas.md: mudar las 4 rutas a apipublica. `FaseActual = 4`.
2. F5 · catalogo → documentations/reorganizacion-modular/plan/F5-catalogo/
   - T5.2–T5.18: conversacion/model (si D-F5-1 = B), catalog.go, indice, catalogimport.
   - T5.19 + TX.15: conmutación nominal, `FaseActual = 5`. F5 no tiene adaptadores ni BD: no toca internal/arranque
     salvo `FaseActual`.
- Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del
  contrato; mutantes en lo complejo (indice/cache.go).
- `inferencia` NO entra en `Conmutados`: su adaptador vive hasta F8 (plan/F4-inferencia/reglas.md §4.11).
- Te paras cuando: huella idéntica con `FaseActual = 5`, test de cableado verde, catalogo sin etiqueta `pendiente`,
  `make ci-local` rc=0 con 0 SKIP.
- Decisiones: D-F4-3, D-F4-4, D-F5-1, D-F5-2 y D-F5-3 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

Orquesta con sub-agentes (por paquete) y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase.
Push de TU rama y `gh pr create --base dev` con "integrar SIN squash"; traspaso solo si algo lo cierra la local.
Si corres esto en local (sin saldo web): mismo encargo, sin PR ni traspaso, `git push origin dev` leyendo el rc sin pipe.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F4-inferencia/tareas.md`](../F4-inferencia/tareas.md): T4.10 y T4.24–T4.28 `[x]` con SHA; en [`../FX-cara-http/tareas.md`](../FX-cara-http/tareas.md): TX.12–TX.15; en [`../F5-catalogo/tareas.md`](../F5-catalogo/tareas.md): T5.2–T5.19 (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` (con el P99 medido de T5.14).
- Los hallazgos nuevos en el README de F4 y en el de F5.
- Un PR con `--base dev`, con el informe de gates, la tabla de `make cobertura-ficheros` (informe) y «integrar SIN squash».

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- El test de rendimiento (T5.14) es inestable en la VM web: parada y decisión (D-F5-3); **nunca** `t.Skip`.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el traspaso o en `06-entorno-web.md` §5), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min, para en un punto limpio (el natural: F4 conmutado con `FaseActual = 4`, antes de empezar F5), cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
