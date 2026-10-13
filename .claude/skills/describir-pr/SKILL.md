---
name: describir-pr
description: Use in wapp-cloud-platform EVERY time a pull request is created or its description is rewritten — at the close of a work session or for any standalone change. Writes the PR body in a fixed order so Jhoan can read it as a mini timeline: the goal explained pedagogically (with examples), where the PR sits (the area it touches, the related PRs, and its plan if it belongs to one), whether the goal was met with its deviations and pending items, and then the usual commits, gates and not-run list. Triggers — "abre el PR", "crea el PR", "gh pr create", "create_pull_request", "describe el PR", "actualiza el cuerpo del PR", "update_pull_request", "cierre de sesión con PR".
---

# Describir un PR: objetivo, dónde encaja y resultado, antes que lo demás

> Decisión de Jhoan (2026-10-04): **todo PR** de este repo lleva este cuerpo. La regla 6 del `CLAUDE.md` dice
> **cuándo** se abre el PR (siempre: ningún código nace en `dev`); esta skill, **qué dice**.

Un PR aquí lo lee alguien que **no estuvo en la sesión** y que quiere saber tres cosas antes que los SHA: **para qué**
era, **en qué punto del camino** estamos, y **si salió**. El cuerpo se escribe en ese orden; los commits y los gates
van después, como evidencia.

## Paso 0 · ¿El PR es parte de un plan?

Míralo, no lo supongas. Lo normal hoy es el **PR suelto** (un arreglo, una deuda de `documentations/deuda.md`, un
cambio de documentación, una herramienta): «Dónde encaja» dice qué área del repo toca, qué deuda o decisión lo
motiva (`D-nn`) y qué PR anteriores están relacionados (`git log --oneline -- <ruta>`, `gh pr list --state merged`).

Es **PR de plan** solo si el trabajo tiene un plan escrito con sus pasos —Jhoan dice dónde vive al abrirlo— y el
prompt que arrancó la sesión nombra uno de esos pasos. Entonces el índice y el estado de **ese** plan son la fuente
de cada ✅ de «Dónde encaja»: nada de memoria. (La reconstrucción modular, 2026-09-27 → 2026-10-12, fue el plan con
el que nació esta skill; su expediente está archivado: `documentations/modular-reorganization.md`.)

## La plantilla (en este orden)

```markdown
**Integrar SIN squash.**

## 🎯 Objetivo
## 🧭 Dónde encaja
### En el plan                                                    ← solo en PR de plan
## ✅ Resultado
### Desvíos
### Pendiente
## 📦 Qué trae
## 🔬 Gates
## 🚫 No corrido
<enlace de sesión> · <línea de autoría que pida el entorno>
```

### 🎯 Objetivo — pedagógico

- Qué problema resuelve y **por qué ahora**, escrito para quien no conoce la sesión. Máximo ~15 líneas.
- Cada término propio del repo se explica **la primera vez** que sale, en media línea: «suite de contrato (los
  tests de un puerto, que corren igual contra el doble en memoria y contra Postgres)», «candado (un test que recorre
  el árbol y falla si se rompe una regla de forma)», «huella (la lista de rutas, rpc, métricas y goroutines que el
  proceso expone)».
- **Un ejemplo** cuando el cambio no se entiende sin él: un antes/después, un fragmento de código de 5–10 líneas,
  una fila de tabla. Mejor uno bueno que tres.

### 🧭 Dónde encaja

**PR suelto**: dos o tres líneas. Qué área toca (el módulo de `internal/modulos/`, la cara, el arranque, la
documentación), qué lo motiva (la deuda `D-nn`, una decisión de Jhoan con su fecha, un fallo visto en UAT) y los PR
relacionados, con su número.

**PR de plan**: además, una mini línea de tiempo con los pasos del plan, uno por línea, con su estado y qué hace, y
una frase de **qué desbloquea**:

```markdown
- ✅ paso 1 · …
- 👉 **paso 2 · …** — este PR
- ⏳ paso 3 · …
```

Leyenda fija: ✅ hecho · 🔄 en curso · 👉 este PR · ⏳ pendiente · ⛔ bloqueado (y por qué).

### ✅ Resultado — honesto

- **Objetivo: conseguido / parcial / no conseguido.** Se comprueba el criterio de parada del encargo **punto a punto**
  (una línea por punto, ✅ o ❌ con su evidencia: SHA, rc, fichero). «Parcial» si falta uno; nunca «conseguido» con un
  ❌.
- **Desvíos**: lo que se hizo distinto del encargo o del plan aprobado — qué, **por qué**, y **quién lo decidió**
  (Jhoan en la sesión, una decisión escrita, o la sesión, y entonces se dice). Incluye los incidentes que cambiaron el
  camino (un reinicio, un candado que estorbó).
- **Pendiente**: decisiones abiertas para Jhoan (🟡), lo que no se pudo correr y quién lo cierra, y lo que queda
  para después (si es deuda, con su `D-nn` de `deuda.md`).

### 📦 Qué trae · 🔬 Gates · 🚫 No corrido

- **Qué trae**: commits agrupados por tarea o paquete, con SHA. Sin repetir el objetivo.
- **Gates**: tabla `gate | resultado`, con el rc **leído del log, sin pipe**, y los SKIP **contados** (skill
  `validar-antes-de-cerrar`). La cobertura, como informe, no como gate. Di con qué toolchain se corrió.
- **No corrido**: lo que la sesión no pudo o no debía correr, y quién lo cierra.
- Cierra con el enlace de sesión y la línea de autoría que pida el entorno.

## Reglas

- 🔴 **El estado que escribes es el de un PR ABIERTO.** Ni el cuerpo ni la documentación que viaja en el PR dicen
  «integrado», «cerrado» o «hecho» mientras esté en revisión: eso es un último commit, justo antes de
  fusionar (skill `pre-merge-docs`). En «Dónde encaja», este PR es 👉 «en revisión», no ✅.

- 🔴 **Nada de estado inventado**: cada ✅ de «Dónde encaja» y de «Resultado» sale de un fichero o de un SHA que acabas de
  leer. Si una fuente dice otra cosa que tu sesión (p. ej. una deuda que sigue «abierta»), se arregla la fuente
  en el PR o se dice.
- **Título**: una frase en imperativo; en PR de plan, con el identificador del paso delante.
- **Cómo se pasa el cuerpo**: escríbelo en un fichero (`$TMPDIR/pr.md`, o el scratchpad) y pásalo entero:
  `gh pr create --base dev --title … --body-file "$TMPDIR/pr.md"`. Al **actualizar** un PR (un push nuevo que
  cambia el resultado, commits que entran en la revisión) se reescribe **entero** con esta plantilla
  (`gh pr edit <N> --body-file …`): «Resultado» y «Gates» tienen que decir lo de ahora.
- Español, frases cortas, sin relleno. Las tablas, solo donde comparan.

## Ejemplo recortado (PR suelto)

```markdown
**Integrar SIN squash.**

## 🎯 Objetivo
`conversation_event_messages` se escribe cifrada y no está en el censo de rotación de llaves: una rotación se
declararía completa dejando esas filas ilegibles. Este PR la mete en el censo. Ejemplo: …

## 🧭 Dónde encaja
Toca `internal/platform/crypto` (el censo) y el proceso de rotación de `test/procesos/`. Cierra la deuda D-1.
Relacionados: #NN (el censo actual).

## ✅ Resultado
**Conseguido.** ✅ la tabla está en el censo (`abc1234`) · ✅ el proceso de rotación la recorre (`def5678`) · …
### Desvíos
- Ninguno.
### Pendiente
- 🟡 falta rotar en UAT: lo hace Jhoan tras fusionar.

## 📦 Qué trae
…
```

## Relacionadas

`validar-antes-de-cerrar` (los gates que van en la tabla) · `pre-merge-docs` (qué dice la documentación mientras el
PR está abierto y qué al fusionarlo).
