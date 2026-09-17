---
id: AW-INF-006
title: Ingress, certificate management, and gRPC/Connect routing
epic: EPIC-01
component: infra
type: infra
status: review
size: M
depends_on: [AW-INF-003, AW-SRV-005]
blocks: []
lane: architecture
risk: medium
---

## Context

ADR-0003 puts gRPC over HTTP/2 with TLS on the wire and serves Connect and gRPC-Web from the same
handler. That combination is unremarkable to write and easy to get wrong to deploy: HTTP/2 must reach
the pod intact, and an ingress that terminates and re-originates as HTTP/1.1 breaks server streaming in
ways that look like intermittent client bugs.

**Decided 2026-09-10 (Brian):** platform is kind v1.36.1 on Brian's box, where the control plane already
publishes `:80` and `:443` and other projects hold `:3000`, `:8081`, `:9000`; `Admin` stays on the same
listener and is restricted by network policy. The engineering choices that follow are in the contract.

**Corrected 2026-09-14 (implementation start):** the controller that owns `:80`/`:443` on that cluster is
**Traefik v3.7** (chart 41.4.0, the default `IngressClass`, serving six other projects), with
**cert-manager v1.21.1** already installed. ingress-nginx — the `[ASSUMPTION]` this story was groomed on —
cannot be installed there without displacing what the port mapping was made for. The contract below is
Traefik's; the shape (two Ingresses on one host, TLS re-originated as HTTP/2, private CA, allowlist at L7,
NetworkPolicy at L3) is unchanged. Each corrected line is marked.

## User story

As a player, I want to connect to Andara over the public internet with the same protocol I use locally,
so that what works in development works in production.

## Scope

### In scope
- The cluster's Traefik as the controller (*corrected: was ingress-nginx*), TLS terminated at the edge with
  cert-manager, re-originated to the pod as HTTPS through a `ServersTransport` so HTTP/2 is end to end.
- Two `Ingress` resources on one host: `/` for `Game` and `Auth`; `/andara.admin.v1.Admin/` behind an
  `ipAllowList` `Middleware` for operators (*corrected: was `whitelist-source-range`*). The pod keeps one
  listener; the path split is how "same listener, restricted by network" is honored for external traffic.
- `NetworkPolicy`: the server pod's gRPC port admits the ingress-controller namespace and the Behavior
  Agent namespace only; nothing else in the cluster reaches it directly.
- cert-manager `ClusterIssuer`: a private CA (`andara-ca`) for `local`/`dev`/box-prod, with an ACME
  issuer selectable by values for a public domain. The pod's own cert comes from the same issuer.
- Stream lifetime: no ingress-initiated close on an idle server stream for at least an hour, proven by
  soak (*corrected: Traefik has no per-Ingress read/send timeout to set; its entrypoint defaults do not
  reap an active stream, and the 60 m soak is the evidence*).
- Edge certificate rotation with no dropped streams (*scoped: the pod's own certificate is loaded once,
  see Open questions*).

### Out of scope
- The workload — `AW-INF-003`. Client-side trust locally — `AW-INF-002` (`ANDARA_TLS_CA_FILE`).
- Edge rate limiting. Per-Session limits live in `AW-SRV-010`.

## Acceptance criteria

1. **Given** a client outside the cluster **when** it opens `Subscribe` **then** the stream stays open
   for 60 minutes with no ingress-initiated close, and `traefik_router_requests_total{protocol="grpc"}`
   on this release's routers is non-zero — gRPC cannot reach the pod over anything but HTTP/2, so that
   counter is HTTP/2 on both legs (*corrected: was `nginx_ingress_controller_requests`*).
2. **Given** a Connect client and a gRPC-Web client **when** both call `OpenSession` on the same host
   **then** both succeed.
3. **Given** the edge certificate within `renewBefore` **when** cert-manager renews it **then** an open
   stream from before renewal is still open after, and new connections present the new cert (*scoped to
   the edge certificate; the server certificate's renewal needs a pod restart — Open questions*).
4. **Given** a request to `/andara.admin.v1.Admin/GetServerInfo` from outside the operator CIDRs
   **when** it arrives **then** the edge returns `403` and the server's `andara_grpc_requests_total` for
   the Admin service does not move.
5. **Given** any rendered manifest **when** grepped **then** no private key appears; keys exist only in
   cert-manager-managed Secrets.
6. **Given** a pod in another namespace **when** it dials the server pod's gRPC port directly **then**
   the connection is refused by NetworkPolicy.
7. **Given** `make k8s-dry ENV=prod` **when** it runs **then** the Ingresses, Certificates,
   ServersTransport, Middleware, and NetworkPolicy render and validate against v1.36.1 with the
   cert-manager and Traefik CRD schemas, and the ClusterIssuer manifest validates alongside.
8. **Given** the host port is already held (`:443` by the kind control plane) **when** the chart is
   installed **then** it uses the kind-mapped controller rather than binding a host port itself, and the
   `local` values document the mapped ports.

## Interface contract

### Ingress (both resources) — *corrected 2026-09-14: Traefik, not ingress-nginx*

| Field / annotation | Value | Why |
|--------------------|-------|-----|
| `ingressClassName` | `values.ingress.className` (`traefik`) | the cluster's default class |
| `traefik.ingress.kubernetes.io/router.entrypoints` | `values.ingress.entrypoint` (`websecure`) | the cluster's `:443` |
| `traefik.ingress.kubernetes.io/router.tls` | `"true"` | |
| `spec.tls[0].secretName` | `andara-edge-tls` | the edge certificate, from cert-manager |
| `traefik.ingress.kubernetes.io/router.middlewares` | `<ns>-andara-admin-allowlist@kubernetescrd` | Admin resource only |

Router precedence is rule length: `Host && PathPrefix(/andara.admin.v1.Admin/)` outranks `Host && PathPrefix(/)`.

### Backend leg

| Object | Field | Value | Why |
|--------|-------|-------|-----|
| `Service andara` (headless) | `traefik.ingress.kubernetes.io/service.serversscheme` | `https` | the pod terminates its own TLS; HTTP/2 negotiated by ALPN |
| `Service andara` | `traefik.ingress.kubernetes.io/service.serverstransport` | `<ns>-andara@kubernetescrd` | |
| `ServersTransport andara` | `serverName` | `andara.<ns>.svc` | Traefik dials the pod IP, which is in no SAN |
| `ServersTransport andara` | `rootCAs[0].secret` | `secrets.tlsCert.secretName` | cert-manager writes the issuing CA to `ca.crt` there; no bundle to bootstrap |
| `ServersTransport andara` | `forwardingTimeouts.dialTimeout` / `responseHeaderTimeout` | `5s` / `0s` | a stream may be silent for hours |
| `Middleware andara-admin-allowlist` | `ipAllowList.sourceRange` | `values.admin.allowedCIDRs` | 403 before proxying; the source IP is the connection peer (Traefik owns the host port; no forwarder to trust) |

Timeouts (*corrected*): Traefik has no per-Ingress read/send timeout. Its entrypoint `readTimeout` (60 s,
cluster-level, outside this repo) stops when the client half-closes the request, which every RPC in
`andara.game.v1` does — see Open questions for the bidi case. Nothing else on the path closes an active
stream; AC-1 is the proof, not a setting.

### Certificates

| Certificate | Issuer | SANs | Secret | Consumer |
|-------------|--------|------|--------|----------|
| `andara-edge` | `values.tls.issuer` | `values.host` | `andara-edge-tls` | Traefik (Ingress `tls`) |
| `andara-server` | `values.tls.privateIssuer` (`andara-ca`) | `andara-0.andara.<ns>.svc`, `andara.<ns>.svc` | `values.secrets.tlsCert.secretName` (`andara-server-tls`) | pod (`grpc.tls_cert_file`/`key_file`) and Traefik's `rootCAs` |

`duration: 2160h`, `renewBefore: 240h`, ECDSA P-256, `rotationPolicy: Always`. The `andara-ca` chain
(self-signed ClusterIssuer → CA Certificate in `cert-manager` → `ClusterIssuer andara-ca`, key
`rotationPolicy: Never`) lives in `deploy/k8s/cert-manager/andara-ca.yaml`, applied and waited on by
`make helm-install` before the chart — a ClusterIssuer is cluster-scoped and three releases in one cluster
cannot each own it. `make helm-install` writes the CA to `.local/tls/cluster/<env>/ca.pem` for
`andara-cli` (*corrected: was `.local/tls/ca.pem`, which is the compose stack's CA and must not be
overwritten; the `andara-ca-bundle` ConfigMap is dropped — Traefik reads the CA from the Secret*).

### Values

| Value | Type | Default |
|-------|------|---------|
| `ingress.enabled` | bool | `true` |
| `ingress.className` | string | `traefik` |
| `ingress.entrypoint` | string | `websecure` |
| `host` | hostname | `andara.local` |
| `tls.issuer` | `andara-ca` \| `letsencrypt` \| `letsencrypt-staging` | `andara-ca` (*corrected: the ACME issuers are the cluster's existing ones, not `letsencrypt-prod`*) |
| `tls.privateIssuer` | `andara-ca` | `andara-ca` |
| `tls.duration` / `tls.renewBefore` | hours | `2160h` / `240h` |
| `admin.allowedCIDRs` | list of CIDR | `["10.0.0.0/8","192.168.0.0/16"]`; `local` adds `172.16.0.0/12` — a connection from the box reaches Traefik from the docker bridge |
| `networkPolicy.enabled` | bool | `true` |
| `networkPolicy.ingressNamespace` | string | `traefik` |
| `networkPolicy.agentNamespace` | string | `andara-agents` |
| `secrets.tlsCert.secretName` | string | `andara-server-tls` (now a chart default: the chart issues it) |

`server.grpc.tls_cert_file` / `tls_key_file` move to chart defaults (the mount path is the chart's choice,
and `dev`/`prod` had never set them — the server would have refused to start).

### Make targets

`make helm-install` (from `AW-INF-003`) gains the platform check, the issuer bootstrap, the CA export, and
the `/etc/hosts` hint; `make kind-platform` installs Traefik and cert-manager into a fresh kind cluster
as the box has them (skipping releases that already exist); `make stream-soak ENV=<env> SOAK=<duration>`
runs AC-1 and AC-3 from outside the cluster (*corrected: `SOAK=`, not `DURATION=` — `DURATION` is
`make measure-tick`'s, in seconds*), 5 m on pull requests and 60 m nightly in the `kind` workflow.

## Data / state impact

None. Certificate material is managed by cert-manager Secrets and never appears in a chart value or a
rendered manifest.

## Observability requirements

- **Metrics:** `traefik_router_requests_total{router, code, protocol}` and
  `traefik_service_request_duration_seconds` (*corrected*; router-level, because a request Traefik cannot
  forward never reaches a service counter), stream duration via `andara_session_duration_seconds`
  (`AW-SRV-005`), `certmanager_certificate_expiration_timestamp_seconds{name}`. Cardinality: routers and
  services are per-Ingress, two and one for this chart. Known gap: Traefik 3.7 labels every gRPC-protocol
  response `code="2"`; the 5xx ratio sees Connect and gRPC-Web clients only.
- **Logs:** Traefik's JSON access log (`ClientHost`, `RequestPath`, `DownstreamStatus`, `RouterName`,
  `ServiceName`, `Duration`), retained 7 d by the observability stack (EPIC-07).
- **Alerts:** `CertificateExpiringSoon` (< 7 d for 15 m) and `IngressErrorRateHigh` (5xx ratio > 1% for
  10 m), both tied to `docs/specs/slo/edge-availability.md`, written here (*corrected: the Session
  availability SLO is `AW-SRV-011`'s and does not exist yet; §7 puts the SLO before the alert*); runbooks
  `docs/runbooks/certificate-expiring.md` and `docs/runbooks/ingress-error-rate.md` ship here.

## Test plan

- **Unit (`make helm-test`):** both Ingresses on `host` from `andara-edge-tls`, the Admin one behind the
  Middleware and the Game one not; Service annotated for HTTPS through the ServersTransport whose
  `serverName` is a server-certificate SAN and whose `rootCAs` is that certificate's Secret; both
  Certificates from ClusterIssuers with `renewBefore: 240h`; the pod mounts what the server Certificate
  issues; NetworkPolicy admits `traefik` and `andara-agents` only; no `PRIVATE KEY` in the render (AC-5);
  `ingress.enabled=false` renders none of it; the schema rejects a bad host, CIDR, or issuer.
- **Integration (kind, CI — the `kind` workflow):** a cluster from `deploy/kind/config.yaml` and
  `make kind-platform`; AC-5 on the installed release; AC-2 with a Connect (JSON) and a gRPC-Web `curl`;
  AC-4 by patching the allowlist to exclude the runner and reading the server's Admin counter before and
  after; AC-6 from a pod in another namespace; AC-1 and AC-3 as `make stream-soak` (5 m on PRs, 60 m
  nightly), which renews the edge certificate 30 s in by setting the same `Issuing` condition
  `cmctl renew` sets (no new tool), then asserts the served serial changed and the stream did not.
- **Manual/operator:**
  ```
  make image && make kind-load && make helm-install ENV=local      # prints the /etc/hosts line and the CA path
  grpcurl -cacert .local/tls/cluster/local/ca.pem -authority andara.local 127.0.0.1:443 \
    -d '{"protocol_version":1,"auth_token":"x","client_name":"probe"}' andara.game.v1.Game/OpenSession
                                                                    # expect: a session_id (andara-cli play is AW-CLI-002; same flags, same CA)
  grpcurl -cacert .local/tls/cluster/local/ca.pem -authority andara.local 127.0.0.1:443 \
    andara.admin.v1.Admin/GetServerInfo                              # from the box: Unauthenticated (reached the server)
  kubectl -n andara-local patch middleware andara-admin-allowlist --type merge \
    -p '{"spec":{"ipAllowList":{"sourceRange":["203.0.113.0/24"]}}}'  # then the same call: HTTP 403 from the edge
  kubectl -n andara-local patch middleware andara-admin-allowlist --type merge \
    -p '{"spec":{"ipAllowList":{"sourceRange":["10.0.0.0/8","192.168.0.0/16","172.16.0.0/12"]}}}'
                                                                    # restore it explicitly: helm patches CRs old→new manifest and never reads live state
  make stream-soak ENV=local SOAK=3m                                # AC-1 short, AC-3, gRPC counter
  ```

## Definition of done

CLAUDE.md §8, plus: `make stream-soak` scheduled in CI; both runbooks exist; `docs/specs/slo/edge-availability.md` exists.

## Open questions

- **Resolved 2026-09-10 (Brian): Admin restricted by network, same listener.** Honored at L7 for
  external traffic and at L3 for in-cluster traffic. In-cluster callers that reach the pod (Agents) are
  bounded by role at `authorize`, which is the ADR-0005 containment.
- **Resolved 2026-09-14 (by the cluster): Traefik and cert-manager.** The `[ASSUMPTION]` of ingress-nginx
  was false on the decided platform; Traefik is what the port mapping was made for. Envoy Gateway with TLS
  passthrough remains the alternative if re-origination ever proves to be the streaming problem. Observed
  2026-09-14 on the box: `make stream-soak ENV=local SOAK=60m` held an idle Subscribe for 1h0m0s
  through Traefik with the edge certificate renewed at +30 s (serial changed, stream open) and a `helm
  upgrade` of the release mid-soak; `traefik_router_requests_total{protocol="grpc"}` = 14 on the
  release's routers. AC-1 and AC-3 hold as written.
- `[ASSUMPTION]` Private CA for the box, ACME selectable by values for a public host. `dev` and `prod`
  values name `andara-dev.solo7.valesordev.com` and `andara.solo7.valesordev.com` under the zone the
  cluster's `letsencrypt` issuer already solves for, on `andara-ca` until Brian picks public names —
  `tls.issuer: letsencrypt` is the switch and needs no other change.
- **Server certificate renewal needs a restart.** `andara-server` loads `tls.crt`/`tls.key` once
  (`AW-SRV-005`, a static `Certificates` slice); a renewed `andara-server-tls` is served only after the
  pod restarts, and past the old certificate's expiry Traefik's verification fails (`502`). The window is
  `renewBefore` (10 d), which any deploy closes; `certificate-expiring.md` carries the manual step.
  `[FOLLOW-UP, implementation lane]` hot-reload of TLS material in the gateway (`GetCertificate` over a
  watched mount) — a small `SRV` story, not touched on this branch.
- **Traefik's entrypoint `readTimeout` (60 s) is cluster-level and outside this repo.** It stops when the
  client half-closes the request, which every RPC in `andara.game.v1` does today; a future
  client-streaming or bidi RPC would be reset at 60 s of open request body. Whoever adds one raises the
  entrypoint timeout on the box's Traefik first.
- **`IngressErrorRateHigh` sees Connect and gRPC-Web clients only** until Traefik labels gRPC responses by
  outcome; the gRPC leg is watched from the server side. Named in the SLO rather than papered over.
