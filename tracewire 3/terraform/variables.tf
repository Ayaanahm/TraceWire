variable "cluster_name" {
  type    = string
  default = "tracewire"
}

variable "image_tag" {
  description = "Tag of the tracewire-* images to load into kind (built by `make images`)."
  type        = string
  default     = "v0.2.0"
}

variable "kube_prometheus_stack_version" {
  type    = string
  default = "66.2.1"
}
