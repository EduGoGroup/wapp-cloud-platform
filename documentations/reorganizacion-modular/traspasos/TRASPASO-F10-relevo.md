# Acta del relevo (F10) — verdad de campo, dorada, prueba en UAT y cierre

> Conserva el nombre de traspaso, pero es el **acta** de la fase [F10 · relevo](../plan/F10-relevo/README.md): cada
> sesión de F10 escribe aquí lo que midió. **Ni un secreto**: de una credencial se dice dónde vive, nunca cuál es.
>
> | § | Qué | Sesión | Estado |
> |---|---|---|---|
> | 1 | Verdad de campo antes del relevo | F10-02 (T10.1) | escrita el 2026-10-11 |
> | 2 | La dorada de la huella | F10-02 (T10.2) | pendiente |
> | 3 | Capturas de referencia (`cmd/prompts`, `cmd/casebank`) | F10-02 (T10.1) | escrita el 2026-10-11 |
> | 4 | Preparación de la prueba en UAT y SHA a desplegar | F10-02 (T10.3) | pendiente |
> | 5 | La ventana de UAT y el veredicto | F10-03 (T10.4–T10.6) | pendiente |
> | 6 | Cierre del relevo | F10-04 y F10-05 | pendiente |

## 1 · Verdad de campo antes del relevo (T10.1)

Medido el **2026-10-11** en la rama `reorg/f10-02-dorada`, partida de `origin/dev` @ `bf2d0ae9` (el merge del PR #74,
que cerró F9). Toolchain efectiva: `go1.26.5` y `golangci-lint v2.12.2` (`make toolchain` → `TOOLCHAIN=OK`, rc=0).

### 1.1 · Las condiciones que paran el relevo si fallan

| Condición | Comando | Resultado |
|---|---|---|
| Adaptadores `bridge_*.go` en `internal/arranque` (`05` §4.2) | `ls internal/arranque/bridge_*.go 2>/dev/null \| wc -l` | **0** |
| Puentes de import (`05` §4.1) | leer `Puentes` en `internal/modulos/fronteras_test.go:138` | **0** (`[]candados.Puente{}`) |
| `Conmutados` completo | leer `Conmutados` en `internal/modulos/fronteras_test.go:126` | **8 de 8**: `acceso`, `edge`, `catalogo`, `nucleo`, `inferencia`, `captacion`, `solicitudes`, `conversacion` |
| El candado de fronteras pasa | `GOWORK=off go test ./internal/modulos/` | rc=0 |
| Contratos sin lógica | `make test-pendiente` | **`PENDIENTES=0`** y **`ROJOS=0`**, rc=0 |
| El mutante `maxTxAttempts = 1` (hallazgo 38 de F1) | copiado de T9.15, abajo | **cae** |

**Ninguna falla: el relevo procede.**

🔴 **El `grep` de la spec no da 0, y no es un fallo.** `grep -rn 'pendiente.Implementar' --include='*.go' internal cmd
| wc -l` da **24**, no 0. Ninguna de las 24 es un contrato sin lógica: 9 están en el propio paquete
`internal/pendiente` (su definición y su test), 11 en `internal/candados` (el detector, sus tests y su `testdata`) y 4
en `internal/modulos/conversacion/modules/cart/orden_consulta_ast_test.go` (cadenas y comentarios de un candado sobre
el AST). La cifra que cuenta contratos es la de `make test-pendiente`, que excluye los tests,
`testdata`, `internal/pendiente` y los comentarios. Del mismo modo, `grep -rln 'go:build pendiente'` da 6 ficheros y
ninguno es un test en rojo: 2 lo citan en su comentario de cabecera (`inv1_aprobar_test.go`, `crmpush/contrato_test.go`)
y 4 son de `internal/candados`. Los dos `grep` de T10.12 («→ 0») solo darán 0 cuando `internal/pendiente` y los
candados que lo nombran se retiren (D-F10-3), no antes.

**El mutante, copiado de F9** (`ESTADO.md`, bloque F9-03, y hallazgo 37 del README de F9; no se repitió aquí):
`TestP3_TxRetryOnSerializationFailure` (T9.15, `250916a`) provoca un `40P01` real en el `Resolve` del contacto y afirma
que `pg_stat_database.deadlocks` sube, que el entrante se contesta y que no hay `ERROR`. Con el mutante
`maxTxAttempts = 1` (copia desechable de `internal/platform/storage/postgres/tx.go`) el test **cae 3 de 3 contra el
viejo y 3 de 3 contra el nuevo**, con `(c) el entrante se perdió: WithTx no reintentó tras el deadlock: … postgres:
transacción tras 1 intentos (último deadlock/serialización): contact: buscar ref: ERROR: deadlock detected (SQLSTATE
40P01)`; sin mutar, 3 de 3 en verde en los dos. **T10.11 no queda bloqueada**: `deadlock_integration_test.go` se puede
borrar en el relevo.

### 1.2 · Las demás entradas de la fase

| Entrada | Cómo se comprobó | Resultado |
|---|---|---|
| F9 cerrada, procesos ×3 contra viejo y nuevo | bloque F9-05 de `ESTADO.md` (no se repitió) | `CUENTA=3 make test-procesos`: viejo y nuevo `RC=0 · PASS=3873 · FAIL=0 · SKIP=0` |
| La huella del nuevo es la del viejo | `GOWORK=off make ci-local`, rc leído del log | **`GATE_RC=0`**, lint `0 issues.` (sobre el código de §2) |
| La cara vieja no sirve ninguna ruta en el binario nuevo | `internal/arranque/old_face_serves_nothing_test.go`, dentro de `ci-local` | verde |
| La integración vieja, por última vez | el procedimiento de `make test-integration` (Postgres 16 efímero, `WAPP_TEST_REQUIRE_DB=1`, `-p 1`) con `-v` y en el puerto 55432 | **`INTEG_RC=0` · 17.497 PASS · 0 FAIL · 0 SKIP** · 156 paquetes `ok` |
| Visto bueno a la ventana de UAT (D-F10-1) | `plan/DECISIONES.md` §6 | decidida la duración (≥ 24 h, 2026-09-30); **la fecha y la hora no están escritas**: las pone F10-03, que además espera a la fase FW (D-FW-3) |

Dos precisiones sobre la integración vieja: se corrió con un `go test` a mano y no con el target, porque el target no
lleva `-v` y sin `-v` no se pueden contar los SKIP; y arrancó sobre `bf2d0ae9` mientras se editaba
`internal/arranque/huellatest/` (solo ese paquete, que no tiene tests con base de datos).

### 1.3 · El inventario de lo que se borra, re-medido

Comando de [`diseno.md`](../plan/F10-relevo/diseno.md) §1:
`for d in $(ls -d internal/*/ | grep -v '^internal/platform/$'); do echo "$d $(find $d -name '*.go' ! -name '*_test.go' | wc -l) $(find $d -name '*_test.go' | wc -l)"; done`.

| Directorio | Prod | Test | | Directorio | Prod | Test |
|---|---|---|---|---|---|---|
| `internal/bootstrap/` | 22 | 21 | | `internal/intake/` | 24 | 38 |
| `internal/casebank/` | 4 | 3 | | `internal/intakeahead/` | 3 | 2 |
| `internal/catalogimport/` | 6 | 5 | | `internal/intakes/` | 28 | 59 |
| `internal/contracts/` | 0 | 1 | | `internal/integrations/` | 9 | 10 |
| `internal/degradation/` | 2 | 2 | | `internal/intentcfg/` | 2 | 2 |
| `internal/diagnostics/` | 2 | 2 | | `internal/llmvia/` | 4 | 10 |
| `internal/entitlements/` | 3 | 6 | | `internal/platformadmin/` | 4 | 5 |
| `internal/evidence/` | 1 | 1 | | `internal/prompts/` | 2 | 1 |
| `internal/filtercfg/` | 1 | 2 | | `internal/publicapi/` | 33 | 64 |
| `internal/flujos/` | 79 | 171 | | `internal/reanalisis/` | 1 | 2 |
| `internal/gateway/` | 28 | 50 | | `internal/receipts/` | 4 | 2 |
| `internal/iam/` | 44 | 31 | | `internal/tenantllm/` | 2 | 1 |
| `internal/inferstats/` | 1 | 1 | | `internal/tenantvars/` | 3 | 1 |
| `internal/ingest/` | 2 | 3 | | `internal/turnoacotado/` | 3 | 2 |

**Total: 28 directorios, 317 ficheros de producción y 498 de test.** Respecto del 2026-09-28 solo cambia
`internal/bootstrap/` (20 → 21 tests: `huella_vieja_test.go`, de F0): el código viejo no se tocó en toda la
reconstrucción.

Lo que se queda, con el mismo comando:

| Directorio | Prod | Test | Nota |
|---|---|---|---|
| `internal/apipublica/` | 52 | 81 | |
| `internal/arranque/` | 41 | 42 | contado tras §2 (antes, 40 y 41) |
| `internal/candados/` | 91 | 71 | incluye su `testdata` |
| `internal/modulos/` | 467 | 577 | |
| `internal/nucleo/` | 14 | 5 | |
| `internal/pendiente/` | 1 | 1 | se retira en T10.12 (D-F10-3) |
| `internal/platform/` | 28 | 34 | el 2026-09-28 eran 27 y 34 |

`cmd/` tiene hoy **7** directorios (`casebank`, `cobertura-ficheros`, `debug_inferencia`, `migrate`, `prompts`,
`server`, `server-modular`), no los 5 `main.go` + `server-modular` que contaba la spec.

### 1.4 · La documentación del repo, re-medida

Comando de [`diseno.md`](../plan/F10-relevo/diseno.md) §4 (menciones de los 28 paquetes viejos fuera de
`reorganizacion-modular/`): **165** (eran 147 el 2026-09-28).

| Fichero | Menciones |
|---|---|
| `documentations/deuda.md` | 57 |
| `documentations/constitucion.md` | 42 |
| `documentations/contratos.md` | 25 |
| `documentations/arquitectura.md` | 21 |
| `documentations/esquema-postgres.md` | 6 |
| `documentations/operacion.md` | 4 |
| `documentations/README.md` | 4 |
| `CLAUDE.md` | 2 |
| `README.md` | 1 |
| `.claude/skills/{contrato-tdd,procesos-testcontainers,reconstruir-modulo}/SKILL.md` | 1 + 1 + 1 |

### 1.5 · Los tests con base de datos de `internal/platform`

`grep -rl 'WAPP_TEST_DB_DSN' --include='*_test.go' internal/platform | wc -l` → **6 ficheros, 22 `Test*`** (la spec
contaba 9 y 25). En todo el repo, 99 ficheros de test leen `WAPP_TEST_DB_DSN`.

## 2 · La dorada de la huella (T10.2)

*Pendiente: se escribe en el commit de T10.3 de esta misma sesión.*

## 3 · Capturas de referencia (T10.1)

Para comparar en T10.7, cuando `cmd/prompts` y `cmd/casebank` dejen de importar los paquetes viejos (R10.3.c). Las dos
se tomaron con el Go fijado (`GOTOOLCHAIN=go1.26.5 GOWORK=off`), sobre `bf2d0ae9`.

### 3.1 · `cmd/prompts`

`go run ./cmd/prompts -volcar <dir>` (rc=0) y después `go run ./cmd/prompts -comprobar <dir>` (rc=0). Salida de
`-comprobar`, con `<dir>` en lugar de la ruta temporal:

```text
p2  <dir>/p2-extraer-ideas.tmpl
p3  <dir>/p3-especificar-item.tmpl
p4  <dir>/p4-normalizar-cantidades.tmpl
p5  <dir>/p5-redactar-cotizacion.tmpl

OK: el cloud arrancaría con este directorio.
```

Las cuatro plantillas volcadas, por los 16 primeros caracteres de su `shasum -a 256` (el texto de los prompts es
contrato: el relevo no puede cambiar ni un byte):

| Plantilla | SHA-256 (16 primeros) |
|---|---|
| `p2-extraer-ideas.tmpl` | `ca8e3ac4057f711d` |
| `p3-especificar-item.tmpl` | `fb98b400dc782a8d` |
| `p4-normalizar-cantidades.tmpl` | `a9dd7abf7f3d2089` |
| `p5-redactar-cotizacion.tmpl` | `c0983edf55593897` |

### 3.2 · `cmd/casebank` sin `-consentido`

`go run ./cmd/casebank` → **rc=1**, y por `stderr` (con la fecha y la hora del `log` delante):

```text
casebank: falta -tenant: un caso sin dueño no se puede consentir ni reutilizar
exit status 1
```

Sin argumentos el CLI corta en la primera validación, así que esta captura solo fija el mensaje y el código de salida.
La divergencia V11 de [`divergencias-visibles.md`](../plan/F10-relevo/divergencias-visibles.md) (el anonimizador
nuevo redacta tres casos que el viejo dejaba pasar) **no la ve**: comparar la salida con un caso real pide una base
con datos y queda para T10.7.

## 4 · Preparación de la prueba en UAT (T10.3)

*Pendiente: se escribe en el commit de T10.3 de esta misma sesión.*

## 5 · La ventana de UAT y el veredicto (T10.4–T10.6)

*Pendiente: sesión F10-03, que necesita UAT por SSH y la fase FW cerrada (D-FW-3).*

## 6 · Cierre del relevo

*Pendiente: sesiones F10-04 y F10-05.*
