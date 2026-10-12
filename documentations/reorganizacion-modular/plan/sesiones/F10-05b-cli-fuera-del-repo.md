# F10-05b · F10 · E · fuera del repo · 💻 CLI

> ✎ **2026-10-12 (F10-05)**: ficha nueva. El bloque E vivía en
> [`F10-05-cli-cierre-y-fuera-del-repo`](F10-05-cli-cierre-y-fuera-del-repo.md), que cubría D y E; al partirse, lo de E
> se trae aquí tal cual estaba, con la regla 6 (rama y PR en cada repo) ya dicha.

| | |
|---|---|
| Fase · bloque | [F10 · relevo](../F10-relevo/README.md) · E · fuera del repo |
| Entorno | 💻 solo local; necesita la raíz de wApp con los repos hermanos |
| Nivel (E-12) | No aplica: F10 no reconstruye un módulo y no lleva inventario E-12 |
| Duración objetivo | 45–90 min |
| Tareas | T10.18–T10.21 |
| Depende de | F10-05 (T10.17) |
| Decisiones | D-F10-7 (el texto del ADR-0010 lo aprueba Jhoan **dentro** de la sesión), D-F10-8 |
| Se para cuando | Los recuentos de `diseno.md` §3.1 y §3.3 dan 0 fuera de lo histórico; `make check-docs` de la raíz, verde; las 16 menciones de la bóveda `analisis/` y del `CLAUDE.md` de la raíz, re-apuntadas. |

**No entran aquí**: T10.14 (F10-04b), T10.26 y T10.25, que tienen PR propio y van después (D-F10-12, D-F10-14).

## Antes de pegar el prompt (Jhoan)

- [ ] El PR de F10-05 (bloque D) está integrado en `dev`.
- [ ] Las decisiones de la fila «Decisiones» están rellenas en [`../DECISIONES.md`](../DECISIONES.md) §6.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F10-05b (bloque E) del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F10 · relevo → documentations/reorganizacion-modular/plan/F10-relevo/
- Bloque: E · fuera del repo
- Tareas: T10.18–T10.21 de plan/F10-relevo/tareas.md
- Entrada: T10.17 hecha y su PR integrado en `dev`
- Te paras cuando: los recuentos de `diseno.md` §3.1 y §3.3 dan 0 fuera de lo histórico; `make check-docs` de la raíz está verde; y las 16 menciones de la bóveda `analisis/` y del `CLAUDE.md` de la raíz están re-apuntadas.
- Decisiones: D-F10-7 y D-F10-8 deben estar rellenas en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: validar-antes-de-cerrar, pre-merge-docs, describir-pr.

Cada tarea va en SU repo, con SU gate. No escribas en `docs/` de la raíz. Las citas históricas no se reescriben: se anota la ruta nueva al lado (y «lo histórico» es solo el eje `historia/` y las fichas de planes cerrados: D-F10-8).
T10.19: presenta a Jhoan el texto nuevo del ADR-0010 y PARA hasta que lo apruebe.
Nivel de ceremonia: no hay inventario E-12 (F10 no reconstruye un módulo).
Ningún código ni documento nace en `dev` (regla 6 del CLAUDE.md): en cada repo tocado, una rama partida de su `dev` y un PR, «integrar SIN squash». No toques `main`.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Mientras un PR esté abierto, la documentación dice «PR #N abierto, en revisión»; no lo fusiones tú.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F10-relevo/tareas.md`](../F10-relevo/tareas.md): T10.18–T10.21 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F10-relevo/README.md).
- Un PR por repo hermano tocado, y los de la raíz de wApp.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
