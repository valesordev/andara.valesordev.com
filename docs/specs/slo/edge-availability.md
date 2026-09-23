# SLO — Edge availability

> **Status: proposed targets, 2026-09-14.** `AW-INF-006` ships the measurement; the nightly stream
> soak is the first evidence.

The edge is the cluster's Traefik in front of `andara-server` (`AW-INF-006`): TLS terminated on
`host`, re-originated to the pod as HTTP/2, two routers on one hostname. It is the first thing a
player's connection meets and the one hop the server cannot observe from the inside — a
certificate that expired, a router that cannot find a ready endpoint, or a backend handshake that
fails verification all look, from the pod, like silence.

This SLO is about what a player sees before a Session exists: whether the edge lets them through.

## SLI

**Definition:** the fraction of requests through the Andara routers that the edge answers with
anything other than a 5xx.

```promql
1 - (
  sum(rate(traefik_router_requests_total{router=~"andara-.*", code=~"5.."}[5m]))
  /
  sum(rate(traefik_router_requests_total{router=~"andara-.*"}[5m]))
)
```

Per environment, `sum by (namespace)` after lifting the namespace out of the router name
(`<namespace>-<ingress>-…`), because on the cluster Traefik's series carry `namespace="traefik"` —
the form `files/alerts.yaml` evaluates (`AW-INF-008`). The SLO is each environment's own.

What it counts: Connect and gRPC-Web requests, and every request Traefik refused to forward — no
ready endpoint (`503`), backend TLS verification failed or the dial timed out (`502`, `504`).
Those are counted on the router, not the service, which is why the router series is the SLI and
not `traefik_service_requests_total`.

What it does not count, named rather than assumed: gRPC-protocol responses. Traefik 3.7 labels
those `code="2"` whatever happened — a successful `OpenSession` and a `503` with no backend carry
the same label. `andara-cli` and every generated Go client speak gRPC, so their failures at the
edge are visible only from the other side, as `AndaraServerUnavailable` (`AW-INF-003`) when the
pod is gone and as a fall in `andara_sessions_active` when it is not. A Traefik release that
labels gRPC honestly narrows this gap; until then the SLI is the Connect and gRPC-Web view.

| | |
|---|---|
| **Target** | 99.9% |
| **Window** | rolling 28 days |
| **Error budget** | **40.3 minutes per 28 days** of 5xx-dominated 5-minute windows |

`[ASSUMPTION]` 99.9% matches `world-write-availability.md`: an edge that fails more often than the
World it fronts would make the World's own budget unmeasurable from outside.

### Precondition: a valid certificate

A certificate the edge cannot present is 100% of requests failing at the handshake, which never
reaches a router counter. The SLI therefore has a precondition, measured separately:

```promql
certmanager_certificate_expiration_timestamp_seconds{name=~"andara-.*"} - time() > 7 * 24 * 3600
```

`renewBefore` is 10 days; a certificate inside 7 days of expiry is one whose renewal did not happen.

## Alerts

| Alert | Fires when | Runbook |
|-------|-----------|---------|
| `IngressErrorRateHigh` | 5xx ratio on the Andara routers > 1% for 10 m | `docs/runbooks/ingress-error-rate.md` |
| `CertificateExpiringSoon` | any `andara-*` Certificate expires in < 7 d, for 15 m | `docs/runbooks/certificate-expiring.md` |

Both need the observability stack (EPIC-07) to scrape Traefik's metrics entrypoint (`:9100`) and
cert-manager (`:9402`). In the compose stack neither series exists and the rules stay silent; an
absent edge is `AndaraServerUnavailable`'s page, not a second one.

## Excluded from the budget

Nothing. A deploy that drops the endpoint from Traefik's view for 30 seconds is 30 seconds of
`503` to players, and `AW-INF-007`'s pre-stop notice exists so that they know why.

## Error budget exhaustion policy

1. Deploys pause except for fixes to whatever burned it.
2. If the burn is certificate-driven, the CA and issuer path in `deploy/k8s/cert-manager/` is
   reviewed before the next issuance, not after.
3. If the burn is `502` verification failures after a server certificate renewal, the server's
   load-once TLS (`AW-SRV-005`) is the cause and the hot-reload follow-up is scheduled ahead of
   feature work.

## What would change this document

- Traefik labelling gRPC responses by outcome: the SLI then covers every client and the caveat
  above is deleted.
- `AW-SRV-011`'s `session-availability.md`: once Session-seconds are measured, edge availability
  becomes an input to that SLO rather than a player-facing target of its own.
