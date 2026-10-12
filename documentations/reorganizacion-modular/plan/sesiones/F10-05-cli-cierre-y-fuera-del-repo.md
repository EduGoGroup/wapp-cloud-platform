# F10-05 · F10 · D · cierre local · 💻 CLI

> ✎ **2026-10-11 (F10-05)**: esta ficha cubría los bloques D y E, decía «Entrada: T10.14 hecha» y «trabajas directo en
> `dev`». Las tres cosas dejaron de ser ciertas: D-F10-12 quita la dependencia de T10.14 para el bloque D, la regla 6
> del `CLAUDE.md` manda rama y PR, y el bloque E pasa a su propia ficha,
> [`F10-05b-cli-fuera-del-repo`](F10-05b-cli-fuera-del-repo.md). El fichero conserva su nombre para no romper enlaces.

| | |
|---|---|
| Fase · bloque | [F10 · relevo](../F10-relevo/README.md) · D · cierre local |
| Entorno | 💻 solo local; necesita **Docker** (T10.15) y **UAT por SSH** (T10.16) |
| Nivel (E-12) | No aplica: F10 no reconstruye un módulo y no lleva inventario E-12 |
| Duración objetivo | 45–90 min |
| Tareas | T10.15–T10.17 |
| Depende de | F10-04 (PR #82 integrado en `dev`). **No** de T10.14 ni de F10-04b (D-F10-12) |
| Decisiones | D-F10-2 (el relevo llega a UAT antes del 2026-10-18), D-F10-12 |
| Se para cuando | `GOWORK=off make ci-local` da `GATE_RC=0`; `CUENTA=3 make test-procesos` da `RC=0` con 0 SKIP y 0 FAIL contra `cmd/server`; UAT corre el commit del relevo (§6 y §9 del runbook: `path …/cmd/server` y `vcs.revision` = SHA de `dev`); y el acta dice `CERRADO <fecha>`. |

## Antes de pegar el prompt (Jhoan)

- [ ] El PR #82 (F10-04) está integrado: `gh pr view 82 --json state` dice `MERGED`.
- [ ] Las decisiones de la fila «Decisiones» están rellenas en [`../DECISIONES.md`](../DECISIONES.md) §6.
- [ ] Docker encendido en el Mac; acceso SSH a UAT.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F10-05 (bloque D) del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Verdad de campo primero: `gh pr view 82 --json state` debe decir MERGED y `origin/dev` debe estar en `21d527af` o posterior. Si no, PARA y dilo.

Tu encargo (y solo este):
- Fase: F10 · relevo → documentations/reorganizacion-modular/plan/F10-relevo/
- Bloque: D · cierre local
- Tareas: T10.15–T10.17 de plan/F10-relevo/tareas.md
- Entrada: PR #82 integrado en `dev`. T10.14 NO está hecha y NO se espera: D-F10-12 (plan/DECISIONES.md §6) quita esa dependencia para el bloque D.
- Te paras cuando: `GOWORK=off make ci-local` da GATE_RC=0; `CUENTA=3 make test-procesos` da RC=0 con 0 SKIP y 0 FAIL contra `cmd/server`; UAT corre el commit del relevo (§6 y §9 del runbook: path …/cmd/server y vcs.revision = SHA de `dev`); y el acta dice `CERRADO <fecha>`.
- Decisiones: D-F10-2 (el relevo llega a UAT antes del 2026-10-18) y D-F10-12. Si falta alguna en plan/DECISIONES.md, PARA y dilo.
- Skills: validar-antes-de-cerrar, procesos-testcontainers, pre-merge-docs, describir-pr.
- Necesitas Docker encendido y SSH a UAT. Si falta alguno, anótalo en el acta y PARA: no adaptes el proyecto.

Orden:
1. T10.15 sobre `dev` tal cual está: no produce código. Lee cada rc sin pipe y cuenta los SKIP. El «git push origin dev» que pide su «Hecho cuando» NO se hace: `dev` ya tiene el relevo por el PR #82.
2. T10.16: despliega en UAT desde `origin/dev`. Conserva `bin/server.viejo-*` hasta T10.22. Antes de reiniciar el servicio, enséñame el SHA que vas a desplegar y espera mi visto bueno.
3. Mira de paso lo que el acta §5.7 dejó sin ver (la conversación con el WhatsApp falso en UAT y una inferencia real con Ollama). Si no se puede ver, dilo en el acta: no lo des por bueno.
4. T10.17: el acta `CERRADO` y `ESTADO.md`.

Ningún código ni documento nace en `dev` (regla 6 del CLAUDE.md): todo lo que escribas va en una rama partida de `dev` (`reorg/f10-05-cierre`) y entra por PR, «integrar SIN squash».

Fuera de este encargo, no lo empieces: el bloque E (T10.18–T10.21), F10-04b (T10.14), T10.26 y T10.25. No toques `main`.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Mientras el PR esté abierto, la documentación dice «PR #N abierto, en revisión»; no lo fusiones tú.
```

## Al terminar debe existir

- En [`../F10-relevo/tareas.md`](../F10-relevo/tareas.md): T10.15–T10.17 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F10-relevo/README.md).
- El acta ([`TRASPASO-F10-relevo.md`](../../traspasos/TRASPASO-F10-relevo.md)) con su §7 y `CERRADO <fecha>`; un PR a `dev`, abierto.

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (sin Docker, sin SSH a UAT, red): se **anota** en el acta, no se adapta el proyecto a ella.
- Un gate en rojo: la sesión para y lo dice con su salida. No despliega en UAT.
- §6 o §9 del runbook en rojo tras el reinicio: marcha atrás al binario anterior (acta §4.5: `cp` a `bin/server.new` y `mv -f`, nunca `cp` directo) y se anota.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir.
