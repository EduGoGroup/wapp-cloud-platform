# F45-03 · F4+F5 · cierre local de `inferencia` y `catalogo` · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F4 · inferencia](../F4-inferencia/README.md) y [F5 · catalogo](../F5-catalogo/README.md) · cierre local de las dos (💻) |
| Entorno | 💻 solo local |
| Nivel (E-12) | No aplica (cierre): suites contra Postgres de los dos puertos con BD de F4; F5 no tiene BD |
| Duración objetivo | 45–90 min |
| Tareas | T4.29–T4.31 de F4 · T5.20–T5.21 de F5 · T9.25 y T9.26 de F9 |
| Depende de | F45-02 |
| Decisiones | D-F9-1 en [`../DECISIONES.md`](../DECISIONES.md) |
| Se para cuando | `make test-procesos` rc=0 contra el binario nuevo con 0 SKIP (suites `tenantllmhelpertest.Contrato` y `degradationhelpertest.Contrato` contra Postgres incluidas); la definición de hecho de `reglas.md` §4 de F4 y de F5 se cumple entera. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión web que cierra (F45-02) terminó y dejó PR y, si aplica, traspaso.
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F45-03 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este): el cierre local de F4 y de F5.
- F4 · inferencia → documentations/reorganizacion-modular/plan/F4-inferencia/ · T4.29–T4.31
  - T4.31 (= T9.25 de plan/F9-procesos/tareas.md): las suites tenantllmhelpertest.Contrato y
    degradationhelpertest.Contrato con su Montaje contra Postgres, con el arnés (testcontainers), nunca un Postgres vivo.
- F5 · catalogo → documentations/reorganizacion-modular/plan/F5-catalogo/ · T5.20–T5.21
  - T9.26: F5 no tiene SQL propio; es la suite entera de procesos contra el binario NUEVO.
- Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del
  contrato; mutantes en lo complejo.
- Te paras cuando: `make test-procesos` rc=0 contra el binario nuevo, contando PASS/FAIL/SKIP con -v (0 SKIP), y la
  definición de hecho de reglas.md §4 de cada fase se cumple entera.
- `Conmutados`: `inferencia` sigue fuera (su adaptador vive hasta F8); `catalogo`, según plan/F5-catalogo/reglas.md §4.10.
- Decisiones: D-F9-1 debe estar rellena en plan/DECISIONES.md. Si falta, PARA y dilo.
- Skills: validar-antes-de-cerrar, procesos-testcontainers, traspaso-web-local.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase.
Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- El cierre empujado a **la rama del PR** (la de la web) y el PR listo para que Jhoan lo integre **sin squash**; nada commiteado ni fusionado directamente en `dev` (regla 6 del `CLAUDE.md`).
- En [`../F4-inferencia/tareas.md`](../F4-inferencia/tareas.md): T4.29–T4.31 `[x]` con SHA; en [`../F5-catalogo/tareas.md`](../F5-catalogo/tareas.md): T5.20–T5.21; en [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md): T9.25 y T9.26.
- Un bloque de la sesión en `ESTADO.md`: F4 y F5 «cerrada», con la cifra de coste (ficheros, commits, horas de sesión).
- Los hallazgos nuevos en el README de F4 y en el de F5.
- Si hubo traspaso de la web: su sección `CERRADO <fecha>`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una suite se comporta distinto en memoria y en Postgres: es un hallazgo: se anota en el README de F4 y la sesión **para y pregunta**.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota**, no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min, para en un punto limpio (F4 cerrada, antes de F5), cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
