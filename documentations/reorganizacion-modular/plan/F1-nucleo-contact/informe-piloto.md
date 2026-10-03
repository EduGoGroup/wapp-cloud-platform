# Informe del piloto F1 · nucleo/contact

> Escrito por la sesión **F1-04** (💻 CLI, 2026-10-02) sobre `dev` @ `ddcf7de` (PR #19, #23, #24 y #25 integrados
> sin squash). Plantilla: [`tareas.md`](tareas.md) §Informe. Cada número lleva su fuente; lo no medido dice «sin medir».
> Toolchain de todas las corridas: `make toolchain` → `TOOLCHAIN=OK` · `GO_EFFECTIVE=go1.26.5` ·
> `LINT_EFFECTIVE=v2.12.2 .bin/golangci-lint`. Docker Desktop 29.8.0. Ningún `rc` se leyó con pipe.

## 0 · En cinco líneas

1. `internal/nucleo/contact` está en verde (4 ficheros, 0 `pendiente.Implementar`, 0 SKIP) y `cmd/server-modular`
   lo usa a través de `internal/arranque/bridge_contact.go`; `cmd/server` no cambió ni un byte.
2. La suite de contrato (19 casos) pasa en memoria y **contra Postgres con los dos binarios** (rc=0 · 20 PASS · 0 SKIP),
   y `make test-procesos` da `RC=0 · 196 PASS · 0 FAIL · 0 SKIP` en viejo y nuevo. Cero divergencias memoria ↔ Postgres.
3. Coste medido: **267 min** sumando las líneas `Piloto:` de F1-01…F1-03 y la revisión (≈ 252 de pared), de los que
   **≈ 94 min** son rojo + verde + refactor de los 4 ficheros de producción (≈ 23,5 min/fichero). El resto es método:
   decisiones, candados y documentación.
4. Los candados cazaron **dos defectos del propio andamiaje** (la exención por sufijo `test` y el `grep` ciego de
   R9.4.d) y ningún defecto del código de `contact`; F1-04 encontró **tres huecos de vigilancia** con mutantes que
   sobreviven (§3, §7) y uno de regla (R9.4.d mira el paquete, no el fichero).
5. Extrapolar es arriesgado: `contact` es una hoja de 4 ficheros sin estado. Con 23,5 min/fichero F2–F8 salen
   **≈ 101 h**; con el coste completo del piloto (67 min/fichero), **≈ 288 h**. La horquilla es la pregunta P1.

## 1 · Coste por fichero

Fuentes: líneas `Piloto:` de los commits (`git log 77df20f..ddcf7de`), `git show <sha-rojo>:<f> | wc -l`, y para el
final `wc -l`, `grep -cE '^\s*//'` y vacías (código = total − comentario − vacías).

| Fichero | Sesiones | Min. rojo | Min. verde | Commits | Líneas contrato (rojo) | Líneas finales (coment./código) | Líneas de test |
|---|---|---|---|---|---|---|---|
| `contact.go` | F1-01, F1-02 | 10 (`b37a8c8`, compartido con `resolver.go`) | 5 (`9e8f740`) | 3 | 112 (80 coment.) | 207 (102 / 94) | 354 |
| `resolver.go` | F1-01, revisión, F1-02 | 0 propios + 1 (`d915d41`, `b001c35`) + parte de `01ae55a`/`73eb2a5` | 12 (`8e7a891`) | 6 | 131 (99) · 147 antes del verde | 250 (145 / 92) | 305 |
| `repository_memory.go` | F1-01, revisión, F1-02 | 7 (`89b223b`) + parte de `01ae55a` | 4 (`222c4c8`) | 4 | 107 (85) | 275 (124 / 134) | 359 |
| `repository_postgres.go` | F1-01, revisión, F1-02 | 5 (`32b7bfb`) + parte de `73eb2a5` | 8 (`8307afb`) | 4 | 199 (175) · 221 antes del verde | 618 (317 / 279) | 434 |
| `contacthelpertest/` (10 ficheros: suite + `estado.go`) | F1-01, F1-02 | 15 (`8f2a4db`) + 10 partición (`7069532`, «aprox.») | — (nace completa) · refactor D-F1-7 6 (`4bbd138`) | 6 | 858 | 1.060 (292 / 646); `estado.go` 141 (43 / 87) | `estado_test.go` 252 |
| `arranque/bridge_contact.go` | F1-03 | 11 (`09f4b72`) | 2 (`0c2bddf`) + 3 refactor (`a62abea`) | 4 | 68 (43) | 131 (65 / 53) | 610 |

**Frente a la referencia vieja** (`internal/flujos/contact/`, recalculada hoy, sigue valiendo): 170 · 151 · 183 · 460
líneas, con 59 / 47 / 33 / 190 de comentario → código 99 / 92 / 134 / 254. El código nuevo es **casi idéntico**
(94 / 92 / 134 / 279: Postgres +25 por los auxiliares `openRows`/`closeRowsErr`); **todo el crecimiento es comentario**
(+43 / +98 / +91 / +127).

**Minutos por sesión** (suma de `Piloto:`): F1-01 **73** · revisión S9–S11 y decisiones **75** (+ 2 commits «sin medir»)
· F1-02 **58** por commit / **43** de pared (sub-agentes en paralelo) · F1-03 **61** · **total 267** (≈ 252 de pared).
F1-04 (esta sesión): ≈ 40 min de pared (22:4x → cierre), con tres sub-agentes en paralelo.

**Sin medir**: 2 commits de código sin `Piloto:` (`88b1d85`, `3ed9bd0`) y 3 dudosos de entorno (`0b78cd1`, `26cbfbf`,
`1e135e5`); el reparto por fichero de `01ae55a`, `73eb2a5` y `a18d4c0` (tocan varios); el tiempo de Jhoan en las
decisiones D-F1-7…16.

## 2 · Cobertura por fichero

`GOWORK=off make ci-local` (F1-04, `GATE_RC=0`, dentro va `cobertura-ficheros`): `FICHEROS_EVALUADOS=14 · POR_DEBAJO=0 ·
EXENTOS_POSTGRES=1`.

| Fichero | Obtenida | Base vieja | Nota |
|---|---|---|---|
| `contact.go` | **97,6 %** | 98,0 % | |
| `resolver.go` | **100,0 %** | 95,2 % | |
| `repository_memory.go` | **95,6 %** | 91,1 % | |
| `repository_postgres.go` | EXENTO (31,1 % unitario) | 0 % | **Con la suite contra Postgres: 80,7 %; unión 85,2 %** (115/135) — medido por F1-04 con `-coverpkg` |
| `contacthelpertest/estado.go` | **92,6 %** | — | medido desde D-F1-13 |
| `arranque/bridge_contact.go` | **100 %** (18/18) | — | a mano: `cobertura-ficheros` no mira `internal/arranque` (hallazgo 32, D-F1-16) |

**Minutos extra para llegar al 80 %**: ninguno registrado; los verdes llegaron por encima con los tests derivados del
contrato (sin medir aparte). **Lo que queda sin cubrir** en `repository_postgres.go` con todo junto: 20 sentencias, **todas
ramas `if err != nil { return … }`** (errores de consulta, scan, `WithTx`). `postgres.WithTx` queda al **36,6 %**: el
reintento por `40P01`/serialización **no se ejerce en ningún test de ejecución** del árbol nuevo ni de `platform`; solo el
viejo `deadlock_integration_test.go` (hallazgo 38).

## 3 · La suite de contrato

- **Casos**: 19 (tabla `casos()` de `contacthelpertest/contrato.go`, partida en 9 ficheros por `7069532`).
- **En memoria**: `TestMemoryResolver_Contrato` → PASS, 19 subtests, 0,00 s (`go test -race -count=1 -v`, F1-04).
- **Contra Postgres** (`test/procesos/contact_contrato_test.go`, `TestContactContrato_Postgres`), corrida que cuenta (T1.18):

  | Binario | rc | PASS / FAIL / SKIP | Padre | Paquete | Postgres listo | Plantilla migrada |
  |---|---|---|---|---|---|---|
  | `viejo` | 0 | 20 / 0 / 0 | 1,77 s | 14,9 s | 2,58 s | 0,65 s |
  | `nuevo` | 0 | 20 / 0 / 0 | 1,87 s | 10,7 s | 1,59 s | 0,57 s |

  (`WAPP_PROCESOS_BINARIO=<b> GOWORK=off go test -tags integracion -race -count=1 -v -run Contact ./test/procesos/`;
  `postgres:17-alpine`; el resto del tiempo del paquete es compilar `cmd/migrate` y el servidor.)
- **Divergencias memoria ↔ Postgres**: **ninguna** (19/19 en los dos). La web tampoco vio ninguna.
- **`make test-procesos`** (viejo y nuevo, el contrato dentro): `RC=0 · PASS=196 · FAIL=0 · SKIP=0` en los dos, 33 s.
- **¿Bastó la firma `Montaje` (D-F1-1)?** Sí para lo que observa, con una matización medida en F1-04: la marca de
  `Estado` (D-F1-7) vigila **solo `current_node`**. Dos mutantes de `fuseDB` que tocan `current_node` caen
  (`Fusion_ConflictoConservaElCanonico`); uno que en conflicto copia del huérfano `vars`, `last_wa_message_id`,
  `event_id` y `flow_version` **sobrevive** (rc=0, 20 PASS). `last_wa_message_id` es la marca de idempotencia
  (hallazgo 35).

## 4 · Candados

| Candado (`05` §5) | Veces que falló | Motivo | ¿Cazó un defecto real? | ¿Estorbó? |
|---|---|---|---|---|
| `un_fichero_un_test` | 1 | sonda E2 a propósito (`afa63f3`) | no (era la sonda) | no; punto ciego en `internal/arranque` (hallazgo 32) |
| `exportados_cubiertos` | 0 | casa por identificador: `var _ Resolver = …` no basta (hallazgo 11) | no | levemente: contradijo `diseno.md` §6 |
| Cobertura por fichero (D-12) | 1 (`68897a8`) | `contrato.go` al 0 %: la suite corre desde otro paquete | no (falso positivo) | **sí**: abrió D-F1-6 → D-F1-10 → D-F1-13 |
| Exención por sufijo `test` (los tres) | 0 | `latest`/`contest` quedaban exentos (hallazgo 21) | **sí, un defecto del propio candado** (cerrado por D-F1-10, 17 mutantes) | — |
| `fronteras` (regla 3) | 1 medido, no commiteado | con `nucleo` en `Conmutados`, 3 violaciones | no: choca con el adaptador | **sí**: `Conmutados` queda vacía (D-F1-15) |
| `sin_bd_viva` / R9.4.d | 0 | el `grep` de R9.4.d no veía nada (hallazgo 27) | **sí, un falso verde del gate** | — · F1-04: aún mira el paquete y no el fichero, y **no está en ningún gate** (hallazgos 36–37) |
| Huella | 0 | ciega a `contact` (T-9) | no aplica | no; la sustituye el test de cableado (hallazgo 31) |
| Lint `unused` (T-1) | 1 (`09f4b72`) | campo `next` sin leer en el rojo → `//nolint:unused` | no | levemente |
| Ciclo de imports (T-2) | 1 (`89b223b`) | `repository_memory_test.go` a `package contact_test` | no | levemente |
| Lint `errcheck` (fuera de la lista) | 2 (`8307afb`, `4bc398d`) | `Close()` sin comprobar; aserciones de tipo | defectos menores, corregidos antes del commit | no |
| `make test-pendiente` (fuera de la lista) | 1 | contó `.claude/worktrees` (hallazgo 28) | no (falso rojo) | **sí** — F1-04 lo evitó borrando los *worktrees* antes de medir |

## 5 · Puentes y adaptadores

- **Puentes de `05` §4.1**: **0** (lo esperado).
- **Adaptadores de arranque**: **1**, `internal/arranque/bridge_contact.go` — 131 líneas (65 coment. / 53 código), test
  de 610 líneas (adaptador, cableado y corpus de equivalencia de 109 casos), 100 % de cobertura. Vida: nace en
  `0c2bddf`/`ce98595`, muere cuando conmute el último consumidor viejo de `viejo.Resolver` (`flowruntime`, `intakes`):
  **F8** (D-F1-5, T8.32).
- **Coste**: 36 min de commits (11 + 2 + 3 + 20) más 25 de documentación del bloque C.
- **Lo que F1-04 midió del adaptador** (§7 del traspaso C):
  - Ningún camino del arranque nuevo construye el resolver sin `newContactResolver`. Los dos consumidores (`fase6`
    `intakes.NewNotifier`, `fase7` `flowruntime.New`) reciben `c.flowDeps.contacts`.
  - Pero un mutante que en `fase7_flujos.go` construye allí el `NewPostgresResolver` **viejo** sobrevive al test de
    cableado y al `grep` de la §3: ese test mira `flowDeps.contacts`, no lo que reciben las fases 6 y 7 (hallazgo 39).
  - La reflexión sobre campos privados de `nucleo/contact` falla ruidosamente si se renombran (aceptable). Hay una
    alternativa más limpia: una costura `var newPostgresContactResolver = contact.NewPostgresResolver` en `arranque` que el
    test sustituya por un espía (hallazgo 39).

## 6 · Reglas viejas

De las 38 reglas (`diseno.md` §4: R-01…R-33, N-01…N-05):

| Destino | Reglas | N.º |
|---|---|---|
| Portadas en F1 (tests de F1 o la suite) | R-01…R-22 (R-11 parcial), R-31, R-32, R-33 (como comentario), N-01…N-05 | 30 |
| Código en F1 + aserción de caja negra en F9 | R-23, R-24, R-26, R-27, R-28 | 5 |
| Solo en F9 | R-29 (P3), R-30 (`Rekey` es de `platform`) | 2 |
| No portada | R-25 (la migración 0007 es del runner de `platform`) | 1 |

Diferidas a P3 de F9 por D-F1-11 (`4b226c9`): R-27, R-28, R-29. **Nuevas descubiertas**: las promesas no fijadas del
hallazgo 14 (solo ASCII, ceros a la izquierda, corte del LID, longitud en bytes, orden de `RefsFrom`, desempate por id) y
las cinco fijadas en `01ae55a` (hallazgo 24). F1-04 añade: el reintento de `WithTx` no tiene test de ejecución en ningún
árbol (hallazgo 38), y P3/T9.15 tiene que medirlo antes de que F10 borre `deadlock_integration_test.go`.

## 7 · Conmutación y huella

- **Huella**: `TestHuella` y `TestHuellaEstatica` PASS dentro de `ci-local` (F1-04, `GATE_RC=0`).
- **`go list -deps`**: `./cmd/server-modular` → `internal/nucleo/contact` **1**; `./cmd/server` → `internal/nucleo/` **0**.
  Ojo: el 1 ya salía desde el rojo (hallazgo 31), así que solo prueba el enlace.
- **Test de cableado**: `TestBuildFlowRuntimeDeps_WiresContactBridgeWithPhaseKeys` PASS. Hueco en el hallazgo 39.
- **Código viejo**: `git diff --stat 77df20f..HEAD -- internal/flujos internal/bootstrap internal/gateway internal/intakes internal/publicapi cmd/server`
  vacío.
- **Arranque real** de `cmd/server-modular` (F1-04, como T0.23):
  - Entorno: `postgres:16` efímero con base vacía, `HeadBucket` real contra el R2 de desarrollo de `.env` (sin imprimir
    credenciales) y `WAPP_KEK_PROVIDER=env` con claves generadas al vuelo.
  - Resultado: **9/9** `arranque: fase completada` (fase 1, migraciones `0.48.0`, 363 ms; fase 3, almacenes con
    `HeadBucket`, 479 ms), `:8100/healthz` **200**, 0 líneas `ERROR`/`WARN`; SIGINT → `servidor detenido limpiamente`, `EXIT=0`.
  - **No corrido**: `WAPP_KEK_PROVIDER=kms` (no hay KMS en local).
- **Equivalencia de normalización viejo ↔ nuevo** (§7.3 del traspaso C):
  - El código es **idéntico token a token** salvo el tipo `Contact`, que no se portó (D-F1-4).
  - Prueba diferencial de F1-04, sin diferencias:
    - tabla adversaria de 104 valores × 7 kinds (dígitos no ASCII, `@` repetidos, espacios Unicode, `İ`, UTF-8 inválido…);
    - 75.712 tripletas de `RefsFrom`;
    - *fuzz* de 60 s por función, con **3.370.994 ejecuciones** en total.
  - Pero el corpus a mano de 109 casos **no caza** un mutante realista (`IndexByte` → `LastIndexByte` en
    `normalizeLID`). La equivalencia se sostiene hoy por la identidad del código, no por el corpus (hallazgo 40).
  - En el arranque nuevo, quien normaliza en producción sigue siendo el código viejo (`runtime`, `gateway`), así que
    `value_bidx`, `self_pn_bidx` y el anti-self-loop no pueden cambiar por este bloque.

## 8 · Extrapolación (con su advertencia)

Ficheros de producción por fase según `04` §3 (cuenta: entradas `*.go` que no son `_test`, por sección de módulo):

| Fase | Módulo | Ficheros | × 23,5 min (solo rojo+verde+refactor) | × 67 min (coste completo de F1 / 4) |
|---|---|---|---|---|
| F2 | acceso | 51 | 20 h | 57 h |
| F3 | edge | 38 | 15 h | 42 h |
| F4 | inferencia | 10 | 4 h | 11 h |
| F5 | catalogo | 10 | 4 h | 11 h |
| F6 | solicitudes | 41 | 16 h | 46 h |
| F7 | captacion | 32 | 13 h | 36 h |
| F8 | conversacion | 76 (`runtime`: 23) | 30 h | 85 h |
| **F2–F8** | | **258** | **≈ 101 h** | **≈ 288 h** |

No incluye los `bridge_<x>.go` (fuera del árbol de `04`), `apipublica` (FX) ni F9.

⚠️ **Advertencia**:
- `contact` es una hoja de 4 ficheros **sin estado**, con un código viejo bien testado (98 / 95 / 91 %) y SQL que se copió
  byte a byte.
- `runtime` (F8) tiene 23 ficheros y estado en memoria, y `acceso` (F2) consume muchos paquetes viejos, de modo que tendrá
  más adaptadores.
- Una buena parte de los 267 min fue **coste de arranque del método**: candados, D-F1-6/10/13 y la revisión S9–S11, que no
  debería repetirse.
- La cifra baja supone que el método ya no cambia; la alta, que cada fase paga lo mismo que el piloto.

## 9 · Preguntas de la parada

| # | Pregunta | Dato del informe |
|---|---|---|
| P1 | ¿**Seguir igual**, **acelerar** (contrato+rojo+verde de un paquete en una pasada; varios paquetes por sesión) o **acotar** (reconstruir solo los módulos con deuda y portar el resto con menos ceremonia)? | §1: el código nuevo es casi idéntico al viejo (+0 líneas de lógica salvo Postgres) y todo el crecimiento es comentario; ≈ 35 % del coste fue rojo+verde. §8: 101–288 h |
| P2 | D-12: ¿el 80 % por fichero se mantiene, sube, baja o pasa a otra medida? | §2: los tres ficheros llegaron por encima sin esfuerzo extra; la exención Postgres esconde un 80,7 % real con la suite. ¿Medir los adaptadores Postgres con la suite de procesos en vez de eximirlos? |
| P3 | ¿Bloques de sesión más grandes (p. ej. un módulo pequeño entero por sesión)? | §1: F1-02 hizo 4 verdes + suite en 43 min de pared; F1-03 el adaptador en 61 |
| P4 | ¿La suite con `Montaje` (D-F1-1) y el arnés adelantado (D-F1-2) se adoptan para F2–F8? | §3: 19/19 en memoria y Postgres, cero divergencias, ≈ 11–15 s por corrida; pero la marca de `Estado` vigila una sola columna (hallazgo 35) |
| P5 | ¿El adaptador de tipos en el arranque (D-F1-5) es el mecanismo estándar? Coste previsto en F2 (`acceso`) | §5: 36 min y 131 líneas para un puerto de 2 métodos; choca con `Conmutados` (D-F1-15) y queda fuera de los candados (D-F1-16); su test de cableado tiene el hueco del hallazgo 39 |
| P6 | ¿Se acepta que los tests de auxiliares no exportados nazcan en el `verde` (T-1) como excepción escrita a E-4? | §4: T-1 mordió 1 vez, levemente |
| P7 | ¿Qué de las contradicciones del `README.md` de F1 se corrige en `04`/`05`? | README (hallazgos 1–41) · §4. Abiertas: D-F1-14, D-F1-15, D-F1-16, resto de D-F1-9 |

## 10 · Decisión de Jhoan (fecha)

*Pendiente (T1.20).* Hasta que Jhoan conteste P1–P7 aquí, con fecha, **no empieza F2** (ni F9-B).
