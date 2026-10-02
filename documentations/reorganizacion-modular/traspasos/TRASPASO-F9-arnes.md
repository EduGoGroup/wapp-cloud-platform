# Traspaso F9 · el arnés de procesos — de la web (F9-01) a la local (F9-02, bloque A)

Contexto: F9 · bloque A (T9.1–T9.12) escrito y **pre-chequeado en la web** el 2026-10-01 (sesión F9-01, adelantada
por D-F9-1): `test/procesos/` con el arnés (Postgres 17 por testcontainers, una base clonada y un servidor por proceso,
PKI/claves/S3/identity de prueba, Edge de prueba) y el proceso **P0** (humo del arranque), más el candado ampliado,
`make test-procesos` y `vet-integracion` en `ci-local`. Falta lo que solo cierra la local: `make test-procesos` contra
los dos binarios en tu máquina e integrar la rama en `dev`.

═══ 0. BLOQUEANTE ═══

Ninguno. Para repetir los gates hace falta `GOTOOLCHAIN=go1.26.5`, `golangci-lint v2.12.2` en el `PATH` y Docker en
marcha (`docker info >/dev/null; echo rc=$?` → `rc=0`). Esta rama parte de `origin/dev` @ `45e01a4`.

═══ 1. Rama y commits ═══

Rama: `reorg/f9-a-arnes` (empujada), sobre `origin/dev` @ `45e01a4`. Commits, en orden:

| SHA | Tarea | Commit |
|---|---|---|
| `a374cdb` | T9.1 | docs: F9, decisiones D-13 y D-F9 |
| `37c7db7` | T9.2 | chore(deps): testcontainers-go v0.44.0 para test/procesos |
| `f300aff` | T9.3 | procesos(arnes): el candado sin_bd_viva prohíbe os.Environ y t.Skip |
| `b5f1601` | T9.4 | procesos(arnes): make test-procesos y vet con la etiqueta integracion |
| `576ba9a` | T9.5 | procesos(arnes): un Postgres por corrida y una base clonada por proceso |
| `2e2ecc1` | T9.6 | procesos(arnes): PKI, lease, X25519, KEK y ES256 generados por corrida |
| `7c63d9e` | T9.7 | procesos(arnes): dobles de S3 e identity en el proceso de test |
| `660947d` | T9.8 | procesos(arnes): el binario elegido, con base, puertos y entorno propios |
| `fa03e6b` | T9.9 | procesos(arnes): cliente con Context Token y fixtures sin puerta |
| `5519343` | T9.10 | procesos(arnes): el Edge de prueba, con mTLS, lease y sellado |
| `10179d6` | T9.11 | procesos(arranque): el binario completo arranca en el arnés |
| (este y el siguiente) | T9.12 | docs: traspaso del arnés y cierre documental |

Nada sin empujar. **Integrar SIN squash.** Aviso: la rama se reescribió una vez con `--force-with-lease` (T9.5–T9.7: el
borrado de `deps_test.go` había caído en el commit equivocado); si tienes una copia anterior de la rama, descártala.

═══ 2. go.mod ═══

`go 1.26.5` intacta; `go.sum` generado con red real (`proxy.golang.org`). Añade `testcontainers-go v0.44.0` y
`modules/postgres v0.44.0` (+35 módulos indirectos) y, **por selección de versiones, sube lo que va en producción**
(decisión T-2, aceptada): `felixge/httpsnoop` 1.0.4 → 1.1.0, `otelhttp` 0.67.0 → 0.69.0 y `klauspost/compress` 1.18.6.
Verificar:

```bash
git diff origin/dev -- go.mod | grep -E '^[-+]' | grep -E 'httpsnoop|otelhttp|klauspost|testcontainers|^[-+]go '
GOWORK=off go mod tidy && git diff --exit-code go.mod go.sum; echo rc=$?          # 0
GOWORK=off go list -deps ./cmd/server ./cmd/server-modular | grep -c testcontainers # 0 (no llega al binario)
```

═══ 3. Gates que la web corrió ═══

Toolchain: `go1.26.5` y `golangci-lint v2.12.2` (**autoritativa**). Cada `rc` leído del log, sin pipe.

| Gate | Resultado |
|---|---|
| `GOWORK=off make ci-local` (con `vet-integracion` dentro) | `GATE_RC=0`, 84 paquetes `ok`, `0 issues`, `FICHEROS_EVALUADOS=10` · `POR_DEBAJO=0` |
| `go vet -tags pendiente ./...` · `make test-pendiente` | rc=0 · `PENDIENTES=0` · `ROJOS=0` |
| `go test -v ./test/procesos/ ./internal/candados/... \| grep -c -- '--- SKIP'` (sin etiqueta) | 0 |
| `GOWORK=off go test -race -cover ./internal/candados/...` | rc=0; `sinbdviva.go` 100 % por fichero |
| `go list -tags integracion -deps ./test/procesos \| grep 'wapp-cloud-platform/internal/'` | vacío |
| `grep -rn 'certs/\|\.env' test/procesos` · `WAPP_PROCESOS_BINARIO` fuera de `main_test.go` · `time.Sleep` | vacío · vacío · vacío |
| `make test-integration` (INTEGRATION_PG_PORT=55441, tras T9.2, en un *worktree* de `37c7db7`) | `rc=0` — **sin `-v`**: no hay conteo de PASS/SKIP; la comparación con los 4.618 PASS de F0-06 es de la local |

**Pre-chequeo web de F9 (no cierra nada):** `make test-procesos` (la VM web trae Docker y
`TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX=mirror.gcr.io/`; hubo que arrancar `dockerd` a mano al empezar):

```text
viejo: RC=0 · PASS=146 FAIL=0 SKIP=0 · /tmp/procesos-viejo.log      (dos pasadas seguidas, idénticas)
nuevo: RC=0 · PASS=146 FAIL=0 SKIP=0 · /tmp/procesos-nuevo.log
CUENTA=3 → viejo y nuevo RC=0 · PASS=438 FAIL=0 SKIP=0
```

- Duración: ≈ 23 s de pared por pasada de **los dos binarios** con la caché de Go caliente; un binario con `GOCACHE`
  vacío tardó 57 s (compilar `server-modular` 25,9 s). Postgres listo ≈ 3 s, `cmd/migrate` ≈ 0,7 s, servidor listo ≈ 105 ms.
  *(Mide la local su propia cifra para T9.30; hoy el dato de esta VM web es el único.)*
- `-race -count=3` (servidor) y `-count=8` (Edge) sin carreras. `address already in use`: 0 en todos los logs.
- R9.1.d: sin Docker el `TestMain` sale con `RC=1` y `procesos: no se pudo levantar Postgres (¿hay Docker?)`, 0 SKIP
  (medido con un espacio de montajes privado; ver §7.2). R9.4.a: `WAPP_PROCESOS_BINARIO=otro` → `rc=1` y mensaje.
- Contenedores: 1 `postgres:17-alpine` durante la corrida, 0 después (queda el *reaper* ~15 s).
- **D-F9-2 CONFIRMADA EJECUTÁNDOLA** (T9.11): `s.S3.Peticiones()` = una `HEAD /wapp-procesos` con `Host: 127.0.0.1:<p>`.
  Con endpoint IP, el SDK usa path-style aunque `UsePathStyle=false`. No hace falta ninguna opción nueva en el arranque.

═══ 4. Lo que solo la sesión local puede hacer ═══

```bash
git fetch origin && git checkout -B reorg/f9-a-arnes origin/reorg/f9-a-arnes
export GOTOOLCHAIN=go1.26.5 ; golangci-lint version          # 2.12.2
docker info >/dev/null 2>&1; echo docker_rc=$?                # 0

# 1 · T9.5 / T9.8 / T9.11 · el cierre de verdad, contra los dos binarios, DOS veces
make test-procesos ; make test-procesos
#   viejo: RC=0 · PASS=146 FAIL=0 SKIP=0   y   nuevo: RC=0 · PASS=146 FAIL=0 SKIP=0   (cada vez)
#   (leer el RC= del log, no del make; los logs son /tmp/procesos-viejo.log y /tmp/procesos-nuevo.log)
CUENTA=3 make test-procesos                                    # PASS=438 por binario, 0 SKIP, 0 FAIL
docker ps -a --format '{{.Image}}' | grep -c 'postgres:17-alpine'   # 0 al terminar (esperar ~20 s al reaper)

# 2 · R9.1.d y R9.4.a (ver §7.2: el comando de la spec puede no servir donde exista un socket por defecto)
WAPP_PROCESOS_BINARIO=otro GOWORK=off go test -tags integracion -count=1 ./test/procesos/ ; echo rc=$?   # ≠ 0
# sin Docker: parar Docker Desktop/daemon, o el método de unshare de §7.2; esperado: rc≠0 y 0 SKIP

# 3 · el gate completo y la integración vieja con -v (T-2: las dependencias de producción cambiaron)
GOWORK=off make ci-local > /tmp/g.log 2>&1; echo "GATE_RC=$?" >> /tmp/g.log; tail -1 /tmp/g.log     # GATE_RC=0
INTEGRATION_PG_PORT=55441 GOWORK=off make test-integration > /tmp/i.log 2>&1; echo "RC=$?" >> /tmp/i.log
#   referencia F0-06: 4.618 PASS · 0 SKIP · 0 FAIL (la web solo midió rc=0 sin -v)

# 4 · integrar SIN squash (F-1) y empujar
git checkout dev && git merge --no-ff origin/reorg/f9-a-arnes && git push origin dev ; echo push_rc=$?
```

Después: `## CERRADO <fecha>` al final de **este** fichero (el hook `SessionStart` da por abierto todo traspaso sin esa
línea), marcar T9.5, T9.8, T9.11 y T9.12 `[x]` en `plan/F9-procesos/tareas.md` con el SHA del merge, y actualizar
`ESTADO.md`. **Si P0 falla solo contra uno de los dos binarios**: es un hallazgo de la reconstrucción (R9.4.c); se
anota en §7 de este fichero y **no se toca el test**.

═══ 5. Lo que quedó sin tocar ═══

- Producción (viejo y nuevo), el arranque y las migraciones: **cero líneas**. `git diff --name-only origin/dev` fuera de `test/procesos/`, `internal/candados/` y `documentations/` solo trae `.golangci.yml`, `Makefile`, `go.mod` y `go.sum`; y `git diff --name-only origin/dev -- internal cmd` fuera de `internal/candados/` está vacío.
- P1–P10, el guion de inferencia (`guion_test.go`, T9.17) y el CRM falso (T9.19): bloques B1/B2.
- Las suites de contrato contra Postgres (bloque C) y la corrida final (bloque D).
- `documentations/operacion.md` §3 (fila `make test-procesos`): lo hace T9.32.
- `make test-integration` y los 132 ficheros de integración vieja siguen como estaban (protegen el código viejo hasta F10).

═══ 6. Integración en dev ═══

`git merge --no-ff origin/reorg/f9-a-arnes` en `dev` (PR a `dev` abierto desde la rama): rojo y verde, y los commits por
tarea, se conservan. Antes, `git fetch origin && git log --oneline -1 origin/dev`: si `dev` avanzó (F1 corre en paralelo
y no comparte ficheros), `git merge origin/dev` en la rama y repetir `ci-local` sobre esa base.

═══ 7. Tres cosas que quiero que revises con ojo crítico, no que aceptes ═══

**7.1 · Lo que pasa «en la web» pasa con la caché caliente, en una VM de 4 vCPU, y yo no lo vi en tu máquina.** La suite
no tiene esperas fijas (todo sondea con tope), pero los topes (30 s al arrancar, 10 s al conectar el Edge, 15 s de
parada) están medidos contra un servidor que arranca en ≈ 105 ms. Si en local hay intermitencias, sospecha primero de
un tope, no del servidor. `-parallel 4` se probó con tres bases a la vez; no con más. Si el proceso de test muere por
pánico o *timeout*, los `Cleanup` no corren y el servidor queda huérfano (sin `Pdeathsig`: exigiría un fichero solo
Linux): `pgrep -f servidor-` y `docker ps -a` tras una corrida rota.
**→ F9-02: hubo una intermitencia y no era un tope de espera (es una carrera con la parada, `TestP0_Arranque/sin_errores`, solo vista
contra `nuevo` pero con código compartido); los huérfanos existen y se autolimpian en segundos. Ver `CERRADO`, H-1 y H-4.**

**7.2 · Cómo medí «sin Docker» y «sin contenedores» no es lo que dice la spec.** R9.1.d manda
`DOCKER_HOST=unix:///nada make test-procesos`: aquí **pasa** (testcontainers prueba ese host, falla y cae a
`/var/run/docker.sock`). Lo medí tapando `/run` con un tmpfs dentro de `unshare --mount --propagation private`. En tu
máquina el socket puede estar en otra ruta, así que el comando original podría sí funcionar: **refútalo en local** y
corrige R9.1.d con el método que valga. Igual `docker ps --filter ancestor=postgres:17-alpine`: da 0 siempre si la imagen
local es `mirror.gcr.io/postgres:17-alpine` (VM web); allí conté `docker ps -a --format '{{.Image}}'`.
**→ F9-02: refutado, el comando de la spec tampoco sirve en macOS (cae al contexto `desktop-linux`); el método que vale y su
control están en `CERRADO`, H-3. Lo del `ancestor=` sí funciona en el Mac (la imagen está etiquetada).**

**7.3 · Cosas que hicieron los sub-agentes y que la spec no pedía; revisa si las quieres.**
(a) `arrancar` hace `t.Errorf` en el `Cleanup` si el servidor no sale con código 0 tras SIGTERM (una regla nueva: si
algún proceso mata al servidor a propósito, hay que usar `Parar` antes — **→ F9-02: falso, `Parar` no es escapatoria, `CERRADO` H-2**); (b) `TestArnes_EdgeFrames` ejerce contra el
servidor real mensajes (`/api/v1/messages`, `/admin/messages/send`), diagnóstico y revocación de lease: es terreno de
B1, aquí como autoprueba del Edge, y el fixture `edgeAltaAdminDelTenant` (porque `platform_admin` no tiene
`messages.send`) vive en `edge_falso_test.go` y B1 puede subirlo a `fixtures_test.go`; (c) el Edge **solo en unitario**:
la inferencia real (se verá con el guion de T9.17), entrante → respuesta (P3) y el push de `intents`; (d) P0 mide 9 métricas
sin tráfico, no las 17 de `contratos.md` §8 (contradicción 12): conviene que alguien confirme que no se está dejando
fuera una que sí debería salir; (e) 7.8 mil líneas de test en total (el Edge 2,5 mil y `servidor_test.go` 1 mil):
muestrea, no leas todo; las trampas de sus propios nombres son las que los agentes anotaron como «helpers que otros
ficheros podrían pisar».

═══ 8. Decisiones que necesitan a Jhoan ═══

1. ¿Se acepta el alcance extra del Edge de prueba (`TestArnes_EdgeFrames`, §7.3 b), o se reduce a lo mínimo que pedía
   T9.10 antes de B1? Recomendación: **dejarlo**; es lo que demuestra que el Edge habla con el servidor real.
2. ¿Se queda la regla «el servidor tiene que salir con 0 al terminar el test» (`Cleanup`, §7.3 a)? Recomendación: sí.
3. Las contradicciones 11 y 12 del README de F9 ya están aplicadas a `diseno.md` y `arquitectura.md`; **15** (R9.1.d)
   queda abierta hasta que la local mida el comando en su máquina.


## CERRADO 2026-10-01

Sesión **F9-02** (💻), sobre `dev` @ `af7b8e9` (el PR #18 ya estaba fusionado sin squash y la rama `reorg/f9-a-arnes` ya no existía: no se
hizo el paso 2 del protocolo ni los comandos de la §4 que la citan). Toolchain: `GOTOOLCHAIN=go1.26.5` y `golangci-lint v2.12.2`. **El
sistema trae `go1.27.1` y `golangci-lint 2.14.0`, que no sirven** (`make lint` aborta por diseño, T-1): la `v2.12.2` se instaló en un
directorio aparte, sin tocar la de Homebrew. Cada `RC` se leyó del log, no del `make`.

### Qué hice (medido en el Mac: 8 núcleos, Docker Desktop, caché de Go caliente)

| Corrida | `viejo` | `nuevo` | Pared |
|---|---|---|---|
| `make test-procesos`, pasada 1 (con un muestreador de `docker ps` a 1 Hz en paralelo) | `RC=0 · PASS=146 · FAIL=0 · SKIP=0` | **`RC=1 · PASS=144 · FAIL=2`** (`TestP0_Arranque` y `…/sin_errores`) | 31 s |
| pasada 2 | `RC=0 · 146 · 0 · 0` | `RC=0 · 146 · 0 · 0` | 25 s |
| pasada 3 | `RC=0 · 146 · 0 · 0` | `RC=0 · 146 · 0 · 0` | 26 s |
| `CUENTA=3 make test-procesos` | `RC=0 · PASS=438 · 0 · 0` | `RC=0 · PASS=438 · 0 · 0` | 33 s |

- Dos verdes consecutivos (pasadas 2 y 3) y `CUENTA=3` verde, **pero la pasada 1 dio un rojo contra `nuevo`** y no se descarta: ver H-1.
- Contenedores: 1 `postgres:17-alpine` durante la corrida (muestreado), 0 a los ~15 s (*reaper*); 0 servidores huérfanos tras las pasadas.
  `address already in use`: 0 en 167 logs de la sesión (los 12 «puerto ocupado… reintento» son `TestArnes_ReintentoPuertoOcupado`, adrede).
- **Gate `GOWORK=off make ci-local`: `GATE_RC=0`** (135 s) · 84 paquetes `ok` · `0 issues` · `FICHEROS_EVALUADOS=10 · POR_DEBAJO=0`:
  idéntico a lo que midió la web. `go vet -tags pendiente ./...` rc=0 · `make test-pendiente` `PENDIENTES=0 · ROJOS=0` ·
  `-race -cover ./internal/candados/...` rc=0 · `go list -tags integracion -deps ./test/procesos | grep 'wapp-cloud-platform/internal/'`
  vacío · `certs/`/`.env`, `WAPP_PROCESOS_BINARIO` fuera de `main_test.go`, `time.Sleep`, `t.Skip`/`Short`/`os.Environ`: todos vacíos ·
  `-v` sin etiqueta sobre `./test/procesos ./internal/candados/...`: 0 SKIP.
- **T-2 · integración vieja con `-v`**: `GOFLAGS='-v -count=1' INTEGRATION_PG_PORT=55441 GOWORK=off make test-integration` →
  **`RC=0 · 4.631 PASS (3.283 de primer nivel) · 0 SKIP · 0 FAIL`**, 79 paquetes `ok` + 9 sin tests = 88, 128 s, contenedor retirado.
  Referencia de F0-06: 4.618 (3.281) y 79 `ok`. **Los +13 (+2) están explicados**: `internal/candados` + `test/procesos` pasan de 105 a
  118 PASS (de 32 a 34 de primer nivel), medido en un *worktree* temporal de `835a7be` frente a HEAD; son los casos del candado de
  T9.3 y nada más, el resto de paquetes no cambió.
- **`go.mod`/`go.sum` con red real**: `GOWORK=off go mod tidy` sin cambios (`git diff --exit-code go.mod go.sum` → 0), `go mod verify`
  «all modules verified», `go 1.26.5` intacta, `go list -deps ./cmd/server ./cmd/server-modular | grep -c testcontainers` → 0 y 0;
  versiones: `httpsnoop` 1.1.0, `otelhttp` 0.69.0, `klauspost/compress` 1.18.6, `testcontainers-go` y `modules/postgres` 0.44.0.

### Qué refuté de la §7 (contra el código y contra lo que corre)

| § | Afirmación | Veredicto |
|---|---|---|
| 7.1 | «las intermitencias, si las hay, serán un tope de espera» | **Parcial**: hubo una (H-1) y **no** es un tope: es una carrera con la parada. Ningún tope (30 s / 10 s / 15 s) llegó a saltar en 161 arranques en frío; el arranque más lento fue de 889 ms (P0 contra el viejo) |
| 7.1 | «si el proceso de test muere, el servidor queda huérfano» | **Cierto pero transitorio** (H-4): con `kill -9` quedó 1 servidor en t+0 y 0 a los 3 s |
| 7.2 | R9.1.d: `DOCKER_HOST=unix:///nada` quizá sí funcione en tu máquina | **Refutado**: da `RC=0 · PASS=146`; corrección con su control en H-3 |
| 7.2 | `ancestor=postgres:17-alpine` da 0 siempre | **Solo en la VM web**: en el Mac sí mide (1 durante, 0 después) |
| 7.3 a | la regla del `Cleanup` y su escapatoria «usar `Parar` antes» | **La regla se sostiene; la escapatoria es falsa** (H-2) |
| 7.3 b | el alcance extra de `TestArnes_EdgeFrames` | **No refutado**: asierta efectos del servidor real (filas, log, HTTP, Ack), no tautologías. Se queda |
| 7.3 d | P0 mide 9 métricas, no 17: ¿falta alguna? | **No falta ninguna**: `/metrics` sin tráfico da 11 familias `wapp_*` (las 9 + las 2 `wapp_http_*`), **idénticas en viejo y nuevo**; las 7 declaradas que no salen son todas `prometheus.NewCounterVec` (T-10) |
| §3 | los gates de la web | **Reproducidos** con mi toolchain, sin diferencias (ver arriba) |

### Hallazgos

- **H-1 · `TestP0_Arranque/sin_errores` es intermitente — DIFERIDA A F6 (decisión de Jhoan, 2026-10-01).** Detalle y cifras en la contradicción 19 del README de F9.
  Resumen: P0 manda SIGTERM ~130 ms después de arrancar y la primera llamada a BD del *webhook worker* puede seguir en vuelo; el worker
  loguea dos `ERROR` (`internal/integrations/worker.go:209` y `:225`) y P0 afirma «cero ERROR». **No es R9.4.c**: el worker es el mismo
  paquete en los dos binarios y `fase9_fondo.go` difiere solo en un comentario; no lo reproduje en el viejo, pero 1 fallo en 161
  arranques en frío (81 viejo, 80 nuevo) no distingue un binario del otro. Solo está expuesto el primer servidor de cada proceso de
  `go test` (≈ 610–890 ms de arranque en el Mac, con medias de 697 ms en el viejo y 703 ms en el nuevo, frente a ≈105 ms): P0, siempre. **No toqué el test ni producción** (la regla es no ajustar
  el test cuando solo falla el nuevo; F9 no toca `internal/**`). Salidas: (a) el test ignora `ERROR` de cancelación posteriores a la señal
  de parada, (b) P0 espera a la primera vuelta del worker, (c) el worker no loguea a `ERROR` con `ctx.Err() != nil` (solo en el reconstruido: el viejo no se toca).
  **Decisión de Jhoan (2026-10-01): no se arregla ahora, se evalúa en F6** al reconstruir `integrations` (D-F6-7), donde muchas cosas se rehacen de cero y el test
  probablemente se redefine. Queda anotado en `deuda.md` §5, `plan/F9-procesos/diseno.md` §4 y T6.12/T6.20/T6.27. Hasta F6: ≈ 1–2 % de falso rojo por corrida.
- **H-2 · «usar `Parar` antes» no evita el fallo del `Cleanup`.** `limpiar` vuelve a llamar a `Parar` (idempotente, mismo código): un
  servidor muerto a propósito suspende el test igual (`el servidor no paró limpio: código de salida -1`, medido con un test temporal
  retirado, viejo y nuevo). Quien necesite matarlo adrede tendrá que añadir una marca de «salida esperada»; no se construye por adelantado.
- **H-3 · R9.1.d corregido** (README de F9, contradicción 15, y `requisitos.md`): `HOME` vacío + `DOCKER_HOST=unix:///nada` + el entorno de Go fijado
  a mano da `rc=1`, el mensaje `procesos: no se pudo levantar Postgres (¿hay Docker?)`, 0 PASS y 0 SKIP; el control (mismo comando con el
  `HOME` real) pasa. En Linux con `/var/run/docker.sock` sigue haciendo falta el `unshare` de la web.
- **H-4 · huérfanos** (contradicción 21): `kill -9` al binario de test → 1 servidor a t+0, 0 a los 3 s (SIGPIPE en su siguiente escritura al log);
  Postgres fuera a los ~15 s; un pánico por *timeout* con `TestP0_Arranque` en marcha dejó 0. Un servidor **callado** podría vivir más: no medido.

### Qué queda

- **H-1 diferida a F6 (decisión de Jhoan, 2026-10-01; D-F6-7)**: no se arregla ahora. Mientras tanto, un rojo de `sin_errores` con esas dos líneas exactas es esta carrera (se repite una vez y se anota); cualquier otro rojo no lo es.
- §8 del traspaso: (1) **dejar** el alcance extra del Edge, no se refutó; (2) **mantener** la regla del `Cleanup`, con H-2 anotado; (3) la contradicción 15 **ya está corregida**.
- **No corrido**: `make ci-docker` (el segundo gate del ecosistema; no se pidió), el arranque real de `cmd/server-modular` (no lo pide este bloque), UAT. De 7.3 (c) queda
  lo que sigue sin verse: la inferencia real, entrante → respuesta (P3) y el push de `intents`; es terreno de T9.15/T9.17.
- No se tocó `main`; no se empezó F1 ni B1. Producción (`internal/**`, `cmd/**`): cero líneas; `test/procesos/` sin cambios (un test temporal creado y retirado, árbol limpio).
- Siguiente: **F1-01** (el piloto `nucleo/contact`); B1 (F9-03) va tras la parada de F1 (F1-05), por D-F9-1 y el orden de `plan/sesiones/`.
- ⚠️ **Revisión independiente (2026-10-01)**: este traspaso está cerrado y no se reescribe; la regla de triaje de H-1 («esas dos líneas exactas») y su alcance se matizan en la contradicción 19 del [README de F9](../plan/F9-procesos/README.md) (nota de revisión), y lo demás que encontró la revisión, en sus contradicciones 22–29. Aquí solo se corrigieron dos erratas de referencia: el nombre del test `TestArnes_ReintentoPuertoOcupado` y la ruta `plan/F9-procesos/diseno.md` §4.
