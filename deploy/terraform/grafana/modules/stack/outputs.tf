output "policy_tree" {
  description = "The applied notification policy tree, as data: what tests/route walks (AW-INF-046 AC-6)."
  value = {
    root = grafana_notification_policy.this.contact_point
    children = [for p in grafana_notification_policy.this.policy : {
      contact_point = p.contact_point
      continue      = p.continue
      mute_timings  = p.mute_timings == null ? [] : tolist(p.mute_timings)
      matchers      = [for m in p.matcher : { label = m.label, match = m.match, value = m.value }]
    }]
  }
}

output "contact_points" {
  description = "The names of the contact points this stack defines."
  value       = concat([grafana_contact_point.slack.name], grafana_contact_point.blackhole[*].name)
}
