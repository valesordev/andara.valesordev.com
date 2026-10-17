variable "stack" {
  description = "The stack this module manages: solo7dev or solo7 (ADR-0012 decision 9)."
  type        = string

  validation {
    condition     = contains(["solo7dev", "solo7"], var.stack)
    error_message = "stack must be solo7dev or solo7."
  }
}

variable "environments" {
  description = "The environment label values this stack holds (ADR-0012 decision 9)."
  type        = list(string)

  validation {
    condition     = length(var.environments) > 0 && alltrue([for e in var.environments : contains(["local", "dev", "staging", "prod"], e)])
    error_message = "${var.stack}: environments must be a non-empty subset of local, dev, staging, prod."
  }
}

variable "required_environments" {
  description = "Environments whose absence rules are rendered (ADR-0012 decision 12); AW-INF-047's cutover sets them."
  type        = list(string)

  validation {
    condition     = alltrue([for e in var.required_environments : contains(var.environments, e)])
    error_message = "${var.stack}: every required_environments value must be in environments."
  }
}

variable "slack" {
  description = "The stack's root receiver is its Slack contact point (ADR-0012 decision 10)."
  type        = bool

  validation {
    condition     = var.slack
    error_message = "${var.stack}: slack must be true; the root receiver is the stack's Slack contact point, and without it an alert with no environment would be silent (ADR-0012 decision 10)."
  }
}

variable "page_contact_point" {
  description = "Contact point severity=page alerts in prod reach before Slack, by name; null leaves the IRM child out (ADR-0012 decision 10)."
  type        = string
  nullable    = true
}

variable "slack_webhook" {
  description = "The stack's Slack incoming webhook, from TF_VAR_slack_webhook (SLACK_WEBHOOK_DEV or SLACK_WEBHOOK_PROD)."
  type        = string
  sensitive   = true
}
