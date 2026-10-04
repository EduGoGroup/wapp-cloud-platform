---
name: validar-antes-de-cerrar
description: Use in wapp-cloud-platform BEFORE saying any work is done, green, ready, closed or pushable — a commit to dev, a red/green step, a module phase, a handoff. Runs the local gates of the repo and of the modular reconstruction and reads them without fooling itself (exit code without pipes, SKIP counted, what the web session cannot close). Triggers — "¿está listo?", "cierra la ola", "valida", "pasa los gates", "¿puedo pushear?", "ci-local", "está en verde".
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
2. **Contar un SKIP como un PASS.** Los tests de integración **viejos** se saltan solos sin
   `WAPP_TEST_DB_DSN`, con `rc=0` (deuda DT-52: 438 tests). Y contar `--- SKIP` **sin `-v`** da
   siempre 0.
3. **Dar por corrido lo que no se pudo correr.** En la web Docker está preinstalado, pero `make test-integration`, UAT y el cierre de F9 son de la sesión local (decisión W-1; que testcontainers funcione en la web lo mide F0-01 en `06-entorno-web.md` §5). Lo que no se corrió
   se reporta como **«no corrido»**, nunca como verde.

## El gate, en orden

```bash
L=/tmp/gate-$(date +%s).log

# 0 · La toolchain EFECTIVA es la fijada. Si no, nada de lo de abajo es autoritativo
make toolchain; echo "TOOLCHAIN_RC=$?"         # última línea TOOLCHAIN=OK y TOOLCHAIN_RC=0

# 1 · El gate del repo: fmt-check + vet + vet-pendiente + vet-integracion + lint + test -race
#     + cobertura-ficheros (informe: no bloquea por un fichero bajo) + build
GOWORK=off make ci-local > "$L" 2>&1; echo "GATE_RC=$?" >> "$L"
tail -1 "$L"                                   # GATE_RC=0, o no hay nada que celebrar
grep -E '^(FAIL|--- FAIL)|issues' "$L" | head

# 2 · Los rojos compilan (va dentro de ci-local; suelto, para iterar)
make vet-pendiente; echo "vet-pendiente rc=$?"

# 3 · Lo que falta por implementar (cuenta estática y exacta)
grep -rn 'pendiente.Implementar' --include='*.go' internal | wc -l
make test-pendiente          # PENDIENTES=<n> y ROJOS=<n>; su rc es el de vet-pendiente

# 4 · Cobertura por fichero: es un INFORME (P2; en código desde F1-06, 2026-10-03); no persigas un número.
#     rc=0 aunque haya ficheros por debajo; rc≠0 solo si un test falla o no compila. No hay exentos.
make cobertura-ficheros; echo "cobertura rc=$?"   # la tabla, FICHEROS_EVALUADOS y POR_DEBAJO (líneas BAJO …)

# 5 · SKIP: en código NUEVO debe ser cero
GOTOOLCHAIN=go1.26.5 GOWORK=off go test -v ./internal/modulos/... ./internal/nucleo/... ./internal/arranque/... 2>&1 \
  | grep -c -- '--- SKIP'     # 0, siempre. Uno solo es un defecto
```

**La toolchain la pone el `Makefile`, en web y en local** (desde el 2026-10-02): exporta
`GOTOOLCHAIN=go$(GO_VERSION)` y `lint` exige `golangci-lint` `$(LINT_VERSION)` (hoy `go1.26.5` y
`v2.12.2`; se citan por el nombre de la variable, no por su línea). Otra versión da otro resultado.

- **`make toolchain` da `TOOLCHAIN=NOT_READY`** (`rc≠0`): el entorno no está preparado. La salida
  dice qué falta. Si es el linter y estás en **local**: `make tools` (deja el fijado en `.bin/`,
  una vez por *checkout*; un clon o un `git worktree` nuevo no lo tiene) y repite el paso 0. En la
  **web** el linter fijado viene en el `PATH`; si no está, el entorno web no está preparado
  (`documentations/reorganizacion-modular/06-entorno-web.md` §3 y §6) y se dice. **No lo sustituyas
  por el que haya**, ni con `LINT_BIN` apuntando a otra versión.
- 🔴 **Un `go` suelto, fuera de `make`, es el del sistema**: en local `go1.27.1`, y un gate corrido
  con él **no es autoritativo**. Por eso el paso 5 lleva `GOTOOLCHAIN=go1.26.5` delante y los pasos
  2–4 van por `make`. Cualquier otro `go vet`/`go test` que corras como gate: igual, o su target
  (`make vet-pendiente`, `make vet-integracion`, `make test-pendiente`, `make cobertura-ficheros`).
  En la web da igual: el `go` de la sesión ya es el fijado.

Los candados de la reconstrucción (`fronteras_test`, `un_fichero_un_test_test`,
`exportados_cubiertos_test`, `huella_test`) corren **dentro** de `ci-local` como tests normales:
si el gate da 0, pasaron.

## Lo que la sesión web NO puede cerrar

| Cosa | Por qué | Quién la cierra |
|---|---|---|
| Tests de proceso de F9 (`make test-procesos`) | testcontainers necesita Docker | Claude Code **local** |
| Tests de integración **viejos** (`make test-integration`) | Docker. Solo importan en F0 (los ✎ de `platform`) y en el relevo: el resto del tiempo el código viejo no se toca | Claude Code **local** |
| Un despliegue o una prueba en UAT | Acceso al VPS | Claude Code **local** |
| Mover `main` | Solo a petición de Jhoan | Claude Code **local** |

Si algo de esto hace falta para cerrar, **no está cerrado**: escribe el traspaso con la skill
`traspaso-web-local`.

## Cómo se informa

Siempre con números, nunca con adjetivos:

```
Toolchain: make toolchain rc=0 · TOOLCHAIN=OK · GO_EFFECTIVE=go1.26.5 · LINT_EFFECTIVE=v2.12.2 <ruta>
Gate ci-local: rc=0 · <N> paquetes ok · lint 0 issues
vet -tags pendiente: rc=0
Pendientes: <N> llamadas a pendiente.Implementar (antes: <M>)
Cobertura (informe, no bloquea): FICHEROS_EVALUADOS=<n> · POR_DEBAJO=<m> · <los BAJO …, si los hay>
SKIP en código nuevo: 0
No corrido: <lista>, y por qué
```

Si `make toolchain` dio `NOT_READY`, la primera línea lo dice con su salida y **ningún** gate de
debajo se declara pasado.

Si algo falló, se dice primero y con su salida. No se escribe «debería pasar».

## Antes de pushear a `dev` (solo la sesión local)

La sesión **web** no empuja a `dev`: empuja **su** rama y abre `gh pr create --base dev`
(`documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md` §5–§6).

- `GATE_RC=0` leído del log, no de una notificación.
- `git status --short` sin restos que no sean del commit.
- `git fetch origin && git log --oneline -1 origin/dev`: que nadie empujó entretanto.
- `git push origin <rama>` y PR hacia `dev`, leyendo **su** `rc`; el cuerpo del PR, con la skill **`describir-pr`** (la tabla de gates de aquí va en su sección «🔬 Gates»). 🔴 Nunca código directo en `dev` ni `main` salvo
  orden expresa de Jhoan (regla innegociable 6 del `CLAUDE.md`).
