# F0 · Andamiaje — reglas

> Lo que es propio de esta fase. Las reglas de siempre (`05`, la constitución, las del
> ecosistema) no se repiten: se enlazan desde [`../00-marco/`](../00-marco/README.md).

## 1 · Lo que no se toca

| Qué | Por qué | Única salvedad en F0 |
|---|---|---|
| `internal/bootstrap/**` (fachada y los 21 ficheros del arranque viejo) | Es el **oráculo** y lo que corre en UAT (`05` E-1, §4) | añadir `huella_vieja_test.go` (test, D-F0-2) |
| Los paquetes viejos de dominio | Referencia y UAT (E-1) | una línea de alias en `gateway/session/registry.go:22`, `iam/ports/in/usecases.go:129`, `inferstats/inferstats.go:144` (D-F0-3); y, en tests viejos, una línea en `llmvia/c2_via_test.go` y otra en `iam/infra/postgres/membresia_unica_ast_test.go` para que no barran el árbol nuevo (D-F4-1, T0.27) |
| `internal/platform/**` | Lo comparten los dos arranques | los tres ✎: `httpapi/admin.go`, `httpapi/audit_mw.go`, `metrics/inferstats.go` |
| `cmd/server/**` | Es el binario que se despliega | ninguna |
| `go.mod` / `go.sum` | testcontainers entra en F9, con su decisión (T-2 del marco) | ninguna: T0.0 prueba testcontainers **fuera** del árbol |
| `internal/platform/storage/postgres/migrations/**` | Runner full-replay (I-CP-7) | ninguna |
| `documentations/literal-aviso-sesion-pasiva.md` | Contrato congelado | ninguna |

## 2 · Trampas conocidas (con `fichero:línea`)

1. **R2 en la fase 3.** `internal/bootstrap/arranque/flows.go:75` construye siempre el cliente
   de presign y `r2_factory.go:64` hace `HeadBucket` *virtual-hosted* (`:51`): sin R2 el
   arranque muere. **No** se «arregla» con una opción en el arranque viejo: la huella simula ese
   grupo (`diseno.md` §6.3) y la prueba real es T0.23 en local. Si el arranque nuevo necesita
   otra cosa en F9, la decisión es de Jhoan (`ESTADO.md`).
2. **Los dos campos diferidos.** `contenedor.go:128` (`intakeService`, lo lee una clausura de
   `fase5_captacion.go:228-231`) y `:142` (`intakeAggregator`, lo lee la `SinkFunc` de
   `fase7_flujos.go:150-152`). La copia los conserva **igual**: resolver al llamar, no al
   construir. «Simplificarlo» con un setter o reordenando fases rompe el ciclo que cortan.
3. **Los handlers de plataforma se construyen inline.** `rutas_admin.go:76-91`: el candado
   I-CP-5 (`platform_permissions_test.go`) reconoce una ruta de plataforma por el texto
   `"platformadmin."` dentro de `adminHandler(…)`. Moverlos a campos de `adminRouteDeps` deja el
   candado **ciego sin ponerse rojo** (`deuda.md` D-13).
4. **Dos rutas relativas en los tests copiados.** `invitaciones_cableado_test.go:98` y
   `roleplane_cableado_test.go:69` leen `../../publicapi/roleplane.go`; desde `internal/arranque`
   es `../publicapi/roleplane.go`. Falla ruidoso (fichero no encontrado), pero hay que tocarlo.
5. **Un test viejo con `t.Skipf`.** `pool_metrics_integration_test.go:68` se salta sin
   `WAPP_TEST_DB_DSN`: **no** se copia (E-5).
6. **`signal.NotifyContext` está dos veces** (`cmd/server/main.go:32` y
   `orquestador.go:77`), y `servir.go:34` lleva un `//nolint:contextcheck` con su porqué. La
   copia los conserva: no es duplicación a limpiar en F0.
7. **El rate-limit del `:8103`** (`http.go:192-194`, 20 rps / ráfaga 40 por defecto): 73 sondas
   seguidas darían 429 y harían aparecer `wapp_ratelimit_hits_total` según el tiempo de cada
   corrida. El contenedor de huella sube los límites; el candado sondea **siempre** los mismos
   candidatos **en el mismo orden** (un `CounterVec` solo aparece tras su primer incremento,
   trampa 6 de `constitucion.md`).
8. **`POST /api/v1/signup` se registra en dos ramas** (`http.go:165` y `:168`, según haya
   cliente M2M): un patrón, dos perfiles. Por eso la huella lleva los perfiles «mínimo» y «con
   M2M»; con uno solo, la otra rama no se compara.
9. **El centinela tiene que ser el mismo valor, no el mismo texto.** En el ✎ de `admin.go`,
   `gateway/session` debe hacer `var ErrSessionOffline = <el de platform>`; un `errors.New` con
   el mismo texto hace que `errors.Is` (`admin.go:306`) deje de casar y la respuesta pase de
   **502** a **500** sin un solo error de compilación. Lo mismo para los tipos: **alias** (`=`),
   no tipo definido.
10. **`WAPP_TEST_REQUIRE_DB=1` no basta.** El `Makefile` lo exporta en `test-integration`, pero
    solo **50** de los **91** ficheros que leen `WAPP_TEST_DB_DSN` lo honran (medido
    2026-09-28): los otros 41 se saltarían con la BD caída y `rc=0`. En T0.22 **se cuentan los
    SKIP con `-v`** (DT-52).
11. **`make test-integration` levanta `postgres:16` con nombre fijo y puerto 5432**
    (`Makefile:57`): en la máquina de Jhoan el 5432 está ocupado; se usa
    `INTEGRATION_PG_PORT=<libre>`. Y UAT corre 17: la versión la arregla F9, no F0.
12. **Homónimo DEK.** El keyring de prueba que usa el contenedor de huella
    (`crypto.NewEnvKeyProvider`) es la KEK del **envelope de PII de negocio** de este repo, con
    un valor de test; nada que ver con la DEK del ADR-0007 (fuera de este repo: *la DEK que
    descifra el almacén de `whatsmeow` la custodia el cliente y nunca cruza el contrato*).
13. **La VM web trae PostgreSQL 16 preinstalado** (`../00-marco/tecnologia.md` §6): prohibido
    usarlo para cualquier test; sería un Postgres vivo.

## 3 · Decisiones de F0 que no necesitan a Jhoan (tomadas aquí, con su porqué)

- **`sin_pendientes_test.go` no nace en F0: nace en F10.** Nacer «apagado» exigiría una tercera
  etiqueta (D-11 fija dos: `pendiente` e `integracion`) o un `t.Skip` (prohibido, E-5). Hasta
  F10, la cifra la da `make test-pendiente`.
- **`sin_bd_viva_test.go` va sin etiqueta `integracion`**, aunque la skill
  `procesos-testcontainers` diga «todo con `//go:build integracion`»: con la etiqueta solo
  correría en `make test-procesos` (local, F9) y no mordería en `ci-local`. Se corrige la skill
  en el mismo commit (T0.8).
- **`make cobertura-ficheros` entra en `ci-local`**: un candado fuera del gate no muerde.
- **Los adaptadores Postgres se eximen por una marca verificada en el fichero**, no por nombre
  ni por lista (`diseno.md` §4.5).
- **`lint` no mira los ficheros con `//go:build pendiente`**; `vet-pendiente` los compila.
- **`cmd/server-modular` no lleva copia del e2e de `cmd/server`** (`diseno.md` §5.3).
- **`make test-procesos` no nace en F0**: lo crea F9 con el arnés (vale cualquiera de las dos
  según `../00-marco/tecnologia.md`; F0 deja solo el candado).

## 4 · Prohibiciones

- 🚫 `git mv`, `sed -i` o reescritura de imports por script sobre `internal/bootstrap` o
  cualquier paquete viejo. La copia es `cp` fichero a fichero hacia `internal/arranque`.
- 🚫 Tocar código viejo fuera de la tabla §1.
- 🚫 `t.Skip` en código nuevo, por ningún motivo.
- 🚫 Una variable de entorno nueva, un `os.Getenv` fuera de `platform/config`.
- 🚫 Levantar `cmd/server` y `cmd/server-modular` a la vez, o cualquiera de los dos contra UAT
  o contra un Postgres que ya estuviera corriendo.
- 🚫 Commitear la sonda de testcontainers de T0.0, o cambiar `go.mod`/`go.sum` en F0.
- 🚫 Regenerar la dorada desde el arranque **nuevo**: solo `TestHuellaVieja -args -actualizar`.
- 🚫 Aplastar commits al integrar en `dev`, o juntar un rojo y su verde en un commit.
- 🚫 Declarar un gate pasado con un `golangci-lint` que no sea `v2.12.2`.
- 🚫 Crear un módulo en `internal/modulos/<m>/`: F0 no crea ninguno.

## 5 · Definición de hecho de F0

F0 está hecha cuando, **en `origin/dev`**:

1. **Huellas idénticas**: `TestHuellaVieja` y `TestHuella` verdes contra la misma dorada (95
   rutas, 2 rpc, familias en frío, 10 goroutines, 13 *hooks*, entorno ∅), después de montar
   `apipublica` y de los tres ✎.
2. **`dev` verde con todos los candados activos y demostrado que muerden**: gate ci-local
   `GATE_RC=0` con `vet-pendiente` y `cobertura-ficheros` dentro; los seis casos `muerde` de
   `internal/candados` y las dos demostraciones de T0.15 anotadas en sus commits.
3. **`make test-pendiente` → `PENDIENTES=0`, `ROJOS=0`.**
4. **Integración vieja verde con Postgres real** tras los ✎: `rc=0`, 0 SKIP, 0 FAIL con `-v`
   (T0.22, 💻).
5. **Entorno web verificado**: §5 de `06-entorno-web.md` escrito (T0.0) y hook commiteado (T0.1).
6. `git diff --stat <base-F0>..origin/dev -- internal/bootstrap cmd/server` → solo
   `huella_vieja_test.go`.
