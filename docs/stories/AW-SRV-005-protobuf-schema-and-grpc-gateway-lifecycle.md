---
id: AW-SRV-005
title: Protobuf schema, gRPC service definition, and gateway connection lifecycle
epic: EPIC-03
component: server
type: feature
status: ready
size: M
depends_on: [AW-INF-001]
blocks: [AW-SRV-008, AW-SRV-010, AW-SRV-011, AW-INF-006, AW-CLI-004]
assignee: cursor
risk: high
---

## Context

ADR-0007 makes protobuf the single schema authority for the wire, the Kafka log, snapshots, and
content. ADR-0003 puts gRPC on the wire and serves Connect and gRPC-Web from the same handler so
Phase 2's browser client needs no proxy.

This story writes that schema and stands up the server that serves it. It is deliberately first among
the transport stories: almost everything downstream — Commands in the log, Events on the stream,
Account records, content manifests, the Python SDK, the Phase 2 client — is generated from what this
story defines. Getting the field numbers and the service shape right here is cheap; changing them once
there is a log full of records written against them is not.

## User story

As a developer, I want one protobuf schema that generates the server, the CLI, the agent SDK, and the
future client, so that a contract change is one diff rather than four that drift.

## Scope

### In scope
- The `.proto` sources under `docs/specs/protocol/`, per the ADR-0007 layout.
- `andara.game.v1.Game` and `andara.admin.v1.Admin` service definitions, both on one endpoint.
- `andara.log.v1` record types: `LoggedCommand`, `Event`, `TickCompleted`.
- `andara.state.v1` snapshot envelope with `state_version`.
- Code generation for Go, Python, and TypeScript, committed.
- The gRPC/Connect/gRPC-Web server: TLS, connection lifecycle, Session establishment, Protocol version
  negotiation, graceful shutdown.
- The canonical-encoding helper for anything that feeds the State Hash (ADR-0007 rule 3).

### Out of scope
- Command ingress behavior — `AW-SRV-010`. This story defines the RPC and accepts the call; it does not
  parse, authorize, or produce.
- Event streaming behavior — `AW-SRV-011`.
- Authentication — `AW-SRV-008`. Session establishment here takes an opaque token and accepts any
  non-empty value against a stub verifier, so the seam exists and is not retrofitted.
- Content message definitions beyond `ZoneDefinition` — `AW-SRV-012`.
- Ingress, certificate issuance in Kubernetes — `AW-INF-006`.

## Acceptance criteria

1. **Given** the `.proto` sources **when** `make proto` runs **then** Go, Python, and TypeScript are
   generated, and **when** `make proto-check` runs afterward **then** it exits 0.
2. **Given** a `.proto` edit that removes a field **when** `make check` runs **then** it exits 1 naming
   the field. ADR-0007's additive-only rule is enforced mechanically.
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
9. **Given** a message that feeds the State Hash **when** it is serialized twice **then** the bytes are
   identical, with map fields either sorted or absent (ADR-0007 rule 3).
10. **Given** an unauthenticated call to any `Admin` method **when** it arrives **then** it is rejected
    at the interceptor, before reaching any handler.
11. **Given** a request with no deadline **when** it arrives **then** the server applies its own maximum
    and does not run unbounded work on a client's behalf.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation
// docs/specs/protocol/andara/game/v1/game.proto

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
- Generated code is committed and `make proto-check` gates merges.
- `buf breaking` runs in CI against the merge base, so an accidental incompatible change cannot land.
- The schema is registered with the schema registry by `AW-INF-004`'s tooling.
- No code path exists that serves the Protocol without TLS.

## Open questions

- `[ASSUMPTION]` Connect's Go implementation, serving all three protocols from one definition
  (ADR-0003). The alternative is grpc-go plus an Envoy gRPC-Web proxy in Phase 2, which is more
  infrastructure for the same outcome.
- `[ASSUMPTION]` `buf` for generation, lint, and breaking-change detection.
- `[NEEDS BRIAN]` Whether `Admin` should be reachable on the same listener in production or restricted
  by network policy. ADR-0003 says same endpoint, same protocol; restricting *reachability* at the
  network layer is compatible with that and is probably wanted. It is an `AW-INF-006` decision.
