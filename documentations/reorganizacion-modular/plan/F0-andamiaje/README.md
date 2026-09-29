# F0 · Andamiaje — portal de la fase

> **Estado: sin empezar** (plan escrito el 2026-09-28 sobre `dev` @ `1b18932`). Norma:
> [`05`](../../05-metodo-contratos-y-tdd.md) §6. Forma: [`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).

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
| 9 | `06-entorno-web.md` §1 (versión del 2026-09-27): Docker ❌, hooks ❓ | **Ya corregido** en `06` §1 el 2026-09-28 (Docker ✅, hooks ✅, según la doc oficial); lo que sigue **sin probar** es testcontainers en la VM | T0.0 lo mide y lo anota en `06` §5 |

## Números medidos (y cómo)

| Qué | Cifra | Comando / método (fecha) |
|---|---|---|
| Arranque viejo | 21 ficheros prod, 3.602 l; 20 de test, 54 `Test*` | `ls`/`wc -l`/`grep -h '^func Test'` (2026-09-28) |
| Rutas | 95 = 22 `:8100` + 73 `:8103` | prototipo en proceso con sondas `httptest` (`arquitectura.md` §2.3) |
| rpc | 2 | `GetServiceInfo()` en el prototipo |
| Métricas | 22 declaradas; 11 familias en frío | `# TYPE wapp_` de `/metrics` en el prototipo |
| Goroutines del arranque | 10 sentencias `go` | `grep -rn '^\s*go '` sin tests |
| *Hooks* de métricas | 14 usos, 12 métodos | `grep -o 'mtx\.[A-Za-z]*'` |
| Integración que honra `WAPP_TEST_REQUIRE_DB` | 50 de 91 ficheros | `grep -rln` (2026-09-28) |
| Contenedor de huella | 0,09 s, sin red | prototipo |
| Lista blanca de fronteras | **sin medir — la mide T0.7** con el script de `02` §5 | — |
| ¿Basta dejar a `nil` tipado los almacenes de la fase 3 en la huella? | **sin medir — lo mide T0.14** | — |
