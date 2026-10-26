output "gateway_url" { value = "http://localhost:8080/api/orders" }
output "grafana_url" { value = "http://localhost:3000/d/tracewire-overview (admin / admin)" }
output "kubeconfig_path" { value = kind_cluster.this.kubeconfig_path }
