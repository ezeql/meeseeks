# Meeseeks - Dynamic Development Environments

A REST API for creating dynamic development environments using ArgoCD and Kubernetes.

## Features

- **Branch deployment** - Deploy any Git branch
- **Resource customization** - Configure CPU, memory, and replicas
- **Service dependencies** - Add PostgreSQL, Redis, MongoDB
- **Environment types** - Dev, staging, prod-like configurations
- **GitOps integration** - Uses ArgoCD for deployment management

## API Endpoints

### Create Environment
```bash
POST /environments
Content-Type: application/json

{
  "name": "my-feature",
  "branch": "feature/new-api",
  "cpu": "500m",
  "memory": "1Gi",
  "replicas": 2,
  "dependencies": ["postgresql", "redis"],
  "env_type": "dev",
  "env_vars": {
    "DEBUG": "true",
    "LOG_LEVEL": "debug"
  }
}
```

### List Environments
```bash
GET /environments
```

### Delete Environment
```bash
DELETE /environments/{name}
```

## Configuration

Set these environment variables:

- `ARGOCD_URL` - ArgoCD server URL (default: http://localhost:8080)
- `ARGOCD_TOKEN` - ArgoCD authentication token (required)
- `PORT` - Server port (default: 8080)

## Example Usage

```bash
# Create a new environment
curl -X POST http://localhost:8080/environments \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-test-env",
    "branch": "main",
    "cpu": "100m",
    "memory": "256Mi",
    "replicas": 1,
    "dependencies": ["postgresql"],
    "env_type": "dev"
  }'

# List all environments
curl http://localhost:8080/environments

# Delete an environment
curl -X DELETE http://localhost:8080/environments/my-test-env
```

## Docker

```bash
# Build
docker build -t meeseeks .

# Run
docker run -p 8080:8080 \
  -e ARGOCD_URL=https://argocd.example.com \
  -e ARGOCD_TOKEN=your-token \
  meeseeks
```

## Architecture

```
Developer → HTTP API → Meeseeks → ArgoCD → Kubernetes
```

Meeseeks creates ArgoCD Applications that use Kustomize patches to customize base Kubernetes manifests for each environment.