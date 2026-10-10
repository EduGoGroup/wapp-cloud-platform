# F10 · Relevo — `cmd/server` pasa al arranque nuevo y lo viejo se borra

> **Estado**: ⏳ sin empezar (spec escrita el 2026-09-28 sobre `dev` @ `1b18932`).
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
| Jhoan da el visto bueno a la ventana de UAT (D-F10-1) | Fecha y hora escritas en «Decisiones» |

## Salidas (es cierto al cerrar)

- UAT corrió `cmd/server-modular` en sustitución durante la ventana acordada, con los criterios de
  [`diseno.md`](diseno.md) §2.4 verdes; hay acta (`traspasos/TRASPASO-F10-relevo.md`).
- `cmd/server/main.go` llama a `internal/arranque`; **no existen** `cmd/server-modular/`,
  `internal/bootstrap/`, `internal/publicapi/` ni los 26 directorios viejos restantes
  ([`diseno.md`](diseno.md) §1).
- `internal/` contiene solo `arranque/`, `apipublica/`, `modulos/`, `nucleo/`, `platform/` (y, según
  D-F10-3, nada de `pendiente/`).
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
| F10-02 | **A · preparación y dorada** | T10.1–T10.3 | — | Dorada de la huella commiteada y verificada contra el viejo; SHA a desplegar escrito en el acta |
| F10-03 | **B · la prueba en UAT en sustitución** | T10.4–T10.6 | **UAT (SSH)** | Acta de la ventana: veredicto «sigue» o «vuelta atrás», con evidencia. La ventana (≥ 24 h) es espera, no sesión: se abre (T10.4) y se relanza para cerrar (T10.5–T10.6) |
| F10-04 | **C · el relevo en el repo** | T10.7–T10.14, T10.23 | — | `ci-local` GATE_RC=0 con un solo arranque, cero adaptadores, cero puentes (import), `Conmutados` completo, cero pendientes, doc del repo al día; un commit por tarea |
| F10-05 | **D · cierre local** | T10.15–T10.17 | **Docker**, **UAT (SSH)** | `make test-procesos` RC=0 contra `cmd/server`; UAT desplegado desde el commit del relevo; acta `CERRADO` |
| F10-05 | **E · fuera del repo** | T10.18–T10.21 | la raíz de wApp y los repos hermanos | Doc del ecosistema, ADR-0010, 12 comentarios hermanos, bóveda `analisis/` con las rutas nuevas |
| F10-06 | **F · `main`** | T10.22 | **`main`**, **Docker**, UAT (SSH) | **Solo a petición expresa de Jhoan** |

## Decisiones que necesita (Jhoan)

| # | Pregunta | Recomendación |
|---|---|---|
| **D-F10-1** | La prueba en UAT: ¿cuánto dura y quién la da por buena? | **24 h mínimo**, con al menos una jornada de tráfico real (el e2e con WhatsApp real del runbook del ecosistema `e2e-con-whatsapp-real.md`) y los criterios de [`diseno.md`](diseno.md) §2.4. Veredicto de Jhoan, por escrito en el acta. El tráfico real de UAT **no está medido** |
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
