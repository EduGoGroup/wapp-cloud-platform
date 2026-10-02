# F9 · Procesos — la integración de cero, por proceso, con testcontainers

> **Estado**: ✅ **bloque A cerrado** (F9-01 🌐 lo escribió el 2026-10-01 → PR #18, `dev` @ `af7b8e9`; F9-02 💻 lo cerró el mismo
> día en local): `make test-procesos` da `RC=0 · PASS=146 · SKIP=0 · FAIL=0` por binario (`CUENTA=3`: 438), con **una intermitencia
> conocida en `TestP0_Arranque/sin_errores`, diferida a F6** (contradicción 19; decisión de Jhoan, 2026-10-01). B1, B2, C y D sin empezar.
> Spec escrita el 2026-09-28 sobre `dev` @ `1b18932`.
> **Norma**: [`05`](../../05-metodo-contratos-y-tdd.md) §7 (y E-5, E-6, §3.2). **Cómo**: skill
> [`procesos-testcontainers`](../../../../.claude/skills/procesos-testcontainers/SKILL.md). Forma:
> [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md). Si esta fase choca con `05`, manda `05`.

## Objetivo en tres líneas

Un paquete `test/procesos/` que levanta **un** Postgres 17 con testcontainers por corrida, clona una
base plantilla por proceso y arranca **el binario de verdad** (viejo o nuevo) contra ella, con un
**Edge de prueba** por gRPC mTLS y dobles de S3, identity, LLM y CRM. Cada proceso de negocio entra por
la puerta real y mira Postgres. Es el oráculo de comportamiento entre `cmd/server` y
`cmd/server-modular`, y la **condición del relevo** (F10).

## 🔍 La propuesta de orden: adelantar F9 (decisión D-F9-1, abajo)

`05` §6 pone F9 **después** de F8. Esta spec recomienda **partirla en cuatro olas** y adelantar las dos
primeras, porque los procesos se escriben **contra el binario viejo**, que no depende de nada
reconstruido:

| Ola | Qué | Cuándo | Bloques |
|---|---|---|---|
| **9A · Arnés** | `TestMain`, dobles, Edge de prueba, candado, `make test-procesos`, proceso P0 (humo) | En cuanto cierre **F0** (en paralelo con F1: no comparten ficheros) | A |
| **9B · Procesos contra el viejo** | P1–P9 escritos y en verde contra `cmd/server`; corridos también contra el nuevo | Tras 9A, **antes de conmutar F2** | B1, B2 |
| **9C · Pasada por conmutación** | Al conmutar cada módulo: suites de contrato de sus adaptadores Postgres contra Postgres + toda la suite contra el nuevo | Dentro de cada ciclo `conmutar(<m>)` de F1–F8 | C (una por módulo) |
| **9D · Cierre** | Corrida final contra los dos binarios, sin intermitencias, 0 SKIP, D-13 cerrada | Tras F8, **antes de F10** | D |

Si Jhoan **no** acepta adelantar, las olas 9A, 9B y 9D se ejecutan en bloque tras F8 (el orden de
`05` §6) y las tareas de 9C se **anulan** y se sustituyen por T9.34. Las tareas que cambian llevan la
marca **🕐** en [`tareas.md`](tareas.md).

## Entradas (tiene que ser cierto para empezar)

| Para | Condición | Cómo se comprueba |
|---|---|---|
| 9A | F0 cerrada: existen `cmd/server-modular` y `internal/arranque`, y los dos binarios compilan | `GOWORK=off go build ./cmd/server ./cmd/server-modular; echo rc=$?` → `rc=0` |
| 9A | D-13 (lista de procesos) y D-F9-1..D-F9-4 decididas por Jhoan | Sección «Decisiones» de este README con fecha |
| 9A | El candado `test/procesos/sin_bd_viva_test.go` y `test/procesos/doc.go` existen (**F0**, T0.8) y, si Jhoan aceptó **D-F1-2**, el mínimo de `test/procesos/main_test.go` y `contact_contrato_test.go` (**F1**, T1.13). ⚠️ *Corrección (revisión independiente, 2026-10-01)*: quedó al revés: `main_test.go` lo creó **F9-A** (T9.5, `576ba9a`) y T1.13 lo **reutiliza**; `contact_contrato_test.go` aún no existe (T1.13, bloque B de F1) | `ls test/procesos/` |
| 9A | La sesión que cierra tiene Docker (la local siempre; la web solo si la prueba de T9.12 salió bien) | `docker info >/dev/null; echo rc=$?` → `rc=0` |
| 9B | 9A cerrada por la sesión local (P0 verde contra el viejo **y** el nuevo) | Traspaso `TRASPASO-F9-arnes.md` con sección `CERRADO` |
| 9C(m) | El módulo `m` está en verde y su commit `conmutar(<m>)` existe | `git log --oneline --grep='conmutar(<m>)'` |
| 9D | F8 conmutada; `fronteras_test` sin puentes declarados | La lista de puentes de `internal/modulos/fronteras_test.go` vacía |

## Salidas (es cierto al cerrar)

- `test/procesos/` con el arnés, el candado `sin_bd_viva_test.go` y **un fichero por proceso** de la
  lista cerrada D-13 (propuesta: P0–P9, [`diseno.md`](diseno.md) §4).
- `make test-procesos` corre **contra los dos binarios** y deja dos logs con su `RC`; `ci-local`
  incluye `go vet -tags integracion ./test/procesos/...`.
- Cada proceso **pasa contra el viejo y contra el nuevo**, 3 veces seguidas (`-count=3`), con
  **0 `--- SKIP`** y **0 `--- FAIL`**.
- Las **suites de contrato** (E-6) de los **22** paquetes con SQL (medidos; `05` dice 20) corren contra
  Postgres desde `test/procesos/<paquete>_contrato_test.go` (la convención que estrena F1, T1.13).
- Los candados de invariante que necesitan BD (`05` §3.2) son aserciones de su proceso.
- Ni un `WAPP_TEST_DB_DSN`, ni un puerto fijo, ni `WithReuseByName` en `test/procesos/` (el candado lo
  prueba en `ci-local`).
- **Ni una línea de código de producción cambiada** por F9 (salvo `go.mod`, `go.sum`, `Makefile`,
  `.golangci.yml`).

## Orden de lectura

1. [`requisitos.md`](requisitos.md) — historias y criterios EARS (H9.1–H9.8).
2. [`arquitectura.md`](arquitectura.md) — la topología del arnés, las olas frente a F1–F8, lo que no cambia.
3. [`diseno.md`](diseno.md) — fichero a fichero de `test/procesos/`, el entorno del servidor, los
   dobles, y **los diez procesos** con su recorrido, tablas, candados, suites y tests viejos.
4. [`reglas.md`](reglas.md) — las trampas medidas y la definición de hecho.
5. [`tareas.md`](tareas.md) — T9.1–T9.34 en bloques de sesión.

## Bloques de sesión

| Bloque | Entorno | Tareas | Para cuando |
|---|---|---|---|
| **A · arnés** | 🌐→💻 | T9.1–T9.12 | P0 verde contra los dos binarios en local; `ci-local` rc=0 con el candado |
| **B1 · procesos de plataforma y acceso** | 🌐→💻 | T9.13–T9.16 (P1, P2, P3, P9) | Los cuatro verdes contra el viejo y el nuevo, en local |
| **B2 · procesos de negocio** | 🌐→💻 | T9.17–T9.21 (P4–P8, doble CRM) y T9.35 (P10, solo si D-F9-4) | Los cinco (o seis) verdes contra el viejo y el nuevo, en local |
| **C · pasada por conmutación** 🕐 | 🌐→💻 | T9.22–T9.29, **una por módulo**, dentro de la sesión que conmuta | Suites del módulo contra Postgres + suite entera contra el nuevo, en local |
| **D · cierre** | 💻 | T9.30–T9.33 | `-count=3` limpio contra los dos; traspaso `CERRADO`; docs al día |

La numeración global de sesiones (`S0n`) vive en [`../sesiones/`](../sesiones/README.md).

## Decisiones que necesita (Jhoan)

| # | Pregunta | Recomendación |
|---|---|---|
| **D-F9-1** | ¿Se adelanta F9 (olas 9A/9B tras F0, 9C en cada conmutación) o se hace entera tras F8 (`05` §6)? | **Adelantar.** Motivo principal: E-6 deja el **SQL de cada adaptador Postgres nuevo sin probar contra Postgres** hasta F9; con el orden de `05`, F2–F8 conmutarían siete módulos con SQL no ejecutado nunca, y el primer aviso llegaría meses después. Con 9C, cada `conmutar(<m>)` tiene detrás una prueba de comportamiento, no solo la huella (que compara **nombres**, no conducta). Coste: ~8 pasadas cortas 💻 más. Análisis completo en [`arquitectura.md`](arquitectura.md) §5 |
| **D-13** | Lista cerrada de procesos | **P0–P9** de [`diseno.md`](diseno.md) §4: los 8 de `05` §7.4 + **P0 humo del arranque** (valida el arnés) + **P9 diagnóstico remoto y config empujada** (el único tramo del gRPC que ningún otro proceso toca). Opcional **P10 plataforma** si se acepta D-F9-4 |
| **D-F9-2** | 🔴 R2: ¿hace falta una opción nueva en el arranque para el doble de S3? (la pendiente de `ESTADO.md`) | **No.** El SDK usa *path-style* cuando el endpoint es una **IP**, aunque `UsePathStyle=false` (leído en `aws-sdk-go-v2/service/s3@v1.98.0/endpoints.go:5573` rama `ForcePathStyle == false`, y `:6185`, `:6298`, `:6496`: `if _url.IsIp == true` → `scheme://authority/<bucket>`). Con `WAPP_STORAGE_S3_ENDPOINT=http://127.0.0.1:<puerto>`, el `HeadBucket` de `r2_factory.go:63` llega como `HEAD /<bucket>` a un doble en proceso. **Leído en el código, no ejecutado**: lo confirma T9.11. Si T9.11 lo refuta, vuelve a Jhoan con la salida (la alternativa sería una variable nueva, que cambia la huella de las variables: `03` §1) |
| **D-F9-3** | Doble de S3: ¿servidor falso en el proceso de test o MinIO por testcontainers? | **En el proceso** (`s3falso_test.go`, `net/http` puro): el servidor solo hace `HeadBucket` al arrancar y **firma** URLs sin red (`presign.go`). MinIO (lo que corre UAT, `documentations/operacion.md` §6) sería un segundo contenedor por corrida sin ganar cobertura. Sin dependencia nueva (`gofakes3` no hace falta) |
| **D-F9-4** | 🔴 Los **9 ficheros / 25 `Test*`** de integración de `internal/platform/` **sobreviven** al relevo (platform no se borra) y seguirían leyendo `WAPP_TEST_DB_DSN` con `t.Skip` (DT-52) | **Re-expresarlos como P10 · plataforma** (réplica de migraciones sobre un clon, grants, rekey por `/admin/crypto/rekey`, el colector de `/metrics`) y borrarlos en F10 junto con `make test-integration`. Sin esto, F10 no puede dejar el repo sin `WAPP_TEST_DB_DSN`. Toca tests viejos de `platform`: por eso es decisión |
| **D-F9-5** (opcional) | ¿Medir la cobertura que los procesos dan al código nuevo (`go build -cover` + `GOCOVERDIR`)? | **Sí, informativa, sin umbral**: diría cuánto SQL de los adaptadores excluidos de E-9 ejecutan los procesos. No bloquea nada |

### Decisiones tomadas (T9.1)

> **2026-09-30**: Jhoan acepta **en bloque** la recomendación de todas las filas de
> [`../DECISIONES.md`](../DECISIONES.md) §4 «Antes de F9» (la fuente; aquí se copian con su fecha para
> que quien abra esta fase no tenga que salir de ella). Ninguna se tomó distinta de la recomendación,
> así que **no se reordena nada**.

| # | Decisión | Fecha | Efecto en las tareas |
|---|---|---|---|
| **D-F9-1** | **Sí**: se adelanta F9 (9A tras F0, 9B tras la parada de F1, 9C en cada `conmutar`, 9D antes de F10) | 2026-09-30 | Las tareas 🕐 se quedan como están; **T9.34 no se ejecuta** (queda solo como alternativa histórica); T9.22–T9.29 son las pasadas 9C |
| **D-13** | **Sí**: lista cerrada **P0–P9** de [`diseno.md`](diseno.md) §4 | 2026-09-30 | Un fichero por proceso, T9.11 y T9.13–T9.21 |
| **D-F9-2** | **Sí**: sin opción R2 en el arranque; con endpoint IP el SDK de S3 hace *path-style* solo | 2026-09-30 | Ninguna variable nueva. 🔴 La confirma **T9.11** ejecutándola; si el SDK pide *virtual-hosted*, se para y vuelve a Jhoan |
| **D-F9-3** | **Sí**: S3 falso dentro del proceso de test (`s3falso_test.go`, `net/http` puro) | 2026-09-30 | Sin MinIO ni `gofakes3` |
| **D-F9-4** | **Sí**: P10 · plataforma para los 9 ficheros / 25 `Test*` de `internal/platform/` con BD | 2026-09-30 | T9.35 **activa** (bloque B2) |
| **D-F9-5** | **Sí** (opcional, informativa, sin umbral): medir la cobertura que dan los procesos al código nuevo | 2026-09-30 | No bloquea nada; se decide cuándo en el bloque D |
| **T-2** | **Sí**: se aceptan las subidas de `httpsnoop` 1.0.4→1.1.0, `otelhttp` 0.67→0.69 y `klauspost/compress` que trae testcontainers v0.44.0 | 2026-09-30 | T9.2 va en un commit `chore(deps)` **aislado** (ver contradicción 10; qué llega de verdad a los binarios, en la 26) |

### Decisiones abiertas por la revisión independiente de S9–S11 (2026-10-01)

> Las plantea la revisión de F9-01, F9-02 y F1-01 sobre `dev` @ `6650e55`; **ninguna está tomada**. El hecho que motiva cada una
> está en la contradicción 19 (nota de revisión) y en las 22–30, abajo. Aún no tienen fila en [`../DECISIONES.md`](../DECISIONES.md).

| # | Pregunta | Propuesta de la revisión |
|---|---|---|
| **D-F9-6** | ¿El candado `SinBDViva` pasa de lista negra de patrones a **lista blanca de quién abre conexiones**? Cambia el contrato del candado que fija `05` §5 (contradicción 22) | Lista blanca: `sql.Open`, `pgx.Connect*` y `pgxpool.New*` solo en `test/procesos/base_test.go` (donde se llaman hoy: `:72` y `:188`), y auto-exención solo para la ruta exacta `test/procesos/sin_bd_viva_test.go`. Toca la norma: decide Jhoan |
| **D-F9-7** | ¿`TestMain` exige `GOWORK=off`? (contradicción 23) | Que salga con código 2 si `GOWORK` no es `off` |
| **D-F9-8** | ¿Quién limpia los directorios `procesos-*` que deja una corrida muerta? (contradicción 24) | Barrer al entrar en `TestMain` los de más de una hora, o añadir el `rm` al procedimiento |
| **D-F9-9** | ¿Acepta Jhoan las dos respuestas que F9-02 dio al §8 del traspaso (dejar el alcance extra del Edge de prueba; mantener la regla del `Cleanup`)? (contradicción 27) | Preguntarlo: hoy no tienen fila en `DECISIONES.md` ni la fórmula «decisión de Jhoan» |
| **D-F9-10** | **Alcance y criterio de D-F6-7** (contradicciones 19 y 28): ¿cubre las otras tres goroutines de fondo, que no son de F6?; ¿qué se hace con el binario `viejo`, que conserva el worker viejo hasta F10?; ¿con qué se remide, si `CUENTA=3` da verde ≈ 98,8 % de las veces sin arreglar nada? | Sin propuesta: es rumbo. Las salidas (a)–(c) de la contradicción 19 siguen siendo las candidatas |
| **D-F9-11** | ¿Se parten los ficheros de test de más de 500 líneas de `test/procesos`, como se hizo con `contacthelpertest/contrato.go` (entonces `contacttest/`)? (contradicción 29) | Sí, por tema y solo moviendo declaraciones, antes de que B1 los haga crecer |
| **D-F9-12** | ¿El Edge de prueba replica también el gate de lease de la **inferencia** (y lleva cuenta de los envíos bloqueados)? (contradicción 30) | Sí al gate de inferencia, antes del proceso que la recorra (P3/P4, T9.15/T9.17): es el mismo agujero que la 25 (b) en el camino LLM. El contador, solo si un proceso lo necesita |

## Encaje con F0 y F1 (escritas antes que esta spec)

- **F0** crea `test/procesos/doc.go` y el candado `sin_bd_viva_test.go` (T0.8; patrones en su
  `diseno.md` §4.4) y hace la **sonda de testcontainers en la web** fuera del árbol (T0.0, veredicto
  en `06-entorno-web.md` §5). F9 **amplía** el candado (T9.3) y **usa** ese veredicto (T9.12).
- **F1**, si Jhoan acepta **D-F1-2**, adelanta el mínimo del arnés (`main_test.go`: contenedor +
  plantilla + base clonada) para correr `contacthelpertest.Contrato` contra `PostgresResolver`
  (`contact_contrato_test.go`, T1.13/T1.18). Entonces T9.5 **amplía** ese `main_test.go` (binarios,
  servidor por proceso) en vez de crearlo, y la pasada 9C de `nucleo` (T9.22) solo añade la corrida de
  la suite entera contra el nuevo. D-F1-2 y D-F9-1 empujan en la misma dirección: si se acepta una,
  conviene aceptar la otra.
  ⚠️ *Corrección (revisión independiente, 2026-10-01)*: el orden quedó **invertido**. F9-A se ejecutó antes que el bloque B de F1:
  `main_test.go` y `base_test.go` los **creó** T9.5 (`576ba9a`, F9-01) y T1.13 los **reutiliza** sin recrearlos
  ([`../F1-nucleo-contact/tareas.md`](../F1-nucleo-contact/tareas.md), T1.13). T9.5 no «amplió» nada.

## Contradicciones encontradas (con `04`/`05`/`ESTADO`/docs, medidas el 2026-09-28)

1. **R2 (ESTADO, skill `procesos-testcontainers`)**: «puede exigir una opción solo en el arranque
   nuevo». El código del SDK dice que no (D-F9-2). Pendiente de confirmar en T9.11.
2. **`documentations/operacion.md` §2.4 y §5.3** sitúan el `HeadBucket` en
   `internal/publicapi/flows.go:75`. Está en `internal/bootstrap/arranque/flows.go:75`
   (`objectstore.NewR2PresignClient`) → `internal/platform/storage/objectstore/r2_factory.go:54`.
3. **`05` E-6: «20 paquetes con adaptador Postgres, 8 con gemelo en memoria»**. Medidos **22** con SQL
   (`grep -rlE '"database/sql"|pgx' --include='*.go' internal | grep -v _test`, más `platformadmin`,
   que usa `*sql.DB` por `access_requests.go` y `postgres.go`): además de los 20, `flujos/events` y
   `flujos/runtime` (`tenant_resolver.go`). Y de los 12 «sin gemelo», **6 sí tienen** implementación en
   memoria con otro nombre de fichero: `gateway/enroll` (`store.go:55`, `edgecert.go:44`),
   `gateway/fleet` (`fleet.go:407`), `gateway/lease` (`repository.go:70`), `ingest` (`dedupe.go:31`),
   `intentcfg` (`store.go:53`), `diagnostics` (`diagnostics.go:115`)
   (`grep -rn '^func NewMemory' --include='*.go' internal | grep -v _test`). Recuento único (22 con
   SQL, 15 con gemelo, 7 sin él —`entitlements` sí lo tiene: `Fake`—) en
   [`../00-marco/tecnologia.md`](../00-marco/tecnologia.md) §9.
4. **`05` §1 y §7: «107 ficheros de integración»**. Medido: **97** `*_integration_test.go`
   (`find internal cmd -name '*_integration_test.go' | wc -l`) y **132** ficheros que dependen de BD
   (unión con `grep -rlE 'WAPP_TEST_DB_DSN|openTestDB|testDB\(' --include='*_test.go' internal cmd`).
   Los **135** usos de `WAPP_TEST_DB_DSN` sí cuadran (`grep -rn 'WAPP_TEST_DB_DSN' --include='*.go' . | wc -l`).
5. **`05` §7: «siguen protegiendo el código viejo hasta el relevo, y se borran con él»**. No todos:
   9 viven en `internal/platform/`, que no se borra (D-F9-4).
6. **Skill: «el proveedor LLM falso, como en el e2e del Plan 044»**. La vía `api` **no se puede
   apuntar a un doble**: `internal/llmvia/llmvia.go:275` construye `api.Config` sin `BaseURL` (el
   campo existe en `wapp-shared/llm@v0.4.5/api/api.go:84`). El único LLM falso posible sin tocar
   código es **el Edge de prueba respondiendo `InferenceRequest`** (vía `local`, la de todo tenant
   sin fila en `tenant_llm`: `llmvia.go:201-208`). Un proceso con vía `api` llamaría al proveedor
   real: prohibido (cero gasto).
7. **Skill: «KEK del cifrado de PII, con el proveedor local»**. El proveedor se llama **`env`**
   (`WAPP_KEK_PROVIDER`, default en `internal/platform/config/config.go:654`).
8. **Variables de entorno: `03`/`contratos.md` dicen 70.** Medido **71**: 69 por `loader.Get*`
   (`awk '/^func Load\(\)/,/^}/' internal/platform/config/config.go | grep -oE 'loader\.Get[A-Za-z]+\("[A-Z_0-9]+"' | sort -u | wc -l`)
   + `FLOW_REPLY_RATE` por `getFloat` (`config.go`, fuera del patrón) + `WAPP_CONFIG_FILE` por
   `os.Getenv` (`config.go:735`). Afecta a la huella de variables de F0, no a F9.
9. **`05` §7.2**: «`make test-integration` … puerto 5432». Es el **default**, sobrescribible
   (`INTEGRATION_PG_PORT ?= 5432`, `Makefile:21`). El vicio de fondo (contenedor vivo, nombre fijo
   `wapp-cloud-platform-pg-test`) sí es cierto.
10. **T9.2 no puede pasar su propio gate**: `go mod tidy && git diff --exit-code go.mod go.sum` borra un
    `require` que ningún fichero importa, y T9.2 va antes de T9.5 (que importa testcontainers). Se resolvió con
    `test/procesos/deps_test.go` (imports en blanco, comentados), que T9.5 borró. Y el prefijo del commit es
    `chore(deps)` (decisión T-2: «commit aislado»), no el `procesos(arnes)` que dice `tareas.md`.
11. **`key_source=config` (R9.3.a, `diseno.md` §4 P0, `arquitectura.md` §2)**: solo lo emite la clave de cifrado de la
    nube (`internal/bootstrap/arranque/pki.go:105`). La del **lease** emite `base64` con `WAPP_LEASE_PRIVATE_KEY_B64`
    (`internal/gateway/lease/signingkey.go:21`, `lease.go:44`), nunca `config`. P0 aserta cada una con su mensaje exacto.
12. **«Los 17 nombres estáticos `wapp_*` de `contratos.md` §8 salen sin tráfico»**: solo **9** (`wapp_db_*` ×6 —`internal/platform/metrics/metrics.go:482-502`; decía «×7», que sumaba 10: corregido en la revisión del 2026-10-01—,
    `wapp_flow_autoreply_streak{,_max}`, `wapp_edge_inference_reporting_edges`). Siete `CounterVec` no aparecen hasta su
    primer incremento (T-10), cuatro familias de Edge solo salen con un Edge reportando, y los dos `wapp_http_*` solo con
    tráfico (el sondeo del propio arnés ya lo provoca). La lista medida y los motivos están en `p0MetricasSinTrafico`.
13. **Detalles de código que la spec tenía desplazados**: `UsePathStyle=false` está en `r2_factory.go:47` (no `:51`); el
    bucket del S3 falso es `wapp-procesos`, así que la petición es `HEAD /wapp-procesos` (no `/procesos`). **D-F9-2 queda
    confirmada ejecutándola** (T9.11): una sola `HEAD`, path-style, `Host: 127.0.0.1:<p>`, en los dos binarios.
14. **Lista blanca de imports (`arquitectura.md` §3)**: no nombra `google.golang.org/grpc`, `google.golang.org/protobuf` ni
    el módulo raíz `identity-shared/auth` (para `ErrTokenExpired`), pero son inevitables para hablar el contrato de
    `wapp-cloudlink` y para el doble de identidad. La regla que se comprueba de verdad es la de `internal/`:
    `go list -tags integracion -deps ./test/procesos | grep 'wapp-cloud-platform/internal/'` vacío.
15. **R9.1.d y R9.1.a no se miden con los comandos de la spec en una máquina con `/var/run/docker.sock`**:
    `DOCKER_HOST=unix:///nada` no basta (testcontainers prueba ese host, falla y cae al socket por defecto, y la corrida
    pasa), y `docker ps --filter ancestor=postgres:17-alpine` da 0 siempre si la imagen local es
    `mirror.gcr.io/postgres:17-alpine` (VM web). Se midió con un espacio de montajes privado que tapa `/run`
    (`unshare --mount --propagation private`; no toca el daemon) y contando `docker ps -a --format '{{.Image}}'`. En la
    máquina local con Docker Desktop el socket puede vivir en otra ruta: ahí el comando de la spec puede servir o no.
    ✅ **Medido en local (F9-02, macOS + Docker Desktop, 2026-10-01)**: el comando de la spec **tampoco sirve**:
    `DOCKER_HOST=unix:///nada make test-procesos` da `RC=0 · PASS=146` (cae al socket de Docker Desktop por el contexto
    `desktop-linux`, `~/Library/Containers/com.docker.docker/Data/docker-cli.sock`; `/var/run/docker.sock` no existe en este
    Mac, así que no es ese el fallback). **Lo que sí mide R9.1.d sin parar Docker**: anular también lo que testcontainers
    descubre desde `HOME` (contexto y `~/.docker/run/docker.sock`), fijando el entorno de Go a mano porque `HOME` es donde
    `go` busca su caché: `GOCACHE=$(go env GOCACHE) GOPATH=$(go env GOPATH) GOMODCACHE=$(go env GOMODCACHE)
    GOENV=$(go env GOENV) HOME=$(mktemp -d) DOCKER_HOST=unix:///nada WAPP_PROCESOS_BINARIO=viejo GOWORK=off go test -tags integracion -count=1 -v
    ./test/procesos/` → `rc=1`, `procesos: no se pudo levantar Postgres (¿hay Docker?)`, 0 PASS y 0 SKIP (control: el mismo
    comando con el `HOME` real pasa). Vale donde no hay `/var/run/docker.sock`; en Linux con ese socket sigue haciendo falta
    el `unshare`. ⚠️ **El orden de las asignaciones importa** (revisión independiente, 2026-10-01): en una misma línea se
    evalúan de izquierda a derecha y cada `$(go env …)` ya ve el `HOME` asignado antes. Hasta hoy este comando llevaba
    `HOME=$(mktemp -d)` **delante**: así `GOCACHE`, `GOPATH` y `GOENV` salen **bajo el `HOME` vacío** (medido en bash 3.2.57, zsh 5.9,
    `sh` y `dash` de macOS; `GOMODCACHE` también, salvo que esté exportada en el entorno) y «fijar el entorno de Go a mano» no fijaba
    nada. El veredicto de R9.1.d no cambia (los dos órdenes dan `rc=1` y el mismo mensaje, 0 PASS y 0 SKIP), pero con `HOME` delante
    se compila sin caché (16 s frente a 3 s, y 324 MB de caché nueva dentro del directorio temporal) y, en una máquina sin
    `GOMODCACHE` exportada, `go` buscaría los módulos en una caché vacía (no medido: aquí está exportada). En cambio **R9.1.a sí se mide con su comando literal en este Mac** (la imagen está etiquetada
    `postgres:17-alpine`): `docker ps --filter ancestor=postgres:17-alpine -q | wc -l` muestreó **1** durante la corrida y
    **0** a los ~15 s (el *reaper* de Ryuk); el ID de imagen sin etiqueta que muestran los contenedores viejos de otros
    proyectos no lo contamina.
16. **Dos refinamientos del arnés, sin efecto observable**: el servidor recibe `HOME=<directorio temporal vacío>` en vez
    del `HOME` del desarrollador (así el SDK de AWS no lee `~/.aws`), y `TestMain` compila `cmd/migrate` y **solo el
    binario elegido**, no los tres.
17. **Para quien orqueste con sub-agentes**: `Agent(isolation: "worktree")` crea el *worktree* desde un commit viejo
    (`2da10b4`, el de `main`), no desde la rama de la sesión; los tres primeros agentes de este bloque tuvieron que
    verificar sobre una copia exportada. Los siguientes trabajaron en el árbol de la sesión, con ficheros disjuntos.
18. **Alcance del Edge de prueba**: `TestArnes_EdgeFrames` ejerce contra el servidor real rutas que B1 recorrerá como
    procesos (mensajes, diagnóstico, revocación de lease). Está aquí como autoprueba del Edge (sin ella el Edge solo habría
    quedado compilado); B1 puede reutilizar o aligerar esos casos. ✅ F9-02 intentó refutarlo leyéndolo y **no lo logró**:
    asierta sobre efectos del servidor real (filas de `ingest_dedupe`, `message_receipts` y `fleet_sessions`, líneas de log,
    códigos HTTP, el Ack con el mismo `command_id`), no sobre lo que el propio Edge se manda. Se queda.
19. **`TestP0_Arranque/sin_errores` es intermitente** (medido en F9-02; **diferida a F6** por decisión de Jhoan, 2026-10-01). La pasada 1 de `make
    test-procesos` dio `viejo RC=0 · PASS=146` y `nuevo RC=1 · PASS=144 FAIL=2` (`TestP0_Arranque` y su subtest `sin_errores`).
    Las dos líneas `ERROR` son del *webhook worker* y caen en el instante de la parada: `webhook worker: rescatar entregas con
    el claim vencido … lookup localhost: operation was canceled` y `webhook worker: reclamar lote … context canceled`. Línea
    temporal: listeners arriba a las 32.0645, SIGTERM a las 32.1967 (**132 ms** después), y la primera llamada a la BD del
    worker (`recoverOrphans` y `pollOnce`, que corren al arrancar; `internal/integrations/worker.go:209` y `:225`) aún no había
    terminado. P0 afirma «medido: cero ERROR, parada incluida»; eso solo es cierto si esa primera llamada acaba antes del SIGTERM.
    - **No es un hallazgo R9.4.c** (el viejo y el nuevo no se apartan): el worker es el mismo paquete en los dos binarios y
      `fase9_fondo.go` del viejo y del nuevo difieren solo en una línea de comentario. Pero **no lo reproduje en el viejo**:
      161 arranques en frío de P0 (81 en el viejo y 80 en el nuevo: 30 invocaciones por binario en reposo, 20 bajo 12 procesos
      `yes` saturando los 8 núcleos, 25 con `docker ps` a 5 Hz, y las pasadas completas, `CUENTA=3` y la del `DOCKER_HOST`)
      dieron **1 fallo**, y fue contra el nuevo. La muestra (≈80 por binario) no distingue un binario de otro; la prueba de
      que es compartido es estructural, no empírica.
    - **Quién queda expuesto**: solo el primer servidor de cada proceso de `go test`, que tarda ≈ 610–890 ms en arrancar en este
      Mac (media 697 ms en el viejo y 703 ms en el nuevo, 80 procesos de cada uno; los siguientes, ≈105 ms) y es siempre P0. Cada `make test-procesos` tiene un P0 en frío por binario; T9.30 tendrá dos.
      Con 1/161 ≈ 0,6 % por P0 en frío (intervalo ancho), un ≈ 1–2 % de falso rojo por corrida final.
    - **No se tocó ni el test ni producción.** ✅ **Decisión de Jhoan (2026-10-01): se difiere a F6.** `internal/integrations` se reconstruye
      en F6 (T6.12 y T6.20) y muchas cosas se rehacen allí de cero: arreglar ahora el test de P0 —o el worker viejo, que además **no se toca**,
      porque es lo que corre en UAT— sería gastar tiempo en algo que probablemente se redefine. Lo que sí se hace es **dejarlo anotado donde F6 lo
      encuentre**: la decisión **D-F6-7** (README de F6 y `DECISIONES.md`), una nota en T6.12, T6.20 y T6.27, la fila de `deuda.md` §5 y la regla
      de `diseno.md` §4. En F6 se evalúa: (i) que el contrato del worker nuevo prometa «contexto cancelado → vuelve sin loguear a `ERROR`»;
      (ii) si con eso `sin_errores` de P0 deja de ser intermitente o hay que **redefinir** el criterio (comprobar el log **antes** de la parada,
      o aceptar solo cancelaciones posteriores a «señal de parada recibida, cerrando»); y (iii) si un test más acorde lo sustituye. Las salidas
      que se barajaron: (a) en el test, que `p0SinErrores` ignore los `ERROR` de cancelación posteriores a la señal de parada; (b) en el test,
      que P0 espere a la primera vuelta del worker (no hay una señal observable hoy); (c) que el worker **reconstruido** no loguee a `ERROR`
      cuando `ctx.Err() != nil` (en el viejo, no). **Hasta F6**, un rojo de `sin_errores` con exactamente esas dos líneas `ERROR` es esta
      carrera y no una regresión: se repite la corrida **una vez**, se compara y se anota; cualquier otro rojo no lo es
      (⚠️ regla **matizada** en la nota de revisión que sigue).
    - ⚠️ **Revisión independiente (2026-10-01, sobre `dev` @ `6650e55`).** Tres hechos comprobados en el código y una matización de la
      regla de triaje. **No cambian la decisión D-F6-7 ni su criterio**, que son de Jhoan; lo que abren es la pregunta **D-F9-10**
      (contradicción 28).
      - **Hecho 1 · el mismo patrón está en otras tres de las cinco goroutines de fondo.** `fase9_fondo.go` lanza cinco
        (`internal/bootstrap/arranque/fase9_fondo.go:54`, `:63`, `:75`, `:84` y `:99`; una línea más abajo en la copia `internal/arranque`).
        Además del *webhook worker*, tres llaman a la BD nada más arrancar y loguean a `ERROR` sin mirar `ctx.Err()`:
        el colector `internal/platform/metrics/flowlifecycle/collector.go` (`Run` `:204` → `pollOnce` `:215` → `becomeLeader`: `Error` en
        `:255` «reservar conexión para el advisory lock» y `:263` «intentar el advisory lock»; ya líder, `:348` «leer max(id) inicial» y
        `:372` «consultar flow_events»), el agregador `internal/flujos/runtime/aggregator.go` (`Run` `:699` → `RecoverAtBoot` `:703` →
        `Sweep` → `:741` «agregador: no se pudieron listar las ventanas vivas») y el pipeline `internal/intake/pipeline/pipeline.go`
        (`Run` `:423` → `Drenar` `:451` → `:547` «pipeline: no se pudo reclamar trabajo»; el bucle mira `ctx.Err()` **antes** de reclamar,
        `:544`, no al volver con error). La quinta (`internal/intakeahead/intakeahead.go:335`) espera en una cola y no toca la BD al
        arrancar. **Ninguna de las tres se ha observado** en una corrida: es la misma carrera con otra firma en el log.
      - **Hecho 2 · esos tres paquetes no se reconstruyen en F6.** El colector es `platform` (no se reconstruye ni se borra: lo enlazan
        los dos binarios), el agregador es `flujos/runtime` (F8) y el pipeline es `intake/pipeline` (F7). Y el binario `viejo`
        (`cmd/server`) conserva el worker viejo hasta el relevo (F10): D-F6-7 dice que el worker viejo no se arregla. Con el worker nuevo
        cableado en `nuevo`, `viejo` sigue expuesto por las cuatro goroutines y `nuevo`, por las otras tres.
      - **Hecho 3 · una remedición con `CUENTA=3` no distingue «arreglado» de «sin arreglar».** Con las cifras de arriba (1/161 ≈ 0,6 %
        por arranque en frío expuesto), `CUENTA=3 make test-procesos` tiene **2** arranques expuestos (uno por binario: `-count=3` repite
        dentro del mismo proceso de `go test`, y solo el primer servidor arranca en frío) y da verde (160/161)² ≈ **98,8 %** de las veces
        sin haber arreglado nada.
      - **La regla de triaje, matizada.** Es esta carrera, y no una regresión, un rojo de `sin_errores` cuyas líneas `ERROR` cumplan
        las tres condiciones: (1) **todas** son de cancelación (`context canceled` u `operation was canceled`); (2) las emite una
        goroutine de fondo (prefijos `webhook worker:`, `colector de telemetría de flow_events:`, `agregador:` o `pipeline:`); y
        (3) caen en la parada: junto a la línea `señal de parada recibida, cerrando` (`internal/bootstrap/arranque/servir.go:53`,
        `internal/arranque/servir.go:54`; `p0MsgSenal`, `test/procesos/p0_arranque_test.go:37`). ⚠️ «Posteriores a la señal» **no** es exigible línea a línea: la línea de la señal y el `ERROR` salen del
        mismo `ctx.Done()` en goroutines distintas (`servir.go:52-53` frente al retorno de la llamada a BD) y su orden en el log no está
        garantizado (leído en el código, no observado). «Exactamente esas dos líneas» era estrecho por dos lados: las otras tres
        goroutines darían otro texto, y el propio worker puede dar **una sola** línea (si la cancelación cae durante `pollOnce`, con
        `recoverOrphans` ya de vuelta, solo sale la de `worker.go:225`). Un rojo que no cumpla las tres condiciones no es esta carrera.
        El procedimiento no cambia: se repite **una vez**, se compara y se anota.
20. **La regla del `Cleanup` («el servidor sale con 0») no tiene la escapatoria que decía el traspaso.** `limpiar` vuelve a
    llamar a `Parar`, que es idempotente y devuelve **el mismo código**; así que un proceso que mata al servidor a propósito y
    llama a `Parar` antes falla igual con `el servidor no paró limpio: código de salida -1` (medido con un test temporal, ya
    retirado: `SIGKILL`, `Parar`, y el `Cleanup` lo suspendió, en viejo y en nuevo). La regla sigue siendo correcta —un servidor que
    muere solo, o por señal, **debe** fallar el test—, pero quien necesite matarlo adrede (p. ej. un proceso que simule una caída)
    tendrá que añadir una marca explícita de «salida esperada» en `servidor`; hoy no existe. Se decide en el primer proceso que
    la necesite (B1/B2); no se construye por adelantado.
21. **Medido en local además** (F9-02): (a) el Go del sistema es `1.27.1` y `golangci-lint` `2.14.0`, y `make lint` aborta con
    ellos (por diseño, T-1): se usó `GOTOOLCHAIN=go1.26.5` y `golangci-lint v2.12.2` instalado en un directorio aparte, sin tocar
    el de Homebrew (✎ 2026-10-02: hoy lo hace solo el `Makefile` —`GOTOOLCHAIN` exportado y `make tools`—; `06` §6);
    (b) **huérfanos** (§7.1 del traspaso): con `kill -9` al binario de test en mitad de la suite quedó **1 servidor
    vivo en t+0 y 0 a los 3 s** (muere por SIGPIPE en su siguiente escritura al log, que ya no tiene lector), y el contenedor
    Postgres desapareció a los ~15 s por el *reaper*; un pánico por *timeout* con `TestP0_Arranque` en marcha dejó 0. No hay
    huérfano persistente en esas dos muestras; un servidor **callado** podría vivir más (no medido); (c) el `-timeout` de `go test`
    cuenta desde `m.Run`, no desde el `TestMain`: la suite entera cabe en ≈ 3 s, el resto de los ≈ 10 s del paquete es el `TestMain`.

### Hallazgos de la revisión independiente de S9–S11 (2026-10-01, sobre `dev` @ `6650e55`)

> Revisión de F9-01, F9-02 y F1-01 (`45e01a4..6650e55`). Son **hechos** comprobados contra el código de `6650e55`; lo que pide una
> decisión está en «Decisiones abiertas por la revisión», arriba, y **no se decide aquí**. Los hallazgos de F1 están en el
> [README de F1](../F1-nucleo-contact/README.md) (21–24). Las correcciones de código viajan en la rama `reorg/revision-s9-s11`.

22. **El candado `SinBDViva` es una lista negra sintáctica, con falsos negativos** (→ **D-F9-6**). `internal/candados/sinbdviva.go`
    persigue formas concretas en el AST (`:95-129` y `patronesLiteral`): literales con `WAPP_TEST_DB_DSN`, con `:5432` o que empiezan
    por `postgres://`/`postgresql://`, el identificador `WithReuseByName`, los selectores `os.Environ` y `testing.Short` **con ese
    nombre de paquete exacto** (`esIdent`, `:132-135`) y `Skip*`. La revisión lo midió con una sonda de ~20 evasiones: mordió 1. El
    alias y el import con punto de `os`/`testing` se corrigieron en el PR de la revisión (`1c247f9`: el candado resuelve por fichero el
    nombre local de `os` y de `testing`, y con import de punto muerde el identificador suelto). Quedan **sin cerrar**: un DSN sin host ni
    puerto (`pgx.Connect(ctx, "")`, `sql.Open("pgx", "dbname=…")`: pgx completa lo que falta con `PGHOST`/`PGPORT` o con sus valores
    por defecto, puerto 5432); un DSN clave=valor (`port=5432`, sin los dos puntos); literales construidos (`"localhost:"+"5432"`,
    `net.JoinHostPort`); `os.Getenv("DATABASE_URL")` o `PGHOST` (el candado declara legítimo `os.Getenv`, `:28-29`); `syscall.Environ`,
    `cmd.Environ()` y un `exec.Cmd` con `Env == nil`, que hereda el entorno entero; `os.Exit(0)` en `TestMain`; y la auto-exención por
    **nombre base** `sin_bd_viva_test.go` en cualquier subdirectorio (`:47`). Hoy `test/procesos` abre conexiones en dos sitios, los
    dos en `base_test.go`: `pgx.Connect` (`:72`) y `sql.Open` (`:188`).
23. **El arnés no fija `GOWORK=off` al compilar** (→ **D-F9-7**). `compilar` (`test/procesos/main_test.go:272`) lanza `go build` con
    `cmd.Env` en `nil`, a propósito (`:278`: «go build necesita el entorno de Go del desarrollador»). En la ubicación real del repo
    hay un `go.work` en la raíz del ecosistema (`wApp/go.work`) cuyas líneas `use` incluyen `./cloud/wapp-cloudlink` y los módulos de
    `./shared/wapp-shared/`: `go test -tags integracion ./test/procesos/` **sin** `GOWORK=off` compila el servidor y el Edge de prueba
    contra los árboles de al lado, no contra las versiones de `go.mod`. `make test-procesos` sí lo fija (`GO := GOWORK=off go`,
    `Makefile:15`); una invocación directa, o la de un IDE, no.
24. **Matiz a H-4 (contradicción 21 b): lo que sí queda huérfano es el directorio temporal** (→ **D-F9-8**). `TestMain` crea
    `procesos-*` con `os.MkdirTemp("", "procesos-")` (`main_test.go:90`) y lo borra con un `defer` (`:96`, `borrarDirectorio`), que no
    corre si el binario de test muere por `kill -9`, pánico o *timeout*. En el `os.TempDir()` del Mac había **tres** del 2026-10-01
    (09:31–09:34; 64 + 64 + 14 MB: los binarios compilados), de los experimentos de F9-02.
25. **Corregido en el PR de la revisión** (rama `reorg/revision-s9-s11`: (a) `af372b6`, (b) `41db3e4`, (c) `5db3a72`, (d) `1e135e5`;
    además `84021ef`, que para en el `Cleanup` el proceso de `TestArnes_Parar*` si el test falla antes). (a) el reintento por puerto ocupado
    (`servidor_test.go:122-132`) relanzaba el servidor contra el **mismo** doble de S3, que quedaba con dos `HeadBucket`: falso rojo
    de `TestP0_Arranque/almacenes_s3`, que exige exactamente uno (`p0_arranque_test.go:354`); (b) el Edge de prueba daba por buena
    una conexión con el lease inicial rechazado y acusaba `SendText` con `ok=true` sin lease vigente o tras la revocación (el Edge
    real responde `Ack{ok=false}`, «lease no vigente»); (c) nada impedía abrir una sesión en la base `plantilla`; (d)
    `make test-procesos` no fallaba con `--- SKIP` (los cuenta, pero su código de salida solo mira el `rc` de `go test`,
    `Makefile:158-164`).
26. **`klauspost/compress` no va en ningún binario de producción** (matiz al §2 del traspaso y a la fila T-2, que dicen «sube lo
    que va en producción»). Es una línea nueva de `go.mod` (`:80`, `// indirect`) que solo usa un test de `promhttp`
    (`go mod why -m github.com/klauspost/compress` → `promhttp.test` → `klauspost/compress/zstd`); `go list -deps` da 0 en
    `./cmd/server`, `./cmd/server-modular` y `./cmd/migrate`. De los **81** módulos enlazados en esos tres binarios (regla:
    `go list -deps -f '{{with .Module}}{{.Path}} {{.Version}}{{end}}' … | sort -u`, cuenta el propio) solo cambian dos entre `a374cdb`
    (el padre de `37c7db7`) y `6650e55`: `httpsnoop` 1.0.4 → 1.1.0 y `otelhttp` 0.67.0 → 0.69.0. Entran por el cliente HTTP de
    `cloud.google.com/go/kms` (`go mod why`: `internal/platform/crypto` → `kms/apiv1` → `google.golang.org/api/transport/http` →
    `otelhttp` → `httpsnoop`); ningún fichero de `cmd/`, `internal/` ni `test/` los importa. `cmd/migrate` no enlaza ninguno de los tres.
27. **Dos preguntas del §8 del traspaso se cerraron en voz de la sesión** (→ **D-F9-9**). «Dejar el alcance extra del Edge» y
    «mantener la regla del `Cleanup`» figuran como hechas en «Qué queda» del `CERRADO` de
    [`TRASPASO-F9-arnes.md`](../../traspasos/TRASPASO-F9-arnes.md), sin fila en [`../DECISIONES.md`](../DECISIONES.md) y sin la
    fórmula «decisión de Jhoan» que sí lleva H-1. El §8 las titulaba «Decisiones que necesitan a Jhoan».
28. **D-F6-7: el alcance y el criterio de remedición quedan abiertos** (→ **D-F9-10**; hechos en la nota de revisión de la
    contradicción 19). La decisión dice «se evalúa al reconstruir `integrations`»; tres de las cuatro goroutines con la carrera no
    son de F6, el binario `viejo` la conserva hasta F10, y la remedición prevista (T6.27 y T9.30, `CUENTA=3`) no discrimina. Las
    tareas llevan la nota: [`../F6-solicitudes/tareas.md`](../F6-solicitudes/tareas.md) T6.27 y [`tareas.md`](tareas.md) T9.30.
29. **Cinco ficheros de test de `test/procesos` pasan de 500 líneas** (→ **D-F9-11**; `wc -l` en `6650e55`): `edge_falso_test.go`
    **2.534** (cuatro temas: transporte y enrolamiento · núcleo y frames · tests sin servidor · tests contra el servidor real),
    `servidor_test.go` **1.033**, `clientes_test.go` **750**, `pki_test.go` **653** y `p0_arranque_test.go` **611** (de este
    convendría sacar el parser de exposición Prometheus, que reutilizarán otros procesos). El paquete suma 7.785 líneas en 12
    ficheros `*_test.go`. Tras el arreglo del gate de lease (`41db3e4`), `edge_falso_test.go` tiene **3.012**.
30. **Lo que el Edge de prueba sigue sin replicar del Edge real, tras el arreglo de la 25 (b)** (→ **D-F9-12**; salió al escribir
    el arreglo, leyendo `wapp-edge-agent`, `internal/adapters/cloudlink/`): (a) **la inferencia no pasa por el lease**: el doble la
    sirve siempre, también revocado; el Edge real aplica un gate propio (`inferencia.go`, `leaseVigente`: de alcance daemon —basta
    una sesión operable—, con gracia, y sin ninguna contesta un error de lease), así que un proceso «tras revocar no se infiere»
    pasaría en falso; (b) **el doble no late solo**: con el gate, un proceso que dure más que el TTL del lease vería sus envíos
    bloqueados si no llama a `latir` (hoy ninguno se acerca; lo dice el comentario de `puedeOperar`); (c) un envío bloqueado solo
    se ve por el `Ack` o por la respuesta de la API: el doble no cuenta bloqueos ni los anota en `Errores()`, y un proceso donde
    envía el motor de flujos (no HTTP) tendría que mirar el estado del servidor; (d) el doble acusa `ok=true` cualquier comando que
    no interpreta si trae `command_id` (*esto último no se contrastó línea a línea con el Edge real: queda como PLAUSIBLE*).
