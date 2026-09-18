---
id: AW-INF-012
title: Ingress forwards the client address to the Gateway
epic: EPIC-01
component: infra
type: bug
status: ready
size: S
depends_on: [AW-INF-006, AW-SRV-025]
blocks: []
lane: architecture
risk: medium
---

## Context

`AW-SRV-025` teaches the Gateway to take the client address from `X-Forwarded-For` when the direct
peer is a trusted proxy. This story is the other half: the chart tells the Gateway which peers to
trust, per environment, and proves on the cluster that a login arriving through Traefik is bucketed
by the player's address and not by Traefik's pod IP. Without it `AW-SRV-025` is a helper nobody calls
with a non-empty list, and the launch-day outage `AW-SRV-008` predicted stands.

Traefik at the edge already sets `X-Forwarded-For` and strips a client's own `X-Forwarded-*` unless
`forwardedHeaders.trustedIPs` says otherwise; the platform values (`deploy/k8s/traefik/values.yaml`)
set no trusted IPs, so the header the Gateway sees is Traefik's alone. The only cluster-side change is
which CIDR the Gateway trusts, and the only risk is trusting too much: a pod CIDR covers every pod,
so a compromised pod could spoof a client address — the `NetworkPolicy` from `AW-INF-006` is what
limits who can reach the Gateway at all, and this story records that dependency.

## User story

As an operator, I want the chart to name the ingress as the only trusted proxy, so that the login
limit counts players and a wrong CIDR is a `make check` failure rather than a discovery.

## Scope

### In scope
- `gateway.trustedProxies` in `values.yaml` and the schema; rendered to `ANDARA_TRUSTED_PROXIES`.
  Default empty; `local.yaml` sets the kind pod CIDR; `dev.yaml` and `prod.yaml` set the cluster's.
- A `helm-test` assertion: when `ingress.enabled` is true, `gateway.trustedProxies` is non-empty —
  an ingress with no trusted proxy is the outage configured on purpose.
- The kind workflow: two logins from the box through Traefik with different usernames and separate
  connections, then a third from a second source address (`docker exec` on the kind node, which
  arrives from a different IP) — `andara_auth_attempts_total{outcome="rate_limited"}` stays at 0
  after the box exceeds `auth.rate_limit` from one address and the node does not. Asserted on the
  server's `/metrics`, the way `AW-INF-006` AC-4 reads its counter.
- `docs/runbooks/login-rate-limited.md`: the symptom (every login `RESOURCE_EXHAUSTED`), the check
  (`remote_addr` on `session opened` lines equal to a pod IP), the fix (the CIDR).

### Out of scope
- The Gateway's header parsing — `AW-SRV-025`.
- Traefik's own configuration. The platform release is Brian's and needs no change for this.
- A rate-limit at the edge (Traefik `RateLimit` middleware). Worth its own story if abuse appears;
  it would be a second, coarser bucket, not a replacement.

## Acceptance criteria

1. **Given** `ingress.enabled: true` and `gateway.trustedProxies: []` **when** `make helm-test` runs
   **then** it fails naming the value.
2. **Given** `ENV=local` **when** the chart renders **then** the server container carries
   `ANDARA_TRUSTED_PROXIES=10.244.0.0/16` and no other environment's CIDR.
3. **Given** the kind cluster **when** the box exceeds `auth.rate_limit` logins through Traefik from
   one address with distinct usernames **then** the next is `RESOURCE_EXHAUSTED`, and a login from the
   kind node's address succeeds immediately after.
4. **Given** the same **when** `session opened` lines are read from the pod **then** `remote_addr` is
   the box's bridge address (`172.19.0.1` on this machine) and never a `10.244.` address.
5. **Given** the runbook **when** an operator follows it against a chart with the CIDR removed
   **then** each step's expected output matches within a minute.

## Interface contract

| Value | Schema | Env | Default | `local` | `dev` / `prod` |
|-------|--------|-----|---------|---------|----------------|
| `gateway.trustedProxies` | `array` of CIDR strings | `ANDARA_TRUSTED_PROXIES` (comma-joined) | `[]` | `[10.244.0.0/16]` | the cluster pod CIDR (`kubectl cluster-info dump \| grep -m1 cluster-cidr`) |

`helm-test` rule: `ingress.enabled && len(gateway.trustedProxies) == 0` is a failure.

## Data / state impact

None.

## Observability requirements

- **Metrics:** none new; `andara_auth_attempts_total{outcome="rate_limited"}` is the SLI the kind
  job reads and the runbook's first check.
- **Logs:** `remote_addr` on `session opened` is the runbook's second check.
- **Traces:** none new.
- **Alerts:** none. A login-failure-rate alert waits on the Session availability SLO (`AW-SRV-011`).

## Test plan

- **Unit:** `helm-test` AC-1, AC-2.
- **Integration (kind, CI):** AC-3, AC-4 as steps in `.github/workflows/kind.yaml`.
- **Manual/operator:** the runbook, AC-5.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-008`'s "For AW-INF-006" note is closed by this story's record.

## Open questions

- `[ASSUMPTION]` The pod CIDR rather than Traefik's pod IPs. Pod IPs change on every Traefik restart;
  the CIDR is stable, and the `NetworkPolicy` already restricts the gRPC port to the ingress and
  agent namespaces. The one thing the wider trust exposes: a Behavior Agent (inside the CIDR, allowed
  by the policy) could present a forged `X-Forwarded-For` and pick its own rate bucket. The address
  gates nothing but that bucket, and Agents authenticate with their own credential kind, so this is
  recorded rather than defended; a `namespaceSelector` cannot express it and Traefik's pod IPs
  would trade it for a rotation problem.
