---
id: AW-SRV-005
title: gRPC gateway — TLS, session lifecycle, and protocol version negotiation
epic: EPIC-03
component: server
type: feature
status: in-progress
size: M
depends_on: [AW-INF-001, AW-SRV-020]
blocks: [AW-SRV-008, AW-SRV-010, AW-SRV-011, AW-INF-006, AW-CLI-004]
lane: implementation
risk: high
---

## Context

ADR-0007 makes protobuf the single schema authority for the wire, the Kafka log, snapshots, and
content. ADR-0003 puts gRPC on the wire and serves Connect and gRPC-Web from the same handler so
Phase 2's browser client needs no proxy.

**Split 2026-09-09:** this story originally wrote the schema *and* stood up the server. The schema is
an interface contract, which CLAUDE.md §2 puts on Claude Code's side of the line, so it is now
`AW-SRV-020` and this story consumes it. What remains here is the server: TLS, connection lifecycle,
Session establishment, version negotiation, interceptors, and graceful drain — all of which run *in*
the game and are implementation lane.

The split also right-sizes it. One story covering a permanent wire contract and a concurrent network
server was an `L` wearing an `M`'s frontmatter.

## User story

As a developer, I want one protobuf schema that generates the server, the CLI, the agent SDK, and the
future client, so that a contract change is one diff rather than four that drift.

## Scope

### In scope
- The gRPC/Connect/gRPC-Web server: TLS, connection lifecycle, Session establishment, Protocol version
  negotiation, graceful shutdown.
- Serving `andara.game.v1.Game` and `andara.admin.v1.Admin` from one endpoint, from the generated code
  `AW-SRV-020` produces.
- The interceptor chain: authentication seam, request deadlines, message size limits.
- The canonical-encoding **helper** and its determinism test (ADR-0007 rule 3). `AW-SRV-020` states the
  rule and shapes the schema so it is satisfiable; this story implements it in Go.

### Out of scope
- The `.proto` sources and codegen — `AW-SRV-020`.
- Command ingress behavior — `AW-SRV-010`. This story defines the RPC and accepts the call; it does not
  parse, authorize, or produce.
- Event streaming behavior — `AW-SRV-011`.
- Authentication — `AW-SRV-008`. Session establishment here takes an opaque token and accepts any
  non-empty value against a stub verifier, so the seam exists and is not retrofitted.
- Content message definitions beyond `ZoneDefinition` — `AW-SRV-012`.
- Ingress, certificate issuance in Kubernetes — `AW-INF-006`.

## Acceptance criteria

1. **Given** the generated code from `AW-SRV-020` **when** the server is built **then** it serves the
   `Game` and `Admin` services from that generated code, with no hand-written message types.
2. **Given** a message that feeds the State Hash **when** the canonical encoder serializes it twice
   **then** the bytes are identical, asserted by test. The schema-level rules are `AW-SRV-020`'s; this
   is the encoder that honours them.
3. **Given** a running server **when** a gRPC client connects over TLS **then** the handshake succeeds
   against the local CA with no insecure flag.
4. **Given** the same running server **when** a Connect client and a gRPC-Web client connect **then**
   both are served from the same handler and the same service definition.
5. **Given** a client declaring a Protocol version inside the supported range **when** it establishes a
   Session **then** the server returns a `SessionID` and the negotiated version.
6. **Given** a client declaring a version outside the range **when** it establishes a Session **then**
   it is rejected with a typed error naming both the client's version and the supported range, and no
   Session is created. Never silently degraded.
7. **Given** an established Session **when** the connection drops **then** the Session is torn down,
   `andara_sessions_active` decrements, and no goroutine or buffer is leaked — asserted by a
   thousand-connect-and-drop test with a bounded memory delta.
8. **Given** a shutdown signal **when** the server receives it **then** it stops accepting new
   connections, drains in-flight RPCs within the drain timeout, closes streams with a typed reason, and
   exits 0.
10. **Given** an unauthenticated call to any `Admin` method **when** it arrives **then** it is rejected
    at the interceptor, before reaching any handler.
11. **Given** a request with no deadline **when** it arrives **then** the server applies its own maximum
    and does not run unbounded work on a client's behalf.

## Interface contract

The service and message shapes below are **defined by `AW-SRV-020`** and reproduced here for reading
convenience. `docs/specs/protocol/` is authoritative; if these disagree, the `.proto` wins.

```protobuf
// Defined in AW-SRV-020 — docs/specs/protocol/andara/game/v1/game.proto

service Game {
  // Establish a Session. Returns SessionID and the negotiated protocol version.
  rpc OpenSession(OpenSessionRequest) returns (OpenSessionResponse);

  // Submit an Intent. Returns "accepted and ordered", NOT "succeeded" —
  // the outcome arrives on the Event stream (AW-SRV-003, ADR-0002).
  rpc Submit(SubmitRequest) returns (SubmitResponse);

  // Server-streaming, perception-scoped Events for this Session.
  rpc Subscribe(SubscribeRequest) returns (stream EventEnvelope);

  rpc CloseSession(CloseSessionRequest) returns (CloseSessionResponse);
}

message OpenSessionRequest {
  uint32 protocol_version = 1;
  string auth_token       = 2;   // opaque; verified by AW-SRV-008
  string client_name      = 3;   // andara-cli/0.1, agent-sdk/0.1, ...
}

message OpenSessionResponse {
  string session_id                = 1;
  uint32 negotiated_version        = 2;
  uint32 server_min_version        = 3;
  uint32 server_max_version        = 4;
}

message SubmitResponse {
  uint64 accepted_offset = 1;   // where in the log it landed
  int32  partition       = 2;
}
```

### Endpoint and versioning

- One TLS endpoint serves `Game`, `Admin`, gRPC, gRPC-Web, and Connect.
- Protocol version is a `uint32` in `OpenSessionRequest`, distinct from protobuf field evolution.
  Protobuf absorbs additive change; the integer exists for what it cannot.
- Package paths carry the major version (`andara.game.v1`), so a v2 is a new package, not a mutation.

### Configuration

| Key | Env | Default |
|-----|-----|---------|
| `grpc.listen` | `ANDARA_GRPC_LISTEN` | `:8443` |
| `grpc.tls_cert_file` / `grpc.tls_key_file` | `ANDARA_TLS_CERT_FILE` / `ANDARA_TLS_KEY_FILE` | — (required) |
| `grpc.max_recv_bytes` | `ANDARA_GRPC_MAX_RECV_BYTES` | `65536` |
| `grpc.max_request_timeout` | `ANDARA_GRPC_MAX_REQUEST_TIMEOUT` | `30s` |
| `grpc.drain_timeout` | `ANDARA_GRPC_DRAIN_TIMEOUT` | `15s` |
| `protocol.min_version` / `protocol.max_version` | `ANDARA_PROTOCOL_MIN` / `_MAX` | `1` / `1` |

Starting without TLS material is a fatal configuration error. There is no plaintext mode and no flag
to create one; the local stack provisions certificates so nobody needs one (`AW-INF-002`).

### Error taxonomy

Mapped to gRPC codes so that generated clients behave correctly without special-casing:

| Condition | gRPC code | Detail |
|-----------|-----------|--------|
| version outside range | `FAILED_PRECONDITION` | client and server ranges |
| missing or invalid token | `UNAUTHENTICATED` | — |
| authorized but not permitted | `PERMISSION_DENIED` | — |
| message over `max_recv_bytes` | `RESOURCE_EXHAUSTED` | limit |
| server draining | `UNAVAILABLE` | retryable |

## Data / state impact

Defines the record types that will fill `andara.commands.v1` and `andara.events.v1` for the life of the
project, and the snapshot envelope. Field numbers assigned here are permanent; removed fields become
`reserved`, never reused.

Session state is created here and is deliberately **not** World state: a Session is a connection, and
losing it must not lose the Character. Whether Sessions survive a restart is `AW-SRV-015`'s question.

## Observability requirements

### Metrics
- `andara_sessions_active` — gauge.
- `andara_sessions_total` — counter, label `outcome` (`closed`, `dropped`, `rejected_version`,
  `rejected_auth`). Bounded enum.
- `andara_session_duration_seconds` — histogram.
- `andara_grpc_requests_total` — counter, labels `method`, `code`. Bounded by the service definition.
- `andara_grpc_request_duration_seconds` — histogram, label `method`.

Session ID, remote address, and auth token are rejected as labels.

### Logs
- `info` on Session open and close: `session_id`, `client_name`, `negotiated_version`, remote address.
- `warn` on version rejection with both ranges.
- Required fields: `ts`, `level`, `msg`, `service`, `env`, `session_id`, `trace_id`.
- Auth tokens never appear in a log line, at any level.

### Traces
- `session.lifetime` — root span per Session. `command.execute` from `AW-SRV-003` becomes a descendant,
  giving one trace from keystroke to Event.
- Trace context is propagated in gRPC metadata, so `andara-cli`'s `cli.command` span from `AW-CLI-001`
  is the parent of the server's work.

### Alerts
None yet. Requires the Session availability SLO, which `AW-SRV-011` writes once there is a stream to
measure.

## Test plan

- **Unit:** version negotiation across boundary values; error-code mapping for every row of the table;
  canonical encoding determinism; interceptor rejection before handler entry.
- **Integration:** gRPC, gRPC-Web, and Connect clients against one server (AC-4); TLS against the local
  CA; a thousand connect-and-drop cycles asserting bounded memory and no goroutine growth; graceful
  drain with an in-flight stream.
- **Manual/operator:**
  ```
  make up
  grpcurl -cacert .local/tls/ca.pem localhost:8443 list
  grpcurl -cacert .local/tls/ca.pem -d '{"protocol_version":99}' \
      localhost:8443 andara.game.v1.Game/OpenSession   # expect FAILED_PRECONDITION with both ranges
  ```

## Definition of done

CLAUDE.md §8, plus:
- No code path exists that serves the Protocol without TLS.
- The Session teardown test runs in CI, since a leak here is invisible until it is an outage.

## Open questions

- `[ASSUMPTION]` Connect's Go implementation, serving all three protocols from one definition
  (ADR-0003). The alternative is grpc-go plus an Envoy gRPC-Web proxy in Phase 2, which is more
  infrastructure for the same outcome. `AW-SRV-020` generates the Connect stubs this consumes.
- **Resolved 2026-09-10 (Brian): same listener, restricted by network policy.** ADR-0003's "same
  endpoint, same protocol" stands — `Admin` is served from the same Connect handler as `Game`, so
  there is no privileged back door and no second transport to secure (CLAUDE.md §10). What changes is
  *reachability*: a NetworkPolicy admits `Admin` only from the operator network, so the authorization
  check is not the only thing between the internet and a privileged RPC.

  This story is unaffected — it still serves both services on one listener. The policy is
  `AW-INF-006`'s to write, and it is named in that story's scope rather than left implied.
