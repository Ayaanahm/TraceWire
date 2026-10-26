# Provisions the whole platform on a local kind cluster:
#   cluster (1 control plane + 2 workers) -> kube-prometheus-stack (Prometheus,
#   Grafana, Alertmanager) -> OTel Collector + Jaeger -> tracewire services,
#   alert rules and the Grafana dashboard.

locals {
  services = ["gateway", "orders", "inventory"]
  repo     = abspath("${path.module}/..")
}

resource "kind_cluster" "this" {
  name           = var.cluster_name
  wait_for_ready = true
  kind_config {
    kind        = "Cluster"
    api_version = "kind.x-k8s.io/v1alpha4"
    node {
      role = "control-plane"
      extra_port_mappings {
        container_port = 30080
        host_port      = 8080
      }
      extra_port_mappings {
        container_port = 30300
        host_port      = 3000
      }
    }
    node { role = "worker" }
    node { role = "worker" }
  }
}

# kind nodes can't pull from the host's Docker, so side-load the images.
resource "terraform_data" "load_images" {
  triggers_replace = [kind_cluster.this.id, var.image_tag]
  provisioner "local-exec" {
    command = join(" && ", [for s in local.services :
    "kind load docker-image tracewire-${s}:${var.image_tag} --name ${var.cluster_name}"])
  }
}

resource "helm_release" "kps" {
  name             = "kube-prometheus-stack"
  repository       = "https://prometheus-community.github.io/helm-charts"
  chart            = "kube-prometheus-stack"
  version          = var.kube_prometheus_stack_version
  namespace        = "observability"
  create_namespace = true
  timeout          = 900
  values           = [file("${path.module}/values/kube-prometheus-stack.yaml")]
}

data "kubectl_file_documents" "otel" {
  content = file("${local.repo}/deploy/otel/k8s-collector.yaml")
}

resource "kubectl_manifest" "otel" {
  for_each   = data.kubectl_file_documents.otel.manifests
  yaml_body  = each.value
  depends_on = [helm_release.kps]
}

data "kubectl_kustomize_documents" "app" {
  target = "${local.repo}/deploy/k8s/overlays/kind"
}

resource "kubectl_manifest" "app" {
  count      = length(data.kubectl_kustomize_documents.app.documents)
  yaml_body  = element(data.kubectl_kustomize_documents.app.documents, count.index)
  depends_on = [helm_release.kps, terraform_data.load_images]
}

# Same rules file Prometheus uses in docker compose, wrapped as a PrometheusRule.
resource "kubectl_manifest" "rules" {
  yaml_body = yamlencode({
    apiVersion = "monitoring.coreos.com/v1"
    kind       = "PrometheusRule"
    metadata   = { name = "tracewire", namespace = "tracewire", labels = { release = "kube-prometheus-stack" } }
    spec       = yamldecode(file("${local.repo}/deploy/prometheus/rules.yml"))
  })
  depends_on = [kubectl_manifest.app]
}

# Grafana's sidecar imports any ConfigMap labelled grafana_dashboard=1.
resource "kubectl_manifest" "dashboard" {
  yaml_body = yamlencode({
    apiVersion = "v1"
    kind       = "ConfigMap"
    metadata   = { name = "tracewire-dashboard", namespace = "observability", labels = { grafana_dashboard = "1" } }
    data       = { "tracewire-overview.json" = file("${local.repo}/deploy/grafana/dashboards/tracewire-overview.json") }
  })
  depends_on = [helm_release.kps]
}

# Expose the gateway on localhost:8080 through the kind port mapping.
resource "kubectl_manifest" "gateway_nodeport" {
  yaml_body = yamlencode({
    apiVersion = "v1"
    kind       = "Service"
    metadata   = { name = "gateway-public", namespace = "tracewire" }
    spec = {
      type     = "NodePort"
      selector = { "app.kubernetes.io/name" = "gateway" }
      ports    = [{ port = 8080, targetPort = "http", nodePort = 30080 }]
    }
  })
  depends_on = [kubectl_manifest.app]
}
