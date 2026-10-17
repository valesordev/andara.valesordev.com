terraform {
  required_version = ">= 1.11, < 2"

  required_providers {
    grafana = {
      source  = "grafana/grafana"
      version = "~> 4.47.0"
    }
  }
}

# solo7's Grafana, with solo7's read or apply token as GRAFANA_AUTH (ADR-0012 decision 4).
# AW-INF-051 adds the IRM provider configuration (the IRM token) with the module's resources.
provider "grafana" {
  url = var.grafana_url
}

module "irm" {
  source = "../../modules/irm"
}
