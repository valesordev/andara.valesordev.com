# The Andara folder and the tick-health dashboard (ADR-0012 decision 11, AW-INF-049). Both stacks
# deploy the same file: the data source UIDs match, so nothing is rendered per stack. AW-INF-047's
# rule groups go in the same folder.

locals {
  dashboard_json = file("${path.module}/../../../../grafana/dashboards/tick-health.json")
  dashboard      = jsondecode(local.dashboard_json)

  dashboard_variables = { for v in try(local.dashboard.templating.list, []) : v.name => v }
  dashboard_missing   = [for n in ["ds", "environment", "namespace"] : n if !contains(keys(local.dashboard_variables), n)]

  # Every panel reads through the ds variable, never a fixed data source.
  dashboard_fixed_ds = [for p in local.dashboard.panels : p.title if try(p.datasource.uid, "") != "$${ds}"]

  # Every selector in every query carries both labels, and no metric name is read without one.
  # A query with no series in it (the Tick Budget line, vector(0.05)) has nothing to filter.
  # Grafana's $environment and $namespace are plain text to Terraform: only "$${" interpolates.
  dashboard_unfiltered = distinct(flatten([
    for p in local.dashboard.panels : [
      for t in try(p.targets, []) : p.title
      if anytrue([
        for s in regexall("\\{[^}]*\\}", t.expr) :
        !strcontains(s, "environment=\"$environment\"") || !strcontains(s, "namespace=\"$namespace\"")
        ]) || length(regexall("[a-z][a-z0-9]*_[a-z0-9_]*[^a-z0-9_{(]",
      format("%s ", replace(t.expr, "/\\{[^}]*\\}/", "{}")))) > 0
    ]
  ]))
}

resource "grafana_folder" "andara" {
  uid   = "andara"
  title = "Andara"
}

resource "grafana_dashboard" "tick_health" {
  folder      = grafana_folder.andara.uid
  config_json = local.dashboard_json
  overwrite   = true

  lifecycle {
    precondition {
      condition     = length(local.dashboard_missing) == 0
      error_message = "tick-health.json lacks the variable(s) ${join(", ", local.dashboard_missing)} (ADR-0012 decision 11)."
    }
    precondition {
      condition     = length(local.dashboard_fixed_ds) == 0
      error_message = "tick-health.json: panel(s) ${join("; ", local.dashboard_fixed_ds)} must use the datasource $${ds}."
    }
    precondition {
      condition     = length(local.dashboard_unfiltered) == 0
      error_message = "tick-health.json: panel(s) ${join("; ", local.dashboard_unfiltered)} read a series without environment=\"$environment\" and namespace=\"$namespace\"."
    }
  }
}
