---
id: AW-INF-006
title: Ingress, certificate management, and gRPC/Connect routing
epic: EPIC-01
component: infra
type: infra
status: draft
size: M
depends_on: [AW-INF-003, AW-SRV-005]
blocks: []
lane: architecture
risk: medium
---

> `status: draft` — scoped and unblocked. Groomed once the target platform is known; ingress is the
> most platform-dependent story in the repo.

## Context

ADR-0003 puts gRPC over HTTP/2 with TLS on the wire and serves Connect and gRPC-Web from the same
handler. That combination is unremarkable to write and easy to get wrong to deploy: HTTP/2 must reach
the pod intact, and an ingress that terminates and re-originates as HTTP/1.1 breaks server streaming in
ways that look like intermittent client bugs.

## User story

As a player, I want to connect to Andara over the public internet with the same protocol I use locally,
so that what works in development works in production.

## Scope

### In scope
- Service and ingress for the single TLS endpoint serving `Game` and `Admin`.
- End-to-end HTTP/2 to the pod, or TLS passthrough — whichever the platform supports without breaking
  streaming.
- Certificate issuance and rotation.
- Long-lived stream handling: idle and total-duration timeouts long enough for a play session, not the
  60-second default that would disconnect every player every minute.
- Network-level restriction of `Admin` reachability. **Decided 2026-09-10 (Brian): same listener,
  restricted by network policy.** A NetworkPolicy admits `Admin` only from the operator network, so
  authorization is not the only thing between the internet and a privileged RPC. ADR-0003's "same
  endpoint, same protocol" is unaffected — this restricts reachability, not the protocol.

### Out of scope
- The workload itself — `AW-INF-003`.
- Client-side TLS trust for `andara-cli` locally — `AW-INF-002`.
- Rate limiting at the edge. Per-Session limits live in `AW-SRV-010`, where the Session is known.

## Acceptance criteria (known now; completed at grooming)

1. **Given** a deployed environment **when** a gRPC client connects from outside the cluster **then**
   the connection is HTTP/2 end to end and a server stream stays open for at least one hour without an
   ingress-initiated close.
2. **Given** the same environment **when** a Connect client and a gRPC-Web client connect **then** both
   succeed against the same endpoint.
3. **Given** a certificate approaching expiry **when** rotation runs **then** it completes with no
   dropped streams.
4. **Given** a request to an `Admin` method from outside the permitted network range **when** it arrives
   **then** it is rejected at the network layer, before reaching the server.
5. **Given** any rendered manifest **when** inspected **then** no certificate private key is present in
   it.

## Interface contract

To be written at grooming, once the platform is known.

## Data / state impact

None. Certificate material is managed by the platform's secret mechanism and never appears in a chart
value or a rendered manifest.

## Observability requirements

- **Metrics:** ingress request rate, status distribution, and stream duration by route; certificate
  expiry as a gauge, so `CertificateExpiringSoon` is a symptom alert with a runbook.
- **Alerts:** `CertificateExpiringSoon` and `IngressErrorRateHigh`, both tied to the Session
  availability SLO from `AW-SRV-011`.

## Test plan

An integration test from outside the cluster asserting a one-hour stream, and a rotation rehearsal
asserting no dropped streams.

## Definition of done

CLAUDE.md §8, plus: a one-hour stream test that runs in CI or on a schedule, because this is the failure
mode most likely to be introduced by an unrelated platform change.

## Open questions

- `[NEEDS BRIAN]` The ingress controller and certificate tooling, which follow from the platform choice
  in `AW-INF-003`.
- `[NEEDS BRIAN]` Whether `Admin` should be network-restricted in production. ADR-0003 keeps it on the
  same protocol and endpoint; restricting reachability is compatible and probably wanted.
