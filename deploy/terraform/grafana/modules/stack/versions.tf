terraform {
  required_version = ">= 1.11, < 2"

  required_providers {
    grafana = {
      source  = "grafana/grafana"
      version = "~> 4.47.0"
    }
  }
}
