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
