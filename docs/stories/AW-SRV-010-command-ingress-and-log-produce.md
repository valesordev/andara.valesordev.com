---
id: AW-SRV-010
title: Command ingress — parse, authorize, and produce to the command log
epic: EPIC-03
component: server
type: feature
status: review
size: M
depends_on: [AW-SRV-003, AW-SRV-005, AW-INF-004]
blocks: [AW-INF-005, AW-INF-010, AW-SRV-030, AW-SRV-031]
lane: implementation
risk: high
---

## Context

This story joins the two halves of the split pipeline. `AW-SRV-003` built the stages;
`AW-SRV-005` built the transport. This is the produce between them — the moment an Intent stops being
a client's assertion and becomes an ordered fact in the World's history.

It is also where the architecture's one new failure mode lives. ADR-0002 notes that Kafka availability
now bounds World availability: if the log is unreachable, no Command can be accepted. The degradation
has to be deliberate, typed, and observable, or it will present as an unexplained hang.

## User story

As a player, I want my command acknowledged the moment it is durably ordered, so that I know the world
received it even before I see what it did.

## Scope

### In scope
- `Game.Submit`: Intent → `Parse` → `Authorize` → produce to `andara.commands.v1` → return the assigned
  partition and offset.
- Partition selection by `hash(ZoneID) % 64`, from the actor's current Zone.
- Producer configuration: `acks=all`, idempotent producer, bounded in-flight requests to preserve
  per-key ordering, bounded retry with a deadline.
- Per-Session rate limiting, applied before parse.
- Read-only degradation when the log is unreachable: typed `UNAVAILABLE`, no hang, no silent drop.
- Unauthorized attempts routed to `andara.audit.v1` rather than the Command log.
- Backpressure: a client submitting faster than the producer drains gets `RESOURCE_EXHAUSTED`, not a
  growing queue.

### Out of scope
- The pipeline stages themselves — `AW-SRV-003`.
- Consuming and applying — `AW-SRV-002`.
- Real authentication — `AW-SRV-008`; a stub verifier stands in.
- Kafka topic provisioning — `AW-INF-004`. Operational contract and SLO — `AW-INF-005`.

## Acceptance criteria

1. **Given** an authenticated Session and a valid Intent **when** `Submit` is called **then** the
   Command is produced to the Partition for the actor's Zone, and the response carries the actual
   assigned partition and offset.
2. **Given** two Intents from the same Session for the same Zone **when** both are submitted **then**
   their offsets are strictly increasing in submission order. Per-Session ordering within a Partition is
   guaranteed, which requires the idempotent producer and bounded in-flight configuration.
3. **Given** an Intent that fails `Parse` **when** it is submitted **then** the RPC returns
   `INVALID_ARGUMENT` with the typed code, and **nothing is written to `andara.commands.v1`**, asserted
   by reading the topic's end offset before and after.
4. **Given** an Intent that fails `Authorize` **when** it is submitted **then** the RPC returns
   `PERMISSION_DENIED`, nothing is written to the Command log, and one record is written to
   `andara.audit.v1` naming the actor, the attempted verb, and the Session.
5. **Given** the broker is unreachable **when** `Submit` is called after detection (at most the
   probe interval, one second, after the outage began) **then** it returns `UNAVAILABLE` with a
   retryable indication at once, with nothing written — it does not hang, does not buffer
   unboundedly, and does not report success. A `Submit` whose record was already handed to the
   producer when the outage began is answered `DEADLINE_EXCEEDED` (outcome unknown) within the
   produce deadline, and the producer's buffer is dropped so that record never lands later.
   *(Reworded on the 2026-09-19 review of PR #34 to say what the code does.)*
6. **Given** the broker is unreachable **when** an already-established Session is inspected **then** it
   remains connected and continues receiving Events, because the tick and the Event stream do not depend
   on the produce path. The World becomes read-only, not unavailable.
7. **Given** a produce that times out with an indeterminate outcome **when** it is retried **then** the
   idempotent producer prevents a duplicate record. A player must never move twice because a retry
   succeeded after an ambiguous timeout.
8. **Given** a Session exceeding `ingress.rate_limit` **when** it submits **then** excess Intents are
   rejected with `RESOURCE_EXHAUSTED`, the Session survives, and nothing is produced.
9. **Given** a Command whose actor is in a Zone owned by a different process **when** it is submitted
   **then** it is produced to that Zone's Partition regardless of which process received the RPC. Any
   Gateway can accept any Command; only the owning consumer applies it.
10. **Given** a produce succeeded **when** the response is returned **then** the Session's trace shows
    `command.execute` spanning `command.parse`, `command.authorize`, and `log.produce`, and the
    produce span carries partition and offset.

## Interface contract

### Produce semantics

| Property | Value | Why |
|----------|-------|-----|
| `acks` | `all` | a client is told "ordered" only when the ISR has it |
| `enable.idempotence` | `true` | AC-7; an ambiguous retry must not duplicate |
| `max.in.flight.requests.per.connection` | `5` | the maximum the idempotent producer preserves order at |
| `delivery.timeout.ms` | `ingress.produce_deadline` | bounds AC-5 |
| key | `ZoneID` | places the record on the Zone's Partition |
| partitioner | explicit `hash(ZoneID) % 64` | not the client default, which may change between library versions and would silently remap Zones |

The explicit partitioner is the line most likely to be dropped as redundant. It is not: relying on a
client library's default hash means a library upgrade can move a Zone to a different Partition, which
splits its history across two Partitions and is unrecoverable.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `ingress.rate_limit` | `ANDARA_INGRESS_RATE_LIMIT` | `20/s` | per Session |
| `ingress.burst` | `ANDARA_INGRESS_BURST` | `40` | token bucket depth |
| `ingress.produce_deadline` | `ANDARA_PRODUCE_DEADLINE` | `2s` | bounds AC-5 |
| `ingress.max_pending` | `ANDARA_INGRESS_MAX_PENDING` | `256` | per Session; beyond this, `RESOURCE_EXHAUSTED` |
| `ingress.agent_rate_limit` | `ANDARA_AGENT_RATE_LIMIT` | `100/s` | Behavior Agents drive many NPCs per Session (ADR-0005) |
| `ingress.transit_hold` | `ANDARA_INGRESS_TRANSIT_HOLD` | `2s` | how long a Session's Intents wait for its Character to arrive in the next Zone, from the `CharacterLeft`; past it, `in_transit` (inherited item 3, added 2026-09-19) |
| `telemetry.trace_sample_ratio` | `ANDARA_TRACE_SAMPLE_RATIO` | `0.01` | head-sampling ratio for the `Game/Submit` root; rejections are kept regardless (inherited item 2, added 2026-09-19) |
| `telemetry.trust_inbound_traceparent` | `ANDARA_TRUST_INBOUND_TRACEPARENT` | `false` | let a client's `traceparent` parent the RPC span and carry its sampling decision; off, the RPC is a new root linked to the client's context and the ratio applies whatever the client sent (review of PR #34; compose local `true`) |

### Error taxonomy

| Condition | gRPC code | Log record written |
|-----------|-----------|--------------------|
| parse failure | `INVALID_ARGUMENT` | none |
| authorize failure | `PERMISSION_DENIED` | audit only |
| Character in transit past `ingress.transit_hold` (pre-log, at `authorize`) | `UNAVAILABLE` (retryable; no `RetryInfo` today) | none |
| rate limited | `RESOURCE_EXHAUSTED` | none |
| pending queue full | `RESOURCE_EXHAUSTED` | none |
| broker unreachable | `UNAVAILABLE` (retryable) | none |
| produce deadline exceeded | `DEADLINE_EXCEEDED` | possibly — the client must treat this as unknown and rely on idempotence |

The last row is the honest one: a produce deadline is genuinely ambiguous. The idempotent producer
makes a retry safe, and the client should retry rather than assume failure.

### As built (2026-09-19)

- `server/ingress`: `Ingress` (implements `gateway.Ingress`) — `New(Options{Pipeline, Bindings,
  RateLimit, AgentRateLimit, Burst, MaxPending, Metrics, Log, Now})`, `Submit`; per-Session token
  bucket (`auth.Limiter`, now exported with a burst depth), per-Session FIFO queue so offsets follow
  arrival order, pending bound. `KafkaProducer` (implements `command.Producer`) —
  `NewKafkaProducer(ProducerOptions{Brokers, Topic, ClientID, Deadline, MaxBuffered, ProbeInterval,
  Dialer, ClientLogger, Metrics, Log, Tracer})`, `Produce`, `Degraded`, `Close`; acks=all, idempotent,
  `ManualPartitioner` + `sim.PartitionFor`, `RecordDeliveryTimeout = deadline`,
  `ProduceRequestTimeout = deadline/2`, `MetadataMinAge = deadline/4` (a failed produce waits for a
  metadata refresh before its retry; the library's five-second floor put every retry past the
  deadline). `Bindings` (implements `command.Binder` and `sim.EventSink`) — `NewBindings(hold, now,
  heldGauge)`, `Bind`, `Unbind`, `Lookup`, `Binding(ctx, session)` (blocks through Transit),
  `Publish(sim.Event)` (follows `CharacterLeft`/`CharacterArrived` addressed to the bound Character).
  Errors `ErrRateLimited`, `ErrPendingFull`, `ErrUnavailable`, `ErrDeadline`; every wire error carries
  `errdetails.ErrorInfo{domain: andara.command, reason}` (+ `stage`, `pre_log`, `arg` for a pipeline
  rejection) and `UNAVAILABLE` a `RetryInfo` of 1 s. `ReadOnlyMessage` is a marked placeholder.
- `server/command`: `Binder.Binding(ctx, sessionID) (Binding, error)` with `ErrNoBinding` and
  `ErrBindingInTransit`; `CodeInTransit` pre-log at `authorize`; a Binder returning ctx's error
  is not a rejection. The produced record is `Parse`'s output plus `zone_id`, `actor_id`,
  `accepted_at_unix_nano`, `trace_id` — asserted byte-for-byte.
- `server/telemetry`: `Sampler` (head: `Game/Submit` roots at `telemetry.trace_sample_ratio`,
  parent-based otherwise, every other root sampled) and `SpanFilter` (tail: `sim.tick` /
  `sim.zone_tick` keep rule as before; `command.apply` on a tick-produced record keeps its tick's one
  in a hundred; a rejected `command.execute` under an unsampled root is exported with the flag set).
  `config.TraceSampleRatio`, default `0.01`.
- `server/auth`: `Auditor.Record` waits `Auditor.WriteTimeout` — the boot sets it to
  `ingress.produce_deadline`, one deadline for a write to the log — and then lets the write
  finish in the background — a refused Submit during a broker outage was waiting 20 s for its audit
  record, which the idempotent producer holds until a broker returns.
- `boot.Runtime.StartIngress` / `CloseIngress`, `Runtime.Ingress`, `Runtime.Bindings`; the engine
  subscribes `Bindings` after recovery beside the Hub; `sim.source=memory` produces into the loop's
  in-memory source, so `Submit` → tick → Event runs in one process with no broker.
- Config: `ingress.rate_limit`, `ingress.agent_rate_limit`, `ingress.burst`,
  `ingress.produce_deadline`, `ingress.max_pending`, `ingress.transit_hold`,
  `telemetry.trace_sample_ratio`, `telemetry.trust_inbound_traceparent` (`gateway.Options`
  gains the same); `keys.yaml` gains a `number` type. `docs/runbooks/world-read-only.md`.

## Data / state impact

This story is what fills `andara.commands.v1`, the topic that is the World's entire history. Two
properties follow from the decisions above and are worth stating as constraints on every future change:

- The log contains only parsed, authorized Commands. It is a record of legitimate intent.
- A record's Partition is determined by its Zone and by nothing else, forever. Any change to the
  partitioner is a change to where history lives.

No schema migration; the record type is `andara.log.v1.LoggedCommand` from `AW-SRV-005`.

## Observability requirements

### Metrics
- `andara_ingress_submits_total` — counter, label `outcome` (`produced`, `rejected_parse`,
  `rejected_authz`, `rate_limited`, `unavailable`, `deadline`). Bounded enum.
- `andara_ingress_produce_duration_seconds` — histogram. The latency a player feels before the ack.
- `andara_ingress_produce_retries_total` — counter.
- `andara_ingress_pending` — gauge. Per-process, not per-Session.
- `andara_ingress_held_intents` — gauge. Intents held while their Character is between Zones
  (inherited item 3, added 2026-09-19).
- `andara_ingress_degraded` — gauge, 0 or 1. 1 when the World is read-only because the log is
  unreachable. This is the metric `AW-INF-005` alerts on.
- `andara_ingress_produced_total` — counter, label `partition`. Cardinality 64. The spread of its rate
  (`topk(5, rate(andara_ingress_produced_total[5m]))`) reveals a hot Zone long before it becomes a
  tick problem, which is exactly the signal ADR-0001 says to watch for. *(Was
  `andara_ingress_partition_skew`, a gauge; retyped on the 2026-09-19 review of PR #34.)*

Session ID, actor ID, and raw Intent text are rejected as labels.

### Logs
- `info` on degradation entry and exit, with the broker error.
- `info` per authorize rejection: actor, verb, session.
- `warn` on produce retry and on rate limiting, sampled.
- Required fields: `ts`, `level`, `msg`, `service`, `env`, `session_id`, `trace_id`, `verb`, and
  `partition`/`offset` when produced.

### Traces
- `log.produce` — child of `command.execute`. Attributes: `partition`, `offset`, `retries`,
  `acks_wait_ms`.

### Alerts
`AW-INF-005` owns the alert on `andara_ingress_degraded`, because it also owns the Kafka availability
SLO the alert must be tied to. This story emits the metric and writes
`docs/runbooks/world-read-only.md`, so the runbook exists before the alert that points at it.

## Test plan

- **Unit:** partition selection against a fixture Zone set, including that two Rooms in one Zone always
  select the same Partition; rate limiter behavior at and beyond burst; error-code mapping per row.
- **Integration:** against a throwaway Redpanda — end-offset assertions for AC-3 and AC-4; ordering
  under concurrent submits from one Session (AC-2); broker kill asserting `UNAVAILABLE` plus a surviving
  Session and a still-ticking World (AC-5, AC-6); an injected ambiguous timeout asserting no duplicate
  record (AC-7).
- **Manual/operator:**
  ```
  make up && andara-cli play
  > north                                  # expect: immediate ack, then movement
  docker compose stop redpanda
  > north                                  # expect: typed "world is read-only", session alive
  docker compose start redpanda
  > north                                  # expect: recovery with no restart
  ```

## Definition of done

CLAUDE.md §8, plus:
- The end-offset assertions in AC-3 and AC-4 exist. "Nothing was written" must be verified against the
  broker, not inferred from a return value.
- `docs/runbooks/world-read-only.md` exists.
- The explicit partitioner is covered by a test that would fail if it fell back to a library default.
- Inherited from `AW-SRV-003`'s §8 pass (2026-09-19): this story is the first in-cluster caller of
  `Pipeline.Submit`, so §8's backend verification here includes showing `andara_commands_total{verb}`,
  `andara_command_duration_seconds{phase="pre_log"}`, and the `command.parse`/`command.authorize` spans
  under `command.execute` on the compose stack's Prometheus and Tempo — the pre-log half `AW-SRV-003`
  could only exercise in-process.

## Open questions

- **Inherited from `AW-SRV-003` (2026-09-18 review of PR #30), contract-bearing:**
  1. **The Gateway produces only what `command.Parse` returned.** `andara.log.v1.LoggedCommand`
     now carries `Arrive` with an Entity by value (and `AW-SRV-028` adds `HandoffAck`/
     `HandoffRejected`); a client-supplied record reaching the log would forge World state. The
     invariant is security, not hygiene: `Submit` accepts `Intent`, never a `LoggedCommand`, and a
     test asserts the produced record is byte-equal to `Parse`'s output plus the Gateway's own
     correlation fields.
  2. **Span sampling.** `command.apply` inherits its parent's sampled flag through the record's
     traceparent, so the sampling decision is made where the root span is: `command.execute` is
     head-sampled at `telemetry.trace_sample_ratio` and **every rejection is kept** (the same shape
     as `AW-SRV-002`'s overrun tail-sample). A record with no traceparent (tick-produced) inherits
     `sim.tick`'s one-in-a-hundred. This answers `AW-SRV-003`'s ~10k spans/s question; it is a
     `telemetry` change that lands here because this story creates the root span. *(Confirmed by
     Brian 2026-09-18.)*
  3. **Hold a Session's Commands while its Character is in transit** (Brian, 2026-09-18: the
     one-tick cross-Zone delay stays perceptible in the Events — `CharacterLeft` on *T*,
     `CharacterArrived` on *T+1* — but a player must never see "you are not here" for typing
     during it). `Submit` keeps the Intents of a Session whose `Binding` is in transit — from the
     Session's own `CharacterLeft` until its `CharacterArrived` or a `HandoffRejected`-restore —
     and releases them in order to the new Zone's Partition on arrival. Bounded: `ingress.transit_hold`
     (default 2 s) after which held Intents are rejected pre-log `in_transit` and the hold is
     cleared, so a stuck handoff (`AW-SRV-028`) surfaces to the player rather than to a queue.
     The sim still rejects `in_transit` post-log; the hold is what makes that path unreachable
     from a well-behaved Gateway. Add `ingress.transit_hold` to this story's configuration table
     and `andara_ingress_held_intents` (gauge) to its metrics when implementing.

- `[ASSUMPTION]` Default per-Session rate limit of 20/s. A human types perhaps 2/s; 20 leaves room for
  a client with macros without leaving room for a flood. Tune with real traffic.
- `[ASSUMPTION]` `Submit` is unary rather than client-streaming. Streaming would cut per-Intent overhead
  but complicates rate limiting and error mapping, and at human typing rates the overhead is irrelevant.
  Behavior Agents at 100/s may change this calculus — revisit at `AW-SRV-009`.
- `[NEEDS BRIAN]` What a player should see when the World goes read-only. A typed error is the
  mechanism; the wording is a design call, and it is the one error message every player will
  eventually see. **As built:** `ingress.ReadOnlyMessage`, marked as a placeholder — *"The world is
  read-only for a moment: your command was not taken. Try it again shortly."* — on the `UNAVAILABLE`
  with reason `world_read_only`. One constant to change.
- **Resolved 2026-09-19 (inherited items 1–3):** (1) `Submit` takes raw text and the produced record
  is byte-equal to `Parse`'s output plus the Gateway's four correlation fields, asserted by
  `TestSubmit_ProducesExactlyWhatParseReturned`. (2) Sampling as specified: the `Game/Submit` root is
  head-sampled at `telemetry.trace_sample_ratio` (default `0.01`), the flag rides
  `LoggedCommand.trace_id` into `command.apply`, a client's own `traceparent` decision is honored
  only when `telemetry.trust_inbound_traceparent` is set (review of PR #34: otherwise a client that
  flags every request sampled holds the collector's cost lever; off, the RPC span is a new root
  linked to the client's context), every rejection is exported, and a tick-produced record keeps
  `sim.tick`'s one in a hundred. What
  it took: the rejection is kept *after* the head said no, which the SDK's exporters refuse, so
  `SpanFilter` forwards a rejected `command.execute` claiming the sampled flag — the child spans of
  such a rejection are not exported, the execute span with `stage_failed` and `code` is.
  (3) The transit hold, as specified, with `ingress.transit_hold` and `andara_ingress_held_intents`
  added. A held Submit is also bounded by the RPC deadline; the hold is measured from the
  `CharacterLeft`, so a stuck handoff surfaces at a bounded time whenever the player types. Past the
  hold the Session's Intents are rejected `in_transit` at once until an arrival resolves it.
- **Corrected 2026-09-19 (implementation): the read-only state is detected by a probe, and the
  Submit in flight when the log goes away is ambiguous, not `UNAVAILABLE`.** AC-5 reads as if
  unreachability were known at the call. It is known when a ping fails: a probe pings once a second
  regardless of traffic (so `andara_ingress_degraded` moves while nobody is playing, and a Submit
  after detection fails at once), and a produce that fails pings too. The record of the Submit that
  discovers the outage had already been handed to the client and may have reached the broker, so it
  is answered `DEADLINE_EXCEEDED` (reason `produce_deadline`, outcome unknown) — never `UNAVAILABLE`,
  which promises nothing was written. Every Submit after detection is `UNAVAILABLE` within
  microseconds. Entering the state swaps the producer client and closes the old one: the idempotent
  producer cannot take back a record it has tried to send, and would have delivered it when a broker
  returned, minutes later, to a player who was told the World was read-only. Dropping the client is
  the bounded buffer; the one record that may have reached the broker in the outage's first moment
  is the residual ambiguity, and the runbook says so.
- **Corrected 2026-09-19 (implementation): metric outcomes.** `andara_ingress_submits_total{outcome}`
  gains `pending_full`, `in_transit`, `canceled`, and `internal` beside the six listed — each row of
  the error taxonomy is one outcome, and a bug is counted as one rather than hidden in `deadline`.
- **Corrected 2026-09-19 (implementation): `max.in.flight.requests.per.connection = 5` is not set.**
  franz-go pins the idempotent producer to five in flight and ignores the option; setting it would
  read as if it did something. The comment on `KafkaProducer` says so.
- **Resolved 2026-09-19 (review of PR #34):** `andara_ingress_partition_skew` (a gauge that only
  `Inc()`ed) is `andara_ingress_produced_total{partition}`, a counter; skew is the dashboard's
  `topk(5, rate(...[5m]))`. `docs/specs/slo/tick-health.md` and `simulation-lagging.md` follow.
- `[ASSUMPTION]` Per-Session Submits are serialized through the whole pipeline — parse, authorize,
  produce, ack — one at a time. Ordering is then structural rather than a property of the client's
  in-flight window, and a human's 2/s never notices. A Behavior Agent at 100/s over one Session is
  bounded by produce latency (~10 ms locally → ~100/s); if `AW-SRV-009` needs more, the queue can
  release after enqueue rather than after ack, keeping the order the client library preserves.
- **Verification 2026-09-19.** Against the compose Redpanda (`make test-integration`): AC-1/2/3/4/7/9
  and the partitioner discriminator (a Zone the library default and `sim.PartitionFor` disagree on
  lands where `PartitionFor` says); a dialer fault that loses one produce response after the broker
  has it — retried, counted, landed once; a dialer refusal — the discovering Submit `DEADLINE_EXCEEDED`
  or `UNAVAILABLE` if the probe won, the next `UNAVAILABLE` in microseconds, recovery on the first
  successful ping, exactly two records on the topic. AC-10 read back from Tempo:
  `Game/Submit → command.execute → command.parse, command.authorize, log.produce{partition, offset,
  retries, acks_wait_ms}`. Against the running server, with no Character bound (`AW-SRV-014`):
  `frobnicate` → `INVALID_ARGUMENT{unknown_verb}`, `move sideways` →
  `INVALID_ARGUMENT{invalid_argument, arg=direction}`, `look` → `PERMISSION_DENIED{not_authorized}`
  with one `andara.audit.v1` record naming the actor, verb, and Session and `andara.commands.v1`'s
  end offsets unchanged; 60 Submits in 0.6 s → 51 taken, 9 `RESOURCE_EXHAUSTED{rate_limited}`;
  `docker compose stop redpanda` with a Session submitting every second → `andara_ingress_degraded`
  1 within a second, `/readyz` 200, the tick running, the Session alive, every refused Submit back
  in 2.0 s (the audit write's bound) rather than 20; `start redpanda` → 0 within a second, no
  restart. Prometheus holds the ingress series (64 `produced_total`), Loki the `command log
  unreachable`/`reachable`, `session rate limited`, `audit record write pending`, and `command
  rejected` lines with `session_id`, `trace_id`, `verb`, `stage`, `code`; Tempo holds 175 rejected
  `command.execute` spans and zero `Game/Submit` roots at the 1 % default. The story's manual plan
  (`andara-cli play`, `north` → movement) needs `AW-SRV-014` to bind a Character and `AW-CLI-004` for
  the client; the produce path is verified by the integration suite until then.
