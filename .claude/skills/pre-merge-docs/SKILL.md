---
name: pre-merge-docs
description: Use in wapp-cloud-platform whenever a session writes, in documentation, the STATE of a pull request, a task, a session or a phase — at the close of a session, when opening a PR, while a PR is under review, and above all right before merging. Fixes what the docs may say at each moment so they never claim «PR integrado», «fase cerrada» or «siguiente sesión» while the PR is still open. Triggers — "cierra la sesión", "abre el PR", "actualiza ESTADO", "mergea", "fusiona el PR", "voy a fusionar", "gh pr merge", "marca como hecha", "PR integrado".
---

# Documentación antes de fusionar: lo que se dice mientras el PR está abierto y lo que se dice al fusionarlo

> Decisión de Jhoan (2026-10-11, tras el cierre de F8): la documentación **no puede dar por fusionado un PR que está en
> revisión**. Sustituye a la regla del 2026-10-10 («el cierre de sesión ya escribe "PR #N integrado"»), que evitaba
> escribir en `dev` después de fusionar pero dejaba la documentación **mintiendo durante toda la revisión**. En el PR #72
> el cierre de F8 dijo «integrado» durante horas mientras se revisaba, y en esa revisión entraron siete commits más.

La idea es simple: **dos momentos, dos textos**. Lo que no cambia de la regla anterior: el cambio de estado viaja
**dentro** del PR, como su último commit, y nadie escribe en `dev` después de fusionar.

## Momento 1 · La sesión termina y abre el PR (o lo actualiza)

La documentación dice **lo que es cierto ahora**: el trabajo está en una rama y el PR está **abierto**.

| Dónde | Se escribe | No se escribe todavía |
|---|---|---|
| `tareas.md` de la fase | `[x]` con el SHA del commit que la hace, y «(PR #N, en revisión)» | — |
| `ESTADO.md`, cabecera y bloque de la sesión | «PR #N **abierto, en revisión**» | «integrado», «en `dev`» |
| `ESTADO.md`, tabla «Avance» | la fase «en revisión (PR #N)» | «✅ cerrada» |
| `plan/sesiones/README.md`, columna Estado | «**en revisión** (PR #N)» | «hecha», «integrado» |
| «Siguiente sesión» / «Siguiente paso» | sigue apuntando a **esta** sesión: «pendiente de fusionar el PR #N; después, <la siguiente>» | la sesión siguiente como si ya tocara |
| README de la fase, línea de estado | «en revisión (PR #N)» | «✅ CERRADA» |
| Deudas y decisiones (`deuda.md`, `DECISIONES.md`) | lo decidido, con su fecha: una decisión es cierta desde que se toma | «cerrada» en una deuda cuyo cierre depende de que el PR entre |

Un `[x]` con SHA **no miente**: dice que el commit existe. Lo que miente es decir dónde está ese commit.

## Momento 2 · Justo antes de fusionar

Ocurre **solo** cuando Jhoan pide fusionar («mergea», «fusiona») o avisa de que va a fusionar él en GitHub.

1. Comprueba que el PR sigue abierto y que la rama está empujada: `gh pr view <N> --json state,headRefOid`.
2. **Un commit, el último de la rama**, que pasa todo lo del momento 1 a su forma final: «PR #N **integrado**»,
   fase «✅ cerrada», sesión «hecha (fecha)», y «Siguiente sesión» apuntando ya a la siguiente. Se cita el **número del
   PR**, no el SHA del merge, que todavía no existe.
   Mensaje: `docs(reorganizacion-modular): PR #N integrado` (más lo que cierre: «F8 cerrada»).
3. Busca lo que se haya quedado atrás, antes de empujar:
   `grep -rn 'en revisión' documentations/ | grep '#<N>'` → vacío.
4. `git push origin <rama>` (rc sin pipe) y, si lo pidió Jhoan, `gh pr merge <N> --merge` (**nunca squash**).
5. Si la revisión añadió commits después del momento 1, el cuerpo del PR se actualiza antes (skill `describir-pr`).

## Lo que no se hace

- **No se escribe «integrado» al cerrar la sesión.** Ni «para ahorrar un commit», ni porque «Jhoan lo va a fusionar ya».
- **No se escribe en `dev` después de fusionar** para arreglar un estado (regla 6 del `CLAUDE.md`).
- **No se fusiona sin el commit del momento 2.** Si Jhoan fusionó en GitHub sin avisar y la documentación quedó en «en
  revisión», se dice, y el arreglo va en la **rama de la sesión siguiente**, como su primer commit.
- **No se da por hecho el momento 2 en una sesión nueva**: quien arranca lee `gh pr view <N> --json state`, no la
  documentación, para saber si el PR anterior entró.

## Comprobación rápida

```bash
# ¿La documentación afirma algo que GitHub desmiente?
gh pr list --state open --json number -q '.[].number' | while read n; do
  grep -rn "PR #$n\b.*integrado\|#$n\*\* \*\*integrado" documentations/ && echo "⚠️ el PR #$n está ABIERTO"
done
```
