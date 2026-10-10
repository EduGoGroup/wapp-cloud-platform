# F8-06b · F8 · D-F7-9: cierre y sobre en un solo acto · 💻 CLI

> ✎ **Hecha el 2026-10-10** (rama `reorg/f8-06b-d-f7-9-cierre-y-sobre`, `a0144628` … `42a06574`): bloque F8-06b de [`ESTADO.md`](../../ESTADO.md) y
> hallazgos 52–59 del [README de F8](../F8-conversacion/README.md).

> ✎ **2026-10-10 (D-F8-13, Jhoan, tras F8-05)**: sesión nueva, para que el arreglo de D-F7-9 no se pierda. Va **después** de
> conmutar ([`F8-06`](F8-06-cli-cara-http-y-conmutar.md)) y **antes** del cierre ([`F8-07`](F8-07-cli-cierre.md)). Motivo:
> hallazgo 44 del [README de F8](../F8-conversacion/README.md) y D-F8-13 en [`../DECISIONES.md`](../DECISIONES.md).

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 6b · D-F7-9: cierre y sobre en un solo acto |
| Entorno | 💻 solo local |
| Nivel (E-12) | complejo (toca un puerto con BD y el cierre de ventana): rojo y verde, suite contra Postgres, mutantes |
| Duración objetivo | 45–90 min |
| Tareas | T8.39, T8.40 (✎ 2026-10-10, D-F8-13: el re-análisis entra como segunda tarea) |
| Depende de | F8-06 (el binario nuevo ya cablea el agregador y el compositor NUEVOS) |
| Decisiones | D-F8-13 (rellena) |
| Se para cuando | el agregador nuevo compone el literal ANTES de cerrar y cierra y guarda el sobre en **una sola sentencia**; un job de ventana nunca es visible para el worker en `pending` sin sobre (salvo el hilo vacío, que es legítimo); la suite `Contrato` de `intake` verde en memoria y en Postgres; el caso de P4 verde contra el binario nuevo; el job de **re-análisis nace ya con su sobre**, en una sentencia (T8.40); mutantes muertos; `make ci-local` rc=0 con 0 SKIP; PR a `dev` abierto desde la rama de la sesión |

## Qué se arregla (y qué no)

Hoy el agregador cierra la ventana (`CloseWindow`: `aggregating → pending`) y **después** compone y escribe el literal
(`ComposeAtFlush` → `PutSourceText`), en dos sentencias. Si el worker del pipeline reclama el job en ese hueco, lo encuentra
sin texto y lo deja `failed` sin reintento: el cliente pidió un presupuesto y no sale nada (D-F7-9; hallazgo 17 de F7;
hallazgos 1, 14, 35 y 44 de F8).

El arreglo decidido (D-F8-13): **el sobre primero, y cierre y sobre en un solo `UPDATE`**.

- El compositor separa «componer y cifrar» (en memoria) de «escribir».
- `intake.JobStore` gana **una operación nueva** que, en una sentencia, pasa la fila de `aggregating` a `pending` **y** le
  escribe las tres columnas del sobre. Una sentencia es atómica: sin transacción y **sin migración** (columnas que ya existen).
- El cierre del agregador (`closeWindow`, `aggregator_sweep.go`) usa esa operación.
- `PutSourceText` **no se toca**: lo sigue usando el re-análisis.

Lo que hay que cuidar, y va con su caso:

- **Un mensaje que entra entre componer y cerrar** dejaría el sobre sin él: la sentencia cierra solo si la ventana no cambió
  desde que se leyó; si cambió, no cierra y la recoge el siguiente barrido.
- **Hilo vacío** (cero mensajes): hoy cierra y deja el sobre a NULL, que es forma legítima. Se conserva.
- **Fallo al componer o cifrar**: hoy cierra y el job muere al reclamarlo. Con el orden nuevo hay que elegir (cerrar sin sobre
  como hoy, o no cerrar y reintentar en el siguiente barrido): **con duda manda el viejo**; si la sesión ve una mejora
  evidente, la propone y PARA a preguntar.
- **D-F9-10** (contexto cancelado → sin `ERROR`) sigue cumpliéndose en el camino nuevo.

✎ **2026-10-10 (Jhoan): el re-análisis entra en esta sesión, como segunda tarea (T8.40).** Tiene el mismo hueco
(`captacion/reanalisis/reanalisis.go`: `OpenReanalysis` crea el job ya `pending` y `ComposeAtFlush` escribe después;
hallazgo 1): si el worker lo reclama en medio, el dueño pidió un re-análisis y no pasa nada. Mismo arreglo: componer y
cifrar primero, y que el job **nazca con su sobre** en una sola sentencia. Va **después** de T8.39, en su propio commit; si
la sesión se queda sin tiempo, para tras T8.39 verde y empujada y T8.40 se relanza. Si al nacer con sobre `PutSourceText`
se queda sin llamante de producción, se dice (no se borra de paso: PARA y pregunta).

**El código viejo no se toca**: el binario viejo conserva el fallo hasta el relevo de F10, así que el caso de P4
tiene que distinguir binario (o tolerar el viejo), como ya hacen los procesos con otras divergencias deliberadas.
✎ **2026-10-10 (Jhoan, al planificar la sesión)**: ningún proceso ramifica por binario y R9.8.b de F9 lo prohíbe; el caso
es una aserción igual para los dos, con el foco en el nuevo (el viejo está congelado y se borra al acabar el plan). Hallazgo 56.

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-06) está integrada y empujada en `dev`.
- [ ] Decisiones rellenas en [`../DECISIONES.md`](../DECISIONES.md): D-F8-13.
- [ ] Docker encendido en el Mac.
- [ ] `go1.26.5` y `golangci-lint v2.12.2` disponibles (`make toolchain`).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-06b del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque: 6b · D-F7-9: cierre y sobre en un solo acto
- Tareas: T8.39 y T8.40 de plan/F8-conversacion/tareas.md, en ese orden
- Lee ENTERA la sección «Qué se arregla (y qué no)» de plan/sesiones/F8-06b-cli-d-f7-9-cierre-y-sobre.md: es el diseño decidido.
- Te paras cuando: el agregador nuevo compone ANTES de cerrar y cierra y guarda el sobre en una sola sentencia; el job de re-análisis nace ya con su sobre en una sentencia (T8.40); la suite `Contrato` de `intake` verde en memoria y en Postgres; el caso de P4 verde contra el binario nuevo; mutantes muertos; `make ci-local` rc=0 con 0 SKIP; PR a `dev` abierto desde la rama de la sesión.
- Decisiones: D-F8-13 y D-F7-9 en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: contrato-tdd, procesos-testcontainers, validar-antes-de-cerrar, describir-pr.

- Base: F8-06 integrada en `dev` (el binario nuevo cablea el agregador y el compositor nuevos; `ls internal/arranque/bridge_*.go` → vacío): compruébalo y, si no está, PARA y dilo. Trabaja en TU rama partida de `origin/dev`, nunca en `dev` (regla 6 del `CLAUDE.md`).

Es una DIVERGENCIA DELIBERADA del viejo: va en su propio commit, con el comentario «Divergencia deliberada del viejo (D-F7-9, D-F8-13)» y su caso. Rojo antes que verde: primero el contrato de la operación nueva de `intake.JobStore` y su caso en la suite (`intakehelpertest`), en memoria y en Postgres; después el compositor y el cierre del agregador.
`PutSourceText` no se toca (lo usa el re-análisis). Ni una migración: las columnas ya existen. El código viejo no se toca.
Un mensaje que entra entre componer y cerrar: la sentencia cierra solo si la ventana no cambió; con su caso de carrera.
Hilo vacío: cierra con el sobre a NULL, como hoy. Fallo al componer o cifrar: con duda manda el viejo; si ves una mejora evidente, PARA y pregunta.
El re-análisis (mismo hueco) es T8.40: DESPUÉS de T8.39 verde y empujada, en su propio commit, con el mismo método (rojo antes que verde, caso en la suite de `intake`, mutantes, caso de proceso). Si no cabe, para tras T8.39 y dilo. Si `PutSourceText` se queda sin llamante de producción, dilo y PARA: no lo borres de paso.
Actualiza lo que el arreglo deja caducado: los comentarios de D-F7-9 en `aggregator_sweep.go`, `source_composer.go` y `captacion/pipeline/pipeline_chain.go`; `ComposeAtFlush_DoesNotOverwrite/window_not_pending` si cambia (hallazgo 35); el «Heredado» del README de F8.
Mutantes: quitar la guarda de «la ventana no cambió»; cerrar sin sobre; escribir el sobre sin cerrar; volver al orden viejo. Cada uno mata al menos un test. Los mutantes, en serie o sobre una copia.
Caso de P4 (F9): con `make test-procesos` contra el binario NUEVO, ningún job de ventana llega a `failed` por «el job no trae literal que analizar»; contra el viejo, el caso lo tolera o no aplica (dilo). Gates sin carga: no corras `make test-procesos` en paralelo con nada.
Reloj inyectado; prohibido `time.Sleep` y `t.Skip`; ningún `//nolint` nuevo; nada de `go ... -mod=mod`. Nombres en inglés en lo nuevo (E-11); ≤ 500 líneas por `.go` (E-13).

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. Push de TU rama (`git push origin <rama>`, rc sin pipe) y PR a `dev` (`gh pr create --base dev`; «integrar SIN squash»): nunca directo a `dev` (regla 6 del `CLAUDE.md`). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.39 y T8.40 `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md), y D-F7-9 marcada como **arreglada** en «Heredado de otras fases».
- Un PR con `--base dev` desde la rama de la sesión, con «integrar SIN squash».

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Si el arreglo pide una columna o una migración: **PARA** (regla de F8: ni una columna nueva).
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt.
