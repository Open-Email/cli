# openemail CLI — developer targets.
BINARY      := openemail
PKG         := github.com/openemail/openemail-cli
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X $(PKG)/internal/cli.Version=$(VERSION)

# Local dev core (wrangler dev) + seed keys, for the integration target.
OPENEMAIL_API_URL ?= http://localhost:8787
export OPENEMAIL_API_URL

.PHONY: build install test vet fmt lint tidy clean snapshot integration live completions help

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
