# solo7's tree against the committed terraform.tfvars, and with page_contact_point set as
# AW-INF-051 will set it (AW-INF-046 AC-6, AC-10). Run by `make tf-test` from stacks/solo7.

mock_provider "grafana" {}

variables {
  slack_webhook = "https://hooks.slack.invalid/services/T0/B0/test"

  alerts = {
    page     = { environment = "prod", severity = "page" }
    warning  = { environment = "prod", severity = "warning" }
    dev_page = { environment = "dev", severity = "page" }
    unknown  = { environment = "unknown", severity = "page" }
    none     = { severity = "page" }
  }
}

run "committed" {
  command = apply

  assert {
    condition     = output.policy_tree.root == "slack-prod"
    error_message = "solo7: the root receiver is not slack-prod."
  }
  assert {
    condition     = !contains([for c in output.policy_tree.children : c.contact_point], "irm-prod")
    error_message = "solo7: with page_contact_point = null the tree must name no irm-prod."
  }
}

run "committed_routes" {
  module {
    source = "../../tests/route"
  }

  variables {
    tree = run.committed.policy_tree
  }

  assert {
    condition     = alltrue([for a in keys(var.alerts) : jsonencode(output.routes[a]) == jsonencode(["slack-prod"])])
    error_message = "solo7 (page_contact_point = null): every alert must reach slack-prod only; got ${jsonencode(output.routes)}."
  }
}

run "irm" {
  command = apply

  variables {
    page_contact_point = "irm-prod"
  }
}

run "irm_routes" {
  module {
    source = "../../tests/route"
  }

  variables {
    tree = run.irm.policy_tree
  }

  assert {
    condition     = jsonencode(output.routes.page) == jsonencode(["irm-prod", "slack-prod"])
    error_message = "solo7: environment=prod, severity=page reaches ${jsonencode(output.routes.page)}, not irm-prod then slack-prod."
  }
  assert {
    condition = alltrue([
      for a in ["warning", "dev_page", "unknown", "none"] : jsonencode(output.routes[a]) == jsonencode(["slack-prod"])
    ])
    error_message = "solo7: every other alert must reach slack-prod only; got ${jsonencode(output.routes)}."
  }
}

run "empty_endpoint_refused" {
  command = plan

  variables {
    fleet_user = ""
  }

  expect_failures = [var.fleet_user]
}
