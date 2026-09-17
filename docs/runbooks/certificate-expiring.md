# CertificateExpiringSoon

**Alert:** `certmanager_certificate_expiration_timestamp_seconds{name=~"andara-.*"} - time() < 7d` for 15 m.
**Severity:** page. **SLO:** `docs/specs/slo/edge-availability.md` (precondition).
**Ships with:** `AW-INF-006`.

## What fired, and what the player is experiencing

One of the chart's two certificates is inside 7 days of expiry and cert-manager has not replaced
it. `renewBefore` is 10 days, so a healthy issuer renewed it 3 days ago; this is a renewal that
failed, not one that is late.

Nothing is broken for the player *yet*. When the certificate expires:

- `andara-edge` (what Traefik presents on `host`): every new connection fails the TLS handshake.
  Open streams stay open until the client reconnects.
- `andara-server` (what the pod presents to Traefik): Traefik's `ServersTransport` stops verifying
  the pod, and every request becomes a `502` — `IngressErrorRateHigh` fires next.

The clock is the `expiration_timestamp` in the alert. You have days, not minutes; use them.

## How to confirm

```
kubectl -n andara-<env> get certificate                       # READY False, or True with an old NOT AFTER
kubectl -n andara-<env> describe certificate andara-edge       # Events: the issuer's last answer
kubectl -n andara-<env> get certificaterequest                 # a stuck request is the usual shape
kubectl get clusterissuer andara-ca                            # READY True? the CA that signs both
```

If `andara-ca` itself is not Ready: `kubectl -n cert-manager get certificate andara-ca` and its
Secret `andara-ca-tls`. The chain is in `deploy/k8s/cert-manager/andara-ca.yaml`.

For a public host on the ACME issuer (`tls.issuer: letsencrypt`): `kubectl -n andara-<env> get
order,challenge` — a DNS-01 challenge that never became `valid` is a Cloudflare token or zone problem,
which is the cluster's, not the chart's.

## How to mitigate

| Symptom | Action |
|---------|--------|
| `CertificateRequest` stuck, issuer Ready | `kubectl -n andara-<env> delete certificaterequest -l cert-manager.io/certificate-name=<name>` — cert-manager creates a fresh one |
| issuer not Ready | `make helm-install ENV=<env>` re-applies and waits on the CA chain; if it times out, read the CA Certificate's events |
| renewed but the pod still serves the old one | see below — a restart, before the old one expires |
| expired already | `kubectl -n andara-<env> patch certificate <name> --subresource=status --type=merge -p '{"status":{"conditions":[{"type":"Issuing","status":"True","reason":"ManuallyTriggered","message":"runbook","lastTransitionTime":"<now, RFC3339>"}]}}'` forces issuance — the same condition `cmctl renew` sets |

### The server certificate is loaded once

`andara-server` reads `tls.crt`/`tls.key` at start (`AW-SRV-005`) and does not watch the mount.
cert-manager renewing `andara-server-tls` updates the Secret and, within about a minute, the
mounted files — but the running pod keeps presenting the certificate it loaded. Traefik keeps
verifying it until the *old* certificate's `NOT AFTER`, and then every request is a `502`.

So a server certificate renewal is complete only after a pod restart, and the window is the old
certificate's remaining validity: `renewBefore` (10 days). Any deploy in that window does it; if
none is due, roll the pod:

```
kubectl -n andara-<env> rollout restart statefulset/andara    # AW-INF-007's pre-stop snapshot makes this short
```

The edge certificate has no such step: Traefik reloads `andara-edge-tls` live, and `make
stream-soak` proves a renewal mid-stream drops nothing.

`AW-SRV-023` is the hot-reload of TLS material in the gateway; until it lands, this restart is the
procedure and `CertificateExpiringSoon` is what reminds you.

## How to diagnose

1. `kubectl -n andara-<env> describe certificate <name>` — the condition message names the stage.
2. `kubectl -n cert-manager logs deploy/cert-manager --since=1h | grep <name>`.
3. Issuer chain: `kubectl get clusterissuer andara-ca -o yaml` — `ca.secretName` must exist in
   `cert-manager`, and `kubectl -n cert-manager get secret andara-ca-tls` must hold `tls.crt`.
4. If the CA itself is what expired (10-year duration, `rotationPolicy: Never`): every leaf and every
   `andara-cli` trust bundle must be reissued — that is a planned procedure, not this runbook.

## When to escalate

- The CA certificate is unhealthy: stop; a CA rotation changes what every client trusts, and it is
  Brian's call when that happens.
- `IngressErrorRateHigh` is also firing: the expiry already happened; follow that runbook's table
  first — it is the player-facing symptom.
