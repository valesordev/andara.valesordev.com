# A throwaway alert for checking a stack's notification policy end to end (AW-INF-046 AC-7):
# one rule that always fires, labelled with the environment under test. Created by
# `make tf-test-alert` and removed by `make tf-test-alert-clean`; its state is local, under
# .local/tf-test-alert/, never in the state bucket. Not part of any stack root.

terraform {
  required_version = ">= 1.11, < 2"

  required_providers {
    grafana = {
      source  = "grafana/grafana"
      version = "~> 4.47.0"
    }
  }

  backend "local" {}
}

variable "grafana_url" {
  description = "The stack's Grafana URL, from its committed terraform.tfvars."
  type        = string
}

variable "environment" {
  description = "The environment label the alert carries."
  type        = string

  validation {
    condition     = contains(["local", "dev", "staging", "prod"], var.environment)
    error_message = "environment must be local, dev, staging or prod."
  }
}

provider "grafana" {
  url = var.grafana_url
}

resource "grafana_folder" "test" {
  title = "andara-test-alert-${var.environment}"
}

resource "grafana_rule_group" "test" {
  name             = "andara-test-alert"
  folder_uid       = grafana_folder.test.uid
  interval_seconds = 60

  rule {
    name           = "AndaraTestAlert"
    condition      = "B"
    for            = "0s"
    no_data_state  = "Alerting"
    exec_err_state = "Alerting"

    labels = {
      environment = var.environment
      severity    = "warning"
    }

    annotations = {
      summary = "AW-INF-046 test alert for environment=${var.environment}; remove it with make tf-test-alert-clean."
    }

    data {
      ref_id         = "A"
      datasource_uid = "__expr__"
      model          = jsonencode({ refId = "A", type = "math", expression = "1" })
      relative_time_range {
        from = 0
        to   = 0
      }
    }

    data {
      ref_id         = "B"
      datasource_uid = "__expr__"
      model          = jsonencode({ refId = "B", type = "math", expression = "$A > 0" })
      relative_time_range {
        from = 0
        to   = 0
      }
    }
  }
}
