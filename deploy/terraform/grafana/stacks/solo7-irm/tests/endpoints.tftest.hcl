# The IRM root is a skeleton until AW-INF-051: its committed endpoints are solo7's, and an
# empty one fails (AW-INF-046 AC-10). Run by `make tf-test` from stacks/solo7-irm.

mock_provider "grafana" {}

run "committed" {
  command = plan
}

run "empty_endpoint_refused" {
  command = plan

  variables {
    prom_url = ""
  }

  expect_failures = [var.prom_url]
}

# Every key, not one: endpoints.tf is the same file in every root (make tf-validate), so this
# run covers all three. A key whose empty check is dropped is missing from the failures.
run "every_empty_endpoint_refused" {
  command = plan

  variables {
    grafana_url = ""
    prom_url    = ""
    prom_user   = ""
    loki_url    = ""
    loki_user   = ""
    tempo_url   = ""
    tempo_user  = ""
    otlp_url    = ""
    fleet_url   = ""
    fleet_user  = ""
  }

  expect_failures = [
    var.grafana_url,
    var.prom_url,
    var.prom_user,
    var.loki_url,
    var.loki_user,
    var.tempo_url,
    var.tempo_user,
    var.otlp_url,
    var.fleet_url,
    var.fleet_user,
  ]
}
