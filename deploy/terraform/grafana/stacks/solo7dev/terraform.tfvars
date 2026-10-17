stack                 = "solo7dev"
environments          = ["local", "dev", "staging"]
required_environments = []
slack                 = true
page_contact_point    = null

# The stack's Details page values (ADR-0012 decision 9), read with `gcx cloud stacks get <stack>`:
# hosts and user IDs from its record; each sender path answered 401 to an unauthenticated POST.
grafana_url = "https://solo7dev.grafana.net"
prom_url    = "https://prometheus-prod-67-prod-us-west-0.grafana.net/api/prom/push"
prom_user   = "3652529"
loki_url    = "https://logs-prod-021.grafana.net/loki/api/v1/push"
loki_user   = "1821929"
tempo_url   = "https://tempo-prod-15-prod-us-west-0.grafana.net/otlp"
tempo_user  = "1816230"
otlp_url    = "https://otlp-gateway-prod-us-west-0.grafana.net/otlp"
fleet_url   = "https://fleet-management-prod-014.grafana.net"
fleet_user  = "1864153"
