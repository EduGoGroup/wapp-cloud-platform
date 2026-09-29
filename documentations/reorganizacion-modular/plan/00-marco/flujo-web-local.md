# Flujo web ↔ local — quién hace qué, ramas, entorno y protocolo de sesión

> Los hechos del entorno web salen de la **documentación oficial de Claude Code**
> (`code.claude.com/docs/en/cloud-environments` y `…/claude-code-on-the-web`), leída el
> **2026-09-28**. Lo que la doc no dice y nadie ha probado se marca **«sin verificar»**.
> Procedimiento del traspaso: skill `traspaso-web-local`. Gates: skill `validar-antes-de-cerrar`.

## 1 · Qué hace cada entorno

**Regla madre**: la **web abre** (escribe contratos, tests, lógica y docs, corre los gates que su
entorno permite); la **local cierra** (solo lo que la web no puede, e intenta refutar lo que la web
dio por cierto). No se adapta el proyecto al entorno: ni bajar Go, ni `t.Skip`, ni un Postgres vivo.

| | Web (claude.ai/code, `claude --cloud`) | Local (Claude Code en la máquina de Jhoan) |
|---|---|---|
| Ve | **Solo este repo** (clon fresco de GitHub): `CLAUDE.md`, `.claude/skills/`, `.claude/agents/`, `.mcp.json`, hooks de `.claude/settings.json` (sesión de **un** repo) | Todo el ecosistema wApp |
| No ve | Skills de la raíz de wApp (`ejecutar-plan`, `pasar-la-pelota`…), skills y `CLAUDE.md` personales, la documentación del ecosistema | — |
| Máquina | VM Ubuntu 24.04 x86_64 · ~4 vCPU · 16 GB · 30 GB · Go, Docker (`docker`, `dockerd`, `docker compose`), `gh`, **PostgreSQL 16 preinstalado** (🚫 prohibido para tests: es un Postgres vivo) | macOS arm64, Go 1.27.1, Docker, lint v2.14.0 en el `PATH` (⚠️ [`tecnologia.md`](tecnologia.md) §1) |
| Red | Nivel **Trusted**: `proxy.golang.org`, `sum.golang.org`, `index.golang.org`, `github.com`, `raw.githubusercontent.com`, `storage.googleapis.com`, Docker Hub… GitHub va por un **proxy propio** | Sin límite |
| Git | `git push` **solo a su rama de trabajo actual**; fetch, clone y PR funcionan (`gh` preinstalado). El proxy de GitHub solo sirve *release assets* de los repos **adjuntos** a la sesión | Todo, incluido `dev` y (a petición de Jhoan) `main` |
| Sub-agentes | ✅ funcionan igual que en local | ✅ |
| Tiempo | Un comando espera 2 min por defecto (hasta 10) y luego **pasa a segundo plano**; la sesión **se detiene por inactividad** y el trabajo en segundo plano (sub-agentes, comandos) **no se restaura** | Sin límite práctico |
| Docker / testcontainers | Docker ✅ · testcontainers **sin probar** (§5) | ✅ |
| UAT, `make test-integration`, ecosistema, `main` | ❌ | ✅ |

## 2 · Ramas y PR

- **La web trabaja en su rama** y solo puede empujar esa. Nombre: el que asigne la sesión, o
  `reorg/<fase>-<bloque>` si se elige al arrancar. 🔴 La rama por defecto de GitHub es **`main`**
  (`origin/HEAD -> origin/main`): la sesión se arranca **sobre `dev`** y el PR se abre con base
  explícita: `gh pr create --base dev`.
- **Un PR no valida nada** (`ci.yml` es `workflow_dispatch`). El cuerpo del PR lleva el informe del
  gate (formato de `validar-antes-de-cerrar`) y el enlace a la sesión
  (`https://claude.ai/code/${CLAUDE_CODE_REMOTE_SESSION_ID/#cse_/session_}`).
- **Se integra SIN squash**: el `rojo` y el `verde` de un fichero deben seguir siendo commits
  distintos (`05` E-4). En GitHub: **«Create a merge commit»** o **«Rebase and merge»**; nunca
  «Squash and merge».
- **Quién fusiona** (propuesta, **decisión F-1**): por defecto **Jhoan** desde GitHub, con
  «Rebase and merge», cuando el bloque es 🌐 y el PR trae `GATE_RC=0`. Si el bloque deja tareas
  🌐→💻, fusiona **la sesión local** tras sus gates: `git fetch origin && git checkout dev &&
  git merge --no-ff origin/<rama>` y `git push origin dev`, leyendo cada `rc`.
- **`dev` siempre verde**: antes de fusionar, la rama está **al día con `origin/dev`** y su gate se
  corrió **sobre esa base**. Si `dev` avanzó, la web rebasa (`git rebase origin/dev`) y empuja su
  rama (`git push --force-with-lease`; **sin verificar** que el proxy lo acepte — si no, `git merge
  origin/dev` en la rama), y repite el gate.
- Si una sesión web se arranca **con `dev` como rama de trabajo**, podría empujar a `dev`
  directamente. **No se hace**: se pierde la revisión y la regla «la local cierra».
- `main` solo lo mueve la sesión local a petición de Jhoan; un push a `main` dispara
  `sync-main-to-dev.yml`.

## 3 · El entorno web: variables y *setup script*

Se configuran en claude.ai/code, en el diálogo del entorno (no son ficheros del repo). ⚠️ Quien use
el entorno **puede leer** sus variables y su script: **ni un secreto** ahí.

**Variables del entorno** (formato `.env`):

```text
GOTOOLCHAIN=go1.26.5
GOWORK=off
BASH_DEFAULT_TIMEOUT_MS=600000
BASH_MAX_TIMEOUT_MS=1800000
```

`GOTOOLCHAIN=go1.26.5` fija la versión **exacta** (con `auto`, un Go preinstalado más nuevo correría
tal cual). Los dos `BASH_*` suben la espera por defecto a 10 min y el máximo a 30: `make ci-local`
tardó **148 s en frío** en local con 8 núcleos ([`tecnologia.md`](tecnologia.md) §4); en 4 vCPU,
sin medir.

**Setup script** — corre **como root** antes de que arranque Claude Code, solo en la nube; si
termina en < ~5 min, su resultado se guarda como **snapshot** (≈7 días) y las sesiones siguientes
no lo repiten. 🔴 **Si sale con `rc≠0`, la sesión no arranca**: aquí nada es fatal; quien avisa es el
hook de §4.

```bash
#!/bin/bash
# Setup del entorno web de wapp-cloud-platform. Nada fatal: el hook SessionStart verifica y avisa.
set -u
GO_WANT=go1.26.5
LINT_WANT=v2.12.2
export GOTOOLCHAIN=$GO_WANT GOWORK=off

# 1 · Toolchain exacta (se descarga de proxy.golang.org, que está en Trusted).
go version || echo "SETUP: go no responde"

# 2 · golangci-lint FIJADO. Primero por el proxy de Go: el install.sh baja un release asset de
#     github.com/golangci/golangci-lint, y el proxy de GitHub de la sesión solo sirve release assets
#     de los repos adjuntos (403 esperable). El segundo intento queda por si acaso.
GOBIN=/usr/local/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$LINT_WANT \
  || curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
       | sh -s -- -b /usr/local/bin $LINT_WANT \
  || echo "SETUP: golangci-lint $LINT_WANT NO instalado"

# 3 · Dependencias del módulo (repo y dependencias del grupo son públicos). Sin verificar en qué
#     directorio está el clon cuando corre el setup: se prueba en los candidatos.
for d in "${CLAUDE_PROJECT_DIR:-}" "$PWD" /home/user/wapp-cloud-platform; do
  [ -n "$d" ] && [ -f "$d/go.mod" ] && (cd "$d" && go mod download) && break
done || echo "SETUP: go mod download pendiente (lo hará el primer go build)"

# 4 · La imagen de los tests de proceso, al snapshot (Docker Hub está en Trusted).
docker pull postgres:17-alpine >/dev/null 2>&1 || echo "SETUP: no se pudo traer postgres:17-alpine"
exit 0
```

**Sin medir**: cuánto tarda (el `go install` del lint compila desde fuente). Si pasa de ~5 min no se
cachea; entonces el paso 2 se mueve al hook en segundo plano.

## 4 · Hook `SessionStart` propuesto (lo implementa una tarea de F0)

Solo diseño. **Verifica y avisa; no instala nada.** Corre en web **y** en local (los hooks del repo
corren en los dos; la doc lo dice): así también delata el lint v2.14.0 de la máquina local. Su
salida estándar entra en el contexto de la sesión.

`.claude/settings.json` (hoy **no existe** en el repo):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|resume",
        "hooks": [
          { "type": "command",
            "command": "bash \"$CLAUDE_PROJECT_DIR\"/.claude/hooks/verificar-entorno.sh",
            "timeout": 90 }
        ]
      }
    ]
  }
}
```

`.claude/hooks/verificar-entorno.sh`:

```bash
#!/bin/bash
# Verifica la toolchain de la reconstrucción y AVISA. Nunca instala, nunca falla la sesión.
cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0
ENT=local; [ "${CLAUDE_CODE_REMOTE:-}" = "true" ] && ENT=web
GO_WANT=go1.26.5; LINT_WANT=2.12.2; AVISOS=0
echo "== Verdad de campo ($ENT) =="
GOV=$(GOWORK=off go env GOVERSION 2>/dev/null)
[ "$GOV" = "$GO_WANT" ] || { echo "⚠️ Go es '$GOV', la fijada es $GO_WANT (GOTOOLCHAIN=$GO_WANT)"; AVISOS=1; }
LV=$(golangci-lint version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
[ "$LV" = "$LINT_WANT" ] || { echo "⚠️ golangci-lint es '${LV:-ausente}', el fijado es v$LINT_WANT: NINGÚN gate es autoritativo"; AVISOS=1; }
if [ "$ENT" = web ]; then docker info >/dev/null 2>&1 && echo "Docker: responde" || echo "Docker: NO responde"; fi
timeout 20 git fetch -q origin 2>/dev/null
echo "Rama: $(git branch --show-current) · origin/dev: $(git log --oneline -1 origin/dev 2>/dev/null)"
echo "Pendientes: $(grep -rn --include='*.go' --exclude-dir=pendiente 'pendiente\.Implementar(' internal 2>/dev/null | grep -vc '_test\.go:')"
ls documentations/reorganizacion-modular/traspasos/*.md 2>/dev/null | while read -r f; do
  grep -q '^## CERRADO' "$f" || echo "Traspaso ABIERTO: $f"; done
[ "$AVISOS" = 0 ] && echo "Toolchain: OK" || echo "Toolchain: NO LISTA — dilo en el informe, no la sustituyas"
exit 0
```

La tarea de F0 lo prueba en local (`CLAUDE_CODE_REMOTE` sin poner) y deja escrito en
`../../06-entorno-web.md` lo que vio la primera sesión web.

## 5 · La prueba de Docker + testcontainers (primera sesión web)

🔒 Decisión de Jhoan (2026-09-27): la **primera sesión web lo prueba**. Si funciona, la web corre
los procesos como **pre-chequeo**; **quien cierra F9 y el relevo sigue siendo la sesión local**.
La prueba se hace **fuera del repo** (no toca `go.mod`):

```bash
docker info >/dev/null; echo "docker rc=$?"
docker run --rm postgres:17-alpine postgres --version; echo "run rc=$?"
mkdir -p /tmp/tcprueba && cd /tmp/tcprueba && go mod init tcprueba >/dev/null 2>&1
go get github.com/testcontainers/testcontainers-go/modules/postgres@v0.44.0 github.com/jackc/pgx/v5/stdlib
# prueba_test.go: postgres.Run(ctx,"postgres:17-alpine",postgres.WithDatabase("p"),
#   postgres.BasicWaitStrategies()) → ConnectionString(ctx,"sslmode=disable") → sql.Open("pgx",…)
#   → SELECT 1 → CREATE DATABASE c TEMPLATE p → Terminate. Con -v y el rc al log.
go test -v -count=1 ./... > /tmp/tc.log 2>&1; echo "TC_RC=$?" >> /tmp/tc.log; tail -1 /tmp/tc.log
docker ps -a --filter label=org.testcontainers=true   # vacío tras la corrida (reaper)
```

Se anota en `06` §5 (el hueco «Resultados de la primera sesión web») y en `ESTADO.md`: `docker rc`, `TC_RC`, tiempo, si el reaper (Ryuk) arrancó y
si el puerto mapeado fue alcanzable. Si falla, se anota el error literal y **la web no corre
procesos**; nada más cambia.

## 6 · Protocolo de sesión

**Al empezar (toda sesión, web o local)** — verdad de campo antes que memoria:

1. Leer la salida del hook (o correr sus comandos a mano si aún no existe).
2. `git fetch -q origin && git status --short && git branch --show-current && git log --oneline -1 origin/dev`.
3. Leer `../../ESTADO.md`, el `README.md` de la fase y su `tareas.md`; localizar el **bloque** que
   toca y confirmar que sus dependencias están `[x]` **con SHA que existe** (`git cat-file -e <sha>`).
4. Si hay un traspaso abierto que le toca, leerlo **entero** (skill `traspaso-web-local`).
5. Si la fase anterior era F1 y no hay decisión escrita de Jhoan, **parar**.

**Al terminar**:

1. Gates con `validar-antes-de-cerrar`; `rc` del log, SKIP en código nuevo = 0.
2. `tareas.md`: `[x] … — cerrada en \`<sha>\`` o `[~]` con lo que falta. Nunca `[x]` sin SHA.
3. `../../ESTADO.md`: fase, bloque, siguiente paso, SHA de `origin/dev` y de la rama.
4. Si algo lo cierra la local: **traspaso** en `documentations/reorganizacion-modular/traspasos/`
   con la skill `traspaso-web-local` (ocho secciones; la §7 con contenido real).
5. Web: `git push` de su rama y `gh pr create --base dev` con el informe. Local: integrar en `dev`.
6. Si la sesión aprendió algo que la norma no decía, va a «Contradicciones encontradas» o
   «Decisiones que necesita» del `README.md` de la fase. No se decide en silencio.

## 7 · `--teleport` y `--cloud`

- `claude --teleport [<id>]` (o `/teleport` dentro del CLI) trae una sesión web a la terminal local:
  **hace checkout de su rama y carga la conversación**. Exige: árbol limpio, un checkout **del
  mismo repo**, la rama **empujada**, la misma cuenta de claude.ai. La copia local es propia: lo que
  se haga allí no vuelve a la sesión web.
- Úsalo para cerrar en local un bloque 🌐→💻 con el contexto a mano. **No sustituye al traspaso**:
  la conversación no vive en el repo, y la sesión local siguiente puede no ser la que teletransportó.
- `claude --cloud "<tarea>"` lanza una sesión web nueva desde la terminal, clonando **la rama actual
  del remoto** (empuja antes). Sirve para lanzar el siguiente bloque 🌐 con su prompt de `plan/sesiones/`.

## 8 · Quién cierra qué

| Tipo de trabajo | Escribe | Corre | Cierra |
|---|---|---|---|
| Contrato + rojo (`rojo(<m>)`) | 🌐 | 🌐 `vet -tags pendiente`, `ci-local` | 🌐 |
| Verde de un fichero (`verde(<m>)`) | 🌐 | 🌐 test del paquete, `cobertura-ficheros`, `ci-local` | 🌐 |
| Conmutar un módulo (`conmutar(<m>)`) | 🌐 | 🌐 `huella_test`, `ci-local` · 💻 `test-integration` si toca `platform` | 🌐, o 🌐→💻 si hubo cambio en `platform` |
| Mudar rutas a `apipublica` | 🌐 | 🌐 huella de rutas | 🌐 |
| F0: ✎ de `platform` (código que comparten los dos arranques) | 🌐 | 🌐 `ci-local` · 💻 `make test-integration` (`WAPP_TEST_REQUIRE_DB=1`, SKIP contados) | 🌐→💻 |
| F0: hook `SessionStart`, targets `make` | 🌐 | 🌐 (T0.1 prueba las dos ramas del script, con y sin `CLAUDE_CODE_REMOTE`); 💻 lo ve correr al arrancar F0-06 | 🌐 (sin traspaso propio: F0 bloque A es 🌐) |
| Cambio de `go.mod` (testcontainers) | 🌐 | 💻 `go.sum` con red real, `test-integration` | 🌐→💻 |
| Proceso de F9 (`procesos(<p>)`) | 🌐 | 🌐 `vet -tags integracion`; pre-chequeo con Docker si §5 salió bien · 💻 `make test-procesos` viejo **y** nuevo | 💻 |
| Prueba en UAT en sustitución (D-9) | — | 💻 | 💻 |
| Relevo F10 | 🌐 | 🌐 `ci-local` · 💻 procesos, `test-integration`, despliegue UAT | 💻 |
| Docs del plan, `ESTADO.md`, `tareas.md` | 🌐 / 💻 | — | quien cierra la tarea |
| Documentación del ecosistema (fuera del repo) | 💻 | — | 💻, al final |
| Mover `main` | — | — | 💻, solo si Jhoan lo pide |

## 9 · Decisiones que necesita Jhoan

- **F-1 · Quién fusiona los PR de la web.** Recomendación: Jhoan con «Rebase and merge» para
  bloques 🌐; la sesión local con `merge --no-ff` para bloques 🌐→💻. Nunca squash.
- **F-2 · Aplicar las variables y el script de §3** en el entorno de claude.ai/code antes de la
  primera sesión (solo lo puede hacer Jhoan).
