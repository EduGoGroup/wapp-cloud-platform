# F1-06 · Ajustes de código previos a F2 · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F1 · nucleo/contact](../F1-nucleo-contact/README.md) · ajustes tras la parada (sesión **completa**, de código) |
| Entorno | 💻 solo local: toca `Makefile` e `internal/candados`, y necesita Postgres (testcontainers) para la marca de `Estado` |
| Nivel (E-12) | Complejo: son candados y la suite de un puerto con BD; cada ajuste se prueba con el mutante que hoy sobrevive |
| Duración objetivo | 45–90 min. Si no cabe, se corta tras un ajuste cerrado y se relanza |
| Tareas | Los seis ajustes de abajo (A1–A6). Vienen de [`../DECISIONES.md`](../DECISIONES.md) §3 (P2, P4, P5) y de los hallazgos 35, 36, 37, 39 y 40 del [README de F1](../F1-nucleo-contact/README.md) |
| Depende de | F1-05 (parada resuelta) y la recalibración de las specs (hecha el 2026-10-03) |
| Decisiones | Ninguna pendiente. Si un ajuste obliga a elegir entre dos remedios, se elige el más barato que haga caer el mutante y se anota |
| Se para cuando | Los seis ajustes están commiteados, sus mutantes **caen**, `make ci-local` da rc=0 con **0 SKIP** en código nuevo y la suite de `contact` pasa en memoria y en Postgres |

## Los seis ajustes

| | Ajuste | Dónde (verifícalo: es la foto del 2026-10-03) | Hecho cuando |
|---|---|---|---|
| A1 | `make cobertura-ficheros` pasa de **gate a informe** (P2): sigue imprimiendo la tabla y `POR_DEBAJO`, pero **no** sale con rc≠0 por un fichero bajo; ya no hay exentos por umbral | `Makefile` (target y su comentario, `ci-local`) · `internal/candados/cobertura.go` y su test · `cmd/cobertura-ficheros` | Un fichero por debajo del antiguo umbral no hace fallar `ci-local`; un error real (no compila, `go list` falla) sí |
| A2 | Los candados de fichero **incluyen `internal/arranque/bridge_*.go`, y solo esos** (P5, cierra D-F1-16): `un_fichero_un_test` y el informe de cobertura | `internal/candados/unfichero.go`, `cobertura.go` · `COBERTURA_DIRS` del `Makefile` | Borrar `bridge_contact_test.go` hace fallar `un_fichero_un_test`; `bridge_contact.go` sale en el informe; el resto de `internal/arranque` sigue fuera |
| A3 | `Conmutados`: un módulo entra **cuando muere su último adaptador**, no al conmutar (P5, cierra D-F1-15). Comentario corregido y, si se puede barato, el candado lo comprueba (un módulo en `Conmutados` con un `bridge_<x>.go` vivo que lo adapta → falla) | `internal/modulos/fronteras_test.go` · `internal/candados/fronteras.go` | El comentario dice la regla nueva; `nucleo/contact` **no** está en `Conmutados` (su `bridge_contact.go` vive hasta F8) |
| A4 | **Test de cableado completo** (hallazgo 39): además del campo del contenedor, que ninguna fase del arranque nuevo importe o construya el resolver viejo fuera del adaptador (grep por ruta de import `internal/flujos/contact` en `internal/arranque/*.go`, salvo `bridge_contact.go` y lo que la spec exima) y/o una costura con espía | `internal/arranque/bridge_contact_test.go` (y el test de cableado de `contact`) | El mutante del hallazgo 39 (pasar `contact.NewPostgresResolver` viejo a `flowruntime.New` en `fase7_flujos.go`) **cae** |
| A5 | **Marca de `Estado` más fuerte** (P4, hallazgo 35): vigila también `vars`, `last_wa_message_id`, `event_id` y `flow_version`, no solo `current_node` | `internal/nucleo/contact/contacthelpertest/` (`estado.go`, `fixtures_contrato.go`, `merge_contrato.go`) y el `postgresState` de `repository_postgres_test.go` | El tercer mutante de `fuseDB` del hallazgo 35 (copiar esas columnas del huérfano) **cae**, en memoria y en Postgres |
| A6 | **R9.4.d en un gate** (hallazgos 36–37) y **corpus adversario** (hallazgo 40) | R9.4.d: el comando de [`../F9-procesos/requisitos.md`](../F9-procesos/requisitos.md) pasa a un target o a `internal/candados`, dentro de `ci-local`; afínalo para que mire lo que la regla dice (el adaptador solo en las suites de contrato). Corpus: `TestContactEquivalence_*` en `internal/arranque/bridge_contact_test.go` con `@` repetidos (`a@@b`), dígitos no ASCII y espacios Unicode | La sonda `p2_sonda_test.go` del hallazgo 36 **sale**; el mutante `IndexByte` → `LastIndexByte` en `normalizeLID` **cae** |

Un commit por ajuste (`refactor(candados): …`, `refactor(contact): …`, `test(arranque): …`), más el de documentación.

## Antes de pegar el prompt (Jhoan)

- [ ] `dev` local al día con `origin/dev` y sin cambios sin commitear.
- [ ] Docker encendido en el Mac; `make toolchain` da `TOOLCHAIN=OK`.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F1-06 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI · ajustes de código previos a F2.

Antes de nada, lee ENTERO y sigue al pie de la letra:
1. documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md (es una sesión COMPLETA: escribes código)
2. documentations/reorganizacion-modular/plan/sesiones/F1-06-cli-ajustes-previos-a-f2.md (los seis ajustes A1–A6)
3. documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md: E-9, E-12 y §4.2
4. documentations/reorganizacion-modular/plan/F1-nucleo-contact/README.md: hallazgos 35, 36, 37, 39 y 40

Tu encargo (y solo este): los seis ajustes A1–A6 de la ficha, uno por commit.
- A1 cobertura-ficheros de gate a informe · A2 candados de fichero sobre bridge_*.go · A3 Conmutados cuando muere
  el último adaptador · A4 test de cableado completo · A5 marca de Estado más fuerte · A6 R9.4.d en un gate y
  corpus de equivalencia adversario.
- Método: para cada ajuste, PRIMERO reproduce el mutante o la sonda que hoy sobrevive (apúntalo con su rc),
  DESPUÉS arregla, y comprueba que ahora cae. Deshaz el mutante antes de commitear. Un ajuste sin su prueba de
  que el mutante cae no está hecho.
- No toques el código viejo (05 E-1) ni la lógica de producción de nucleo/contact: son tests, candados y Makefile.
- Las rutas de la ficha son la foto del 2026-10-03: verifícalas contra el código antes de fiarte.
- Te paras cuando: los seis ajustes están commiteados, sus mutantes caen, `make ci-local` da rc=0 (leído sin
  pipe) con 0 SKIP en código nuevo, y la suite de contact pasa en memoria y en Postgres (testcontainers).
- Skills: contrato-tdd, validar-antes-de-cerrar, procesos-testcontainers.

Orquesta con sub-agentes (uno por ajuste independiente; A1+A2 comparten ficheros, van juntos) y protege tu
contexto. Ojo con los worktrees de sub-agentes: nacen de origin/main; ponlos en el SHA de dev.
Idioma: nombres en inglés; comentarios, documentación, commits y mensajes de fallo de tests en español.

Al terminar, las tres cosas: (1) en plan/F1-nucleo-contact/tareas.md, una sección «Ajustes previos a F2» con
A1–A6 [x] y su SHA; (2) un bloque en ESTADO.md; (3) los hallazgos nuevos en el README de F1, y D-F1-15 y D-F1-16
marcadas como aplicadas en código. Actualiza también la skill validar-antes-de-cerrar si aún trata
cobertura-ficheros como gate. Luego `git push origin dev` leyendo el rc sin pipe. No toques main.
No empieces F9-B (F9-03) ni F2.
```

## Al terminar debe existir

- Seis commits de código (uno por ajuste) y uno de documentación, en `dev`, empujados.
- En [`../F1-nucleo-contact/tareas.md`](../F1-nucleo-contact/tareas.md): A1–A6 `[x]` con SHA.
- Un bloque en `ESTADO.md` de la reorganización.
- En el [README de F1](../F1-nucleo-contact/README.md): los hallazgos nuevos, y D-F1-15 / D-F1-16 aplicadas.

## Si algo sale mal

- Un mutante que **no** cae tras el arreglo: el ajuste no está hecho; se anota qué se probó y se para.
- Un ajuste obliga a tocar lógica de producción o el código viejo: **para y pregunta**.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt.
