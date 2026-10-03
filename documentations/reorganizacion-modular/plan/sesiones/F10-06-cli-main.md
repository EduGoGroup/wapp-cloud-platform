# F10-06 · F10 · F · `main` · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F10 · relevo](../F10-relevo/README.md) · F · `main` |
| Entorno | 💻 solo local; necesita **`main`**, **Docker** (`make test-procesos`) y UAT por SSH (borrar `bin/server.viejo-*`) |
| Nivel (E-12) | No aplica: F10 no reconstruye un módulo y no lleva inventario E-12 |
| Duración objetivo | 45–90 min |
| Tareas | T10.22 — **solo a petición expresa de Jhoan** |
| Depende de | F10-05 |
| Decisiones | D-F10-6 (tag `v0.3.0`) |
| Se para cuando | `main` = `dev` con `make ci-local` y `make test-procesos` verdes sobre ese SHA, `sync-main-to-dev.yml` alineó `dev` y, si hay tag, `CHANGELOG.md` está rellenado. |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F10-05) cerró con sus tres cosas y está empujada a `dev`.
- [ ] Las decisiones de la fila «Decisiones» están rellenas en [`../DECISIONES.md`](../DECISIONES.md) §6.
- [ ] Vas a pedir **expresamente** el paso a `main` en la conversación.
- [ ] Docker encendido en el Mac.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F10-06 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md
En F10 todo es local: no hay sesión web que cerrar, ni rama que integrar, ni traspaso que leer. Los gates se corren sobre `dev`.

Tu encargo (y solo este):
- Fase: F10 · relevo → documentations/reorganizacion-modular/plan/F10-relevo/
- Bloque(s): F · `main`
- Tareas: T10.22 de plan/F10-relevo/tareas.md (solo a petición expresa de Jhoan)
- Entrada: T10.17 hecha; petición expresa de Jhoan en esta conversación
- Te paras cuando: `main` = `dev` con `make ci-local` y `make test-procesos` verdes sobre ese SHA, `sync-main-to-dev.yml` alineó `dev` y, si hay tag, `CHANGELOG.md` está rellenado.
- Decisiones: D-F10-6 debe estar rellena en plan/DECISIONES.md. Si falta, PARA y dilo.
- Skills: validar-antes-de-cerrar, desplegar-ecosistema (del ecosistema).

🔴 SOLO si Jhoan lo ha pedido expresamente en esta conversación. Si no, no hagas nada y dilo.
Si el clasificador deniega el push a `main`, no lo rodees: Jhoan lo lanza él.
Nivel de ceremonia: no hay inventario E-12 (F10 no reconstruye un módulo). Sin umbral de cobertura: `make cobertura-ficheros` es un informe.
Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Esta es la única sesión que toca `main`; lee el rc de cada push sin pipe.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F10-relevo/tareas.md`](../F10-relevo/tareas.md): T10.22 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F10-relevo/README.md).
- `main` y `dev` en el mismo SHA; el tag, si Jhoan lo pidió.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (sin Docker, sin SSH a UAT, red): se **anota** en el acta (`traspasos/TRASPASO-F10-relevo.md`), no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo en ese caso se escribe un traspaso.
