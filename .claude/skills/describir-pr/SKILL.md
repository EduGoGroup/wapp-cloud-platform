---
name: describir-pr
description: Use in wapp-cloud-platform EVERY time a pull request is created or its description is rewritten — at the close of a plan session (web or local) or for any standalone change. Writes the PR body in a fixed order so Jhoan can read it as a mini timeline: the goal explained pedagogically (with examples), where the PR sits in its phase/session and in the overall plan (or why it is standalone), whether the goal was met with its deviations and pending items, and then the usual commits, gates and not-run list. Triggers — "abre el PR", "crea el PR", "gh pr create", "create_pull_request", "describe el PR", "actualiza el cuerpo del PR", "update_pull_request", "cierre de sesión con PR".
---

# Describir un PR: objetivo, dónde encaja y resultado, antes que lo demás

> Decisión de Jhoan (2026-10-04, sesión F2-01): **todo PR** de este repo lleva este cuerpo. Los protocolos de sesión
> (`plan/sesiones/PROTOCOLO-WEB.md` §6, `PROTOCOLO-CLI.md` §2) dicen **cuándo** se abre el PR; esta skill, **qué dice**.

Un PR aquí lo lee alguien que **no estuvo en la sesión** y que quiere saber tres cosas antes que los SHA: **para qué**
era, **en qué punto del camino** estamos, y **si salió**. El cuerpo se escribe en ese orden; los commits y los gates
van después, como evidencia.

## Paso 0 · ¿El PR es parte de un plan?

Míralo, no lo supongas. Es **PR de plan** si se cumple cualquiera de estas, en este orden:

1. el prompt que arrancó la sesión nombra una sesión del plan (`F2-01`, `F9-04`, `F1-06`…);
2. la rama es `reorg/*`;
3. `git diff --stat origin/dev...HEAD` toca `documentations/reorganizacion-modular/plan/**` o
   `internal/{modulos,nucleo,arranque,apipublica,candados}/**` o `test/procesos/**`;
4. la sesión aparece en `documentations/reorganizacion-modular/plan/sesiones/README.md` («El orden»).

Si **no**, es **PR suelto** (un arreglo, un cambio de documentación fuera del plan, una herramienta): la sección
«Dónde encaja» se reduce a qué área del repo toca y qué PR anteriores están relacionados (`git log --oneline -- <ruta>`,
o la herramienta de GitHub para listar PR). Si existiera **otro** plan en el repo (p. ej. `docs/plans/NNN`), se trata
igual que este: su índice y su estado son la fuente.

**Fuentes del PR de plan** (de ahí sale cada ✅; nada de memoria):

| Qué | Dónde |
|---|---|
| Sesiones de la fase, con su estado | `plan/sesiones/README.md`, tabla «El orden» (columna Estado) |
| Fases del plan y su estado | `ESTADO.md`, tabla «Avance de la ejecución»; el orden de fases, `plan/README.md` |
| Qué hace cada sesión de la fase | README de la fase, tabla «Bloques de sesión» |
| Criterio de parada de la sesión | la ficha `plan/sesiones/<id>-*.md` («Se para cuando») |
| Desvíos y pendientes | `tareas.md` (`[~]`, ✎), «Contradicciones» y «Decisiones que necesita» del README de la fase |

## La plantilla (en este orden)

```markdown
**Integrar SIN squash: rojo y verde son commits distintos.**      ← solo en PR de plan

## 🎯 Objetivo
## 🧭 Dónde encaja
### En la fase
### En el plan
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
- Cada término del método se explica **la primera vez** que sale, en media línea: «inventario E-12 (la tabla que
  clasifica cada fichero en simple / medio / complejo según tenga estado, concurrencia o BD)», «hoja (un paquete que
  no depende de otros del módulo)», «rojo (contrato y test sin lógica, que fallan a propósito)».
- **Un ejemplo** cuando el cambio no se entiende sin él: un antes/después, un fragmento de código de 5–10 líneas,
  una fila de tabla. Mejor uno bueno que tres.

> Ejemplo de ejemplo: «`entitlements.go` es **simple** (constantes y una interfaz): contrato, test y lógica en un solo
> commit. `middleware.go` es **medio** (decide 403 en tres casos): primero un commit rojo con el contrato y su test
> fallando, luego el verde con la lógica.»

### 🧭 Dónde encaja — una mini línea de tiempo

**En la fase**: la lista de sesiones de la fase, una por línea, con su estado y qué hace:

```markdown
- ✅ F2-00 … (si hubo)
- 👉 **F2-01 · inventario E-12 + hojas simples** — este PR
- ⏳ F2-02 · `usecase` e `identity`
- ⏳ F2-03 · Postgres, HTTP y `platformadmin`
- ⏳ F2-04 · `bridge_iam.go`, conmutación y rutas
- ⏳ F2-05 · cierre local (suites contra Postgres, procesos)
```

**En el plan**: una línea con las fases en su orden real (`plan/README.md`), el estado de cada una (`ESTADO.md`) y
dónde está este PR; y una frase de **qué desbloquea**:

```markdown
F0 ✅ → F9-A ✅ → F1 ✅ → F9-B ✅ → **F2 🔄 (F2-01 👈)** → F3 ⏳ → F4+F5 ⏳ → F6 ⏳ → F7 ⏳ → F8 ⏳ → F9-D ⏳ → F10 ⏳
```

Leyenda fija: ✅ hecha · 🔄 en curso · 👉/👈 este PR · ⏳ pendiente · ⛔ bloqueada (y por qué). FX (la cara HTTP) va
dentro de las fases que la tocan; se nombra solo si el PR la toca.

### ✅ Resultado — honesto

- **Objetivo: conseguido / parcial / no conseguido.** Se comprueba el criterio de parada de la ficha **punto a punto**
  (una línea por punto, ✅ o ❌ con su evidencia: SHA, rc, fichero). «Parcial» si falta uno; nunca «conseguido» con un
  ❌.
- **Desvíos**: lo que se hizo distinto de la spec, la ficha o el plan aprobado — qué, **por qué**, y **quién lo decidió**
  (Jhoan en la sesión, una decisión `D-…`, o la sesión, y entonces se dice). Incluye los incidentes que cambiaron el
  camino (un reinicio, un candado que estorbó).
- **Pendiente**: decisiones abiertas para Jhoan (🟡, con su número de hallazgo), lo que no se pudo correr y quién lo
  cierra, y lo que hereda la sesión siguiente.

### 📦 Qué trae · 🔬 Gates · 🚫 No corrido

- **Qué trae**: commits agrupados por tarea o paquete, con SHA. Sin repetir el objetivo.
- **Gates**: tabla `gate | resultado`, con el rc **leído del log, sin pipe**, y los SKIP **contados** (skill
  `validar-antes-de-cerrar`). La cobertura, como informe, no como gate. Di con qué toolchain se corrió.
- **No corrido**: lo que la sesión no pudo o no debía correr, y quién lo cierra (la local, F9, otra sesión).
- Cierra con el enlace de sesión (`https://claude.ai/code/${CLAUDE_CODE_REMOTE_SESSION_ID/#cse_/session_}` en la web)
  y la línea de autoría que pida el entorno.

## Reglas

- 🔴 **Nada de estado inventado**: cada ✅ de «Dónde encaja» y de «Resultado» sale de un fichero o de un SHA que acabas de
  leer. Si una fuente dice otra cosa que tu sesión (p. ej. `sesiones/README.md` sin actualizar), se arregla la fuente
  en el PR o se dice.
- **Título**: en PR de plan, `<id de sesión> · <bloque>` (PROTOCOLO-WEB §6). En PR suelto, una frase en imperativo.
- **Cómo se pasa el cuerpo**: escríbelo en un fichero (`$TMPDIR/pr.md`, o el scratchpad) y pásalo entero:
  `gh pr create --base dev --title … --body-file "$TMPDIR/pr.md"` en local; en la web, la herramienta MCP de GitHub
  (`create_pull_request`, y `update_pull_request` para reescribirlo). Al **actualizar** un PR (la sesión de cierre
  local que trabaja en la rama de la web, un push nuevo que cambia el resultado) se reescribe **entero** con esta
  plantilla: «Resultado» y «Gates» tienen que decir lo de ahora.
- Español, frases cortas, sin relleno. Las tablas, solo donde comparan.

## Ejemplo recortado (F2-01, PR #29)

```markdown
**Integrar SIN squash: rojo y verde son commits distintos.**

## 🎯 Objetivo
Arrancar la fase F2 (`acceso`: IAM, derechos comerciales y operador de plataforma). Antes de escribir código hay
que clasificar sus 51 ficheros viejos con el **inventario E-12** … y construir las **hojas** … Ejemplo: …

## 🧭 Dónde encaja
### En la fase
- 👉 **F2-01 · inventario E-12 + hojas simples** — este PR
- ⏳ F2-02 · `usecase` e `identity` …
### En el plan
F0 ✅ → F9-A ✅ → F1 ✅ → F9-B ✅ → **F2 🔄 (F2-01 👈)** → F3 ⏳ → …
Desbloquea F2-02: los usecases se prueban contra los dobles de memoria que nacen aquí.

## ✅ Resultado
**Conseguido.** ✅ inventario aprobado (`1d73874`) · ✅ `auth.go` partido, huella igual (`f46a107`) · …
### Desvíos
- T2.34 nombraba mal el fichero del candado de invitaciones → `auth_roleplane.go` (Jhoan, P3).
### Pendiente
- 🟡 hallazgo 13: el valor cero de `RedemptionVerdict` es «procede». …

## 📦 Qué trae
…
```

## Relacionadas

`validar-antes-de-cerrar` (los gates que van en la tabla) · `reconstruir-modulo` (cierre de fase) ·
`traspaso-web-local` (si algo lo cierra el otro entorno, el PR lo enlaza en «Pendiente»).
