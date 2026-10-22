terraform {
  required_version = ">= 1.11, < 2"

  required_providers {
    grafana = {
      source  = "grafana/grafana"
      version = "~> 4.47.0"
    }
  }
}

# The token reaches the provider only as GRAFANA_AUTH, and the Fleet token only as
# GRAFANA_FLEET_MANAGEMENT_AUTH, `<fleet_user>:<token>`, which scripts/tf.py composes (ADR-0012
# decision 4, AW-INF-051).
provider "grafana" {
  url                  = var.grafana_url
  fleet_management_url = var.fleet_url
}

module "stack" {
  source = "../../modules/stack"

  stack                 = var.stack
  environments          = var.environments
  required_environments = var.required_environments
  slack                 = var.slack
  page_contact_point    = var.page_contact_point
  slack_webhook         = var.slack_webhook
}

output "policy_tree" {
  description = "The notification policy tree, as data (AW-INF-046 AC-6)."
  value       = module.stack.policy_tree
}
