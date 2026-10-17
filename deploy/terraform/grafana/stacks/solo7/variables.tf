variable "environments" {
  description = "The environment label values this stack holds (ADR-0012 decision 9)."
  type        = list(string)
}

variable "required_environments" {
  description = "Environments whose absence rules are rendered (ADR-0012 decision 12)."
  type        = list(string)
}

variable "slack" {
  description = "The root receiver is the stack's Slack contact point (ADR-0012 decision 10)."
  type        = bool
}

variable "page_contact_point" {
  description = "Contact point prod pages reach before Slack, by name; null until AW-INF-051 creates irm-prod."
  type        = string
  nullable    = true

  validation {
    condition     = var.stack != "solo7dev" || var.page_contact_point == null
    error_message = "${var.stack}: page_contact_point must be null; solo7dev has no IRM resources (ADR-0012 decision 10)."
  }
}

variable "slack_webhook" {
  description = "Slack incoming webhook; from TF_VAR_slack_webhook, never committed."
  type        = string
  sensitive   = true
}
