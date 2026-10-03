# Protocolo de una sesión CLI (Claude Code en la máquina de Jhoan)

> Lo lee **entero**, antes de nada, toda sesión local del plan. Se arranca con
> `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude` (así cargan las skills del
> repo y, por herencia, el `CLAUDE.md` de la raíz de wApp). Hay dos clases de sesión local:
> **de cierre** (la web abrió: la local hace lo que la web no puede e intenta **refutar** lo que la web dio
> por cierto; §2 y §5) y **completa** (no hubo web: la sesión escribe el código ella misma, como dice
> [`PROTOCOLO-WEB.md`](PROTOCOLO-WEB.md) §2, §3 y §5, y se cierra aquí; se salta §2 y §5). La ficha dice cuál es.
> La separación web/local dura lo que dure la promoción web; después todo es local.

## 1 · Verdad de campo

```bash
git fetch -q origin && git status --short && git branch --show-current
git log --oneline -1 origin/dev
make toolchain; echo "rc=$?"               # TOOLCHAIN=OK y rc=0: go1.26.5 y v2.12.2 EFECTIVOS
docker info >/dev/null && echo docker-ok
```

**La toolchain la pone el `Makefile`** (desde el 2026-10-02): no hace falta exportar `GOTOOLCHAIN`
ni instalar el lint aparte. En el Mac el sistema trae `go1.27.1` y `golangci-lint 2.14.0`
(Homebrew); `GO_SYSTEM` dice ese Go, pero lo que cuenta es `GO_EFFECTIVE` y `LINT_EFFECTIVE`.

- **Una vez por *checkout*** (el clon, y cada `git worktree` nuevo): `make tools`. Deja el
  `golangci-lint` fijado en `.bin/`, ignorado por git. Es idempotente.
- Si `make toolchain` da `TOOLCHAIN=NOT_READY` por el lint → `make tools` y otra vez
  `make toolchain`. Si es por el Go → hace falta red una vez (Go baja la toolchain a su caché).
- 🔴 **Un `go` suelto, fuera de `make`, es `go1.27.1`**: como gate lleva `GOTOOLCHAIN=go1.26.5`
  delante, o se usa su target. Sin toolchain fijada, **ningún gate es autoritativo**.
- Detalle, web y local lado a lado: [`../../06-entorno-web.md`](../../06-entorno-web.md) §6.

1. Si cierras una sesión web: localiza su rama / PR y, **si lo hay**, el traspaso en
   `documentations/reorganizacion-modular/traspasos/` (skill `traspaso-web-local`); léelo entero. Sin
   traspaso, la verdad son los `[x]` con SHA de `tareas.md` y el PR.
2. Comprueba que las decisiones que bloquean tu bloque en [`../DECISIONES.md`](../DECISIONES.md)
   están rellenas.
3. Lee de la fase: `README.md`, `reglas.md` y **tu bloque** de `tareas.md` (y lo que el traspaso
   cite). No más.
4. 🔴 **Worktrees de sub-agentes** (hallazgo 41 de F1): nacen de `origin/main`, no de `dev`. Si orquestas con
   `isolation: worktree`, dile al sub-agente que se ponga en el SHA de `dev` antes de medir, y borra los
   worktrees antes de `make test-pendiente`.

## 2 · Integrar en `dev`, sin squash

```bash
git checkout dev && git pull --ff-only origin dev
git merge --no-ff origin/<rama-de-la-web>; echo "rc=$?"
```

(o, si Jhoan ya lo fusionó en GitHub con «Rebase and merge», solo `git pull`). **Nunca squash**: el
rojo y el verde de un fichero son commits distintos.

## 3 · Repetir los gates con TU toolchain (skill `validar-antes-de-cerrar`)

`make toolchain` (`TOOLCHAIN=OK`, `rc=0`), `make ci-local` (rc sin pipe), `make vet-pendiente`,
`make test-pendiente`, SKIP en código nuevo = 0 con `-v` (es un `go` suelto:
`GOTOOLCHAIN=go1.26.5 GOWORK=off go test -v …`), y los que diga el bloque. **No hay umbral de cobertura**
(`05` E-9): `make cobertura-ficheros` es un informe. Compara con lo que declaró la web (PR o traspaso): una
diferencia es un hallazgo, no un ruido.

## 4 · Lo que solo la local puede hacer (lo que diga tu bloque)

- **Suites de contrato contra Postgres** (`05` P4): todo puerto con BD, en memoria **y** en Postgres con el
  arnés. Al cerrar una conmutación, el **test de cableado** del `bridge_<x>.go` está completo (`05` §4.2).
- **Procesos de F9** con testcontainers: `make test-procesos` contra el binario **viejo** y el
  **nuevo** (la variable del arnés), SKIP = 0 (skill `procesos-testcontainers`). Es el oráculo
  autoritativo: el pre-chequeo de la web no cierra nada.
- **Integración vieja** si se tocó código compartido (`platform`, F0): `WAPP_TEST_REQUIRE_DB=1 make
  test-integration`, contando `--- SKIP` con `-v` (solo 50 de los 91 ficheros que leen
  `WAPP_TEST_DB_DSN` honran `WAPP_TEST_REQUIRE_DB`).
- **`go.sum` con red real** tras un cambio de `go.mod`.
- **Arranque real** de `cmd/server-modular` cuando el bloque lo pida (nunca a la vez que `cmd/server`
  contra la misma BD o puertos).
- **UAT** por SSH y la documentación del ecosistema fuera del repo (F10).

## 5 · Refutar lo que la web dio por cierto

Cada afirmación del PR (o de la §7 del traspaso, si lo hay) se intenta **refutar contra el código y contra lo
que corre**, no contra la documentación. Lo que se refute, se corrige (o se anota como hallazgo en el
`README.md` de la fase).

## 6 · Cerrar

**Siempre las mismas tres cosas** (`05` E-12):

1. `tareas.md`: `[x]` con SHA de las tareas de la sesión (o `[~]` diciendo qué falta).
2. Un bloque en `ESTADO.md` de la reorganización. Si la sesión cierra una **fase**, lo dice ahí y en el
   estado del `README.md` de la fase.
3. Los hallazgos nuevos, en el `README.md` de la fase.

Y además: si había un traspaso abierto, su sección final **`CERRADO <fecha>`**; `git push origin dev` (rc sin
pipe); **`main` no se toca** salvo petición expresa de Jhoan. Una sesión es un bloque de **45–90 min**: si no
cabe, para en un punto limpio, cierra con las tres cosas y se relanza.
