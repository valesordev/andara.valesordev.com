stack                 = "solo7"
environments          = ["prod"]
required_environments = []
slack                 = true
# "irm-prod" once AW-INF-051 creates it in the solo7-irm root (ADR-0012 decision 10).
page_contact_point = null

# From the stack's Details page (ADR-0012 decision 9).
grafana_url = "https://solo7.grafana.net"
prom_url    = ""
prom_user   = ""
loki_url    = ""
loki_user   = ""
tempo_url   = ""
tempo_user  = ""
otlp_url    = ""
fleet_url   = ""
fleet_user  = ""
