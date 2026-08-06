# Makefile — wapp-cloud-platform
#
# Régimen CI/CD nuevo (decisión del dueño, 2026-08-01): GitHub Actions ya NO
# se dispara con push/PR (ci.yml quedó en workflow_dispatch) — sirve para
# corridas manuales y de base para releases futuros. La red real es este
# Makefile: valida en LOCAL antes de mergear y pushear.
#   - ci-local        espeja los jobs "test" + "lint" del ci.yml.
#   - test-integration espeja el job "integration" (Postgres efímero).
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

.PHONY: fmt-check vet lint build test test-integration ci-local ci-docker migrate migrate-status

fmt-check: ## gofmt -l vacío (sin archivos sin formatear)
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "Archivos sin gofmt:"; echo "$$unformatted"; exit 1; \
	fi

vet: ## go vet ./...
	$(GO) vet ./...

lint: ## golangci-lint $(LINT_VERSION) (binario fijado — no el de ~/go/bin)
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

ci-local: fmt-check vet lint test build ## Pre-push: fmt + vet + lint + test + build (sin integración: correr test-integration aparte)

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
