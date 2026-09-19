---
id: AW-SRV-011
title: Event egress — server-streaming subscription with per-session backpressure
epic: EPIC-03
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-004, AW-SRV-005]
blocks: [AW-SRV-009, AW-CLI-004]
lane: implementation
risk: high
---

## Context

`AW-SRV-004` computes which observers may perceive which Events, inside the simulation, because
perception is a security boundary rather than a rendering concern. This story delivers those Events to
Sessions over the `Game.Subscribe` server stream.

The constraint that shapes everything here: **a slow client must never slow the tick.** The sim's
`EventSink.Publish` is called from the tick and must return promptly. Every buffer, every flow-control
decision, and every drop policy sits on the far side of that call.

## User story

As a player, I want the world's events to arrive on my screen as they happen, and I want a slow
connection to cost only me, so that one bad network does not lag everyone in the room.

## Scope

### In scope
- `Game.Subscribe`: a server-streaming RPC delivering perception-scoped Events for the Session.
- Per-Session buffering and drop policy, entirely off the tick goroutine.
- Fan-out from the sim's `EventSink` to Sessions, batched per tick.
- Stream resume: reconnecting with a last-seen Event ID.
- Heartbeats so a silent world is distinguishable from a dead connection.
- The Session availability SLO and its alert.

### Out of scope
- Perception scoping itself — `AW-SRV-004`. The Gateway filters nothing and adds nothing.
- Publishing Events to Kafka — `AW-SRV-004`. That is a different subscriber to the same seam.
- Rendering Events as prose — `AW-CLI-004`. That is a client concern, including for the text client.
- Projections — `AW-SRV-017`, `AW-SRV-018`.

## Acceptance criteria

1. **Given** a Session subscribed and a Character in Room A **when** another Character arrives in Room A
   **then** the Event is delivered on the stream, and **when** an Event occurs in Room C **then** it is
   not.
2. **Given** Events produced by one tick **when** they are delivered **then** they arrive in the order
   the sim produced them, and Event IDs on a stream are strictly increasing.
3. **Given** 500 Sessions subscribed to one Room **when** an Event is emitted there **then**
   `andara_tick_duration_seconds` shows no attributable increase. Fan-out happens outside the tick.
4. **Given** a Session whose client stops reading **when** its buffer fills **then** the Session is
   disconnected with a typed reason, `andara_session_egress_drops_total` increments, and no other
   Session and no tick is affected.
5. **Given** a deliberately stalled TCP receiver **when** the World keeps ticking for 60 seconds
   **then** memory attributable to that Session stays within its configured buffer bound. gRPC flow
   control must not become an unbounded server-side queue.
6. **Given** a Session that reconnects with `last_event_id` **when** the requested Events are still
   within the resume window **then** the stream resumes from the next Event with no gap and no
   duplicate; **and given** they are not **then** the server returns a typed `resync_required` rather
   than silently skipping. A silent gap is worse than an explicit resync.
7. **Given** an idle World **when** `heartbeat_interval` elapses with no Events **then** a heartbeat
   frame is sent, so a client can distinguish a quiet world from a dead connection.
8. **Given** a Session subscribed **when** the server begins draining for shutdown **then** the stream
   is closed with `UNAVAILABLE` and a reason, not dropped silently.
9. **Given** a Game Master Session with world-scope permission **when** any Event is emitted **then** it
   is delivered, and the privileged subscription is recorded once in `andara.audit.v1` at subscribe
   time — not once per Event.
10. **Given** the Kafka Event producer is failing **when** a tick completes **then** Sessions still
    receive their Events, because Events reach Sessions through the in-process seam and reach Kafka
    through a different subscriber to that same seam. A broker problem must not stop the game from
    being visible.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation

// SessionSink implements sim.EventSink. Publish is called FROM THE TICK and must
// return promptly: it does one bounded, non-blocking enqueue per interested
// Session and nothing else. All serialization, framing, and writing happens on
// the Session's own goroutine.
type SessionSink struct{ /* ... */ }

func (s *SessionSink) Publish(e sim.ScopedEvent)   // non-blocking; never errors upward

type SessionStream struct {
    SessionID   SessionID
    buf         chan EventEnvelope   // bounded by egress.buffer
    lastSent    EventID
}
```

```protobuf
message SubscribeRequest {
  string session_id    = 1;
  uint64 last_event_id = 2;   // 0 for a fresh subscription
}

message EventEnvelope {
  oneof payload {
    Event     event     = 1;
    Heartbeat heartbeat = 2;
    Resync    resync    = 3;   // resume window exceeded; client must resync
  }
}
```

### Drop policy

When a Session's buffer is full, the Session is **disconnected**, not degraded by dropping Events.
Dropping individual Events would leave the client with a silently incorrect view of the world — it
would believe a Character is in a room they left. A disconnect is honest and recoverable; a gap is
neither. This is the same reasoning as AC-6's explicit `resync_required`.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `egress.buffer` | `ANDARA_EGRESS_BUFFER` | `1024` | per Session; overflow disconnects |
| `egress.resume_window` | `ANDARA_EGRESS_RESUME_WINDOW` | `2048` | Events retained per Session for resume |
| `egress.heartbeat_interval` | `ANDARA_HEARTBEAT_INTERVAL` | `20s` | |
| `egress.max_sessions` | `ANDARA_MAX_SESSIONS` | `10000` | subscribe beyond this returns `RESOURCE_EXHAUSTED` |

`resume_window` interacts with ADR-0006's linkdead grace period: a Character held for 180 seconds whose
client reconnects must be able to resume. If the window is too small for the grace period at the
observed Event rate, every linkdead reconnect becomes a resync. Both numbers belong in the same
conversation, and `AW-SRV-015` owns the other one.

## Data / state impact

Per-Session resume buffers are process-local and are lost on restart — a reconnect after a server
restart always resyncs. That is correct: the client's view is being rebuilt from an authoritative World
that itself just recovered.

Event IDs must not restart after a recovery (`AW-SRV-004`), or a resumed client would see reused IDs
and silently misorder its view.

## Observability requirements

### Metrics
- `andara_stream_subscribers` — gauge.
- `andara_stream_events_sent_total` — counter, label `type`. Bounded by the EventType enum.
- `andara_session_egress_drops_total` — counter, label `reason` (`buffer_full`, `client_gone`,
  `draining`).
- `andara_stream_buffer_depth` — histogram, sampled across Sessions. Not per-Session — that is a
  cardinality bomb.
- `andara_stream_resyncs_total` — counter. A rising resync rate means `resume_window` is too small.
- `andara_stream_fanout_duration_seconds` — histogram, measured outside the tick.

Session ID and Entity ID are rejected as labels.

### Logs
- `warn` on disconnect-for-buffer-full: `session_id`, `buffered`, `last_sent`.
- `info` on privileged world-scope subscribe, with the actor. This is an audit line.
- Required fields: `ts`, `level`, `msg`, `service`, `env`, `session_id`, `trace_id`.

### Traces
- `event.fanout` — one span per tick's fan-out, not per Event. Attributes: `event_count`,
  `session_count`, `dropped`.
- Per-Event and per-Entity spans are explicitly not emitted (CLAUDE.md §7).

### Alerts
This story writes `docs/specs/slo/session-availability.md`:

- **SLI:** fraction of Session-seconds during which the Session was connected and its stream was not
  in a drop state.
- **Target / window:** proposed 99.5% over a rolling 28 days. `[NEEDS BRIAN]` — this is a player
  experience target before it is an engineering one.
- **Alert:** `SessionsDroppingAtRate` on `andara_session_egress_drops_total` rate against the SLO,
  paired with `docs/runbooks/sessions-dropping.md`, shipped in this story.

## Test plan

- **Unit:** scoping delivery for Room, Entity, and World scopes; ordering and ID monotonicity; the
  resume window at, inside, and beyond its boundary; heartbeat emission on an idle world; drop policy
  choosing disconnect over gap.
- **Integration:** 500 subscribers on one Room asserting no tick-duration change (AC-3); a stalled TCP
  receiver over 60 ticking seconds asserting bounded memory (AC-5); Kafka producer failure asserting
  Sessions still receive Events (AC-10); drain asserting typed stream closure.
- **Manual/operator:**
  ```
  make up
  andara-cli play           # terminal 1
  andara-cli play           # terminal 2, same room
  # move in terminal 1; terminal 2 sees it
  # SIGSTOP terminal 2's process; keep playing in 1; terminal 2 is disconnected, 1 is unaffected
  ```

## Definition of done

CLAUDE.md §8, plus:
- The stalled-client test asserts a memory bound, not merely that the tick survived.
- `docs/specs/slo/session-availability.md` and `docs/runbooks/sessions-dropping.md` exist.
- The resume-window and linkdead-grace interaction is reconciled with `AW-SRV-015` before either ships.

## Open questions

- **Inherited from `AW-SRV-004` (2026-09-19 review of PR #32), contract-bearing — re-groom before
  start.** The seam this story consumes is `events.Hub`, not the `SessionSink.Publish(ScopedEvent)`
  sketched above: `Hub.Subscribe(ctx, Subscriber{Observer{Entity, Room, World}, Principal,
  SessionID}) → *Subscription` with `Events() <-chan Delivery`, `Reason()`, drop-on-full ending the
  subscription with `SubscriberDropped`, `events.subscriber_buffer` / `events.max_subscribers`
  already configured. Two rules this story must carry:
  1. **An Observer bound to an Entity follows that Entity inside the Hub.** As built, the
     Subscription's Room is moved by the consumer (`Subscription.Move`) after it reads
     `CharacterArrived` off its own buffer — so between the sim moving the Character and the
     Session calling back, Room-scoped Events in the new Room are missed and old-Room Events still
     arrive: perception eventually-consistent with the consumer's read latency, which AC-1 of 004
     exists to forbid. The Hub sees `CharacterLeft`/`CharacterArrived` first, in order, addressed to
     the Entity, with the target Room on the envelope; it updates the Observer's Room before the
     next delivery. `Move` is not a Gateway duty. During an `AW-SRV-028` transit the Observer is
     Room-less and Entity-addressed Events still reach it — the correct state. Requested on #32; if
     it lands there this item is a confirmation, not work.
  2. **`client_ref` is blanked in one place.** The envelope echoes the actor's `client_ref` to every
     recipient. Rather than every transport remembering to blank it, `sim.Event` carries the
     originating `SessionID` (in-process only, from the Record) and the Hub blanks `client_ref` at
     `send` for any subscriber whose `SessionID` differs. This story asserts a bystander's stream
     never carries another Session's ref.

- `[ASSUMPTION]` Disconnect-on-overflow rather than drop-and-continue, for the reason in the drop policy
  above. If a lossy mode is ever wanted for a spectator or replay client, it is a distinct subscription
  type, not a degraded version of this one.
- `[ASSUMPTION]` One `Subscribe` stream per Session. Multiplexing several would let a client separate
  chat from combat; nothing here forecloses it.
- `[NEEDS BRIAN]` The Session availability target.
