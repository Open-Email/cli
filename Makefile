# openemail CLI — developer targets.
BINARY      := openemail
PKG         := github.com/Open-Email/cli
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X $(PKG)/internal/cli.Version=$(VERSION)

# Local dev core (wrangler dev) + seed keys, for the integration target.
OPENEMAIL_API_URL ?= http://localhost:8787
export OPENEMAIL_API_URL

# Sibling core checkout, source of the vendored OpenAPI snapshot.
CORE_DIR ?= ../openemail-core

.PHONY: build install test vet fmt lint tidy clean snapshot sync-spec integration live completions help

build: ## Build the binary into ./bin
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/openemail

install: ## go install the binary
	go install -ldflags "$(LDFLAGS)" ./cmd/openemail

test: ## Run the fast unit tests
	go test -race ./...

vet: ## go vet
	go vet ./...

fmt: ## Check formatting (fails if any file is unformatted)
	@test -z "$$(gofmt -l .)" || (echo "unformatted:"; gofmt -l .; exit 1)

lint: fmt vet ## fmt + vet

tidy: ## go mod tidy
	go mod tidy

clean: ## Remove build artifacts
	rm -rf bin completions dist

snapshot: ## Dry-run a full goreleaser build (needs goreleaser installed)
	goreleaser release --snapshot --clean

release: ## Cut a release: make release VERSION=v0.2.1 [MESSAGE="..."]
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=vX.Y.Z [MESSAGE=\"...\"]"; exit 1; }
	./scripts/release.sh $(VERSION) $(if $(MESSAGE),-m "$(MESSAGE)")

sync-spec: ## Refresh the vendored openapi.snapshot.json from core ($(CORE_DIR); run `npm run spec` there first)
	@test -f $(CORE_DIR)/openapi.snapshot.json || { echo "not found: $(CORE_DIR)/openapi.snapshot.json (set CORE_DIR or run 'npm run spec' in core)"; exit 1; }
	cp $(CORE_DIR)/openapi.snapshot.json ./openapi.snapshot.json
	@echo "vendored openapi.snapshot.json from $(CORE_DIR)"

completions: build ## Generate shell completions into ./completions
	@mkdir -p completions
	@for sh in bash zsh fish; do ./bin/$(BINARY) completion $$sh > completions/$(BINARY).$$sh; done
	@echo "wrote completions/$(BINARY).{bash,zsh,fish}"

integration: build ## Run the integration suite against local wrangler dev (must be running + seeded)
	go test -tags integration -race ./test/...

live: ## Run the live e2e suite against a DEPLOYED core (needs OE_HOST + OE_SYSTEM_KEY; see test/live/README.md)
	go test -tags live -count=1 -timeout 20m ./test/live/...

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS=":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
