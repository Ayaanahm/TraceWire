terraform {
  required_version = ">= 1.6"
  required_providers {
    kind    = { source = "tehcyx/kind", version = "~> 0.7" }
    helm    = { source = "hashicorp/helm", version = "~> 2.16" }
    kubectl = { source = "gavinbunney/kubectl", version = "~> 1.14" }
  }
}

provider "helm" {
  kubernetes {
    host                   = kind_cluster.this.endpoint
    cluster_ca_certificate = kind_cluster.this.cluster_ca_certificate
    client_certificate     = kind_cluster.this.client_certificate
    client_key             = kind_cluster.this.client_key
  }
}

provider "kubectl" {
  host                   = kind_cluster.this.endpoint
  cluster_ca_certificate = kind_cluster.this.cluster_ca_certificate
  client_certificate     = kind_cluster.this.client_certificate
  client_key             = kind_cluster.this.client_key
  load_config_file       = false
}
