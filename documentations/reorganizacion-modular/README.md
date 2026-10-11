# Reorganización modular de `internal/` — portal

> ✎ **2026-10-11: F6, F7 y F8 cerradas** (2026-10-08, 2026-10-09 y 2026-10-11). Los ocho módulos están reconstruidos y
> conmutados en `cmd/server-modular` (`FaseActual = 8`, 0 adaptadores, 0 puentes de import, `Conmutados` completo).
> Quedan **F9-D** (el cierre de los procesos) y **F10** (el relevo). El párrafo de abajo es el resumen anterior.
>
> **Estado: EN EJECUCIÓN** (resumen al 2026-10-07; **el estado al día vive en [`ESTADO.md`](ESTADO.md)**,
> § «Avance de la ejecución», y este resumen no se mantiene sesión a sesión). **F0, F1, F2 y F3 cerradas** y en
> `dev`. **F4** (`inferencia`) y **F5** (`catalogo`) **cerradas** el 2026-10-07 (F45-03, por PR a `dev`), con
> `FaseActual = 5` y las dos fuera de `Conmutados` (`inferencia` hasta F8, `catalogo` hasta F7). **F9** adelantada en
> parte: bloques A y B hechos y la pasada por conmutación de `nucleo`, `acceso`, `edge`, `inferencia` y `catalogo`;
> falta el cierre (F9-D). **F6, F7, F8 y F10, sin
> empezar.** El análisis (01–06) está cerrado (2026-09-28)
> y el **plan de trabajo ejecutable** vive en [`plan/`](plan/README.md): una *spec* por fase
> (historias de usuario, arquitectura, diseño, reglas y tareas), el registro de decisiones y las
> **sesiones con su prompt** para Claude Code en la web y en local.

## Qué se pretende

Ordenar `internal/` por **módulos de negocio** en carpetas, para que la separación de
responsabilidades se lea en el árbol de directorios y no haya que reconstruirla leyendo 707
ficheros. Dos condiciones fijas:

1. **Hacia fuera no se rompe nada**: ni una ruta HTTP, ni un rpc, ni una tabla, ni una variable de
   entorno, ni un flag de CLI, ni un texto observable.
2. **Hacia dentro se permite mover y re-cablear**, siempre con los gates verdes.

## Veredicto en una línea

**Factible.** El análisis de [`01`](01-factibilidad.md) lo evaluó como un movimiento mecánico de
coste bajo-medio. 🔒 El **2026-09-27** Jhoan eligió otro método, más caro y más limpio: **no se
mueve nada, se reconstruye** cada fichero por **contrato → test en rojo → lógica en verde**,
aprovechando para limpiar los tests, con un **arranque paralelo** que permite ir de a poco. Las
reglas están en [`05-metodo-contratos-y-tdd.md`](05-metodo-contratos-y-tdd.md), y empiezan por un
**piloto con parada** (F1, `nucleo/contact`) que mide el coste real antes de seguir.

> 📍 **Para retomar el trabajo, empieza por [`ESTADO.md`](ESTADO.md)**: qué se hizo, qué está
> decidido, qué falta decidir y cuál es el siguiente paso.

## Índice

| Documento | Qué contesta |
|---|---|
| [`01-factibilidad.md`](01-factibilidad.md) | **Empieza aquí.** El veredicto, por qué es factible, qué lo encarece, las opciones y la recomendación |
| [`02-mapa-de-dependencias.md`](02-mapa-de-dependencias.md) | El grafo medido hoy, los paquetes que están en la carpeta equivocada, los ciclos que sobreviven a una agrupación candidata, y **cómo volver a medirlo** |
| [`03-pendientes-y-contratos.md`](03-pendientes-y-contratos.md) | Lo que **no se toca** (contratos externos), las **precondiciones** antes de pasárselo a Claude Code en la web, y las **decisiones abiertas** |
| [`05-metodo-contratos-y-tdd.md`](05-metodo-contratos-y-tdd.md) | 🔒 **Normativo.** Cómo se construye: contratos sin lógica, un test por fichero escrito **desde el contrato** para que el código nazca cubierto, rojo antes que verde, los candados, las fases F0–F10 y la integración **de cero, por proceso**. Los tests viejos se consultan, no se portan. **Manda sobre los anteriores** |
| [`06-entorno-web.md`](06-entorno-web.md) | Qué ve Claude Code en la web (y qué no), **las cinco skills del repo**, y el script de preparación del entorno (lint fijado) |
| [`plan/`](plan/README.md) | 🚀 **El plan de trabajo ejecutable**: marco común, una *spec* por fase (F0–F10 y la transversal FX, la cara HTTP nueva), [`DECISIONES.md`](plan/DECISIONES.md) y [`sesiones/`](plan/sesiones/README.md), con el orden y el prompt de cada sesión |
| [`04-estructura-final.md`](04-estructura-final.md) | **El acabado, visual**: el árbol completo de `cmd/` e `internal/` fichero a fichero, cómo se llega por **fases** con **dos arranques en paralelo**, y la tabla de correspondencia que usará el script |

## Para quien implemente (Claude Code en la web)

Este directorio está aquí, y no en la documentación del ecosistema, porque **quien lo va a
implementar solo ve este repo**. Todo lo necesario para entender la tarea debe estar en
`documentations/` de este repo. Si algo de aquí remite a un documento de fuera, se cita el dato,
no solo el enlace.

Lee antes [`../constitucion.md`](../constitucion.md) (invariantes y trampas) y
[`../arquitectura.md`](../arquitectura.md) (los dominios y dónde se rompen las fronteras). Este
análisis **se apoya** en ellos y no los repite.
