terraform {
  backend "gcs" {
    bucket = "andara-tfstate"
    prefix = "grafana/solo7dev"
  }
}
