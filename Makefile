# mittens – Makefile
# Run Claude Code in isolated Docker containers with credential forwarding,
# firewall, DinD, and pluggable extensions.

BINARY   := mittens
MODULE   := github.com/Skroby/mittens

# Version info injected via -ldflags (override with: make build VERSION=v1.2.3)
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X '$(MODULE).version=$(VERSION)' \
	-X '$(MODULE).commit=$(COMMIT)' \
	-X '$(MODULE).date=$(DATE)'

# Install destination (go install uses GOPATH/bin by default)
PREFIX   ?= /usr/local

# Docker image
IMAGE    := mittens
TAG      ?= latest
USER_ID  := $(shell id -u)
GROUP_ID := $(shell id -g)

# ─── Default ──────────────────────────────────────────────────────────────────

.DEFAULT_GOAL := all

all: build ## Build the binary (default)

# ─── Build ────────────────────────────────────────────────────────────────────

build: tidy ## Build the mittens binary
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .
	@echo "Built ./$(BINARY) — run 'make help' to see all targets"

install: build ## Install binary to PREFIX/bin (default: /usr/local/bin)
	install -d $(PREFIX)/bin
	install -m 755 $(BINARY) $(PREFIX)/bin/$(BINARY)

# ─── Dependencies ─────────────────────────────────────────────────────────────

tidy: ## Run go mod tidy
	go mod tidy

# ─── Docker ───────────────────────────────────────────────────────────────────

docker: ## Build the Docker base image (no extensions)
	docker build -f container/Dockerfile -t $(IMAGE):$(TAG) \
		--build-arg USER_ID=$(USER_ID) \
		--build-arg GROUP_ID=$(GROUP_ID) \
		.

# ─── Quality ──────────────────────────────────────────────────────────────────

test: ## Run all tests
	go test ./...

test-v: ## Run tests with verbose output
	go test -v ./...

test-race: ## Run tests with race detector
	go test -race ./...

lint: ## Run golangci-lint (install: https://golangci-lint.run/welcome/install)
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found – install from https://golangci-lint.run"; exit 1; }
	golangci-lint run ./...

fmt: ## Format Go source files
	gofmt -s -w .

vet: ## Run go vet
	go vet ./...

check: fmt vet lint test ## Run fmt, vet, lint, and test

# ─── Release ──────────────────────────────────────────────────────────────────

DIST := dist

release: tidy ## Cross-compile for common platforms into dist/
	@mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64  .
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64  .
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64   .
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64   .

# ─── Clean ────────────────────────────────────────────────────────────────────

clean: ## Remove build artifacts
	rm -f $(BINARY)
	rm -rf $(DIST)
	go clean

# ─── Dev helpers ──────────────────────────────────────────────────────────────

run: build ## Build and run with ARGS (e.g. make run ARGS="--firewall .")
	./$(BINARY) $(ARGS)

# ─── Help ─────────────────────────────────────────────────────────────────────

help: ## Show this help
	@printf '\nUsage: make \033[36m<target>\033[0m\n\n'
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo

.PHONY: all build install tidy docker test test-v test-race lint fmt vet check release clean run help
