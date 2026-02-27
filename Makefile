# Conduit Gateway Makefile

.PHONY: build run test clean deps format lint install-deps channel-deps install

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

# Initialize workspace directory
init-workspace:
	@echo "Creating workspace directory..."
	@mkdir -p workspace
	@echo "Workspace created at ./workspace"

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
	@echo "  make build         Build the gateway binary"
	@echo "  make build-prod    Build optimized production binary"
	@echo "  make install       Build and install via install.sh"
	@echo "  make run          Build and run the gateway"
	@echo "  make test         Run tests"
	@echo "  make test-coverage Run tests with coverage report"
	@echo "  make clean        Clean build artifacts"
	@echo "  make deps         Download Go dependencies"
	@echo "  make channel-deps Install Node.js dependencies"
	@echo "  make install-deps Install all dependencies"
	@echo "  make format       Format Go code"
	@echo "  make lint         Lint Go code"
	@echo "  make init         Full initialization for new setup"
	@echo "  make dev          Development mode with auto-restart"
	@echo "  make health       Check if gateway is running"
	@echo "  make help         Show this help"
	@echo ""
	@echo "Environment variables:"
	@echo "  ANTHROPIC_API_KEY  Your Anthropic API key"
	@echo "  TELEGRAM_BOT_TOKEN Your Telegram bot token"
	@echo ""