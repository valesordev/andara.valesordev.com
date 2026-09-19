---
id: AW-SRV-004
title: Event emission, subscription seam, and perception scoping
epic: EPIC-02
component: server
type: feature
status: in-progress
size: M
depends_on: [AW-SRV-002, AW-SRV-003]
blocks: [AW-SRV-006, AW-SRV-011, AW-SRV-019, AW-SRV-029]
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

### As built (2026-09-18)

- `server/sim`: `Scope{Room RoomRef, Entities []EntityID, World bool}` with `ScopeRoom`, `ScopeZone`
  (Room empty = every Room in the Zone), `ScopeEntities`, `ScopeWorld`, `.With(...)`, `.AndWorld()`;
  `ApplyContext.Emit(scope, env)`; `Event` gains `Scope` and `Redacted` (the player-safe form the sim
  prepares when the type carries operator detail); `Event.Deliverable(privileged)`.
- `server/events`: `Hub` (the Engine's one sink), `Observer{Entity, Room, World}` — an Entity-bound
  Observer follows its Entity inside the Hub — `Subscriber{Observer, Principal, SessionID}`,
  `Hub.Subscribe` → `*Subscription` with `Events()`, `Reason()`; `Hub.Unsubscribe`, `Flush`, `Close`
  (delivers what is queued first); `Delivery{ID, Tick, Type, Envelope}`; errors
  `ErrTooManySubscribers`, `ErrNotPrivileged`, `ErrClosed`. `sim.Event.Session` (in-process only)
  lets the Hub hand `client_ref` to the causing Session alone.
- `server/tickloop`: `EventRecord` (deterministic marshal, Scope on `log.v1.Event`),
  `KafkaPublisher.OnBoundaryAcked` for the publish-lag gauge.
- `auth.ActionSubscribeWorld`; `boot.Runtime.Events`; `andara-cli sim repl --tap-events`.

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

- **Inherited from `AW-SRV-002` (2026-09-18):** the shipped seam is `sim.Event{ID, Tick, Zone,
  Type, Envelope}`, `sim.EventSink{Publish(Event)}`, and `Engine.Subscribe(EventSink)` with no
  `SubscriptionID` — this sketch's return value is yours to add if you need it. The Event ID counter
  is already in the State Hash. `SimulationStopped` carries `event_id` 0 and consumes no ID; it is a
  lifecycle notification, not World history, and a recovered process's next real Event takes the ID
  it would have taken. Keep it or argue it here before implementing.
  **Kept, 2026-09-18.** The Engine keeps one sink and no `SubscriptionID`; subscriptions and their
  IDs live on `events.Hub`, which is the one sink boot registers — after recovery, so replayed
  history is neither fanned out nor counted again. `SimulationStopped` stays `event_id` 0: the Hub
  treats it as lifecycle — delivered to every subscriber in the form its privilege allows, not
  scoped and not gated on the start tick, then every subscription ends with reason `shutdown`.
- **Resolved 2026-09-18: what 002 already built.** `TickCompleted` published last (AC-11), the async
  Kafka producer keyed by Zone with retries (AC-12), IDs monotonic across recovery, `schema_version` on
  `log.v1.Event`. AC-12's `andara_event_publish_failures_total{reason}` is 002's
  `andara_tick_publish_failures_total{kind}` — one series, not two; verified by stopping Redpanda under
  the compose stack: 206 ticks during the outage, `/readyz` 200, ticking resumed on return.
- **Corrected 2026-09-18 (implementation): World visibility is the view of everything.** AC-8 says a
  Game Master observer receives *any* Event; so an Observer with `World` sees every Event, and a
  Scope's `World` flag marks detail only such observers may see whole. World subscriptions require
  `game_master` or `operator`, are refused otherwise (`ErrNotPrivileged`), and are audited once, at
  subscription (`subscribe_world`) — not per Event, which is what the Logs section already said.
- **Corrected 2026-09-18 (implementation): redaction is two forms, chosen by privilege.** "Before
  Publish, not by the subscriber" and "depends on the observer" meet in the middle: the sim emits the
  whole envelope and, for a type carrying operator detail, a redacted one beside it (`ZoneFaulted`
  without the Zone ID, `SimulationStopped` without the reason). The Hub picks a form by the
  observer's privilege and never edits one. The DoD's leak case is `RoomDescribed` and
  `CommandRejected`: addressed to the actor alone, so a bystander in the same Room — who a transport
  filtering by Room would have sent them to — receives nothing.
- **Resolved 2026-09-18:** `SchemaVersion` is one number on `log.v1.Event` (`tickloop.EventSchemaVersion`
  = 1) rather than a per-type table: every payload type shares the envelope and the one field is what
  an older reader checks. A per-type bump is a per-type field when a type first needs one.
- **Added 2026-09-18:** `andara_event_fanout_dropped_total` — Events the Hub could not even queue
  because its own goroutine was starved. Not in the metric list; any value above zero is a process
  problem, and silently blocking the tick would have been the alternative.
  `andara_event_publish_lag_seconds` is measured as Tick Boundary Record publish → broker ack.
- `[ASSUMPTION]` Perception is Room-scoped for Phase 1. Senses with longer reach — shouting, scrying,
  a Zone-wide announcement — are additional `Scope` shapes, not a different mechanism, and are added
  by the stories that introduce them. **Zone-wide already exists** (a Room-less `RoomRef`), used by
  `ZoneFaulted`.
- **Review of PR #32 (2026-09-19), both findings taken:** (1) an Observer bound to an Entity follows
  it *inside the Hub*, in Event order — `CharacterLeft` addressed to it clears the Room,
  `CharacterArrived` sets it, before the next delivery — rather than a `Move` the Session stream
  would call after reading its own buffer, which left perception a read latency behind the sim
  (AC-1). Cross-Zone transit is a Room-less Observer, which is where the Character is; `Move` is gone
  and `AW-SRV-011` has nothing to build there. (2) `client_ref` is blanked in one place: `sim.Event`
  carries the causing `Session` (in-process only, not on the log record) and the Hub hands the ref to
  that Session alone. Two Codex findings taken with them: `Close` delivers what is queued before
  ending streams, and `closed` is decided under the lock `dropAll` sets it under.
- **Resolved by Brian (2026-09-19):** perceiving into an adjacent Room is an attribute on the Exit,
  set by the Builder per connection — a `Scope` shape plus a `zone.proto` Exit field, groomed as its
  own story. Nothing here changes.
- Serialization is protobuf, per ADR-0007. AC-3 is asserted in `make test`: every log record goes
  through one `Deterministic` marshal, and a descriptor walk over `log.v1` and the Event payloads
  fails on any `map` or float field.
- `[ASSUMPTION]` `TickCompleted` goes on `andara.events.v1` rather than its own topic, so a replay
  reads one ordered stream. Splitting it would mean correlating two streams by offset, which is
  strictly worse. **As shipped by 002.**
