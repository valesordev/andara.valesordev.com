---
id: AW-SRV-025
title: Client address behind the ingress — trusted proxies and the forwarded peer
epic: EPIC-08
component: server
type: bug
status: ready
size: S
depends_on: [AW-SRV-008]
blocks: [AW-INF-012]
lane: implementation
risk: medium
---

## Context

`AW-SRV-008` keys the per-peer half of `auth.rate_limit` on the transport address, and its open
questions handed `AW-INF-006` the consequence: behind an ingress every player arrives from the proxy's
address, and `10/m` becomes ten logins a minute for the whole game. `AW-INF-006` closed on 2026-09-17
without picking that note up — the Gateway still reads `req.Peer().Addr` raw, the chart forwards
nothing, and on the cluster the peer the Gateway sees is Traefik's pod IP. That is the launch-day
outage the note predicted, found on the 2026-09-18 review pass rather than by players.

The review also found the bucket vacuous even without a proxy: `req.Peer().Addr` is `ip:port`, so
every TCP connection is its own bucket and `andara-cli auth login`, one process per attempt, never
shares one. The username bucket has been carrying the whole limit. This story makes the peer the
**client address** — the IP without the port, taken from the forwarded header only when the direct
peer is a configured trusted proxy — so both defects close on one seam.

## User story

As an operator, I want the per-peer login limit to count per player address behind the ingress, so
that one abusive client is throttled and everyone else is not.

## Scope

### In scope
- `gateway.trusted_proxies`: CIDRs whose forwarded headers are believed. Empty (the default) trusts
  no one: the direct peer's IP is the client address.
- Client-address resolution in one Gateway helper used by `Auth` (rate limiting), `OpenSession`
  (`remote_addr` on the Session and its log line), and the audit records `AW-SRV-008` writes.
- `X-Forwarded-For`: the **last** address not in `trusted_proxies`, walking right to left — the
  only entry a trusted proxy is known to have set. `X-Real-Ip` is not read.
- The peer bucket keyed on the client IP, never `ip:port`.

### Out of scope
- Making Traefik forward the header and setting the CIDRs per environment — `AW-INF-012`, which
  verifies this story on the cluster.
- PROXY protocol. Traefik terminates TLS here, so the header path is available and cheaper; PROXY
  protocol is the fallback if TLS ever passes through, and would be its own story.
- The `ingress.rate_limit` per-Session limits in `AW-SRV-010`; those key on Session, not peer.

## Acceptance criteria

1. **Given** `gateway.trusted_proxies` empty and a request carrying `X-Forwarded-For: 203.0.113.9`
   **when** `Authenticate` is called **then** the peer bucket key is the direct peer's IP and the
   header is ignored; a `debug` line notes the untrusted header at most once a minute — not per
   request, and not per peer, which would be an unbounded set held in memory.
2. **Given** `gateway.trusted_proxies: [10.244.0.0/16]`, a direct peer `10.244.1.7`, and
   `X-Forwarded-For: 203.0.113.9, 10.244.1.7` **when** `Authenticate` is called **then** the bucket
   key is `203.0.113.9`, and the same request from direct peer `192.168.1.5` keys on `192.168.1.5`.
3. **Given** a trusted direct peer and `X-Forwarded-For: 198.51.100.1, 203.0.113.9` (a client that
   sent its own header, which the proxy appended to) **when** resolved **then** the client address is
   `203.0.113.9` — the rightmost untrusted entry — and `198.51.100.1` is never a bucket key.
4. **Given** eleven `Authenticate` attempts in a minute from one client IP over eleven separate TCP
   connections, each with a different username **when** the eleventh arrives **then** it is
   `RESOURCE_EXHAUSTED` — the bucket is the IP, not the connection.
5. **Given** a session opened through a trusted proxy **when** its `session opened` log line and
   `OpenSession` audit record are inspected **then** `remote_addr` is the client IP, and the
   proxy's address appears in neither.
6. **Given** a malformed `X-Forwarded-For` (empty entry, not an IP) from a trusted peer **when**
   resolved **then** the direct peer's IP is used and `andara_gateway_forwarded_header_invalid_total`
   increments; the request is not refused — a broken header is the proxy's bug, not the player's.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation; server/gateway
// ClientAddr returns the caller's IP: the rightmost X-Forwarded-For entry outside
// trusted, when the direct peer is inside trusted; else the direct peer's IP.
func ClientAddr(peer string, header http.Header, trusted []netip.Prefix) (netip.Addr, error)
// auth.Peer becomes ClientAddr(...).String() at every call site; never host:port.
```

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `gateway.trusted_proxies` | `ANDARA_TRUSTED_PROXIES` | `` | comma-separated CIDRs; empty trusts none |

Boot refuses a value that is not a CIDR list, exit 1 naming the entry.

### Error taxonomy

None new. A bad header degrades to the direct peer (AC-6); a bad config refuses boot.

## Data / state impact

None. `remote_addr` on audit records changes meaning from proxy to client address, which is the
meaning `AW-SRV-008` intended; no record is rewritten.

## Observability requirements

- **Metrics:** `andara_gateway_forwarded_header_invalid_total` (counter, no labels). The existing
  `andara_auth_attempts_total{outcome="rate_limited"}` is the symptom to watch after `AW-INF-012`
  rolls out: a jump means the CIDRs are wrong and everyone shares one bucket.
- **Logs:** `debug` at most once a minute for untrusted peers presenting the header (no per-peer
  state); `remote_addr` on `session opened`
  and audit lines is the client IP. No header value is logged at `info` or above.
- **Traces:** `net.peer.ip` attribute on `session.authenticate` is the client IP.
- **Alerts:** none.

## Test plan

- **Unit:** `ClientAddr` table — AC-1, AC-2, AC-3, AC-6, IPv6 peers, `[::1]:port` parsing, a
  trusted list that covers the whole chain (falls back to the leftmost entry rather than nothing).
- **Integration:** AC-4 and AC-5 against the in-process Gateway with a test proxy prefix.
- **Manual/operator:** on the compose stack (no proxy), `for i in $(seq 11); do andara-cli auth login
  --username u$i ...; done` — the eleventh is `RESOURCE_EXHAUSTED`. On the cluster, `AW-INF-012`.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-008`'s open question "For AW-INF-006" is recorded as answered by this
story and `AW-INF-012`.

## Open questions

- `[ASSUMPTION]` Rightmost-untrusted rather than leftmost: leftmost is client-controlled whenever
  the client sends its own header, and Traefik appends rather than replaces. This is the only
  reading that is safe with an appending proxy.
- `[ASSUMPTION]` IPv6 clients bucket per address, not per /64. A /64 bucket is fairer against a
  single host rotating addresses and unfairer to a household behind one; revisit with real traffic.
