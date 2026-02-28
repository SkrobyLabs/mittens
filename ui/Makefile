# mittens-ui – Makefile

BINARY   := mittens-ui
MODULE   := github.com/Skroby/mittens/ui

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X 'main.version=$(VERSION)' \
	-X 'main.commit=$(COMMIT)' \
	-X 'main.date=$(DATE)'

.DEFAULT_GOAL := all

all: frontend build ## Build frontend and backend

# ─── Frontend ────────────────────────────────────────────────────────────────

frontend: ## Build the frontend
	cd frontend && npm install && npm run build

frontend-dev: ## Start Vite dev server
	cd frontend && npm run dev

# ─── Backend ─────────────────────────────────────────────────────────────────

build: tidy ## Build the mittens-ui binary
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/mittens-ui
	@echo "Built ./$(BINARY)"

tidy: ## Run go mod tidy
	go mod tidy

# ─── Dev ─────────────────────────────────────────────────────────────────────

dev: ## Run backend in dev mode (proxy to Vite)
	go run ./cmd/mittens-ui --dev

run: all ## Build and run
	./$(BINARY)

# ─── Quality ─────────────────────────────────────────────────────────────────

test: ## Run all tests
	go test ./...

vet: ## Run go vet
	go vet ./...

# ─── Clean ───────────────────────────────────────────────────────────────────

clean: ## Remove build artifacts
	rm -f $(BINARY)
	rm -rf frontend/dist frontend/node_modules
	go clean

# ─── Help ────────────────────────────────────────────────────────────────────

help: ## Show this help
	@printf '\nUsage: make \033[36m<target>\033[0m\n\n'
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo

.PHONY: all frontend frontend-dev build tidy dev run test vet clean help
