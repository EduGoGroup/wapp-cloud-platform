# F45-01 · F4+F5 · inventario E-12 de las dos fases e `inferencia` en verde · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F4 · inferencia](../F4-inferencia/README.md) y [F5 · catalogo](../F5-catalogo/README.md) · inventario E-12 de las dos + F4 entero en verde (💻) |
| Entorno | 💻 local (era 🌐 web: desde el 2026-10-04 todo es local, D-R-8) |
| Nivel (E-12) | Provisional: F4 medio (`prompts`, dominios, `llmvia/local`) y complejo (los dos `postgres.go` con su suite, `llmvia`). Lo fija el inventario |
| Duración objetivo | 45–90 min |
| Tareas | T4.1–T4.9, T4.11–T4.23 de F4 · T5.1 de F5 |
| Depende de | F3-05 |
| Decisiones | D-F4-1, D-F4-2 y D-F5-1 en [`../DECISIONES.md`](../DECISIONES.md) |
| Se para cuando | Jhoan aprobó las dos tablas de niveles; `grep -rln 'go:build pendiente' internal/modulos/inferencia` vacío; el candado C2 nuevo y el viejo verdes; `make ci-local` rc=0 con 0 SKIP. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F3-05) está integrada en `dev` (PR fusionado **sin squash**).
- [ ] D-F4-1, D-F4-2 y D-F5-1 están rellenas en [`../DECISIONES.md`](../DECISIONES.md).
- [ ] El inventario E-12 (de F4 y de F5) lo apruebas tú **dentro** de la sesión: no cierres la pestaña hasta contestarle.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`, con `dev` al día.

## Prompt

```text
Sesión F45-01 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fases: F4 · inferencia → documentations/reorganizacion-modular/plan/F4-inferencia/
         F5 · catalogo → documentations/reorganizacion-modular/plan/F5-catalogo/ (solo su inventario)
- Tareas: T4.1–T4.9 y T4.11–T4.23 de plan/F4-inferencia/tareas.md; T5.1 de plan/F5-catalogo/tareas.md.
  T4.10 (el adaptador bridge_inferencia.go) NO es de esta sesión.
- Empieza por el inventario E-12 de F4 y de F5 juntos (T4.1 y T5.1): tabla de niveles por archivo + adaptadores
  `bridge_<x>.go` (F4: nace bridge_inferencia.go y se retira bridge_gateway.go; F5: ninguno, y sin BD).
  Preséntaselo a Jhoan y PARA hasta que lo apruebe; luego sigue con F4.
- Después: T4.2 (candados viejos ciegos al árbol nuevo), contratos y rojo, verde de las hojas (prompts, tenantllm,
  degradation y sus dobles), verde de llmvia/local y llmvia, y el candado C2 nuevo.
- Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del
  contrato; mutantes en lo complejo.
- Te paras cuando: internal/modulos/inferencia entero sin etiqueta `pendiente`, C2 nuevo y viejo verdes,
  `make ci-local` rc=0 con 0 SKIP.
- Decisiones: D-F4-1, D-F4-2 y D-F5-1 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

Orquesta con sub-agentes (por paquete) y protege tu contexto.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase.
Push de TU rama y `gh pr create --base dev` con "integrar SIN squash".
Trabaja en TU rama partida de `dev` y con PR (`gh pr create --base dev`, «integrar SIN squash»), leyendo el rc sin pipe. Nunca directo a `dev` (regla 6 del `CLAUDE.md`).
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F4-inferencia/tareas.md`](../F4-inferencia/tareas.md): T4.1–T4.9 y T4.11–T4.23 `[x]` con SHA (o `[~]` con lo que falta). En [`../F5-catalogo/tareas.md`](../F5-catalogo/tareas.md): T5.1 `[x]` con SHA y D-F5-1 anotada.
- Un bloque de la sesión en `ESTADO.md`, con las dos tablas de niveles aprobadas (o su enlace).
- Los hallazgos nuevos en el README de F4 (y en el de F5 si el inventario encontró alguno).
- Un PR con `--base dev`, con el informe de gates, la tabla de `make cobertura-ficheros` (informe) y «integrar SIN squash».

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- D-F4-1 no está aplicada (T4.2 falla): 🛑 no hay primer verde; se vuelve a Jhoan.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** (en el README de la fase), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min, para en un punto limpio (un paquete entero en verde), cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
