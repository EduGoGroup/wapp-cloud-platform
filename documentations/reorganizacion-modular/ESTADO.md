# Estado de la reorganización modular — punto de retoma

> **Última actualización: 2026-09-30**, al cerrar la sesión **F0-03** (F0 · bloque C, los candados de fichero). Este fichero es
> para **retomar**: dónde estamos, qué está decidido, qué falta decidir y cuál es el siguiente paso.
> Cada sesión de ejecución lo actualiza al cerrar (fase, bloque, siguiente paso, SHA).

## Dónde estamos

**Fase: plan de trabajo escrito y validado · ejecución sin empezar.** No hay ni una línea de código
de la reconstrucción. Existe el **plan ejecutable** en [`plan/`](plan/README.md): el marco común,
una *spec* por fase (F0–F10 y la transversal FX), el registro de decisiones y **81 sesiones** con
su prompt. **00-02 hecho (2026-09-30)**: Jhoan acepta en bloque las recomendaciones de
[`plan/DECISIONES.md`](plan/DECISIONES.md) §1, §2, §4, §5 y §6 (§3 sigue abierta hasta la parada de F1).

**F0-01 hecha (2026-09-30)**: entorno web verificado (`go1.26.5`, lint `v2.12.2`, `make ci-local`
`GATE_RC=0` en 217 s, **testcontainers funciona**: `TC_RC=0`, Ryuk limpia) y hook `SessionStart`
commiteado ([`06-entorno-web.md`](06-entorno-web.md) §5). Rama `reorg/f0-a-entorno-web`, PR a `dev`.

**F0-02 hecha (2026-09-30)**: `internal/pendiente` (rojo `d7600d3` → verde `f3b322c`, 100 %),
`make vet-pendiente` y `make test-pendiente` con `vet-pendiente` dentro de `ci-local` (`d74dd7f`) y
`make lint` que exige `v2.12.2` (T0.26, `d05ac3a`). `make test-pendiente` → `PENDIENTES=0`,
`ROJOS=0`, `rc=0`; gate ci-local `GATE_RC=0`. Rama `reorg/f0-b-pendiente-make`, PR a `dev`.

**F0-03 hecha (2026-09-30)**: `internal/candados` (rojo `2c2bbd6` → seis verdes, de `b2ecfce` a
`42884fb`, 91,7–100 % por fichero) y los cinco candados de fichero en `ci-local` sobre el árbol real:
`internal/modulos/{fronteras,un_fichero_un_test,exportados_cubiertos}_test.go` (`ca462a6`, lista
blanca **medida**: 13 aristas), `test/procesos/sin_bd_viva_test.go` sin etiqueta (`b522b0f`) y
`make cobertura-ficheros` (`3e85144`: `FICHEROS_EVALUADOS=7`, `POR_DEBAJO=0`). Cada uno muerde con su
caso de `testdata/` y con una demostración en el árbol real. `PENDIENTES=0`, `ROJOS=0`; gate ci-local
`GATE_RC=0` **en un *worktree* limpio** (contradicción 15 del README de F0: la raíz ignora `*.out`).
Rama `reorg/f0-c-candados`, PR a `dev`.

**Siguiente paso:**
1. **Jhoan**: fusionar el PR de F0-03 **sin squash** («Rebase and merge», F-1). Sigue pendiente
   aplicar **F0-A-1** en claude.ai/code (variable `TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX=mirror.gcr.io/`
   y el paso 4 del *setup script*, [`plan/00-marco/flujo-web-local.md`](plan/00-marco/flujo-web-local.md) §3).
2. Arrancar **F0-04** (F0 · bloque D, arranque nuevo y huella, T0.10–T0.15; D-F0-1 y D-F0-2 ya
   decididas). Ojo: desde F0-03 `ci-local` incluye los candados; `internal/arranque` entra en
   `fronteras` y `exportados_cubiertos` (no en un-fichero-un-test ni en la cobertura, D-F0-1), y
   `internal/arranque/huellatest` en todos. Correr el gate también en un clon limpio (contradicción 15).

## Avance de la ejecución

| Fase | Estado | Último bloque cerrado | SHA |
|---|---|---|---|
| F0 | en curso | C · los candados de fichero (T0.5–T0.9) | A: `98e806d`, `de04088`. B: `d7600d3`, `f3b322c`, `d74dd7f`, `d05ac3a` (en `dev`). C: `3040e82`, `2c2bbd6`, `b2ecfce`, `65d4bc0`, `e61567e`, `681d84e`, `d48e319`, `42884fb`, `b522b0f`, `ca462a6`, `3e85144` (rama `reorg/f0-c-candados`; `origin/dev` @ `c55e9e3`) |
| F9-A/B (adelantado) | pendiente | — | — |
| F1 | pendiente | — | — |
| F2–F8 | pendiente | — | — |
| F9-D | pendiente | — | — |
| F10 | pendiente | — | — |

*(La sesión que cierre un bloque actualiza esta tabla y la línea «Siguiente paso».)*

## Qué se hizo

| Fecha | Qué | Dónde |
|---|---|---|
| 2026-09-27 | Análisis de factibilidad, grafo medido, árbol destino, **el método** (reconstruir por contratos y TDD, arranque paralelo, procesos con testcontainers), cinco skills del repo | `01`–`06`, `.claude/skills/` |
| 2026-09-27 | La rama `refactor/arranque-por-fases` a `dev`, con integración real 4.318 PASS · 0 SKIP; `main` alineado en `2da10b4` | git |
| 2026-09-28 | **El plan de trabajo**, escrito por un equipo de agentes sobre el código real (`dev` @ `1b18932`) y validado en dos pasadas (coherencia entre fases y verdad de campo) | [`plan/`](plan/README.md) |
| 2026-09-28 | `06-entorno-web.md` corregido con la documentación oficial de Claude Code (Docker sí; hooks sí; push solo a la rama de la sesión; *setup* que no aborta) | [`06`](06-entorno-web.md) |
| 2026-09-28 | `04` marca `publicapi` como sustituido por D-10 | [`04`](04-estructura-final.md) |
| 2026-09-30 | **Paso 00-02**: `DECISIONES.md` rellenado con la recomendación por defecto (§1, §2, §4, §5, §6); §3 abierta | [`plan/DECISIONES.md`](plan/DECISIONES.md) |
| 2026-09-30 | **F0-01**: entorno web medido (Docker: daemon a mano, Docker Hub 429 → espejo `mirror.gcr.io`; testcontainers v0.44.0 `TC_RC=0`; el proxy acepta `--force-with-lease`) y hook `SessionStart` (`.claude/settings.json`) | [`06`](06-entorno-web.md) §5 · [`plan/F0-andamiaje/tareas.md`](plan/F0-andamiaje/tareas.md) |
| 2026-09-30 | **F0-02**: `internal/pendiente` (rojo→verde), `make vet-pendiente`/`test-pendiente`, `vet-pendiente` en `ci-local`, `make lint` exige `v2.12.2` (T-1) | [`plan/F0-andamiaje/tareas.md`](plan/F0-andamiaje/tareas.md) · `Makefile` |
| 2026-09-30 | **F0-03**: `internal/candados` (rojo → 6 verdes) y los cinco candados de fichero en `ci-local` (fronteras con lista blanca medida, un fichero un test, exportados cubiertos, sin BD viva, cobertura por fichero ≥ 80 %), cada uno con su caso que muerde | [`plan/F0-andamiaje/tareas.md`](plan/F0-andamiaje/tareas.md) T0.5–T0.9 |

## 🔒 Decisiones de Jhoan (cerradas)

1. **No es un movimiento mecánico: es una reconstrucción** por contrato → rojo → verde (2026-09-27).
2. **Arranque paralelo**: `cmd/server` (viejo, oráculo) y `cmd/server-modular` → `internal/arranque`.
3. **Los tests viejos no se portan**, se consultan.
4. **La integración no se porta**: se escribe de cero, por proceso, en caja negra, contra los dos
   binarios; es condición del relevo.
5. **Los tests de proceso usan testcontainers** con una instancia compartida; nunca un Postgres vivo.
6. **La documentación de este trabajo se commitea y pushea a `dev`**; `main`, solo a petición.
7. **Implementa Claude Code en la web**, que solo ve este repo; la sesión local cierra.
8. *(2026-09-27, sesión de plan)* **D-2** `internal/modulos/<m>/` · **D-5** los 7 módulos de `04` ·
   **D-9** `cmd/server-modular`, una prueba en UAT antes del relevo · **D-11** etiquetas `pendiente`
   e `integracion`, cero `t.Skip` · **D-12** 80 % por fichero, recalibrar tras F1.
9. 🔄 **D-10 · la cara HTTP es ÚNICA y NUEVA, `internal/apipublica`, construida por olas**
   (estrangulador delante del `publicapi` viejo; cada módulo muda sus rutas al conmutar). Sustituye
   la recomendación de repartir en `modulos/<m>/http` y el §9.2 de `05`. Ver [`plan/FX-cara-http/`](plan/FX-cara-http/README.md).
10. **Docker en la web**: la primera sesión web lo prueba; si testcontainers funciona, la web corre
    los procesos como pre-chequeo; **cierra la sesión local**.

## Decisiones abiertas

Todas, con su recomendación y la sesión que bloquean, en [`plan/DECISIONES.md`](plan/DECISIONES.md).
Las que cambian el plan entero: **D-F9-1** (adelantar F9: el orden de sesiones lo asume), las
excepciones a E-1 de F0 (**D-F0-1/2/3**, **D-F4-1**) y la **parada tras F1**.

## Lo que el plan corrigió de los documentos 01–05 (resumen)

La norma (`05`) **sigue mandando**; estas son erratas o precisiones medidas, no cambios de método
(salvo D-10, arriba). El detalle, en la sección «Contradicciones encontradas» de cada fase.

- **`05` E-6**: los paquetes con SQL son **22** (no 20); **15 tienen gemelo en memoria y 7 no**
  (`casebank`, `degradation`, `flujos/events`, `flujos/runtime`, `integrations`, `platformadmin`,
  `tenantllm`), no «12 sin gemelo». La suite de contrato de un puerto con BD toma la forma
  `Contrato(t, func(t) Montaje)` (D-F1-1), no `func() Puerto`.
- **`05` §5**: los candados cubren también `internal/nucleo` e `internal/apipublica`, no solo
  `internal/modulos/` (T0.7).
- **`05` §3.2**: faltan el candado C2 de `llmvia`, el de vocabulario de `degradation` y
  `inv1_pedirinfo_ast_test.go`; de `events/summary_test.go` solo 1 de 26 tests es candado; los
  candados AST del canje **no** necesitan BD (D-F2-1).
- **`05` §4 y `04` §2.2**: las ventanas de agregación son **durables** (`intake_jobs`), no memoria;
  el singleton `*runtime.Runtime` sí guarda estado (mutex por clave, limitador, semáforo, rachas).
  Y en la transición `cmd/server-modular` **sí** enlaza paquetes viejos (vía adaptadores).
- **`05` §7 / `03` / skill `traspaso-web-local`**: «la web no tiene Docker» está desfasado.
- **`04` §5**: `catalog.go` no es autocontenido y los alias en el carrito viejo no aplican con E-1;
  `model` se reconstruye en F5 (D-F5-1).
- **Cifras**: 97 ficheros `*_integration_test.go` (132 con BD), no 107 · 71 variables de entorno
  efectivas · 2.623 menciones de `internal/` fuera del repo · 12 comentarios en repos hermanos.
- **Documentación del repo con rutas caducadas**: `contratos.md` (`internal/bootstrap/http.go`),
  `operacion.md` y la constitución (`internal/publicapi/flows.go:75`, que está en
  `internal/bootstrap/arranque/flows.go:75`): las corrige F0 (T0.20).

## Pendientes y obstáculos conocidos

- 🔴 **El lint de `ci-local` no está fijado** (`Makefile:42`): T-1, en F0.
- **testcontainers sube dependencias de producción**: T-2, commit aislado en F9.
- **R2 / `HeadBucket`** (`internal/bootstrap/arranque/flows.go:75`): **resuelto en el diseño** de F9
  sin tocar el arranque (endpoint IP → *path-style*; S3 falso en el proceso de test). Sin ejecutar.
- `make test-integration` usa `postgres:16`; UAT corre `postgres:17-alpine`; la VM web trae un
  PostgreSQL 16 que **no se usa** para tests.
- Fuera de este repo (F10, sesión local): la documentación del ecosistema, la regla de conteo del
  ADR-0010 y los comentarios en repos hermanos.

## Estado de git

- `origin/dev` = `016a657` (el plan y 00-02). `origin/main` = `2da10b4`, sin tocar.
- `origin/reorg/f0-a-entorno-web`: F0-01 (T0.0 `98e806d`, T0.1 `de04088` y el cierre), en PR a `dev`
  para integrar **sin squash**.

## Para retomar

1. Lee [`plan/README.md`](plan/README.md) y, si vas a ejecutar, el fichero de tu sesión en
   [`plan/sesiones/`](plan/sesiones/README.md) (él te dice qué más leer).
2. La norma: [`05-metodo-contratos-y-tdd.md`](05-metodo-contratos-y-tdd.md).
3. Skills del repo: `reconstruir-modulo`, `contrato-tdd`, `validar-antes-de-cerrar`,
   `traspaso-web-local`, `procesos-testcontainers`.
