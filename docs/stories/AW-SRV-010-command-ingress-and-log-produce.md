---
id: AW-SRV-010
title: Command ingress — parse, authorize, and produce to the command log
epic: EPIC-03
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-003, AW-SRV-005, AW-INF-004]
blocks: [AW-INF-005, AW-INF-010]
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
5. **Given** the broker is unreachable **when** `Submit` is called **then** it returns `UNAVAILABLE`
   with a retryable indication within the produce deadline — it does not hang, does not buffer
   unboundedly, and does not report success.
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

### Error taxonomy

| Condition | gRPC code | Log record written |
|-----------|-----------|--------------------|
| parse failure | `INVALID_ARGUMENT` | none |
| authorize failure | `PERMISSION_DENIED` | audit only |
| rate limited | `RESOURCE_EXHAUSTED` | none |
| pending queue full | `RESOURCE_EXHAUSTED` | none |
| broker unreachable | `UNAVAILABLE` (retryable) | none |
| produce deadline exceeded | `DEADLINE_EXCEEDED` | possibly — the client must treat this as unknown and rely on idempotence |

The last row is the honest one: a produce deadline is genuinely ambiguous. The idempotent producer
makes a retry safe, and the client should retry rather than assume failure.

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
- `andara_ingress_degraded` — gauge, 0 or 1. 1 when the World is read-only because the log is
  unreachable. This is the metric `AW-INF-005` alerts on.
- `andara_ingress_partition_skew` — gauge, label `partition`. Cardinality 64. Reveals a hot Zone long
  before it becomes a tick problem, which is exactly the signal ADR-0001 says to watch for.

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

## Open questions

- `[ASSUMPTION]` Default per-Session rate limit of 20/s. A human types perhaps 2/s; 20 leaves room for
  a client with macros without leaving room for a flood. Tune with real traffic.
- `[ASSUMPTION]` `Submit` is unary rather than client-streaming. Streaming would cut per-Intent overhead
  but complicates rate limiting and error mapping, and at human typing rates the overhead is irrelevant.
  Behavior Agents at 100/s may change this calculus — revisit at `AW-SRV-009`.
- `[NEEDS BRIAN]` What a player should see when the World goes read-only. A typed error is the
  mechanism; the wording is a design call, and it is the one error message every player will
  eventually see.
