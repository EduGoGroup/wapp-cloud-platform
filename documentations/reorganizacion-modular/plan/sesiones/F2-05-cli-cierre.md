# F2-05 · F2 · cierre local · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F2 · acceso](../F2-acceso/README.md) · sesión F2-05 (💻) |
| Entorno | 💻 solo local |
| Nivel (E-12) | no aplica (cierre: gates, suites contra Postgres, mutantes del nivel complejo, procesos) |
| Duración objetivo | 45–90 min |
| Tareas | T2.32–T2.33 (= T9.23) |
| Depende de | F2-04 |
| Decisiones | D-F9-1 de [`../DECISIONES.md`](../DECISIONES.md) (sí, 2026-09-30) |
| Se para cuando | las suites de los puertos con BD pasan contra Postgres sin divergencias con memoria · `make test-procesos` rc=0 con 0 SKIP contra `cmd/server` y `cmd/server-modular` · `make ci-local` rc=0 con 0 SKIP · PR a `dev` abierto desde la rama de la sesión. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión F2-04 terminó y dejó PR (y traspaso, si lo hubo).
- [ ] Docker encendido en el Mac; `go1.26.5` y `golangci-lint v2.12.2` disponibles.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F2-05 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F2 · acceso → documentations/reorganizacion-modular/plan/F2-acceso/
- Sesión: F2-05 · cierre local
- Tareas: T2.32–T2.33 de plan/F2-acceso/tareas.md (T2.33 = T9.23 de plan/F9-procesos/tareas.md: se marcan las dos)
- Integra en `dev` SIN squash el PR de F2-04 si sigue abierto.
- Corre contra Postgres (testcontainers, nunca un Postgres vivo) las suites de los puertos con BD de `acceso` y los mutantes del nivel complejo; luego los procesos de F9 del módulo contra el binario viejo y el NUEVO.
- Te paras cuando: suites sin divergencias memoria ↔ Postgres, `make test-procesos` rc=0 con 0 SKIP en los dos binarios, `make ci-local` rc=0 con 0 SKIP.
- Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.
- Skills: validar-antes-de-cerrar, procesos-testcontainers.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase (y README de F2 → «cerrada» con SHA; si hubo traspaso, su sección «CERRADO <fecha>»).
Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- El cierre empujado a **la rama del PR** (la de la web) y el PR listo para que Jhoan lo integre **sin squash**; nada commiteado ni fusionado directamente en `dev` (regla 6 del `CLAUDE.md`).
- En [`../F2-acceso/tareas.md`](../F2-acceso/tareas.md): T2.32–T2.33 `[x]` con SHA; T9.23 `[x]` en [`../F9-procesos/tareas.md`](../F9-procesos/tareas.md).
- Un bloque de la sesión en `ESTADO.md`.
- Los hallazgos nuevos en el [README de F2](../F2-acceso/README.md), con el informe de fase (minutos por sesión, mutantes, lo que subió de nivel).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una divergencia memoria ↔ Postgres o un mutante vivo: es un hallazgo; se anota en el README de F2 y se arregla con su commit (`verde`/`refactor`), no se silencia.
- Si el bloque no cabe en ~90 min: para en un punto limpio (tras las suites, antes de los procesos), cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
