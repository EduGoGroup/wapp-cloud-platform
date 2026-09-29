# 00-01 · Preparar el entorno de Claude Code en la web · 🧑 Jhoan

> Una sola vez, antes de la primera sesión web (F0-01). No es un prompt: lo haces tú en
> claude.ai/code. Todo lo que hay que pegar está en
> [`../00-marco/flujo-web-local.md`](../00-marco/flujo-web-local.md) §3 — **cópialo de allí**, no de
> aquí, para no tener dos versiones.

## Checklist

- [ ] En claude.ai/code, crear (o editar) el **entorno** para `EduGoGroup/wapp-cloud-platform`.
- [ ] **Acceso de red: Trusted** (el de por defecto). Incluye `proxy.golang.org`, `sum.golang.org`,
      `github.com`, `raw.githubusercontent.com` y Docker Hub.
- [ ] **Variables del entorno** (`flujo-web-local.md` §3): `GOTOOLCHAIN=go1.26.5`, `GOWORK=off`,
      `BASH_DEFAULT_TIMEOUT_MS=600000`, `BASH_MAX_TIMEOUT_MS=1800000`. ⚠️ Ni un secreto: quien use el
      entorno puede leerlas.
- [ ] **Setup script** (`flujo-web-local.md` §3): el que **no aborta nunca** (instala el lint
      `v2.12.2` con `go install`, descarga dependencias y trae `postgres:17-alpine`). 🔴 Un setup que
      sale con `rc≠0` impide arrancar la sesión.
- [ ] Decisiones **F-1** (quién fusiona los PR) y **F-2** (este entorno aplicado) anotadas en
      [`../DECISIONES.md`](../DECISIONES.md) §1.
- [ ] En GitHub, la rama por defecto del repo es `main`: recuerda que toda sesión web se arranca
      **sobre `dev`** y abre el PR con `--base dev`.

## Cómo se arranca cada sesión web después

claude.ai/code → repo `EduGoGroup/wapp-cloud-platform` → **rama base `dev`** → este entorno → pegar
el prompt de la sesión. Alternativa desde la terminal: `claude --cloud "<prompt>"` (clona la rama
actual del remoto: empuja antes).

## Qué comprobará la primera sesión (F0-01)

Que `go version` da `go1.26.5`, `golangci-lint version` da `v2.12.2`, cuánto tarda `make ci-local`, si
Docker y testcontainers funcionan en la VM, y si el proxy acepta `git push --force-with-lease`. Lo
anota en [`../../06-entorno-web.md`](../../06-entorno-web.md) §5. Si algo falla, se ajusta **el
entorno**, nunca el proyecto.
