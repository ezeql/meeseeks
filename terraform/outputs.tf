output "argocd_admin_password" {
  description = "ArgoCD admin password"
  value       = "admin123"
}

output "argocd_server_url" {
  description = "ArgoCD server URL"
  value       = "http://localhost:30080"
}

output "port_forward_command" {
  description = "Command to port forward ArgoCD server"
  value       = "kubectl port-forward svc/argocd-server -n argocd 8080:80"
}

output "get_token_instructions" {
  description = "Instructions to get API token"
  value = <<-EOT
    1. Access ArgoCD at http://localhost:30080 (or use port-forward)
    2. Login with username 'admin' and the password from argocd_admin_password
    3. Click on 'User Info' (admin dropdown in top right)
    4. Generate a new token
    5. Set environment variable: export ARGOCD_TOKEN='your-generated-token'
  EOT
}