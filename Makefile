# Conduit Gateway Makefile

.PHONY: build build-prod build-full build-prod-full build-sre build-iot build-custom \
        run test test-full test-coverage clean deps format lint \
        install-deps channel-deps install init dev health help \
        container-core container-full container-sre container-iot

# Build configuration
BINARY_NAME=conduit
BUILD_DIR=bin
MAIN_PATH=./cmd/gateway

# Go configuration
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOFMT=gofmt
GOLINT=golint

# Version information
VERSION := $(shell git describe --tags --always --dirty)
GIT_COMMIT := $(shell git rev-parse --short HEAD)
GIT_TAG := $(shell git describe --tags --exact-match 2>/dev/null || echo "")
GIT_DIRTY := $(shell git diff-index --quiet HEAD -- || echo "true")
BUILD_DATE := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')

# Build flags
LDFLAGS := -X 'conduit/internal/version.Version=$(VERSION)'
LDFLAGS += -X 'conduit/internal/version.GitCommit=$(GIT_COMMIT)'
LDFLAGS += -X 'conduit/internal/version.GitTag=$(GIT_TAG)'
LDFLAGS += -X 'conduit/internal/version.GitDirty=$(GIT_DIRTY)'
LDFLAGS += -X 'conduit/internal/version.BuildDate=$(BUILD_DATE)'

# Default target
all: deps build

# Build the gateway binary
build:
	@echo "Building $(BINARY_NAME)..."
	@echo "Version: $(VERSION)"
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -buildvcs=false -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Built $(BUILD_DIR)/$(BINARY_NAME)"

# Build with optimizations for production
build-prod:
	@echo "Building $(BINARY_NAME) for production..."
	@echo "Version: $(VERSION)"
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -buildvcs=false -ldflags="$(LDFLAGS) -s -w" -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Built $(BUILD_DIR)/$(BINARY_NAME)"

# =============================================================================
# Optional Tool Build Variants
# =============================================================================
# Build tags for optional tools
OPTIONAL_TOOLS := datadog k8s pagerduty sre mqtt ssh unifi
SRE_TOOLS := datadog pagerduty sre
IOT_TOOLS := mqtt unifi

# Convert tool names to build tags: "datadog k8s" -> "with_datadog,with_k8s"
define tags_for
$(shell echo $(1) | tr ' ' '\n' | sed 's/^/with_/' | tr '\n' ',' | sed 's/,$$//')
endef

# Build with all optional tools included
build-full:
	@echo "Building $(BINARY_NAME) with all optional tools..."
	@echo "Tags: $(call tags_for,$(OPTIONAL_TOOLS))"
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -buildvcs=false -tags "$(call tags_for,$(OPTIONAL_TOOLS))" -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Built $(BUILD_DIR)/$(BINARY_NAME) (full)"

# Build production binary with all optional tools
build-prod-full:
	@echo "Building $(BINARY_NAME) for production with all optional tools..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -buildvcs=false -tags "$(call tags_for,$(OPTIONAL_TOOLS))" -ldflags="$(LDFLAGS) -s -w" -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Built $(BUILD_DIR)/$(BINARY_NAME) (full, production)"

# Build SRE-focused variant (Datadog + PagerDuty + SRE)
build-sre:
	@echo "Building $(BINARY_NAME) with SRE tools..."
	@echo "Tags: $(call tags_for,$(SRE_TOOLS))"
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -buildvcs=false -tags "$(call tags_for,$(SRE_TOOLS))" -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-sre $(MAIN_PATH)
	@echo "Built $(BUILD_DIR)/$(BINARY_NAME)-sre"

# Build IoT-focused variant (MQTT + UniFi)
build-iot:
	@echo "Building $(BINARY_NAME) with IoT tools..."
	@echo "Tags: $(call tags_for,$(IOT_TOOLS))"
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -buildvcs=false -tags "$(call tags_for,$(IOT_TOOLS))" -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-iot $(MAIN_PATH)
	@echo "Built $(BUILD_DIR)/$(BINARY_NAME)-iot"

# Build with custom tools: make build-custom TOOLS="datadog mqtt"
build-custom:
	@echo "Building $(BINARY_NAME) with custom tools: $(TOOLS)..."
	@echo "Tags: $(call tags_for,$(TOOLS))"
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -buildvcs=false -tags "$(call tags_for,$(TOOLS))" -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Built $(BUILD_DIR)/$(BINARY_NAME) (custom)"

# Run tests with all optional tools
test-full:
	@echo "Running tests with all optional tools..."
	$(GOTEST) -v -tags "$(call tags_for,$(OPTIONAL_TOOLS))" ./...

# =============================================================================

# Install binary to $(BUILD_DIR) and manage service
install: build
	@echo "Installing Conduit..."
	@./install.sh

# Run the gateway with default config
run: build
	@echo "Starting gateway..."
	./$(BUILD_DIR)/$(BINARY_NAME) --config config.json --verbose

# Run with Telegram adapter enabled
run-telegram: build
	@echo "Starting gateway with Telegram..."
	@if [ -z "$$TELEGRAM_BOT_TOKEN" ]; then \
		echo "Error: TELEGRAM_BOT_TOKEN environment variable is required"; \
		exit 1; \
	fi
	./$(BUILD_DIR)/$(BINARY_NAME) --config config.telegram.json --verbose

# Run tests
test:
	@echo "Running tests..."
	$(GOTEST) -v ./...

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	$(GOTEST) -v -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html
	rm -f conduit.db *.db

# Download Go dependencies
deps:
	@echo "Downloading Go dependencies..."
	$(GOGET) -d ./...
	go mod tidy
	go mod verify

# Install channel adapter dependencies (if any TypeScript adapters exist)
channel-deps:
	@if [ -f channels/adapters/package.json ]; then \
		echo "Installing channel adapter dependencies..."; \
		cd channels/adapters && npm install; \
	else \
		echo "No TypeScript channel adapters configured (skipping)"; \
	fi

# Install all dependencies
install-deps: deps channel-deps

# Format Go code
format:
	@echo "Formatting Go code..."
	$(GOFMT) -w .
	go mod tidy

# Lint Go code (requires golint: go install golang.org/x/lint/golint@latest)
lint:
	@echo "Linting Go code..."
	golint ./...
	go vet ./...

# Initialize workspace directory with starter templates
init-workspace:
	@echo "Creating workspace directory..."
	@mkdir -p workspace
	@for f in configs/workspace-templates/*.md; do \
		name=$$(basename $$f); \
		if [ ! -f "workspace/$$name" ]; then \
			cp "$$f" "workspace/$$name"; \
			echo "  Created workspace/$$name"; \
		else \
			echo "  Skipped workspace/$$name (already exists)"; \
		fi; \
	done
	@echo "Workspace ready at ./workspace"

# Create example config if it doesn't exist
init-config:
	@if [ ! -f config.json ]; then \
		echo "Creating config.json from example..."; \
		cp configs/examples/config.example.json config.json; \
		echo "Edit config.json with your API keys"; \
	else \
		echo "config.json already exists"; \
	fi

# Full initialization for new setup
init: deps channel-deps init-workspace init-config
	@echo ""
	@echo "Conduit Gateway initialized!"
	@echo ""
	@echo "Next steps:"
	@echo "1. Edit config.json with your API keys"
	@echo "2. Run 'make run' to start the gateway"
	@echo ""

# Development mode with auto-restart (requires 'air' tool)
dev:
	@if command -v air > /dev/null; then \
		echo "Starting development mode with auto-restart..."; \
		air; \
	else \
		echo "Install 'air' for auto-restart: go install github.com/cosmtrek/air@latest"; \
		echo "Falling back to single run..."; \
		make run; \
	fi

# =============================================================================
# Container Build Targets (podman)
# =============================================================================
CONTAINER_IMAGE ?= conduit

container-core:
	@echo "Building core container image..."
	podman build -f Containerfile \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg GIT_TAG=$(GIT_TAG) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(CONTAINER_IMAGE):core .

container-full:
	@echo "Building full container image (all optional tools)..."
	podman build -f Containerfile \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg GIT_TAG=$(GIT_TAG) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		--build-arg BUILD_TAGS="$(call tags_for,$(OPTIONAL_TOOLS))" \
		-t $(CONTAINER_IMAGE):full .

container-sre:
	@echo "Building SRE container image (Datadog+PagerDuty+SRE)..."
	podman build -f Containerfile \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg GIT_TAG=$(GIT_TAG) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		--build-arg BUILD_TAGS="$(call tags_for,$(SRE_TOOLS))" \
		-t $(CONTAINER_IMAGE):sre .

container-iot:
	@echo "Building IoT container image (MQTT+UniFi)..."
	podman build -f Containerfile \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg GIT_TAG=$(GIT_TAG) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		--build-arg BUILD_TAGS="$(call tags_for,$(IOT_TOOLS))" \
		-t $(CONTAINER_IMAGE):iot .

# Quick health check
health:
	@echo "Checking gateway health..."
	@if curl -s http://localhost:18789/health > /dev/null; then \
		echo "Gateway is running ✓"; \
	else \
		echo "Gateway is not responding ✗"; \
	fi

# Show help
help:
	@echo "Conduit Gateway - Available commands:"
	@echo ""
	@echo "Build Commands:"
	@echo "  make build           Build core binary (minimal, no optional tools)"
	@echo "  make build-prod      Build optimized core binary for production"
	@echo "  make build-full      Build with all optional tools included"
	@echo "  make build-prod-full Build production binary with all optional tools"
	@echo "  make build-sre       Build with SRE tools (Datadog+PagerDuty+SRE)"
	@echo "  make build-iot       Build with IoT tools (MQTT+UniFi)"
	@echo "  make build-custom    Build with custom tools: TOOLS=\"datadog mqtt\""
	@echo ""
	@echo "Run & Test:"
	@echo "  make run             Build and run the gateway"
	@echo "  make test            Run core tests"
	@echo "  make test-full       Run tests with all optional tools"
	@echo "  make test-coverage   Run tests with coverage report"
	@echo "  make dev             Development mode with auto-restart"
	@echo "  make health          Check if gateway is running"
	@echo ""
	@echo "Setup & Maintenance:"
	@echo "  make install         Build and install via install.sh"
	@echo "  make deps            Download Go dependencies"
	@echo "  make init            Full initialization for new setup"
	@echo "  make clean           Clean build artifacts"
	@echo "  make format          Format Go code"
	@echo "  make lint            Lint Go code"
	@echo ""
	@echo "Container Builds (podman):"
	@echo "  make container-core  Build core container image"
	@echo "  make container-full  Build container with all optional tools"
	@echo "  make container-sre   Build container with SRE tools"
	@echo "  make container-iot   Build container with IoT tools"
	@echo ""
	@echo "Optional Tools (build tags):"
	@echo "  datadog, k8s, pagerduty, sre, mqtt, ssh, unifi"
	@echo ""
	@echo "Environment variables:"
	@echo "  ANTHROPIC_API_KEY   Your Anthropic API key"
	@echo "  TELEGRAM_BOT_TOKEN  Your Telegram bot token"
	@echo ""