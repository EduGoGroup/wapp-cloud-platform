# 06 · El entorno de Claude Code en la web

> Qué ve una sesión de claude.ai/code sobre este repo, qué hay que prepararle, y qué no puede
> hacer. Verificado contra la documentación de Claude Code el 2026-09-27.

## 1 · Qué llega a la sesión web

| | ¿Llega? | Nota |
|---|---|---|
| El repo `wapp-cloud-platform` (la rama que se elija) | ✅ | Solo este repo: ni la raíz de wApp ni sus hermanos |
| `CLAUDE.md` del repo | ✅ | Se carga siempre. Por eso apunta a `05` como norma |
| **Skills del repo** (`.claude/skills/`) | ✅ | Documentado: las skills de proyecto se cargan en las sesiones en la nube |
| Skills de la raíz de wApp (`ejecutar-plan`, `pasar-la-pelota`…) | ❌ | Viven en otro repo |
| Skills personales (`~/.claude/skills`) | ❌ | Solo en local |
| Hooks de `.claude/settings.json` | ❓ | La documentación **no confirma** que corran en la nube. **No se confía en ellos para hacer cumplir nada**: para eso están los candados en tests de `05` §5, que corren en cualquier sitio |
| Docker | ❌ | Por eso los tests de proceso (F9) los corre la sesión local |

## 2 · Las skills de este repo

| Skill | Para qué |
|---|---|
| [`contrato-tdd`](../../.claude/skills/contrato-tdd/SKILL.md) | Un fichero, de contrato sin lógica a verde |
| [`reconstruir-modulo`](../../.claude/skills/reconstruir-modulo/SKILL.md) | Orquestar una fase entera (F0, o un módulo de F1–F8) |
| [`validar-antes-de-cerrar`](../../.claude/skills/validar-antes-de-cerrar/SKILL.md) | Los gates, leídos sin engañarse |
| [`traspaso-web-local`](../../.claude/skills/traspaso-web-local/SKILL.md) | El fichero de traspaso entre la sesión web y la local |
| [`procesos-testcontainers`](../../.claude/skills/procesos-testcontainers/SKILL.md) | Los tests de proceso de F9, con testcontainers |

Todas remiten a [`05`](05-metodo-contratos-y-tdd.md) como norma: si una skill y `05` chocan, manda
`05` y la skill se corrige.

## 3 · El script de preparación del entorno

Claude Code en la web permite un **script de setup por entorno**, que corre al preparar la máquina.
No es un fichero del repo: se pega en la configuración del entorno en claude.ai/code. Sin él,
`make ci-local` no puede pasar, porque el lint está **fijado** a una versión.

```bash
#!/usr/bin/env bash
set -euo pipefail

# Go: go.mod fija `go 1.26.5`. Con GOTOOLCHAIN=auto (el defecto), un Go más viejo descarga la
# toolchain exacta al primer comando. NO se baja la línea de go.mod para acomodar el entorno.
go version
export GOTOOLCHAIN=auto

# golangci-lint FIJADO a la versión del Makefile (LINT_VERSION := v2.12.2). Otra versión da otro
# resultado, y un gate con otra versión no es autoritativo.
LINT_VERSION=v2.12.2
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
  | sh -s -- -b "$(go env GOPATH)/bin" "$LINT_VERSION"
"$(go env GOPATH)/bin/golangci-lint" version

# Descargar dependencias una vez (el repo es público y sus dependencias del grupo también).
cd "${CLAUDE_PROJECT_DIR:-.}" 2>/dev/null || true
GOWORK=off go mod download
```

⚠️ **Sin verificar desde aquí**: que la red del entorno web deje llegar a `proxy.golang.org` y a
`raw.githubusercontent.com`. Si no, el script falla en su primera línea de red y hay que abrir
esos dominios en la configuración del entorno. La primera sesión web debe correr
`golangci-lint version` y `GOWORK=off go build ./...` y anotar el resultado aquí.

## 4 · Comprobación al empezar cualquier sesión web

```bash
go version                          # 1.26.5 (o la toolchain descargada por GOTOOLCHAIN)
golangci-lint version               # v2.12.2 — si no, el entorno no está preparado
GOWORK=off go build ./... ; echo "rc=$?"
git fetch -q origin && git log --oneline -1 origin/dev
```

Si `golangci-lint` no es la `v2.12.2`, **no se declara ningún gate pasado**: se dice que el entorno
no está listo (skill `validar-antes-de-cerrar`).
