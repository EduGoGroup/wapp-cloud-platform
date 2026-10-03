# F1-05 · La parada del piloto · 🧑 Jhoan

> ✅ **Resuelta el 2026-10-03.** Jhoan contestó P1–P7: [`../DECISIONES.md`](../DECISIONES.md) §3 y la §10 de
> [`../F1-nucleo-contact/informe-piloto.md`](../F1-nucleo-contact/informe-piloto.md). La norma ya lo recoge
> (`05` E-12, §4.2, E-9, E-4) y la **recalibración** de las specs F2–F10 y de estas sesiones está hecha
> (2026-10-03). Esta ficha queda como registro.

## Qué se decidió (en una línea cada una)

| | Decisión |
|---|---|
| P1 | Tres niveles de ceremonia (simple / medio / complejo), clasificados en el **inventario** de cada fase y aprobados por Jhoan |
| P2 | El umbral de cobertura por fichero (D-12) **deja de bloquear**: es un informe. Lo sustituyen un test por promesa del contrato, los mutantes del nivel complejo y los procesos de F9 |
| P3 | Sesiones de tamaño medio (45–90 min) y cierre fijo de tres cosas: tareas `[x]` con SHA, bloque en `ESTADO.md`, hallazgos en el README de la fase |
| P4 | La suite con `Montaje` y el arnés, para todo puerto con BD |
| P5 | El adaptador `bridge_<x>.go` es el mecanismo estándar; un módulo entra en `Conmutados` cuando muere su último adaptador |
| P6 | El test de un auxiliar no exportado nace en el verde, solo si lleva regla de negocio |
| P7 | `04`, `05`, las skills y el `CLAUDE.md` corregidos |

## Qué sigue

1. [`F1-06-cli-ajustes-previos-a-f2.md`](F1-06-cli-ajustes-previos-a-f2.md): la sesión de **código** que deja
   los candados y los tests de F1 como piden P2, P4 y P5. Sin ella no arranca F9-B ni F2.
2. Después, la tabla de [`README.md`](README.md), en orden.
