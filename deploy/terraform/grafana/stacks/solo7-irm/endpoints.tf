# The stack's endpoints, from its Details page (ADR-0012 decision 9). Not secrets, so
# committed in terraform.tfvars. No defaults: an endpoint is never guessed. The same file
# in every root.

variable "stack" {
  description = "The root's name: solo7dev, solo7 or solo7-irm."
  type        = string

  validation {
    condition     = contains(["solo7dev", "solo7", "solo7-irm"], var.stack)
    error_message = "stack must be solo7dev, solo7 or solo7-irm."
  }
}

variable "grafana_url" {
  description = "The stack's Grafana URL, with no path; what Terraform and gcx use."
  type        = string

  validation {
    condition     = var.grafana_url != ""
    error_message = "${var.stack}: grafana_url is empty."
  }
  validation {
    condition     = var.grafana_url == "" || can(regex("^https://[a-z0-9.-]+$", var.grafana_url))
    error_message = "${var.stack}: grafana_url must be https://<host> with no path."
  }
}

variable "prom_url" {
  description = "Prometheus remote-write URL, path included."
  type        = string

  validation {
    condition     = var.prom_url != ""
    error_message = "${var.stack}: prom_url is empty."
  }
  validation {
    condition     = var.prom_url == "" || startswith(var.prom_url, "https://")
    error_message = "${var.stack}: prom_url must be an https URL."
  }
}

variable "prom_user" {
  description = "Prometheus instance ID."
  type        = string

  validation {
    condition     = var.prom_user != ""
    error_message = "${var.stack}: prom_user is empty."
  }
  validation {
    condition     = var.prom_user == "" || can(regex("^[0-9]+$", var.prom_user))
    error_message = "${var.stack}: prom_user must be digits."
  }
}

variable "loki_url" {
  description = "Loki push URL, path included."
  type        = string

  validation {
    condition     = var.loki_url != ""
    error_message = "${var.stack}: loki_url is empty."
  }
  validation {
    condition     = var.loki_url == "" || startswith(var.loki_url, "https://")
    error_message = "${var.stack}: loki_url must be an https URL."
  }
}

variable "loki_user" {
  description = "Loki instance ID."
  type        = string

  validation {
    condition     = var.loki_user != ""
    error_message = "${var.stack}: loki_user is empty."
  }
  validation {
    condition     = var.loki_user == "" || can(regex("^[0-9]+$", var.loki_user))
    error_message = "${var.stack}: loki_user must be digits."
  }
}

variable "tempo_url" {
  description = "Tempo URL, path included."
  type        = string

  validation {
    condition     = var.tempo_url != ""
    error_message = "${var.stack}: tempo_url is empty."
  }
  validation {
    condition     = var.tempo_url == "" || startswith(var.tempo_url, "https://")
    error_message = "${var.stack}: tempo_url must be an https URL."
  }
}

variable "tempo_user" {
  description = "Tempo instance ID."
  type        = string

  validation {
    condition     = var.tempo_user != ""
    error_message = "${var.stack}: tempo_user is empty."
  }
  validation {
    condition     = var.tempo_user == "" || can(regex("^[0-9]+$", var.tempo_user))
    error_message = "${var.stack}: tempo_user must be digits."
  }
}

variable "otlp_url" {
  description = "OTLP gateway URL, path included."
  type        = string

  validation {
    condition     = var.otlp_url != ""
    error_message = "${var.stack}: otlp_url is empty."
  }
  validation {
    condition     = var.otlp_url == "" || startswith(var.otlp_url, "https://")
    error_message = "${var.stack}: otlp_url must be an https URL."
  }
}

variable "fleet_url" {
  description = "Fleet Management URL."
  type        = string

  validation {
    condition     = var.fleet_url != ""
    error_message = "${var.stack}: fleet_url is empty."
  }
  validation {
    condition     = var.fleet_url == "" || startswith(var.fleet_url, "https://")
    error_message = "${var.stack}: fleet_url must be an https URL."
  }
}

variable "fleet_user" {
  description = "Fleet Management user ID (an ID, not a URL)."
  type        = string

  validation {
    condition     = var.fleet_user != ""
    error_message = "${var.stack}: fleet_user is empty."
  }
  validation {
    condition     = var.fleet_user == "" || can(regex("^[0-9]+$", var.fleet_user))
    error_message = "${var.stack}: fleet_user must be digits."
  }
}
