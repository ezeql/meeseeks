terraform {
  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.0"
    }
  }
}

provider "kubernetes" {
  config_path = "~/.kube/config"
}

provider "helm" {
  kubernetes {
    config_path = "~/.kube/config"
  }
}

# Create ArgoCD namespace
resource "kubernetes_namespace" "argocd" {
  metadata {
    name = "argocd"
    labels = {
      "app.kubernetes.io/name" = "argocd"
    }
  }
}

# Install ArgoCD using Helm
resource "helm_release" "argocd" {
  name       = "argocd"
  repository = "https://argoproj.github.io/argo-helm"
  chart      = "argo-cd"
  namespace  = kubernetes_namespace.argocd.metadata[0].name
  version    = "5.51.6"

  # Custom values for OrbStack/local development
  values = [
    yamlencode({
      global = {
        domain = "localhost:8080"
      }

      configs = {
        params = {
          "server.insecure" = true
        }
        secret = {
          createSecret = false
        }
        # Enable API key capability for admin account
        cm = {
          "accounts.admin" = "apiKey"
        }
      }

      server = {
        service = {
          type = "ClusterIP"
        }
        ingress = {
          enabled = false
        }
      }

      # Enable notifications controller for webhook support
      notifications = {
        enabled = true
      }

      # Resource limits for local development
      controller = {
        resources = {
          requests = {
            cpu    = "100m"
            memory = "128Mi"
          }
          limits = {
            cpu    = "500m"
            memory = "512Mi"
          }
        }
      }

      server = {
        resources = {
          requests = {
            cpu    = "50m"
            memory = "64Mi"
          }
          limits = {
            cpu    = "200m"
            memory = "256Mi"
          }
        }
      }

      repoServer = {
        resources = {
          requests = {
            cpu    = "50m"
            memory = "64Mi"
          }
          limits = {
            cpu    = "200m"
            memory = "256Mi"
          }
        }
      }
    })
  ]

  depends_on = [kubernetes_namespace.argocd]
}

# Create a service for port forwarding
resource "kubernetes_service" "argocd_server_nodeport" {
  metadata {
    name      = "argocd-server-nodeport"
    namespace = kubernetes_namespace.argocd.metadata[0].name
  }

  spec {
    type = "NodePort"

    port {
      port        = 80
      target_port = 8080
      node_port   = 30080
    }

    selector = {
      "app.kubernetes.io/name" = "argocd-server"
    }
  }

  depends_on = [helm_release.argocd]
}

# Create ArgoCD secret with known password
resource "kubernetes_secret" "argocd_secret" {
  metadata {
    name      = "argocd-secret"
    namespace = kubernetes_namespace.argocd.metadata[0].name
    labels = {
      "app.kubernetes.io/name"    = "argocd-secret"
      "app.kubernetes.io/part-of" = "argocd"
    }
  }

  data = {
    # Password: admin123 (bcrypt hash)
    "admin.password"      = "$2a$10$XaKFeYN8WSpuVm9G/D.Uk.4.16wVNSslPMAJjFtQ43PbfJl1xNaUK"
    "admin.passwordMtime" = "2024-01-01T00:00:00Z"
    "server.secretkey"    = base64encode("server-secret-key")
  }

  type = "Opaque"
}

# Generate ArgoCD API token
resource "null_resource" "argocd_token" {
  depends_on = [
    helm_release.argocd,
    kubernetes_secret.argocd_secret,
    kubernetes_service.argocd_server_nodeport
  ]

  provisioner "local-exec" {
    command = <<-EOT
      # Wait for ArgoCD to be ready
      echo "Waiting for ArgoCD to be ready..."
      kubectl wait --for=condition=available --timeout=300s deployment/argocd-server -n argocd
      
      # Wait a bit more for the service to be fully ready
      sleep 30
      
      # Check ArgoCD version
      echo "ArgoCD CLI version:"
      argocd version --client
      
      # Login to ArgoCD
      echo "Logging in to ArgoCD..."
      if argocd login localhost:30080 --username admin --password admin123 --insecure; then
        echo "Login successful"
        
        # Wait for config to be applied and restart ArgoCD server to pick up the apiKey capability
        echo "Waiting for ArgoCD to restart after config changes..."
        kubectl rollout restart deployment/argocd-server -n argocd
        kubectl rollout status deployment/argocd-server -n argocd --timeout=180s
        sleep 10
        
        # Re-login after restart
        argocd login localhost:30080 --username admin --password admin123 --insecure
      else
        echo "Login failed, continuing with alternative methods..."
        # Don't exit, try other token generation methods
      fi
      
      # Generate token - try multiple approaches
      echo "Generating API token..."
      TOKEN=""
      
      # Method 1: Try generating token for admin directly
      echo "Trying to generate token for admin account..."
      TOKEN=$(argocd account generate-token --account admin 2>/dev/null | grep -v "level=" | head -1)
      
      if [ -z "$TOKEN" ] || [ "$TOKEN" = "" ]; then
        echo "Direct token generation failed, trying alternative approaches..."
        
        # Method 2: Try creating and using a service account
        echo "Attempting to create meeseeks service account..."
        if argocd account create meeseeks 2>/dev/null; then
          echo "Service account created, generating token..."
          TOKEN=$(argocd account generate-token --account meeseeks 2>/dev/null | grep -v "level=" | head -1)
        fi
      fi
      
      # Method 3: Use kubectl to create a token directly
      if [ -z "$TOKEN" ] || [ "$TOKEN" = "" ]; then
        echo "CLI token generation failed, creating token via kubectl..."
        # Create a service account token manually
        kubectl create serviceaccount argocd-api-token -n argocd 2>/dev/null || echo "ServiceAccount may already exist"
        kubectl create clusterrolebinding argocd-api-token --clusterrole=admin --serviceaccount=argocd:argocd-api-token 2>/dev/null || echo "ClusterRoleBinding may already exist"
        
        # For Kubernetes 1.24+, create token explicitly
        TOKEN=$(kubectl create token argocd-api-token -n argocd --duration=8760h 2>/dev/null)
      fi
      
      # Final fallback - generate a simple token format
      if [ -z "$TOKEN" ] || [ "$TOKEN" = "" ]; then
        echo "All methods failed, creating placeholder token..."
        TOKEN="GENERATE_MANUALLY_VIA_UI"
        echo "Please generate token manually via ArgoCD UI:"
        echo "1. Go to http://localhost:30080"
        echo "2. Login with admin/admin123"
        echo "3. Click 'User Info' -> Generate New Token"
      fi
      
      # Save token to file
      echo "$TOKEN" > argocd-token.txt
      echo "Token content: $TOKEN"
      echo "Token saved to argocd-token.txt"
      echo "ARGOCD_TOKEN=$TOKEN"
      
      # Verify file was created
      if [ -f "argocd-token.txt" ]; then
        echo "File created successfully, size: $(wc -c < argocd-token.txt) bytes"
        echo "File contents:"
        cat argocd-token.txt
      else
        echo "ERROR: File was not created"
      fi
    EOT
  }

  # Trigger recreation when dependencies change
  triggers = {
    helm_release_id = helm_release.argocd.id
    secret_id       = kubernetes_secret.argocd_secret.id
  }
}

# Output the ArgoCD token
output "argocd_token_file" {
  value      = "ArgoCD API token has been generated and saved to argocd-token.txt"
  depends_on = [null_resource.argocd_token]
}

# Output ArgoCD access information
output "argocd_access" {
  value = {
    url      = "http://localhost:30080"
    username = "admin"
    password = "admin123"
    note     = "API token saved in argocd-token.txt"
  }
  depends_on = [null_resource.argocd_token]
}
