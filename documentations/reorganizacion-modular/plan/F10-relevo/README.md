# F10 · Relevo — `cmd/server` pasa al arranque nuevo y lo viejo se borra

> **Estado**: 🔧 **T10.26 hecha (F10-05c, 2026-10-12), PR #86 abierto, en revisión**; queda T10.25. **Bloque E, T10.18–T10.21, hecho (F10-05b, 2026-10-12, PR #85 integrado)**. **bloque A cerrado (F10-02, 2026-10-11, PR #75 integrado)**: verdad de campo limpia, dorada de la
> huella congelada, acta abierta e inventario de divergencias. **Bloque B cerrado (F10-03, 2026-10-11, PR #81 integrado)**:
> el modular corre en UAT, la prueba funcional pasó en local, sin ventana de 24 h (Jhoan); veredicto: «sigue». **T10.14 hecha (F10-04b, 2026-10-11, PR #84 integrado): bloque C cerrado entero**. **Bloque C integrado, salvo T10.14 (F10-04, 2026-10-11, PR #82 integrado)**: el relevo de código está hecho (T10.7–T10.13 y T10.23); T10.14, la documentación del repo, va en un PR propio (F10-04b). Spec escrita el
> 2026-09-28 sobre `dev` @ `1b18932`.
> Recalibrado el 2026-10-03 tras la parada de F1 (`05` E-12, §4.2, E-9, E-4; `plan/DECISIONES.md` §3).
> **Norma**: [`05`](../../05-metodo-contratos-y-tdd.md) §6 (fila F10), §4.1 (cero puentes de import), §4.2 (cero
> adaptadores `bridge_*.go`, `Conmutados` completo), §5
> (`no_pending_test`; antes `sin_pendientes_test.go`, D-F1-12). Forma: [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).
> Decisiones que la fundan: **D-9** (una prueba en UAT en sustitución antes del relevo; el despliegue
> `go build -o bin/server ./cmd/server` no cambia) y **D-10** (la cara vieja `internal/publicapi` se
> borra aquí: [`FX-cara-http`](../FX-cara-http/README.md) TX.25).

## Objetivo en tres líneas

Probar `cmd/server-modular` **en sustitución** del viejo en UAT, una vez, con marcha atrás en minutos.
Después, `cmd/server` pasa a llamar a `internal/arranque`, se borran `cmd/server-modular`,
`internal/bootstrap`, `internal/publicapi` y los otros 26 directorios viejos de primer nivel **con sus tests**, y el repo
queda con **cero adaptadores `bridge_*.go`, cero puentes (import), `Conmutados` completo, cero pendientes** y un
solo arranque. Lo de fuera del repo (doc del ecosistema, ADR-0010, comentarios en repos hermanos) lo cierra F10-05.

F10 **no reconstruye un módulo**: no lleva inventario E-12, y no crea ni retira adaptadores (exige que ya no quede
ninguno). Sin umbral de cobertura (P2): `make cobertura-ficheros` es un informe.

## Entradas (tiene que ser cierto para empezar)

| Condición | Cómo se comprueba |
|---|---|
| **F9 cerrada** (bloque `F9 CERRADA <fecha>` en `ESTADO.md`, sesión F9-05): procesos `RC=0` ×3 contra viejo **y** nuevo | Los dos logs de `CUENTA=3 make test-procesos` |
| F8 conmutada; **cero adaptadores** `bridge_*.go` en `internal/arranque` (`05` §4.2) | `ls internal/arranque/bridge_*.go 2>/dev/null \| wc -l` → 0 |
| **Cero puentes (import)**: la lista `Puentes` de `internal/modulos/fronteras_test.go` **vacía** (`05` §4.1) | Leer la lista; `GOWORK=off go test ./internal/modulos/` rc=0 |
| **`Conmutados` completo**: están todos los módulos de F1–F8 (cada uno entra cuando muere su último adaptador) | Leer `Conmutados` en el mismo fichero |
| T9.15 (P3) ejerce el reintento de `postgres.WithTx` y hace caer el mutante `maxTxAttempts = 1` (hallazgo 38 de F1) | El resultado del mutante, escrito por F9; T10.1 lo copia al acta. Si no cae, **no se borra** `deadlock_integration_test.go` |
| `pendiente.Implementar` = 0 en todo el árbol | `grep -rn 'pendiente.Implementar' --include='*.go' internal cmd \| wc -l` → 0 |
| La huella del nuevo es igual a la del viejo (F0, `internal/arranque/huella_test.go` contra `internal/bootstrap/arranque/huella_vieja_test.go`, D-F0-2) | `make ci-local` GATE_RC=0 |
| La cara vieja no sirve **ninguna** ruta en el binario nuevo (FX, cierre de F8) | Test del estrangulador de FX en verde |
| `make test-integration` (batería vieja) verde, por última vez, sobre el `dev` de entrada | `make test-integration; echo rc=$?` → 0, SKIP contados con `-v` |
| Jhoan da el visto bueno a la ventana de UAT (D-F10-1) | Fecha y hora escritas en «Decisiones». ✎ 2026-10-11: la fecha es **relativa** (al cerrar FW; D-F10-1 en `DECISIONES.md` §6); el día y la hora reales los escribe F10-03 en el acta |
| ✎ 2026-10-11 · **Solo para el bloque B**: la fase [FW](../FW-whatsapp-enchufable/README.md) cerrada (el Edge de UAT puede conversar con WhatsApp falso). Los bloques de antes (F10-01, F10-02) no la esperan | Bloque «FW cerrada» en `ESTADO.md`; TW.10 `[x]` |

## Salidas (es cierto al cerrar)

- UAT corrió `cmd/server-modular` en sustitución durante la ventana acordada, con los criterios de
  [`diseno.md`](diseno.md) §2.4 verdes; hay acta (`traspasos/TRASPASO-F10-relevo.md`).
- `cmd/server/main.go` llama a `internal/arranque`; **no existen** `cmd/server-modular/`,
  `internal/bootstrap/`, `internal/publicapi/` ni los 26 directorios viejos restantes
  ([`diseno.md`](diseno.md) §1).
- `internal/` contiene solo `arranque/`, `apipublica/`, `candados/` (✎ 2026-10-11, D-F10-10: se queda, sin la
  parte de «pendiente»), `modulos/`, `nucleo/`, `platform/` (y, según D-F10-3, nada de `pendiente/`).
- `no_pending_test.go` activo; `fronteras_test.go` con **cero** puentes (import) y `Conmutados` completo;
  **cero** `bridge_*.go` en `internal/arranque`; `huella_test.go` compara contra una **dorada** congelada del viejo.
- `make test-procesos` corre contra `cmd/server` (ya nuevo) y `WAPP_PROCESOS_BINARIO` desaparece.
- Según D-F9-4/D-F10-5: `make test-integration`, `WAPP_TEST_DB_DSN` y `WAPP_TEST_REQUIRE_DB` fuera del
  repo, y el job `integration` de `.github/workflows/ci.yml` corre los procesos.
- UAT desplegado desde el commit del relevo con el procedimiento de siempre.
- La documentación del repo y la del ecosistema dicen las rutas nuevas.
- `main` **no** se ha movido salvo petición expresa de Jhoan.

## Orden de lectura

[`requisitos.md`](requisitos.md) (H10.1–H10.7) → [`arquitectura.md`](arquitectura.md) (qué se borra, el
orden de los commits, lo que no cambia) → [`diseno.md`](diseno.md) (inventario medido, la prueba de UAT
paso a paso, lo de fuera del repo) → [`reglas.md`](reglas.md) → [`tareas.md`](tareas.md).

## Bloques de sesión

**Todo F10 es local (💻)**: ya no hay sesión web, ni PR, ni traspaso (solo si una sesión se corta). Cada sesión
commitea directo en `dev`, dura **45–90 min** y cierra con tres cosas: tareas `[x]` con SHA, un bloque en `ESTADO.md`
y los hallazgos nuevos en este README. `traspasos/TRASPASO-F10-relevo.md` conserva el nombre, pero aquí es el **acta** del
relevo (verdad de campo, prueba de UAT, cierre), no un traspaso.

| Sesión | Bloque | Tareas | Necesita | Para cuando |
|---|---|---|---|---|
| F10-01 🧑 | — | — | — | D-F10-1, D-F10-2 y D-F10-6 decididas; fecha de la ventana de UAT |
| F10-02 | **A · preparación y dorada** | T10.1–T10.3, T10.24 | — | Dorada de la huella commiteada y verificada contra el viejo; SHA a desplegar escrito en el acta; ✎ 2026-10-11: el inventario de divergencias visibles desde fuera (D-36, primera mitad) |
| F10-03 | **B · la prueba en UAT en sustitución** | T10.4–T10.6 | **UAT (SSH)** y ✎ 2026-10-11 **la fase [FW](../FW-whatsapp-enchufable/README.md) cerrada** (D-FW-3) | Acta de la ventana: veredicto «sigue» o «vuelta atrás», con evidencia. La ventana (≥ 24 h) es espera, no sesión: se abre (T10.4) y se relanza para cerrar (T10.5–T10.6) |
| F10-04 | **C · el relevo en el repo** | T10.7–T10.14, T10.23 | — | `ci-local` GATE_RC=0 con un solo arranque, cero adaptadores, cero puentes (import), `Conmutados` completo, cero pendientes, doc del repo al día; un commit por tarea |
| F10-05 | **D · cierre local** | T10.15–T10.17 | **Docker**, **UAT (SSH)** | `make test-procesos` RC=0 contra `cmd/server`; UAT desplegado desde el commit del relevo; acta `CERRADO` |
| F10-05b (✎ 2026-10-11: ficha propia) | **E · fuera del repo** | T10.18–T10.21 (✎ 2026-10-11, F10-05: T10.26 y T10.25 van después, cada una con su PR; D-F10-12, D-F10-14) | la raíz de wApp y los repos hermanos | Doc del ecosistema, ADR-0010, 12 comentarios hermanos, bóveda `analisis/` con las rutas nuevas |
| F10-06 | **F · `main`** | T10.22 | **`main`**, **Docker**, UAT (SSH) | **Solo a petición expresa de Jhoan** |

## Decisiones que necesita (Jhoan)

| # | Pregunta | Recomendación |
|---|---|---|
| **D-F10-1** | La prueba en UAT: ¿cuánto dura y quién la da por buena? | **24 h mínimo**, con al menos una jornada de tráfico real (el e2e con WhatsApp real del runbook del ecosistema `e2e-con-whatsapp-real.md`) y los criterios de [`diseno.md`](diseno.md) §2.4. Veredicto de Jhoan, por escrito en el acta. El tráfico real de UAT **no está medido**. ✎ 2026-10-11 (Jhoan): el e2e de la ventana **no** se hace con WhatsApp real sino con el **WhatsApp falso** de la fase [FW](../FW-whatsapp-enchufable/README.md) (receta de TW.9); la ventana de ≥ 24 h no cambia |
| **D-F10-2** | Si la prueba sale bien, ¿se deja el binario modular corriendo hasta el despliegue del relevo? | **Sí, si el relevo aterriza en ≤ 7 días** (es el mismo código que se va a desplegar). Si no, vuelta al viejo: no dejar en UAT un binario de una rama que ya no existirá |
| **D-F10-3** | ¿Qué se hace con `internal/pendiente`, la etiqueta `pendiente`, `make test-pendiente` y `vet -tags pendiente`? | **Retirarlos** en el mismo commit que activa `no_pending_test.go`, que entonces prohíbe el identificador **y** la etiqueta. `00-marco/estructura.md` dice «desaparece o queda vacía de usos»: vacía de usos y viva invita a reabrir el método sin decidirlo. Si Jhoan quiere el método para trabajo futuro, es una decisión nueva |
| **D-F10-4** | `cmd/server/integration_test.go` (en proceso, sin BD, `bufconn`) y `cmd/server/flows_integration_test.go` (con BD, importa `internal/flujos/**`) | **Portar** el primero contra los paquetes nuevos (`04` §3 lo marca ✎: es el único e2e del gRPC que corre en `ci-local` sin Docker). **Borrar** el segundo: su escenario lo cubre P3 |
| **D-F10-5** | Los 9 ficheros / 25 `Test*` con BD de `internal/platform/` (sobreviven: `platform` no se borra) | Depende de **D-F9-4**. Si hay P10: borrarlos, borrar `make test-integration` y dejar el repo sin `WAPP_TEST_DB_DSN`. Si no: `make test-integration` se queda acotado a `./internal/platform/...` y sube a `postgres:17-alpine` |
| **D-F10-6** | ¿Tag de versión tras pasar a `main`? (no hay `release.yml`; hoy `v0.1.0` y `v0.2.0`, `documentations/operacion.md` §4) | **`v0.3.0`**, sobre `main`, tras `ci-local` y `test-procesos` verdes, con `CHANGELOG.md` rellenado. Decide Jhoan |
| **D-F10-7** | La regla de conteo de ADR-0010 (fuera del repo) | Reescribirla: «módulo» = `internal/modulos/<m>` (segundo nivel), y `nucleo`, `platform`, `arranque`, `apipublica` como unidades propias. Texto propuesto en [`diseno.md`](diseno.md) §3.2; lo aprueba Jhoan porque es un ADR |

## Contradicciones encontradas (medidas el 2026-09-28)

1. **`03` §2.1: «5 comentarios de repos hermanos»**. Son **12** líneas de código Go en 7 repos que
   citan rutas de paquetes de este repo ([`diseno.md`](diseno.md) §3.3, comando incluido).
2. **`03` §2.1: «2.565 menciones de rutas `internal/` en 150 ficheros»**. Hoy, **desde la raíz de wApp** (la
   `documentations/` del ecosistema, fuera de este repo: solo lo mide la sesión 💻; desde la raíz
   del repo el mismo comando da 2.057 en 101, 2026-09-29):
   `grep -rno 'internal/' documentations --include='*.md' | wc -l` → **2.623** en **246** ficheros
   (incluye rutas `internal/` de **otros** repos); acotado a los 28 paquetes viejos de este repo,
   **1.124** en **170** ([`diseno.md`](diseno.md) §3.1).
3. **`05` §6, fila F10**, no nombra cosas que el relevo **tiene** que tocar: `cmd/casebank` y
   `cmd/prompts` importan paquetes viejos (`internal/casebank`, `internal/prompts`; medido con
   `go list -f '{{.Imports}}'`), los dos tests de `cmd/server`, la huella cuando ya no hay dos
   arranques, los tests con BD de `internal/platform`, el job `integration` de `ci.yml`
   (`postgres:16` + `WAPP_TEST_DB_DSN`) y la variable del arnés `WAPP_PROCESOS_BINARIO`.
4. **`04` §3**: `cmd/server` «(+ 1 _test.go)» además de `integration_test.go` ✎. Ese test es
   `flows_integration_test.go`, que importa `internal/flujos/**` y necesita BD: no puede sobrevivir
   tal cual (D-F10-4).
5. **`05` §7: los 107 ficheros de integración «se borran con él»**. Se borran los de los paquetes
   viejos; **9** de `internal/platform/` se quedan (D-F10-5).
6. **Documentación de UAT del ecosistema** (`documentations/operacion/despliegue-uat.md` §10) sitúa
   `/healthz` y `/metrics` en `internal/bootstrap/bootstrap.go:1426-1427`: hoy están en
   `internal/bootstrap/arranque/rutas_admin.go:64-65`, y tras el relevo en `internal/arranque/`.
   ADR-0010 cita `internal/bootstrap/bootstrap.go:1519`, que tampoco existe ya (`bootstrap.go` mide 42 líneas).

## Hallazgos de la ejecución

### F10-02 · bloque A (2026-10-11)

1. **Los `grep` de «cero pendientes» de la spec no dan 0 y no pueden darlo hasta T10.12.**
   `grep -rn 'pendiente.Implementar' --include='*.go' internal cmd | wc -l` da **24**: 9 en `internal/pendiente`, 11 en
   `internal/candados` (el detector, sus tests y su `testdata`) y 4 en un candado sobre el AST de `cart`. Ninguna es un
   contrato sin lógica. La cifra que vale es la de `make test-pendiente` (`PENDIENTES=0`, `ROJOS=0`). Afecta a la fila
   de «Entradas» de este README, a `reglas.md` §4 y a T10.12: sus `grep` solo darán 0 cuando se retiren
   `internal/pendiente` **y** las partes de `internal/candados` que lo nombran. **D-F10-3 no dice qué pasa con
   `internal/candados`** (91 ficheros de producción y 71 de test), que tampoco está en la lista de lo que queda en
   `internal/` según «Salidas». ✅ **Decidido el 2026-10-11 (D-F10-10, Jhoan)**: los candados se quedan, sin la parte de
   «pendiente»; el recorte va en T10.12.
2. **La dorada ya existía, pero a medias.** F0 dejó `internal/arranque/testdata/huella.json` con la parte de ejecución;
   la estática (goroutines y hooks) se comparaba leyendo la fuente del arranque viejo y se habría perdido en T10.10.
   T10.2 las junta en `huella-vieja.golden` (texto, una entrada por línea, como pide `diseno.md` §5) y retira el JSON
   y `huellatest.Leer`/`Escribir`. Las 216 entradas de ejecución son las mismas que escribió F0 el 2026-09-30: la
   reconstrucción no movió ni una ruta. Las specs de F0 y de F6 siguen nombrando `huella.json`: son historia y no se
   tocan.
3. **«El SHA que se desplegará» no se puede escribir en esta sesión.** La spec suponía commits directos en `dev`; con
   rama y PR (regla 6) el commit de `dev` es el del merge, y además F10-03 espera a FW, que añade commits. El acta
   (§4.1) fija el último commit de código de la rama y la condición que debe cumplir el `dev` que se despliegue;
   F10-03 escribe el SHA. Las cabeceras de `tareas.md` y la sección «Bloques de sesión» de este README siguen diciendo
   «sin PR» y «directo en `dev`»: contradicen la regla 6 y manda la regla.
4. **La integración vieja se corrió entera, por última vez**: `INTEG_RC=0`, 17.497 PASS, 0 FAIL, **0 SKIP**, con `-v` y
   `WAPP_TEST_REQUIRE_DB=1`. `make test-integration` no lleva `-v`, así que no sirve para contar SKIP: se corrió su
   procedimiento a mano.
5. **El inventario de divergencias (T10.24) se hizo leyendo, no ejecutando.** La tarea decía que «se corren los dos
   binarios»; no se corrió ninguno. Lo que quedó sin verificar está en `divergencias-visibles.md` §8. Tres cosas que
   salen de él: la frase «Divergencia deliberada» solo cubre 1 de las 11 visibles; V5 (un callback del CRM con un
   timestamp absurdo pasa de 200 a 401) no tiene decisión escrita; y un comentario de
   `internal/modulos/captacion/reanalisis/reanalisis.go:337-338` dice que un error temprano «no ha tocado una sola
   fila» cuando el texto pegado ya está guardado. ✅ **V5 decidida el 2026-10-11 (D-F10-11, Jhoan)**: se queda el 401.
6. **Las cifras de la spec que han cambiado** (todas en el acta §1): 498 tests viejos y no 497; 165 menciones de rutas
   viejas en la documentación del repo y no 147; 6 ficheros y 22 `Test*` con base de datos en `internal/platform` y no
   9 y 25; 7 directorios en `cmd/`.
7. **La fecha de la ventana de UAT no está escrita.** D-F10-1 decide la duración (≥ 24 h), pero la fila de «Entradas»
   pide fecha y hora. No bloquea el bloque A; la sesión F10-01 sigue «pendiente» en el índice de sesiones aunque sus
   tres decisiones (D-F10-1, 2 y 6) tienen respuesta desde el 2026-09-30. ✅ **Decidido el 2026-10-11 (Jhoan)**: la fecha es
   relativa, al cerrar FW; F10-03 escribe el día y la hora en el acta. F10-01 queda hecha.
8. **`make ci-docker` y `make test-procesos` no se corrieron en esta sesión.** Los procesos valen por F9-05 (el binario
   no cambia: solo tests y `testdata`).

### F10-03 · bloque B (2026-10-11)

9. **UAT no corría `dev`, sino `main` @ `9493cea`, del 2026-09-03**, y el Edge, `main` @ `d09d127`, sin el transporte
   falso. `diseno.md` §2 («UAT se sirve de `dev`») lo daba por hecho desde el 2026-08-30 y nadie lo re-midió. El
   «viejo» de UAT no es el «viejo» de `dev`: 31 ficheros de producción de diferencia en el código viejo, ninguna
   migración. Una comparación de textos contra él podía cortar la ventana por algo ajeno a la reconstrucción.
10. 🔴 **La marcha atrás escrita no funcionaba**: `cp <binario> bin/server` con el servicio arrancado da `Text file
    busy`. Se vio al instalar el modular (el reinicio levantó otra vez el viejo, sin daño). Corregido en `diseno.md`
    §2.5 y en el acta §4.5: copiar a `bin/server.new` y `mv -f`.
11. **La prueba cambió de forma por decisión de Jhoan** (acta §5, las tres citas): el modular entró en UAT sin correr
    antes el guion con el viejo; la conversación con el WhatsApp falso se hizo en local (`make test-procesos-edge-real`,
    viejo y nuevo `PASS=3`), no en UAT, para no pedir un token de la identidad compartida; y no hay ventana de 24 h
    porque UAT está suspendido. Lo que eso deja sin ver está en el acta §5.7; lo más grande, una inferencia real con
    Ollama pedida por el modular.
12. **La sesión del Edge de UAT es de WhatsApp real y está viva**, no caducada como decía la receta (§6, dato del
    2026-10-10). Las palancas no se echaron, así que no se la dejó muda.
13. **Tras un reinicio faltan familias de métricas, con el viejo y con el nuevo**: las mismas cuatro `CounterVec` en los
    dos. El `diff` de §2.3 · 6 no puede salir vacío sin tráfico.
14. **`make ci-docker` no se corrió.** `ci-local` sí: `GATE_RC=0` sobre `e82b6e92`, 0 SKIP.

### F10-04 · bloque C (2026-10-11)

15. **El orden de la spec no deja el árbol verde entre commits: T10.10 va antes que T10.9.** `arquitectura.md` §3 borra
    primero la cara vieja (commit 4) y después el arranque viejo (commit 5), «cada uno deja `dev` verde». No puede ser:
    `internal/bootstrap/arranque` era lo único que importaba `internal/publicapi`, así que con la cara borrada el árbol
    no compila. Se invirtió: `ae4c0291` (T10.10) y después `db5657b4` (T10.9). Y tres candados del árbol viejo leían la
    fuente de `publicapi` (los dos INV1 de `internal/intakes` y el C2 de `internal/llmvia`): se retiraron en T10.9, un
    commit antes que el resto de sus paquetes; sus gemelos del árbol nuevo siguen.
16. **El candado de fronteras clasificaba `cmd/server`, `cmd/casebank` y `cmd/prompts` como árbol viejo.** En cuanto
    importan `internal/arranque` o `internal/modulos`, salta la regla 5. La spec no lo decía. Pasan al árbol nuevo en
    T10.7.
17. **`internal/platform` sí se toca**, contra `reglas.md` §1: cuatro tests suyos importaban paquetes viejos
    (`iam/ports/in`, `gateway/session`, `inferstats`). Los tres de `httpapi` usaban alias del propio paquete; el del
    colector de inferencia pasa a un agregado fijo, porque `platform` no puede importar `internal/modulos`. Y se borró
    `internal/platform/storage/postgres/tenant.go`: no tenía ningún llamador salvo el test que se borraba.
18. **Un test del arranque viejo no tenía gemelo**: el del techo del pool (`WAPP_DB_MAX_OPEN_CONNS` → `/metrics`, con
    base de datos). «Sus reglas viven ya en `internal/arranque`» era cierto para los nueve de cableado y para I-CP-5,
    no para este. Portado a P0 (`TestP0_PoolCeilingComesFromTheEnvironment`); con el mutante que deja de pasar el techo
    a la conexión cae con «`wapp_db_max_open = 25`, quería 7».
19. **P10 no re-expresaba los 25 tests con base de datos de `platform`, sino 21.** La rotación de KEK la cubre
    `rekey_contrato_test.go`, no P10. Los 4 restantes: tres reglas del colector de `flow_events` sobre la elección de
    líder (una, deuda D-43; las otras dos, declaradas solo en una cabecera) y `TenantRepository` (hallazgo 17). Las tres
    del colector se portaron a `test/procesos/collector_contrato_test.go`, sin servidor y sobre una base clonada, y
    cada una cae con su mutante; el candado de imports de los procesos admite para ello un paquete más de `platform`.
    `internal/platform/metrics/flowlifecycle` se queda sin tests propios: lo ejercen los procesos.
20. **T10.23 no puede cerrarse sin Jhoan, y el inventario se quedaba corto.** De 95 `//nolint` quedan 45 con motivo: la
    tarea pide que los apruebe Jhoan. Y hay **17 `#nosec`** (4 en producción), la directiva propia de `gosec`, que el
    `grep` de `//nolint` no veía. No se tocaron. Todo, en `deuda.md` D-31.
21. **T10.14 es una reescritura, no un re-apuntado**, y sale a un PR propio (F10-04b, decisión de Jhoan en la sesión).
    El patrón de `diseno.md` §4 solo ve las menciones con prefijo `internal/`; quedan fuera `publicapi.go:770`,
    `bootstrap/http.go:168` y ≈190 citas `fichero:línea`. La tabla de `04` §4 mapea directorios, pero los ficheros se
    partieron (E-13) y se renombraron (E-11). **Mientras ese PR no entre, la documentación de la pieza, `CLAUDE.md` y
    cinco skills nombran rutas y targets que ya no existen.**
22. **La definición de hecho dice «cinco directorios» en `internal/`; son seis.** `candados` se queda (D-F10-10).
    `reglas.md` §4 y la ficha de la sesión no se corrigieron al decidirlo.
23. **Queda sin decidir qué pasa con los comentarios del código.** Unas 735 líneas de comentario del árbol que sobrevive
    citan rutas viejas (`internal/flujos/…`), sobre todo como procedencia. Y tres textos `Help` de
    `internal/platform/metrics/metrics.go` nombran ficheros borrados: salen por `/metrics`, así que son texto observable
    (deuda D-36). `portal-reorganizacion/` nombra `cmd/server-modular`. No se tocó nada de esto.
    ✎ **2026-10-11 · D-F10-14 (Jhoan)**: los comentarios se limpian en un PR propio, entre el bloque D y T10.25 (T10.26).
    `portal-reorganizacion/` se mira en T10.25.
24. **No corrido**: `make ci-docker`; el job `integration` de `ci.yml` (solo su sintaxis); `CUENTA=3` de los procesos (se
    corrió con 1; es T10.15). Y nada en UAT, donde sigue un binario de `cmd/server-modular`, directorio que ya no
    existe: lo del acta §5.7 sigue sin ver.

### F10-05 · bloque D (2026-10-11)

25. **El binario del relevo corre en UAT**: `cmd/server` @ `21d527af` desde las 01:49:27 UTC del 2026-10-12, con §6 y §9
    del runbook verificados, 0 `ERROR` y 0 `WARN` en el cambio. Antes, sobre ese SHA: `GATE_RC=0`, 0 `--- SKIP`, y
    `CUENTA=3 make test-procesos` `RC=0 · PASS=3915 FAIL=0 SKIP=0`. Acta §7.
26. **Lo del acta §5.7 sigue sin ver**, y no se da por bueno. Jhoan decidió mirar sin echar las palancas (la receta pide
    un login de administradora contra la identidad real, y deja muda la sesión de WhatsApp real del Edge de UAT). En solo
    lectura: Ollama vivo, 0 `intakes` en 7 días, ningún job en curso, ni una inferencia desde el arranque. UAT no tiene
    tráfico, así que **ninguna inferencia real ha pasado todavía por el binario del relevo**. Acta §7.4.
27. **`cmd/migrate -status` en el VPS necesita el `EnvironmentFile` cargado.** A pelo, como lo escriben `diseno.md` §2.1
    y el acta §4.2, falla contra una base por defecto (`wapp_cloud`) que no es la de UAT. No es del relevo: el comando
    del guion estaba incompleto.
28. **En UAT quedan dos binarios de marcha atrás, no uno**: `bin/server.viejo-9493cea` (el de `main` de septiembre, que
    T10.22 borra) y `bin/server.modular-e82b6e92` (el que corría hasta hoy). T10.22 nombra solo `server.viejo-*`: el
    segundo hay que borrarlo allí también.
29. **Las dos skills que pedía la sesión siguen describiendo lo que el relevo borró** (`make vet-pendiente`,
    `make test-pendiente`, `WAPP_PROCESOS_BINARIO`, «contra los dos binarios»). Es el hallazgo 21, visto desde quien las
    carga: se usaron solo los targets que existen. Lo arregla F10-04b (T10.14).
30. **La ficha de F10-05 se partió**: decía «Entrada: T10.14 hecha», «trabajas directo en `dev`» y cubría D + E. Queda
    solo con el bloque D, y el E pasa a [`F10-05b`](../sesiones/F10-05b-cli-fuera-del-repo.md). `reglas.md` §2, la
    cabecera de `tareas.md` y «Bloques de sesión» de este README siguen diciendo «directo en `dev`» y «sin PR»: no se tocaron (manda la regla 6 del
    `CLAUDE.md`).
31. **No corrido en esta sesión**: `make ci-docker`, `make test-procesos-edge-real` y el job `integration` de `ci.yml`.
    Y 159 de los 179 paquetes de `ci-local` salieron de la caché de `go test`; el `-v` posterior los corrió todos.

### F10-04b · T10.14 (2026-10-11)

32. **La documentación de la pieza está reescrita contra el árbol que existe** (D-F10-8), no re-apuntada: `contratos.md`,
    `arquitectura.md`, `constitucion.md`, `operacion.md`, `esquema-postgres.md`, `deuda.md`, los dos `README.md` y
    `CLAUDE.md`. Antes: 163 rutas de paquetes borrados y 199 referencias que no existían en el árbol (rutas, citas
    `fichero:línea`, símbolos, variables). Después: 0 y 0. Lo comprueba `scripts/check-docs-tree.sh`, que queda en el
    repo y se explica en `documentations/README.md`. **No comprueba que la línea citada diga lo que se afirma**: eso lo
    miró quien escribió cada cita.
33. **Cuatro cifras de cabecera estaban mal, y no por el relevo.** Variables de entorno: **71**, no 70 (el `grep` del
    documento no veía `WAPP_FLOW_REPLY_RATE`, que se lee con otra función del loader). Tablas: **45**, no 47 (los 47
    sumaban `schema_version`, que crea el runner, y una vista). Comandos de `cmd/`: **6**, no 4. Tablas tocadas por más
    de un módulo: **9**, no 14. Las 95 rutas (22 + 73) y los 2 rpc sí casan con la dorada.
34. **Afirmaciones que la documentación daba por ciertas y el código desmiente** (corregidas en su documento): con la
    clave de cifrado vacía el arranque genera un par efímero con aviso, no «no abre los sobres»; con la PKI vacía no
    levanta el proceso entero, no solo el `:8101`; el *deny* `*.any` lo siembra la migración `0059`, no la `0060`;
    no todas las rutas de solicitudes llevan la feature `cart_basic`; `anclaje` sí se importa (lo que no tiene llamante
    es `anclaje.Distribute`); los no-op de `rows.Close()` son 14 en 8 ficheros, no 41 en 24; `POST /api/v1/members` no
    se monta siempre.
35. **Tres skills retiradas, por decisión de la sesión, confirmada por Jhoan al revisar el PR (2026-10-11)**: `contrato-tdd`, `reconstruir-modulo` y `traspaso-web-local`.
    Son el procedimiento de la reconstrucción y ya no se pueden seguir (piden el fichero viejo de referencia, la
    etiqueta `pendiente` y `cmd/server-modular`). Van en un commit propio, `19ed4883`, para poder revertirlo solo;
    T10.25 ya preveía «se reescribe o se retira». `validar-antes-de-cerrar` y
    `procesos-testcontainers` se reescribieron (hallazgo 29). El hook de `SessionStart` ya no imprimía ningún recuento
    de pendientes: no hubo nada que tocar.
36. **Lo que queda con rutas o nombres que ya no existen, y de quién es**: tres enlaces de `06-entorno-web.md` a las
    skills retiradas, y `PROTOCOLO-CLI.md` §3–§4 (`make vet-pendiente`, `make test-integration`, `cmd/server-modular`):
    son del expediente, que sale en T10.25. Comentarios del código, para T10.26: la cabecera de `cmd/server/main.go`
    dice «DOS listeners gRPC y un HTTP», la de la dorada nombra `TestHuellaVieja` (el test es `TestHuella`), y
    `lease` llama «tabla de platform» a `tenants`. `deploy/README.md` enlaza `docs/` de la raíz de wApp (se corrigió
    solo su comando de arranque y su sección de tests). El `Makefile` conserva `COBERTURA_BRIDGE_DIRS`, que ya no
    aporta ficheros.
37. **Tres cosas del código que la reescritura sacó a la luz y no están en `deuda.md`** (no se añadieron: piden que
    alguien las confirme): ningún `.go` de producción escribe `plans`, `plan_features`, `tenant_features` ni
    `tenant_settings` (un *override* de plan sería SQL a mano); la lista blanca de fronteras permite tres pares de
    módulos que se importan en los dos sentidos (`conversacion` con `captacion`, `catalogo` y `solicitudes`); y
    `internal/apipublica/eventstelemetry_store.go` tiene SQL propio sobre `flow_events`. Sin verificar del todo, y
    dicho en su entrada: D-4 (medida del 2026-08-29), D-45 y D-46 de `deuda.md`.
38. **No corrido en esta sesión**: `make test-procesos`, `make ci-docker` y `make test-procesos-edge-real` (no se tocó
    código: solo documentación, skills y un script de comprobación).

### F10-05b · bloque E (2026-10-11 → 2026-10-12)

39. **La raíz de wApp no admite «rama de `dev` y PR»**: no tiene remoto ni rama `dev`, solo `main` local. Jhoan, en
    la sesión: «wApp es solo local y main». T10.18, T10.19 y T10.21 son commits directos en ese `main`. Además tenía
    trabajo sin commitear del 2026-09-27/28, y 13 de las 16 menciones de T10.21 vivían en ficheros que git no seguía:
    lo que era documentación (`analisis/`, `ACTIVE.md`, `scripts/check-md-links.py`) entró antes, en `10e1b5c`. **Sigue
    sin commitear allí, y no es de esta sesión**: `.gitignore`, `.obsidian/core-plugins.json`, el borrado de
    `Limpieza de documentación.md` y los dos `probar-frase.sh`.
40. **T10.18 en números**. 1.124 menciones en 170 ficheros; 80 históricas (`historia/` 3, `planes/historico/` 77) y
    1.044 a reescribir. Un script (tabla de `04` §4, comprobando que cada ruta resultante existe) resolvió 907: 697
    completas —309 de ellas con el número de línea **re-localizado** buscando el texto de la línea vieja (`git show`
    de cuatro commits) en el árbol nuevo— y 210 que quedaron **sin número de línea** porque el texto ya no existe
    igual (los nombres nuevos están en inglés). Las ~150 restantes no tenían sucesor de un fichero (`bootstrap.go`,
    `publicapi.go`, tests viejos que no se portaron uno a uno): tres sub-agentes, cita a cita. Las 80 históricas
    conservan su ruta y llevan `[hoy …]` al lado, o `[hoy sin sucesor: borrado en el relevo del 2026-10-11]`.
41. 🔴 **Re-apuntar no es re-verificar, y D-F10-8 pide lo segundo.** El recuento de §3.1 da 0, pero mide rutas, no
    afirmaciones. Lo que queda, medido o visto en la sesión: (a) las citas **abreviadas**, sin el prefijo
    `internal/` (`postgres.go:152`, `store/store.go:624`), no las ve el `grep` y siguen con su número viejo; (b) hay
    documentos del ecosistema con **cifras medidas sobre el árbol viejo** que ahora llevan rutas nuevas:
    `diagramas/salud/tamano-y-complejidad.md` §0–§2 (43.282 líneas de producción; hoy 139.645 con otra regla),
    `diagramas/salud/dependencias-y-acoplamiento.md` §2 («83 aristas entre 29 módulos»), la tabla de paquetes de
    `diagramas/piezas/cloud-platform.md`, `MEJ-24` y `MEJ-27`; se corrigió lo que cada cita exigía (los ficheros y
    funciones más largos, los paquetes sin test, los ecos del ADR-0010) y lo demás lleva su aviso; (c) cosas que **parecen
    ya resueltas por la reconstrucción** y nadie ha cerrado: la deuda DT-52 y `MEJ-41` (el cloud ya no tiene
    `WAPP_TEST_DB_DSN` ni un `t.Skip` real), el micro-plan MP-12 (`TestP3_TxRetryOnSerializationFailure` existe) y una
    tarea del plan 036 (los cuatro tests de repos de `iam` existen); (d) candados que los ADR-0020, 0029 y 0044 citaban
    y cuyo test nuevo cubre **menos** que el viejo (`carril_rapido_test.go` no se portó uno a uno). Pide una sesión
    propia: es una auditoría de afirmaciones, no una sustitución.
42. **ADR-0010: de 14 a 9 tablas, y no es mejora** (T10.19). Con la regla nueva salen 9 (`fleet_sessions` y
    `flow_events` con 3 módulos; `conversation_events`, `flow_state`, `intake_items`, `intakes`, `leases`,
    `tenant_settings` y `tenants` con 2). Seis salen de la lista solo porque `iam`, `platformadmin` y `entitlements`
    son ahora `acceso`; una entra (`flow_state`) porque `contact` pasó a `nucleo`. Las escrituras ajenas siguen:
    `edge` escribe `tenants` y `conversacion` escribe `intakes` e `intake_items`. El ADR lo dice con esas palabras. La
    medición es `scripts/adr-0010-tablas-por-modulo.py` de la raíz. Casa con el «9, no 14» del hallazgo 33.
43. **T10.20: 12 comentarios y 6 menciones `.md`, en seis repos** (`wapp-edge-intent` no tenía ninguna). Tres cosas:
    (a) el `make lint` de `wapp-edge-agent` **falla en su `dev`** sin el cambio, con el Go 1.27.1 y el golangci-lint
    2.14.0 del sistema (3 avisos SA1019 en `cmd/wapp-ctl/proxy.go:49` e `internal/auth/keystore.go:109-110`): su
    `Makefile` no fija la toolchain, como sí hace este repo; (b) `golangci-lint` no admite dos corridas a la vez:
    lanzar los seis gates en paralelo da un falso rojo («parallel golangci-lint is running»); (c) las menciones de
    `internal/bootstrap` en la documentación del BFF y de las dos consolas son de **su** paquete (T-12) y no se tocan;
    `wapp-shared/llm/CHANGELOG.md` conserva la ruta vieja con la de hoy al lado, por ser una versión publicada.
44. **`make check-docs` de la raíz estaba en rojo antes de empezar, y uno de sus tres gates no medía.** Rojo: 7
    enlaces rotos, 4 en las notas de `analisis/` (T10.21) y **3 en `06-entorno-web.md` de este repo**, hacia las skills
    retiradas en F10-04b (el hallazgo 36 los dejaba para T10.25; se quitan aquí, en `4d08d18a`, porque el gate de
    T10.18 los exige). No medía: `check-diagramas` **se salta con rc=0** si no hay `mmdc` en el PATH, y en esta
    máquina no lo había. Se instaló en el directorio temporal de la sesión y los 80 diagramas renderizan; quien
    repita el gate sin `mmdc` verá un verde que no es tal.
45. **No corrido en esta sesión**: `make ci-docker` en ningún repo; `make test-procesos` (no se tocó código de este
    repo: solo documentación). `make ci-local` de este repo, sí (bloque de la sesión en `ESTADO.md`).
46. **T10.26 en números** (F10-05c, 2026-10-12). Medido antes de tocar con un escáner de `go/scanner`: **718** líneas de
    comentario en **590** ficheros citaban una ruta vieja (el hallazgo 23 decía «unas 735»: contaba líneas de `grep`,
    que incluyen literales). 304 eran una cabecera que solo decía «Porta `internal/…` @ sha» y se quitaron con un
    script; las otras 414, en 302 ficheros, se reescribieron una a una. Donde el comentario remitía a algo vivo se puso
    la ruta de hoy, comprobada; donde era procedencia, se quitó. `-987 +629` líneas, todas de comentario.
47. 🟡 **«Recuento → 0» vale para los comentarios, no para los `.go` enteros.** Quedan **52 líneas** con una ruta vieja
    dentro de un **literal**: 24 en `internal/candados/fronteras_test.go` y 15 en sus `testdata/` (los casos del candado
    de fronteras, que sigue probando reglas sobre un árbol «viejo» de mentira), 7 en tests de cableado de
    `internal/arranque` (rutas de import prohibidas y mensajes de error que aún dicen `bootstrap.go`), 2 en
    `processimports_test.go`, 1 en `responsecontroller_test.go` y los 3 `Help` de `/metrics` (D-36). Tocarlos es tocar
    código. **T10.25 pide «0 en todo el repo»**: o su recuento excluye estos literales, o antes hay una tarea que los
    reescriba. Lo decide Jhoan.
    ✎ 2026-10-12 · **Decidido (D-F10-15)**: el recuento de T10.25 los excluye y pasan a deuda, **D-50** de
    `deuda.md`, para después del relevo.
48. **`TestHuellaVieja` no es un comentario** (venía del hallazgo 36). El nombre está en un literal de
    `internal/arranque/huellatest/golden.go:43`, que es quien escribe la cabecera de `testdata/huella-vieja.golden`.
    Corregirlo es cambiar código y regenerar la dorada: no se tocó. Los otros dos del hallazgo 36, sí: la cabecera de
    `cmd/server/main.go` dice los cuatro listeners y `lease` dice que `public.tenants` es del módulo `acceso`.
49. **«Los binarios no cambian» no se puede cumplir, y por qué no importa.** Quitar una línea de comentario mueve el
    número de línea de todo lo que hay debajo, y esos números van dentro del binario (trazas, `pclntab`). La
    comprobación que sí prueba «ni una línea de código» es otra: la secuencia de tokens de cada `.go`, sin comentarios y
    con las directivas `//go:` y `//nolint`, es **idéntica** en los 1.656 ficheros antes y después; `gofmt -l` vacío.
    El efecto secundario fue real: **160 citas `fichero:línea`** de `documentations/*.md` apuntaban a líneas que se
    movieron; se re-apuntaron con el diff exacto de cada fichero (192 citas a ficheros tocados, 32 no se movían, 0 sin
    correspondencia). 🔴 **Las de la documentación de la raíz de wApp** (las que T10.18 dejó con número de línea)
    quedan desfasadas en una o más líneas cuando este PR entre: se re-apuntan con el mismo método, después de fusionar.
50. **Lo que sigue hablando del código borrado sin nombrar su ruta** (no entraba: el recuento mide rutas). Hay muchos
    «el viejo», «en la cara vieja», «Nombres (E-11), viejo → nuevo», y citas a `plan/F*/diseno.md`, que solo existe
    dentro de esta carpeta y se irá con T10.25. Y afirmaciones que el relevo dejó falsas, vistas al pasar y **no
    tocadas**: `limits.go` de `apipublica` («hasta entonces lo usa solo su test»); `crmpush/contrato_test.go` («se
    re-toca en F8»); `casebank/seed.go` («Ninguna de las dos copias…»); `config_resolver_normalize_test.go` («el que
    corre en UAT»); `platform/config/config.go` (`offlinePersistTimeout`, `publicapi.SendBudgetFrom`);
    `test/procesos/p0_arranque_test.go` (números de línea del arranque viejo); y una docena de cabeceras de
    `internal/arranque` que solo dicen «desde F8 ya no cablea ningún paquete viejo».
51. **Candados con reglas que ya no pueden morder.** `Mapa`, `Puentes` y la regla 3 de fronteras hablan de un árbol que
    no existe (en `internal/modulos/fronteras_test.go` el `Mapa` está vacío). Y el barrido de
    `inferencia/llmvia/c2_via_test.go` prometía cubrir todo `internal/` «desde F10»: sigue en `modulos`, `nucleo`,
    `arranque` y `apipublica`. Es código: deuda, no de esta tarea.
52. **No corrido**: `make test-procesos` (los 29 ficheros de `test/procesos` tocados cambian solo comentarios: mismos
    tokens) ni `make ci-docker`. `make ci-local`, sí: `GATE_RC=0`.

