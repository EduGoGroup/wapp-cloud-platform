# 00 · Marco común del plan — el *steering*

> El plan de la reconstrucción modular está escrito **a lo spec-driven** (estilo Kiro): cada fase
> `F<n>-*/` es una *spec* con requisitos, arquitectura, diseño, reglas y tareas. Esta carpeta es lo
> que **todas** las fases comparten y **no repiten**: qué se construye y por qué, con qué
> herramientas, con qué forma, entre qué entornos, y con qué palabras. Escrita el 2026-09-28 sobre
> `dev` @ `1b18932`.
>
> **La norma sigue siendo [`05-metodo-contratos-y-tdd.md`](../../05-metodo-contratos-y-tdd.md).**
> Si algo de aquí choca con `05`, manda `05` y este marco se corrige. La única excepción escrita es
> **D-10** (la cara HTTP nueva `apipublica`), decisión de Jhoan posterior a `05`.

## Qué es cada fichero

| Fichero | Contesta | Equivale en Kiro a |
|---|---|---|
| [`producto.md`](producto.md) | Qué es la reconstrucción y **por qué** (limpiar los tests: un fichero ↔ un test, contratos explícitos), qué **no** es, los **contratos hacia fuera** que no se tocan (recontados), los siete módulos y **todas las decisiones cerradas** con fecha | `product.md` |
| [`tecnologia.md`](tecnologia.md) | Toolchain fijada (y dónde **no** lo está), `make` de hoy y los que nacen en F0, las dos etiquetas, **cómo se lee un gate**, cobertura por fichero, testcontainers (versión y su efecto en `go.mod`), lo que **no** se añade | `tech.md` |
| [`estructura.md`](estructura.md) | El árbol destino por paquete **con D-10**, las piezas transitorias (nacen / mueren), convenciones de nombres, cabecera `// Porta … @ <sha>`, commits y el mapa **fase → módulo → paquetes viejos** | `structure.md` |
| [`flujo-web-local.md`](flujo-web-local.md) | Qué hace la sesión web y qué la local, **ramas y PR** (sin squash), el **entorno web** (variables, *setup script*), el **hook `SessionStart`** propuesto, la prueba de Docker + testcontainers, el **protocolo de sesión**, `--teleport` y **quién cierra qué** | *steering* propio |
| [`glosario.md`](glosario.md) | Los términos del plan (contrato, rojo, puente, huella, estrangulador…) y los del dominio (Edge, Lease, DEK 🔴, tokens, P1–P5, CRM…) con el dato de cada ADR | — |
| [`plantilla-de-fase.md`](plantilla-de-fase.md) | **La forma** de cada carpeta de fase: seis ficheros, criterios EARS, formato de tarea, bloques de sesión | — |

## Orden de lectura para una sesión nueva

1. `CLAUDE.md` del repo (se carga solo) → [`../../ESTADO.md`](../../ESTADO.md): dónde está el trabajo.
2. **Este README** y [`flujo-web-local.md`](flujo-web-local.md) §6 (protocolo): qué hacer al empezar.
3. [`producto.md`](producto.md) §3–§4: lo que **no** se puede cambiar.
4. [`glosario.md`](glosario.md) si una palabra no está clara — sobre todo **DEK**, `intake`/`intakes`
   y `apipublica`/`publicapi`.
5. [`tecnologia.md`](tecnologia.md) §5 **antes de declarar cualquier gate**.
6. La carpeta de **su** fase (`../F<n>-*/README.md`, luego `tareas.md`) y el bloque que le toca en
   `../sesiones/`.
7. [`estructura.md`](estructura.md) y [`plantilla-de-fase.md`](plantilla-de-fase.md) cuando vaya a
   crear ficheros o a escribir documentación del plan.

Una sesión que **escribe** el plan (no que lo ejecuta) lee primero `plantilla-de-fase.md` y `05`.

## Lo que este marco corrigió o descubrió (2026-09-28)

| Qué | Dónde se trata |
|---|---|
| El entorno web **sí** tiene Docker y **sí** corre los hooks del repo; el *setup script* con `set -e` **impediría arrancar** la sesión si falla; el lint por `install.sh` choca con el proxy de GitHub (release assets solo de repos adjuntos) | [`../../06-entorno-web.md`](../../06-entorno-web.md) (corregido) · [`flujo-web-local.md`](flujo-web-local.md) §3 |
| El `Makefile` **no fija** el lint (usa el del `PATH`: v2.14.0 en local) y `go 1.26.5` es un mínimo (local corre 1.27.1) | [`tecnologia.md`](tecnologia.md) §1, decisión T-1 |
| testcontainers v0.44.0 sube `httpsnoop` y `otelhttp`, que están en el binario de producción | [`tecnologia.md`](tecnologia.md) §6, decisión T-2 |
| La rama por defecto del remoto es `main`: el PR de la web va con `--base dev` | [`flujo-web-local.md`](flujo-web-local.md) §2 |
| `contratos.md` sitúa rutas en `internal/bootstrap/http.go` y `bootstrap.go:1425-1476`; hoy están en `internal/bootstrap/arranque/{http.go,rutas_admin.go}`. Las **cifras** (95 rutas, 70 variables, 17+5 métricas) sí se sostienen | [`producto.md`](producto.md) §4 |
| La VM web trae **PostgreSQL 16 preinstalado**: prohibido para los tests (sería un Postgres vivo) | [`tecnologia.md`](tecnologia.md) §6 |

## Decisiones que necesita Jhoan (del marco)

| Id | Pregunta | Recomendación | Bloquea |
|---|---|---|---|
| T-1 | ¿F0 hace que `make lint` falle si la versión no es `v2.12.2`? | Sí | F0 |
| T-2 | ¿Se acepta que testcontainers suba `httpsnoop`/`otelhttp` de producción? | Sí, en commit aislado en F9 con `test-integration` antes | F9 |
| F-1 | ¿Quién fusiona los PR de la web? | Jhoan («Rebase and merge») en bloques 🌐; la local (`merge --no-ff`) en 🌐→💻. Nunca squash | S01 |
| F-2 | Configurar variables y *setup script* del entorno web | Las de [`flujo-web-local.md`](flujo-web-local.md) §3 | S01 |
