# One stack's contact points and notification policy tree (ADR-0012 decision 10).
# The folder and dashboard are in dashboard.tf (AW-INF-049). Rules (AW-INF-047) join this module in
# their own story. Fleet pipelines are each root's pipelines.tf (AW-INF-051).

locals {
  # solo7dev posts to #andaras-world-dev, solo7 to #andaras-world.
  channel = var.stack == "solo7" ? "prod" : "dev"
  prod    = contains(var.environments, "prod")
  local   = contains(var.environments, "local")
}

resource "grafana_contact_point" "slack" {
  name = "slack-${local.channel}"

  slack {
    url = var.slack_webhook
  }
}

# "Notify no one" has to be an explicit receiver: with no matching policy Grafana falls back
# to its built-in email receiver. Grafana's API has no contact point without an integration,
# so `blackhole` holds one webhook to a reserved .invalid name (RFC 6761: never resolves), and
# the route to it is muted around the clock, so the webhook is not even attempted.
resource "grafana_contact_point" "blackhole" {
  count = local.local ? 1 : 0
  name  = "blackhole"

  webhook {
    url = "https://blackhole.invalid/"
  }
}

resource "grafana_mute_timing" "always" {
  count = local.local ? 1 : 0
  name  = "always"

  intervals {
    times {
      start = "00:00"
      end   = "24:00"
    }
  }
}

locals {
  # Children in evaluation order. Each matches every label in `labels`; a child without
  # `continue` ends the walk when it matches. An alert that matches none reaches the root
  # receiver, which is Slack in both stacks: never IRM, never silent.
  children = concat(
    local.local ? [{
      labels        = { environment = "local" }
      contact_point = grafana_contact_point.blackhole[0].name
      continue      = false
      mute_timings  = [grafana_mute_timing.always[0].name]
    }] : [],
    local.prod && var.page_contact_point != null ? [{
      labels        = { environment = "prod", severity = "page" }
      contact_point = var.page_contact_point
      continue      = true
      mute_timings  = []
    }] : [],
    local.prod ? [{
      labels        = { environment = "prod", severity = "page" }
      contact_point = grafana_contact_point.slack.name
      continue      = false
      mute_timings  = []
    }] : [],
  )
}

# A singleton that owns the whole tree: the first apply replaces any hand-made policy
# (ADR-0012 decision 8).
resource "grafana_notification_policy" "this" {
  contact_point = grafana_contact_point.slack.name
  group_by      = ["grafana_folder", "alertname"]

  dynamic "policy" {
    for_each = local.children
    content {
      contact_point = policy.value.contact_point
      continue      = policy.value.continue
      mute_timings  = policy.value.mute_timings

      dynamic "matcher" {
        for_each = policy.value.labels
        content {
          label = matcher.key
          match = "="
          value = matcher.value
        }
      }
    }
  }
}
