# Traspaso F0 · andamiaje — de la web (F0-05) a la local (F0-06, bloque F)

Contexto: F0 · bloque E cerrado en la web el 2026-09-30 (sesión F0-05): `internal/apipublica`
montada vacía delante del `publicapi` viejo, los tres ✎ de `platform`, los barridos AST viejos
ciegos al árbol nuevo y la deriva documental. Falta el bloque F (💻): la integración vieja con
Postgres real, el arranque real de `cmd/server-modular` e integrar F0 en `dev`.

═══ 0. BLOQUEANTE ═══

Ninguno para empezar. Para **T0.24** (integrar en `dev`), antes tiene que estar fusionado en `dev` el
PR de F0-04 (`reorg/f0-d-arranque-huella`). Esta rama parte de `origin/dev` @ `d3deb27`, que ya lo
contiene (merge del PR #16).

═══ 1. Rama y commits ═══

Rama: `reorg/f0-e-cara-platform` (empujada), sobre `origin/dev` @ `d3deb27`. Commits, en orden:

| SHA | Tarea | Commit |
|---|---|---|
| `8096232` | T0.20 | docs: F0 cierra la deriva de rutas del arranque |
| `5a11f5b` | TX.1 (T0.16) | rojo(apipublica): contratos de la Cara y del estrangulador |
| `6d83620` | T0.17 | ✎ platform/httpapi deja de depender de gateway/session |
| `b65b788` | T0.18 | ✎ platform/httpapi deja de depender de iam/ports/in |
| `5305134` | T0.19 | ✎ platform/metrics deja de depender de inferstats |
| `de0c29b` | TX.2 (T0.16) | verde(apipublica): la Cara y el estrangulador (un commit para los dos ficheros: comparten ayudantes de test) |
| `9dcf7e8` | TX.3 (cierra T0.16) | la cara nueva vacía delante de la vieja |
| `7b7e01f` | TX.4 | candado de mudanzas de la cara HTTP |
| `dd1e2bd` | T0.27 | los barridos AST viejos no ven el árbol nuevo (D-F4-1) |
| (este y el siguiente) | T0.21 | traspaso y cierre documental |

Nada sin empujar. **Integrar SIN squash**: rojo (`5a11f5b`) y verde (`de0c29b`) son commits distintos.

═══ 2. go.mod ═══

Sin cambios: `git diff --stat origin/dev -- go.mod go.sum` vacío. `go 1.26.5` intacto; no hay
dependencia nueva (`apipublica` solo importa la stdlib).

═══ 3. Gates que la web corrió ═══

Toolchain: `go1.26.5` y `golangci-lint v2.12.2` (autoritativa). Cada gate, en un *worktree* limpio
del commit y **siempre en la misma ruta**, `/tmp/wt-gate` (contradicción 21):

| Commit | `GOWORK=off make ci-local` |
|---|---|
| `5a11f5b` (rojo) | `GATE_RC=0` |
| `de0c29b` (✎ ×3 + verde) | `GATE_RC=0` |
| `7b7e01f` (TX.3 + TX.4) | `GATE_RC=0` |
| `dd1e2bd` (último con código) | `GATE_RC=0` (1 min 16 s con caché) |

Sobre `dd1e2bd`, en el *worktree*:

- `go vet -tags pendiente ./...` → rc=0.
- `make test-pendiente` → rc=0, `PENDIENTES=0`, `ROJOS=0`.
- `make cobertura-ficheros` → rc=0: `FICHEROS_EVALUADOS=10`, `POR_DEBAJO=0` (`apipublica.go` y
  `estrangulador.go` al 100,0 %).
- `go test -count=1 -v ./internal/apipublica/... ./internal/arranque/...` → rc=0: 159 PASS, 0 SKIP,
  0 FAIL.
- `go list -f '{{join .Imports "\n"}}' ./internal/platform/... | grep -cE 'internal/(gateway|iam|inferstats)'`
  → `0`.
- `git diff --stat origin/dev -- internal/bootstrap` → vacío. El arranque viejo no cambió.

**Huella** tras cada paso (T0.16, T0.17, T0.18, T0.19, TX.4):

- `go test -count=1 -run '^TestHuella' -v ./internal/arranque/ ./internal/bootstrap/arranque/` →
  rc=0 (`TestHuella`, `TestHuellaEstatica`, `TestHuellaVieja`).
- `git diff --quiet -- internal/arranque/testdata/huella.json` → 0. **La dorada no cambió**: 95 = 22
  + 73, 2 rpc, 11 familias, 10 goroutines, 13 *hooks*, entorno ∅.
- Bloques gemelos «Contenedor de huella»: `diff` vacío.

**Tests viejos tocados por los ✎** (sin editarlos), todos rc=0:

- `go test ./internal/platform/httpapi/ ./internal/gateway/... ./internal/publicapi/ ./internal/flujos/admin/`;
- `go test ./internal/iam/...`;
- `go test -v ./internal/platform/metrics/... ./internal/inferstats/...`: 32 PASS y **8 SKIP**.
  Los 8 son `flowlifecycle/collector_integration_test.go`, integración vieja sin
  `WAPP_TEST_DB_DSN`; **T0.22 los tiene que ver en PASS**.

**Muerde** (salidas pegadas en cada commit):

- el cableado de la cara (TX.3) con un `Componer` sobre otro mux y un segundo `InstrumentHTTP`;
- el candado de mudanzas (TX.4) con una ruta en la cara nueva y `FaseActual = 0`;
- los barridos viejos (T0.27) con `internal/modulos/zz/zz.go`: RC=1 antes de la línea, RC=0 después.

**No corrido en la web**: la integración vieja con Postgres real (T0.22) y el arranque real de
`cmd/server-modular` (T0.23).

═══ 4. Lo que solo la sesión local puede hacer ═══

Copiado literal de `plan/F0-andamiaje/tareas.md` (bloque F):

- **T0.22 · integración vieja con Postgres real tras los ✎** · 💻 · dep. T0.19, T0.21 · cumple R0.7.f
  - **Por qué**: los ✎ tocan código viejo compartido por los dos arranques (DT-52: 438 tests
    saltados con la pantalla en verde, `05` E-5).
  - **Qué se corre**: `INTEGRATION_PG_PORT=<libre> make test-integration` **y**, para contar,
    la misma batería con `-v` a un log. El `Makefile` ya exporta `WAPP_TEST_REQUIRE_DB=1`, pero
    solo **50** de los **91** ficheros de test que leen `WAPP_TEST_DB_DSN` lo honran:
    - `grep -rln WAPP_TEST_REQUIRE_DB --include='*.go' . | wc -l` → 50;
    - `grep -rln WAPP_TEST_DB_DSN --include='*_test.go' . | wc -l` → 91 (2026-09-28).

    Los otros 41 se saltarían en silencio si la BD fallara. Por eso se cuentan los SKIP.
  - **Hecho cuando**: `rc=0`; `grep -c -- '--- SKIP'` → `0`; `--- FAIL` → `0`; y el nº de
    `--- PASS` ≥ el de `ESTADO.md` (4.318, 2026-09-27) o la diferencia explicada.
  - **Commit**: ninguno (se anota en el `CERRADO` de este traspaso).

- **T0.23 · arranque real de `cmd/server-modular` en local** · 💻 · dep. T0.22 · cumple R0.4.f
  - **Qué se corre**: el binario nuevo, **nunca a la vez** que `cmd/server`, contra una base
    **desechable** (contenedor efímero en puerto libre, jamás UAT ni el Postgres compartido) y
    con el R2/MinIO de desarrollo que ya usa Jhoan (`flows.go:75` hace `HeadBucket` y sin él no
    arranca). Es la única prueba de F0 de las fases 1 y 3 **reales** del arranque nuevo.
  - **Hecho cuando**: el log trae las nueve líneas `arranque: fase completada` (`9/9`), `curl
    -s :8100/healthz` 200, y se apaga limpio con SIGINT (`servidor detenido limpiamente`).
  - **Añadido de F0-05**: con el binario arriba, una ruta del `:8103` que no sea pública (p. ej.
    `curl -s -o /dev/null -w '%{http_code}' :8103/api/v1/auth/tenants` sin token) responde lo
    mismo que el binario viejo (401), y una inexistente da `404 page not found`. Así se ve que el
    compuesto no cambia nada en caliente.
  - **Commit**: ninguno.

- **T0.24 · integrar F0 en `dev`** · 💻 · dep. T0.22, T0.23 · cumple R0.9.b
  - **Qué se hace**: gate ci-local en local con la toolchain fijada; merge de la rama de la web
    **sin squash** (`rojo`/`verde` distintos, E-4); `git push origin dev` leyendo su `rc`.
  - **Hecho cuando**: `git log origin/dev` contiene los commits de T0.1–T0.21 en orden.

Después, T0.25: cerrar F0 en la documentación.

═══ 5. Lo que quedó sin tocar ═══

- **`internal/arranque/auth.go`**: no se partió. Es T2.34 de F2 (D-F2-8).
- **Comentario caducado en código viejo**: `internal/iam/ports/in/usecases.go:~155-158` dice
  «internal/platform/httpapi ya importa este paquete». Desde T0.18 es al revés. No se toca (E-1:
  D-F0-3 permite solo la línea del alias).
- **`Makefile:123`** (`test-integration` con `postgres:16`): lo arregla F9/F10, no F0.
- **Dos referencias sin directorio** que siguen siendo ciertas: `tareas.md` T0.23 cita `flows.go:75`,
  que hoy es `internal/bootstrap/arranque/flows.go:75`. En la copia es `internal/arranque/flows.go:76`:
  la copia desplaza +1 línea por la cabecera.
- **TX.10 (F3)**: `edge/session` nuevo declarará `var ErrSessionOffline = httpapi.ErrSessionOffline`.
  El centinela ya vive en `internal/platform/httpapi` desde T0.17 (D-F3-2).

═══ 6. Integración en dev ═══

1. Fusionar primero F0-04 si no lo está: ya está (PR #16, `d3deb27`).
2. Luego esta rama **tal cual, sin squash ni reordenar** (`git merge --no-ff` o «Rebase and merge»).
   El orden de los commits ya es el de la dependencia:
   - T0.20 es solo documentación;
   - los ✎ no dependen de `apipublica`;
   - TX.3 necesita el verde.
3. Tras integrar, T0.25: `CERRADO <fecha>` aquí, y en `ESTADO.md` y el README de F0 poner F0
   cerrada y F1 (o 9A, D-F9-1) siguiente.

═══ 7. Tres cosas que quiero que revises con ojo crítico, no que aceptes ═══

1. **La simulación de las fases 1 y 3 es la misma en los dos lados, y por eso la huella no prueba
   las reales.**
   - `huella_test.go` y `huella_vieja_test.go` comparten el bloque «Contenedor de huella»: `diff`
     vacío, verificado tras cada paso.
   - La fase 1 va simulada; la fase 3, entera contra un S3 falso `httptest` (contradicción 19).
     Que las dos huellas coincidan dice que el cableado de fases 2–8 expone lo mismo, **no** que
     las fases 1 y 3 reales arranquen.
   - **T0.23 es la única prueba real**. Refútalo arrancando `cmd/server-modular` de verdad.
   - Lo mismo vale para el compuesto: `TestMudanzas_HuellaPorElCompuesto` resuelve las 73 rutas
     por `Compuesto.Resolver` sobre el contenedor de huella. En ejecución real solo lo ve T0.23.
2. **Los alias de D-F0-3 no cambian ningún texto, y los tipos son el MISMO tipo.** Lo que doy por
   cierto porque compila y pasan los tests viejos:
   - **`ErrSessionOffline`**: `session.ErrSessionOffline == httpapi.ErrSessionOffline`, el mismo
     valor (`var X = httpapi.X`), no otro `errors.New`. Los 5 consumidores viejos
     (`gateway/grpc/inference.go:320,354`, `publicapi/flows.go:235`, `publicapi/messages.go:212`,
     `flujos/admin/handlers.go:326`) siguen casando con `errors.Is`. **Sin verificar**: el 502 de
     `/admin/messages/send` con una sesión offline **en ejecución**. Solo lo cubre
     `admin_senderror_test.go`, que construye el error a mano.
   - **`in.AuditInput` y `inferstats.Agregado`** son alias (`=`): el `AuditService` y
     `(*inferstats.Store).Agrega` satisfacen las interfaces de `platform` sin adaptador.
   - **Sin verificar**: que la auditoría real escriba los seis campos igual (integración con
     Postgres, T0.22: `internal/iam/...` y `publicapi`), y que `/metrics` publique las cinco
     `wapp_edge_*` con un Edge real. Solo lo afirma el test unitario `inferstats_test.go`.
3. **`go.sum` no cambió, pero hubo tres desviaciones de la spec.** `go.mod`/`go.sum` sin diff. Las
   tres desviaciones las decidió el usuario en la sesión, y hay que mirarlas:
   - **(a) `Agregado`** vive en un paquete hoja nuevo, `internal/platform/metrics/inferencia`, y no
     en `platform/metrics` como decía la spec. La razón: `platform/metrics/inferstats_test.go` es
     `package metrics` e importa `inferstats`, y eso daría «import cycle not allowed in test».
     **Refuta**: que ningún test **viejo** fuera de los que corrí —p. ej. los de integración de
     `internal/gateway/grpc`, que publican partes de inferencia— dependa de que `Agregado` sea un
     tipo definido y no un alias. Un `reflect.TypeOf(...).PkgPath()` lo delataría.
   - **(b) La copia de `internal/arranque` se desvía en tres ficheros**, no solo en `http.go`:
     `buildPublicAPIServer` devuelve también el `*apipublica.Compuesto`, `fase8_transporte.go` lo
     guarda y `contenedor.go` tiene el campo `publicCompuesto`. Más dos ficheros nuevos,
     `mudanzas.go` y `cara_nueva_cableado_test.go`. Así la prueba `diff` de T0.10 ya **no** sale
     «solo cabeceras». Es esperado desde T0.16, pero cualquier `diff` futuro contra el viejo tiene
     que saberlo.
   - **(c) El ✎ de T0.27** en `c2_via_test.go` son +14 líneas: un ayudante `esDelArbolNuevo`, porque
     la condición en línea subía `gocyclo` a 18. No es «una línea».

═══ 8. Decisiones que necesitan a Jhoan ═══

- **Revisar las desviaciones 23, 24 y 27 del README de F0**: el paquete hoja `inferencia`, la
  copia desviada en tres ficheros y la regla 4 de fronteras frente a RX.6.a.
- **La contradicción 27 no bloquea F0, pero sí F2.** `internal/candados/fronteras.go:196` (regla 4)
  prohíbe **cualquier** import de `apipublica` a código viejo, «ni con puente». RX.6.a de FX sí
  admite puentes declarados. Si una ola de FX necesita un puente desde `apipublica`, hay que
  decidir cuál manda **antes** de esa ola.
- Sigue pendiente de F0-04: la contradicción 19 y aplicar **F0-A-1** en claude.ai/code.
