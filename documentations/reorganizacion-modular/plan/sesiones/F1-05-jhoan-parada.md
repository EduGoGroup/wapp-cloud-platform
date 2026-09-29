# F1-05 · La parada del piloto · 🧑 Jhoan

> **F1 es un piloto con parada** (`05` §6 y §9.1). Tras F1-04 existe
> `plan/F1-nucleo-contact/informe-piloto.md` con el coste real por fichero, la cobertura, la fricción
> de cada candado y la extrapolación. **Nada de F2 en adelante (ni F9-03) arranca sin esta decisión.**

## Qué hacer

1. Leer `../F1-nucleo-contact/informe-piloto.md` entero.
2. Contestar las preguntas **P1–P7** de su §9 (seguir igual / acelerar / acotar; recalibrar el 80 %
   de D-12; tamaño de bloque; si algún candado estorba…). La plantilla está en
   [`../F1-nucleo-contact/tareas.md`](../F1-nucleo-contact/tareas.md) §Informe.
3. Escribir la decisión en el §10 del informe y en [`../DECISIONES.md`](../DECISIONES.md) §3, con
   fecha.
4. Rellenar las decisiones de §5 de `DECISIONES.md` que bloquean F2 (y las de F9 §4 si faltan).

## Si la decisión cambia el método

Si se acelera, se acota o se recalibra algo, las specs de F2–F10 y estas sesiones tienen que
ajustarse **antes** de seguir. Arranca una sesión CLI con este prompt:

```text
Sesión de recalibración del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.
Lee documentations/reorganizacion-modular/plan/F1-nucleo-contact/informe-piloto.md (§9 y §10: la
decisión de Jhoan) y plan/DECISIONES.md §3. Aplica esa decisión a las specs de F2–F10 y a
plan/sesiones/ (tamaño de bloques, umbral de cobertura, candados, orden), orquestando un sub-agente
por fase. No toques código. Commit `docs(reorganizacion-modular): recalibración tras el piloto` a
dev y push, leyendo el rc sin pipe. Actualiza ESTADO.md.
```
