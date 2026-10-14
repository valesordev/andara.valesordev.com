# solo7's IRM resources: the integration, the schedule `primary`, the escalation chain
# `page` and the `irm-prod` contact point (ADR-0012 decision 10). Empty until AW-INF-051,
# which adds them with the IRM provider configuration in stacks/solo7-irm.

terraform {
  required_version = ">= 1.11, < 2"

  required_providers {
    grafana = {
      source  = "grafana/grafana"
      version = "~> 4.47.0"
    }
  }
}
