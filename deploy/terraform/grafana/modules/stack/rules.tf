# The alert rules, as Grafana-managed rules (ADR-0012 decision 1, AW-INF-047). The source is
# deploy/helm/andara/files/alerts.yaml, read as it is: one rule group per groups[] entry, named as
# the file names it, in the Andara folder (dashboard.tf). Each rule becomes three queries:
#   A  the rule's expr, an instant query against grafanacloud-prom;
#   B  Math `$A * 0 + 1`, so every series A returns is 1 whatever its value (`up == 0` returns 0);
#   C  Threshold `B > 0`, the condition.
# That keeps Prometheus's "a rule fires for each series its expression returns", with the series'
# labels (environment and namespace among them) riding through to the alert.
#
# Per-stack rendering (decision 12): a line of an expr that ends `# env:<environment>` is kept only
# when var.required_environments names that environment; every other line is kept as written. So
# the rendered expr is the file's, byte for byte, less the marked lines this stack doesn't require.
# `make tf-test` renders every rule for every stack, compares it with the file and parses it with
# promtool (scripts/tf.py).

locals {
  alerts = yamldecode(file("${path.module}/../../../../helm/andara/files/alerts.yaml"))

  # Every rule is paused until its stack's cutover (decision 7): solo7dev's at step 4(ii-b), solo7's at
  # step 5, each by a merge that flips its entry here. A paused rule is defined, never evaluated.
  paused = { solo7dev = true, solo7 = true }[var.stack]

  env_marker = "#\\s*env:([A-Za-z0-9_-]+)\\s*$"

  # A duration as alerts.yaml writes it (one unit: 30s, 2m, 15m, 6h) in seconds, and back in the form
  # Grafana stores and returns (Prometheus's model.Duration: 0s, 30s, 2m, 1h30m), so a plan after the
  # apply reads back what it wrote.
  units = { s = 1, m = 60, h = 3600 }

  groups = {
    for g in local.alerts.groups : g.name => {
      interval = try(g.interval, "1m")
      rules = [
        for r in g.rules : {
          name = r.alert
          expr = join("\n", [
            for line in split("\n", r.expr) : line
            if length(regexall(local.env_marker, line)) == 0 || contains(var.required_environments, try(regex(local.env_marker, line)[0], ""))
          ])
          for             = try(tostring(r["for"]), "0s")
          keep_firing_for = try(tostring(r.keep_firing_for), null)
          labels          = { for k, v in try(r.labels, {}) : k => tostring(v) }
          annotations     = { for k, v in try(r.annotations, {}) : k => tostring(v) }
        }
      ]
    }
  }

  durations = distinct(flatten([
    for g in local.groups : concat([g.interval], [for r in g.rules : [r.for, r.keep_firing_for == null ? "0s" : r.keep_firing_for]])
  ]))
  bad_durations = [for d in local.durations : d if length(regexall("^[0-9]+[smh]$", d)) == 0]
  seconds = {
    for d in local.durations : d => length(regexall("^[0-9]+[smh]$", d)) == 0 ? 0 : tonumber(regex("^([0-9]+)", d)[0]) * local.units[regex("([smh])$", d)[0]]
  }
  canonical = {
    for d, s in local.seconds : d => s == 0 ? "0s" : join("", [
      floor(s / 3600) > 0 ? "${floor(s / 3600)}h" : "",
      floor(s % 3600 / 60) > 0 ? "${floor(s % 3600 / 60)}m" : "",
      s % 60 > 0 ? "${s % 60}s" : "",
    ])
  }

  rule_names     = flatten([for g in local.groups : [for r in g.rules : r.name]])
  rule_names_dup = distinct([for n in local.rule_names : n if length([for m in local.rule_names : m if m == n]) > 1])
}

resource "grafana_rule_group" "andara" {
  for_each = local.groups

  name             = each.key
  folder_uid       = grafana_folder.andara.uid
  interval_seconds = local.seconds[each.value.interval]

  dynamic "rule" {
    for_each = each.value.rules
    content {
      name            = rule.value.name
      condition       = "C"
      for             = local.canonical[rule.value.for]
      keep_firing_for = rule.value.keep_firing_for == null ? null : local.canonical[rule.value.keep_firing_for]
      no_data_state   = "OK"
      exec_err_state  = "Error"
      is_paused       = local.paused
      labels          = rule.value.labels
      annotations     = rule.value.annotations

      data {
        ref_id         = "A"
        datasource_uid = "grafanacloud-prom"
        model = jsonencode({
          refId         = "A"
          expr          = rule.value.expr
          instant       = true
          range         = false
          intervalMs    = 1000
          maxDataPoints = 43200
        })
        relative_time_range {
          from = 600
          to   = 0
        }
      }

      data {
        ref_id         = "B"
        datasource_uid = "__expr__"
        model          = jsonencode({ refId = "B", type = "math", expression = "$A * 0 + 1" })
        relative_time_range {
          from = 0
          to   = 0
        }
      }

      data {
        ref_id         = "C"
        datasource_uid = "__expr__"
        model = jsonencode({
          refId      = "C"
          type       = "threshold"
          expression = "B"
          conditions = [{ evaluator = { type = "gt", params = [0] } }]
        })
        relative_time_range {
          from = 0
          to   = 0
        }
      }
    }
  }

  lifecycle {
    precondition {
      condition     = length(local.bad_durations) == 0
      error_message = "alerts.yaml: duration(s) ${join(", ", local.bad_durations)} are not one number and one unit (s, m or h)."
    }
    precondition {
      condition     = length(local.rule_names_dup) == 0
      error_message = "alerts.yaml: alert name(s) ${join(", ", local.rule_names_dup)} appear more than once; a rule's name is its identity in Grafana."
    }
  }
}
