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
| `sim.seed` | `ANDARA_SIM_SEED` | derived | PRNG seed; `0` derives one from the World's topology. Overriding is a debugging affordance. |
| `sim.partitions` | `ANDARA_SIM_PARTITIONS` | `0-63` | Assigned Partitions: a range, or the comma list the chart's init container writes from the pod ordinal. |
| `sim.checkpoint_every_ticks` | `ANDARA_CHECKPOINT_EVERY_TICKS` | `100` | Offset commit cadence — a startup-cost knob, not a correctness one. |
| `events.subscriber_buffer` | `ANDARA_SUBSCRIBER_BUFFER` | `1024` | Events a subscriber may leave unread before it is dropped with `SubscriberDropped`. |
| `events.max_subscribers` | `ANDARA_MAX_SUBSCRIBERS` | `10000` | Event subscriptions this process accepts; registration past it is refused. |
| `command.max_intent_bytes` | `ANDARA_MAX_INTENT_BYTES` | `4096` | Largest Intent `parse` will read; over it is `intent_too_large` on the length alone, before tokenizing. |
| `command.verb_table_path` | `ANDARA_VERB_TABLE` | built in | JSON verb table that *replaces* the built-in one (`look`, `move`, the twelve Directions and their compass aliases). A file that does not parse fails the boot. |

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
parse, authorize, append, tick, print — which is how it is exercised before `AW-SRV-010` puts a
broker behind `Game.Submit`; `Ingress` stays `UNIMPLEMENTED` until then.

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
`duplicate_component_type`, `invalid_component_field`; and for Templates `unresolved_extends`,
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

Warnings appear in both counters: `validation_errors_total` counts every finding by code,
`load_warnings_total` counts only the advisory ones, so an operator can ask whether a content pack is
sloppy without knowing which codes happen to be advisory. `room_id` is a label on neither — that is
the unbounded cardinality CLAUDE.md §7 rejects on sight, and it lives on the log line instead.

Every rejection and every warning logs one structured line carrying `code`, `file`, `line`,
`zone`, `room`, `template`, `detail`, and `trace_id`; each pack's Template count logs at `info`. Component and Direction validation are attributes on the
existing `content.load` and `content.validate` spans (`component_count`, `error_count`,
`warning_count`), not spans of their own: a span per Room would be one span per Room.
