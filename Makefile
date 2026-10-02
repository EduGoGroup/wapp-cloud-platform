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

GO_VERSION   := 1.26.5
LINT_VERSION := v2.12.2
GO           := GOWORK=off go

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

.PHONY: fmt-check vet vet-pendiente vet-integracion test-pendiente test-procesos cobertura-ficheros lint build test test-integration ci-local ci-docker migrate migrate-status

fmt-check: ## gofmt -l vacío (sin archivos sin formatear)
	@unformatted=$$(gofmt -l .); \
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
# arranque copiado (internal/arranque, D-F0-1) queda fuera. ⚠️ `internal/arranque/huellatest`
# sigue en la lista pero HOY NO SE EVALÚA: desde D-F1-6 (`776d6a2`) los paquetes cuyo nombre
# acaba en `test` están exentos de la cobertura por fichero, y `huellatest` cae en esa
# condición (FICHEROS_EVALUADOS pasó de 10 a 9). Sus tests sí corren aquí; lo que no hay es
# umbral sobre `huellatest.go`. Qué hacer con él (renombrarlo, acotar la exención) está
# pendiente de decisión. Esta lista es la ÚNICA: el comando la recibe por -dirs y no tiene otra.
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

lint: ## golangci-lint $(LINT_VERSION) — falla si el binario del PATH es otra versión (decisión T-1)
	@v=$$(golangci-lint version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1); \
	if [ "v$$v" != "$(LINT_VERSION)" ]; then \
		echo "lint: golangci-lint $${v:-ausente} no es la versión fijada $(LINT_VERSION) (LINT_VERSION del Makefile)"; \
		exit 1; \
	fi
	GOWORK=off golangci-lint run --timeout=5m

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

ci-docker: ## Simula el CI en Docker (Go $(GO_VERSION) + golangci-lint $(LINT_VERSION)) — requiere Docker
	@docker run --rm \
		-e GOFLAGS=-buildvcs=false \
		-v "$$(go env GOPATH)/pkg/mod:/go/pkg/mod" \
		-v "$(CURDIR):/workspace" -w /workspace \
		golang:$(GO_VERSION)-bookworm \
		bash -c "set -e; curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b /usr/local/bin $(LINT_VERSION) && make ci-local"
	@echo "NOTA: ci-docker no corre test-integration (requeriría Docker-in-Docker); ejecuta 'make test-integration' aparte en el host."
