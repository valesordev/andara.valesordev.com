---
id: AW-INF-006
title: Ingress, certificate management, and gRPC/Connect routing
epic: EPIC-01
component: infra
type: infra
status: ready
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

## User story

As a player, I want to connect to Andara over the public internet with the same protocol I use locally,
so that what works in development works in production.

## Scope

### In scope
- ingress-nginx as the controller (the one the kind port mapping was made for), TLS terminated at the
  ingress with cert-manager, re-originated to the pod as `GRPCS` so HTTP/2 is end to end.
- Two `Ingress` resources on one host: `/` for `Game` and `Auth`; `/andara.admin.v1.Admin/` with
  `whitelist-source-range` for operators. The pod keeps one listener; the path split is how "same
  listener, restricted by network" is honored for external traffic.
- `NetworkPolicy`: the server pod's gRPC port admits the ingress-controller namespace and the Behavior
  Agent namespace only; nothing else in the cluster reaches it directly.
- cert-manager `ClusterIssuer`: a private CA (`andara-ca`) for `local`/`dev`/box-prod, with an ACME
  issuer selectable by values for a public domain. The pod's own cert comes from the same issuer.
- Stream timeouts: 4 h read/send at the ingress; HTTP/2 keepalive aligned with `AW-SRV-005`.
- Certificate rotation with no dropped streams.

### Out of scope
- The workload — `AW-INF-003`. Client-side trust locally — `AW-INF-002` (`ANDARA_TLS_CA_FILE`).
- Edge rate limiting. Per-Session limits live in `AW-SRV-010`.

## Acceptance criteria

1. **Given** a client outside the cluster **when** it opens `Subscribe` **then** the stream stays open
   for 60 minutes with no ingress-initiated close, and `nginx_ingress_controller_requests` shows HTTP/2
   on both legs.
2. **Given** a Connect client and a gRPC-Web client **when** both call `OpenSession` on the same host
   **then** both succeed.
3. **Given** a certificate within `renewBefore` **when** cert-manager renews it **then** an open stream
   from before renewal is still open after, and new connections present the new cert.
4. **Given** a request to `/andara.admin.v1.Admin/GetServerInfo` from outside the operator CIDRs
   **when** it arrives **then** ingress returns `403` and the server logs no request.
5. **Given** any rendered manifest **when** grepped **then** no private key appears; keys exist only in
   cert-manager-managed Secrets.
6. **Given** a pod in another namespace **when** it dials the server pod's gRPC port directly **then**
   the connection is refused by NetworkPolicy.
7. **Given** `make k8s-dry ENV=prod` **when** it runs **then** the Ingress, Certificate, ClusterIssuer,
   and NetworkPolicy render and validate against v1.36.1 with the cert-manager CRD schemas.
8. **Given** the host port is already held (`:443` by the kind control plane) **when** the chart is
   installed **then** it uses the kind-mapped controller rather than binding a host port itself, and the
   `local` values document the mapped ports.

## Interface contract

### Ingress annotations (both resources)

| Annotation | Value | Why |
|------------|-------|-----|
| `nginx.ingress.kubernetes.io/backend-protocol` | `GRPCS` | HTTP/2 to the pod; the pod terminates its own TLS |
| `nginx.ingress.kubernetes.io/proxy-read-timeout` / `proxy-send-timeout` | `14400` | 4 h streams |
| `nginx.ingress.kubernetes.io/proxy-body-size` | `4m` | above `grpc.max_recv_bytes` |
| `cert-manager.io/cluster-issuer` | `values.tls.issuer` | |
| `nginx.ingress.kubernetes.io/whitelist-source-range` | `values.admin.allowedCIDRs` | Admin resource only |
| `nginx.ingress.kubernetes.io/use-regex` | `true` | Admin path prefix |

Ingress class `nginx`; host `values.host` (`andara.local` for `local`, resolved by `/etc/hosts` line
printed by `make helm-install`).

### Certificates

| Certificate | Issuer | SANs | Secret | Consumer |
|-------------|--------|------|--------|----------|
| `andara-edge` | `values.tls.issuer` | `values.host` | `andara-edge-tls` | ingress |
| `andara-server` | `andara-ca` | `andara-0.andara.<ns>.svc`, `andara.<ns>.svc` | `andara-server-tls` | pod (`grpc.tls_cert_file`/`key_file`) |

`renewBefore: 240h` (10 d of a 90 d cert). The CA bundle is published as ConfigMap `andara-ca-bundle`
and `make helm-install` writes it to `.local/tls/ca.pem` for `andara-cli`.

### Values

| Value | Type | Default |
|-------|------|---------|
| `ingress.enabled` | bool | `true` |
| `ingress.className` | string | `nginx` |
| `host` | hostname | `andara.local` |
| `tls.issuer` | `andara-ca` \| `letsencrypt-prod` | `andara-ca` |
| `admin.allowedCIDRs` | list of CIDR | `["10.0.0.0/8","192.168.0.0/16"]` |
| `networkPolicy.agentNamespace` | string | `andara-agents` |

### Make targets

`make helm-install` (from `AW-INF-003`) gains the issuer bootstrap and the `/etc/hosts` hint;
`make stream-soak ENV=<env> DURATION=60m` runs AC-1 from outside the cluster and is scheduled nightly
in CI.

## Data / state impact

None. Certificate material is managed by cert-manager Secrets and never appears in a chart value or a
rendered manifest.

## Observability requirements

- **Metrics:** `nginx_ingress_controller_requests{host, path, status}`,
  `nginx_ingress_controller_request_duration_seconds`, stream duration via
  `andara_session_duration_seconds` (`AW-SRV-005`), `certmanager_certificate_expiration_timestamp_seconds`.
- **Logs:** controller access logs with `upstream_status` and `request_time`, retained 7 d.
- **Alerts:** `CertificateExpiringSoon` (< 7 d) and `IngressErrorRateHigh` (5xx ratio > 1% for 10 m),
  both tied to the Session availability SLO from `AW-SRV-011`; runbooks
  `docs/runbooks/certificate-expiring.md` and `docs/runbooks/ingress-error-rate.md` ship here.

## Test plan

- **Integration (kind, CI):** AC-1 as `make stream-soak` (nightly, 60 m; 5 m on PRs); AC-2 with the
  `gen/ts` Connect client and a gRPC-Web probe; AC-3 by forcing `cmctl renew` mid-stream; AC-4 and AC-6
  with `curl` from an out-of-range pod; AC-5 as a `helm unittest` grep.
- **Manual/operator:**
  ```
  make helm-install ENV=local
  andara-cli --server andara.local:443 --tls-ca .local/tls/ca.pem play   # expect: session opens
  curl -sk https://andara.local/andara.admin.v1.Admin/GetServerInfo      # from outside CIDR: 403
  ```

## Definition of done

CLAUDE.md §8, plus: `make stream-soak` scheduled in CI; both runbooks exist.

## Open questions

- **Resolved 2026-09-10 (Brian): Admin restricted by network, same listener.** Honored at L7 for
  external traffic and at L3 for in-cluster traffic. In-cluster callers that reach the pod (Agents) are
  bounded by role at `authorize`, which is the ADR-0005 containment.
- `[ASSUMPTION]` ingress-nginx and cert-manager. Both are what a kind cluster with `:80`/`:443` mapped
  was prepared for, and both are boring. Envoy Gateway with TLS passthrough is the alternative if
  re-origination ever proves to be the streaming problem; AC-1 would catch that.
- `[ASSUMPTION]` Private CA for the box, ACME selectable by values for a public host.
