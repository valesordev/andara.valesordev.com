# The stack's Fleet Management pipelines (ADR-0014 §3, AW-INF-051), pulled by the fleet collector that
# AW-INF-065 installs. Each is matched on stack alone, one literal file() per source, never a for_each
# over file names; scripts/pipelines_check.py checks every source in make check. solo7's root gets the
# same resources from the same sources in AW-INF-078.

resource "grafana_fleet_management_pipeline" "shared_kafka" {
  name                       = "shared_kafka"
  contents                   = file("${path.module}/../../pipelines/shared_kafka.alloy")
  matchers                   = ["stack=\"${var.stack}\""]
  enabled                    = true
  terraform_source_namespace = var.stack
}
