# Acta del relevo (F10) — verdad de campo, dorada, prueba en UAT y cierre

> Conserva el nombre de traspaso, pero es el **acta** de la fase [F10 · relevo](../plan/F10-relevo/README.md): cada
> sesión de F10 escribe aquí lo que midió. **Ni un secreto**: de una credencial se dice dónde vive, nunca cuál es.
>
> | § | Qué | Sesión | Estado |
> |---|---|---|---|
> | 1 | Verdad de campo antes del relevo | F10-02 (T10.1) | escrita el 2026-10-11 |
> | 2 | La dorada de la huella | F10-02 (T10.2) | escrita el 2026-10-11 |
> | 3 | Capturas de referencia (`cmd/prompts`, `cmd/casebank`) | F10-02 (T10.1) | escrita el 2026-10-11 |
> | 4 | Preparación de la prueba en UAT y SHA a desplegar | F10-02 (T10.3) | escrita el 2026-10-11; el SHA de `dev` lo fija F10-03 |
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
| Visto bueno a la ventana de UAT (D-F10-1) | `plan/DECISIONES.md` §6 | decidida la duración (≥ 24 h, 2026-09-30) y, el 2026-10-11, que **la fecha es relativa**: al cerrar la fase FW (D-FW-3). El día y la hora reales los escribe F10-03 en §5 |

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

Commit `374d8652` (`relevo: la huella del viejo, congelada`).

**Qué había y qué cambia.** F0 ya había dejado una dorada, `internal/arranque/testdata/huella.json`, pero solo con la
parte de **ejecución**. La parte **estática** (qué goroutines lanza el arranque y qué hooks de métricas cablea) no
estaba en ningún fichero: `TestHuellaEstatica` la comparaba leyendo la **fuente** de `internal/bootstrap/arranque`, y
habría desaparecido con ese directorio en T10.10. Ahora todo está en un solo fichero de texto:

| | |
|---|---|
| Fichero | `internal/arranque/testdata/huella-vieja.golden` (239 entradas, una por línea, ordenadas, más 4 líneas de cabecera) |
| Formato | `perfil <TAB> clase <TAB> listener (o -) <TAB> valor`; lo leen y escriben `huellatest.ReadGolden` y `WriteGolden` (`internal/arranque/huellatest/golden.go`) |
| Perfiles `minimo` y `con-m2m` | cada uno 95 rutas (22 en `:8100`, 73 en `:8103`), 2 rpc (`:8101` y `:8102`) y 11 familias `wapp_*` |
| Perfil `estatica` | 10 sentencias `go` y 13 hooks de `*metrics.Metrics` |
| Quién la escribe | solo `go test -run '^TestHuellaVieja$' ./internal/bootstrap/arranque/ -args -actualizar`, y solo mientras ese paquete exista |

**Verificada contra el viejo en ejecución.** `TestHuellaVieja` arma el arranque viejo en el proceso (fases 2–8
reales, sin red ni Postgres), mide su huella y la compara con la dorada: **PASS**. La parte de ejecución es, entrada a
entrada, la del JSON de F0 (216 de 216, comparadas con un script antes de borrar el JSON): la reconstrucción de F1–F8
no movió ni una ruta, ni un rpc, ni una métrica.

**Verificada contra el nuevo.** `TestHuella` (`internal/arranque/huella_test.go`) compara el arranque nuevo con la
dorada entera, incluida la lectura de entorno del árbol nuevo, que debe ser vacía: **PASS** (`entorno=0`).
`TestHuellaEstatica` sigue comparando además con la fuente viva del viejo, y se borra con él en T10.10.

**Muerde.** Dos pruebas:

- `TestHuellaBites`, que se queda en el repo: seis casos (sin tocar; una ruta de menos; una de más; una goroutine de
  menos; un hook de más; una lectura de entorno), cada uno con la línea exacta que debe salir.
- Un mutante real, hecho a mano y deshecho: con `mux.Handle("/healthz", …)` comentado en
  `internal/arranque/rutas_admin.go`, `TestHuella` **cae** con `huella [minimo] :8100 falta /healthz` y
  `huella [con-m2m] :8100 falta /healthz`.

**Gates sobre este commit**: `make toolchain` rc=0 · `GOWORK=off make ci-local` → `GATE_RC=0` (leído del log), 239
paquetes `ok`, lint `0 issues.` · `make test-pendiente` → `PENDIENTES=0`, `ROJOS=0`, rc=0 · cobertura (informe):
`golden.go` 100,0 %, `huellatest.go` 96,7 % · SKIP con `-v` en `./internal/arranque/...`: **0**.

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

### 4.1 · Qué SHA se despliega

**En la ventana se despliega `origin/dev` tal como esté cuando F10-03 la abra**, no un SHA de esta rama. Dos razones:
esta sesión entra en `dev` por PR, así que el commit que quedará en `dev` es el del merge, que aún no existe; y F10-03
espera a que la fase FW esté cerrada (D-FW-3), que meterá más commits en `dev` antes.

Lo que esta sesión sí fija:

| | |
|---|---|
| Último commit de **código** de esta rama | `374d8652` (`relevo: la huella del viejo, congelada`), sobre `dev` @ `bf2d0ae9` |
| Qué cambia en el binario respecto de `bf2d0ae9` | **nada**: solo tests, `testdata` y el paquete `internal/arranque/huellatest`, que ningún binario importa (`GOWORK=off go list -deps ./cmd/server ./cmd/server-modular \| grep -c huellatest` → 0) |
| Cómo fija F10-03 el SHA | tras el `git reset --hard origin/dev` de §4.3, `git rev-parse HEAD`; lo escribe en §5 y lo comprueba contra `vcs.revision` del proceso (§4.4 · 1) |
| Condición | ese `dev` contiene `374d8652` (`git merge-base --is-ancestor 374d8652 HEAD; echo rc=$?` → 0) y `make ci-local` da `GATE_RC=0` sobre él |

### 4.2 · Antes de la ventana (captura) — `diseno.md` §2.1, literal

Acceso: `ssh wapp-vps`. Las credenciales viven en el `EnvironmentFile` de la máquina
(`/root/source/wApp/cloud/wapp-cloud-platform/.env`, modo `600`); aquí no se escribe ninguna.

```bash
cd /root/source/wApp/cloud/wapp-cloud-platform
pid=$(systemctl show -p MainPID --value wapp-cloud)
go version -m /proc/$pid/exe | grep -E '^\s+path|vcs\.(revision|modified)'   # el viejo: path …/cmd/server
cp bin/server bin/server.viejo-$(git rev-parse --short HEAD)                 # marcha atrás
md5sum bin/server bin/server.viejo-*
curl -s 127.0.0.1:8100/metrics | grep -oE '^wapp_[a-z_]+' | sort -u > /root/metricas-antes.txt
docker exec wapp-postgres pg_dump -U wapp -d wapp -Fc > /root/backups/wapp-antes-f10-$(date +%F).dump
GOWORK=off go run ./cmd/migrate -status                                    # version y content_hash del viejo
```

### 4.3 · El cambio de binario — `diseno.md` §2.2, literal

```bash
git fetch origin && git checkout dev && git reset --hard origin/dev         # el SHA de §4.1
GOWORK=off go build -o bin/server ./cmd/server-modular                      # 🔴 el modular, en la MISMA ruta
GOWORK=off go run ./cmd/migrate -status                                     # mismo content_hash que en 4.2: si no, PARAR
systemctl restart wapp-cloud                                                # restart, nunca stop+start
systemctl restart wapp-client-console wapp-platform-console
```

No se toca la unidad systemd ni el `.env`: se sustituye **el fichero**.

### 4.4 · Verificación inmediata (los 10 primeros minutos) — `diseno.md` §2.3, literal

1. **Qué corre**: `go version -m /proc/$(systemctl show -p MainPID --value wapp-cloud)/exe` → `path …/cmd/server-modular`, `vcs.revision` = SHA de §4.1, `vcs.modified=false`; `md5sum` de `/proc/$pid/exe` = `bin/server`.
2. **Las líneas de clave**: `grep -E "clave pública de(l)? (cifrado|lease)" /root/source/wApp/logs/cloud.log | tail -2` → `key_source=config` o `file`. 🔴 `generated` = el Edge no puede abrir ni validar: **vuelta atrás ya**.
3. **Las nueve fases**: `grep 'arranque: fase completada' /root/source/wApp/logs/cloud.log | tail -9`.
4. **Salud**: `curl -s 127.0.0.1:8100/healthz` (200, postgres `healthy`), `:8104/healthz`, `:8106/healthz`, `:8107/healthz`.
5. **Edge**: la sesión vuelve a `online` en la consola de plataforma (`GET /admin/tenants/{id}/installations`) en menos de un minuto; `LeaseUpdate` válido en el log del Edge.
6. **Métricas**: `curl -s 127.0.0.1:8100/metrics | grep -oE '^wapp_[a-z_]+' | sort -u | diff /root/metricas-antes.txt -` → vacío (o solo `CounterVec` que aún no se incrementaron: se anotan y se re-miran al final).

### 4.5 · Marcha atrás (objetivo < 5 min) — `diseno.md` §2.5, literal

```bash
cd /root/source/wApp/cloud/wapp-cloud-platform
cp bin/server.viejo-<sha> bin/server
systemctl restart wapp-cloud && systemctl restart wapp-client-console wapp-platform-console
# y repetir 4.4 · 1–4 sobre el viejo: path …/cmd/server
```

Los criterios de la ventana son los de [`diseno.md`](../plan/F10-relevo/diseno.md) §2.4, que la fase FW reescribe en
TW.9 (las filas 1 y 6 se miran con el WhatsApp falso): por eso no se copian aquí.

### 4.6 · Lo que F10-03 tiene que saber de las divergencias

El binario que entra en UAT **no** responde igual que el viejo en 11 casos, a propósito
([`divergencias-visibles.md`](../plan/F10-relevo/divergencias-visibles.md) §5). Un error de esos durante la ventana no
es un «tipo de error que el viejo no daba» en el sentido del corte de §2.4: se coteja con esa lista antes de volver
atrás. Los que un consumidor puede ver sin provocarlos: V1 (`POST …/reanalyze` → 500 donde el viejo daba 200), V7 y V8
(más tramas `LeaseUpdate` y `ConfigPush` hacia el Edge) y V10 (el stream de CloudLink se corta en ≈ 0,06 s al parar,
no en 10 s).

## 5 · La ventana de UAT y el veredicto (T10.4–T10.6)

*Pendiente: sesión F10-03, que necesita UAT por SSH y la fase FW cerrada (D-FW-3).*

## 6 · Cierre del relevo

*Pendiente: sesiones F10-04 y F10-05.*
