# solo7dev's Fleet pipelines against the committed terraform.tfvars (AW-INF-051 AC-3).
# Run by `make tf-test` from stacks/solo7dev. The mock provider still parses each source as Alloy.

mock_provider "grafana" {}

variables {
  slack_webhook = "https://hooks.slack.invalid/services/T0/B0/test"
}

run "pipelines" {
  command = plan

  assert {
    condition     = grafana_fleet_management_pipeline.shared_kafka.name == "shared-kafka"
    error_message = "solo7dev: the pipeline is not named shared-kafka."
  }
  assert {
    condition     = grafana_fleet_management_pipeline.shared_kafka.matchers == tolist(["stack=\"solo7dev\""])
    error_message = "solo7dev: shared-kafka must be matched on stack alone, stack=\"solo7dev\"."
  }
  assert {
    condition     = grafana_fleet_management_pipeline.shared_kafka.enabled
    error_message = "solo7dev: shared-kafka is not enabled."
  }
}
