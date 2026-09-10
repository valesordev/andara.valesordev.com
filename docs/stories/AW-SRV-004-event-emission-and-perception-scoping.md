---
id: AW-SRV-004
title: Event emission, subscription seam, and perception scoping
epic: EPIC-02
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-002, AW-SRV-003]
blocks: [AW-SRV-006, AW-SRV-011, AW-SRV-019]
lane: implementation
risk: medium
---

## Context

Events are the simulation's only legitimate output (CLAUDE.md §10, glossary). This story defines the
Event type, the subscription seam through which everything outside the sim core observes the World,
and the perception scoping that decides which observer sees which Event.

Perception scoping belongs here rather than in the Gateway for one reason: it is a security
boundary. If the sim emits everything and the transport filters, then every future transport — the
Text Interface, the WebGL client, the CLI, a debugging tool — reimplements the filter, and one of
them will get it wrong. Scoping in the sim means a Character cannot learn what happens two rooms
away regardless of what client they use.

ADR-0002 changes what Events are *for*. They are **derived**: replaying the Command log regenerates
them identically, so they need not be durable before a player is told the outcome, and the durability
write leaves the tick's critical path entirely. What they carry instead is the system's read path —
every Projection, the audit trail, and the Tick Boundary Records that make replay exact.

Getting the Event type right now therefore saves both a wire migration and a log migration.

## User story

As a player, I want to see only what my Character could perceive, so that no client can be modified
to reveal the world beyond my senses.

## Scope

### In scope
- The `Event` type as protobuf (ADR-0007): immutable, past-tense, versioned, canonically serializable.
- The `TickCompleted` Tick Boundary Record carrying tick, `state_version`, per-Partition offsets, and
  State Hash (ADR-0002 §4).
- The subscription seam: an interface the sim owns, through which observers receive Events.
- A Kafka producer implementation of that seam, publishing to `andara.events.v1` keyed by `ZoneID`,
  asynchronously and off the tick.
- Perception scoping: which observers an Event is visible to, computed inside the sim.
- Event ordering guarantees within a tick and across ticks.
- Backpressure policy: a slow subscriber must never slow the Tick Loop.

### Out of scope
- Streaming Events to Sessions — `AW-SRV-011`. That is a subscriber, not the seam.
- Snapshots and recovery — `AW-SRV-006` and `AW-SRV-007`.
- The compacted state topic and the indexes built from it — `AW-SRV-019`, `AW-SRV-017`, `AW-SRV-018`.
- Rendering Events into player-facing prose. That is a client concern, including for the Text
  Interface.
- Read Projections — M3.

## Acceptance criteria

1. **Given** a Character moves from Room A to Room B **when** the tick completes **then** an observer
   scoped to Room A receives `CharacterLeft`, an observer scoped to Room B receives
   `CharacterArrived`, and an observer scoped to Room C receives neither.
2. **Given** two Events produced by the same tick **when** they are delivered **then** they arrive in
   the order the sim produced them, and that order is identical across replays of the same input.
3. **Given** an Event **when** it is serialized twice **then** the two serializations are
   byte-identical. Protobuf is not canonical by default and map field ordering is unspecified, so
   anything feeding the State Hash uses a sorted-key canonical encoder or avoids `map` fields
   entirely (ADR-0007 rule 3).
4. **Given** an Event type that has gained a field **when** an older consumer reads it **then** the
   unknown field is ignored rather than causing a parse failure, and the Event's `schema_version`
   reflects the change.
5. **Given** a subscriber that stops reading **when** its buffer fills **then** the subscriber is
   dropped with a `SubscriberDropped` Event and `andara_subscriber_drops_total` increments, and
   `andara_tick_duration_seconds` shows no increase attributable to the stalled subscriber.
6. **Given** 500 subscribers on one Room **when** an Event is emitted there **then** the tick that
   emitted it stays within the Tick Budget. Fan-out happens outside the tick.
7. **Given** an Event carrying state a Character cannot perceive **when** it is scoped to that
   Character's observer **then** the Event is either withheld entirely or delivered in a redacted
   form; it is never delivered whole. A test asserts this against at least one Event type carrying
   privileged detail.
8. **Given** a Game Master observer with world-visibility permission **when** any Event is emitted
   **then** the observer receives it, and receiving it is recorded as a privileged read in the audit
   stream.
9. **Given** the sim core source **when** the import-boundary lint runs **then** the subscription
   seam introduces no transport, serialization-library, or I/O dependency into `server/sim`.
10. **Given** a subscriber registered mid-tick **when** that tick completes **then** the subscriber
    receives Events from tick `T+1` onward and never a partial tick.
11. **Given** a completed tick **when** its Events are published **then** a `TickCompleted` record is
    published last, carrying the tick number, `state_version`, the per-Partition offset range the tick
    applied, and the State Hash.
12. **Given** the Kafka producer is unavailable **when** a tick completes **then** the tick still
    completes and Events are still delivered to Sessions, because Events are derived and regenerable.
    Production is retried; `andara_event_publish_failures_total` increments. A broker outage must not
    stop the World from being played.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation

package sim

type EventID uint64   // monotonic within a World; part of state, part of StateHash

type Event struct {
    ID            EventID
    Tick          Tick
    Type          EventType
    SchemaVersion uint16
    Zone          ZoneID
    Payload       EventPayload   // typed per EventType
}

// Scope answers "who may perceive this". Computed inside the sim, never by a transport.
type Scope struct {
    Room     *RoomRef   // observers in this Room
    Entities []EntityID // explicitly addressed observers
    World    bool       // privileged; GM/operator visibility only
}

type ScopedEvent struct {
    Event
    Scope Scope
}

// EventSink is the seam. The sim owns this interface; implementations
// (transport fan-out, durable log, projections) live outside the core.
// Publish must not block; a slow sink is dropped, never awaited.
type EventSink interface {
    Publish(ScopedEvent)   // non-blocking, must return promptly
}

func (e *Engine) Subscribe(s EventSink) SubscriptionID
func (e *Engine) Unsubscribe(SubscriptionID)

type EventType string

const (
    EvRoomDescribed     EventType = "room_described"
    EvCharacterArrived  EventType = "character_arrived"
    EvCharacterLeft     EventType = "character_left"
    EvCommandRejected   EventType = "command_rejected"
    EvZoneFaulted       EventType = "zone_faulted"
    EvSubscriberDropped EventType = "subscriber_dropped"
    EvSimulationStopped EventType = "simulation_stopped"
)
```

**Guarantees:**

- Events within a tick are totally ordered by `EventID`, ascending.
- `EventID` is monotonic across ticks and never reused, including across a recovery.
- `Publish` is called synchronously from the tick but must return without blocking. Fan-out,
  serialization, and delivery happen on the subscriber's side of the seam.
- Redaction is applied before `Publish`, not by the subscriber.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `events.subscriber_buffer` | `ANDARA_SUBSCRIBER_BUFFER` | `1024` | per-subscriber; overflow drops the subscriber |
| `events.max_subscribers` | `ANDARA_MAX_SUBSCRIBERS` | `10000` | registration beyond this is rejected |

## Data / state impact

`EventID` and the next-ID counter become part of World state and therefore part of `StateHash` and
of every future snapshot. `EventID` must not restart after a recovery — an observer that reconnects
must not see reused IDs.

`SchemaVersion` per Event type exists from the first Event ever emitted. This is the migration hook
for `AW-SRV-006`: the durable log will contain Events written by older code, forever, and the only
affordable time to add the version field is before the first one is written.

Payload evolution rule: fields may be added; fields may not be removed or have their meaning changed
within a `SchemaVersion`. Removing a field requires a version bump and a documented reader path for
both versions.

## Observability requirements

### Metrics
- `andara_events_emitted_total` — counter. Labels: `type`. Cardinality: bounded by the EventType
  enum.
- `andara_event_fanout_duration_seconds` — histogram. Labels: none. Measured outside the tick.
- `andara_subscribers` — gauge. Labels: none.
- `andara_subscriber_drops_total` — counter. Labels: `reason` (`buffer_full`, `unsubscribed`,
  `shutdown`).
- `andara_event_scope_redactions_total` — counter. Labels: `type`.
- `andara_event_publish_failures_total` — counter. Labels: `reason`.
- `andara_event_publish_lag_seconds` — gauge. How far the Event producer trails the tick.

Entity ID, Room ID, and Subscription ID are rejected as labels.

### Logs
- `warn` on subscriber drop: `subscription_id`, `reason`, `buffered`, `session_id` when known.
- `info` on privileged world-scope subscription: this is an audit event, and it names the actor.
- Required fields: `ts`, `level`, `msg`, `service`, `env`, `trace_id`, `tick`, and `session_id`
  wherever a Session is attributable.

### Traces
- `event.fanout` — span per tick's fan-out, not per Event. Attributes: `event_count`,
  `subscriber_count`. Per-Event spans are explicitly not emitted.

### Alerts
None. `andara_subscriber_drops_total` is a dashboard panel. Alerting requires the Session SLO, which
arrives with `AW-SRV-005`.

## Test plan

- **Unit:** scope computation for Room-scoped, Entity-scoped, and World-scoped Events; redaction
  applied before publish; serialization determinism (AC-3); unknown-field tolerance (AC-4); ordering
  within and across ticks; ID monotonicity.
- **Integration:** 500 subscribers on one Room asserting no tick-duration increase; a deliberately
  stalled subscriber asserting drop-not-block; subscriber registered mid-tick asserting it starts at
  `T+1`; an information-leak fixture asserting a privileged-detail Event is withheld or redacted for
  an unprivileged observer.
- **Manual/operator:**
  ```
  ./andara-cli sim repl --content ./testdata/content/valid --tap-events
  # move a character; observe the scoped event stream for two observers in different rooms
  ```

## Definition of done

CLAUDE.md §8, plus:
- The perception-scoping test includes at least one case that would be an information leak if
  scoping were done in the transport.
- `Event`, `Projection`, and `Scope` semantics are reflected in `docs/glossary.md`.
- Serialization determinism is asserted in CI.

## Open questions

- `[ASSUMPTION]` Perception is Room-scoped for Phase 1. Senses with longer reach — shouting, scrying,
  a Zone-wide announcement — are additional `Scope` shapes, not a different mechanism, and are added
  by the stories that introduce them.
- `[NEEDS BRIAN]` Whether a Character perceives Events in an adjacent Room at all (hearing a fight
  next door is a MUD staple). This adds a `Scope` shape but does not change the seam.
- Serialization is protobuf, per ADR-0007. The canonical-encoding requirement in AC-3 is the part
  most likely to be got subtly wrong; it is asserted by test, not by review.
- `[ASSUMPTION]` `TickCompleted` goes on `andara.events.v1` rather than its own topic, so a replay
  reads one ordered stream. Splitting it would mean correlating two streams by offset, which is
  strictly worse.
