# Plantilla de fase — cómo se escribe cada carpeta `Fn-*/` del plan

> El plan de la reconstrucción está escrito **a lo spec-driven** (el estilo de Kiro): cada fase es
> una *spec* con sus **requisitos** (historias de usuario y criterios de aceptación verificables),
> su **arquitectura**, su **diseño**, sus **reglas** y sus **tareas**. Este fichero fija la forma
> para que las once fases se lean igual. **La norma de fondo sigue siendo
> [`05-metodo-contratos-y-tdd.md`](../../05-metodo-contratos-y-tdd.md)**: si una fase choca con
> `05`, manda `05` y la fase se corrige.

## 1 · Los seis ficheros de cada fase

| Fichero | Qué contesta | Equivale en Kiro a |
|---|---|---|
| `README.md` | Estado, objetivo en tres líneas, **entradas** (qué tiene que ser cierto para empezar), **salidas** (qué es cierto al cerrar), orden de lectura, bloques de sesión y decisiones que necesita | — (portal) |
| `requisitos.md` | **Historias de usuario** y sus **criterios de aceptación** en formato EARS | `requirements.md` |
| `arquitectura.md` | La vista **macro**: paquetes viejos → nuevos, qué importa cada uno, **puentes** al código viejo (cuándo nacen y cuándo mueren), cableado en el arranque nuevo, estado en memoria y goroutines, rutas y rpc afectados, y **lo que no cambia** hacia fuera | `design.md` (parte de arquitectura) |
| `diseno.md` | La vista **micro**: por paquete, sus ficheros y el **contrato** de cada uno (exportados y lo que prometen), puertos y **suites de contrato**, dobles en memoria, tests viejos que hay que **leer** (E-8) y las reglas que se llevan al contrato, candados de invariante (`05` §3.2) que aterrizan aquí | `design.md` (parte de diseño) |
| `reglas.md` | Las reglas **de esta fase**: lo que no se toca, las trampas conocidas con `fichero:línea`, prohibiciones, y la **definición de hecho** | *steering* local |
| `tareas.md` | La lista ejecutable: tareas numeradas, con su entorno, dependencias, requisitos que cumple, ficheros, «hecho cuando», gate y commit. Agrupadas en **bloques de sesión** | `tasks.md` |

El marco común a todas las fases (producto, tecnología, estructura, flujo web ↔ local, glosario)
vive en [`00-marco/`](README.md) y **no se repite** en cada fase: se enlaza.

## 2 · `requisitos.md` — historias y criterios EARS

**Historia de usuario**, una por capacidad de la fase:

> **H1.2** · Como **<rol>**, quiero **<capacidad>**, para **<beneficio>**.

Roles válidos (no se inventan otros): **Jhoan** (dueño y arquitecto: decide rumbo y paradas) ·
**la sesión web** (implementa) · **la sesión local** (cierra: Docker, integración vieja, UAT,
`main`) · **la operación de UAT** (despliega y vigila) · **la dueña del negocio** (usuaria final
de la consola: no debe notar nada) · **el Edge** (el sistema del cliente, que habla por gRPC) ·
**el integrador CRM** (el sistema externo del contrato `intake.push`).

**Criterios de aceptación** bajo cada historia, en **EARS en español**, numerados `R1.2.a`,
`R1.2.b`…, y cada uno **verificable con un comando o un test que se nombra**:

| Patrón | Forma |
|---|---|
| Ubicuo | **EL** <componente> **DEBERÁ** <respuesta>. |
| Por evento | **CUANDO** <disparador>, **EL** <componente> **DEBERÁ** <respuesta>. |
| Por estado | **MIENTRAS** <estado>, **EL** <componente> **DEBERÁ** <respuesta>. |
| No deseado | **SI** <condición indeseada>, **ENTONCES EL** <componente> **DEBERÁ** <respuesta>. |
| Opcional | **DONDE** <característica>, **EL** <componente> **DEBERÁ** <respuesta>. |

Ejemplo: *R1.1.c · **SI** un test de `internal/nucleo/contact` llama a `t.Skip`, **ENTONCES EL**
gate **DEBERÁ** fallar. — Verifica: `grep -rn 't.Skip' internal/nucleo` vacío y el candado X.*

## 3 · `tareas.md` — el formato de una tarea

```markdown
- [ ] **T1.4 · rojo(nucleo): contrato y test de `contact/repository.go`** · 🌐 · dep. T1.2 · cumple R1.2.a, R1.2.c
  - **Ficheros**: `internal/nucleo/contact/repository.go`, `…/repository_test.go`
  - **Hecho cuando**: <condición observable, no «está hecho»>
  - **Gate**: `go vet -tags pendiente ./internal/nucleo/...; echo rc=$?` → `rc=0`
  - **Commit**: `rojo(nucleo): contrato de contact/repository`
```

- **IDs**: `T<fase>.<n>` (`T0.1`, `T10.3`, `TX.2` para la transversal). No se renumeran nunca:
  una tarea que sobra se tacha (`~~T1.7~~ — anulada: motivo`), una que falta se añade al final.
- **Estado**: `[ ]` pendiente · `[~]` empezada o cerrada a medias (dice qué falta) · `[x]` cerrada,
  **con el SHA** del commit que la cierra: `[x] … — cerrada en \`abc1234\``.
- **Entorno**: 🌐 la sesión web la hace entera · 💻 solo la local (Docker obligatorio sin
  alternativa, integración vieja con `WAPP_TEST_REQUIRE_DB=1`, UAT, `main`, ecosistema fuera del
  repo) · 🌐→💻 la web la escribe y la **local la cierra**.
- **«Hecho cuando»** es observable: un `rc`, un conteo, un fichero que existe, una huella igual.
  Prohibido «queda implementado».
- **Commits** (`05` E-4): `rojo(<m>): …` · `verde(<m>): …` · `refactor(<m>): …` ·
  `conmutar(<m>): …` · `andamiaje(f0): …` · `procesos(<proceso>): …` · `relevo: …` ·
  `docs(reorganizacion-modular): …` · `chore(deps): …` (cambios de `go.mod`/`go.sum`, **siempre
  aislados** en su propio commit). Un `rojo` y su `verde` **nunca** en el mismo commit.

**Bloques de sesión.** Las tareas se agrupan en bloques, cada uno lo que cabe en **una** sesión
de Claude Code (orientativo: hasta ~25 ficheros nuevos o una conmutación, y un solo entorno). El
bloque dice su entorno y su punto de parada:

```markdown
## Bloque B · contratos y rojo de `nucleo/contact` · 🌐 · T1.3–T1.7
Para cuando: `make test-pendiente` cuenta N y `make ci-local` rc=0.
```

La carpeta [`../sesiones/`](../sesiones/README.md) numera los bloques de todas las fases en
**sesiones** globales (`S01`, `S02`…) y da el prompt de arranque de cada una.

## 4 · Reglas de escritura (todas las fases)

1. **Autosuficiente para la web.** La sesión web **solo ve este repo**. Un dato que viva fuera
   (un ADR, la documentación del ecosistema, UAT) se **cita** — el dato, no solo el enlace:
   *«ADR-0007 (fuera de este repo): la DEK la custodia el cliente y nunca cruza el contrato»*.
2. **Medido, no recordado.** Todo número (ficheros, tests, rutas, exportados) lleva el comando
   con que se contó y la fecha, o `fichero:línea`. Regla del ecosistema: **se cuenta lo que se
   registra en ejecución, no las líneas que lo registran** — dilo cuando cuentes rutas.
3. **Rutas de hoy exactas.** Cada paquete viejo de referencia se nombra con su ruta real en
   `dev`; cada fichero nuevo, con su ruta en el árbol de [`04`](../../04-estructura-final.md) §3
   (salvo lo que cambió D-10: ver [`FX-cara-http/`](../FX-cara-http/README.md)).
4. **Nada que contradiga un ADR ni `05`.** Si la fase necesita algo que `05` no prevé, va a
   «Decisiones que necesita» del `README.md` de la fase, con recomendación. No se decide en
   silencio.
5. **Ni un secreto.** De una credencial se dice dónde vive, jamás cuál es.
6. **Denso, no inflado.** Tablas y listas antes que prosa. Español con tildes; los
   identificadores de código en su forma original.
