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

# 1 · El gate del repo: fmt + vet + lint + test -race + build
GOWORK=off make ci-local > "$L" 2>&1; echo "GATE_RC=$?" >> "$L"
tail -1 "$L"                                   # GATE_RC=0, o no hay nada que celebrar
grep -E '^(FAIL|--- FAIL)|issues' "$L" | head

# 2 · Los rojos compilan (hasta que F0 lo meta dentro de ci-local)
GOWORK=off go vet -tags pendiente ./... ; echo "vet-pendiente rc=$?"

# 3 · Lo que falta por implementar (cuenta estática y exacta)
grep -rn 'pendiente.Implementar' --include='*.go' internal | wc -l
make test-pendiente          # nace en F0

# 4 · Cobertura por fichero de lo que ya está en verde (≥ 80 %, D-12)
make cobertura-ficheros      # nace en F0

# 5 · SKIP: en código NUEVO debe ser cero
GOWORK=off go test -v ./internal/modulos/... ./internal/nucleo/... ./internal/arranque/... 2>&1 \
  | grep -c -- '--- SKIP'     # 0, siempre. Uno solo es un defecto
```

`golangci-lint` tiene que ser **`v2.12.2`** (`Makefile:12`): otra versión da otro resultado. Si
no está instalado, el entorno no está preparado (ver el setup del entorno web en
`documentations/reorganizacion-modular/06-entorno-web.md`); no lo sustituyas por el que haya.

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
Gate ci-local: rc=0 · <N> paquetes ok · lint 0 issues
vet -tags pendiente: rc=0
Pendientes: <N> llamadas a pendiente.Implementar (antes: <M>)
Cobertura: <fichero> 87 % … (o: target aún no existe — F0 no completo)
SKIP en código nuevo: 0
No corrido: <lista>, y por qué
```

Si algo falló, se dice primero y con su salida. No se escribe «debería pasar».

## Antes de pushear a `dev` (solo la sesión local)

La sesión **web** no empuja a `dev`: empuja **su** rama y abre `gh pr create --base dev`
(`documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-WEB.md` §5–§6).

- `GATE_RC=0` leído del log, no de una notificación.
- `git status --short` sin restos que no sean del commit.
- `git fetch origin && git log --oneline -1 origin/dev`: que nadie empujó entretanto.
- `git push origin dev`, leyendo **su** `rc`.
