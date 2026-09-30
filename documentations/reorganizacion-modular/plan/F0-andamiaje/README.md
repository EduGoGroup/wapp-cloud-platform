# F0 · Andamiaje — portal de la fase

> **Estado: ✅ CERRADA (2026-09-30)** — bloques A–F. F0-01, T0.0 `98e806d`, T0.1 `de04088`; F0-02,
> T0.2–T0.4 y T0.26; F0-03, T0.5–T0.9, de `3040e82` a `3e85144`; F0-04, T0.10–T0.15, de `d64dbbf` a
> `61ce04b`; F0-05, T0.16–T0.21 y T0.27 + TX.1–TX.4, de `8096232` a `15223ff` (PR #17, `835a7be`); y
> **F0-06 (💻)**, T0.22–T0.25: integración vieja con Postgres real, arranque real de `cmd/server-modular`, `dev`
> verificado y este cierre (`d3b3f3f`). `origin/dev` contiene F0 entera, sin squash. Siguiente:
> **F9-01 (9A, el arnés; D-F9-1 = sí)** y luego F1, con el [traspaso](../../traspasos/TRASPASO-F0-andamiaje.md)
> ya **CERRADO**. Plan escrito el 2026-09-28 sobre `dev` @ `1b18932`. Norma: [`05`](../../05-metodo-contratos-y-tdd.md) §6.
> Forma: [`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).

## Objetivo

Montar lo que todas las fases necesitan **sin crear ningún módulo**: el segundo arranque
(`cmd/server-modular` → `internal/arranque`, copia del viejo que cablea paquetes viejos), el
paquete `pendiente` y su etiqueta, los candados de `05` §5 con la **huella** entre los dos
arranques, la cara HTTP nueva vacía, y `platform` sin dependencias de dominio. Es la fase más
crítica: si el andamiaje está mal, todo lo demás hereda el fallo.

## Entradas (tiene que ser cierto para empezar)

- `dev` al día con `origin/dev`, `git status` limpio; `internal/arranque` **no** existe
  (`reconstruir-modulo`, paso 0).
- El entorno web preparado según [`../00-marco/flujo-web-local.md`](../00-marco/flujo-web-local.md)
  (setup script, variables, `golangci-lint v2.12.2`).
- **D-F0-1 y D-F0-2** decididas antes del bloque D; **D-F0-3** y **D-F4-1** antes del bloque E
  (abajo).
- Para el mecanismo de montaje de T0.16: [`../FX-cara-http/diseno.md`](../FX-cara-http/diseno.md) escrito.

## Salidas (criterio de salida, detallado en [`reglas.md`](reglas.md) §5)

1. **Huellas idénticas** entre los dos arranques (rutas, rpc, métricas, goroutines, *hooks*,
   entorno), con `apipublica` montada y tras los tres ✎.
2. **`dev` verde con todos los candados activos**, y demostrado que muerden.
3. **`make test-pendiente` = 0.**
4. **Integración vieja verde con Postgres real** tras los ✎ de `platform` (0 SKIP, 0 FAIL, `-v`).
5. **Entorno web verificado** y anotado en `06-entorno-web.md` §5.

### Cierre: cada criterio, con su evidencia (2026-09-30, sesión F0-06, `go1.26.5` + `golangci-lint v2.12.2`)

| # | Criterio | Evidencia |
|---|---|---|
| 1 | Huellas idénticas | `TestHuellaVieja`, `TestHuella` y `TestHuellaEstatica` PASS; `huella.json` sin diff; 95 rutas = 22 + 73, 2 rpc, 11 familias, 10 goroutines, 13 *hooks*, entorno ∅. **Y en ejecución real**: las 98 peticiones del barrido dan los mismos códigos en los dos binarios |
| 2 | `dev` verde con todos los candados | `GOWORK=off make ci-local` en un *worktree* limpio de `835a7be`: `GATE_RC=0`, 84 paquetes `ok`, `0 issues`, `FICHEROS_EVALUADOS=10`, `POR_DEBAJO=0`; cada candado con su caso que muerde (F0-03/04/05) |
| 3 | `make test-pendiente` = 0 | `PENDIENTES=0`, `ROJOS=0`, rc=0; `vet -tags pendiente` rc=0 |
| 4 | Integración vieja verde con Postgres real | T0.22: `rc=0`, **0 SKIP, 0 FAIL, 4.618 PASS** con `-v` y `-count=1`; los 8 `TestCollector_*` en PASS |
| 5 | Entorno web verificado | `06-entorno-web.md` §5 (T0.0) y el hook (T0.1) |
| 6 | `git diff --stat 1b18932..origin/dev -- internal/bootstrap cmd/server` | solo `huella_vieja_test.go` (+305) |

Y el arranque real (T0.23): **9/9** fases, `/healthz` 200 y SIGINT limpio (`EXIT=0`), en el nuevo y en el viejo.

## Orden de lectura

1. [`arquitectura.md`](arquitectura.md) — el arranque viejo inventariado (9 fases, 21 ficheros,
   3.602 l), lo que F0 añade, la huella y los ✎.
2. [`diseno.md`](diseno.md) — el contrato de cada paquete nuevo, el formato de la tabla de
   fronteras, la agregación de cobertura y el contenedor de huella.
3. [`reglas.md`](reglas.md) — lo que no se toca, 13 trampas con `fichero:línea`, la definición de hecho.
4. [`requisitos.md`](requisitos.md) — nueve historias, criterios EARS con su verificación.
5. [`tareas.md`](tareas.md) — T0.0–T0.27 en seis bloques.

## Bloques de sesión

| Bloque | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| A · el entorno web | 🌐 | T0.0, T0.1 | `06` §5 escrito; hook probado en sus dos ramas |
| B · `pendiente` y los `make` | 🌐 | T0.2–T0.4 (+ T0.26 si T-1 = sí) | `PENDIENTES=0` y gate ci-local `GATE_RC=0` con `vet-pendiente` |
| C · los candados de fichero | 🌐 | T0.5–T0.9 | cinco candados en `ci-local`, cada uno con su caso que muerde |
| D · arranque nuevo y huella | 🌐 | T0.10–T0.15 | `TestHuellaVieja` y `TestHuella` verdes contra la misma dorada. **Para en T0.11 si D-F0-1/D-F0-2 no están decididas** |
| E · cara vacía, ✎ y deriva | 🌐→💻 | T0.16–T0.21 (+ T0.27 si D-F4-1 = sí) | huella intacta tras cada paso; barridos AST viejos ciegos al árbol nuevo; traspaso escrito y rama empujada |
| F · cierre local | 💻 | T0.22–T0.25 | los cinco criterios de salida en `origin/dev` |

La numeración global de sesiones (`S0n`) la pone [`../sesiones/`](../sesiones/README.md).

## Decisiones que necesita Jhoan

| Id | Pregunta | Recomendación | Bloquea |
|---|---|---|---|
| **D-F0-1** | `05` exige contrato → rojo → verde por fichero y no portar tests (E-2, E-4, E-8). ¿Se acepta que `internal/arranque` nazca **por copia** en F0, con 19 de los 20 tests del viejo copiados (los 11 que leen AST —10 candados y su ayudante— y los unitarios de sus funciones puras), fuera de `un_fichero_un_test` y de la cobertura por fichero? | **Sí.** Es cableado, no dominio: un contrato con `panic` de una copia que debe salir idéntica no especifica nada. Su contrato es la huella + los candados AST, que `05` §3.2 ya asigna «al arranque nuevo». Se reconstruye por módulo en cada `conmutar` (`diseno.md` §5.1) | bloque D |
| **D-F0-2** | ¿Se permite **un fichero de test** nuevo en el paquete viejo, `internal/bootstrap/arranque/huella_vieja_test.go`? | **Sí.** Sin él no hay forma de medir en ejecución la huella del viejo sin levantarlo (sus constructores son privados y sus fases 1 y 3 exigen Postgres y R2). No cambia producción ni lo que corre en UAT, y muere con el paquete en F10. Alternativa peor: dorada congelada desde la copia en F0, que deja de ser oráculo en el primer arreglo «hecho dos veces» | T0.14 |
| **D-F0-3** | E-1 exceptúa **solo** los tres ficheros ✎ de `platform`. Cortarlos sin tocar el arranque viejo exige **una línea de alias** en tres paquetes viejos de dominio (`gateway/session/registry.go:22`, `iam/ports/in/usecases.go:129`, `inferstats/inferstats.go:144`). ¿Se acepta? | **Sí.** Alternativa: adaptadores en los dos arranques, que **tocan el oráculo** (fases 4 y 8 del viejo). Con alias, ni el arranque viejo ni ningún texto cambia, y los módulos nuevos declararán el mismo alias (`arquitectura.md` §7) | bloque E |
| **D-F4-1** *(de F4, se ejecuta aquí)* | Dos barridos AST **viejos** recorren todo `internal/` con lista exacta (`llmvia/c2_via_test.go:117`, `iam/infra/postgres/membresia_unica_ast_test.go:74,97`) y se pondrían rojos con el primer verde de F2/F4. ¿Una línea en cada uno para que salten el árbol nuevo? Subsume D-F2-2 | **Sí** → T0.27 (segunda excepción a E-1). Alternativa peor: tocar la lista vieja en F2, F4, TX.13 y F7 | bloque E |
| T-1 *(del marco)* | ¿`make lint` falla si la versión no es `v2.12.2`? | Sí → T0.26 | T0.26 |

| **F0-A-1** *(de F0-01, 2026-09-30)* — ✅ **sí (Jhoan, 2026-09-30)** y ✅ **aplicada en claude.ai/code (2026-09-30)**; texto en `flujo-web-local.md` §3 | Docker en la VM: el daemon **no arranca solo** y Docker Hub responde **429** (`06` §5). ¿Se añade `TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX=mirror.gcr.io/` a las variables del entorno y, al *setup script*, `(dockerd >/tmp/dockerd.log 2>&1 &)` + espera antes del `docker pull` (con la imagen tirada de `mirror.gcr.io/library/postgres:17-alpine`)? | **Sí a la variable** (probada en frío, `TC_RC=0`; en local no aplica porque no está puesta). El cambio del *setup* **sin verificar**: que el *snapshot* conserve `/var/lib/docker` no está probado; si no lo conserva, sobra. Mientras tanto, cada sesión arranca `dockerd` a mano (el hook lo recuerda) | F9 (pre-chequeo web); no bloquea F0 |

Decididas en esta fase sin necesitar a Jhoan (con su porqué en [`reglas.md`](reglas.md) §3):
`sin_pendientes_test` nace en F10; `sin_bd_viva_test` sin etiqueta; `cobertura-ficheros` dentro
de `ci-local`; exención de adaptadores Postgres por marca verificada; `make test-procesos` en F9.

## Contradicciones encontradas (no corregidas en `04`/`05`; se señalan)

| # | Dice | El código dice | Dónde se trata |
|---|---|---|---|
| 1 | `05` §3.2: «los **11** `*_cableado_test.go`»; `constitucion.md` §5 y `deuda.md` D-9: «**8**» | **9** ficheros con `cablead` en el nombre; 11 es el nº de ficheros de test que leen AST (9 + `platform_permissions_test.go` + `astpaquete_test.go`) — `ls internal/bootstrap/arranque/*cablead*_test.go \| wc -l` | `diseno.md` §5.2 · T0.20 corrige la constitución y la deuda |
| 2 | `05` E-1: la única excepción son los tres ✎ de `platform` | cortarlos pide 3 líneas de alias en dominio viejo | D-F0-3 |
| 3 | `05` §5: la huella falla «para un módulo ya conmutado» | F0 compara **siempre** la superficie entera: en F0 los dos cablean lo mismo y la huella debe ser idéntica desde el primer día | `arquitectura.md` §6 |
| 4 | `04` §3: huella de «rutas, rpc, métricas, **variables**»; `05`: «…, **goroutines**» | F0 toma las dos: goroutines estática y `entorno` = ∅ (solo `platform/config` lee entorno, `config.go:735`) | `diseno.md` §6.1 |
| 5 | `04` §2.1: los dos binarios usan «los MISMOS paquetes» y `server-modular` lleva copia del e2e | sustituido por `05` §4 (no comparten dominio) y E-8; el e2e de `cmd/server` no ejerce el arranque | `diseno.md` §5.3 |
| 6 | `02` §3.3: `ErrSessionOffline` (3 usos) en `platform/httpapi` | 1 uso en código (`admin.go:306`) + el import (`:13`) | `arquitectura.md` §7 |
| 7 | `constitucion.md` trampa 8, `deuda.md` D-14, `operacion.md:99,194`: `internal/publicapi/flows.go:75` | es `internal/bootstrap/arranque/flows.go:75` | T0.20 (18 líneas caducadas, medido el 2026-09-29) |
| 8 | skill `procesos-testcontainers`: «todo con `//go:build integracion`», incluido el candado | con etiqueta, el candado no mordería en `ci-local` | T0.8 corrige la skill |
| 9 | `06-entorno-web.md` §1 (versión del 2026-09-27): Docker ❌, hooks ❓ | **Ya corregido** en `06` §1 el 2026-09-28 (Docker ✅, hooks ✅, según la doc oficial); lo que sigue **sin probar** es testcontainers en la VM | T0.0 lo midió (2026-09-30): funciona, `06` §5 |
| 10 | `flujo-web-local.md` §4 (diseño del hook): imprime versiones **solo** en el aviso | R0.1.f y T0.1: «el hook DEBERÁ imprimir la versión de Go y de `golangci-lint`» siempre | T0.1 añade la línea `Go: … · golangci-lint: …` y corrige el diseño (✎) |
| 11 | `06` §1 y `flujo-web-local.md` §1: Docker «preinstalado» ✅ | el binario sí; el **daemon no corre** al empezar la sesión (`docker info` rc=1), y Docker Hub da 429 | `06` §5; el hook lo avisa con el comando; decisión F0-A-1 |
| 12 | `tareas.md` T0.4: la demostración de que muerden «se deshace con `git stash -u && git stash drop`» | en T0.4 el `Makefile` **aún no está commiteado**: el `stash -u` se lo lleva junto con los ficheros de la demostración y el `drop` lo borra (pasó en F0-02; se recuperó del commit del stash con `git checkout <sha-del-stash> -- Makefile`) | Para T0.4 (y toda demostración con cambios propios sin commitear): commitear antes, o deshacer con `rm` de los ficheros de la demostración. T0.27 tiene la misma receta: allí los dos tests viejos **sí** estarán sin commitear |
| 13 | `diseno.md` §3: «los paquetes del alcance que aún no existen se omiten con `go list`» | `go list ./internal/modulos/... ./internal/nucleo/...` con un directorio inexistente da `rc=1` y **no lista ninguno**, tampoco los que sí existen (`lstat … no such file or directory`) | `test-pendiente` filtra antes por existencia del directorio (`[ -d ]`) y solo entonces llama a `go list`; T0.9 (`cobertura-ficheros`) necesitará lo mismo |
| 14 | `diseno.md` §3: `test-pendiente` cuenta `pendiente.Implementar(` y `//go:build pendiente` en todo el repo | el `grep` y el `find` entraban en `testdata/`, que Go ignora; los árboles de prueba de `internal/candados` imitan rojos a propósito (un fichero en rojo en `cobertura`, un test con etiqueta en `exportados`) y las cifras no podrían volver a 0 | `3040e82` (F0-03): `--exclude-dir=testdata` y `-name testdata -prune`, con comentario en el `Makefile` |
| 15 | `diseno.md` §4.5 y T0.9: el caso `muerde` de la cobertura es «un perfil» en `testdata/cobertura/` | la raíz `.gitignore:10` ignora `*.out`: los `perfil.out` no se commitearon y los verdes pasaban **solo en el árbol de trabajo**; lo destapó correr `ci-local` en un *worktree* limpio (F0-03) | `internal/candados/testdata/.gitignore` con `!*.out`, metido por *fixup* en el rojo `2c2bbd6` (rama propia sin PR). **Norma para toda sesión**: el gate que cuenta es el de un clon limpio del commit (`git worktree add --detach … HEAD`) |
| 16 | `diseno.md` §4.2: exención D-F1-3 «todo fichero de un paquete cuyo nombre termina en `test`» | literal: exime también un paquete de producción que se llamara, p. ej., `contest` | Se dejó literal (una condición, como pide el diseño). Si un módulo necesitara un nombre así, se decide entonces |
| 17 | T0.10 «hecho cuando»: el `diff` de la copia muestra **solo** las dos rutas relativas | `exportados_cubiertos` (F0-03) exige que `Ejecutar` aparezca en `orquestador_test.go`, y el test viejo no lo menciona (`diseno.md` §5.2 sí lo preveía) | `d64dbbf` (F0-04): una línea `var _ func(context.Context) error = Ejecutar` con su porqué; no añade `Test*` (siguen 53). El `diff` de T0.10 muestra esa línea además de las dos rutas |
| 18 | `arquitectura.md` §2.3, R0.5.e, `reglas.md` §5: «**14** usos de 12 métodos» de `*metrics.Metrics` | **13** usos de 12 métodos (`InstrumentHTTP` ×2); el 14.º de `grep -o 'mtx\.[A-Za-z]*'` es el **comentario** de `fase9_fondo.go:59`. `*metrics.Metrics` tiene 13 métodos por `reflect`; `RateLimitHit` no lo usa el arranque | `huellatest.Hooks` recorre el AST (los comentarios no cuentan); `TestHuellaEstatica` da 13 en los dos árboles (`61ce04b`) |
| 19 | `diseno.md` §6.3: en la huella, la fase 3 «**simulado** solo el grupo `flowDeps` (…presign nulo…)»; medir si basta con `nil` tipado | simular `flowDeps` obliga a **copiar el cuerpo de la fase 3** en los dos tests (la fase construye `flowDeps` en su primera línea). Su única salida a la red es el `HeadBucket` de R2, y con endpoint IP el SDK hace *path-style* solo (D-F9-2) | `fde5849` (F0-04): la fase 3 corre **entera** contra un S3 falso `httptest` dentro del proceso (el mecanismo de D-F9-3) y el keyring de prueba entra por `WAPP_KEK_MASTER_B64`/`_INDEX_B64`. Más fiel que la simulación, y la pregunta del `nil` tipado no aplica. **Para revisar por Jhoan** |
| 20 | `diseno.md` §6.1: goroutines normalizadas con `go/types` e importador **`source`** | `source` tarda **~105 s por paquete** sobre el arranque (grpc, aws-sdk, prometheus desde fuente) y 57 s la suite de `huellatest` con `-race` | `141d960`: importador `gc` alimentado por `go list -export -deps` (solo stdlib; ~0,55 s por paquete). Consecuencia: `Goroutines` necesita el comando `go` y un directorio dentro del módulo |
| 21 | contradicción 15: «el gate que cuenta es el de un clon limpio (`git worktree add --detach … HEAD`)» | si se borra el *worktree* y se crea otro con **otra ruta**, `golangci-lint` sirve de su caché *issues* con rutas del *worktree* borrado, sin poder leer sus `//nolint`: `GATE_RC=2` falso (visto en F0-04 con `servir.go:34`) | reutilizar **siempre la misma ruta** de *worktree* para el gate, o `golangci-lint cache clean` antes |
| 22 | *(revisión del PR de F0-04)* la copia `internal/arranque/auth.go` tiene 835 líneas y seis temas, y la editan F2 y F3 | R0.4.b obliga a que `internal/arranque` sea copia **exacta** del viejo hasta cerrar F0: partirlo aquí rompería la prueba de T0.10 | Se parte en F2, como su primera tarea: **T2.34**, decisión **D-F2-8** (`auth_*.go` + `edge_config.go`) |
| 23 | `diseno.md` §7, `arquitectura.md` §7, T0.19: `Agregado` «se declara en `platform/metrics`» | `platform/metrics/inferstats_test.go` es `package metrics` e importa `internal/inferstats`: con `inferstats → platform/metrics` da «import cycle not allowed in test» (y R0.7.d prohíbe editar ese test); además `inferstats` arrastraría Prometheus, contra su doc (`inferstats.go:44-46`) | `5305134` (F0-05, **decidido por el usuario**): paquete hoja `internal/platform/metrics/inferencia` (solo el tipo). `diseno.md` §7 ya dejaba elegir el paquete con la condición «sin ciclo» |
| 24 | T0.16: `http.go` es «la primera y única desviación de la copia en F0»; TX.4: «la huella resuelve los 73 patrones por `Compuesto.Resolver`» | la huella sondea por `ServeHTTP` desde el bloque gemelo «Contenedor de huella», que no puede cambiar sin cambiar el viejo (que no tiene compuesto) | `9dcf7e8`/`7b7e01f` (F0-05, **decidido por el usuario**): `buildPublicAPIServer` devuelve el compuesto, `fase8_transporte.go` lo guarda en el campo nuevo `contenedor.publicCompuesto`, y `TestMudanzas_HuellaPorElCompuesto` resuelve las 73 filas sobre el contenedor de huella. La copia se desvía en tres ficheros (más `mudanzas.go` y `cara_nueva_cableado_test.go`, nuevos) |
| 25 | `Makefile`: `PENDIENTE_DIRS := internal/modulos internal/nucleo internal/arranque` | `internal/apipublica` (que FX construye en rojo/verde) no estaba: sus rojos no se habrían corrido en `test-pendiente` | `5a11f5b`: añadida |
| 26 | T0.20: «`deuda.md:137` y `README.md:53` narran historia» | `README.md:53` afirmaba en presente «se lee `internal/bootstrap/bootstrap.go`», que hoy es una fachada de 42 líneas | `8096232`: apunta a `internal/bootstrap/arranque/orquestador.go`. Solo `deuda.md:139` (antes `:137`) es historia |
| 27 | FX RX.6.a: `apipublica` puede importar «los puentes declarados en `fronteras_test.go`» | `internal/candados/fronteras.go:196` (regla 4): «la cara nueva solo habla con lo nuevo, **ni con puente**» — manda sobre la regla 2 | No afecta a F0 (`apipublica` solo importa stdlib). **Decidir antes de la primera ola de FX que necesite un puente** (con D-FX-1 = sí, ninguna lo prevé); traspaso §8 |
| 28 | `iam/ports/in/usecases.go:~155-158`: «internal/platform/httpapi ya importa este paquete» | desde T0.18 es al revés (`in` importa `httpapi` por el alias) | No se toca (E-1: D-F0-3 autoriza solo la línea del alias). Muere con el paquete |
| 29 | `mapa-de-rutas.md` y RX.2.c citan líneas de `internal/bootstrap/arranque/http.go` | en la copia `internal/arranque/` todo va +1 línea (la cabecera `// Copia de`), y desde TX.3 más en `http.go` | Las citas del plan se leen contra el viejo; ninguna cita de línea en la copia |
| 30 | T0.27: «+1 línea cada uno (más un import si hace falta)» | con la condición en línea, `gocyclo` de `TestC2_LaViaSoloSePreguntaEnLaSeleccion` pasa de 15 (18) y `ci-local` falla; `gofmt` además parte el `if` en tres líneas | `dd1e2bd`: en `c2_via_test.go` un ayudante `esDelArbolNuevo` (+14 líneas); en `membresia_unica_ast_test.go`, `if` en línea (+3) |
| 31 | *(F0-06)* traspaso §7.2 y T0.19: `/metrics` publica «las cinco `wapp_edge_*`» | **en frío solo sale una**, `wapp_edge_inference_reporting_edges 0`; las otras cuatro no emiten familia sin muestras (`ConstMetric`). Las cinco salen con un Edge que envíe `SessionHealth` (un Edge de mentira con mTLS lo demostró: 9/3, 7/3/2, 5/1/2, 10/10, 1 Edge; idéntico en el viejo) | Es lo que ya fijaba la dorada (`huella.json:120,245`). Corrección de lectura, sin código |
| 32 | *(F0-06)* T0.22 y el traspaso: «91 ficheros de test leen `WAPP_TEST_DB_DSN`, 50 honran `WAPP_TEST_REQUIRE_DB`» | hoy son **96**: los 5 nuevos son del candado `sin_bd_viva` (`sinbdviva_test.go` y 4 *fixtures* de `testdata`, F0-03), que mencionan la variable sin conectarse. Los 91 viejos y los 50 no cambian; con 0 SKIP los 41 restantes también corrieron | Sin acción. La cifra de la regla E-5 sigue siendo 91/50 para el código viejo |
| 33 | *(F0-06)* el traspaso da por cubierta la auditoría (`AuditInput`, seis campos) | no hay **ningún test de integración** que la verifique a través del middleware contra Postgres (`audit_mw_test.go`: auditor falso; `TestIntegration_Audit`: `len==1` y `Action`). Hoy solo la vio esta sesión, en ejecución: `public.audit_events` con `tenant_id`, `actor`, `action`, `resource`, `result=failure`, `meta={"status":502}`, también con 404, y **ninguna** fila con 401/403 | Hueco para F9 (un proceso de auditoría). Además `publicapi/messages.go:162-165` dice 502 donde el código da 404: comentario caducado en código viejo, no se toca (E-1) |

## Números medidos (y cómo)

| Qué | Cifra | Comando / método (fecha) |
|---|---|---|
| Arranque viejo | 21 ficheros prod, 3.602 l; 20 de test, 54 `Test*` | `ls`/`wc -l`/`grep -h '^func Test'` (2026-09-28) |
| Rutas | 95 = 22 `:8100` + 73 `:8103` | prototipo en proceso con sondas `httptest` (`arquitectura.md` §2.3) |
| rpc | 2 | `GetServiceInfo()` en el prototipo |
| Métricas | 22 declaradas; 11 familias en frío | `# TYPE wapp_` de `/metrics` en el prototipo |
| Goroutines del arranque | 10 sentencias `go` | `grep -rn '^\s*go '` sin tests |
| *Hooks* de métricas | 13 usos, 12 métodos (el 14.º del `grep` es un comentario, contradicción 18) | `TestHuellaEstatica` (AST) |
| Integración que honra `WAPP_TEST_REQUIRE_DB` | 50 de 91 ficheros | `grep -rln` (2026-09-28) |
| Contenedor de huella | 0,09 s, sin red | prototipo |
| Lista blanca de fronteras | **13 aristas** módulo→módulo entre los 7 de D-5 (+2 de `platform`, los ✎ del bloque E, fuera de `Capas`); los tests viejos añadirían 4 más, que no entran (E-8) | script de `02` §5 con el `MAP` de D-5 (`platformadmin`, `entitlements` → `acceso`), solo `.Imports`, sobre `dev` @ `c55e9e3` (2026-09-30, T0.7) |
| ¿Basta dejar a `nil` tipado los almacenes de la fase 3 en la huella? | **no hace falta**: la fase 3 corre entera con un S3 falso en proceso (contradicción 19) | `TestHuellaVieja` (2026-09-30, T0.14) |
| Huella medida (dorada) | 95 rutas = 22 `:8100` + 73 `:8103` en los dos perfiles; 2 rpc; 11 familias `wapp_*` en frío; 10 goroutines; **13** *hooks* de 12 métodos; entorno ∅ | `TestHuellaVieja` / `TestHuella` / `TestHuellaEstatica` (2026-09-30, T0.14–T0.15); contenedor + sondas 0,25 s |
| Importador de tipos para `Goroutines` | `source` ~105 s/paquete · `gc` + `go list -export` ~0,55 s/paquete | medido en T0.13 (2026-09-30) |
| Integración vieja con Postgres real (T0.22) | `rc=0`, **4.618 PASS** (3.281 de primer nivel), 0 SKIP, 0 FAIL, 79 paquetes `ok`; 4.318 + 296 (código nuevo de F0) + 3 (`internal/modulos`) + 1 (`TestHuellaVieja`) | `GOFLAGS='-count=1 -v' INTEGRATION_PG_PORT=<libre> make test-integration`, `go1.26.5` (2026-09-30) |
| Arranque real (T0.23) | 9/9 fases; fase 1 (migraciones `0.48.0` desde cero) 476 ms, fase 3 (`HeadBucket` R2) 3.620 ms; 98 peticiones con los mismos códigos en viejo y nuevo | `cmd/server-modular` y `cmd/server` contra `postgres:16` efímero, `barrido.sh` (2026-09-30) |
| Gate `ci-local` con la toolchain fijada | `GATE_RC=0` en 2 min 50 s; 84 paquetes `ok`; `0 issues` | `go1.26.5` + `golangci-lint v2.12.2`, *worktree* limpio de `835a7be` (2026-09-30) |
