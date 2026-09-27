# Reorganización modular de `internal/` — portal

> **Estado: ANÁLISIS DE FACTIBILIDAD** (2026-09-27). Todavía **no es un plan ejecutable**: no
> hay olas, ni tareas, ni prompts de implementación. Esos documentos se escriben **después** de que
> Jhoan valide este análisis y decida las preguntas abiertas de
> [`03-pendientes-y-contratos.md`](03-pendientes-y-contratos.md) §3.

## Qué se pretende

Ordenar `internal/` por **módulos de negocio** en carpetas, para que la separación de
responsabilidades se lea en el árbol de directorios y no haya que reconstruirla leyendo 707
ficheros. Dos condiciones fijas:

1. **Hacia fuera no se rompe nada**: ni una ruta HTTP, ni un rpc, ni una tabla, ni una variable de
   entorno, ni un flag de CLI, ni un texto observable.
2. **Hacia dentro se permite mover y re-cablear**, siempre con los gates verdes.

## Veredicto en una línea

**Factible y de riesgo bajo si se hace en dos tiempos**: primero *mover* (mecánico, verificable por
el compilador, no toca comportamiento), después *cortar ciclos* (trabajo real de diseño, módulo a
módulo). Hacer las dos cosas a la vez es lo que lo volvería caro. El detalle y la evidencia, en
[`01-factibilidad.md`](01-factibilidad.md).

## Índice

| Documento | Qué contesta |
|---|---|
| [`01-factibilidad.md`](01-factibilidad.md) | **Empieza aquí.** El veredicto, por qué es factible, qué lo encarece, las opciones y la recomendación |
| [`02-mapa-de-dependencias.md`](02-mapa-de-dependencias.md) | El grafo medido hoy, los paquetes que están en la carpeta equivocada, los ciclos que sobreviven a una agrupación candidata, y **cómo volver a medirlo** |
| [`03-pendientes-y-contratos.md`](03-pendientes-y-contratos.md) | Lo que **no se toca** (contratos externos), las **precondiciones** antes de pasárselo a Claude Code en la web, y las **decisiones abiertas** |

## Para quien implemente (Claude Code en la web)

Este directorio está aquí, y no en la documentación del ecosistema, porque **quien lo va a
implementar solo ve este repo**. Todo lo necesario para entender la tarea debe estar en
`documentations/` de este repo. Si algo de aquí remite a un documento de fuera, se cita el dato,
no solo el enlace.

Lee antes [`../constitucion.md`](../constitucion.md) (invariantes y trampas) y
[`../arquitectura.md`](../arquitectura.md) (los dominios y dónde se rompen las fronteras). Este
análisis **se apoya** en ellos y no los repite.
