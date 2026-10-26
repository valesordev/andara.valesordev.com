# The rule groups solo7dev renders from alerts.yaml, one plan per required_environments value
# (AW-INF-047 AC-2, AC-3). The runs have no assertions of their own: `make tf-test` runs this file
# with -verbose -json, reads each run's planned rule groups, compares every rendered expr with the
# file's (scripts/tf.py rules_render) and parses them with promtool. A test can't reach a child
# module's resources, and an output would take tf_policy's separate-merge path.

mock_provider "grafana" {}

variables {
  slack_webhook = "https://hooks.slack.invalid/services/T0/B0/test"
}

# As committed: none required, so no absence line is rendered.
run "rules_required_none" {
  command = plan

  variables {
    required_environments = []
  }
}

# After the cutover's step 4(v) (ADR-0012 decision 7).
run "rules_required_dev" {
  command = plan

  variables {
    required_environments = ["dev"]
  }
}

# Every marked environment required, so the rendered expr is the file's, byte for byte (AC-2). prod
# is outside solo7dev's environments, so this run widens them; only the rules read the difference.
run "rules_required_every" {
  command = plan

  variables {
    environments          = ["local", "dev", "staging", "prod"]
    required_environments = ["dev", "prod"]
  }
}
