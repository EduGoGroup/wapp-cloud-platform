# Makefile — wapp-cloud-platform
#
# Régimen CI/CD nuevo (decisión del dueño, 2026-08-01): GitHub Actions ya NO
# se dispara con push/PR (ci.yml quedó en workflow_dispatch) — sirve para
# corridas manuales y de base para releases futuros. La red real es este
# Makefile: valida en LOCAL antes de mergear y pushear.
#   - ci-local        espeja los jobs "test" + "lint" del ci.yml, más los gates de la
#                     reconstrucción modular (vet-pendiente, vet-integracion, cobertura-ficheros).
#   - test-integration espeja el job "integration" (Postgres efímero).
#   - test-procesos   procesos de negocio (F9) contra los dos binarios, con testcontainers.
#   - ci-docker       reproduce el toolchain exacto del CI (imagen golang).
#   - tools           deja el golangci-lint fijado en .bin/ (una vez por checkout: .bin/ vive en el repo, y un worktree nuevo no lo trae).
#   - toolchain       dice qué Go, gofmt y golangci-lint corren DE VERDAD bajo este Makefile.

GO_VERSION   := 1.26.5
LINT_VERSION := v2.12.2
GO           := GOWORK=off go

# ── La toolchain fijada se usa SOLA (decisión de Jhoan, 2026-10-02) ───────────
# No hay dos versiones válidas: todo lo que lanza este Makefile corre con go$(GO_VERSION) y
# golangci-lint $(LINT_VERSION), en la sesión web y en la local, sin exportar nada a mano.
#
# Go: GOTOOLCHAIN es el mecanismo NATIVO de Go (≥ 1.21) para esto. Con `GOTOOLCHAIN=goX.Y.Z`
# un `go` del PATH de OTRA versión (el 1.27 de Homebrew) no compila él: ejecuta la toolchain
# pedida, que descarga UNA vez a la caché de módulos (golang.org/toolchain@…) verificándola
# contra sum.golang.org; si el `go` del PATH ya es esa versión, no hace nada. `export` la pasa
# a todo subproceso: los `$(MAKE)` anidados, el `go build` que lanza el arnés de test/procesos
# y el `go` que invoca golangci-lint para cargar los paquetes. Se asigna aquí y no con `?=`:
# un GOTOOLCHAIN heredado del entorno (`auto`, `local`) NO la cambia; solo la línea de comandos
# (`make GO_VERSION=…`). En ci-docker es inocuo: la imagen ya es golang:$(GO_VERSION).
# ⚠️ No lleva `+auto`: si go.mod pide un Go más nuevo que GO_VERSION, `go` FALLA y lo dice, en
# vez de subir de versión por su cuenta. Subir de Go es cambiar GO_VERSION (y go.mod) a la vez.
# ⚠️ `gofmt` NO obedece a GOTOOLCHAIN (es un binario aparte, no pasa por `go`): fmt-check usa
# el de la toolchain fijada (el `bin/gofmt` del GOROOT que da `go env GOROOT`), nunca el del PATH.
# Lo que hay de verdad se ve con `make toolchain`.
export GOTOOLCHAIN := go$(GO_VERSION)

# golangci-lint: `make tools` deja el binario OFICIAL de la release, verificado por sha256, en
# $(TOOLS_DIR)/ (ignorado por git), fuera del alcance de `brew upgrade`. `lint` usa ese si
# existe, ejecuta aquí y es la versión fijada; si no, el del PATH (así llega en la sesión web
# y en ci-docker, donde un .bin/ de macOS montado en el contenedor Linux no ejecuta y se
# descarta solo). Si ninguno es la versión fijada, `lint` falla (decisión T-1: otra versión da
# otro resultado). LINT_BIN=<ruta> fuerza un binario concreto, que también debe ser la fijada.
# TOOLS_DIR, LINT_BIN y LINT_RELEASE_URL son `:=` a propósito: se cambian SOLO en la línea de
# comandos (`make lint LINT_BIN=…`), nunca por una variable que casualmente esté en el entorno.
TOOLS_DIR        := $(CURDIR)/.bin
LINT_LOCAL       := $(TOOLS_DIR)/golangci-lint
LINT_BIN         :=
LINT_RELEASE_URL := https://github.com/golangci/golangci-lint/releases/download

# Fragmento de shell: versión `X.Y.Z` que dice un binario de golangci-lint; vacío si no existe
# o no ejecuta en este SO/arquitectura.
LINT_VERSION_OF = lint_version_of() { "$$1" version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1; }

# Fragmento de shell, la ÚNICA regla de elección (la usan `lint` y `toolchain`). Deja en
# variables de shell:
#   lint_bin   la ruta del golangci-lint elegido (vacío si no hay ninguno)
#   lint_ver   su versión con `v` (vacío si no hay o no ejecuta)
#   lint_seen  qué miró y qué encontró, para el mensaje de error
#   lint_fix   cómo se arregla
RESOLVE_LINT = $(LINT_VERSION_OF); lint_bin=""; lint_ver=""; \
	if [ -n "$(LINT_BIN)" ]; then \
		lint_bin="$(LINT_BIN)"; lint_ver=$$(lint_version_of "$$lint_bin"); \
		lint_seen="LINT_BIN=$$lint_bin: $${lint_ver:+v}$${lint_ver:-ausente o no ejecuta}"; \
		lint_fix="apunta LINT_BIN a un golangci-lint $(LINT_VERSION), o quítalo y corre 'make tools'"; \
	else \
		lint_fix="se arregla con 'make tools' (deja el fijado en $(TOOLS_DIR)/)"; \
		if [ -e "$(LINT_LOCAL)" ]; then \
			lint_ver=$$(lint_version_of "$(LINT_LOCAL)"); \
			lint_seen="$(LINT_LOCAL): $${lint_ver:+v}$${lint_ver:-no ejecuta en este SO/arquitectura}"; \
		else \
			lint_seen="$(LINT_LOCAL): ausente"; \
		fi; \
		if [ "v$$lint_ver" = "$(LINT_VERSION)" ]; then \
			lint_bin="$(LINT_LOCAL)"; \
		else \
			lint_bin=$$(command -v golangci-lint 2>/dev/null || true); lint_ver=""; \
			if [ -n "$$lint_bin" ]; then \
				lint_ver=$$(lint_version_of "$$lint_bin"); \
				lint_seen="$$lint_seen · PATH $$lint_bin: $${lint_ver:+v}$${lint_ver:-no ejecuta}"; \
			else \
				lint_seen="$$lint_seen · PATH: ausente"; \
			fi; \
		fi; \
	fi; \
	if [ -n "$$lint_ver" ]; then lint_ver="v$$lint_ver"; fi

# Postgres efímero para integración — mismo usuario/contraseña/BD/puerto que
# el service container de ci.yml. Se levanta y se destruye en el propio
# target: nunca depende de un contenedor de otro proyecto ya corriendo.
#
# El puerto es sobrescribible (`?=`) porque el 5432 del host suele estar ocupado
# por otro Postgres —en la máquina de desarrollo, el compartido con EduGo— y
# entonces el `docker run` falla por entorno, no por el código:
#   INTEGRATION_PG_PORT=55441 make test-integration
INTEGRATION_PG_CONTAINER := wapp-cloud-platform-pg-test
INTEGRATION_PG_PORT      ?= 5432
INTEGRATION_PG_USER      := wapp
INTEGRATION_PG_PASSWORD  := wapp
INTEGRATION_PG_DB        := wapp_test
INTEGRATION_DSN          := postgres://$(INTEGRATION_PG_USER):$(INTEGRATION_PG_PASSWORD)@localhost:$(INTEGRATION_PG_PORT)/$(INTEGRATION_PG_DB)?sslmode=disable

.PHONY: tools toolchain fmt-check vet vet-pendiente vet-integracion test-pendiente test-procesos cobertura-ficheros lint build test test-integration ci-local ci-docker migrate migrate-status

# Idempotente: si $(LINT_LOCAL) ya es la versión fijada, no descarga nada. Si no, baja el
# tarball de la release para ESTA máquina (GOHOSTOS/GOHOSTARCH: el binario tiene que ejecutar
# aquí, aunque haya un GOOS exportado para compilar cruzado) y el fichero de checksums de la
# misma release, y compara el sha256 ANTES de extraer: si no casa, falla y no deja binario.
# No ejecuta ningún script remoto. Todo lo temporal vive en $(TOOLS_DIR)/.tmp.* y se borra al
# salir, bien o mal. 🌐 En la sesión web el proxy de GitHub solo sirve release assets de los
# repos adjuntos (06-entorno-web.md §3): allí el linter fijado ya viene en el PATH y `lint` lo
# usa; este target es para la máquina local.
tools: ## Deja golangci-lint $(LINT_VERSION) (binario oficial, sha256 verificado) en .bin/ — idempotente
	@set -e; $(LINT_VERSION_OF); \
	dir="$(TOOLS_DIR)"; bin="$(LINT_LOCAL)"; want="$(LINT_VERSION)"; num="$${want#v}"; \
	have=$$(lint_version_of "$$bin"); \
	if [ "v$$have" = "$$want" ]; then \
		echo "tools: $$bin ya es golangci-lint $$want — no se descarga nada"; exit 0; \
	fi; \
	command -v curl >/dev/null 2>&1 || { echo "tools: falta curl en el PATH"; exit 1; }; \
	if command -v sha256sum >/dev/null 2>&1; then sha="sha256sum"; \
	elif command -v shasum >/dev/null 2>&1; then sha="shasum -a 256"; \
	else echo "tools: ni sha256sum ni shasum en el PATH: sin verificar el sha256 no se instala nada"; exit 1; fi; \
	os=$$($(GO) env GOHOSTOS); arch=$$($(GO) env GOHOSTARCH); \
	name="golangci-lint-$$num-$$os-$$arch"; sums="golangci-lint-$$num-checksums.txt"; \
	url="$(LINT_RELEASE_URL)/$$want"; \
	mkdir -p "$$dir"; \
	tmp=$$(mktemp -d "$$dir/.tmp.XXXXXX"); \
	trap 'rm -rf "$$tmp"; rmdir "$$dir" 2>/dev/null || true' EXIT; trap 'exit 1' HUP INT TERM; \
	echo "tools: descargando $$url/$$name.tar.gz"; \
	curl -fsSL --retry 2 --connect-timeout 20 -o "$$tmp/$$name.tar.gz" "$$url/$$name.tar.gz" \
		|| { echo "tools: no pude descargar $$name.tar.gz (¿sin red, o el proxy de la sesión web? si el PATH ya trae el $$want, 'make lint' lo usa)"; exit 1; }; \
	curl -fsSL --retry 2 --connect-timeout 20 -o "$$tmp/$$sums" "$$url/$$sums" \
		|| { echo "tools: no pude descargar $$sums: sin checksums no se instala nada"; exit 1; }; \
	expected=$$(awk -v f="$$name.tar.gz" '$$2 == f { print $$1 }' "$$tmp/$$sums"); \
	[ -n "$$expected" ] || { echo "tools: $$sums no lista $$name.tar.gz: no se instala nada"; exit 1; }; \
	actual=$$($$sha "$$tmp/$$name.tar.gz" | awk '{ print $$1 }'); \
	if [ "$$actual" != "$$expected" ]; then \
		echo "tools: el sha256 de $$name.tar.gz NO casa con $$sums: no se instala nada"; \
		echo "tools:   esperado  $$expected"; echo "tools:   calculado $$actual"; exit 1; \
	fi; \
	echo "tools: sha256 verificado ($$sha): $$actual"; \
	tar -xzf "$$tmp/$$name.tar.gz" -C "$$tmp" "$$name/golangci-lint"; \
	chmod +x "$$tmp/$$name/golangci-lint"; \
	got=$$(lint_version_of "$$tmp/$$name/golangci-lint"); \
	[ "v$$got" = "$$want" ] || { echo "tools: el binario extraído dice '$${got:-nada}', no $$want: no se instala nada"; exit 1; }; \
	mv -f "$$tmp/$$name/golangci-lint" "$$bin"; \
	echo "tools: instalado golangci-lint $$want en $$bin"

# La verdad de campo de la toolchain, en líneas `CLAVE=valor ruta` estables y fáciles de grepear:
# qué Go hay en la máquina, cuál corre DE VERDAD bajo este Makefile, qué gofmt usa fmt-check y
# qué golangci-lint usaría `lint`. Sale ≠ 0 si el Go o el gofmt efectivos no son go$(GO_VERSION)
# o el golangci-lint elegido no es $(LINT_VERSION). El hook de SessionStart lo llama con
# GOPROXY=off, que impide a Go descargar la toolchain: verificar no es instalar.
toolchain: ## Imprime la toolchain EFECTIVA (Go, gofmt, golangci-lint); rc≠0 si no es la fijada
	@$(RESOLVE_LINT); \
	rc=0; pinned="go$(GO_VERSION)"; \
	sys_bin=$$(command -v go 2>/dev/null || true); \
	sys=$$(GOTOOLCHAIN=local go version 2>/dev/null | awk '{ print $$3 }'); \
	eff=$$($(GO) env GOVERSION) || eff=""; \
	root=""; fmt_ver=""; \
	if [ -n "$$eff" ]; then \
		root=$$($(GO) env GOROOT); \
		fmt_ver=$$($(GO) version "$$root/bin/gofmt" 2>/dev/null | awk '{ print $$NF }'); \
	fi; \
	echo "GO_PINNED=$$pinned"; \
	echo "GO_SYSTEM=$${sys:-missing} $$sys_bin"; \
	echo "GO_EFFECTIVE=$${eff:-missing} $${root:+$$root/bin/go}"; \
	echo "GOFMT_EFFECTIVE=$${fmt_ver:-missing} $${root:+$$root/bin/gofmt}"; \
	echo "LINT_PINNED=$(LINT_VERSION)"; \
	echo "LINT_EFFECTIVE=$${lint_ver:-missing} $$lint_bin"; \
	if [ "$$eff" != "$$pinned" ]; then rc=1; \
		echo "toolchain: el Go efectivo es '$${eff:-ninguno}', no $$pinned (GOTOOLCHAIN=$$GOTOOLCHAIN): si no está en la caché de Go, la primera vez hace falta red para bajarlo"; \
	elif [ "$$fmt_ver" != "$$pinned" ]; then rc=1; \
		echo "toolchain: el gofmt de $$root/bin es '$${fmt_ver:-ausente}', no $$pinned"; \
	fi; \
	if [ "$$lint_ver" != "$(LINT_VERSION)" ]; then rc=1; \
		echo "toolchain: ningún golangci-lint es $(LINT_VERSION) — $$lint_seen"; \
		echo "toolchain: $$lint_fix"; \
	fi; \
	if [ $$rc -eq 0 ]; then echo "TOOLCHAIN=OK"; else echo "TOOLCHAIN=NOT_READY"; fi; \
	exit $$rc

fmt-check: ## gofmt -l vacío (sin archivos sin formatear) — con el gofmt de go$(GO_VERSION), no el del PATH
	@gofmt="$$($(GO) env GOROOT)/bin/gofmt"; \
	if [ ! -x "$$gofmt" ]; then \
		echo "fmt-check: no hay gofmt de go$(GO_VERSION) en '$$gofmt' (mira 'make toolchain')"; exit 1; \
	fi; \
	unformatted=$$("$$gofmt" -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "Archivos sin gofmt:"; echo "$$unformatted"; exit 1; \
	fi

vet: ## go vet ./...
	$(GO) vet ./...

# ── Etiqueta `pendiente` (reconstrucción modular, 05 E-2/E-5 · F0 T0.4) ───────
# Un contrato sin lógica tiene cuerpo `panic(pendiente.Implementar("paq.Símbolo"))`
# y su test nace con `//go:build pendiente`: fuera de `vet`, `lint` y `test`.
# vet-pendiente los compila (un rojo que no compila rompe ci-local); test-pendiente
# cuenta lo que falta. Alcance del rojo que se ejecuta: los directorios que aún no
# existen se omiten (el target no falla por un módulo que todavía no nació).
# Los dos contadores ignoran `testdata/`, como la toolchain de Go: los árboles de
# prueba de internal/candados imitan rojos a propósito (F0 T0.5).
PENDIENTE_DIRS := internal/modulos internal/nucleo internal/arranque internal/apipublica

vet-pendiente: ## go vet -tags pendiente ./... — los rojos también compilan
	$(GO) vet -tags pendiente ./...

test-pendiente: ## Informa, no juzga: PENDIENTES, ROJOS, corre los rojos y vet-pendiente (rc = el de vet-pendiente)
	@pendientes=$$(grep -rn --include='*.go' --exclude='*_test.go' --exclude-dir=.git --exclude-dir=testdata 'pendiente\.Implementar(' . \
		| grep -v '^\./internal/pendiente/' \
		| sed -E 's/^[^:]+:[0-9]+://' \
		| grep -vE '^[[:space:]]*//' \
		| grep -o 'pendiente\.Implementar(' | wc -l); \
	echo "PENDIENTES=$$pendientes"
	@rojos=$$(find . \( -path ./.git -o -name testdata \) -prune -o -name '*_test.go' -type f -print \
		| xargs -r awk 'FNR==1 { if ($$0 == "//go:build pendiente") n++; nextfile } END { print n+0 }'); \
	echo "ROJOS=$$rojos"
	@pats=""; for d in $(PENDIENTE_DIRS); do [ -d "$$d" ] && pats="$$pats ./$$d/..."; done; \
	paqs=$$( [ -n "$$pats" ] && $(GO) list $$pats 2>/dev/null ); \
	if [ -z "$$paqs" ]; then \
		echo "test-pendiente: aún no existe ningún paquete en $(PENDIENTE_DIRS)"; \
	else \
		$(GO) test -tags pendiente $$paqs || echo "test-pendiente: hay rojos que fallan (esperado; manda la cifra estática)"; \
	fi
	@$(MAKE) --no-print-directory vet-pendiente

# ── Etiqueta `integracion` (procesos de negocio, F9 · T9.4) ───────────────────
# Los tests de test/procesos/ nacen con `//go:build integracion`: fuera de `vet`, `lint`
# y `test` salvo que se pida la etiqueta. vet-integracion los compila (un proceso que no
# compila rompe ci-local sin necesitar Docker); .golangci.yml lleva la misma etiqueta
# en run.build-tags para que el linter también los vea. Correrlos es test-procesos.
vet-integracion: ## go vet -tags integracion ./test/procesos/... — los procesos también compilan (sin Docker)
	$(GO) vet -tags integracion ./test/procesos/...

# ── Cobertura por fichero (reconstrucción modular, 05 §5 · D-12 · F0 T0.9) ────
# Cada fichero de producción EN VERDE del alcance exige ≥ 80 % de sentencias cubiertas;
# los contratos en rojo (con pendiente.Implementar) no se miden y los adaptadores Postgres
# marcados en su cabecera quedan exentos (05 E-6). La lógica vive en internal/candados; el
# comando cmd/cobertura-ficheros la cablea. Alcance: el árbol NUEVO (diseno.md §3/§4); el
# arranque copiado (internal/arranque, D-F0-1) queda fuera salvo su huellatest, que SÍ se
# evalúa: los paquetes exentos son solo los de suite de contrato y dobles, los que terminan en
# el sufijo compuesto `helpertest` (D-F1-10, que estrecha D-F1-6: p. ej. contacthelpertest), y
# `huellatest` no termina así. Entre D-F1-6 (`776d6a2`) y D-F1-10 la exención era por `test`
# a secas, `huellatest` quedó sin medir y FICHEROS_EVALUADOS bajó de 10 a 9; vuelve a ser 10.
# Esta lista es la ÚNICA: el comando la recibe por -dirs y no tiene otra.
# Los directorios que aún no existen se filtran con `[ -d ]` ANTES de `go list`: con un solo
# patrón inexistente `go list` falla y no lista ninguno (contradicción 13 del README de F0).
COBERTURA_DIRS := internal/modulos internal/nucleo internal/apipublica internal/pendiente internal/candados internal/arranque/huellatest

cobertura-ficheros: ## Cobertura ≥ 80 % por fichero en verde del árbol nuevo (D-12): FICHEROS_EVALUADOS, POR_DEBAJO, EXENTOS_POSTGRES
	@dirs=""; pats=""; \
	for d in $(COBERTURA_DIRS); do \
		if [ -d "$$d" ]; then dirs="$${dirs:+$$dirs,}$$d"; pats="$$pats ./$$d/..."; fi; \
	done; \
	if [ -z "$$pats" ]; then \
		echo "cobertura-ficheros: aún no existe ningún directorio de $(COBERTURA_DIRS)"; exit 0; \
	fi; \
	paqs=$$($(GO) list $$pats) || exit 2; \
	if [ -z "$$paqs" ]; then \
		echo "cobertura-ficheros: ningún paquete en$$pats"; exit 0; \
	fi; \
	perfil=$$(mktemp "$${TMPDIR:-/tmp}/cobertura-ficheros.XXXXXX") || exit 2; \
	trap 'rm -f "$$perfil"' EXIT; \
	$(GO) test -covermode=set -coverprofile="$$perfil" $$paqs || exit $$?; \
	$(GO) run ./cmd/cobertura-ficheros -perfil "$$perfil" -umbral 80 -dirs "$$dirs"

lint: ## golangci-lint $(LINT_VERSION): el de .bin/ (make tools) o el del PATH — falla si ninguno es la versión fijada (decisión T-1)
	@$(RESOLVE_LINT); \
	if [ "$$lint_ver" != "$(LINT_VERSION)" ]; then \
		echo "lint: ningún golangci-lint es la versión fijada $(LINT_VERSION) (LINT_VERSION del Makefile) — $$lint_seen"; \
		echo "lint: $$lint_fix"; \
		exit 1; \
	fi; \
	echo "GOWORK=off $$lint_bin run --timeout=5m"; \
	GOWORK=off "$$lint_bin" run --timeout=5m

build: ## go build ./...
	$(GO) build ./...

test: ## Tests unitarios -race (los *_integration_test.go se saltan solos sin WAPP_TEST_DB_DSN)
	$(GO) test -race ./...

test-integration: ## Tests de integración con Postgres efímero en Docker — espejo del job "integration" del ci.yml
	@docker rm -f $(INTEGRATION_PG_CONTAINER) >/dev/null 2>&1 || true
	@docker run -d --name $(INTEGRATION_PG_CONTAINER) \
		-e POSTGRES_USER=$(INTEGRATION_PG_USER) \
		-e POSTGRES_PASSWORD=$(INTEGRATION_PG_PASSWORD) \
		-e POSTGRES_DB=$(INTEGRATION_PG_DB) \
		-p $(INTEGRATION_PG_PORT):5432 \
		postgres:16 >/dev/null
	@echo "Esperando Postgres efímero ($(INTEGRATION_PG_CONTAINER))..."
	@for i in $$(seq 1 30); do \
		docker exec $(INTEGRATION_PG_CONTAINER) pg_isready -U $(INTEGRATION_PG_USER) -d $(INTEGRATION_PG_DB) >/dev/null 2>&1 && break; \
		sleep 1; \
	done
	@WAPP_TEST_DB_DSN="$(INTEGRATION_DSN)" WAPP_TEST_REQUIRE_DB=1 $(GO) test -p 1 ./...; status=$$?; \
	docker rm -f $(INTEGRATION_PG_CONTAINER) >/dev/null 2>&1; \
	exit $$status

# ── Procesos de negocio (F9 · T9.4) ───────────────────────────────────────────
# Cada proceso corre contra un Postgres de testcontainers (un contenedor por corrida, una
# base clonada por proceso), así que NECESITA Docker: sin él, falla (no se salta). Se corre
# contra los DOS binarios, `viejo` (cmd/server) y `nuevo` (cmd/server-modular), cada uno con
# su log. La sesión web lo corre solo como pre-chequeo; la que cierra es la sesión local.
#   BINARIO=viejo|nuevo  acota a un binario (por defecto, los dos)
#   CUENTA=3             -count (por defecto 1)
#   PROCESOS_LOG_DIR     dónde dejar los logs (por defecto /tmp → /tmp/procesos-<binario>.log)
# El rc de `go test` se escribe como última línea `RC=<n>` del log ANTES de pasar al
# siguiente binario: nunca se lee detrás de una tubería (la tubería devolvería el rc de grep).
# Las tres cuentas (PASS, FAIL, SKIP) se sacan del log UNA vez por binario, con `grep -c` sin
# tubería, y son las que imprime la línea de resumen.
# El target sale ≠ 0 si en cualquiera de los binarios `go test` salió ≠ 0 **o hubo algún
# `--- SKIP`**. Un SKIP NO es verde y aquí no se deja a quien lee: `go test` da rc=0 con un
# proceso saltado, así que es el target el que lo pone en rojo y lo dice en una línea propia
# (E-5: un proceso que no puede correr falla, no se salta). La etiqueta `integracion` es
# obligatoria: sin ella el paquete solo tiene el candado y `go test` da `ok` sin ejecutar
# ningún proceso (T-15).
PROCESOS_LOG_DIR ?= /tmp

test-procesos: ## Procesos (F9) contra los DOS binarios, testcontainers; necesita Docker (BINARIO=viejo|nuevo, CUENTA=n). Rojo si rc≠0 o SKIP>0
	@fallo=0; for b in $${BINARIO:-viejo nuevo}; do \
		L=$(PROCESOS_LOG_DIR)/procesos-$$b.log; \
		WAPP_PROCESOS_BINARIO=$$b $(GO) test -tags integracion -count=$${CUENTA:-1} -v -timeout 30m -parallel 4 ./test/procesos/... > $$L 2>&1; \
		rc=$$?; echo "RC=$$rc" >> $$L; [ $$rc -eq 0 ] || fallo=1; \
		pass=$$(grep -c -- '--- PASS' $$L); fail=$$(grep -c -- '--- FAIL' $$L); skip=$$(grep -c -- '--- SKIP' $$L); \
		echo "$$b: RC=$$rc · PASS=$$pass FAIL=$$fail SKIP=$$skip · $$L"; \
		if [ "$${skip:-0}" != "0" ]; then \
			fallo=1; echo "$$b: ROJO por SKIP=$$skip — un proceso que no puede correr falla, no se salta (E-5)"; \
		fi; \
	done; exit $$fallo

ci-local: fmt-check vet vet-pendiente vet-integracion lint test cobertura-ficheros build ## Pre-push: fmt + vet + vet-pendiente + vet-integracion + lint + test + cobertura-ficheros + build (sin integración: correr test-integration y test-procesos aparte)

# ── Esquema ───────────────────────────────────────────────────────────────────
# cmd/migrate aplica el DDL y SALE: sin listeners HTTP/gRPC ni plano de control
# del Edge. La conexión sale de las MISMAS variables que el servidor (WAPP_DB_*),
# nunca de un argumento, para que no haya dos formas de decir a qué base se apunta.
#
# ⚠️ Contra Neon: apunta al host DIRECTO, NUNCA al `-pooler`. El runner serializa
# con pg_advisory_lock sobre una conexión dedicada y un pooler en modo transacción
# puede repartir el lock y el unlock en sesiones distintas.

migrate: ## Aplica las migraciones de esquema y sale (lee WAPP_DB_*)
	$(GO) run ./cmd/migrate

migrate-status: ## Consulta la versión/hash del esquema SIN escribir nada
	$(GO) run ./cmd/migrate -status

# La caché de módulos se monta desde `go env GOMODCACHE`, no desde `GOPATH/pkg/mod`: son lo
# mismo por defecto, pero una máquina con GOMODCACHE propio (la de desarrollo lo tiene en otro
# volumen) montaría un directorio vacío y el contenedor no encontraría los módulos privados.
# El linter lo instala `make tools` DENTRO del contenedor, en /usr/local/bin (el mismo binario
# oficial verificado por sha256 que en local; antes era `curl … install.sh | sh` desde HEAD).
# El .bin/ del host viaja montado, pero es de otro SO: `lint` lo descarta solo y usa el del PATH.
ci-docker: ## Simula el CI en Docker (Go $(GO_VERSION) + golangci-lint $(LINT_VERSION)) — requiere Docker
	@docker run --rm \
		-e GOFLAGS=-buildvcs=false \
		-v "$$(go env GOMODCACHE):/go/pkg/mod" \
		-v "$(CURDIR):/workspace" -w /workspace \
		golang:$(GO_VERSION)-bookworm \
		bash -c "set -e; make tools TOOLS_DIR=/usr/local/bin && make ci-local"
	@echo "NOTA: ci-docker no corre test-integration (requeriría Docker-in-Docker); ejecuta 'make test-integration' aparte en el host."
