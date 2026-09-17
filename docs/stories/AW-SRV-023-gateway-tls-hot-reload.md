---
id: AW-SRV-023
title: Gateway TLS hot-reload — serve a renewed certificate without a restart
epic: EPIC-03
component: server
type: feature
status: ready
size: S
depends_on: [AW-SRV-005]
blocks: []
lane: implementation
risk: low
---

## Context

`AW-SRV-005` loads the server certificate once, into a static `tls.Config.Certificates` slice, and never
looks at the mount again. `AW-INF-006` put cert-manager underneath it: `andara-server-tls` is renewed
ten days before it expires, the kubelet swaps the mounted files within a minute, and the running pod
keeps presenting the certificate it loaded at start. Traefik verifies that old certificate until its
`NotAfter`, and then every request through the edge is a `502`. Today the answer is a pod restart
inside the ten-day window (`docs/runbooks/certificate-expiring.md`), and `CertificateExpiringSoon`
watches cert-manager's view of the Certificate — not what the pod actually serves.

This story closes both gaps: the gateway serves whatever is on the mount, and it says what it is
serving. Constrained by `AW-SRV-005`'s "no plaintext mode" — a reload that fails keeps the last good
pair; it never falls through to nothing.

## User story

As an operator, I want a renewed server certificate to be served without a restart, so that a
certificate renewal is a background event and not a deploy I have to remember.

## Scope

### In scope
- `tls.Config.GetCertificate` backed by a reloading pair, replacing the static slice.
- Reload trigger: **poll the mount directory** every `grpc.tls_reload_interval` (default `30s`) and
  reload when the certificate file's content hash changes. Polling rather than fsnotify because the
  kubelet updates a Secret mount by swapping the `..data` symlink — a watch on the file path misses it,
  and a watch on the directory fires on every kubelet tick.
- Load `tls.crt` and `tls.key` as an atomic pair; a pair that does not parse or does not match keeps
  the previous pair and logs at `error`.
- A gauge for the served certificate's `NotAfter`, so the alert can watch what the pod presents.
- Update `docs/runbooks/certificate-expiring.md` (the restart step goes away) and `AW-INF-006`'s
  `[FOLLOW-UP]` line to name this story.

### Out of scope
- Rotating the *CA* (`andara-ca`, ten years, `rotationPolicy: Never`) — a planned procedure, not a
  reload; `AW-INF-006`'s runbook names it.
- Client-side trust reload in `andara-cli`. The CLI is a short-lived process; it reads the bundle at
  start (`AW-CLI-001`).
- Mutual TLS. Nothing in Phase 1 authenticates a client by certificate (ADR-0006).

## Acceptance criteria

1. **Given** a running server **when** `tls.crt` and `tls.key` on the mount are replaced by a valid pair
   **then** within `grpc.tls_reload_interval` + 1 s a new TLS handshake presents the new certificate,
   `andara_tls_certificate_not_after_timestamp_seconds` equals the new certificate's `NotAfter`, and a
   log line at `info` names the new serial and `not_after`.
2. **Given** a `Subscribe` stream open before the replacement **when** the reload happens **then** the
   stream is still open afterwards and its next message is delivered (no connection is closed by the
   reload).
3. **Given** the mount is replaced by a certificate whose key does not match, or by an unparseable
   file **when** the poll runs **then** the previous pair is still served, one log line at `error` names
   the file and the reason, `andara_tls_reload_total{outcome="failed"}` increments, and the gauge is
   unchanged.
4. **Given** the mount is unchanged **when** the poll runs **then** nothing is logged and
   `andara_tls_reload_total` does not move — a quiet mount is quiet.
5. **Given** the server starts with a missing or invalid pair **when** it boots **then** it exits as
   `AW-SRV-005` specifies today (fatal configuration error, exit `1`); the reloader never relaxes the
   start-time requirement.
6. **Given** the chart from `AW-INF-006` on kind **when** `andara-server` is renewed by setting the
   `Issuing` condition (the same patch `make stream-soak` uses on the edge) **then** within 90 s the
   gauge read off `:8080/metrics` changes to the new `NotAfter`, a Traefik-fronted `OpenSession` still
   succeeds (the edge verifies the new certificate against the same CA), and a stream opened before the
   renewal is still open — `AW-INF-006` AC-3 without its "edge only" scope.

## Interface contract

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `grpc.tls_reload_interval` | `ANDARA_TLS_RELOAD_INTERVAL` | `30s` | `0` disables reloading (tests, `make up` with a static pair); minimum `1s` otherwise |

Registered in `deploy/helm/andara/keys.yaml` in the same change, so the chart can set it.

### Gateway

```go
// CONTRACT SKETCH — not an implementation
// Options gains ReloadInterval time.Duration (0 = load once, as today).
// The tls.Config uses GetCertificate; Certificates is no longer set.
type certPair struct{ cert *tls.Certificate; notAfter time.Time; serial string; hash [32]byte }
// reloader: ticks every ReloadInterval; reads cert then key; sha256(cert) unchanged → return;
// tls.X509KeyPair fails → keep previous, log error, count failed; else swap atomically (atomic.Pointer).
```

`Server.Close` stops the ticker before draining. The reloader never holds a lock across a handshake.

### Error taxonomy

| Condition | Effect |
|-----------|--------|
| file unreadable | keep previous; `error` log `tls reload failed` with `path`, `err`; `outcome="failed"` |
| pair does not parse or mismatches | same, with `reason` |
| `ReloadInterval < 1s && != 0` | start-time configuration error, exit `1` |

## Data / state impact

None. No wire change, no schema change, no session state touched: an in-flight TLS session keeps the
key material it negotiated; only new handshakes see the new certificate.

## Observability requirements

- **Metrics:** `andara_tls_certificate_not_after_timestamp_seconds` (gauge, no labels — one served
  certificate per process); `andara_tls_reload_total{outcome="reloaded"|"failed"}` (counter, both
  outcomes pre-seeded so a rate over `failed` is zero, not absent).
- **Logs:** `tls certificate loaded` at `info` with `serial`, `not_after`, `dns_names` at start and on
  every successful reload; `tls reload failed` at `error` with `path`, `reason`. No request path is
  involved, so no correlation ID.
- **Traces:** none — the reload is not a request.
- **Alerts:** none new. `CertificateExpiringSoon` (`AW-INF-006`) gains a second clause in the same
  change: `andara_tls_certificate_not_after_timestamp_seconds - time() < 7d` — the served view — with
  the runbook's restart step replaced by "the reload failed; read the `tls reload failed` line".

## Test plan

- **Unit:** reload on content change; unchanged content is a no-op (AC-4); mismatched pair and
  unparseable file keep the previous pair (AC-3); the symlink-swap shape the kubelet produces
  (`..data` → new directory) is detected by the poll; `ReloadInterval: 0` never starts the ticker;
  `Close` stops it.
- **Integration (`server/gateway`):** open a stream, swap the pair on disk, assert the stream survives
  and a new dial sees the new serial (AC-1, AC-2); an in-process registry shows the gauge moving.
- **Integration (kind, `kind` workflow):** AC-6, appended to the `stream-soak` step of `AW-INF-006`'s
  workflow: renew `andara-server` at +60 s, poll the gauge off the pod, assert the soak stream lives.
- **Manual/operator:**
  ```
  make helm-install ENV=local
  kubectl -n andara-local patch certificate andara-server --subresource=status --type=merge \
    -p '{"status":{"conditions":[{"type":"Issuing","status":"True","reason":"ManuallyTriggered","message":"manual","lastTransitionTime":"<now>"}]}}'
  kubectl -n andara-local exec andara-0 -c server -- wget -qO- http://127.0.0.1:8080/metrics | grep andara_tls_
  # expect: not_after moved within 90 s, reload_total{outcome="reloaded"} = 1
  kubectl -n andara-local logs andara-0 -c server | grep 'tls certificate loaded'
  ```

## Definition of done

CLAUDE.md §8, plus: `docs/runbooks/certificate-expiring.md` no longer contains a restart step;
`AW-INF-006` AC-3's "edge only" scope note is removed and its `[FOLLOW-UP]` names this story.

## Open questions

- `[ASSUMPTION]` 30 s polling. cert-manager renews ten days early; the kubelet syncs a Secret mount
  within its sync period (about a minute). Anything under a few minutes is invisible to the outcome;
  30 s keeps AC-6 fast in CI.
- `[ASSUMPTION]` Hash the certificate file, not its mtime. A `..data` symlink swap does not change the
  mtime of the path the server opened; content is the only honest signal.
