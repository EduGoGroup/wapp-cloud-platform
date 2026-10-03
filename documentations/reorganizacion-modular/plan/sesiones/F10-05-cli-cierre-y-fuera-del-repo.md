# F10-05 · F10 · D + E · cierre local y fuera del repo · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F10 · relevo](../F10-relevo/README.md) · D · cierre local y E · fuera del repo |
| Entorno | 💻 solo local; necesita **Docker** (T10.15), **UAT por SSH** (T10.16) y la raíz de wApp con los repos hermanos (T10.18–T10.21) |
| Nivel (E-12) | No aplica: F10 no reconstruye un módulo y no lleva inventario E-12 |
| Duración objetivo | 45–90 min (si no cabe, el corte limpio es tras T10.17) |
| Tareas | T10.15–T10.21 |
| Depende de | F10-04 |
| Decisiones | D-F10-2, D-F10-7 (el texto del ADR-0010 lo aprueba Jhoan **dentro** de la sesión) |
| Se para cuando | `CUENTA=3 make test-procesos` da `RC=0` con 0 SKIP contra `cmd/server`; UAT corre el commit del relevo (§6 y §9 del runbook); el acta dice `CERRADO`; y los recuentos de `diseno.md` §3.1 y §3.3 dan 0 fuera de lo histórico. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F10-04) cerró con sus tres cosas y está empujada a `dev`.
- [ ] Las decisiones de la fila «Decisiones» están rellenas en [`../DECISIONES.md`](../DECISIONES.md) §6.
- [ ] Docker encendido en el Mac; acceso SSH a UAT.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F10-05 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md
En F10 todo es local: no hay sesión web que cerrar, ni rama que integrar, ni traspaso que leer. Trabajas directo en `dev`.

Tu encargo (y solo este):
- Fase: F10 · relevo → documentations/reorganizacion-modular/plan/F10-relevo/
- Bloque(s): D · cierre local y E · fuera del repo
- Tareas: T10.15–T10.21 de plan/F10-relevo/tareas.md
- Entrada: T10.14 hecha y empujada
- Te paras cuando: `CUENTA=3 make test-procesos` da `RC=0` con 0 SKIP contra `cmd/server`; UAT corre el commit del relevo (§6 y §9 del runbook); el acta dice `CERRADO`; y los recuentos de `diseno.md` §3.1 y §3.3 dan 0 fuera de lo histórico.
- Decisiones: D-F10-2 y D-F10-7 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, procesos-testcontainers.

Primero el bloque D (T10.15–T10.17), luego el E (T10.18–T10.21). En el E, cada tarea va en SU repo, con SU gate y commit a SU `dev`. No escribas en `docs/` de la raíz. Las citas históricas no se reescriben: se anota la ruta nueva al lado.
T10.19: presenta a Jhoan el texto nuevo del ADR-0010 y PARA hasta que lo apruebe.
Nivel de ceremonia: no hay inventario E-12 (F10 no reconstruye un módulo). Sin umbral de cobertura: `make cobertura-ficheros` es un informe.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`, o el PR ya abierto de esa rama; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`) en cada repo tocado. No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F10-relevo/tareas.md`](../F10-relevo/tareas.md): T10.15–T10.21 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F10-relevo/README.md).
- El acta con `CERRADO <fecha>`; un commit a `dev` por repo hermano tocado, y los de la raíz de wApp.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (sin Docker, sin SSH a UAT, red): se **anota** en el acta (`traspasos/TRASPASO-F10-relevo.md`), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo en ese caso se escribe un traspaso.
