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

  # Every selector in every query carries both labels as whole matchers, and no metric name is read
  # without a selector. A query with no series in it (the Tick Budget line, vector(0.05)) has nothing
  # to filter. Grafana's $environment and $namespace are plain text to Terraform: only "$${" interpolates.
  dashboard_targets = flatten([
    for p in local.dashboard.panels : [for t in try(p.targets, []) : { title = p.title, expr = t.expr }]
  ])
  dashboard_matchers = ["environment=\"\\$environment\"", "namespace=\"\\$namespace\""]
  promql_keywords    = ["and", "or", "unless", "by", "without", "on", "ignoring", "group_left", "group_right", "bool", "offset", "inf", "nan"]

  # What is left of a query outside its selectors: selectors emptied, then string literals, ranges and
  # grouping clauses removed, so every remaining name is a function call, a selector's metric or bare.
  dashboard_outside = [
    for t in local.dashboard_targets : replace(replace(replace(replace(replace(t.expr,
      "/\\{[^}]*\\}/", "{}"),
      "/\"(?:[^\"\\\\]|\\\\.)*\"/", "\"\""),
      "/\\[[^\\]]*\\]/", ""),
      "/\\b(?:by|without|on|ignoring|group_left|group_right)\\s*\\([^)]*\\)/", " "),
    "/\\s+([{(])/", "$1")
  ]

  dashboard_unfiltered = distinct([
    for i, t in local.dashboard_targets : t.title
    if anytrue([
      for s in regexall("\\{[^}]*\\}", t.expr) : anytrue([
        for m in local.dashboard_matchers : length(regexall("[{,]\\s*${m}\\s*[,}]", s)) == 0
      ])
      ]) || anytrue([
      for n in regexall("[A-Za-z_:][A-Za-z0-9_:]*[{(]?", local.dashboard_outside[i]) :
      !endswith(n, "{") && !endswith(n, "(") && !contains(local.promql_keywords, n)
    ])
  ])
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
