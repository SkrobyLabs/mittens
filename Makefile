# mittens – Makefile
# Run Claude Code in isolated Docker containers with credential forwarding,
# firewall, DinD, and pluggable extensions.

BINARY   := mittens
MODULE   := github.com/SkrobyLabs/mittens

# ─── Windows detection ───────────────────────────────────────────────────────
# On Windows (detected via the OS env var), all Go commands run inside WSL.
# The build produces mittens.exe (a thin shim) + mittens-linux (the real binary).

ifeq ($(OS),Windows_NT)
  GO     := wsl.exe go
else
  GO     := go
endif

# Version info injected via -ldflags (override with: make build VERSION=v1.2.3)
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X '$(MODULE)/cmd/mittens.version=$(VERSION)' \
	-X '$(MODULE)/cmd/mittens.commit=$(COMMIT)' \
	-X '$(MODULE)/cmd/mittens.date=$(DATE)'

# Disable Go's automatic VCS stamping — version info is already injected via
# LDFLAGS above, and the automatic stamping can fail in some environments
# (worktrees, detached HEAD, cross-compilation).
export GOFLAGS := -buildvcs=false

# Install destination: ~/.local on Linux (no sudo), /usr/local on macOS
UNAME_S  := $(shell uname -s)
ifeq ($(UNAME_S),Linux)
PREFIX   ?= $(HOME)/.local
else
PREFIX   ?= /usr/local
endif

# Docker image
IMAGE    := mittens
TAG      ?= latest

# ─── Default ──────────────────────────────────────────────────────────────────

.DEFAULT_GOAL := help

all: build ## Build the binary

# ─── Build ────────────────────────────────────────────────────────────────────

INIT_BINARY := mittens-init

ifeq ($(OS),Windows_NT)
# On Windows: build both the Linux binary and a .exe shim via WSL.
#   mittens.exe       - Windows shim (run this from PowerShell/cmd)
#   mittens-linux     - real binary (executed inside WSL by the shim)
build: tidy init-binary # (internal) Build mittens for Windows (Linux binary + WSL shim + clipboard helper)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux ./cmd/mittens
	wsl.exe env GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY).exe ./cmd/shim
	-@powershell -NoProfile -c "Stop-Process -Name '$(BINARY)-clipboard-helper' -Force -EA 0; sleep 1"
	wsl.exe env GOOS=windows GOARCH=amd64 go build -o $(BINARY)-clipboard-helper.exe ./cmd/mittens-clipboard-helper
	@echo "Built $(BINARY).exe (WSL shim) + $(BINARY)-linux + $(BINARY)-clipboard-helper.exe"
	@echo "Run mittens.exe - it transparently uses WSL under the hood."
else
build: tidy init-binary team-mcp-binary ## Build the mittens binary
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/mittens
	@echo "Built ./$(BINARY) - run 'make help' to see all targets"
endif

# Container-side init binary (always linux, matches container arch).
# Cross-compiled as a static binary so it works in any container base image.
init-binary: ## Build the container-side mittens-init binary
	CGO_ENABLED=0 GOOS=linux $(GO) build -ldflags "-s -w" -o cmd/mittens/container/$(INIT_BINARY) ./cmd/mittens-init
	@echo "Built cmd/mittens/container/$(INIT_BINARY)"

# Container-side team-mcp binary (MCP server for team mode leaders).
team-mcp-binary: ## Build the container-side team-mcp binary
	CGO_ENABLED=0 GOOS=linux $(GO) build -ldflags "-s -w" -o cmd/mittens/container/team-mcp ./cmd/team-mcp
	@echo "Built cmd/mittens/container/team-mcp"

install: build ## Symlink binary into PREFIX/bin (default: /usr/local/bin)
	install -d $(PREFIX)/bin
	ln -sf $(CURDIR)/$(BINARY) $(PREFIX)/bin/$(BINARY)

# ─── Setup ────────────────────────────────────────────────────────────────────

ifeq ($(OS),Windows_NT)
init: init-windows # Set up development environment

init-windows: # (internal) Set up Windows development environment (WSL + Go + Docker)
	@echo "Checking Windows development prerequisites..."
	@echo ""
	@echo "1. WSL"
	@wsl.exe echo ok >/dev/null 2>&1 && echo "   [OK] WSL is running" \
		|| (echo "   [MISSING] WSL is required - install with: wsl --install" && exit 1)
	@echo ""
	@echo "2. Go (in WSL)"
	@wsl.exe which go >/dev/null 2>&1 && echo "   [OK] $$(wsl.exe go version)" \
		|| (echo "   [MISSING] Go is not installed in WSL - run: wsl sudo apt install golang-go" && exit 1)
	@echo ""
	@echo "3. Docker"
	@wsl.exe which docker >/dev/null 2>&1 && echo "   [OK] docker found" \
		|| (echo "   [MISSING] Docker Desktop is required - https://docs.docker.com/desktop/install/windows-install/" && exit 1)
	@wsl.exe docker info >/dev/null 2>&1 && echo "   [OK] Docker daemon is running" \
		|| echo "   [WARNING] Docker daemon is not running - start Docker Desktop"
	@echo ""
	@echo "4. Dependencies"
	@wsl.exe go mod tidy
	@echo "   [OK] go mod tidy complete"
	@echo ""
	@echo "All checks passed. Run 'make build' to build mittens."
else
init: ## Set up development environment
	@echo "Checking development prerequisites..."
	@echo ""
	@echo "1. Go"
	@command -v go >/dev/null 2>&1 && echo "   [OK] $$(go version)" \
		|| (echo "   [MISSING] Go 1.23+ is required - https://go.dev/dl/" && exit 1)
	@echo ""
	@echo "2. Docker"
	@command -v docker >/dev/null 2>&1 && echo "   [OK] docker found" \
		|| (echo "   [MISSING] Docker is required - https://docs.docker.com/engine/install/" && exit 1)
	@docker info >/dev/null 2>&1 && echo "   [OK] Docker daemon is running" \
		|| echo "   [WARNING] Docker daemon is not running"
	@echo ""
	@echo "3. Dependencies"
	@go mod tidy
	@echo "   [OK] go mod tidy complete"
	@echo ""
	@echo "All checks passed. Run 'make build' to build mittens."
endif

# ─── Dependencies ─────────────────────────────────────────────────────────────

tidy: ## Run go mod tidy
	$(GO) mod tidy

# ─── Docker ───────────────────────────────────────────────────────────────────

docker: ## Build the Docker base image (no extensions)
	docker build -f cmd/mittens/container/Dockerfile -t $(IMAGE):$(TAG) cmd/mittens

# ─── Quality ──────────────────────────────────────────────────────────────────

test: ## Run all tests
	$(GO) test ./...

test-v: ## Run tests with verbose output
	$(GO) test -v ./...

test-race: ## Run tests with race detector
	$(GO) test -race ./...

lint: ## Run golangci-lint (install: https://golangci-lint.run/welcome/install)
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found – install from https://golangci-lint.run"; exit 1; }
	golangci-lint run ./...

fmt: ## Format Go source files
	$(GO) fmt ./...

vet: ## Run go vet
	$(GO) vet ./...

check: fmt vet lint test ## Run fmt, vet, lint, and test

# ─── Release ──────────────────────────────────────────────────────────────────

DIST := dist

release: tidy ## Cross-compile for common platforms into dist/
	@mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64  ./cmd/mittens
	GOOS=darwin  GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64  ./cmd/mittens
	GOOS=linux   GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64   ./cmd/mittens
	GOOS=linux   GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64   ./cmd/mittens
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe ./cmd/shim
	GOOS=windows GOARCH=amd64 $(GO) build -o $(DIST)/$(BINARY)-clipboard-helper-windows-amd64.exe ./cmd/mittens-clipboard-helper
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -ldflags "-s -w" -o $(DIST)/$(INIT_BINARY)-linux-amd64 ./cmd/mittens-init
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags "-s -w" -o $(DIST)/$(INIT_BINARY)-linux-arm64 ./cmd/mittens-init

# ─── Distribution ────────────────────────────────────────────────────────────

dist: build ## Build a self-contained dist/ folder with all runtime files
	@rm -rf $(DIST)
	@mkdir -p $(DIST)/container $(DIST)/extensions
	@# Binaries
	cp $(BINARY) $(DIST)/
	@# Container runtime files (Dockerfile, entrypoint, configs, scripts)
	cp cmd/mittens/container/* $(DIST)/container/
	@# Extension build scripts (YAML manifests are embedded in the binary)
	@for ext in cmd/mittens/extensions/*/; do \
		name=$$(basename "$$ext"); \
		[ "$$name" = "registry" ] && continue; \
		if ls "$$ext"build.sh >/dev/null 2>&1; then \
			mkdir -p "$(DIST)/extensions/$$name"; \
			cp "$$ext"build.sh "$(DIST)/extensions/$$name/"; \
		fi; \
	done
	@echo ""
	@echo "Distribution ready in $(DIST)/"
	@echo "  $(DIST)/mittens        - CLI"
	@echo "  $(DIST)/container/     - Docker image files"
	@echo "  $(DIST)/extensions/    - Extension build scripts"

# ─── Clean ────────────────────────────────────────────────────────────────────

clean: ## Remove build artifacts
	rm -f $(BINARY) $(BINARY).exe $(BINARY)-linux $(BINARY)-clipboard-helper.exe
	rm -f cmd/mittens/container/$(INIT_BINARY)
	rm -rf $(DIST)
	$(GO) clean

# ─── Dev helpers ──────────────────────────────────────────────────────────────

run: build ## Build and run with ARGS (e.g. make run ARGS="--firewall .")
	./$(BINARY) $(ARGS)

# ─── Help ─────────────────────────────────────────────────────────────────────

help: ## Show this help
	@printf '\nUsage: make \033[36m<target>\033[0m\n\n'
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo

.PHONY: all build init init-windows install tidy docker test test-v test-race lint fmt vet check release dist clean run help init-binary team-mcp-binary
