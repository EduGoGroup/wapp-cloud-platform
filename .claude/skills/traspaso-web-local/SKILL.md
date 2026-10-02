---
name: traspaso-web-local
description: Use when work on wapp-cloud-platform is split between a Claude Code web session (claude.ai/code — writes code and docs, sees only this repo; has Docker, so it may run the F9 processes only as a pre-check, W-1) and a Claude Code local session (closes: testcontainers runs that count, the old integration tests, UAT over SSH, moving main, the rest of the wApp ecosystem). Invoke to write the handoff that bridges them, or when starting a session that received one. Triggers — "traspaso", "handoff", "pásalo a local", "lo cierra la sesión local", "continúa lo que dejó la web", "prompt para la sesión local".
---

# Traspaso web ↔ local

> Versión para **este repo** de la skill `pasar-la-pelota` del ecosistema (que es para el tándem
> Cowork ↔ CLI y habla de Neon y de la raíz de wApp, que desde aquí no se ven).

## La regla madre: la web abre, la local cierra

1. **La sesión web abre.** Escribe contratos, tests, lógica y documentación, corre los gates que
   su entorno permite y deja el terreno preparado. El grueso del trabajo es suyo.
2. **La sesión local cierra.** Hace **solo** lo que la web no puede. No viene a rehacer ni a
   revisar de oficio: viene a **cerrar**, y a intentar refutar lo que la web dio por cierto.
3. **El puente es el fichero de traspaso.** La sesión que entra **no ve la conversación
   anterior**. Lo que no esté escrito ahí, no existe.

**No adaptes el proyecto a las limitaciones del entorno.** Si la web no puede correr algo, lo
anota; no lo esquiva (bajar la versión de Go, meter un `t.Skip`, cambiar un test para que no
necesite Docker, apuntar a un Postgres vivo).

## Qué puede cada una

| | Web (claude.ai/code) | Local (Claude Code en la máquina de Jhoan) |
|---|---|---|
| Escribir código, tests y docs | ✅ | ✅ |
| `make ci-local` (fmt, vet, lint `v2.12.2`, test, build) | ✅ si `make toolchain` da `TOOLCHAIN=OK` (el lint fijado lo deja en el `PATH` el setup del entorno: `documentations/reorganizacion-modular/06-entorno-web.md` §3 y §6) | ✅ si `make toolchain` da `TOOLCHAIN=OK`; si falta el lint, `make tools` (una vez por *checkout*) |
| Contratos y rojo/verde (tests unitarios) | ✅ | ✅ |
| **Tests de proceso de F9** (testcontainers) | Escribir y compilar (`go vet -tags integracion`) · **correrlos como pre-chequeo** si la primera sesión web (T0.0) dejó «funciona» en `06-entorno-web.md` §5 (decisión **W-1**); su verde **no cierra** nada | ✅ **los corre y los cierra** (la corrida que cuenta) |
| Tests de integración **viejos** (`make test-integration`) | ❌ | ✅ (solo importan en F0 y en el relevo) |
| Ver el resto del ecosistema (docs raíz, BFF, consolas, Edge) | ❌ | ✅ |
| UAT por SSH, desplegar | ❌ | ✅ |
| Mover `main` | ❌ | ✅ solo si Jhoan lo pide |

**Ramas.** Toda ola aterriza en `dev`. Si la sesión web trabajó en una rama propia (lo habitual en
claude.ai/code), el traspaso **la nombra**, y la sesión local la integra en `dev` después de sus
gates. Nunca a `main`.

## Dónde va el fichero

`documentations/reorganizacion-modular/traspasos/TRASPASO-<fase>-<tema>.md`
(p. ej. `TRASPASO-F1-nucleo-contact.md`). **Nunca** en la raíz del repo ni suelto en otra carpeta.
Cuando la sesión local lo cierra, añade al final una sección `## CERRADO <fecha>` (literal: el hook `SessionStart` da por abierto todo traspaso sin una línea `^## CERRADO`) con lo que hizo y
lo que refutó; el fichero no se borra: es la historia de la reconstrucción.

## Estructura obligatoria

Ocho secciones, en este orden, con **comandos literales** que se puedan copiar:

```
Contexto: [qué fase, qué módulo, qué fecha — una frase]

═══ 0. BLOQUEANTE ═══
    [lo que impide empezar. Si no hay nada: "ninguno" — la sección no se omite.
     La toolchain NO se escribe aquí como receta: quien recibe corre `make toolchain` (rc=0 y
     TOOLCHAIN=OK) y, en local, `make tools` si falta el lint. Solo va aquí si eso no basta]

═══ 1. Rama y commits ═══
    [rama de trabajo, último SHA, commits rojo/verde/conmutar hechos, qué hay sin pushear]

═══ 2. go.mod ═══
    [dependencias añadidas (p. ej. testcontainers-go) y qué verificar: que `go 1.26.5` no cambió,
     que go.sum se generó con red real]

═══ 3. Gates que la web corrió ═══
    [la salida de `make toolchain` (GO_EFFECTIVE, LINT_EFFECTIVE, TOOLCHAIN=OK|NOT_READY) y,
     después, cada comando, su rc leído SIN pipe, conteos PASS/FAIL, pendientes, cobertura y
     SKIP en código nuevo. Un `go` suelto como gate: con `GOTOOLCHAIN=go1.26.5` delante, o por `make`]

═══ 4. Lo que solo la sesión local puede hacer ═══
    [comandos exactos y la salida esperada: `make test-procesos` contra el binario viejo y contra
     el nuevo, `make test-integration` si tocó platform, prueba en UAT…]

═══ 5. Lo que quedó sin tocar ═══

═══ 6. Integración en dev ═══
    [orden de merge de la rama de la web en dev, commits temáticos si hay que reordenar]

═══ 7. Tres cosas que quiero que revises con ojo crítico, no que aceptes ═══
    [OBLIGATORIA Y CON CONTENIDO REAL — ver abajo]

═══ 8. Decisiones que necesitan a Jhoan ═══
    [p. ej. seguir tras el piloto F1, D-10..D-13 de 03-pendientes-y-contratos.md]
```

## La sección 7 es la que da valor

Ahí va **toda decisión que no estaba en `05`** (una excepción a «un fichero, un test», un puente
nuevo, una regla vieja que se decidió no mantener) y, sobre todo, **lo que no pudiste comprobar**,
dicho como lo que es. Se escribe preguntándose: *«¿qué estoy dando por cierto porque lo leí, no
porque lo vi?»*. La sesión local intenta **refutarlo contra el código y contra lo que corre**, no
contra la documentación.

El antipatrón: llenar la §7 con las decisiones vistosas y omitir la afirmación aburrida que nadie
comprobó.

## Prohibiciones

- 🚫 Bajar `go 1.26.5` en `go.mod` para acomodar un entorno.
- 🚫 Generar `go.sum` sin red real.
- 🚫 Declarar pasado un gate corrido con una versión de herramienta distinta de la fijada: se dice
  qué versión se usó y que **no es autoritativa**. La que cuenta es la **efectiva**, la que dice
  `make toolchain`, no la del `PATH`: en local un `go` suelto es `go1.27.1` aunque `make` use
  `go1.26.5`. La toolchain la pone el `Makefile` y el lint, `make tools`: no hace falta exportar
  `GOTOOLCHAIN` en la sesión ni instalar el lint en un directorio aparte.
- 🚫 `t.Skip` para que algo «pase» en la web.
- 🚫 Apuntar un test a un Postgres vivo (`WAPP_TEST_DB_DSN`, `localhost:5432`, UAT): los tests de
  proceso usan **testcontainers** (skill `procesos-testcontainers`).
- 🚫 Levantar a la vez `cmd/server` y `cmd/server-modular` contra la misma BD o los mismos puertos.
- 🚫 Decidir rumbo en solitario: ante un conflicto de producto o con un ADR, **pregunta a Jhoan**.

## Al recibir un traspaso

1. Lee el fichero entero antes de tocar nada.
2. Verdad de campo: `git fetch origin`, la rama y el SHA que dice el traspaso existen.
3. Repite los gates de la §3 con **tu** toolchain fijada, y compara. Antes: `make toolchain` →
   `TOOLCHAIN=OK` y `rc=0`; en local, si falta el lint, `make tools` y otra vez `make toolchain`.
4. Haz la §4. Refuta la §7.
5. Cierra con la sección `CERRADO <fecha>` en el mismo fichero, e integra en `dev`.
