# ArgoCD OpenTofu Installation

This OpenTofu configuration installs ArgoCD on your OrbStack Kubernetes cluster.

## Prerequisites

1. **OrbStack** with Kubernetes enabled
2. **kubectl** configured to use your OrbStack cluster
3. **OpenTofu** installed
4. **Helm** (OpenTofu will use it via the Helm provider)

## Installation

```bash
# Navigate to terraform directory
cd terraform

# Initialize OpenTofu
tofu init

# Plan the deployment
tofu plan

# Apply the configuration
tofu apply
```

## Access ArgoCD

After installation, you have two options to access ArgoCD:

### Option 1: NodePort (Direct Access)
```bash
# ArgoCD will be available at:
open http://localhost:30080
```

### Option 2: Port Forward (Recommended)
```bash
# Port forward to ArgoCD server
kubectl port-forward svc/argocd-server -n argocd 8080:80

# Access at:
open http://localhost:8080
```

## Login Credentials

- **Username:** `admin`
- **Password:** `admin123`

## Get API Token for Meeseeks

1. Login to ArgoCD UI with username `admin` and password `admin123`
2. Click on your username (admin) in the top right corner
3. Click "Generate New" under the Tokens section
4. Copy the generated token
5. Set the environment variable:
   ```bash
   export ARGOCD_TOKEN='your-generated-token'
   export ARGOCD_URL='http://localhost:8080'
   ```

## Test Meeseeks Integration

```bash
# From the project root directory
make run

# In another terminal, test the API
curl -X POST http://localhost:8080/environments \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-env",
    "branch": "main",
    "cpu": "100m",
    "memory": "128Mi",
    "replicas": 1,
    "env_type": "dev"
  }'
```

## Cleanup

```bash
tofu destroy
```

## Why This Approach?

✅ **Infrastructure as Code** - Repeatable, version-controlled setup  
✅ **Helm Integration** - Uses official ArgoCD Helm chart  
✅ **Local Development Optimized** - Resource limits suitable for OrbStack  
✅ **Multiple Access Methods** - NodePort and port-forward options  
✅ **Easy Cleanup** - `tofu destroy` removes everything