---
name: pre-merge-docs
description: Use in wapp-cloud-platform whenever a session writes, in documentation, the STATE of a pull request or of the work it carries (a debt entry, a plan, a task list) — at the close of a session, when opening a PR, while a PR is under review, and above all right before merging. Fixes what the docs may say at each moment so they never claim «PR integrado», «cerrada» or «hecho» while the PR is still open. Triggers — "cierra la sesión", "abre el PR", "actualiza el estado", "mergea", "fusiona el PR", "voy a fusionar", "gh pr merge", "marca como hecha", "PR integrado".
---

# Documentación antes de fusionar: lo que se dice mientras el PR está abierto y lo que se dice al fusionarlo

> Decisión de Jhoan (2026-10-11): la documentación **no puede dar por fusionado un PR que está en revisión**. Nació
> en la reconstrucción modular —el cierre de una fase dijo «integrado» durante horas mientras se revisaba, y en esa
> revisión entraron siete commits más— y vale para **cualquier** PR de este repo (regla 6 del `CLAUDE.md`).

La idea es simple: **dos momentos, dos textos**. El cambio de estado viaja **dentro** del PR, como su último commit, y
nadie escribe en `dev` después de fusionar.

## Qué documentación lleva estado

La que diga **dónde está** un trabajo: una entrada de `documentations/deuda.md` que el PR cierra, el plan o la lista
de tareas que el trabajo tenga (si lo tiene: en este repo un plan vive donde Jhoan diga al abrirlo), la ficha de un
trabajo terminado (`documentations/modular-reorganization.md` es el ejemplo) y el cuerpo del propio PR. La que
describe **el código** (`arquitectura.md`, `contratos.md`…) no lleva estado: describe el árbol de la rama, y es cierta
en cuanto el PR entra.

## Momento 1 · La sesión termina y abre el PR (o lo actualiza)

La documentación dice **lo que es cierto ahora**: el trabajo está en una rama y el PR está **abierto**.

| Dónde | Se escribe | No se escribe todavía |
|---|---|---|
| Lista de tareas del trabajo, si la hay | `[x]` con el SHA del commit que la hace, y «(PR #N, en revisión)» | — |
| Estado del trabajo (plan, ficha) | «PR #N **abierto, en revisión**» | «integrado», «en `dev`», «✅ cerrado» |
| «Siguiente paso» | «pendiente de fusionar el PR #N; después, <lo siguiente>» | lo siguiente como si ya tocara |
| `deuda.md` | lo decidido, con su fecha: una decisión es cierta desde que se toma | «✅ CERRADA» en una deuda cuyo cierre depende de que el PR entre |

Un `[x]` con SHA **no miente**: dice que el commit existe. Lo que miente es decir dónde está ese commit.

## Momento 2 · Justo antes de fusionar

Ocurre **solo** cuando Jhoan pide fusionar («mergea», «fusiona») o avisa de que va a fusionar él en GitHub.

1. Comprueba que el PR sigue abierto y que la rama está empujada: `gh pr view <N> --json state,headRefOid`.
2. **Un commit, el último de la rama**, que pasa todo lo del momento 1 a su forma final: «PR #N **integrado**»,
   la deuda «✅ CERRADA», el trabajo «hecho (fecha)», y el «siguiente paso» apuntando ya a lo siguiente. Se cita el
   **número del PR**, no el SHA del merge, que todavía no existe.
   Mensaje: `docs: PR #N integrado` (más lo que cierre).
3. Busca lo que se haya quedado atrás, antes de empujar:
   `grep -rn 'en revisión' CLAUDE.md documentations/ | grep '#<N>'` → vacío.
4. `git push origin <rama>` (rc sin pipe) y, si lo pidió Jhoan, `gh pr merge <N> --merge` (**nunca squash**).
5. Si la revisión añadió commits después del momento 1, el cuerpo del PR se actualiza antes (skill `describir-pr`).

## Lo que no se hace

- **No se escribe «integrado» al cerrar la sesión.** Ni «para ahorrar un commit», ni porque «Jhoan lo va a fusionar ya».
- **No se escribe en `dev` después de fusionar** para arreglar un estado (regla 6 del `CLAUDE.md`).
- **No se fusiona sin el commit del momento 2.** Si Jhoan fusionó en GitHub sin avisar y la documentación quedó en «en
  revisión», se dice, y el arreglo va en la **rama del trabajo siguiente**, como su primer commit.
- **No se da por hecho el momento 2 en una sesión nueva**: quien arranca lee `gh pr view <N> --json state`, no la
  documentación, para saber si el PR anterior entró.

## Comprobación rápida

```bash
# ¿La documentación afirma algo que GitHub desmiente?
gh pr list --state open --json number -q '.[].number' | while read n; do
  grep -rn "PR #$n\b.*integrado\|#$n\*\* \*\*integrado" CLAUDE.md documentations/ && echo "⚠️ el PR #$n está ABIERTO"
done
```
