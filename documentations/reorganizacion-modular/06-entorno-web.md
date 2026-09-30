# 06 · El entorno de Claude Code en la web

> Qué ve una sesión de claude.ai/code sobre este repo, qué hay que prepararle, y qué no puede
> hacer. Verificado contra la documentación de Claude Code el 2026-09-27.
>
> ✎ **Corregido el 2026-09-28** contra la documentación oficial
> (`code.claude.com/docs/en/cloud-environments` y `code.claude.com/docs/en/claude-code-on-the-web`).
> La versión del 2026-09-27 se equivocaba en dos hechos (Docker y hooks) y su *setup script* habría
> **impedido arrancar** la sesión si fallaba. Cada fila o párrafo que cambió lleva ✎. El reparto
> completo web ↔ local, las ramas, el hook propuesto y el protocolo de sesión viven en
> [`plan/00-marco/flujo-web-local.md`](plan/00-marco/flujo-web-local.md).

## 1 · Qué llega a la sesión web

| | ¿Llega? | Nota |
|---|---|---|
| El repo `wapp-cloud-platform` (la rama que se elija) | ✅ | Solo este repo: ni la raíz de wApp ni sus hermanos. ✎ Es un **clon fresco** de GitHub: lo no empujado no existe. 🔴 La rama por defecto del remoto es `main`: se arranca sobre `dev` |
| `CLAUDE.md` del repo | ✅ | Se carga siempre. Por eso apunta a `05` como norma |
| **Skills del repo** (`.claude/skills/`) | ✅ | Documentado: las skills de proyecto se cargan en las sesiones en la nube. ✎ También `.claude/agents/` y `.mcp.json` |
| Skills de la raíz de wApp (`ejecutar-plan`, `pasar-la-pelota`…) | ❌ | Viven en otro repo |
| Skills personales (`~/.claude/skills`) | ❌ | Solo en local |
| Hooks de `.claude/settings.json` | ✅ ✎ | **Corren** en una sesión de **un solo repo** (la doc lo dice en «What carries over»). Corren también en local: para limitar algo a la nube, el script comprueba `CLAUDE_CODE_REMOTE=true` (vale `true` en la VM, **nunca** en local). Los candados en tests de `05` §5 siguen siendo la única garantía; el hook **avisa**, no hace cumplir (diseño en `flujo-web-local.md` §4). Hoy el repo **no tiene** `.claude/settings.json` |
| Docker | ✅ ✎ | **Preinstalado**: `docker`, `dockerd`, `docker compose`; Docker Hub está en la lista *Trusted*. ⚠️ **Sin probar** que testcontainers funcione ahí: lo prueba la primera sesión web (`flujo-web-local.md` §5). 🔒 Aunque funcione, **F9 y el relevo los cierra la sesión local**; la web corre los procesos solo como pre-chequeo |
| PostgreSQL 16 preinstalado ✎ | ⚠️ | Viene en la VM. **Prohibido** para cualquier test: es un Postgres vivo (`05` §7.2) |
| Red ✎ | *Trusted* | Por defecto: `proxy.golang.org`, `sum.golang.org`, `index.golang.org`, `github.com`, `raw.githubusercontent.com`, `storage.googleapis.com`, Docker Hub… GitHub va por un **proxy propio** que solo sirve *release assets* de los repos **adjuntos** a la sesión |
| `git push` ✎ | ⚠️ | **Solo a la rama de trabajo actual** de la sesión; fetch, clone y PR funcionan (`gh` preinstalado). La web **no empuja a `dev`**: abre PR a `dev` (`gh pr create --base dev`), que se integra **sin squash** (`05` E-4) |
| Sub-agentes ✎ | ✅ | Funcionan igual que en local |
| Límites ✎ | ⚠️ | VM Ubuntu 24.04 x86_64, ~4 vCPU, 16 GB, 30 GB. Un comando espera **2 min** por defecto (hasta 10) y luego pasa a segundo plano: se sube con `BASH_DEFAULT_TIMEOUT_MS`/`BASH_MAX_TIMEOUT_MS` en las variables del entorno. La sesión **se detiene por inactividad** y lo que corría en segundo plano no se restaura |

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

✎ **El script y las variables vigentes están en
[`plan/00-marco/flujo-web-local.md`](plan/00-marco/flujo-web-local.md) §3.** El que había aquí
(2026-09-27) se retira por tres motivos, todos de la documentación oficial:

1. **Un *setup script* que sale con `rc≠0` impide que la sesión arranque.** El viejo empezaba con
   `set -euo pipefail`: un fallo de red en el lint dejaba sin sesión. El nuevo no es fatal en nada;
   quien avisa es el hook `SessionStart`.
2. **El `install.sh` del lint descarga un *release asset* de `github.com/golangci/golangci-lint`**, y
   el proxy de GitHub de la sesión solo sirve *release assets* de los repos adjuntos (403
   esperable). El nuevo instala primero con `go install …@v2.12.2` por `proxy.golang.org`.
3. **`GOTOOLCHAIN=auto` no fija la versión**: la línea `go 1.26.5` es un mínimo y un Go
   preinstalado más nuevo correría tal cual. Se fija con `GOTOOLCHAIN=go1.26.5` como **variable del
   entorno**, junto a `GOWORK=off`, `BASH_DEFAULT_TIMEOUT_MS=600000` y `BASH_MAX_TIMEOUT_MS=1800000`.

Además: el script corre **como root** y, si termina en menos de ~5 min, su resultado se guarda como
**snapshot** del entorno (≈7 días; se rehace al cambiar el script o la red). Por eso también trae
`postgres:17-alpine` al snapshot.

⚠️ **Sin verificar desde aquí**: cuánto tarda el script (el `go install` del lint compila desde
fuente) y en qué directorio está el clon mientras corre. La primera sesión web corre
`golangci-lint version` y `GOWORK=off go build ./...` y anota el resultado aquí.

## 4 · Comprobación al empezar cualquier sesión web

```bash
go version                          # go1.26.5 (con GOTOOLCHAIN=go1.26.5 en el entorno)
golangci-lint version               # v2.12.2 — si no, el entorno no está preparado
GOWORK=off go build ./... ; echo "rc=$?"
git fetch -q origin && git log --oneline -1 origin/dev
docker info >/dev/null 2>&1; echo "docker rc=$?"   # ✎ informativo
```

Si `golangci-lint` no es la `v2.12.2`, **no se declara ningún gate pasado**: se dice que el entorno
no está listo (skill `validar-antes-de-cerrar`). ✎ Cuando F0 añada el hook `SessionStart`
(`plan/00-marco/flujo-web-local.md` §4), estos comandos los corre el hook y su salida llega sola al
contexto de la sesión.

## 5 · Resultados de la primera sesión web ✎

> **2026-09-30 · sesión F0-01** (T0.0). Medido en la VM, cada `rc` leído del log, sin pipe. La
> sonda de testcontainers vivió en el *scratchpad* de la sesión (fuera del árbol), no se commitea, y
> `git diff go.mod go.sum` quedó vacío.

**Máquina y entorno.** Ubuntu 24.04.4 · 4 vCPU (`nproc`) · 15 GiB · entorno `cloud_default`. Las
cuatro variables de [`plan/00-marco/flujo-web-local.md`](plan/00-marco/flujo-web-local.md) §3
**están aplicadas** (`GOTOOLCHAIN=go1.26.5`, `GOWORK=off`, `BASH_DEFAULT_TIMEOUT_MS=600000`,
`BASH_MAX_TIMEOUT_MS=1800000`) y `CLAUDE_CODE_REMOTE=true`.

| # | Comando | Resultado | `rc` |
|---|---|---|---|
| 1 | `go version` | `go1.26.5 linux/amd64` (y `go env GOVERSION` = `go1.26.5`) | 0 |
| 2 | `golangci-lint version` | `2.12.2 built with go1.26.2 from c0d3ddc9 on 2026-05-06` en `/usr/local/bin` → **es la fijada**: los gates de esta sesión **cuentan** | 0 |
| 3 | `GOWORK=off go build ./...` | 48,9 s de pared (módulos ya en caché por el *setup*) | 0 |
| 4 | `GOWORK=off make ci-local` | `GATE_RC=0` leído del log · **217 s** · 71 paquetes `ok`, 0 `FAIL`, lint `0 issues` | 0 |
| 5 | `docker info` | al empezar: `failed to connect to the docker API at unix:///var/run/docker.sock … no such file or directory` — **el daemon no corre**. Tras `(nohup dockerd >/tmp/dockerd.log 2>&1 &)` (la sesión es root): Docker 29.3.1, `overlayfs`, cgroup v1 | 1 → 0 |
| 6 | `docker run --rm postgres:17-alpine postgres --version` | Docker Hub: `unexpected status from HEAD request to https://registry-1.docker.io/v2/library/postgres/manifests/17-alpine: 429 Too Many Requests` (dos veces, con 20 s entre medias). Por el espejo `mirror.gcr.io/library/postgres:17-alpine` (9,7 s): `postgres (PostgreSQL) 17.11` | 125 → 0 |

**Tiempo de `ci-local`**: 217 s en 4 vCPU. No es un frío puro: un `go build ./...` de 49 s lo
precedió (caché de compilación sin `-race` caliente; la de `-race`, la del lint y la de tests,
frías). Referencia: 148 s en frío en local con 8 núcleos ([`plan/00-marco/tecnologia.md`](plan/00-marco/tecnologia.md) §4).
Cabe en `BASH_DEFAULT_TIMEOUT_MS` (10 min), pero **no** en los 2 min por defecto de antes.

**Setup script.** Corrió (el lint y la caché de módulos tienen fecha de arranque de la VM, 15:46–15:47,
≈1–2 min: estimado por fechas, **sin medir**). El `golangci-lint` instalado lleva la firma del
binario publicado (`built with go1.26.2 from c0d3ddc9`), no la de un `go install` (que diría
`go1.26.5`): ganó la vía `install.sh`, así que el `go install` falló o no llegó a correr — **sin
verificar** cuál. El paso 4 (`docker pull`) **no dejó la imagen** en el *snapshot*: `docker images`
salió vacío, coherente con que el daemon no corre durante el *setup*.

**Prueba de testcontainers** (receta de `flujo-web-local.md` §5: `testcontainers-go/modules/postgres`
v0.44.0 + `pgx/v5/stdlib` v5.11.0; `postgres.Run(…"postgres:17-alpine"…, BasicWaitStrategies())` →
`SELECT 1` → `CREATE DATABASE c TEMPLATE p` → `Terminate`):

| Corrida | Imágenes | Contenedor listo | `CREATE DATABASE … TEMPLATE` | `go test` | `TC_RC` |
|---|---|---|---|---|---|
| 1 | `postgres:17-alpine` retaggeada desde el espejo; Ryuk `testcontainers/ryuk:0.14.0` de Docker Hub (esa vez sin 429) | 5,1 s | 40 ms | 5,6 s (17 s con compilación) | **0** |
| 2 | ninguna local; `TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX=mirror.gcr.io/` (Postgres **y** Ryuk por el espejo) | 12,8 s | 52 ms | 13,4 s (15 s) | **0** |

- **Reaper (Ryuk)**: arrancó las dos veces; ~15 s después, `docker ps -a --filter label=org.testcontainers=true` vacío.
- **Puerto mapeado**: alcanzable desde el proceso de test (`localhost:32769` → 5432 en la corrida 1).
- **PostgreSQL 16 de la VM**: instalado (`/usr/lib/postgresql/16`), **no escucha** en 5432. Sigue prohibido.

**Veredicto: testcontainers FUNCIONA en la web** → la web puede correr los procesos de F9 como
**pre-chequeo** (W-1); quien los cierra sigue siendo la sesión local. Dos condiciones, ninguna
cambia el proyecto:

1. **Arrancar el daemon** al empezar: `(nohup dockerd >/tmp/dockerd.log 2>&1 &)`. El hook de
   `SessionStart` lo detecta y lo dice («Docker: NO responde»), pero no lo arranca: verifica, no instala.
2. **Docker Hub limita por IP compartida (429)**: si pasa, `TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX=mirror.gcr.io/`
   (espejo público de Google, probado arriba en frío) en la línea de `go test`. Que vaya a las
   variables del entorno es decisión de Jhoan (README de F0, «Decisiones que necesita»).
