# andara-server

The world simulation, session gateway, and persistence adapters. Go, deployed to Kubernetes.

`server/sim` is the load-bearing constraint of this codebase. It imports no network, no datastore,
no filesystem, no wall clock, and no global randomness. This is enforced by `depguard` in
`.golangci.yml`, not by convention — see `ADR-0001` for why the seam exists and `ADR-0002` for why
determinism is a production dependency rather than a preference.

```
server/sim/        simulation core — types, BuildWorld, PartitionFor, CanonicalBytes
server/content/    ZoneDefinition source adapters (dir now; Kafka in AW-SRV-012)
server/gateway/    the Protocol endpoint — TLS, Sessions, version negotiation, interceptors, drain
server/canonical/  deterministic protobuf encoding for anything that feeds the State Hash
server/boot/       load orchestration, /livez /readyz /metrics
server/config/     flag > env > file > default
server/telemetry/  JSON logs, Prometheus registry, boot traces
```

## Configuration

Every key is settable by YAML config file (`--config` / `ANDARA_CONFIG`), by environment
variable, and (where it is a process flag) by flag. Precedence is **flag > env > file > default**.

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `content.source` | `ANDARA_CONTENT_SOURCE` | `kafka` | `kafka` or `dir`. Kafka is AW-SRV-012; until then the process exits 1 naming the source. Local compose uses `dir`. |
| `content.path` | `ANDARA_CONTENT_PATH` | `./content` | Directory of Zone Definition JSON files. Used only when `content.source=dir`. |
| `content.strict_orphans` | `ANDARA_STRICT_ORPHANS` | `false` | When `true`, Rooms with no inbound Exit in their Zone are errors. |
| `http.port` | `ANDARA_HTTP_PORT` | `8080` | `/livez`, `/readyz`, `/metrics`. Plaintext, operator surface. |
| `telemetry.otlp_endpoint` | `ANDARA_OTLP_ENDPOINT` | `localhost:4317` | Empty disables export. |
| `telemetry.service_name` | `ANDARA_SERVICE_NAME` | `andara-server` | |
| `telemetry.environment` | `ANDARA_ENV` | `local` | |
| `telemetry.log_format` | `ANDARA_LOG_FORMAT` | `json` | |
| `telemetry.log_level` | `ANDARA_LOG_LEVEL` | `info` | |
| `grpc.listen` | `ANDARA_GRPC_LISTEN` | `:8443` | The Protocol endpoint: `Game` and `Admin`, gRPC, gRPC-Web, and Connect, TLS only. Flag `--grpc-listen`. |
| `grpc.tls_cert_file` | `ANDARA_TLS_CERT_FILE` | — | **Required.** PEM server certificate. Flag `--tls-cert-file`. |
| `grpc.tls_key_file` | `ANDARA_TLS_KEY_FILE` | — | **Required.** PEM private key. Flag `--tls-key-file`. |
| `grpc.max_recv_bytes` | `ANDARA_GRPC_MAX_RECV_BYTES` | `65536` | Largest request message accepted; over it is `RESOURCE_EXHAUSTED` before any handler runs. |
| `grpc.max_request_timeout` | `ANDARA_GRPC_MAX_REQUEST_TIMEOUT` | `30s` | Deadline applied to a unary RPC that carries none, and the cap on one that carries a longer one. Streams are bounded by the Session, not by this. |
| `grpc.drain_timeout` | `ANDARA_GRPC_DRAIN_TIMEOUT` | `15s` | How long shutdown waits for in-flight RPCs after refusing new ones. Past it, remaining connections are closed. |
| `protocol.min_version` | `ANDARA_PROTOCOL_MIN` | `1` | Lowest Protocol version `OpenSession` accepts. Must be at least 1: `0` is what proto3 sends for an unset field. |
| `protocol.max_version` | `ANDARA_PROTOCOL_MAX` | `1` | Highest Protocol version accepted. |

Starting without TLS material is a fatal configuration error (exit 1). There is no plaintext
mode and no flag to create one; `make up` provisions certificates so nobody needs one
(ADR-0003). `--validate-only` never listens and is exempt.

`--validate-only` loads and validates, prints every finding, and exits without serving. Exit `0`
on a valid World, `1` on any fatal finding.

Dir-mode files are protobuf JSON (`formatVersion`, one Zone per `.json` file). That is the test
and CLI surface, not the Builder language (ADR-0009).

`Zone.Partition` is `PartitionFor(ZoneID)`: FNV-1a 32 of the ID, modulo 64. Callers that produce
to `andara.commands.v1` (AW-SRV-010) must use this function, not a Kafka client default.
`World.PartitionOf(RoomRef)` answers the same question for a Room, which has no Partition of its
own — it inherits its Zone's, because a Zone is the unit of simulation authority. The mapping is
pinned by golden vectors in `server/sim/partition_test.go`: under ADR-0002 changing it is a
migration, not a refactor, because repartitioning a keyed topic reorders history.

## The Protocol endpoint (AW-SRV-005)

One TLS listener serves `andara.game.v1.Game` and `andara.admin.v1.Admin` from the code in
`gen/go`, over gRPC, gRPC-Web, and Connect, with server reflection so `grpcurl ... list` works.
`Admin` is the same handler and the same TLS as `Game`; restricting who can reach it is a
network-policy question (`AW-INF-006`), not a second transport.

**Session lifecycle.** `OpenSession` negotiates a Protocol version (client inside
`[protocol.min_version, protocol.max_version]` gets what it asked for; outside it is
`FAILED_PRECONDITION` naming both, with a `PreconditionFailure` detail, and no Session), verifies
the token, and returns a `SessionID`. The Session is bound to the transport connection it was
opened on: when that connection closes, the Session is torn down, `andara_sessions_active`
decrements, and every stream on it ends. `CloseSession` does the same deliberately. An unknown
`session_id` on `Submit`, `Subscribe`, or `CloseSession` is `UNAUTHENTICATED`.

**Interceptor chain**, outermost first: metrics → trace → draining → deadline → auth. Any
`Admin` method without `Authorization: Bearer <token>` is `UNAUTHENTICATED` before a handler
runs. Trace context is read from W3C `traceparent` metadata, so `andara-cli`'s `cli.command` span
is the parent of the server's RPC span; `session.lifetime` is a root span per Session linked to
the `OpenSession` RPC that created it.

**Seams.** `Submit` hands off to a `gateway.Ingress` (`AW-SRV-010`; the stub answers
`UNIMPLEMENTED`), `Subscribe` to a `gateway.Egress` (`AW-SRV-011`; the stub holds the stream open
with no Events until the Session ends or the server drains), and tokens to a
`gateway.TokenVerifier` (`AW-SRV-008`; the stub accepts any non-empty token). Each is an
`Options` field.

**Drain.** On `SIGTERM`/`SIGINT`, `/readyz` goes 503, new RPCs get `UNAVAILABLE` (retryable),
every Session is closed with reason `server draining` and every open stream ends with
`UNAVAILABLE`, in-flight unary RPCs finish, and the process exits 0 — within
`grpc.drain_timeout`, or after force-closing what remains.

**Error taxonomy**, as gRPC codes: version outside range `FAILED_PRECONDITION`; missing or
invalid token, or unknown Session, `UNAUTHENTICATED`; authenticated but not permitted
`PERMISSION_DENIED`; message over `grpc.max_recv_bytes` `RESOURCE_EXHAUSTED`; draining
`UNAVAILABLE`.

```
make up
grpcurl -cacert .local/tls/ca.pem localhost:8443 list
grpcurl -cacert .local/tls/ca.pem -d '{"protocol_version":1,"auth_token":"x","client_name":"grpcurl"}' \
    localhost:8443 andara.game.v1.Game/OpenSession
grpcurl -cacert .local/tls/ca.pem -d '{"protocol_version":99}' \
    localhost:8443 andara.game.v1.Game/OpenSession   # FailedPrecondition, names 99 and 1..1
grpcurl -cacert .local/tls/ca.pem -H 'Authorization: Bearer x' \
    localhost:8443 andara.admin.v1.Admin/GetServerInfo
```

`canonical.Marshal` (`server/canonical`) is the encoder for anything that feeds the State Hash
(ADR-0007 rule 3): deterministic protobuf, and it refuses a message whose descriptor contains a
`map`, a `float`/`double`, or `google.protobuf.Any` rather than encode it non-canonically.

`CanonicalBytes(*World)` serializes topology in a stable order — zones by ID, rooms by ID, exits by
Direction — with free-text fields escaped so the encoding is injective. It is how AW-SRV-001 AC-10
is asserted, and it is deliberately not ADR-0002's State Hash, which covers mutable state and
arrives with `AW-SRV-002`.
