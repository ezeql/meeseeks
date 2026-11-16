# Meeseeks Makefile

.PHONY: build run test clean docker docker-run fmt lint vet help up down

# Variables
BINARY_NAME=meeseeks
DOCKER_IMAGE=meeseeks
DOCKER_TAG=latest
PORT=22282
# Written by `make up`: the kind cluster's Argo CD port, URL and token. While it exists, `make run` uses kind.
-include .kind/env
# Defaults (terraform/OrbStack). Exported variables and command-line values win.
ARGOCD_PORT ?= 30080
ARGOCD_URL ?= http://localhost:30080
ARGOCD_TOKEN ?= $(shell cat terraform/argocd-token.txt 2>/dev/null)

# Local cluster for `make up`: kind through `go run`, so there is nothing else to install.
# Not `?=`: a generic CLUSTER in the environment must not point `make down` at another cluster.
KIND = go run sigs.k8s.io/kind@v0.33.0
CLUSTER = meeseeks
KUBECTL = kubectl --kubeconfig .kind/kubeconfig

# GitOps client repo: Meeseeks commits one ArgoCD Application per environment here.
GITOPS_REPO ?= https://github.com/ezeql/meeseeks-environments
GITOPS_BRANCH ?= main
# Needs write access to GITOPS_REPO; defaults to the gh CLI login. Exported, never echoed.
GITHUB_TOKEN ?= $(shell gh auth token 2>/dev/null)
export GITOPS_REPO GITOPS_BRANCH GITHUB_TOKEN

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
	@docker run --rm -p 127.0.0.1:$(PORT):$(PORT) \
		-e ARGOCD_URL=http://host.docker.internal:$(ARGOCD_PORT) \
		-e ARGOCD_TOKEN=${ARGOCD_TOKEN} \
		-e GITOPS_REPO -e GITOPS_BRANCH -e GITHUB_TOKEN \
		-e PORT=$(PORT) \
		$(DOCKER_IMAGE):$(DOCKER_TAG)

# Local platform: kind cluster + Argo CD v3.6.1 + API token in .kind/env (never printed).
# Its own kubeconfig in .kind/, so the current kubectl context is left alone.
up:
	@mkdir -p .kind
	@$(KIND) get clusters 2>/dev/null | grep -qx $(CLUSTER) || printf '%s\n' \
		'kind: Cluster' 'apiVersion: kind.x-k8s.io/v1alpha4' 'nodes:' '- role: control-plane' \
		'  extraPortMappings:' '  - containerPort: 30080' '    hostPort: $(ARGOCD_PORT)' '    listenAddress: "127.0.0.1"' \
		| $(KIND) create cluster --name $(CLUSTER) --kubeconfig .kind/kubeconfig --config=-
	@$(KIND) export kubeconfig --name $(CLUSTER) --kubeconfig .kind/kubeconfig
	@echo "Installing Argo CD..."
	@$(KUBECTL) apply -k deploy/argocd --server-side --force-conflicts >/dev/null
	@$(KUBECTL) -n argocd rollout status deploy/argocd-server --timeout=300s
	@umask 077; url=http://localhost:$(ARGOCD_PORT); session=; \
	for i in $$(seq 60); do \
		pass=$$($(KUBECTL) -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' 2>/dev/null | base64 -d); \
		session=$$(printf '{"username":"admin","password":"%s"}' "$$pass" | curl -sf -H "Content-Type: application/json" $$url/api/v1/session -d @- | sed -n 's/.*"token":"\([^"]*\)".*/\1/p'); \
		[ -n "$$session" ] && break; sleep 2; \
	done; \
	token=$$(curl -sf -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $$session" $$url/api/v1/account/admin/token -d '{}' | sed -n 's/.*"token":"\([^"]*\)".*/\1/p'); \
	[ -n "$$token" ] || { echo "Could not get an Argo CD API token from $$url"; exit 1; }; \
	printf 'ARGOCD_PORT ?= %s\nARGOCD_URL ?= %s\nARGOCD_TOKEN ?= %s\n' "$(ARGOCD_PORT)" "$$url" "$$token" > .kind/env
	@echo "Argo CD is up at http://localhost:$(ARGOCD_PORT). Next: make run"

# Delete the kind cluster and its token
down:
	@$(KIND) delete cluster --name $(CLUSTER) --kubeconfig .kind/kubeconfig
	@rm -rf .kind

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
	@DEV_MODE=true go run .

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
	@echo "  up          - Create a kind cluster with Argo CD (needs Docker, Go, kubectl)"
	@echo "  down        - Delete the kind cluster"
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