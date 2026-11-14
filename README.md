# Meeseeks

Ephemeral, customizable environments on Kubernetes through a small Go API and web UI.
Each environment is an ArgoCD `Application`, so every environment is GitOps-managed:
ArgoCD renders the manifests from Git, applies them, and keeps them in sync.

```
UI / curl ──► Meeseeks (Go) ──► ArgoCD API ──► Application ──► Git (deploy/) ──► Kubernetes
```

![UI](docs/ui.png)

## How a request becomes an environment

`POST /environments` creates one multi-source ArgoCD Application in namespace `env-<name>`:

| Request field | What Meeseeks generates |
|---|---|
| `branch` | `targetRevision` of every source (deploy any branch of this repo's manifests) |
| `cpu`, `memory` | Strategic-merge patch on Deployment `app` (requests = limits) |
| `replicas` | ArgoCD `kustomize.replicas` |
| `env_vars` | Env vars in the same patch |
| `dependencies` | One extra source per dependency (`deploy/deps/<name>`), plus `DATABASE_URL` / `REDIS_URL` / `MONGODB_URL` injected into the app |
| `env_type` | `env-type` label (`dev`, `staging`, `prod`) |

Manifests live in [`deploy/`](deploy): `base/` is the app (a tiny echo server that prints its pod name, so
you can see replicas load-balance), `deps/` has PostgreSQL, Redis and MongoDB.

## Run it locally (OrbStack)

Requirements: [OrbStack](https://orbstack.dev) with Kubernetes enabled, Terraform, the `argocd` CLI, Go 1.24+.

```bash
cd terraform && terraform init && terraform apply   # installs ArgoCD on :30080, writes argocd-token.txt
cd .. && make run                                   # http://localhost:22282
```

Without a cluster, `make dev-mock` serves the UI with fake data.

With OrbStack, each environment is reachable from the Mac at `http://app.env-<name>.svc.cluster.local`
(the UI links to it). On other clusters use `kubectl port-forward -n env-<name> svc/app 8080:80`.

## API

```bash
curl -X POST localhost:22282/environments -H 'Content-Type: application/json' -d '{
  "name": "my-feature", "branch": "main", "env_type": "dev",
  "cpu": "100m", "memory": "128Mi", "replicas": 2,
  "dependencies": ["postgresql", "redis"],
  "env_vars": {"LOG_LEVEL": "debug"}
}'
curl localhost:22282/environments               # list, with sync/health status
curl -X DELETE localhost:22282/environments/my-feature
```

The same endpoints return HTML fragments when called by the UI (HTMX, `HX-Request: true`).

## Development

```bash
make test        # unit tests
make docker      # build the image
make docker-run  # run it against the local ArgoCD
```

## Known limits

- Deleting an environment removes its resources, but ArgoCD leaves the `env-<name>` namespace behind.
- `env_type` is only a label; it doesn't change sizing.
- Dependencies use throwaway credentials and no persistent volumes. These environments are ephemeral.
