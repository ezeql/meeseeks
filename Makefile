# Meeseeks Makefile

.PHONY: build run test clean docker docker-run fmt lint vet help

# Variables
BINARY_NAME=meeseeks
DOCKER_IMAGE=meeseeks
DOCKER_TAG=latest
PORT=22282
ARGOCD_URL=http://localhost:30080
ARGOCD_TOKEN=$(shell cat terraform/argocd-token.txt)

# Default target
all: fmt vet test build

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@go build -o $(BINARY_NAME) .

# Run the application
run:
	@echo "Running $(BINARY_NAME)..."
	@ARGOCD_URL=$(ARGOCD_URL) ARGOCD_TOKEN=$(ARGOCD_TOKEN) go run .

# Run tests
test:
	@echo "Running tests..."
	@go test -v ./...

# Format code
fmt:
	@echo "Formatting code..."
	@go fmt ./...

# Lint code
lint:
	@echo "Linting code..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, running go vet instead"; \
		go vet ./...; \
	fi

# Vet code
vet:
	@echo "Vetting code..."
	@go vet ./...

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@go clean
	@rm -f $(BINARY_NAME)

# Build Docker image
docker:
	@echo "Building Docker image $(DOCKER_IMAGE):$(DOCKER_TAG)..."
	@docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

# Run Docker container
docker-run: docker
	@echo "Running Docker container..."
	@docker run -p $(PORT):$(PORT) \
		-e ARGOCD_URL=${ARGOCD_URL} \
		-e ARGOCD_TOKEN=${ARGOCD_TOKEN} \
		-e PORT=$(PORT) \
		$(DOCKER_IMAGE):$(DOCKER_TAG)

# Install dependencies
deps:
	@echo "Installing dependencies..."
	@go mod download
	@go mod tidy

# Development server with auto-reload (requires air)
dev:
	@if command -v air >/dev/null 2>&1; then \
		air; \
	else \
		echo "air not installed. Install with: go install github.com/cosmtrek/air@latest"; \
		echo "Falling back to regular run..."; \
		$(MAKE) run; \
	fi

# Run in mock mode for frontend development (no ArgoCD required)
dev-mock:
	@echo "🚀 Starting Meeseeks in Development Mode"
	@echo "🌐 Frontend available at: http://localhost:22282"
	@echo "💡 Running in mock mode - no real ArgoCD calls"
	@DEV_MODE=true go run main.go argocd.go validation.go

# Check if required environment variables are set
check-env:
	@if [ -z "$$ARGOCD_TOKEN" ]; then \
		echo "Error: ARGOCD_TOKEN environment variable is required"; \
		exit 1; \
	fi
	@echo "Environment variables OK"

# Run with environment check
run-prod: check-env build
	@echo "Running in production mode..."
	@./$(BINARY_NAME)

# Generate go.sum
mod-tidy:
	@echo "Tidying modules..."
	@go mod tidy

# Create test environment
create-env:
	@echo "Creating test environment..."
	@curl -X POST http://localhost:22282/environments \
		-H "Content-Type: application/json" \
		-d '{ \
			"name": "test-env", \
			"branch": "main", \
			"cpu": "100m", \
			"memory": "256Mi", \
			"replicas": 1, \
			"dependencies": ["postgresql"], \
			"env_type": "dev" \
		}'

# Destroy terraform infrastructure
terraform-destroy:
	@echo "Destroying terraform infrastructure..."
	@terraform destroy  -auto-approve

# Show help
help:
	@echo "Available targets:"
	@echo "  build       - Build the binary"
	@echo "  run         - Run the application"
	@echo "  test        - Run tests"
	@echo "  fmt         - Format code"
	@echo "  lint        - Lint code"
	@echo "  vet         - Vet code"
	@echo "  clean       - Clean build artifacts"
	@echo "  docker      - Build Docker image"
	@echo "  docker-run  - Build and run Docker container"
	@echo "  deps        - Install dependencies"
	@echo "  dev         - Run development server with auto-reload"
	@echo "  dev-mock    - Run in mock mode for frontend development (no ArgoCD required)"
	@echo "  check-env   - Check required environment variables"
	@echo "  run-prod    - Run in production mode with env check"
	@echo "  mod-tidy    - Tidy go modules"
	@echo "  create-env  - Create a test environment via API"
	@echo "  terraform-destroy - Destroy terraform infrastructure"
	@echo "  help        - Show this help"