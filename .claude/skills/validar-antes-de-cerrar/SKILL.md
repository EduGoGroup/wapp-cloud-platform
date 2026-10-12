---
name: validar-antes-de-cerrar
description: Use in wapp-cloud-platform BEFORE saying any work is done, green, ready, closed or pushable — a commit, a branch about to become a PR, a phase, a deploy. Runs the local gates of the repo and reads them without fooling itself (exit code without pipes, SKIP counted, what was not run reported as not run). Triggers — "¿está listo?", "cierra la ola", "valida", "pasa los gates", "¿puedo pushear?", "ci-local", "está en verde".
---

# Validar antes de cerrar

Aquí **un PR no valida nada**: `.github/workflows/ci.yml` es `workflow_dispatch` desde el
2026-08-01, por decisión consciente. El gate es **local**, y se lee con cuidado, porque en este
repo se ha dado por verde lo que no lo estaba más de una vez.

## Las tres trampas que ya mordieron

1. **Leer el `rc` de otro comando.** Con `make ci-local | tail`, el `rc` es el de `tail`: siempre 0.
   Y si lanzas el gate en segundo plano con `…; echo "rc=$?"`, la notificación dirá «exit code 0»
   porque ese es el `rc` del `echo`, no el del gate. **Pasó el 2026-09-27**: la notificación decía
   0 y el gate había dado **rc=2** por el lint. El `rc` verdadero se escribe en el log y se lee de ahí.
2. **Contar un SKIP como un PASS.** `go test` da `rc=0` con un test saltado, y contar `--- SKIP`
   **sin `-v`** da siempre 0. En este repo un SKIP es un defecto: `make test-procesos` se pone en rojo
   él solo si hay uno, y en los unitarios se cuenta a mano (paso 3).
3. **Dar por corrido lo que no se pudo correr.** `make test-procesos` necesita Docker;
   `make test-procesos-edge-real`, además, el repo del Edge; UAT, acceso por SSH. Lo que no se corrió
   se reporta como **«no corrido»**, nunca como verde.

## El gate, en orden

```bash
L=/tmp/gate-$(date +%s).log

# 0 · La toolchain EFECTIVA es la fijada. Si no, nada de lo de abajo es autoritativo
make toolchain; echo "TOOLCHAIN_RC=$?"         # última línea TOOLCHAIN=OK y TOOLCHAIN_RC=0

# 1 · El gate del repo: fmt-check + vet + vet-integracion + lint + test -race
#     + cobertura-ficheros (informe: no bloquea por un fichero bajo) + build. Sin Postgres ni Docker
GOWORK=off make ci-local > "$L" 2>&1; echo "GATE_RC=$?" >> "$L"
tail -1 "$L"                                   # GATE_RC=0, o no hay nada que celebrar
grep -E '^(FAIL|--- FAIL)|issues' "$L" | head  # ningún FAIL; el lint dice «0 issues.»

# 2 · Cobertura por fichero: es un INFORME; no persigas un número.
#     rc=0 aunque haya ficheros por debajo; rc≠0 solo si un test falla o no compila. No hay exentos.
make cobertura-ficheros; echo "cobertura rc=$?"   # la tabla, FICHEROS_EVALUADOS y POR_DEBAJO (líneas BAJO …)

# 3 · SKIP en los unitarios: cero
V=/tmp/test-v-$(date +%s).log
GOTOOLCHAIN=go1.26.5 GOWORK=off go test -v ./... > "$V" 2>&1; echo "RC=$?" >> "$V"
tail -1 "$V"; grep -c -- '--- SKIP' "$V"       # RC=0 y 0. Uno solo es un defecto

# 4 · Los procesos contra Postgres (testcontainers): NECESITA Docker. No va dentro de ci-local
CUENTA=3 PROCESOS_LOG_DIR=/tmp make test-procesos; echo "procesos rc=$?"
#     imprime «procesos: RC=0 · PASS=<n> FAIL=0 SKIP=0 · /tmp/procesos.log»; rojo si rc≠0 o SKIP>0
```

Corre los pasos **uno detrás de otro**, no a la vez: la carga falsea los tests sensibles al tiempo.

**La toolchain la pone el `Makefile`**: exporta `GOTOOLCHAIN=go$(GO_VERSION)` y `lint` exige
`golangci-lint` `$(LINT_VERSION)` (hoy `go1.26.5` y `v2.12.2`; se citan por el nombre de la variable,
no por su línea). Otra versión da otro resultado.

- **`make toolchain` da `TOOLCHAIN=NOT_READY`** (`rc≠0`): el entorno no está preparado. La salida
  dice qué falta. Si es el linter: `make tools` (deja el fijado en `.bin/`, una vez por *checkout*;
  un clon o un `git worktree` nuevo no lo tiene) y repite el paso 0. **No lo sustituyas por el que
  haya**, ni con `LINT_BIN` apuntando a otra versión.
- 🔴 **Un `go` suelto, fuera de `make`, es el del sistema** (`go1.27.1` en la máquina de desarrollo),
  y un gate corrido con él **no es autoritativo**. Por eso el paso 3 lleva `GOTOOLCHAIN=go1.26.5`
  delante. Cualquier otro `go vet`/`go test` que corras como gate: igual, o su target
  (`make vet`, `make vet-integracion`, `make test`, `make cobertura-ficheros`).
- 🔴 **El lint, siempre `make lint`**, nunca `.bin/golangci-lint run` a pelo: el target usa una caché
  por *checkout*; el binario suelto, la del usuario, que puede arrastrar rutas de un *worktree* borrado.

Los candados del árbol (`internal/modulos/fronteras_test.go`, `un_fichero_un_test_test.go`,
`exportados_cubiertos_test.go`, `file_size_test.go`, `no_pending_test.go`, la huella de
`internal/arranque/huella_test.go` y `test/procesos/sin_bd_viva_test.go`) corren **dentro** de
`ci-local` como tests normales: si el gate da 0, pasaron.

## Lo que `ci-local` NO cierra

| Cosa | Por qué | Cómo |
|---|---|---|
| Los procesos de negocio contra Postgres | testcontainers necesita Docker | `make test-procesos` (skill `procesos-testcontainers`) |
| Los casos con Edge real y WhatsApp falso | Docker y el repo del Edge | `make test-procesos-edge-real` |
| El gate dentro de la imagen del CI | Docker | `make ci-docker` (no corre los procesos) |
| Un despliegue o una prueba en UAT | Acceso al VPS | Por SSH, con el runbook de UAT de la documentación del ecosistema |
| Mover `main` | Solo a petición de Jhoan | — |

Si algo de esto hace falta para cerrar y no se corrió, **no está cerrado**: se dice.

## Cómo se informa

Siempre con números, nunca con adjetivos:

```
Toolchain: make toolchain rc=0 · TOOLCHAIN=OK · GO_EFFECTIVE=go1.26.5 · LINT_EFFECTIVE=v2.12.2 <ruta>
Gate ci-local: GATE_RC=0 · <N> paquetes ok · 0 FAIL · lint 0 issues
Cobertura (informe, no bloquea): FICHEROS_EVALUADOS=<n> · POR_DEBAJO=<m>
SKIP en unitarios (go test -v ./...): 0
Procesos: RC=0 · PASS=<n> FAIL=0 SKIP=0 (CUENTA=<c>)
No corrido: <lista>, y por qué
```

Si `make toolchain` dio `NOT_READY`, la primera línea lo dice con su salida y **ningún** gate de
debajo se declara pasado.

Si algo falló, se dice primero y con su salida. No se escribe «debería pasar».

## Antes de empujar

🔴 Nunca código directo en `dev` ni `main` salvo orden expresa de Jhoan (regla innegociable 6 del
`CLAUDE.md`): se empuja **la rama** y se abre un PR hacia `dev`, sin squash.

- `GATE_RC=0` leído del log, no de una notificación.
- `git status --short` sin restos que no sean del commit.
- `git fetch origin && git log --oneline -1 origin/dev`: que nadie empujó entretanto.
- `git push origin <rama>` leyendo **su** `rc`, y `gh pr create --base dev`; el cuerpo del PR, con la
  skill **`describir-pr`** (la tabla de gates de aquí va en su sección «🔬 Gates»). Qué dice la
  documentación mientras el PR está abierto: skill **`pre-merge-docs`**.
