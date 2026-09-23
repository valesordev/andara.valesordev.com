# IngressErrorRateHigh

**Alert:** 5xx ratio on `traefik_router_requests_total{router=~"andara-.*"}` > 1% for 10 m, per
environment — the alert's `namespace` label is taken from the router name, since Traefik's series
carry Traefik's own namespace (`AW-INF-008`).
**Severity:** page. **SLO:** `docs/specs/slo/edge-availability.md`.
**Ships with:** `AW-INF-006`.

## What fired, and what the player is experiencing

Traefik is answering Andara requests with 5xx it generated itself: it could not forward them. Players
on Connect or gRPC-Web clients get an HTTP error instead of a Session; players on gRPC (`andara-cli`,
the Go clients) get `UNAVAILABLE` — those are *not* in this ratio (Traefik 3.7 labels every gRPC
response `code="2"`), so the real failure rate is at least what the alert shows, and usually more.

Open streams are usually not affected: this is about new requests reaching the pod, and a stream
that was already forwarded keeps flowing until its own connection ends.

## How to confirm

```
kubectl -n andara-<env> get ingress,serverstransport,middleware     # the three the edge is made of
kubectl -n andara-<env> get endpointslice -l kubernetes.io/service-name=andara \
  -o jsonpath='{.items[*].endpoints[*].conditions.ready}'          # true, or the pod is not ready
kubectl -n traefik logs deploy/traefik --since=10m | grep '"andara' | tail -20
```

The access log line's `DownstreamStatus` and the error message tell them apart:

| Status | Log says | Meaning |
|--------|----------|---------|
| `503` | `no available server` | no ready endpoint behind the Service |
| `502` | `tls: failed to verify certificate` / `x509` | Traefik could not verify the pod's certificate |
| `502` | `connection refused` | endpoint listed but nothing listening on 8443 |
| `504` | `context deadline exceeded` | the pod accepted the connection and never answered the headers |
| `404` | — | not this alert; a Host the routers do not match — check `host` in the values file |

## How to mitigate

| Symptom | Action |
|---------|--------|
| no ready endpoint, pod absent or not ready | this is `AndaraServerUnavailable`; follow `server-unavailable.md` — the edge is reporting the server honestly |
| no ready endpoint, pod `1/1 Running` | the endpoint slice disagrees with the pod: `kubectl -n andara-<env> rollout restart statefulset/andara` |
| certificate verification failed | the server certificate expired while the pod kept serving it (see `certificate-expiring.md`), or `ServersTransport.serverName` no longer matches the certificate's SANs — `kubectl -n andara-<env> get secret andara-server-tls -o jsonpath='{.data.tls\.crt}' \| base64 -d \| openssl x509 -noout -dates -ext subjectAltName`. A restart picks up a renewed certificate |
| connection refused | the server is not listening on 8443: `kubectl -n andara-<env> logs andara-0 -c server \| grep listen` — `grpc.listen` was changed, or the process is mid-restart |
| `504` | the pod is wedged (`livez` will restart it) or `forwardingTimeouts.dialTimeout` (5 s) is too short for the node — the former is `server-crashlooping.md` territory |

## How to diagnose

1. `kubectl -n traefik logs deploy/traefik --since=10m | grep -v '"DownstreamStatus":2'` — every
   non-2xx with `RouterName`, `ServiceName`, and the error.
2. `kubectl -n andara-<env> describe serverstransport andara` and the Secret it names in `rootCAs` —
   `ca.crt` must be the CA that signed the pod's `tls.crt`.
3. Whether the request even reached the pod: `andara_grpc_requests_total` on the pod's `:8080/metrics`
   moves for forwarded requests and not for edge-generated errors.
4. `kubectl -n andara-<env> get networkpolicy andara -o yaml` — `ingressNamespace` must be the
   namespace Traefik actually runs in (`traefik`); a policy that names the wrong one is a `503` from
   Traefik and silence from the pod.
5. Traefik itself: `kubectl -n traefik get pod` — a restarting Traefik is every project on the
   cluster, not only this one.

## When to escalate

- Traefik is unhealthy: the cluster's ingress is shared with six other projects; that is the
  platform, not this chart.
- `502` verification failures with a fresh, valid server certificate and matching SANs: escalate
  with the Traefik error line — the transport, not the certificate, is the question.
