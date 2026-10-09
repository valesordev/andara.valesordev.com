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
server/boot/       load orchestration, /livez /startedz /readyz /metrics
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
| `content.max_blob_bytes` | `ANDARA_CONTENT_MAX_BLOB_BYTES` | `8388608` | Largest blob read or published; a larger one refuses its version `blob_too_large`, and a larger `PublishBlob` is refused before anything is produced. Kafka only. |
| `content.max_pack_bytes` | `ANDARA_CONTENT_MAX_PACK_BYTES` | `268435456` | Largest version accepted by `PublishVersion`, all its blobs together (`pack_too_large`). Kafka only. |
| `content.core_pack` | `ANDARA_CONTENT_CORE_PACK` | `andara.core` | The pack the server publishes at boot and no RPC may. It must name the core this build embeds. Kafka only. |
| `content.operator_self_approval` | `ANDARA_CONTENT_OPERATOR_SELF_APPROVAL` | `true` | An Operator may approve a version they published, flagged `self_approval` (ADR-0004, amended 2026-09-26). Off, it's refused like a Builder's. Kafka only. |
| `content.reload_debounce` | `ANDARA_CONTENT_RELOAD_DEBOUNCE` | `2s` | How long a burst of pointer moves is coalesced before it is applied. Kafka only. |
| `content.strict_orphans` | `ANDARA_STRICT_ORPHANS` | `false` | When `true`, Rooms with no inbound Exit in their Zone are errors. A Zone of one Room has no orphan: its Room is its fallback and entry, and nothing in the Zone could lead into it (`AW-SRV-034`). |
| `http.port` | `ANDARA_HTTP_PORT` | `8080` | `/livez`, `/startedz`, `/readyz`, `/metrics`. Plaintext, operator surface. |
| `telemetry.otlp_endpoint` | `ANDARA_OTLP_ENDPOINT` | `localhost:4317` | Traces and logs go to this collector over OTLP/gRPC (AW-SRV-024). Empty disables both exporters; an endpoint the exporter cannot be built for is fatal. |
| `telemetry.service_name` | `ANDARA_SERVICE_NAME` | `andara-server` | |
| `telemetry.environment` | `ANDARA_ENV` | `local` | |
| `telemetry.log_format` | `ANDARA_LOG_FORMAT` | `json` | |
| `telemetry.log_level` | `ANDARA_LOG_LEVEL` | `info` | |
| `grpc.listen` | `ANDARA_GRPC_LISTEN` | `:8443` | The Protocol endpoint: `Game` and `Admin`, gRPC, gRPC-Web, and Connect, TLS only. Flag `--grpc-listen`. |
| `grpc.tls_cert_file` | `ANDARA_TLS_CERT_FILE` | — | **Required.** PEM server certificate. Flag `--tls-cert-file`. |
| `grpc.tls_key_file` | `ANDARA_TLS_KEY_FILE` | — | **Required.** PEM private key. Flag `--tls-key-file`. |
| `grpc.max_recv_bytes` | `ANDARA_GRPC_MAX_RECV_BYTES` | `65536` | Largest request message accepted; over it is `RESOURCE_EXHAUSTED` before any handler runs. `Admin` reads up to 2 MiB, or this if larger, for the publish path's messages (AW-SRV-013). |
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
| `session.linkdead_grace` | `ANDARA_LINKDEAD_GRACE` | `180s` | How long a linkdead Character waits for a reconnect before it despawns (ADR-0006). Converted to Ticks at `sim.tick_rate` into each `MarkLinkdead`, so retuning it never moves a body already linkdead. **Must exceed `recovery.rto_target`**: every restart drops every Session. |
| `session.linkdead_combat_extension` | `ANDARA_LINKDEAD_COMBAT_EXTENSION` | `60s` | How far one combat interaction pushes a linkdead deadline: to `max(deadline, now + this)`. |
| `session.linkdead_max` | `ANDARA_LINKDEAD_MAX` | `300s` | ADR-0006's hard ceiling on linkdead duration, combat included. **At least `session.linkdead_grace`.** |
| `session.linkdead_detect` | `ANDARA_LINKDEAD_DETECT` | `5s` | How long a silent stream may miss its keepalive before its Session is marked linkdead. A transport close is detected at once. |
| `recovery.rto_target` | `ANDARA_RECOVERY_RTO_TARGET` | `60s` | `docs/specs/slo/recovery.md`'s target. Read only for the `linkdead_grace` assertion. |
| `sim.source` | `ANDARA_SIM_SOURCE` | `kafka` | `kafka` or `memory`. `memory` ticks the World with no Command input and publishes nothing; development only. |
| `sim.tick_rate` | `ANDARA_TICK_RATE` | `10` | Ticks per second (ADR-0008). 1..100. |
| `sim.tick_budget_ms` | `ANDARA_TICK_BUDGET_MS` | `50` | Overrun threshold — half the interval, so overruns warn before lag accrues. Must not exceed the interval. |
| `sim.max_per_tick` | `ANDARA_MAX_PER_TICK` | `1024` | Records applied per tick, taken round-robin across Partitions; the rest wait. |
| `sim.handoff_retry_ticks` | `ANDARA_HANDOFF_RETRY_TICKS` | `10` | Ticks before an unacknowledged cross-Zone `Arrive` is produced again: the first retry interval (1 s at 10 Hz). Greater than 0. Should exceed the broker round trip and stay below `ingress.transit_hold` once converted by `sim.tick_rate`; startup logs a `warn`, and never refuses, when it doesn't. |
| `sim.handoff_retry_max_ticks` | `ANDARA_HANDOFF_RETRY_MAX_TICKS` | `100` | The longest gap between attempts of one handoff (10 s at 10 Hz): the interval doubles from `sim.handoff_retry_ticks` up to this. At least `sim.handoff_retry_ticks`, so raising the first interval past 100 means raising this too. |
| `sim.handoff_retry_batch` | `ANDARA_HANDOFF_RETRY_BATCH` | `50` | The most `Arrive` retries produced in one tick, earliest due first, so a restart with many handoffs in flight doesn't fill `sim.max_per_tick` and defer players' Commands. Greater than 0. |
| `sim.drain_timeout_ms` | `ANDARA_DRAIN_TIMEOUT_MS` | `5000` | Shutdown budget for the in-flight tick, the checkpoint, and `SimulationStopped`; past it, exit 1 naming the tick. |
| `sim.seed` | `ANDARA_SIM_SEED` | derived | PRNG seed; `0` derives one from the topology the Engine starts with. Every Engine starts with no content (AW-SRV-012), so the derived seed is the same for every World; set it to tell Worlds apart. Overriding is a debugging affordance. |
| `sim.partitions` | `ANDARA_SIM_PARTITIONS` | `0-63` | Assigned Partitions: a range, or the comma list the chart's init container writes from the pod ordinal. |
| `sim.checkpoint_every_ticks` | `ANDARA_CHECKPOINT_EVERY_TICKS` | `100` | Offset commit cadence — a startup-cost knob, not a correctness one. |
| `events.subscriber_buffer` | `ANDARA_SUBSCRIBER_BUFFER` | `1024` | Events a subscriber may leave unread before it is dropped with `SubscriberDropped`. |
| `events.max_subscribers` | `ANDARA_MAX_SUBSCRIBERS` | `10000` | Event subscriptions this process accepts; registration past it is refused. |
| `command.max_intent_bytes` | `ANDARA_MAX_INTENT_BYTES` | `4096` | Largest Intent `parse` will read; over it is `intent_too_large` on the length alone, before tokenizing. |
| `command.verb_table_path` | `ANDARA_VERB_TABLE` | built in | JSON verb table that *replaces* the built-in one (`look`, `move`, the twelve Directions and their compass aliases, and `goto`, which needs `builder`). A file that does not parse fails the boot. |
| `ingress.rate_limit` | `ANDARA_INGRESS_RATE_LIMIT` | `20/s` | Submits per Session, `N/period`; `off` disables. Applied before parse. |
| `ingress.agent_rate_limit` | `ANDARA_AGENT_RATE_LIMIT` | `100/s` | The rate for `agent` Principals, which drive many NPCs per Session (ADR-0005). |
| `ingress.burst` | `ANDARA_INGRESS_BURST` | `40` | Token bucket depth per Session: how many Submits may arrive at once before the rate applies. |
| `ingress.produce_deadline` | `ANDARA_PRODUCE_DEADLINE` | `2s` | How long one produce may take. It is the client's record delivery timeout; a produce request times out at half of it, so one idempotent retry fits inside. |
| `ingress.max_pending` | `ANDARA_INGRESS_MAX_PENDING` | `256` | Submits one Session may have in flight; past it, `RESOURCE_EXHAUSTED` rather than a growing queue. |
| `egress.buffer` | `ANDARA_EGRESS_BUFFER` | `1024` | Events a Session's stream may leave unsent before the stream is ended with `buffer_full`. The Session survives; the client reopens the stream. |
| `egress.resume_window` | `ANDARA_EGRESS_RESUME_WINDOW` | `2048` | Delivered Events retained per Session, for a stream reopened with `last_event_id`. At least `egress.buffer`. A resume from before the window is a `Resync` frame. |
| `egress.assumed_event_rate` | `ANDARA_EGRESS_ASSUMED_EVENT_RATE` | `5` | Events/s per Session the resume window is sized for. **`egress.resume_window` must be at least `session.linkdead_max × this`**, or a reconnect near the end of the grace is a `Resync`. `andara_reconnect_resyncs_total` rising in production says the rate is too low. |
| `egress.heartbeat_interval` | `ANDARA_HEARTBEAT_INTERVAL` | `20s` | How long a stream may be silent before a `Heartbeat` frame is sent. Also how long a stream reset is given to return a blocked writer before its connection is closed. |
| `ingress.transit_hold` | `ANDARA_INGRESS_TRANSIT_HOLD` | `2s` | How long a Session's Intents wait for its Character to arrive in the next Zone, measured from the `CharacterLeft`. Past it they are rejected `in_transit`. A held Submit is also bounded by the RPC deadline (`grpc.max_request_timeout`). `0` holds nothing: any Submit during a transit, including the same-tick window of a same-Zone move, is `in_transit`. |
| `ingress.idempotency_window` | `ANDARA_INGRESS_IDEMPOTENCY_WINDOW` | `30s` | How long a Submit's outcome is remembered against its `(Session, client_ref)` once known, so a retry inside it is the same Command. It governs *resolved* keys; a key still in flight lives until its outcome is known, however long that takes. Must exceed `ingress.produce_deadline`. Per process; at most `ingress.max_pending` keys per Session, the oldest resolved one evicted first — a key still in flight is never evicted. |
| `character.max_per_account` | `ANDARA_CHARACTER_MAX_PER_ACCOUNT` | `5` | Characters an Account may hold (ADR-0006), ACTIVE or DELETED — a deleted one keeps its slot until purged (`AW-SRV-032`). A sixth is `RESOURCE_EXHAUSTED roster_full`. |
| `character.delete_retention` | `ANDARA_CHARACTER_DELETE_RETENTION` | `720h` | How long a deleted Character's dormant body stays before the Gateway's sweep produces its `PurgeCharacter` (30 d, `AW-SRV-032`). Judged on the wall clock by the Gateway; the purge applies when its Command does, so a replay purges on the same Tick. The name stays reserved after the purge, and the roster entry stays `DELETED`; only the slot is freed. |
| `character.purge_sweep_interval` | `ANDARA_CHARACTER_PURGE_SWEEP_INTERVAL` | `10m` | How often the Gateway looks for deleted Characters whose retention has expired, and for name reservations with no Character behind them. |
| `character.spawn_room` | `ANDARA_CHARACTER_SPAWN_ROOM` | `town/plaza` | `zone_id/room_id` a never-bound Character is placed in. Resolved against the loaded content at boot; a value the World lacks fails the boot. The default is the dev fixture's Room, so every values file outside `ENV=local` must set it (the chart's schema requires it). |
| `character.name_pattern` | `ANDARA_CHARACTER_NAME_PATTERN` | `^[\p{L}][\p{L}' -]{2,23}$` | RE2 a Character name must match, as typed. Uniqueness is on the folded form (NFKC, case-folded, trimmed), across every Account, forever. |
| `snapshot.interval` | `ANDARA_SNAPSHOT_INTERVAL` | `60s` | Cadence of a snapshot round — one consistent cut of every owned Zone at a tick boundary (`docs/specs/slo/recovery.md`). It sets RTO only: RPO is zero and is decided by broker settings. The round starts on the first boundary past the interval, never mid-tick. `0` disables snapshots, which makes every recovery a replay from the log's beginning: correct, and unbounded. |
| `snapshot.max_stall_ms` | `ANDARA_SNAPSHOT_MAX_STALL_MS` | `15` | Budget for the in-tick copy — the only part of a round inside the tick, and the part players can feel. A warning, not a refusal: over it, the round continues and `andara_snapshot_failures_total{reason="stall"}` counts it. The sizing fixture's measurement and margin are in [AW-SRV-006](https://github.com/valesordev/andara.valesordev.com/issues/232) ("Sizing fixture, measured"). |
| `snapshot.store` | `ANDARA_SNAPSHOT_STORE` | `fs` | `fs` or `s3`. `fs` is `make up` and the volume `AW-INF-003` mounts; `s3` is the cluster's store, and versitygw locally (the compose service is still named `minio`). |
| `snapshot.fs_path` | `ANDARA_SNAPSHOT_FS_PATH` | `/var/lib/andara/snapshots` | Directory the `fs` store writes under, keyed `{zone_id}/{tick}/{state_version}/{offset}`, tick and offset zero-padded to 20 digits so a lexical listing is in tick order. The tick is what makes a round's objects immutable — without it an idle Zone rewrote the same key every round, and a partial failure destroyed the last complete round (AC-5). Writes go to `{key}.tmp` and are renamed, so an interrupted write leaves nothing a listing returns. |
| `snapshot.s3_bucket` | `ANDARA_SNAPSHOT_S3_BUCKET` | — | Required when `snapshot.store=s3`; the server refuses to start without it rather than failing its first round a minute after it looked healthy. |
| `snapshot.s3_endpoint` | `ANDARA_SNAPSHOT_S3_ENDPOINT` | — | S3 endpoint. versitygw locally; empty for AWS. |
| `snapshot.upload_timeout` | `ANDARA_SNAPSHOT_UPLOAD_TIMEOUT` | `30s` | A round exceeding it is failed, not queued behind the next one; the next round starts on schedule. It also bounds the wait for the broker to acknowledge the round's `TickCompleted`: with `sim.source=kafka` nothing is encoded or written until that acknowledgement arrives, a boundary reported lost abandons the round as `andara_snapshot_failures_total{reason="boundary"}`, and no acknowledgement inside the timeout abandons it as `reason="timeout"`. A boundary whose `Publish` failed outright takes no round, and a round that was due counts `reason="boundary"`. Must not exceed `snapshot.interval` — if the two would have to meet, the body has outgrown the cadence. |
| `recovery.require_snapshot` | `ANDARA_RECOVERY_REQUIRE_SNAPSHOT` | `false` | Boot with no complete snapshot round exits `7` instead of replaying the log from offset zero. `true` in prod once M2 lands: a cold replay is a retention bug, not a boot. |
| `recovery.replay_batch` | `ANDARA_RECOVERY_REPLAY_BATCH` | `4096` | Boundaries one replay step takes. It bounds memory only: the State Hash sequence is the same at any value. At least `1`. |
| `recovery.verify_timeout` | `ANDARA_RECOVERY_VERIFY_TIMEOUT` | `600s` | Bounds `andara-server recover --verify` and `Admin.VerifySnapshotRound`; past it the RPC answers `DEADLINE_EXCEEDED`. |
| `recovery.pin_round` | `ANDARA_RECOVERY_PIN_ROUND` | `0` | `0` recovers from the newest complete round. `T > 0` names round `T`: boot recovery uses it, and exits `7` if it isn't complete, trying no other. Read on every boot and never cleared by the server; `make rollback ROUND=T` sets and clears it. |
| `recovery.mismatch_linger` | `ANDARA_RECOVERY_MISMATCH_LINGER` | `0s` | How long a boot that ends in exit `8` or `6` serves `/metrics` and `/livez` (`200`) and `/readyz` and `/startedz` (`503`) before exiting, so `andara_recovery_state_hash_match` `0` is scraped. A signal ends it at once. `grpc.listen` is never bound. Compose sets `60s`; the chart leaves `0s`, where only a Ready pod is scraped. |
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
the server container sources it before exec. Probes, all on `http.port`:

| Path | 200 when | Probe |
|------|----------|-------|
| `/livez` | always, while the process runs. It must never depend on Kafka or a datastore | liveness |
| `/startedz` | recovery has finished and the gRPC listener serves, whether or not the World waits for content. It stays 200 until exit, through the drain (`AW-SRV-042`) | startup |
| `/readyz` | content is in effect and the server isn't draining. 503 while it waits for content | readiness |

Start-up is judged on `/startedz`, not `/readyz`. A fresh environment waits for its
first content indefinitely, and a startup probe on `/readyz` would restart it forever.

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
tick, counted, never a crash. A lost **boundary** is the exception (`AW-SRV-026`): a World whose
recent history can't be replayed is one whose State Hash nobody can check, so the loop finishes the
tick that learned of it, drains with `SimulationStopped{reason: boundary_lost}`, and the process
exits `5` into exact recovery. A boundary goes to the producer only once every earlier one is
acknowledged, so a loss can never leave a later one on the topic. One unacknowledged past the
delivery timeout is lost even if the producer is still retrying it, which is what makes a stopped
broker a loss a minute in rather than never. Offsets are committed only for an acknowledged
boundary, so a restart re-applies from the last delivered one. A handler panic is contained at the Zone: the Zone is marked faulted
with a `ZoneFaulted` Event, its Partition freezes at the panicking record, and the other Zones keep
ticking. Verb handlers register on `sim.Config.Handlers` (`AW-SRV-003`); a Command with none is
rejected `unsupported_command` and its offset advances.

**Drain.** On `SIGTERM` the gateway drains first, then the loop finishes its in-flight tick,
emits `SimulationStopped` (event_id 0 — a notification, not World history), flushes the publisher,
checkpoints the newest boundary the flush delivered, and exits 0; past `sim.drain_timeout_ms` it
exits 1 naming the tick.

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
| `andara_tick_publish_failures_total` | counter | `kind` | `events`, `commands`, `checkpoint`, `boundary` |
| `andara_tick_boundary_lost_total` | counter | — | 1; 0 or 1 per process (`AW-SRV-026`) |
| `andara_handoffs_in_transit` | gauge | — | 1; Entities that left a Zone and aren't acknowledged by the target (`AW-SRV-028`) |
| `andara_handoff_retries_total`, `andara_handoff_stale_arrivals_total` | counter | — | 1 |
| `andara_handoff_placed_entries` | gauge | — | 1; handoff marks across every Zone, kept for good |

Logs: `tick` at `info` once a second with `tick`, `lag_ms`, `consumer_lag`, `deferred`,
`checkpoint_age_ticks`, `applied_offsets`; `tick overran its budget` at `warn` with `tick`,
`duration_ms`, `budget_ms`, and the slowest `zone`; `tick input starved` at `warn`; `zone faulted` at
`error` with `tick`, `zone`, `partition`, `offset`, `panic`; `tick boundary lost; exiting into
recovery` at `error` with `lost_tick`, `last_delivered_tick`, `err`. Spans: `sim.tick` per tick with
`record_count`, `event_count`, `overrun`, `starved`, `lag_seconds`, and `sim.zone_tick` per Zone —
always started, exported one in a hundred plus every overrun (`telemetry.TickSampler`) and the tick
that stopped on a lost boundary (`boundary_lost=true`). Per-Entity
spans are not emitted. The dashboard is `andara-tick-health`; the SLO is
`docs/specs/slo/tick-health.md`.

### Cross-Zone handoff (AW-SRV-028)

A move or a goto across a Zone boundary doesn't delete the Entity from the source: it moves to the
source Zone's **Transit**, inert (every Command for it is rejected `in_transit`), and an `Arrive`
carrying its `handoff_seq` goes to the target Zone's Partition. The target decides the `Arrive` by
its own state alone, with a per-Entity high-water mark of decided sequences (`ZoneState.placed`, kept
for good): above the mark it places the Entity and acks; at or below it, it is a retry or stale, acked
and placed nowhere. The ack is a `HandoffAck` Command to the source's Partition, and the source drops
the record when an ack for that Entity **and sequence** applies. An `Arrive` into a Zone that still
holds the Entity in its own Transit at a lower sequence is an implicit ack.

The retry is the live loop's alone. After `Step` the loop calls `Engine.DueHandoffs(tick)` and
produces what it returns, at most `sim.handoff_retry_batch` per tick: the first retry after
`sim.handoff_retry_ticks`, then the interval doubling to `sim.handoff_retry_max_ticks`. The schedule
is in the engine's memory and not in the State Hash, so replay under any config matches. Replay
(recovery, the state projector) never calls `DueHandoffs` and writes no schedule entry, even for a
departure inside the replayed range: every record found in Transit after a recovery is due on the first
live tick. Nothing is produced for a faulted Zone or a frozen Partition.

`andara_handoffs_in_transit` sustained above 0 means the broker or the target Partition is stuck.
`andara_handoff_placed_entries` grows with the Entities that cross Zones. Logs: `handoffs retried: an
Arrive was not acknowledged` at `warn`, at most once per `sim.handoff_retry_ticks` window, with
`retries` (the window's count), `oldest_attempt`, `tick`, `in_transit` and `trace_id`; `handoff retried` at `debug` per retry with `entity_id`, `from_zone`, `to_zone`,
`seq`, `attempt`; `error` for a refused `entity_present`, `invalid_arrival` or `id_reused`. A retry
starts a new trace.

**Deploying this needs a reset of `dev`.** A log written before this change that holds a cross-Zone
move doesn't replay (the old code deleted the Entity at the source; this leaves it in Transit), and
recovery exits `6`. `make world-reset ENV=dev CONFIRM=andara-dev` with the deploy, which destroys
`dev`'s Characters and Accounts. A binary from before it can't read a log that has a `HandoffAck`
either: the same reset. An Entity ID is never reused: a `BindCharacter` for an ID some Zone holds a
mark for is refused `id_reused`.

## Recovery (AW-SRV-007)

A boot recovers the World before it serves anything. It picks the newest **complete** snapshot round
(or the one `recovery.pin_round` names), rebuilds the content that round recorded and checks its
digest, restores the Zones and checks the result against the round's own tick (AW-SRV-043), seeks the
Tick Boundary log to that tick by binary search, and replays every boundary after it to the head,
verifying the State Hash at each. It streams boundaries `recovery.replay_batch` at a time and never
loads the topic. With no complete round it replays from offset zero. It never tries another round
when the one it chose is refused, and it never serves a World that didn't reproduce its history.

| Exit | Condition |
|-----:|-----------|
| `0` | recovered and serving |
| `1` | configuration or store error before recovery began |
| `3` | the log no longer holds the round (`ErrLogGap`): retention is shorter than the snapshot age. The `error` line names the Partition, the round's offset and the log's earliest |
| `4` | the round was written by a newer binary (`ErrStateVersion`) |
| `5` | not recovery's: a running server lost a Tick Boundary Record (AW-SRV-026) |
| `6` | the round doesn't reproduce its own tick: `ErrRestoreMismatch`, `ErrSeedMismatch`, or it doesn't restore onto the content (`reason=content`): a digest mismatch, an object for a Zone the round's content doesn't list, or recorded content that names a pack version the content source doesn't have (`ErrRoundContent`; a source that merely fails to answer is exit `1`) |
| `7` | no complete round with `recovery.require_snapshot=true`, or a named round that isn't complete; the line carries `round_tick` and `cause` (`missing`, `duplicate`, `hash`, `disagree`) |
| `8` | a replayed boundary's State Hash differs from the recorded one (`ErrHashMismatch`); the line names the tick, both hashes and the round used |

`2` is never assigned: it is Go's own exit for a runtime fatal error. Exits `8` and `6` set
`andara_recovery_state_hash_match` to `0` and linger under `recovery.mismatch_linger`. The gauge has no
sample until a recovery sets it.

`/readyz` answers `200` only when recovery verified the World, the Gateway serves with content in
effect, and the first live tick has completed within ten `sim.tick_budget_ms` of schedule.

**Which Zones a round must hold.** A round is complete when it holds one hash-valid object for every
Zone of the content *it records* (the `content` of its envelopes), not of the content in effect at boot: a
content swap that adds a Zone after the last round must not make that round incomplete. Discovery lists the
loaded content's Zones, and each round's own Zones are resolved through the content source (once per set of
versions). With no Zones loaded, nothing is discovered and recovery replays the log.

**Characters a crash left standing.** Every Session is gone at recovery. Each Character body present
and not linkdead gets a `MarkLinkdead` (or an `UnbindCharacter{QUIT}` when `session.linkdead_grace` is
`0`) produced before the loop runs, counted on `andara_character_unbinds_total{reason="linkdead"}`.

```
andara-server recover --verify [--round T]     # match or mismatch, both hashes, phase timings
andara-cli snapshot list [--zone Z] [--local]  # rounds the server sees; --local reads the store
andara-cli snapshot verify --round T           # Admin.VerifySnapshotRound, a scratch Engine
```

`recover --verify` loads a round, replays to head, prints `match` or `mismatch reason=...`, and exits
`0` or `8` (a restore mismatch is a mismatch too); `7`, `4` and `3` are refusals. It never binds
`grpc.listen`, never lingers, and never serves what it built. `Admin.VerifySnapshotRound` is the same
check against a scratch Engine inside the running server (OPERATOR only): a mismatch is a response with
an `outcome`, `NOT_FOUND` is a tick with no object, `FAILED_PRECONDITION` an incomplete round, a log
gap or a newer `state_version`.

| Metric | Type | Labels | Cardinality bound |
|--------|------|--------|-------------------|
| `andara_recovery_duration_seconds` | histogram | `phase` | 5: `load`, `seek`, `replay`, `verify`, `total` |
| `andara_recovery_replayed_ticks` | gauge | none | 1 |
| `andara_recovery_state_hash_match` | gauge | none | 1; `0` or `1`, absent until a recovery sets it |
| `andara_recovery_failures_total` | counter | `reason` | 6: `hash`, `restore`, `gap`, `version`, `round`, `store` |
| `andara_recovery_round_tick` | gauge | none | 1; the round used, `0` for a cold start |
| `andara_restore_total` | counter | `caller`, `outcome` | `recovery` and `verify` here × `ok`, `hash_mismatch`, `seed_mismatch` |

Spans: `recovery.run`, with children `recovery.load_snapshot` (per Zone object read, around the read itself: `zone_id`, `key`, `bytes`), `restore.verify`,
`recovery.seek`, `recovery.replay` (`ticks`, `records`) and `recovery.verify`.

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

`server/sim` is the post-log half. `sim.Handlers()` is the apply column — `look`, `move`, `goto`,
`arrive`, and the roster's binds — and each handler is `validate` then `apply`: validate reads state and never mutates it,
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
(content moved under the log) lands at the target Zone's fallback Room with
`EntityRelocated{room_removed}` (`AW-SRV-012`). Every `Arrive` then describes the Room it landed in
to the arrival alone, since it can't tell a `move` from a `goto` (`AW-SRV-036`), and an in-Zone
`move` describes its destination the same way, after its `CharacterArrived` (`AW-SRV-038`): every
way a Character walks or jumps into a Room shows it the Room. Bystanders read only the departure and
the arrival.

**`goto <zone>/<room>`** (or `goto <room>`, in the actor's Zone) is a Builder's jump, gated to
`builder` in the verb table, with no abbreviation and no alias. The pipeline fills a bare Room's
Zone from the Binding before the log, so the `Goto` arm always carries both. Its apply validates the
target against the World in effect, and a missing one is `unknown_zone` or `unknown_room` at
`validate`, `there is no room <zone>/<room>`. Within a Zone it relocates the actor, with
`CharacterLeft` and `CharacterArrived` carrying no direction, then describes the new Room to the
jumper. Across Zones it leaves as a cross-Zone move does, through an `Arrive` with no direction.
The Room one already stands in is a fresh look. The tick's `command applied` line and
`command.apply` span carry `from_room` and `to_room` for a `goto`.

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

"None answers" means none. The probe, the ping after a failed produce, and the tick source's check
behind `tick input starved` all use `recordlog.Ping`. It asks every broker the client has
discovered at once, and the first answer wins. kgo's own `Ping` asks them one at a time under one
deadline, so a deleted broker pod whose address has gone dark used up the whole budget while two
live brokers went unasked. That lasted until the controller dropped the broker from metadata,
about 14 s, and made one broker bounce read as an outage (#129).

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

- **Attached.** Every stream's first frame is `Attached` (Event ID 0, no `EventType`), written after
  the stream's cursor is positioned and before anything else, a `Resync` included. It is the
  stream's only open signal: a client that submits a Command once it has arrived gets that
  Command's Events, and may rely on nothing before it. Response headers say nothing about the
  egress, and the same handler serves Connect, gRPC and gRPC-Web, so no transport's header timing
  can be the signal. `cursor_event_id` is `last_event_id` when a resume holds, else the newest
  Event retained for the Session (0 if none), so the first Event the stream delivers has a greater
  ID. It does not move the client's resume point (it has no Event ID), and a `Rebind` sends no
  second one. A client built against it waits for `Attached`, so against a server without it
  `andara-cli play` fails at `--timeout` with exit `4` rather than proceeding.
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
| `andara_stream_events_sent_total` | counter | `type` | the EventType enum + `heartbeat`, `resync`, `attached` |
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

### Builder pack grants (AW-SRV-035)

`Admin.SetBuilderPacks` (`andara-cli account set-packs`) replaces an Account's
`builder_packs`, the Content Packs a `builder` may publish to, as `SetRoles` replaces roles. Only
an Operator may, and no Account holds `andara.core`. A refusal carries `ErrorInfo{domain:
andara.accounts, reason}`, with reason one of `operator_only`, `account_not_found`,
`invalid_pack_id`, `core_not_grantable` or `record_version`. The store write is the
`accounts.write` span, and the audit record is written outside the Account write lock.

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

A Session's end decides what its Character does (AW-SRV-015):

| End | Produced | Body |
|-----|----------|------|
| `CloseSession`, or a revoked credential | `UnbindCharacter{QUIT}` | dormant; `CharacterDespawned{quit}` to its Room |
| a lost connection: a transport close, or a keepalive miss past `session.linkdead_detect` | `MarkLinkdead` | linkdead where it stands; `CharacterLinkdead` |
| a drain (SIGTERM) | `MarkLinkdead` | linkdead; no Character despawns because of a deploy |
| its deadline or ceiling Tick | nothing: the sim despawns it | dormant; `CharacterDespawned{linkdead\|linkdead_ceiling}` |

The produce runs on its own context bounded by `ingress.produce_deadline`, and records the
roster's position from the routing table. A quit frees the flag. `CloseSession` answers only once
the `UnbindCharacter` is durable and the flag is free, so a `SelectCharacter` sent after the
response is never `already_live`. Until a teardown has produced, the Character is `already_live`
to the same Account. The exception is a reconnect of that Character after a lost connection,
which waits for the `MarkLinkdead` instead. A body a crash left present is marked linkdead at recovery
(see Recovery); a mark that fails to produce leaves it present, and the next select takes it where it stands. A `BindCharacter` the roster routed to the
wrong Zone is re-produced by the sim to the Zone that holds the body.

**A drain, and what it leaves.** Every bound Session is closed as linkdead, so every teardown
produces a `MarkLinkdead`. The produces run concurrently, one per Session, so a drain costs about
one `ingress.produce_deadline` of wall clock however many Sessions are bound. `CloseIngress`
waits for them before the producer closes. A clean drain therefore leaves its bodies **linkdead**,
each with its grace from the Tick the mark applied. `linkdead_grace > recovery.rto_target` is
what lets the players reconnect before any of them despawns. If the broker is unreachable, every
one of those produces fails instead. Each is counted on
`andara_character_unbinds_total{reason="linkdead",outcome="produce_failed"}` with a `warn` line
naming the Session and the Character. The restarted World then holds those bodies **present with
no Session**: the next `SelectCharacter` takes each where it stands, and until then they stand in
their Rooms and are listed by `look`.

### Linkdead (AW-SRV-015)

A `MarkLinkdead` carries `session.linkdead_grace`, `_combat_extension` and `_max`, converted to
Ticks at `sim.tick_rate` and rounded up. The tick that applies it sets the body's four linkdead
fields from its own Tick and those durations, so a replay under retuned configuration despawns on
the same Tick. `look` lists a linkdead body in `occupants` and in `linkdead`.

- **The Account's flag.** The roster keeps it on the linkdead Character. Selecting that Character
  again is the reconnect: a `BindCharacter` that clears the four fields and emits
  `CharacterReconnected` to the Room in place of `CharacterArrived`. Selecting any other Character
  is `already_live`, naming the linkdead one.
- **Freeing the flag.** Only the sim frees it: the body's despawn, which the loop hands to the
  roster in the tick's `StepResult.Linkdead`, or a reconnect. There is no wall-clock bound. The
  deadline starts when the mark applies and runs in Ticks, so a timer could free the Account while
  the body is still in the World. A reconnect whose `BindCharacter` fails to produce puts the hold
  back.
- **A drop mid-crossing.** The teardown waits for the crossing to settle, bounded by
  `ingress.transit_hold`, and produces to the Zone the body arrived in. That applies to a quit's
  `UnbindCharacter` too. A crossing that doesn't settle is produced to the Zone the body left, with
  a `warn` line, and applies as a no-op. The body then stays present with no Session, and the
  hold keeps it for the reconnect.
- **Combat.** `sim.Engine.OnCombatInteraction(target)` is the one hook. It moves a linkdead
  target's deadline to `max(deadline, now + extension)`, capped at the ceiling. Nothing calls it
  until combat exists; the tests use a fixture verb.
- **The Event stream across a reconnect.** When a Session ends linkdead, the egress keeps its
  retained ring and the pump that fills it, parked under the Character, for up to
  `session.linkdead_max`. The gateway parks it before it cancels the Session, so a reconnect that
  arrives before the old Session's teardown finishes still finds it (#121). The reconnecting
  Session's first `Subscribe` adopts it, so a stream
  carrying `last_event_id` resumes with no gap, including what the Room did meanwhile. A reconnect
  whose first stream is a `Resync` counts on `andara_reconnect_resyncs_total`. The consequence to
  own: `events.max_subscribers` counts parked subscriptions too.
- **Keepalive.** The gateway's HTTP/2 server pings a connection that has sent nothing for half of
  `session.linkdead_detect`, and closes it if the ping goes unanswered for the other half. A
  partitioned client is therefore linkdead within `linkdead_detect`, and a healthy idle one, which
  answers its pings, is untouched.
- **Startup.** The server refuses to start (exit 1, `config.ErrInvariant` naming the relation and
  every value in it) unless, in this order: `linkdead_max >= linkdead_grace`,
  `linkdead_grace > recovery.rto_target`,
  `egress.resume_window >= linkdead_max × egress.assumed_event_rate`, and
  `auth.session_ttl > linkdead_max`.

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
| `andara_character_unbinds_total` | counter | `reason`, `outcome` | `quit` (an `UnbindCharacter`), `linkdead` (a `MarkLinkdead`), `switch` (an `UnbindCharacter{SWITCH}` from a body switch) × `ok`, `produce_failed` |
| `andara_roster_characters` | gauge | `status` | `active`, `deleted` — roster entries, from the Gateway's index; a purge leaves the entry `deleted` (the sim's `andara_characters_total{state="dormant"}` is what falls) |
| `andara_character_purges_total` | counter | `outcome` | `ok` (the sim removed a body), `no_body` (it had none to remove), `already_purged` (the sweep's mark found the entry marked), `reclaimed` (a name reservation with no Character removed) |
| `andara_character_sweep_duration_seconds` | histogram | — | one sample per retention sweep |
| `andara_sessions_linkdead` | gauge | `in_combat` | `true`, `false`: bodies waiting out their grace, seeded from recovery and kept by the loop. `in_combat` is whether combat extended the body's deadline on this process |
| `andara_linkdead_outcomes_total` | counter | `outcome` | `reconnected`, `despawned`, `ceiling`, `died` (declared; nothing produces it until combat exists), `quit` |
| `andara_linkdead_duration_seconds` | histogram | `in_combat` | `true`, `false`; from the mark to the end of the grace, in Ticks at `sim.tick_rate` |
| `andara_linkdead_combat_extensions_total` | counter | — | combat interactions against a linkdead body |
| `andara_linkdead_ceiling_despawns_total` | counter | — | despawns at `session.linkdead_max` |
| `andara_reconnect_resyncs_total` | counter | — | reconnects whose first stream was a `Resync`; the AC-7 signal that `egress.assumed_event_rate` is too low |

Character name and Account ID are never labels. Logs at `info`: `character created`,
`character selected` (with `zone`, `partition`, `offset`, `reconnect`), `character unbound` and
`character marked linkdead` (with `reason`, `outcome`, `zone`, `room`), each with `account_id`,
`character_id`, `session_id`, `trace_id`. From the loop, `character linkdead`,
`character reconnected` and `character despawned` carry `session_id`, `character_id`, `outcome`,
`deadline_tick`, `tick`, `zone` and `trace_id`. An expiry's `session_id` is the Session that went
linkdead, and its `trace_id` the `sim.tick` span that applied it; a body recovery left linkdead has
no Session, so its line omits `session_id` and carries `recovered=true`; the
name appears quoted as a value on `created`, never as a key. The tick loop logs `character bind
applied` at `info` when a `BindCharacter` applies, with the same four fields plus `tick`, the
`zone` and `room` the body is in, and `body`: `spawned` (a never-bound Character made at the spawn
Room), `woken` (a dormant body, where it went dormant), `present` (a body with no Session, taken
where it stands), or `rerouted` (a dormant body in another Zone; the Command is produced there, and
that Zone's apply logs `woken`), or `reconnected` (a linkdead body taken back by a new Session, AW-SRV-015). A rejected bind logs no such line; `command applied` has its
code. A failed teardown produce is `warn`
with the same fields. Spans: `character.create` and `character.select` under the RPC span, linked
to `session.lifetime`; `log.produce` under `select`; the tick's `command.apply` for the
`BindCharacter` joins through the record's `trace_id`; `character.unbind` and
`character.linkdead` are roots linked to the Session. `linkdead.enter` and `linkdead.reconnect` are
span events on `session.lifetime`.

### Deleting, switching, and purging (AW-SRV-032)

`DeleteCharacter` is a soft delete: the roster entry becomes `DELETED` with `deleted_unix`, the body
is left dormant, and nothing is produced to the log. A Character that is live, being bound, or
lingering linkdead is `FAILED_PRECONDITION character_live`; one not on the Account, or already
deleted, is `NOT_FOUND no_such_character`. The delete and `SelectCharacter` claim the Character
under one lock, so of a delete and a select racing from two Sessions exactly one wins and a live
body is never `DELETED`. A deleted Character still counts against `character.max_per_account` until
purged, and its name stays reserved for good (the reservation is on `andara.accounts.v1`, so a World
rollback does not release it). `ListCharacters` lists deleted entries with `status` and
`deleted_unix`.

The sweep (`character.purge_sweep_interval`) produces one `PurgeCharacter` per Character whose
`deleted_unix + character.delete_retention` has passed, to the Zone the roster last knew the body
in, then marks the entry `purged_unix` so it is not produced again. The sim handles the body
wherever it is: a dormant body is removed; a present one (a crash leaves one with no Session) is
despawned first, `CharacterDespawned{reason: "purge"}` to its Room, with its linkdead fields cleared;
a body in another Zone is re-routed there; no body is a silent no-op. `CharacterPurged` goes to
the purged Entity alone. Accepted residual until `AW-SRV-027`: a purge rejected `in_transit` or
`zone_faulted`, or lost between a re-route's apply and its re-produce, leaves a `DELETED`
Character's body in Zone state, unreachable and harmless. The same sweep removes name reservations
no Character is behind (the debris of a crash between creation's two writes) with a tombstone on
the accounts topic; a reservation with a Character behind it, deleted or not, is never removed.

Selecting another Character on a Session that already drives one switches bodies: the routing table
names the second before either produce, then `UnbindCharacter{SWITCH}` for the first, then
`BindCharacter` for the second, so the Room reads `CharacterDespawned{reason: "switch"}` and then
`CharacterArrived`. A failed first produce puts the Session back on the first Character; a failed
second leaves it unbound with the first body dormant.

Logs at `info`: `character deleted`, `character purge produced`, `character purge applied` (with
`was_present`, `rerouted`), `character purge re-routed`, `character switched out`, and `name
reservation with no character reclaimed`. Spans: `character.delete` under the RPC span, linked to
`session.lifetime`; `character.sweep` is a root with `expired`, `produced`, `failed`, `reclaimed`;
the purge's apply joins through the record's `trace_id`.

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
`duplicate_component_type`, `invalid_component_field`, `duplicate_direction` (a second Exit with a
Direction its Room already uses, reported at the second, `AW-SRV-034`), `fallback_missing` (a Zone
with no `fallback_room`, or one naming a Room it does not contain, AW-SRV-012 AC-10); and for
Templates `unresolved_extends`, `unflattened_template`, `duplicate_template`, `chain_mismatch`,
`chain_too_deep`, `invalid_provenance`. **A refused load reports only its errors** (`errors.md` §1
rule 7): a warning describes content the loader accepted, and a refused load accepted nothing, so
no `orphan_room` or `missing_reverse_exit` is reported beside an error, unless
`content.strict_orphans` makes `orphan_room` an error itself.

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
it's whatever moved while the process was down. A boot that ends with no Zones in effect exits 1,
with one exception.

**Waiting for content** (`AW-SRV-042`). With `content.source=kafka`, a World that has never had
Zones (no swap with `zone_count > 0` has applied in its log) is a fresh environment, and it waits:
- The Gateway serves, Admin is served in full, and `/startedz` is 200.
- `/readyz` is 503.
- `OpenSession` is refused `UNAVAILABLE`, with `ErrorInfo{andara.game, no_content_in_effect}`,
  counted `andara_sessions_total{outcome="rejected_no_content"}`.
- A `warn` line, `waiting for content: …`, is written once.

An empty store is this state's expected cause, so its `no_zones_found` isn't reported at load
(`AW-SRV-042`, #299). `LoadContent` holds it until the boot decides:
- **A wait** drops it, so the wait line stands alone.
- **A serve from the log, with Zones no pointer names,** logs `the content the Active Pointers
  name does not load; recovering what the log recorded` once.
- **An exit 1 before the decision, by any path,** logs it at `error` and counts it in
  `andara_content_validation_errors_total{code="no_zones_found"}` once.

The projector decides when it enters the wait. Its reloads during the wait log only `debug`
`content reload: no Zones in effect yet`, with `next_retry`. A rejected version, an unreachable
store, the directory source and `--validate-only` report at load as before.

The decision reads the World Partition's swap records alone, never the store, deciding each as the
Engine does on the two refusals a record settles (`misrouted`, `stale_base`). So it holds when the
store refuses every version. A World that has had Zones, and a directory with none, still exit 1.

The wait ends when the first swap with Zones applies through the tick loop, after an Active Pointer
move. The server logs `content in effect: leaving the wait` with `zones` and `pack@version`, and
becomes ready with no restart. While it waits, the Loader refuses an activation whose World would
have Zones but lack `character.spawn_room` (`spawn_room_removed`). Every swap the Loader produces
carries `zone_count`, the Zones in the World after it.

Otherwise the roster's spawn Room is checked against that content, the Gateway starts, and only
then is the process ready (`/readyz` 200). `FollowContent` then applies Active Pointer moves for as long as the
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

An activation is followed into its load (AW-SRV-045). `ActivateVersion` writes the W3C traceparent
of its server span as `ActiveVersion.trace_parent`, and the Loader's `content.load` carries one span
link per pointer move it coalesced for that pack (attributes `content.pack`, `content.version`), a
link and not a parent because one debounced load can serve several moves. A retry of a refused load
carries the same links. The boot's core activation and a record from before the field have no
`trace_parent` and no link; one that is not a traceparent is skipped with one `warn`
(`pack`, `version`, `trace_parent` cut to 128 bytes) and the load goes ahead.

### Publishing content (AW-SRV-013)

A `content.source=kafka` server is also the content store's writer, over `Admin`. `HasBlobs` and
`PublishBlob` (client-streaming, chunks of at most 1 MiB) write blobs keyed by their sha256.
`PublishVersion` validates a version from blobs already written and writes its manifest. The
validator is the Loader's, against the packs in effect other than the version's own, so the
findings are the ones a load would give. Then `ApproveVersion`, `ActivateVersion` (which moves
the Active Pointer the Loader follows), `ListVersions`, `GetVersion`, `GetBlob`
(server-streaming, 1 MiB chunks), and `ReloadContent`. On a `dir` server they're `UNIMPLEMENTED`.

- **Who may.** A `builder` holding the pack in `Account.builder_packs`, or an `operator`.
  `andara.core` is readable by any Builder and published by no RPC. The matrix is in the story.
- **Two people.** A version is activated once someone other than its publisher approves it. The
  publisher is the manifest's `author` and the real actor behind an acting-as publish. An Operator
  may approve their own while `content.operator_self_approval` is on, and may activate unapproved
  with `override` and a `reason`. Rolling back to an approved version needs no fresh approval.
- **Refused before the pointer moves.** `ActivateVersion` runs the Loader's own evaluation against
  the World in effect: `zone_removed`, `spawn_room_removed` and `core_version` are
  `FAILED_PRECONDITION` with an `ActivationRefusal` naming the subjects. `override` doesn't skip them.
- **Paths stay in the pack.** `PublishVersion` refuses a manifest naming a blob path that is
  absolute, or holds a backslash, a `.`, `..` or empty element, or isn't clean under `path.Clean`. That's
  `INVALID_ARGUMENT` `validation`, naming the path, audited as a `reject`, and no manifest is written.
  `content fetch` writes blobs by these paths, so the gate doesn't rely on each client's guard. The
  rule is the CLI's `unsafe_source_path` (#267), which is `filepath.IsLocal` on the fetching
  machine. So the gate also refuses, whatever OS the server runs on, the forms Windows can't
  write locally: a colon (a drive or a stream), a NUL byte, and a device name as an element (`CON`,
  `PRN`, `AUX`, `NUL`, `COM1`–`9`, `LPT1`–`9`, `CONIN$`, `CONOUT$`, any case, with or without an
  extension). The compiler reports the same names offline as `unportable_name`: both call
  `lang.UnportableElement`, so a pack that passes `content validate` doesn't trip this rule.
- **Errors** carry `ErrorInfo{domain: "andara.content", reason}`, plus `PublishFindings` on
  `validation` from the Loader. A manifest the gate refuses before validating (no path, a bad hash,
  a path twice, a path that leaves the pack) carries none.
- **The server's own core.** At boot, before it reads the content, the server publishes the
  `andara.core` it embeds (`content/core/`) as `andara.core@<content/core/VERSION>`, author
  `server`. It activates that over nothing, or over its own older pointer. It leaves an Account's
  pointer or a newer core, saying so at `warn` or `info`. A store holding the same version with
  other bytes exits the boot `1`, writing nothing. Readiness waits for the active core to be in
  effect. `content/core/VERSIONS` is append-only, and `make check` holds the embedded digest to it.
- **History** is read back at boot from `andara.audit.v1`, the only history compaction leaves: who
  really published each version, and every pointer move.

| Metric | Type | Labels | Cardinality bound |
|--------|------|--------|-------------------|
| `andara_content_publishes_total` | counter | `outcome` | `ok`, `rejected`, `denied`, `too_large`, `stale_parent` |
| `andara_content_approvals_total` | counter | `outcome` | `ok`, `self` (refused), `denied`, `self_operator` |
| `andara_content_pointer_moves_total` | counter | `direction`, `override` | `forward`, `rollback` × `true`, `false`; the boot's core activation counts `forward`, `false` |
| `andara_content_activations_refused_total` | counter | `reason` | `unapproved`, `zone_removed`, `spawn_room_removed`, `core_version` |
| `andara_content_blob_bytes_total` | counter | — | 1; bytes accepted after deduplication |
| `andara_content_validation_failures_total` | counter | `code` | `sim.AllErrCodes` |

Pack ID is not a label on any of them; the audit topic answers "which pack". RED per RPC is the
Gateway's `andara_grpc_requests_total{method,code}`. Every line carries `actor_account_id`,
`acting_as_account_id`, `pack_id`, `version`, `session_id` and `trace_id`. Publish, approve and
activate log at `info`. A rejection, an override, a self-approval, or a refused activation logs at
`warn`, and the core boot logs one `content core: …` line. Spans: `content.publish_blob` →
`content.write_blob`; `content.publish` → `content.validate`, `content.write_manifest`;
`content.approve` → `content.write_manifest`; `content.activate` → `content.write_pointer`;
`content.get_blob`; `audit.write` under each; and `content.core_boot`, a root span, at boot.
