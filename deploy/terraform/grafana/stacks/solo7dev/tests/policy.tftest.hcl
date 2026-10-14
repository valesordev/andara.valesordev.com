# solo7dev's tree against the committed terraform.tfvars (AW-INF-046 AC-6, AC-10, AC-11).
# Run by `make tf-test` from stacks/solo7dev; the endpoints are the committed ones.

mock_provider "grafana" {}

variables {
  slack_webhook = "https://hooks.slack.invalid/services/T0/B0/test"
}

run "tree" {
  command = apply

  assert {
    condition     = output.policy_tree.root == "slack-dev"
    error_message = "solo7dev: the root receiver is not slack-dev."
  }
  assert {
    condition = alltrue([
      for c in output.policy_tree.children : jsonencode(c.mute_timings) == jsonencode(["always"]) if c.contact_point == "blackhole"
    ])
    error_message = "solo7dev: the route to blackhole must be muted by the always mute timing."
  }
}

run "routes" {
  module {
    source = "../../tests/route"
  }

  variables {
    tree = run.tree.policy_tree
    alerts = {
      local   = { environment = "local", severity = "page" }
      dev     = { environment = "dev", severity = "page" }
      staging = { environment = "staging" }
      unknown = { environment = "unknown" }
      none    = { alertname = "NoEnvironment" }
    }
  }

  assert {
    condition     = jsonencode(output.routes.local) == jsonencode(["blackhole"])
    error_message = "solo7dev: environment=local reaches ${jsonencode(output.routes.local)}, not blackhole only."
  }
  assert {
    condition = alltrue([
      for a in ["dev", "staging", "unknown", "none"] : jsonencode(output.routes[a]) == jsonencode(["slack-dev"])
    ])
    error_message = "solo7dev: dev, staging, unknown and no environment must reach slack-dev only; got ${jsonencode(output.routes)}."
  }
}

# AC-11: solo7dev has no IRM resources.
run "page_contact_point_refused" {
  command = plan

  variables {
    page_contact_point = "irm-prod"
  }

  expect_failures = [var.page_contact_point]
}

# AC-10: an empty endpoint fails, naming the stack and the key.
run "empty_endpoint_refused" {
  command = plan

  variables {
    loki_url = ""
  }

  expect_failures = [var.loki_url]
}
