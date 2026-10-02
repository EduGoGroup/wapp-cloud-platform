# Protocolo de una sesión CLI (Claude Code en la máquina de Jhoan)

> Lo lee **entero**, antes de nada, toda sesión local del plan. Se arranca con
> `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude` (así cargan las skills del
> repo y, por herencia, el `CLAUDE.md` de la raíz de wApp). La regla madre: **la web abre, la local
> cierra** — la local hace **solo** lo que la web no puede, e intenta **refutar** lo que la web dio
> por cierto.

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

1. Localiza la rama / el PR de la sesión web que cierras y el **traspaso** en
   `documentations/reorganizacion-modular/traspasos/`. **Léelo entero** (skill `traspaso-web-local`).
2. Comprueba que las decisiones que bloquean tu bloque en [`../DECISIONES.md`](../DECISIONES.md)
   están rellenas.
3. Lee de la fase: `README.md`, `reglas.md` y **tu bloque** de `tareas.md` (y lo que el traspaso
   cite). No más.

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
`GOTOOLCHAIN=go1.26.5 GOWORK=off go test -v …`), y los que diga el bloque. Compara con la §3 del
traspaso: una diferencia es un hallazgo, no un ruido.

## 4 · Lo que solo la local puede hacer (lo que diga tu bloque)

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

## 5 · Refutar la §7 del traspaso

Cada afirmación de la §7 se intenta **refutar contra el código y contra lo que corre**, no contra la
documentación. Lo que se refute, se corrige (o se anota como hallazgo en el `README.md` de la fase).

## 6 · Cerrar

1. En el traspaso, sección final **`CERRADO <fecha>`**: qué hiciste, qué refutaste, qué queda.
2. `tareas.md`: `[x]` con SHA de las tareas 💻 y 🌐→💻 del bloque.
3. `ESTADO.md` de la reorganización al día.
4. `git push origin dev` (rc sin pipe). **`main` no se toca** salvo petición expresa de Jhoan.
5. Si el bloque cierra una **fase**, dilo en `ESTADO.md` y en el `README.md` de la fase (estado).
