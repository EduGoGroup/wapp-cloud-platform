# F0 · Andamiaje — requisitos

> Historias y criterios EARS (`../00-marco/plantilla-de-fase.md` §2). Cada criterio nombra el
> comando o el test que lo verifica. Las tareas que los cumplen están en [`tareas.md`](tareas.md).

## H0.1 · El entorno web, verificado y vigilado

> **H0.1** · Como **Jhoan**, quiero que la primera sesión web mida su entorno y que cada sesión
> avise si la toolchain no es la fijada, para **no dar por pasado un gate corrido con otra
> herramienta**.

- **R0.1.a** · CUANDO empiece la primera sesión web, LA sesión web DEBERÁ anotar `go version` en
  `06-entorno-web.md` §5. — Verifica: T0.0, la línea existe.
- **R0.1.b** · SI `golangci-lint version` no es `v2.12.2`, ENTONCES LA sesión web DEBERÁ
  anotarlo y NO DEBERÁ declarar pasado ningún gate de esa sesión. — Verifica: T0.0, §5 de `06`.
- **R0.1.c** · LA sesión web DEBERÁ anotar el `rc` de `GOWORK=off go build ./...`. — Verifica: T0.0.
- **R0.1.d** · LA sesión web DEBERÁ anotar el `rc` y la duración en segundos de `make ci-local`,
  leídos del log (`GATE_RC=`), nunca de una notificación. — Verifica: T0.0.
- **R0.1.e** · CUANDO pruebe testcontainers, LA sesión web DEBERÁ hacerlo en un directorio
  temporal **fuera** del árbol, con `postgres:17-alpine`, sin commitear nada y sin cambiar el
  `go.mod` del repo. — Verifica: `git status --short` y `git diff go.mod go.sum` vacíos tras T0.0.
- **R0.1.f** · MIENTRAS `CLAUDE_CODE_REMOTE=true`, EL hook `SessionStart` DEBERÁ imprimir la
  versión de Go y de `golangci-lint` y avisar si esta no es `v2.12.2`; y EL hook DEBERÁ salir
  con `rc=0` en los dos entornos. — Verifica: los dos comandos del gate de T0.1.

## H0.2 · El rojo se marca, se cuenta y compila

> **H0.2** · Como **la sesión web**, quiero un paquete `pendiente` y una etiqueta que aparte los
> tests en rojo del gate, para **tener `dev` siempre verde sin esconder lo que falta** (`05` E-5).

- **R0.2.a** · EL paquete `internal/pendiente` DEBERÁ exportar una sola función, `Implementar`,
  cuyo error lleva el prefijo `pendiente: ` y el símbolo literal. — Verifica: `pendiente_test.go`.
- **R0.2.b** · SI el símbolo está vacío, ENTONCES `Implementar` DEBERÁ devolver
  `pendiente: (símbolo sin nombre)`. — Verifica: `pendiente_test.go`.
- **R0.2.c** · SI un `_test.go` con `//go:build pendiente` no compila, ENTONCES `make ci-local`
  DEBERÁ fallar. — Verifica: `vet-pendiente` dentro de `ci-local`; demostración de T0.4.
- **R0.2.d** · `make test-pendiente` DEBERÁ imprimir `PENDIENTES=<n>` con `n` = número exacto de
  llamadas `pendiente.Implementar(` en ficheros de producción fuera de `internal/pendiente/`. —
  Verifica: demostración de T0.4 (`PENDIENTES=1` con una llamada añadida).
- **R0.2.e** · `make test-pendiente` NO DEBERÁ fallar porque un test en rojo falle; SOLO DEBERÁ
  fallar si `vet-pendiente` falla. — Verifica: `make test-pendiente; echo rc=$?` con rojos → `rc=0`.
- **R0.2.f** · EL código nuevo de F0 NO DEBERÁ contener `t.Skip`. — Verifica:
  `grep -rn 't\.Skip' internal/pendiente internal/candados internal/modulos internal/arranque internal/apipublica test/procesos` vacío, y
  `GOWORK=off go test -v <esos paquetes> 2>&1 | grep -c -- '--- SKIP'` → `0`.
- **R0.2.g** · DONDE Jhoan acepte T-1, `make lint` DEBERÁ fallar si `golangci-lint` no es la
  `LINT_VERSION` del `Makefile`. — Verifica: T0.26.

## H0.3 · Los candados de fichero muerden

> **H0.3** · Como **Jhoan**, quiero que las reglas de `05` (fronteras, un fichero un test,
> exportados cubiertos, sin BD viva, cobertura por fichero) sean **tests que fallan**, para que
> se cumplan sin depender de que alguien las recuerde.

Regla común a los siete criterios: cada candado DEBERÁ tener en `internal/candados/testdata/`
un árbol `muerde` que produce ≥ 1 violación con fichero y motivo, y un árbol `pasa` que produce
0; y el test sobre el árbol real DEBERÁ afirmar que recorrió > 0 ficheros. — Verifica:
`GOWORK=off go test -race -v ./internal/candados/` → `rc=0`, 0 SKIP.

- **R0.3.a** · SI un paquete de `internal/modulos/<m>` importa otro módulo fuera de su lista
  blanca, ENTONCES `fronteras_test` DEBERÁ fallar nombrando la arista. — Verifica: caso
  `fronteras/muerde`.
- **R0.3.b** · SI un paquete nuevo importa un paquete viejo sin un `Puente` declarado (salvo
  `internal/arranque`), o `internal/apipublica` importa cualquier paquete viejo, o código viejo
  de producción importa algo nuevo, ENTONCES `fronteras_test` DEBERÁ fallar. — Verifica: casos
  `fronteras/muerde`.
- **R0.3.c** · SI un `x.go` del alcance no tiene `x_test.go` y no cumple la **condición** de una
  excepción de E-3, ENTONCES `un_fichero_un_test_test` DEBERÁ fallar. — Verifica: caso
  `unfichero/muerde` (incluye un «puerto» con una función, que ya no es solo de interfaces).
- **R0.3.d** · SI un exportado de `x.go` no aparece como identificador en `x_test.go` (con o sin
  etiqueta `pendiente`), ENTONCES `exportados_cubiertos_test` DEBERÁ fallar. — Verifica: caso
  `exportados/muerde` (incluye un exportado citado solo en un comentario).
- **R0.3.e** · SI un fichero de `test/procesos/` contiene `WAPP_TEST_DB_DSN`, `:5432`,
  `WithReuseByName` o un literal `postgres://`, ENTONCES `sin_bd_viva_test` DEBERÁ fallar, y
  DEBERÁ correr en `ci-local` (sin etiqueta). — Verifica: caso `sinbdviva/muerde`; `go test -v
  ./test/procesos/` lo lista.
- **R0.3.f** · SI un fichero en verde (sin `pendiente.Implementar`) del alcance queda por debajo
  del 80 % de sentencias, ENTONCES `make cobertura-ficheros` DEBERÁ salir con `rc≠0` y
  nombrarlo. — Verifica: caso `cobertura/muerde`.
- **R0.3.g** · DONDE un fichero lleve la marca `// cobertura: adaptador postgres (05 E-6)`, EL
  candado DEBERÁ eximirlo del umbral SOLO si importa `database/sql` o `pgx`. — Verifica: caso
  `cobertura/muerde` con una marca ilegítima.
- **R0.3.h** · *(fuera de la regla común: no es un candado nuevo, es D-F4-1)* SI un fichero del
  árbol nuevo (`internal/{modulos,nucleo,arranque,apipublica,pendiente,candados}`) compara por vía o
  escribe `INSERT INTO public.tenant_members`, ENTONCES los barridos viejos `c2_via_test.go` y
  `membresia_unica_ast_test.go` NO DEBERÁN verlo, y SÍ DEBERÁN seguir viendo todo lo viejo
  (incluido `internal/bootstrap/arranque`). — Verifica: T0.27 (muerde al revés y listas intactas).

## H0.4 · El segundo arranque existe y es el primero

> **H0.4** · Como **la sesión web**, quiero `cmd/server-modular` sobre una copia exacta del
> arranque viejo, para **poder conmutar módulo a módulo** sin tocar el binario que corre en UAT.

- **R0.4.a** · `cmd/server-modular` DEBERÁ compilar y NO DEBERÁ depender de
  `internal/bootstrap`. — Verifica: `go list -deps ./cmd/server-modular | grep -c internal/bootstrap` → `0`.
- **R0.4.b** · `internal/arranque` DEBERÁ ser copia de los 21 ficheros de producción de
  `internal/bootstrap/arranque`, sin más diferencias que la cabecera de origen (y, desde T0.16,
  el montaje de `apipublica` en `http.go`). — Verifica: el bucle `diff` de T0.10.
- **R0.4.c** · EL árbol `internal/bootstrap/` NO DEBERÁ cambiar en F0, salvo el fichero de test
  `huella_vieja_test.go` (D-F0-2). — Verifica: `git diff --stat <base-F0>.. -- internal/bootstrap`.
- **R0.4.d** · LOS tests copiados al arranque nuevo DEBERÁN pasar sin un solo SKIP. — Verifica:
  `go test -race -v ./internal/arranque/` → 53 `Test*` en PASS, 0 SKIP.
- **R0.4.e** · CUANDO reciba SIGINT o SIGTERM, `cmd/server-modular` DEBERÁ apagarse igual que
  `cmd/server` (misma `main.go`, distinta llamada). — Verifica: `diff cmd/server/main.go
  cmd/server-modular/main.go` → solo import y llamada; T0.23.
- **R0.4.f** · CUANDO arranque en local contra una base desechable, `cmd/server-modular` DEBERÁ
  completar las nueve fases y responder 200 en `/healthz`. — Verifica: T0.23 (💻).

## H0.5 · La huella dice si los dos arranques exponen lo mismo

> **H0.5** · Como **la dueña del negocio**, quiero que el arranque nuevo exponga exactamente lo
> mismo que el viejo, para **no notar nada** cuando se despliegue en su lugar.

- **R0.5.a** · EL candado de huella DEBERÁ comparar, por listener, las rutas montadas en
  ejecución de los dos arranques. — Verifica: `TestHuella`, 22 en `:8100` y 73 en `:8103`.
- **R0.5.b** · EL candado DEBERÁ comparar los rpc de `:8101` y `:8102` por `GetServiceInfo`. —
  Verifica: `TestHuella`, 2 rpc.
- **R0.5.c** · EL candado DEBERÁ comparar las familias `wapp_*` de `/metrics` tras las mismas
  sondas en el mismo orden. — Verifica: `TestHuella`.
- **R0.5.d** · EL candado DEBERÁ comparar el multiconjunto de goroutines lanzadas por el
  arranque (10 sentencias `go` hoy). — Verifica: `TestHuella`, parte estática.
- **R0.5.e** · EL candado DEBERÁ comparar el multiconjunto de *hooks* de `*metrics.Metrics` que
  cablea cada arranque (14 usos de 12 métodos hoy). — Verifica: `TestHuella`.
- **R0.5.f** · EL árbol nuevo NO DEBERÁ leer variables de entorno fuera de `platform/config`. —
  Verifica: componente `entorno` = ∅.
- **R0.5.g** · SOLO `TestHuellaVieja` DEBERÁ poder escribir la dorada, y SOLO con `-actualizar`.
  — Verifica: `huella_test.go` no tiene camino de escritura (revisión) y la dorada no cambia
  tras `go test ./internal/arranque/`.
- **R0.5.h** · SI el arranque nuevo pierde una ruta o duplica una goroutine, ENTONCES
  `TestHuella` DEBERÁ fallar nombrándola. — Verifica: las dos demostraciones de T0.15.

## H0.6 · La cara HTTP nueva nace vacía

> **H0.6** · Como **el integrador CRM**, quiero que `/api/v1/integrations/callback` y el resto
> del `:8103` respondan igual con la cara nueva montada, para **no tener que cambiar nada**.

- **R0.6.a** · `internal/apipublica` DEBERÁ existir y NO DEBERÁ registrar ninguna ruta en F0. —
  Verifica: `apipublica_test.go`.
- **R0.6.b** · MIENTRAS la cara nueva esté vacía, EL `:8103` del arranque nuevo DEBERÁ servir
  **todas** las rutas por el `publicapi` viejo, con la huella idéntica. — Verifica: `TestHuella`
  tras T0.16.
- **R0.6.c** · `internal/apipublica` NO DEBERÁ importar ningún paquete viejo. — Verifica:
  `fronteras_test`.

## H0.7 · `platform` deja de depender de dominios

> **H0.7** · Como **la operación de UAT**, quiero que el arreglo de `platform` no cambie nada de
> lo que corre, para **seguir desplegando `cmd/server` sin sorpresas**.

- **R0.7.a** · `internal/platform/httpapi` NO DEBERÁ importar `internal/gateway/...`. —
  Verifica: `go list -f '{{join .Imports "\n"}}' ./internal/platform/httpapi | grep -c internal/gateway` → `0`.
- **R0.7.b** · `internal/platform/httpapi` NO DEBERÁ importar `internal/iam/...`. — Verifica:
  ídem con `internal/iam` → `0`.
- **R0.7.c** · `internal/platform/metrics` NO DEBERÁ importar `internal/inferstats`. — Verifica:
  `go list … ./internal/platform/... | grep -cE 'internal/(gateway|iam|inferstats)'` → `0`.
- **R0.7.d** · LOS textos observables (`"sesión offline"` y el 502 de `admin.go:306-307`, los
  seis campos de auditoría, las cinco series `wapp_edge_*`) NO DEBERÁN cambiar. — Verifica: los
  tests viejos de `httpapi`, `session`, `metrics` e `inferstats` verdes sin editarlos.
- **R0.7.e** · TRAS cada ✎, LA huella DEBERÁ seguir igual a la dorada y EL arranque viejo NO
  DEBERÁ haber cambiado. — Verifica: `TestHuella` y `TestHuellaVieja` verdes; R0.4.c.
- **R0.7.f** · TRAS los ✎, LA sesión local DEBERÁ correr la integración vieja con Postgres real
  y `WAPP_TEST_REQUIRE_DB=1`, con `-v`, y obtener 0 SKIP y 0 FAIL. — Verifica: T0.22 (💻).

## H0.8 · La documentación dice dónde está el arranque

> **H0.8** · Como **la sesión web**, quiero que la documentación nombre las rutas reales del
> arranque, para **no buscar ficheros que ya no existen**.

- **R0.8.a** · LA documentación de `documentations/` NO DEBERÁ afirmar en presente una ruta
  `internal/bootstrap/<fichero>.go` ni `internal/publicapi/flows.go` que no exista. — Verifica:
  el `grep` de T0.20 devuelve solo líneas de historia.

## H0.9 · F0 se cierra en `dev`, entera y trazable

> **H0.9** · Como **la sesión local**, quiero recibir F0 con un traspaso y cerrarla en `dev` sin
> perder los commits rojo/verde, para **que la historia de la reconstrucción se pueda leer**.

- **R0.9.a** · CUANDO la web termine el bloque E, LA sesión web DEBERÁ dejar
  `traspasos/TRASPASO-F0-andamiaje.md` con las ocho secciones de `traspaso-web-local`. —
  Verifica: T0.21.
- **R0.9.b** · LA integración en `dev` NO DEBERÁ aplastar commits (rojo y verde distintos). —
  Verifica: `git log --oneline origin/dev` muestra T0.2 y T0.3 por separado.
- **R0.9.c** · CUANDO F0 cierre, `ESTADO.md` y el `README.md` de esta fase DEBERÁN decir «F0
  cerrada» con el SHA de cada tarea. — Verifica: T0.25.
