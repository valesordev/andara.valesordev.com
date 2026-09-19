---
id: AW-SRV-030
title: Gateway read path from the Event topic — routing and perception for Zones another process owns
epic: EPIC-03
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-004, AW-SRV-010]
blocks: [AW-INF-007]
lane: implementation
risk: high
---

## Context

ADR-0001 decides that a Gateway may run in any process and that "behavior must be identical whether
the target Zone is in this process or another one"; its point 6 makes the Gateway extractable
because "the Gateway's only write is a Kafka produce". That sentence has a silent partner: the
Gateway's only *read* must then be the Event topic. Two shipped stories read the local engine
instead. `AW-SRV-004`'s `events.Hub` and `AW-SRV-010`'s `ingress.Bindings` both implement
`sim.EventSink` and are subscribed to the engine in this process — which is every Zone at
`replicaCount: 1` and, the moment sharding is switched on, only the Zones this pod consumes.

The consequence was found on the review of PR #34 (Codex, 2026-09-19): a Character crossing from a
Zone on pod A to a Zone on pod B produces `CharacterLeft` on A — whose `Bindings` marks the Session in
transit — and `CharacterArrived` on B, which A never sees. The Session's hold expires and every later
Submit is `in_transit` forever. The Hub has the same shape of gap: a Session on A whose Character
lives on B receives nothing. Neither is a bug in what shipped — both are correct at one replica and
the chart pins one — but sharding cannot be turned on until the Gateway reads the same stream every
other process writes. The seam is already right (`sim.EventSink`); what changes is the source.

## User story

As an operator, I want to raise `replicaCount` and have every Session keep routing and perceiving,
so that sharding is the configuration change ADR-0001 promised and not a migration.

## Scope

### In scope
- `events.TopicSource`: a consumer of `andara.events.v1` (every Partition, from the latest offset —
  Gateway state is not history) that decodes each `log.v1.Event` into a `sim.Event` and delivers it,
  in Partition order, to the `sim.EventSink`s the Gateway registers: the Hub and `Bindings`.
- The engine's own Events for Zones this process owns reach the Gateway **through the topic too**,
  not directly — one path, so a Session's perception does not depend on which pod its Character's
  Zone happens to be on. The engine's direct subscription of the Hub and `Bindings` is removed.
- Two additive fields on `log.v1.Event` so the topic carries what the in-process path carried:
  `redacted_payload` (field 7, the sim-prepared player-safe form for a type that has one, else empty)
  and `session_id` (field 8, the causing Session, so `client_ref` is handed to that Session alone).
- `TickCompleted` records pass through the source and are not delivered to the Hub.
- The read-path lag as an SLI: `andara_gateway_event_lag_seconds` (Event record's ack time → delivery
  to the sinks), because a Session's perception now trails the tick by a consume, and the SLO in
  `AW-SRV-011` needs the number.

### Out of scope
- Session streaming and backpressure — `AW-SRV-011`, which builds on the Hub and does not change
  when the Hub's source does.
- Cross-Zone handoff durability — `AW-SRV-028`. This story moves where `Bindings` hears about
  arrivals, not what an arrival is.
- Read Projections consuming the same topic — M3 (`AW-SRV-017`, `AW-SRV-018`).
- Extracting the Gateway to its own Deployment — a later `INF` story; this is what makes it possible.

## Acceptance criteria

1. **Given** two processes, A consuming the Partition of Zone `town` and B the Partition of Zone
   `docks`, and a Session on A bound to a Character in `town` **when** the Character moves `town →
   docks` **then** A's `Bindings` settles on `docks` within the read-path lag, the Session's next
   Submit is produced to `docks`'s Partition, and no `in_transit` is returned.
2. **Given** the same two processes and a Session on A whose Character is in `docks` **when** an
   Event is emitted in the Character's Room on B **then** the Session's Hub subscription on A
   receives it, in the form its privilege allows, with `client_ref` present only if the Session
   caused it.
3. **Given** one process consuming every Partition (`replicaCount: 1`) **when** the same Events flow
   **then** delivery to the Hub and `Bindings` is identical to AC-1 and AC-2 — the topic is the only
   path, in one process or many.
4. **Given** a `ZoneFaulted` on the topic **when** delivered to an unprivileged observer **then** the
   `redacted_payload` form is what arrives, byte-equal to what the in-process path delivered before
   this story, asserted by a fixture.
5. **Given** the source starts **when** it subscribes **then** it begins at the latest offset of every
   Partition and never replays history into the Hub; `andara_events_emitted_total` does not move
   on a restart.
6. **Given** the broker is unreachable **when** a tick completes **then** the tick still completes
   and `andara_gateway_event_lag_seconds` rises; Sessions keep their subscriptions and receive the
   backlog when the broker returns, in order. Nothing is dropped by the source; drops remain the
   Hub's per-subscriber rule (`AW-SRV-004` AC-5).
7. **Given** a `TickCompleted` record **when** the source reads it **then** it is not delivered to
   the Hub and does not count in `andara_events_emitted_total`.
8. **Given** a `log.v1.Event` written before this story (no `redacted_payload`, no `session_id`)
   **when** the source reads it **then** it is delivered whole to privileged observers and, for a
   type that has a redacted form, withheld from unprivileged ones — never delivered whole to them.
9. **Given** the sim core source **when** the import-boundary lint runs **then** nothing here reaches
   `server/sim`; the source lives beside `tickloop` and the sim is unchanged.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package events   // server/events, beside the Hub

type TopicSource struct{ /* franz-go consumer over andara.events.v1, no group */ }
func NewTopicSource(TopicSourceOptions) (*TopicSource, error)   // Brokers, Partitions (all), Metrics, Log
func (s *TopicSource) Subscribe(sim.EventSink)                  // the Hub, Bindings; called in record order
func (s *TopicSource) Run(ctx context.Context) error            // consumes from latest; returns on ctx
func (s *TopicSource) Lag() time.Duration                       // andara_gateway_event_lag_seconds
```

```protobuf
// CONTRACT SKETCH — andara/log/v1/log.proto, additive (ADR-0007 rule 1)
message Event { /* 1–6 as shipped */ bytes redacted_payload = 7; string session_id = 8; }
```

- `tickloop.EventRecord` writes both new fields from `sim.Event.Redacted` and `sim.Event.Session`.
  The in-process `sim.Event.Session` stays in-process for the sim; on the topic it is `session_id`.
- Decoding is the inverse of `EventRecord`: `sim.Event{ID, Tick, Zone, Type, Envelope, Redacted,
  Scope, Session}`; `Type` is derived from the envelope's payload arm, the same table
  `tickloop` uses to write it.
- `boot.Runtime` subscribes the Hub and `Bindings` to the `TopicSource`, not the engine; with
  `sim.source=memory` a `MemoryPublisher` feeds the same source in-process so `sim repl` and the
  loopback boot are unchanged in behavior.
- Consumer position: latest at start; no committed offsets, no consumer group — the Gateway is a
  tail reader and a restart resumes at latest by design (AC-5). Recovery of *Session* state across a
  restart is `AW-SRV-015`'s.

### Error taxonomy

`ErrEventDecode` (a record that does not decode: counted, logged at `error` with partition/offset,
skipped — one bad record must not stall every Session). No new player-facing errors.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `events.source` | `ANDARA_EVENTS_SOURCE` | `kafka` | `kafka` or `memory`; follows `sim.source` when unset |

## Data / state impact

`log.v1.Event` gains two fields, additive; `buf breaking` clean; records already on the topic decode
(AC-8). `redacted_payload` roughly doubles the bytes of the few types that carry one (`ZoneFaulted`,
`SimulationStopped`), which are rare. `session_id` on the Event record is the same identifier already
on every `LoggedCommand` record.

Rollout: a pod on the old binary and a pod on the new one can coexist — the old writes records the
new reads (AC-8); the new writes records the old ignores the extra fields of. No live-Session
impact beyond one consume of lag on the read path, which AC-6's metric makes visible.

## Observability requirements

- **Metrics:** `andara_gateway_event_lag_seconds` — gauge, no labels; `andara_gateway_events_consumed_total{type}`
  — counter, cardinality the `EventType` enum; `andara_gateway_event_decode_failures_total` — counter.
- **Logs:** `info` once at start with the Partition count and starting offsets; `error` per decode
  failure with `partition`, `offset`, `event_id`. No per-Event lines.
- **Traces:** none new; the Hub's `event.fanout` span is unchanged. The consume is not per-Event
  span-worthy.
- **Alerts:** none here. `andara_gateway_event_lag_seconds` becomes an SLI of `AW-SRV-011`'s Session
  SLO, which owns the alert.

## Test plan

- **Unit:** `EventRecord` round trip including both new fields; the decode of a record without them
  (AC-8); `TickCompleted` skipped (AC-7); decode failure counted and skipped.
- **Integration (Redpanda, `make test-integration`):** two `tickloop.Loop`s over disjoint Partition
  sets in one test process with two `Bindings` and two Hubs each fed by its own `TopicSource` —
  AC-1 and AC-2 end to end; AC-3 with one loop over every Partition asserting the same deliveries;
  AC-5 on a restart of the source; AC-6 by stopping the broker mid-test.
- **Manual/operator:** on kind with `replicaCount: 2`, two Sessions on different pods, a cross-Zone
  move; `andara_gateway_event_lag_seconds` on both pods in Prometheus; `sim repl` unchanged.

## Definition of done

CLAUDE.md §8, plus: the engine has no direct Gateway subscriber left (`grep engine.Subscribe` finds
the tick loop's own sinks only); `AW-INF-007` records that `replicaCount > 1` is unblocked; the
`docs/runbooks/world-read-only.md` "gauge 0, players still see `UNAVAILABLE`" row names the
read-path lag as a third cause.

## Open questions

- `[ASSUMPTION]` One `TopicSource` per process reading every Partition, rather than one per
  consumed Partition: a Gateway's Sessions are bound to Characters anywhere in the World, so the
  Gateway needs every Zone's Events regardless of which it simulates. The cost is one consumer of
  the whole Event stream per pod; at Phase 1 volumes that is small, and it is the same stream
  every Projection reads.
- `[ASSUMPTION]` The topic is the only path even in one process (AC-3), at the cost of a consume of
  lag for local Zones. The alternative — local Zones direct, remote through the topic — has two
  delivery orders and two latencies for the same Session, which is the "sharded and unsharded
  deployments differ" bug ADR-0001 exists to prevent.
- `[ASSUMPTION]` `session_id` goes on the Event record. It is already on the Command record the
  Event derives from and on every audit record; nothing new is disclosed to the topic's readers.
