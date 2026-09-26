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
| `content.source` | `ANDARA_CONTENT_SOURCE` | `kafka` | `kafka` (the Active Pointers, AW-SRV-012) or `dir`. Every environment sets it explicitly; local compose uses `dir`. Either way the content in effect comes through the log (see "Content in effect and the swap"). |
| `content.path` | `ANDARA_CONTENT_PATH` | `./content` | Directory of Zone Definition JSON files. Used only when `content.source=dir`. |
| `content.packs` | `ANDARA_CONTENT_PACKS` | `andara.core` | Packs to follow, comma-separated; `*` follows every Active Pointer. Kafka only. |
| `content.cache_dir` | `ANDARA_CONTENT_CACHE_DIR` | `/var/cache/andara/blobs` | On-disk blob cache, keyed by hash; blobs are immutable. Kafka only. |
| `content.max_blob_bytes` | `ANDARA_CONTENT_MAX_BLOB_BYTES` | `8388608` | Largest blob read; a larger one refuses its version `blob_too_large`. Kafka only. |
| `content.reload_debounce` | `ANDARA_CONTENT_RELOAD_DEBOUNCE` | `2s` | How long a burst of pointer moves is coalesced before it is applied. Kafka only. |
| `content.strict_orphans` | `ANDARA_STRICT_ORPHANS` | `false` | When `true`, Rooms with no inbound Exit in their Zone are errors. |
| `http.port` | `ANDARA_HTTP_PORT` | `8080` | `/livez`, `/readyz`, `/metrics`. Plaintext, operator surface. |
| `telemetry.otlp_endpoint` | `ANDARA_OTLP_ENDPOINT` | `localhost:4317` | Traces and logs go to this collector over OTLP/gRPC (AW-SRV-024). Empty disables both exporters; an endpoint the exporter cannot be built for is fatal. |
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
| `kafka.brokers` | `ANDARA_KAFKA_BROKERS` | — | Comma-separated. **Required** when `auth.store=kafka`. |
| `auth.store` | `ANDARA_AUTH_STORE` | `kafka` | `kafka` or `memory`. `memory` loses every Account on restart and warns at boot; it exists for development and tests. |
| `auth.token_key_file` | `ANDARA_AUTH_TOKEN_KEY_FILE` | — | **Required.** Session-token signing keys, one `key_id: base64` per line, first signs. Mode `0400`/`0600`, or `0440`/`0640` for a Kubernetes Secret with `fsGroup`. See *Key rotation*. |
| `auth.bootstrap_operator` | `ANDARA_AUTH_BOOTSTRAP_OPERATOR` | — | `username:password` for the first operator; applied only while no operator Account exists, ignored afterwards. Never a value in the chart — `secrets.bootstrapOperator` injects it from a Secret. |
| `auth.session_ttl` | `ANDARA_AUTH_SESSION_TTL` | `1h` | Session token lifetime. **Must exceed `session.linkdead_max`** or boot fails: a token that expires inside the grace period fails every linkdead reconnect. |
| `auth.refresh_ttl` | `ANDARA_AUTH_REFRESH_TTL` | `720h` | Refresh token lifetime, 30 days. |
| `auth.argon2.memory_kib` | `ANDARA_AUTH_ARGON2_MEMORY_KIB` | `65536` | Argon2id cost. Changing any of the three rehashes each Account on its next successful login. |
| `auth.argon2.time` | `ANDARA_AUTH_ARGON2_TIME` | `3` | |
| `auth.argon2.threads` | `ANDARA_AUTH_ARGON2_THREADS` | `4` | |
| `auth.rate_limit` | `ANDARA_AUTH_RATE_LIMIT` | `10/m` | Auth attempts per username and per peer address, `N/period`; `off` disables. No lockout, ever — a lockout is a denial-of-service lever. |
| `auth.invite_ttl` | `ANDARA_AUTH_INVITE_TTL` | `168h` | Invite Code lifetime, 7 days. |
| `auth.recheck_interval` | `ANDARA_AUTH_RECHECK_INTERVAL` | `30s` | How often every open Session re-reads its Account's status and roles; the bound on how long a disabled Account stays connected. |
| `auth.k8s_issuer` | `ANDARA_AUTH_K8S_ISSUER` | — | Issuer of projected service-account tokens for `WORKLOAD_JWT` agent Accounts. Unset disables the kind. Set together with `auth.k8s_jwks_url`. |
| `auth.k8s_jwks_url` | `ANDARA_AUTH_K8S_JWKS_URL` | — | JWKS endpoint for `auth.k8s_issuer`. |
| `session.linkdead_max` | `ANDARA_LINKDEAD_MAX` | `300s` | ADR-0006's hard ceiling on linkdead duration. Read here for the `auth.session_ttl` assertion; `AW-SRV-015` reads it for what it means. |
| `sim.source` | `ANDARA_SIM_SOURCE` | `kafka` | `kafka` or `memory`. `memory` ticks the World with no Command input and publishes nothing; development only. |
| `sim.tick_rate` | `ANDARA_TICK_RATE` | `10` | Ticks per second (ADR-0008). 1..100. |
| `sim.tick_budget_ms` | `ANDARA_TICK_BUDGET_MS` | `50` | Overrun threshold — half the interval, so overruns warn before lag accrues. Must not exceed the interval. |
| `sim.max_per_tick` | `ANDARA_MAX_PER_TICK` | `1024` | Records applied per tick, taken round-robin across Partitions; the rest wait. |
| `sim.drain_timeout_ms` | `ANDARA_DRAIN_TIMEOUT_MS` | `5000` | Shutdown budget for the in-flight tick, the checkpoint, and `SimulationStopped`; past it, exit 1 naming the tick. |
| `sim.seed` | `ANDARA_SIM_SEED` | derived | PRNG seed; `0` derives one from the topology the Engine starts with. Every Engine starts with no content (AW-SRV-012), so the derived seed is the same for every World; set it to tell Worlds apart. Overriding is a debugging affordance. |
| `sim.partitions` | `ANDARA_SIM_PARTITIONS` | `0-63` | Assigned Partitions: a range, or the comma list the chart's init container writes from the pod ordinal. |
| `sim.checkpoint_every_ticks` | `ANDARA_CHECKPOINT_EVERY_TICKS` | `100` | Offset commit cadence — a startup-cost knob, not a correctness one. |
| `events.subscriber_buffer` | `ANDARA_SUBSCRIBER_BUFFER` | `1024` | Events a subscriber may leave unread before it is dropped with `SubscriberDropped`. |
| `events.max_subscribers` | `ANDARA_MAX_SUBSCRIBERS` | `10000` | Event subscriptions this process accepts; registration past it is refused. |
| `command.max_intent_bytes` | `ANDARA_MAX_INTENT_BYTES` | `4096` | Largest Intent `parse` will read; over it is `intent_too_large` on the length alone, before tokenizing. |
| `command.verb_table_path` | `ANDARA_VERB_TABLE` | built in | JSON verb table that *replaces* the built-in one (`look`, `move`, the twelve Directions and their compass aliases). A file that does not parse fails the boot. |
| `ingress.rate_limit` | `ANDARA_INGRESS_RATE_LIMIT` | `20/s` | Submits per Session, `N/period`; `off` disables. Applied before parse. |
| `ingress.agent_rate_limit` | `ANDARA_AGENT_RATE_LIMIT` | `100/s` | The rate for `agent` Principals, which drive many NPCs per Session (ADR-0005). |
| `ingress.burst` | `ANDARA_INGRESS_BURST` | `40` | Token bucket depth per Session: how many Submits may arrive at once before the rate applies. |
| `ingress.produce_deadline` | `ANDARA_PRODUCE_DEADLINE` | `2s` | How long one produce may take. It is the client's record delivery timeout; a produce request times out at half of it, so one idempotent retry fits inside. |
| `ingress.max_pending` | `ANDARA_INGRESS_MAX_PENDING` | `256` | Submits one Session may have in flight; past it, `RESOURCE_EXHAUSTED` rather than a growing queue. |
| `egress.buffer` | `ANDARA_EGRESS_BUFFER` | `1024` | Events a Session's stream may leave unsent before the stream is ended with `buffer_full`. The Session survives; the client reopens the stream. |
| `egress.resume_window` | `ANDARA_EGRESS_RESUME_WINDOW` | `2048` | Delivered Events retained per Session, for a stream reopened with `last_event_id`. At least `egress.buffer`. A resume from before the window is a `Resync` frame. |
| `egress.heartbeat_interval` | `ANDARA_HEARTBEAT_INTERVAL` | `20s` | How long a stream may be silent before a `Heartbeat` frame is sent. Also how long a stream reset is given to return a blocked writer before its connection is closed. |
| `ingress.transit_hold` | `ANDARA_INGRESS_TRANSIT_HOLD` | `2s` | How long a Session's Intents wait for its Character to arrive in the next Zone, measured from the `CharacterLeft`. Past it they are rejected `in_transit`. A held Submit is also bounded by the RPC deadline (`grpc.max_request_timeout`). `0` holds nothing: any Submit during a transit, including the same-tick window of a same-Zone move, is `in_transit`. |
| `ingress.idempotency_window` | `ANDARA_INGRESS_IDEMPOTENCY_WINDOW` | `30s` | How long a Submit's outcome is remembered against its `(Session, client_ref)` once known, so a retry inside it is the same Command. It governs *resolved* keys; a key still in flight lives until its outcome is known, however long that takes. Must exceed `ingress.produce_deadline`. Per process; at most `ingress.max_pending` keys per Session, the oldest resolved one evicted first — a key still in flight is never evicted. |
| `character.max_per_account` | `ANDARA_CHARACTER_MAX_PER_ACCOUNT` | `5` | Characters an Account may hold (ADR-0006), ACTIVE or DELETED — a deleted one keeps its slot until purged (`AW-SRV-032`). A sixth is `RESOURCE_EXHAUSTED roster_full`. |
| `character.spawn_room` | `ANDARA_CHARACTER_SPAWN_ROOM` | `town/plaza` | `zone_id/room_id` a never-bound Character is placed in. Resolved against the loaded content at boot; a value the World lacks fails the boot. The default is the dev fixture's Room, so every values file outside `ENV=local` must set it (the chart's schema requires it). |
| `character.name_pattern` | `ANDARA_CHARACTER_NAME_PATTERN` | `^[\p{L}][\p{L}' -]{2,23}$` | RE2 a Character name must match, as typed. Uniqueness is on the folded form (NFKC, case-folded, trimmed), across every Account, forever. |
| `snapshot.interval` | `ANDARA_SNAPSHOT_INTERVAL` | `60s` | Cadence of a snapshot round — one consistent cut of every owned Zone at a tick boundary (`docs/specs/slo/recovery.md`). It sets RTO only: RPO is zero and is decided by broker settings. The round starts on the first boundary past the interval, never mid-tick. `0` disables snapshots, which makes every recovery a replay from the log's beginning: correct, and unbounded. |
| `snapshot.max_stall_ms` | `ANDARA_SNAPSHOT_MAX_STALL_MS` | `5` | Budget for the in-tick copy — the only part of a round inside the tick, and the part players can feel. A warning, not a refusal: over it, the round continues and `andara_snapshot_failures_total{reason="stall"}` counts it. Measured against the sizing fixture at 2.7 ms; see `docs/feedback/AW-SRV-006-zone-snapshots.md` §7 for the margin. |
| `snapshot.store` | `ANDARA_SNAPSHOT_STORE` | `fs` | `fs` or `s3`. `fs` is `make up` and the volume `AW-INF-003` mounts; `s3` is the cluster, and MinIO locally. |
| `snapshot.fs_path` | `ANDARA_SNAPSHOT_FS_PATH` | `/var/lib/andara/snapshots` | Directory the `fs` store writes under, keyed `{zone_id}/{state_version}/{tick}/{offset}`, both numbers zero-padded to 20 digits so a lexical listing is in tick order. The tick is what makes a round's objects immutable — without it an idle Zone rewrote the same key every round, and a partial failure destroyed the last complete round (AC-5). Writes go to `{key}.tmp` and are renamed, so an interrupted write leaves nothing a listing returns. |
| `snapshot.s3_bucket` | `ANDARA_SNAPSHOT_S3_BUCKET` | — | Required when `snapshot.store=s3`; the server refuses to start without it rather than failing its first round a minute after it looked healthy. |
| `snapshot.s3_endpoint` | `ANDARA_SNAPSHOT_S3_ENDPOINT` | — | S3 endpoint. MinIO locally; empty for AWS. |
| `snapshot.upload_timeout` | `ANDARA_SNAPSHOT_UPLOAD_TIMEOUT` | `30s` | A round exceeding it is failed, not queued behind the next one; the next round starts on schedule. Must not exceed `snapshot.interval` — if the two would have to meet, the body has outgrown the cadence. |
| `telemetry.trace_sample_ratio` | `ANDARA_TRACE_SAMPLE_RATIO` | `0.01` | Fraction of `Game/Submit` traces exported, decided at the root and carried into the tick's `command.apply`. Every rejection is exported whatever it says; every other root is. |
| `telemetry.trust_inbound_traceparent` | `ANDARA_TRUST_INBOUND_TRACEPARENT` | `false` | Let a client's W3C `traceparent` parent the RPC span — and carry its sampling decision. Off, the RPC span is a new root that links to the client's context, so the ratio applies whatever the client sent. `make up` sets it, so andara-cli's `cli.command` root sits above the RPC locally. |

Starting without TLS material is a fatal configuration error (exit 1). There is no plaintext
mode and no flag to create one; `make up` provisions certificates so nobody needs one
(ADR-0003). `--validate-only` never listens and is exempt.

`--validate-only` loads and validates, prints every finding, and exits without serving. Exit `0`
on a valid World, `1` on any fatal finding.

### In Kubernetes

The chart (`deploy/helm/andara`, AW-INF-003) sets every key above through `server.<key>` in a
values file — `server.content.source: dir` renders `ANDARA_CONTENT_SOURCE=dir` into the
`andara-config` ConfigMap. The mapping lives in `deploy/helm/andara/keys.yaml`, which `make
values-schema-check` holds against this package: **a new `ANDARA_*` read anywhere under `server/`
fails `make check` until `keys.yaml` lists it**, and then `make values-schema` regenerates the
values schema and the env template. Keys that are groomed but not yet read here are in
`keys.yaml` already and stay out of the schema until the code lands — a values file cannot set
a key this binary would ignore.

`ANDARA_SIM_PARTITIONS` is the one variable the chart sets that is not a value: the `partitions`
init container derives it from the pod ordinal (`p mod replicaCount == ordinal`, over 0–63) and
the server container sources it before exec. Probes: startup and readiness on `/readyz`, liveness
on `/livez`, all on `http.port`. `/livez` must never depend on Kafka or a datastore.

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
`gateway.TokenVerifier`, which is `auth.Store` — there is no accept-anything verifier outside the
tests, and `gateway.New` refuses a nil one. Each is an `Options` field.

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
TOKEN=$(grpcurl -cacert .local/tls/ca.pem -d '{"username":"operator","password":"andara-local"}' \
    localhost:8443 andara.auth.v1.Auth/Authenticate | jq -r .tokens.sessionToken)
grpcurl -cacert .local/tls/ca.pem -d "{\"protocol_version\":1,\"auth_token\":\"$TOKEN\",\"client_name\":\"grpcurl\"}" \
    localhost:8443 andara.game.v1.Game/OpenSession
grpcurl -cacert .local/tls/ca.pem -d '{"protocol_version":99}' \
    localhost:8443 andara.game.v1.Game/OpenSession   # FailedPrecondition, names 99 and 1..1
grpcurl -cacert .local/tls/ca.pem -H "Authorization: Bearer $TOKEN" \
    localhost:8443 andara.admin.v1.Admin/GetServerInfo
```

## The tick loop (AW-SRV-002)

`server/sim` is the core and `server/tickloop` schedules it. The core is handed a `TickInput` of
records — Partition, offset, typed Command — and returns a `StepResult`: the Events it emitted,
the cross-Zone Commands to produce, the records it did not apply, and the `TickCompleted` boundary.
It reads no clock and imports nothing on `depguard`'s denied list (AC-13); the loop owns the clock
(real, or stepped for tests), the franz-go consumer and producer, checkpoints, and every SLI.

**Determinism.** `sim.WorldState` is everything mutable — `state_version`, tick, seed, the
xoshiro256** RNG state, the next Event ID, the next-to-read offset per Partition, each Zone's
faulted flag and Entities — and `Hash` is SHA-256 over its canonical serialization. `Step` is a pure
function of (state, input): the same records in the same batches produce the same hash on every
platform, which `make test-determinism` runs on linux/amd64, linux/arm64, and darwin/arm64 in CI,
against a committed golden sequence of 1,000 hashes (`server/tickloop/testdata/golden_hashes.txt`).
A golden mismatch is a determinism regression, or an intended state change that needs a
`state_version` decision — never a `-update` on its own.

**Boundaries and recovery** (ADR-0002 §4). Every tick publishes a `TickCompleted` — tick,
next-to-read offset per Partition, hash — to Partition 0 of `andara.events.v1` under the key
`tick-boundary`. `Engine.Replay` drives `Step` from those records rather than re-deciding how records
were batched, and halts with `ErrHashMismatch` if a hash disagrees. Until `AW-SRV-006` gives it a
snapshot, a boot recovers this way from tick 0: correct, exact, and slow in proportion to the log. A
missing boundary is a lost batching decision; recovery refuses to replay past it (`ErrBoundaryGap`)
rather than guess — the policy for that case is `AW-SRV-007`'s, and the operator's path today is an
empty log.

**The schedule.** Anchored at start and never moved: tick *n* is due at `start + n·interval`, lag is
the distance behind that, and a late tick is never skipped — so `andara_simulation_lag_seconds` is
honest under overload rather than something the loop resets by falling behind. Records are taken
round-robin across Partitions up to `sim.max_per_tick`; the rest wait and show as
`andara_tick_deferred_records`. Publishing is asynchronous with a minute of retries, because Events
are derived (ADR-0002 §3) and a broker stall must not be a tick stall; a lost broker is a starved
tick, counted, never a crash. A handler panic is contained at the Zone: the Zone is marked faulted
with a `ZoneFaulted` Event, its Partition freezes at the panicking record, and the other Zones keep
ticking. Verb handlers register on `sim.Config.Handlers` (`AW-SRV-003`); a Command with none is
rejected `unsupported_command` and its offset advances.

**Drain.** On `SIGTERM` the gateway drains first, then the loop finishes its in-flight tick,
checkpoints, emits `SimulationStopped` (event_id 0 — a notification, not World history), flushes the
publisher, and exits 0; past `sim.drain_timeout_ms` it exits 1 naming the tick.

### Tick metrics, logs, and traces

| Metric | Type | Labels | Cardinality bound |
|--------|------|--------|-------------------|
| `andara_tick_duration_seconds` | histogram | — | 1; a bucket boundary at exactly `0.05` |
| `andara_zone_tick_duration_seconds` | histogram | `zone` | Zones |
| `andara_ticks_total`, `andara_tick_overruns_total` | counter | — | 1 |
| `andara_simulation_lag_seconds` | gauge | — | 1 — the symptom; `SimulationLagging` |
| `andara_tick_applied_records_total` | counter | — | 1 |
| `andara_tick_deferred_records` | gauge | — | 1 |
| `andara_tick_input_starved_total` | counter | — | 1 |
| `andara_consumer_lag` | gauge | `partition` | 64 |
| `andara_checkpoint_age_ticks` | gauge | — | 1 |
| `andara_tick_zone_faults_total` | counter | `zone` | Zones |
| `andara_tick_publish_failures_total` | counter | `kind` | `events`, `commands`, `checkpoint` |

Logs: `tick` at `info` once a second with `tick`, `lag_ms`, `consumer_lag`, `deferred`,
`checkpoint_age_ticks`, `applied_offsets`; `tick overran its budget` at `warn` with `tick`,
`duration_ms`, `budget_ms`, and the slowest `zone`; `tick input starved` at `warn`; `zone faulted` at
`error` with `tick`, `zone`, `partition`, `offset`, `panic`. Spans: `sim.tick` per tick with
`record_count`, `event_count`, `overrun`, `starved`, `lag_seconds`, and `sim.zone_tick` per Zone —
always started, exported one in a hundred plus every overrun (`telemetry.TickSampler`). Per-Entity
spans are not emitted. The dashboard is `andara-tick-health`; the SLO is
`docs/specs/slo/tick-health.md`.
## The command pipeline (AW-SRV-003)

Five stages, with the log in the middle (CLAUDE.md §10, ADR-0002):

```
Gateway:  parse ──▶ authorize ──▶ [ produce to andara.commands.v1 ] ──▶ ack "accepted"
Tick:     [ consume ] ──▶ validate ──▶ apply ──▶ emit events
```

`server/command` is the pre-log half. `Parse` is stateless: it knows the verb table and nothing
else, resolves the verb (exact name, then alias, then the unique abbreviable prefix — `no` is
refused naming `north`, `northeast`, `northwest`), binds and checks the arguments, and produces a
`LoggedCommand` arm. `Authorize` is `auth.Authorizer` from `AW-SRV-008` — the verb's role from the
table's role column — plus the one rule this story adds: a Session bound to no Character may submit
nothing. Both reject before the produce, so **the log holds only Commands that parsed and were
authorized**; an unauthorized attempt goes to `andara.audit.v1` instead. An ack means *accepted and
ordered*, never *succeeded* — the outcome arrives later as an Event.

`server/sim` is the post-log half. `sim.Handlers()` is the apply column — `look`, `move`, and
`arrive` — and each handler is `validate` then `apply`: validate reads state and never mutates it,
apply is the only mutating stage, and a validate failure returns before apply runs. Both are
reachable only through a `Record` that `Step` took from the log: a handler refuses an
`ApplyContext` it did not build (`ErrNotConsumed`), so the boundary is a guard, not a convention. A
post-log rejection has consumed its offset — it was legitimately ordered and turned out to be
illegal — and is a `CommandRejected` Event to the actor with a stable code and a player-safe message
(`there is no exit west`, never the Room beyond it); World state is unchanged.

**Position** is `EntityState.Room`, owned by the Zone that holds the Room and hashed. **A cross-Zone
move** removes the Character from the source Zone and produces an `Arrive` — the Entity by value,
Components and all — to the target Zone's Partition, where the next tick places it (ADR-0001 rule
4). It is never a call, whichever process owns the target, so the arrival is one tick later even in
a single process; in between, a Command on either Zone is `actor_not_found`. `Arrive` is not a verb:
only a tick produces one, and the verb table cannot bind it. An `Arrive` whose Room is gone
(content moved under the log) is bounced back to its origin once, and rejected `unknown_room` if
that is gone too.

| Code | Stage | Pre-log |
|------|-------|:-------:|
| `unknown_verb`, `missing_argument`, `invalid_argument`, `intent_too_large` | parse | yes |
| `not_authorized` | authorize | yes |
| `no_such_exit`, `exit_blocked`, `actor_not_found`, `unknown_room` | validate | no |
| `zone_faulted`, `unsupported_command`, `unknown_zone`, `misrouted`, `rejected` | apply | no |

`andara-cli sim repl --content <dir>` drives the whole pipeline in-process with a fake log —
parse, authorize, append, tick, print — the same stages `Game.Submit` runs against the broker.

### Command ingress (AW-SRV-010)

`server/ingress` is what `Game.Submit` plugs into: the produce between the two halves, the
moment an Intent stops being a client's assertion and becomes an ordered fact. A Submit is:

1. **Rate limited** per Session before anything is parsed — `ingress.rate_limit` refilling a
   bucket `ingress.burst` deep, `ingress.agent_rate_limit` for agents. Over it is
   `RESOURCE_EXHAUSTED` (`rate_limited`); nothing is produced and the Session survives.
2. **Queued behind the Session's earlier Submits** — each waits for the one before it, so a
   Session's Commands reach the log in the order its Submits arrived, whatever goroutine each ran
   on. More than `ingress.max_pending` in flight is `RESOURCE_EXHAUSTED` (`pending_full`).
3. **Run through the pipeline** — `command.Parse`, `auth.Authorizer`, the Character binding. The
   record produced is exactly what `Parse` returned plus `zone_id`, `actor_id`,
   `accepted_at_unix_nano`, and `trace_id`; `Submit` takes raw text, never a `LoggedCommand`, so a
   client can put nothing in the log it did not type.
4. **Produced** to `andara.commands.v1` on the Zone's Partition — `sim.PartitionFor`, FNV-1a of the
   ZoneID, never the library's default hash, because a library upgrade that remapped Zones would
   split their history across Partitions unrecoverably; the client is built with a manual
   partitioner so an unset Partition is a bug rather than a fallback — with `acks=all`, the
   idempotent producer (five requests in flight per broker, the number it preserves order at),
   the record keyed by ZoneID, and `ingress.produce_deadline` bounding the wait.
5. **Answered** with the actual Partition and offset. That means *accepted and ordered*, not
   *succeeded*.

**Idempotency (AW-SRV-031).** `(Session, client_ref)` is a Submit's idempotency key. The ingress
makes an entry for it before the Submit queues and resolves it with the outcome, so a retry with
the same `client_ref` inside `ingress.idempotency_window` is answered with the original outcome —
the same Partition and offset for a produce that landed, the same typed rejection for an Intent
that was refused — without queueing, parsing, or producing; a retry that arrives while the
original is still in flight waits for it, bounded by its own deadline, and does not take a place
in the Session's queue. Only outcomes that are the Command's fate are remembered: an offset, or a
`parse`/`authorize` rejection. A transient refusal — `rate_limited`, `pending_full`,
`world_read_only`, `in_transit`, the caller giving up — is not, so the retry the client was told
to make runs the Command. A Submit answered `DEADLINE_EXCEEDED` `produce_deadline` keeps its key
open: the record is still live in the producer and its promise still fires, so the key is resolved
by the record's fate — *landed*, and a retry gets the offset it landed at; *not written*, and the
retry is a new Command; *outcome unknown*, and a retry inside the window is answered
`DEADLINE_EXCEEDED` `outcome_unknown`, which is terminal: the record may be in the log and nothing
will ever say, so the client stops retrying and tells the player to `look`. The Events a Command
causes carry its `client_ref`, so a client watching its stream learns the truth without asking.
*Not written* means exactly this: no produce request from this process reached a socket since the
record was enqueued — franz-go's promise does not say whether a failed record was ever sent, so a
process-wide count of produce requests written is the discriminator, sound in the safe direction
and imprecise in the other: under concurrent produce traffic a never-sent record is classified
unknown. The same `client_ref` with different text inside the window is a client bug and is
rejected `INVALID_ARGUMENT` `duplicate_client_ref`, nothing produced; an empty `client_ref` is not
deduplicated at all — two Submits with none are two Commands — and `andara-cli` and the client
always send one. The table is per process: a retry after a restart or on another pod is a new
Command. `ingress.max_pending` bounds keys as well as Submits in flight; a key kept open by an
unsettled record counts while its Submit has already returned, so during an outage burst
`pending_full` can be answered with the queue itself not full.

**Bindings.** `ingress.Bindings` is the Gateway's routing view: which Character each Session
drives and which Zone it was last seen in. It is Session state, not World state. It is kept
current from the sim's own Events — a `CharacterLeft` addressed to a bound Character puts its
Session *in transit*, the `CharacterArrived` that follows settles it on the new Zone — so any
Gateway routes to the right Partition whichever process owns the Zone (AC-9). While in transit a
Session's Intents are **held**, in order, and released to the new Zone's Partition on arrival:
the one-tick cross-Zone delay stays visible in the Events, but a player never sees *you are not
here* for typing during it. The hold is bounded by `ingress.transit_hold` from the `CharacterLeft`;
past it, held and later Intents are rejected `in_transit` (`UNAVAILABLE`, retryable) until an
arrival resolves the Session, so a stuck handoff surfaces to the player rather than to a queue.
`Bind` is the seam `AW-SRV-014` fills when `SelectCharacter` lands; until then no Session is bound
on a running server and every Submit is `not_authorized: you are not in the world`, audited.

**Read-only World.** The log's availability bounds the World's (ADR-0002): with no broker, no
Command can be accepted. A probe pings the brokers once a second, always; when none answers — or a
produce fails and a ping then fails — the ingress is *degraded*: `andara_ingress_degraded` reads
1, an `info` line names the broker error, and every Submit fails at once with `UNAVAILABLE`
(reason `world_read_only`, a `RetryInfo` of one second) — no wait, no buffer. The tick and the
Event stream do not pass through here and carry on; `/readyz` stays 200. The Submits in flight when
the outage was detected are the ambiguous ones: their records were already handed to the client
and may have reached the broker, so each is answered `DEADLINE_EXCEEDED` (reason
`produce_deadline`, *outcome unknown*), and entering the degraded state swaps the producer client
so nothing it still held lands minutes later on a player who was told the World was read-only. When a broker answers the
probe again the state clears without a restart. `docs/runbooks/world-read-only.md` is the runbook.

| Condition | gRPC code | `ErrorInfo.reason` | Log record written |
|-----------|-----------|--------------------|--------------------|
| parse failure | `INVALID_ARGUMENT` | the pre-log code | none |
| unauthorized, or no Character bound | `PERMISSION_DENIED` | `not_authorized` | audit only |
| Character in transit past the hold | `UNAVAILABLE` | `in_transit` | none |
| rate limited | `RESOURCE_EXHAUSTED` | `rate_limited` | none |
| pending queue full | `RESOURCE_EXHAUSTED` | `pending_full` | none |
| log unreachable | `UNAVAILABLE` (retryable) | `world_read_only` | none |
| produce deadline exceeded | `DEADLINE_EXCEEDED` | `produce_deadline` | possibly — the outcome is unknown; the client retries with the same `client_ref` and gets the original outcome once the record's fate is known |
| the record's fate settled unknown | `DEADLINE_EXCEEDED` | `outcome_unknown` | possibly, and nothing will ever say — terminal; the client stops and the player `look`s |
| `client_ref` reused for a different Intent inside the window | `INVALID_ARGUMENT` | `duplicate_client_ref` | none |

Every error carries an `ErrorInfo` with `domain: andara.command`, the reason above, and for a
pipeline rejection `stage`, `pre_log`, and `arg`. The `UNAVAILABLE` message is the read-only
wording every player eventually sees (Brian, 2026-09-19).

#### Ingress metrics, logs, and traces

| Metric | Type | Labels | Cardinality bound |
|--------|------|--------|-------------------|
| `andara_ingress_submits_total` | counter | `outcome` | `produced`, `rejected_parse`, `rejected_authz`, `rate_limited`, `pending_full`, `in_transit`, `unavailable`, `deadline`, `canceled`, `internal`, `deduplicated`, `rejected_ref` (a `client_ref` reused for a different Intent) |
| `andara_ingress_produce_duration_seconds` | histogram | — | enqueue to acknowledgement: what a player waits for the ack |
| `andara_ingress_produce_retries_total` | counter | — | produce requests sent again after a transport failure |
| `andara_ingress_pending` | gauge | — | Submits in flight on this process |
| `andara_ingress_idempotency_keys` | gauge | — | `(Session, client_ref)` keys remembered on this process: in flight, or resolved inside `ingress.idempotency_window` |
| `andara_ingress_held_intents` | gauge | — | Intents waiting for their Character to arrive |
| `andara_ingress_degraded` | gauge | — | 1 while the World is read-only; `AW-INF-005` alerts on it |
| `andara_ingress_produced_total` | counter | `partition` | 64; Commands produced by Partition — `topk(5, rate(...[5m]))` is the hot-Zone view |

Logs: `command log unreachable` / `command log reachable` at `info` on degradation entry and exit
with the broker error; `command rejected` at `info` per authorize rejection (the pipeline's line,
with `session_id`, `verb`, `trace_id`); `session rate limited`, `session pending queue full`, and
`produce request failed` at `warn`, sampled to one line a second, as is `client_ref reused for a
different command`; `command accepted` at `debug` with `partition` and `offset`, and `submit
deduplicated` at `debug` with `session_id`, `client_ref`, `trace_id`. Spans: `log.produce` is the
child of `command.execute` after `command.parse` and `command.authorize`, with `partition`,
`offset`, `retries`, `acks_wait_ms`; a deduplicated Submit's `command.execute` carries
`deduplicated=true` and has no children.

**Sampling.** `Game/Submit` is the one root per keystroke, so it is head-sampled at
`telemetry.trace_sample_ratio`; the decision is made where the root starts, rides the sampled flag
into `LoggedCommand.trace_id`, and the tick's `command.apply` inherits it as a remote parent — a
sampled trace is whole, an unsampled one is absent from both ends. A client's own `traceparent`
(andara-cli sends one) parents the RPC — and decides — only under
`telemetry.trust_inbound_traceparent`; otherwise a client that flagged every request sampled
would hold the collector's cost lever, so the RPC is a new root that links to it. Every
rejection is exported whatever the head said — `command.execute` records under an unsampled root
and `telemetry.SpanFilter` forwards it when `stage_failed` is set — and a tick-produced record with
no Gateway root (an `Arrive`) keeps `sim.tick`'s one in a hundred. Every other root — a Session's
lifetime, recovery, the other RPCs — is sampled.

### Command metrics, logs, and traces

| Metric | Type | Labels | Cardinality bound |
|--------|------|--------|-------------------|
| `andara_commands_total` | counter | `verb` | the verb table; an unknown verb is never a label |
| `andara_command_rejected_total` | counter | `stage`, `code`, `pre_log` | 5 stages × the code table × 2 |
| `andara_command_duration_seconds` | histogram | `verb`, `phase` | verbs × `pre_log`, `post_log` |

Session ID, Entity ID, Room ID, and raw Intent text are never labels. Logs: `command rejected` at
`info` for an authorize rejection and `debug` otherwise, with `session_id`, `verb`, `stage`, `code`,
`detail`; `intent received` at `debug` with the raw text quoted and escaped; `command applied` at
`debug` per record with `verb`, `actor`, `session_id`, `tick`, `partition`, `offset`, `code`,
`stage`, `duration_ms`. Spans: `command.execute` per Submit with `command.parse` and
`command.authorize` beneath it; the record's `trace_id` carries the W3C traceparent, and the tick's
`command.apply` span (`verb`, `partition`, `offset`, `stage_failed`, `code`) is its child, linked to
`sim.tick` — one trace from keystroke to Event, with the queue time in the log visible as the gap.

## Events and perception (AW-SRV-004)

Events are the sim's only output and they are *derived*: replaying the log regenerates them, so a
player is told the outcome before anything durable happens. Every Event carries a **Scope**,
computed inside the sim at emit — a Room (or a whole Zone with the Room empty), explicitly addressed
Entities, and World, the privileged view — and, when the type carries operator detail (`ZoneFaulted`'s
Zone ID, `SimulationStopped`'s reason), a **redacted form** prepared alongside it. Scoping is a
security boundary, which is why it lives in `server/sim` and not in a transport: a Character cannot
learn what happens two Rooms away whatever client it uses, and a bystander is not sent the
description the Character next to it just read. `look` and rejections are addressed to the actor;
a move reaches the source Room, the target Room, and the mover.

`server/events` is the fan-out behind the Engine's one sink. `Publish` is one non-blocking enqueue
from the tick; delivery runs on the Hub's goroutine, subscriber by subscriber, each behind a bounded
buffer (`events.subscriber_buffer`). A subscriber that stops reading is dropped with a
`SubscriberDropped` Event as its last and counted; the tick never waits. An observer is a Room, an
Entity, and/or World visibility — World requires `game_master` or `operator` and is audited
(`subscribe_world`) as a privileged read. An observer bound to an Entity follows it inside the Hub,
in Event order — `CharacterLeft` addressed to it clears the Room, `CharacterArrived` sets it — so a
Session's perception is never its own read latency behind the sim, and in cross-Zone transit it is
in no Room, which is where the Character is. `client_ref` is handed to the Session whose Command
caused the Event and blanked for every other recipient, here rather than in each transport. A subscription registered while tick *T* is publishing
starts at *T+1*, never a partial tick. `SimulationStopped` reaches everyone in the form their
privilege allows, then every subscription ends with reason `shutdown`. The Kafka producer stays
independent: a broker outage counts `andara_tick_publish_failures_total` and the Hub keeps
delivering.

Every log record is marshaled deterministically, log.v1 has no `map` or float fields (asserted by a
descriptor walk in `make test`), and `andara.events.v1` records now carry the Scope.

`andara-cli sim repl --tap-events town/hall,docks/pier,world` prints, beside the Character's own
stream, what observers elsewhere are sent — the scoping watched from two Rooms at once.

### Event egress (AW-SRV-011)

`server/egress` is what `Game.Subscribe` plugs into: the Session's side of the stream, between the
fan-out's buffer and the client's socket. A Session that subscribes gets **one Hub subscription for
as long as it lives**, read by its own goroutine — the pump — into a ring of what the Session has
been sent, `egress.resume_window` deep. The stream is a cursor over that ring. So:

- **Resume.** A stream that ends and is reopened with `last_event_id` continues from the next
  retained Event with no gap and no duplicate, including Events that arrived while no stream was
  open — the pump kept retaining. A resume point the window no longer reaches, or one this server
  never sent the Session (a fresh process, a Session whose perception was rebound), opens the
  stream with a `Resync` frame (`reason` `resume_window_exceeded` or `no_history`) and then runs
  live: the client rebuilds its view with a `look`. A silent gap is never sent.
- **Backpressure.** A client that stops consuming leaves the cursor behind; when it trails by more
  than `egress.buffer` the stream is ended with `RESOURCE_EXHAUSTED` (`buffer_full`), never by
  skipping an Event. The warn line names the Session, `buffered`, and `last_sent`. If the writer is
  blocked inside a Send at that moment the stream is reset from the pump's goroutine
  (`gateway.AbortStream`: a write deadline in the past, `RST_STREAM` on HTTP/2), which returns a
  writer held by the client's flow-control window while the connection and its other streams
  survive. A client that has stopped reading its **socket** cannot be reached that way — the
  connection's writer is blocked in the kernel with every frame behind it — so a reset that has not
  returned the writer within `egress.heartbeat_interval` closes the connection
  (`gateway.DropConnection`): the Session is disconnected as a pulled cable would disconnect it,
  and nothing else on the server notices. Either way memory attributable to the Session is the ring
  plus the Hub buffer, whatever the client does.
- **Heartbeat.** A stream with nothing to send for `egress.heartbeat_interval` sends a `Heartbeat`
  frame carrying the last Tick the server has seen, so a quiet World and a dead connection look
  different, and an advancing Tick says the simulation is running.
- **World visibility** is asked for per stream (`SubscribeRequest.world`), refused with
  `PERMISSION_DENIED` (`world_visibility`) without `game_master` or `operator`, and audited once
  by the Hub at subscribe (`subscribe_world`). A Game Master who does not ask perceives from their
  Character like anyone else.
- **One stream per Session.** A second `Subscribe` while one is open is `FAILED_PRECONDITION`
  (`already_subscribed`); a client wanting a new stream ends the old one.
- **Perception** comes from the routing table: `ingress.Bindings` knows which Character a Session
  drives and the Room it was last seen in, and tells the egress when a binding changes
  (`Bindings.OnChange` → `Egress.Rebind`), which replaces the Session's Hub subscription and
  discards the old perception's history. Until `AW-SRV-014` binds Characters on a running server,
  every Session perceives from nowhere and a stream carries heartbeats and World-scope Events only.
- **Drain.** The gateway ends every stream with `UNAVAILABLE` (`server draining`), and a stream
  the Hub ends at shutdown is `UNAVAILABLE` (`draining`) too.
- **Revoked.** A Session the recheck loop closes (`AW-SRV-008` AC-12) has its stream told first:
  the gateway calls `Egress.EndSession` before it cancels the Session, the stream's last frame is
  `SubscriberDropped{reason=revoked}`, and it ends `PERMISSION_DENIED` (`revoked`). A code a seam
  chose itself stands in the gateway even when the Session has since closed.

The tick never passes through here: `Publish` is the Hub's one enqueue, the Hub fills the
subscription buffer, the pump drains it, and the stream writes on its own goroutine. The Hub's
`buffer_full` drop now guards the pump — a process that cannot keep its own goroutines fed — not
the client; when it happens the stream ends `buffer_full`, the Session's history is discarded, and
the next `Subscribe` starts over.

**Two moments, in order, on a Session's first `Subscribe`:** the Hub subscription is made first —
`andara_subscribers` rises, the pump starts retaining — and the stream attaches to the ring after
it, at which point `andara_stream_subscribers` rises. A stream opened with `last_event_id` 0 starts
*from now*, meaning from the moment it attaches: an Event the pump retained in the gap between the
two moments is history to that stream, not backlog, and is never sent to it. The gap is
microseconds on a running server and matters to nobody but a test. **A test that emits right after
subscribing and expects the stream to carry the Events waits on the egress's `Streams` gauge
(`andara_stream_subscribers`), never on the fan-out's `Subscribers`** — the fan-out's count is
satisfied before attach, so a stream that loses that race sees nothing, and a test waiting on
what it would have sent (a `buffer_full`, an abort) waits forever. That is how
`TestEscalation_DisconnectsWhenResetDoesNotReturn` hung CI for `go test`'s ten-minute limit on
2026-09-21 (PR #39, fixed in `e1622aa`); the eleven waits in `server/egress/egress_test.go` that
precede an emit read `Streams` since.

| Condition | gRPC code | `ErrorInfo.reason` |
|-----------|-----------|--------------------|
| client trailed by more than `egress.buffer` | `RESOURCE_EXHAUSTED` | `buffer_full` |
| Session revoked | `PERMISSION_DENIED` | `revoked` |
| a stream already open on the Session | `FAILED_PRECONDITION` | `already_subscribed` |
| `events.max_subscribers` reached | `RESOURCE_EXHAUSTED` | `too_many_subscribers` |
| `world` without the role | `PERMISSION_DENIED` | `world_visibility` |
| fan-out shut down | `UNAVAILABLE` | `draining` |

`docs/specs/slo/session-availability.md` is the SLO; `docs/runbooks/sessions-dropping.md` the
runbook for `SessionsDroppingAtRate`.

#### Egress metrics, logs, and traces

| Metric | Type | Labels | Cardinality bound |
|--------|------|--------|-------------------|
| `andara_stream_subscribers` | gauge | — | 1; open `Subscribe` streams |
| `andara_stream_events_sent_total` | counter | `type` | the EventType enum + `heartbeat`, `resync` |
| `andara_session_egress_drops_total` | counter | `reason` | `buffer_full`, `client_gone`, `draining`, `revoked` |
| `andara_sessions_in_drop_state` | gauge | — | 1; Sessions whose last stream the server ended (`buffer_full`, `draining`) and that have not reopened one — the SLI's unavailable Session-seconds |
| `andara_stream_buffer_depth` | histogram | — | 1; a stream's unsent count when an Event was appended for it, across Sessions |
| `andara_stream_resyncs_total` | counter | `reason` | `resume_window_exceeded`, `no_history` |

The fan-out's series (`andara_subscribers`, `andara_event_fanout_duration_seconds`, the
`event.fanout` span) are the egress's fan-out numbers too; they are not duplicated. Logs: `stream
ended: client not reading, buffer full` at `warn` with `session_id`, `buffered`, `last_sent`,
`tick`, `trace_id`; `stream reset did not return: client not reading its socket; closing the
connection` at `warn`; `stream resync: resume point not retained` at `info` with `last_event_id`
and `reason`; the Hub's privileged-read line for World streams. The `Game/Subscribe` span carries
`stream.world`, `stream.last_event_id`, `stream.resumed` or `stream.resync`; no per-Event span.

### Event metrics, logs, and traces

| Metric | Type | Labels | Cardinality bound |
|--------|------|--------|-------------------|
| `andara_events_emitted_total` | counter | `type` | the EventType enum |
| `andara_event_fanout_duration_seconds` | histogram | — | 1; per batch, outside the tick |
| `andara_subscribers` | gauge | — | 1 |
| `andara_subscriber_drops_total` | counter | `reason` | `buffer_full`, `unsubscribed`, `shutdown` |
| `andara_event_scope_redactions_total` | counter | `type` | the EventType enum |
| `andara_event_fanout_dropped_total` | counter | — | 1; above zero is a process problem |
| `andara_event_publish_lag_seconds` | gauge | — | 1; boundary publish → ack |

Logs: `subscriber dropped: buffer full` at `warn` with `subscription_id`, `reason`, `buffered`,
`tick`; `world-scope subscription: privileged read` at `info` naming the actor and Session. Spans:
`event.fanout` per batch with `event_count`, `subscriber_count`, `tick`; per-Event spans are not
emitted.

## Logs (AW-SRV-001, AW-SRV-024)

Every line goes to stderr as JSON and, when `telemetry.otlp_endpoint` is set, to the collector
over OTLP as the same record — a `slog` fan-out handler in front of the stderr handler and the
`otelslog` bridge, so `service`, `env`, and every attribute reach both. A line emitted inside a
span carries `trace_id` as an attribute (what stderr shows) *and* as the OTLP record's trace
context, which is what makes Loki's trace-to-logs link resolve in Tempo. In Loki the message is
the body, the level is `severity_text`, and every other field is structured metadata under its
own name: `{service_name="andara-server"} | session_id="<id>"`.

The export queue is bounded (2,048 records, flushed every second or at 512) and never blocks a
caller: with the collector away, records are dropped and counted in
`andara_log_export_dropped_total`, `andara_log_export_queue_size` shows what waits, the exporter's
own failure goes to stderr alone at `warn` once a minute (an exporter that logs through itself is
a loop), and the process neither stalls nor grows. The exporter's own retry is bounded (5 s per
attempt, 15 s elapsed), so a dead collector is reported within seconds rather than after the OTel
default minute of backoff. `make stack-smoke` asserts a real Session's
line in Loki matches its stderr line and follows its trace into Tempo; the stack workflow stops
the collector for a minute and asserts the same afterwards.

## Accounts and authentication (AW-SRV-008)

`server/auth` owns who a caller is and what they may do. Account state is **not** World state
(ADR-0006): it lives on `andara.accounts.v1`, compacted, keyed by `account_id` plus one
`config/registration` key, written only by this process and replayed into an in-memory index at
boot. Nothing under `server/sim` can reach it, and no credential rides in a Snapshot.
`andara.audit.v1` gets one record per privileged action, keyed by actor. `server/recordlog` is the
adapter behind both: `Memory` for `auth.store=memory` and tests, `Kafka` (franz-go, `acks=all`,
idempotent producer) for the broker. The topics must already exist — `make topics-apply` — and
`make up` starts the server only after they do.

**Credentials.** Passwords and agent API keys are Argon2id with the parameters stored beside
the hash; a login under stale parameters rewrites the record with the current ones. A
`WORKLOAD_JWT` agent Account holds no secret at all: the projected service-account token is
verified against `auth.k8s_jwks_url` (RS256 or ES256, `iss`, `exp`, `nbf`) and its `sub` must
equal the Account's `workload_subject`. Failed authentication is `UNAUTHENTICATED` with one
message whether the username is unknown or the password wrong, and the two paths cost the same —
an unknown username is verified against a dummy credential. Rate limiting is a token bucket per
username and per peer address, never a lockout.

**Tokens.** A session token is `base64url(payload).base64url(HMAC-SHA256)` signed with the
first key in `auth.token_key_file`, carrying `account_id`, `exp`, `kid`, and a nonce — never
roles. Roles and status are read from the index every time a token is verified, so disabling an
Account takes effect on its next `OpenSession` and, for Sessions already open, within
`auth.recheck_interval` (closed with outcome `revoked`). Session tokens are stateless and survive
a restart. A refresh token is 32 random bytes; only its SHA-256 is stored, `Refresh` rotates it,
and presenting a revoked one is refused and audited.

**Registration.** `closed` (the default and the only mode with no `AuthConfig` record on the
topic): `Register` is `FAILED_PRECONDITION` and Accounts come from `Admin.CreateAccount`. `invite`:
a valid unredeemed Invite Code is required; the code is burned on the issuer's record, durably,
before the new Account is written, under the one write lock — fifty concurrent presentations of
one code yield one Account. `open`: self-service. `Admin.SetRegistrationMode` writes the switch
to the topic; it is not a deploy.

**The first operator.** Every Admin RPC requires `operator`, so the first one cannot be created
through Admin. `auth.bootstrap_operator` (`username:password`) creates it at boot when the index
holds no operator, and is ignored once one exists — it can stay configured without being a back
door. Rotate the password with `Admin.ResetPassword` afterwards. `make up` sets
`operator:andara-local`; the chart injects it from `secrets.bootstrapOperator`.

**Acting as.** `OpenSessionRequest.act_as_account_id` lets an `operator` or `game_master` open
a Session as another Account: the Session gets the target's roles, and every audit record in it
carries both `actor_account_id` and `acting_as_account_id`. Anyone else setting the field gets
`PERMISSION_DENIED`, audited.

**Authorize.** `auth.Authorizer` is the `authorize` stage's decision: a verb table column
(`VerbRoles`, verb → required role; roles are a set, not a ladder) and the agent scope check
(`AuthorizeBind`: an agent may bind only Entities whose Template comes from its own pack). A
rejection is `ErrNotAuthorized` → `PERMISSION_DENIED`, audited with actor, verb, and Session,
and consumes no log offset. `AW-SRV-010` calls it from `Submit`.

### Key rotation

`auth.token_key_file` is one `key_id: base64` per line. The **first** line signs; every line
verifies. Rotation never invalidates a token in flight:

1. Add the new key **below** the current one. Deploy. Tokens still sign with the old key; both verify.
2. Move the new key to the **first** line. Deploy. New tokens sign with the new key; old ones still verify.
3. After `auth.session_ttl` has elapsed since step 2, remove the old key. Deploy.

A key is at least 32 bytes (`openssl rand -base64 32`). Key IDs contain no spaces or dots and
are never reused. Locally, `make auth-keys FORCE=1` generates a fresh single-key file, which
invalidates every local token — fine for a development stack, which is why it is not the
procedure above. In the chart the file is `secrets.tokenKey`, mounted `0400` at
`/etc/andara/secrets/token-key/token.keys`; with `fsGroup` set the kubelet presents it `0440`,
which the server accepts.

### Auth metrics, logs, and traces

| Metric | Type | Labels | Cardinality bound |
|--------|------|--------|-------------------|
| `andara_auth_attempts_total` | counter | `outcome` | `ok`, `bad_credential`, `rate_limited`, `disabled` |
| `andara_auth_verify_duration_seconds` | histogram | — | 1 — the Argon2id cost as seen in production |
| `andara_registrations_total` | counter | `mode` | `closed`, `invite`, `open` |
| `andara_invite_redemptions_total` | counter | `outcome` | `ok`, `invalid`, `race_lost` |
| `andara_privileged_actions_total` | counter | `action` | the audited action list in `server/auth/audit.go` |
| `andara_accounts_total` | gauge | `role` | the five roles; an Account holding two counts under both |
| `andara_audit_write_failures_total` | counter | — | 1; anything above zero is an operator page |

Account ID, username, token, and hash are never labels, never span attributes, and never in
an error message; `TestNoSecretLeaks` plants every secret the flows produce and scans. Logs:
`authentication attempt` at `info` with `outcome`, and `account_id` on success only — never the
username on a failure, which may be a password typed in the wrong box; `privileged action` at
`info` with `actor_account_id`, `acting_as_account_id`, `action`, `target`, `outcome`. Spans:
`session.authenticate` under the `OpenSession` RPC span (which `session.lifetime` links to);
`auth.verify_credential` under `Authenticate` with `argon2.memory_kib`.

## The roster and the binding (AW-SRV-014)

`server/roster` is the Gateway's side of a Session entering the World. `Game.ListCharacters`,
`CreateCharacter`, and `SelectCharacter` take a `session_id` like `Submit`; every refusal carries
`ErrorInfo{domain: andara.character, reason}`: `roster_full` (`RESOURCE_EXHAUSTED`), `name_taken`
(`ALREADY_EXISTS`, one message whatever the cause), `name_invalid` (`INVALID_ARGUMENT`, naming the
rule), `already_live` (`FAILED_PRECONDITION`, naming the live Character), `no_such_character`
(`NOT_FOUND`). A produce failure on select is answered as `Submit` answers it.

A Character's identity is Account state — `Account.characters` and a `name/{fold}` reservation
written first on `andara.accounts.v1`, both under the store's single-writer lock — and its body is
World state: an Entity made from `andara.core.Character` whose `EntityID` is the `character_id`
and whose display name is carried on it. `SelectCharacter` sets the Account's **live flag** (one
live at a time; Session state in process memory, ADR-0001), binds the routing table with the
roster's last-known Zone and Room so the stream perceives from the Room the body will appear in,
and produces `BindCharacter` to that Zone's Partition; the response is the offset. The tick
materializes the body — at the spawn Room when never bound, where it went dormant otherwise, and
untouched when a crash left it present — and emits `CharacterArrived` with an empty
`from_direction`, to the Room when the body arrives in it and **to the Character alone when the
body was already present**: the Room never saw it leave, and the Session still has to learn where
it actually stands, which after a crash need not be where the roster last wrote.

The live flag and the Session's teardown are ordered by the roster's lock: `close` sets the
Session's `Closing` before it tells the roster, and the roster reads it under the lock it
registers under, so a `SelectCharacter` that resolved its Session a moment before the teardown is
refused (`CANCELED`) rather than leaving a flag nothing will clear.

A bound Character that crosses a Zone moves the roster with it — the Gateway already watches
those arrivals to route the Session's Commands — so a crash leaves the roster naming the wrong
Zone only if it lands inside that window. The write is best-effort and unordered: it is dropped
when too many are in flight, and nothing sequences it against another or against the teardown's,
so the roster can end up naming an older Zone. That is exactly the stale roster the sim's
re-route exists for, and `spawn_room_id` is ignored for a body that exists; nothing may be built
on the roster's position being current.

A Session's end, however it ends, produces `UnbindCharacter{QUIT}` on its own context bounded by
`ingress.produce_deadline`, records the roster's position from the routing table, and frees the
flag; the tick makes the body **dormant** — in no Room's occupants, invisible to `look`, acting
for nobody — and emits `CharacterLeft` with an empty `to_direction`. Until the teardown has
produced, the Character is `already_live` to the same Account, a reconnecting client included.
Nothing at boot invents an unbind: a body left present by a crash is taken where it stands by the
next select. A `BindCharacter` the roster routed to the wrong Zone is re-produced by the sim to
the Zone that holds the body.

**A drain, and what it leaves.** Every bound Session is closed, so every teardown runs; the
produces go concurrently, one per Session, so a drain costs about one `ingress.produce_deadline`
in wall clock however many Sessions are bound, and `CloseIngress` waits for them before the
producer closes. A clean drain therefore leaves its bodies **dormant**. With the broker
unreachable every one of those produces fails instead — counted on
`andara_character_unbinds_total{reason="quit",outcome="produce_failed"}`, each with a `warn` line
naming the Session and the Character — and the restarted World holds those bodies **present with
no Session**: the next `SelectCharacter` takes each where it stands, and until then they stand in
their Rooms and are listed by `look`.

The boot requires `character.spawn_room` to resolve and `andara.core.Character` to be loaded. The
dev World (`testdata/content/valid`) carries the core pack under `templates/`; the kind chart
mounts it from `contentVolume.templatesConfigMapName`.

### Roster metrics, logs, and traces

| Metric | Type | Labels | Cardinality bound |
|--------|------|--------|-------------------|
| `andara_characters_total` | gauge | `state` | `present`, `dormant` — bodies in Zone state, set by the loop after recovery and after every tick that applied a Command; a present body with no Session counts as present |
| `andara_sessions_bound` | gauge | — | Sessions whose `BindCharacter` is in the log and whose teardown has not run; `present − bound` is the bodies no Session drives |
| `andara_character_creations_total` | counter | `outcome` | `ok`, `roster_full`, `name_taken`, `name_invalid` |
| `andara_character_bindings_total` | counter | `outcome` | `ok`, `already_live`, `race_lost` (lost to a select whose produce was still in flight), `not_found`, `produce_failed` |
| `andara_character_unbinds_total` | counter | `reason`, `outcome` | `quit` × `ok`, `produce_failed` |

Character name and Account ID are never labels. Logs at `info`: `character created`,
`character selected` (with `zone`, `partition`, `offset`), `character unbound` (with `reason`,
`outcome`, `zone`, `room`), each with `account_id`, `character_id`, `session_id`, `trace_id`; the
name appears quoted as a value on `created`, never as a key. A failed teardown produce is `warn`
with the same fields. Spans: `character.create` and `character.select` under the RPC span, linked
to `session.lifetime`; `log.produce` under `select`; the tick's `command.apply` for the
`BindCharacter` joins through the record's `trace_id`; `character.unbind` is a root linked to the
Session.

`canonical.Marshal` (`server/canonical`) is the encoder for anything that feeds the State Hash
(ADR-0007 rule 3): deterministic protobuf, and it refuses a message whose descriptor contains a
`map`, a `float`/`double`, or `google.protobuf.Any` rather than encode it non-canonically.

`CanonicalBytes(*World)` serializes topology in a stable order — zones by ID, rooms by ID, exits by
Direction, Components by type and their fields by name — with free-text fields escaped so the
encoding is injective. It is how AW-SRV-001 AC-10 is asserted, and it is deliberately not ADR-0002's
State Hash, which covers mutable state and arrives with `AW-SRV-002`.

## Content validation (AW-SRV-001, AW-SRV-021, AW-SRV-022)

`sim.BuildWorld` is the one validator — the same code serves boot, `content validate`, and the
publish path (ADR-0004). It returns every finding, never only the first, so a Builder fixing ten
broken Exits needs one boot rather than ten.

**Findings that refuse the load** (the process exits 1 and `/readyz` stays 503):
`unknown_room`, `unknown_zone`, `duplicate_room`, `duplicate_zone`, `unsupported_format_version`,
`malformed_file`, `no_zones_found`, `unknown_direction`, `unknown_component_type`,
`duplicate_component_type`, `invalid_component_field`, `fallback_missing` (a Zone with no
`fallback_room`, or one naming a Room it does not contain, AW-SRV-012 AC-10); and for Templates `unresolved_extends`,
`unflattened_template`, `duplicate_template`, `chain_mismatch`, `chain_too_deep`,
`invalid_provenance`.

**Findings that are advisory** — the World loads and the process serves, and each is logged at
`warn`: `missing_reverse_exit`, and `orphan_room` unless `content.strict_orphans` is set, which
promotes it to a refusal.

**Directions are a closed set** — the twelve in `docs/glossary.md`, each with a reverse. It is
enforced here rather than as a protobuf enum: an unknown enum member is dropped silently on the wire,
where a rejected string names the file and the line. An Exit whose target has no Exit back along the
reverse Direction is a `missing_reverse_exit` warning; one-way Exits are legal, and silent ones are
not. Growing the set is a glossary edit plus `canonicalDirections` in `server/sim/direction.go` plus
a content revalidation — never a schema change.

**Component types are a closed, server-defined table** (`componentRegistry` in
`server/sim/component.go`, ADR-0010 decision 7). Content composes from it; a type outside it is a
load error, and adding one is a code change and a release. A registry entry declares its fields by
name and kind, so a field the entry does not declare is also a load error rather than something the
State Hash covers and nothing reads.

**Templates are loaded flattened and never resolved here** (`sim.BuildTemplates`, ADR-0010
decision 9). A pack keeps one `TemplateDefinition` per declaration at `templates/<name>.json` — in
dir mode a `templates/` subdirectory of `content.path`, which may be absent; a World of Rooms with
nothing in them is still a World. The loader checks what the compiler emitted rather than
recomputing it: every Component type is registered; the chain is the parent's chain plus the
Template itself, at most `sim.MaxChainDepth` (16) long, and of one kind; every ancestor is in the
same pack or in the core pack (`andara.core`) and the loader resolves across no other pack boundary;
everything an ancestor carries is present, because a subtype may override and extend but never
remove (decision 5); provenance names real fields and chain members; names are unique per pack. A
definition with `resolved: false` is `unflattened_template` — the server carries no resolver. The
registry is `Runtime.Templates`; `sim.Instantiate(t, id, contentVersion)` makes an `EntityState`
that names its Template (and so its pack) and the content version it came from, with its Components
sorted by type, deterministically.

`content/core/templates/` is the `andara.core` seed in compiled form — `Entity`, `Character`,
`Npc` (carrying `andara.core.Memory`), and `Item`, which is its own root because a chain has one
kind. `testdata/templates/` carries a byte-identical copy, plus a `town` pack, and
`TestCoreSeedMatchesFixture` holds the two together.

### Content in effect and the swap (AW-SRV-012)

The log is the source of the content a World runs on. Every version enters through a
`ContentSwap` Command (`log.proto`), the first included, and replay reads which content each tick
ran on the way it reads tick boundaries. The Engine holds the versions in effect and their digest
(`Engine.Content`), and starts with none: `sim.EmptyWorld`, no Zones, until the first swap applies.

**`world_digest`** is `sim.ContentDigest`: SHA-256 over `CanonicalBytes(world)` followed by
`TemplatesCanonicalBytes(templates)`. It covers the whole World after the swap, every pack's Zones
and Templates, not only the swapped pack's. So the second of two swaps in one tick digests a World
that includes the first, and a divergence in any pack is caught. `CanonicalBytes` records each
Zone's `fallback_room`. `TemplatesCanonicalBytes` orders Templates by reference and records kind,
chain, Components and provenance. A Template's blob path and `source` are left out, because they are
provenance and not content, and the same content mounted at another path must digest the same. The
digest covers topology only. Entity state is the State Hash's.

**Applying a swap.** `Engine.Step` holds a tick's `ContentSwap` records out of the per-Partition
loop and applies them after every other record of the tick, in (Partition, offset) order. Every
Command of tick *T* sees the old content, and every Command of *T+1* the new. Before the tick
mutates anything, each swap is decided against the content the one before it leaves:

- **Refused — a deterministic no-op**, the same live and on replay (`StepResult.SwapsRefused`,
  `sim.SwapRefused`). The record is consumed and nothing else changes. Four cases:
  - a swap not on `sim.WorldPartition`, or with a `zone_id` (`misrouted`);
  - one whose `base_digest` is not the content in effect (`stale_base`), meaning it was built on a
    World the log has since moved past;
  - one whose World removes a Zone the content in effect has (`zone_removed`);
  - one with a Zone whose `fallback_room` is not its own (`fallback_missing`).
  The content source is told and re-evaluates. Removing a Zone is refused at the Loader too, since
  deleting one needs an evacuation policy, which is a later story.
- **Halted.** A swap on the right base whose `world_digest` its version no longer builds is
  `sim.ErrContentDigest`: the content itself differs, so the whole Step is refused with the state
  untouched, as a State Hash mismatch halts recovery.

Applying a swap:
- Zones the new content adds get empty state.
- An Entity in a Room the new content removed moves to its Zone's `fallback_room`. A present one
  emits `EntityRelocated{zone_id, entity_name, from_room_id, to_room_id, reason: "room_removed"}`,
  scoped to the fallback Room and the moved Entity. A dormant body moves silently, so "where you
  were" stays a Room that exists.
- An Entity in transit whose target Room a swap removed lands in the target Zone's
  `fallback_room` on arrival, with the same `EntityRelocated`. It is never bounced or lost.
- Two swaps in one tick apply one after the other, so an Entity can be relocated twice in that
  tick, with an Event for each.

A swap changes what future spawns are, never what an existing body is. A Character is an Entity
instantiated from `andara.core.Character`, read from the Entity and not from the registry, and a
new body records the `pack@version` of its Template's pack in effect.

`StepResult.Swaps` reports each applied swap with its relocations. The events hub, the ingress
bindings and the state projector's `Touched` table follow `EntityRelocated` as they follow an
arrival, so a relocated Character perceives and is routed from the fallback Room.

**Where swaps come from.** The `content.Loader` (Kafka) and the dir source are the Engine's
`sim.ContentSource`, and **serving means applied**: a version is serving, and
`andara_content_active_version{pack}` moves, only when the Engine applies its swap, never when the
Loader accepts it or the produce is acknowledged. The Loader resolves, validates against the
content in effect, builds and digests a version off the tick, and stages the build under exactly
the versions it assumes. It then produces the `ContentSwap` through the Gateway's producer, with
an empty `zone_id` and the digest it was built on as `base_digest`, to `sim.WorldPartition` (0).
It waits for the swap to apply or be refused before it handles the next move:
- A swap refused as stale is evaluated again against what is now in effect, up to three times.
- An ambiguous produce (`ingress.Unsettled`) is never counted as not written until it settles.
  One whose outcome stays unknown waits until the World Partition is consumed past it, then knows.
- The wait for apply is bounded at `content.reload_debounce` × 15 (30 s by default). A faulted
  Partition 0 then costs one move `store_unavailable`, which is retried, and the rest keep
  draining.

Two more versions are refused, both under `validation` and so the Builder's: one that removes a
Zone in effect (`zone_removed`), and one whose World loses `character.spawn_room`
(`spawn_room_removed`). On replay, or for a swap this process did not produce, `Prepare` resolves
every version named from the store and builds it. A version that no longer builds halts the tick.

**Boot.** `LoadContent` validates the *candidate* content (what the source names now) but brings
nothing into effect. For `kafka` a candidate that cannot load is logged and not fatal: the log may
hold content that does, and a bad activation must not survive a restart as an outage. For `dir` it
is fatal. `StartTickLoop` builds the Engine
from `sim.EmptyWorld` and recovers: content is built only from the swaps the log replays, and the
Loader follows each one. A log that applied a Command while no content was in effect predates this
rule and is refused (`boot.ErrPreRuleLog`, exit 1): recover onto a fresh log (`make down VOLUMES=1`
locally, fresh topics for `dev`). With the loop running, `ReconcileContent` brings the World to the
source through the log. It first waits until the World Partition is consumed to its end, because a
swap a previous process produced may lie past the last boundary. That wait is bounded like the wait
for apply. If it runs out, as with a Partition 0 frozen by a Zone fault, every pending pack is
`store_unavailable` and deferred to `Follow`'s retry, and the boot carries on with what the log
recorded. On an empty log that's
**genesis**: one swap per followed pack, `andara.core` first, then by `pack_id`. After a restart
it's whatever moved while the process was down. A boot that ends with no Zones in effect exits 1.
The roster's spawn Room is checked against that content, the Gateway starts, and only then is the
process ready (`/readyz` 200). `FollowContent` then applies Active Pointer moves for as long as the
process runs, starting with a retry of anything reconcile could not load for the store's sake. A
pack in effect that `content.packs` no longer names is logged at `warn`.

A pre-rule log is refused as `boot.ErrPreRuleLog` either way it shows itself: as a Command applying
with no content in effect, or, more often, as a State Hash mismatch before any content is in
effect (`boot.RecoveryError`). The mismatch alone isn't enough, because a post-rule log also runs
idle ticks before genesis, and a changed `sim.seed` mismatches there too. So it counts as pre-rule
only when the World Partition carries no `ContentSwap` at all. Swaps refused in the log's history
are not reported again on replay.

**`content.source=dir`** is one pack, `dir`, at version 0. Its genesis swap carries the directory's
digest, and recovery rebuilds the directory and compares it. So a directory changed while the
server was down halts recovery with a digest mismatch. It never replays silently over different
content. Reset the log to start over.

**A load the store could not serve** (`store_unavailable`, which includes a swap the command log
would not take) is retried from 1 s, doubling to 30 s, until it loads or the pointer moves again.
Every other refusal holds the version until the next move.

**Snapshots** carry the content in effect at their tick (`SnapshotEnvelope.content`,
`content_digest`, `sim.Snapshot.Content`). `sim.RestoreEngine` checks the digest of the topology
it's given before loading any body, and `sim.PrepareContent` rebuilds a round's content through a
`ContentSource`. The state projector restores and replays through both. `Admin.GetServerInfo`
reports the same: `content` (one entry per pack, sorted) and `content_digest`. The deprecated
`content_pack_id`/`content_version` fields hold `andara.core`'s version.

### Content-load metrics

| Metric | Type | Labels | Cardinality bound |
|--------|------|--------|-------------------|
| `andara_content_zones_loaded` | gauge | — | 1 |
| `andara_content_rooms_loaded` | gauge | `zone` | Zones in the content set |
| `andara_content_load_duration_seconds` | histogram | — | 1 |
| `andara_content_validation_errors_total` | counter | `code` | the `ErrCode` set, closed in `server/sim/errors.go` |
| `andara_content_components_total` | counter | `component_type` | the Component registry, closed by construction |
| `andara_content_load_warnings_total` | counter | `kind` | `orphan_room`, `missing_reverse_exit` |
| `andara_content_templates_loaded` | gauge | `pack` | the Content Packs the server follows |
| `andara_content_active_version` | gauge | `pack` | the packs followed; moves when a swap applies |
| `andara_content_pending_seconds` | gauge (computed at scrape) | `pack` | the packs followed; seconds since the pointer moved to a version neither in effect nor refused for a Builder's reason (`validation`, `fallback_missing`, `pack_mismatch`, `blob_too_large`), else 0. The SLI of `docs/specs/slo/content-freshness.md` |
| `andara_content_load_failures_total` | counter | `reason` | the ten reasons in `server/content/errors.go` |
| `andara_content_load_phase_duration_seconds` | histogram | `phase` | `resolve` (reading the version), `build` (building the World and Templates, which is where their findings come from), `validate` (that build plus the checks against the content in effect), `swap` (the in-tick apply) |
| `andara_content_reload_stall_seconds` | histogram | — | 1; the in-tick cost of applying one swap (AC-9: under `sim.tick_budget_ms / 2`). Preparing it is in-tick too and not in this metric: checking the base, the Zones and the fallbacks, and the whole-World digest, about 1 ms at the sizing fixture. So is a stage miss, where `Prepare` resolves from the store on the tick goroutine; that happens only on replay or for a swap this process did not produce |
| `andara_content_relocations_total` | counter | `zone` | Zones in the content set |
| `andara_content_cache_hits_total` | counter | `outcome` | `hit`, `miss` |
| `andara_build_info` | gauge, always 1 | `version`, `commit`, `env`, `pack`, `content_version` | one series per pack in effect; before any content, one with `pack` and `content_version` empty |

Warnings appear in both counters: `validation_errors_total` counts every finding by code,
`load_warnings_total` counts only the advisory ones, so an operator can ask whether a content pack is
sloppy without knowing which codes happen to be advisory. `room_id` is a label on neither — that is
the unbounded cardinality CLAUDE.md §7 rejects on sight, and it lives on the log line instead.

Every rejection and every warning logs one structured line carrying `code`, `file`, `line`,
`zone`, `room`, `template`, `detail`, and `trace_id`; each pack's Template count logs at `info`. Component and Direction validation are attributes on the
existing `content.load` and `content.validate` spans (`component_count`, `error_count`,
`warning_count`), not spans of their own: a span per Room would be one span per Room.

A swap (AW-SRV-012) is traced as `content.load` → `content.resolve`, `content.validate`,
`content.build` in the Loader, and `content.swap` in the tick that applies it, always kept. Boot's
reconcile is `content.reconcile`. It logs `content version accepted; swap produced` (pack,
version, core_version, zones, templates, world_digest, duration_ms), `content swap applied`
(stall_ms, trace_id), and `content in effect` when the swap applies. A relocation is a `warn` line
with `zone`, `entity_id`, `from`, `to` and `dormant`, and a refused swap is a `warn` naming its
reason. The swap `LoggedCommand` carries the load's W3C traceparent in `trace_id`, so
`content.swap` is a child of `content.load` and carries a link to the `sim.tick` that applied it.
A refusal is logged at `error`, ending "the previous version keeps serving". The runbook's query
matches that suffix, so a rewording keeps it.
