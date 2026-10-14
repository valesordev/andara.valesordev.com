# Walks a one-level notification policy tree the way Grafana's Alertmanager does, for
# terraform test: children in order, a child matches when every `=` matcher equals the
# alert's label (an absent label is ""), the walk stops at the first match without
# `continue`, and an alert that matches no child reaches the root receiver.

variable "tree" {
  description = "A stack's policy_tree output."
  type = object({
    root = string
    children = list(object({
      contact_point = string
      continue      = bool
      mute_timings  = list(string)
      matchers      = list(object({ label = string, match = string, value = string }))
    }))
  })
}

variable "alerts" {
  description = "Test alerts by name, each a map of labels."
  type        = map(map(string))
}

locals {
  matched = {
    for name, labels in var.alerts : name => [
      for i, c in var.tree.children : i
      if alltrue([for m in c.matchers : m.match == "=" && lookup(labels, m.label, "") == m.value])
    ]
  }

  routes = {
    for name, idx in local.matched : name => length(idx) == 0 ? [var.tree.root] : [
      for k, i in idx : var.tree.children[i].contact_point
      if alltrue([for j in slice(idx, 0, k) : var.tree.children[j].continue])
    ]
  }
}

output "routes" {
  description = "The receivers each alert reaches, in order."
  value       = local.routes
}
