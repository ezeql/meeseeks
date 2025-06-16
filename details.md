# Meeseeks

## What You're Building

A **REST API for creating dynamic development environments** where developers can spin up isolated, customizable environments on-demand.

## Your Requirements

**Customization options:**

- **Branch selection** - Deploy any Git branch
- **Infrastructure sizing** - Choose CPU/memory/replicas
- **Service dependencies** - Add PostgreSQL, Redis, MongoDB as needed
- **Environment types** - Dev, staging, prod-like configurations

## Your Chosen Architecture

**Tech Stack:**

- **Go** - API server (your preferred language)
- **ArgoCD** - GitOps deployment (you already use this)
- **Kubernetes** - Runtime platform
- **OrbStack** - Local testing environment

**Flow:**

```noformat
HTTP API Call → Go Service → ArgoCD Application → Git Manifests → Kubernetes Resources
```

## Why This Approach

✅ **Leverages existing ArgoCD setup** - No new tooling  
✅ **GitOps benefits** - All changes tracked, auditable  
✅ **Clean separation** - API handles logic, ArgoCD handles deployment  
✅ **No exec.Command mess** - Direct ArgoCD API calls  
✅ **Kustomize patches** - Dynamic customization without template complexity

## The Solution

Your Go API creates ArgoCD Applications that point to base Kubernetes manifests in Git, then uses Kustomize patches to customize:

- Image tags (branch selection)
- Resource limits (infrastructure sizing)
- Additional services (PostgreSQL/Redis deployment)
- Environment variables (dev vs prod-like configs)

**Result:** Developers get isolated environments in minutes via simple HTTP calls, fully integrated with your existing GitOps workflow.
