# F10-04 · F10 · C · el relevo en el repo · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F10 · relevo](../F10-relevo/README.md) · C · el relevo en el repo |
| Entorno | 💻 solo local; no necesita Docker ni UAT |
| Nivel (E-12) | No aplica: F10 no reconstruye un módulo y no lleva inventario E-12 |
| Duración objetivo | 45–90 min (ocho commits: si no cabe, corta entre dos tareas, que cada commit deja `dev` verde) |
| Tareas | T10.7–T10.14 |
| Depende de | F10-03 |
| Decisiones | D-F10-3, D-F10-4, D-F10-5, D-V-1 |
| Se para cuando | la definición de hecho del bloque C (`reglas.md` §4) se cumple sobre el último commit: `GATE_RC=0`, solo cinco directorios en `internal/`, cero adaptadores `bridge_*.go`, cero puentes (import), `Conmutados` completo, cero pendientes. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F10-03) cerró con sus tres cosas y está empujada a `dev`.
- [ ] Las decisiones de la fila «Decisiones» están rellenas en [`../DECISIONES.md`](../DECISIONES.md) §6.
- [ ] El acta de F10-03 dice «sigue».
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F10-04 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md
En F10 todo es local: no hay sesión web que cerrar, ni rama que integrar, ni traspaso que leer. Trabajas directo en `dev`.

Tu encargo (y solo este):
- Fase: F10 · relevo → documentations/reorganizacion-modular/plan/F10-relevo/
- Bloque(s): C · el relevo en el repo
- Tareas: T10.7–T10.14 de plan/F10-relevo/tareas.md
- Entrada: T10.6 con «sigue»
- Te paras cuando: la definición de hecho del bloque C (`reglas.md` §4) se cumple sobre el último commit: `GATE_RC=0`, solo cinco directorios en `internal/`, cero adaptadores `bridge_*.go`, cero puentes (import), `Conmutados` completo, cero pendientes.
- Decisiones: D-F10-3, D-F10-4, D-F10-5 y D-V-1 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar.

Un commit por tarea, directo en `dev`, en el orden de `arquitectura.md` §3. Sin squash.
🔴 Antes de T10.11 y T10.13: comprueba en el acta que T9.15 (P3) ejerce el reintento de `postgres.WithTx` y que el mutante `maxTxAttempts = 1` cae (hallazgo 38 de F1). Si no, NO borres `deadlock_integration_test.go` ni la integración vieja: PARA.
Nivel de ceremonia: no hay inventario E-12 (F10 no reconstruye un módulo). Sin umbral de cobertura: `make cobertura-ficheros` es un informe.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F10-relevo/tareas.md`](../F10-relevo/tareas.md): T10.7–T10.14 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F10-relevo/README.md).
- Ocho commits (`relevo: …` y `docs(reorganizacion-modular): …`), uno por tarea, en `dev`.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (sin Docker, sin SSH a UAT, red): se **anota** en el acta (`traspasos/TRASPASO-F10-relevo.md`), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo en ese caso se escribe un traspaso.
