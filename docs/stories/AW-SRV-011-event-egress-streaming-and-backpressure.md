---
id: AW-SRV-011
title: Event egress — server-streaming subscription with per-session backpressure
epic: EPIC-03
component: server
type: feature
status: in-progress
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

**Re-groomed 2026-09-20 (implementation, in-branch), before the code.** The sketch this story
carried predated `AW-SRV-004`'s `events.Hub` and `AW-SRV-005`'s `event.proto`; both exist, and the
contract below is written against them. The old sketch — `SessionSink.Publish(ScopedEvent)`, an
`EventEnvelope` wrapping `Event | Heartbeat | Resync` — is retired; the corrections are itemised
under Open questions.

### The seam

```go
// CONTRACT SKETCH — not an implementation

// The tick side is AW-SRV-004's, unchanged: Engine → Hub.Publish (one enqueue) →
// Hub goroutine → one bounded channel per Subscription.

// Egress implements gateway.Egress over one events.Hub. Each Session that
// subscribes holds ONE Hub subscription for as long as the Session lives, read by
// its own goroutine into a ring of egress.resume_window sent Events; a Subscribe
// stream is a cursor over that ring.
type Egress struct{ /* ... */ }
func New(Options{Hub, Observers, Buffer, ResumeWindow, HeartbeatInterval, ...}) *Egress
func (e *Egress) Subscribe(ctx, *gateway.Session, *SubscribeRequest, *ServerStream[EventEnvelope]) error
func (e *Egress) Rebind(sessionID string)   // the routing table's binding changed; re-read perception
func (e *Egress) Drain()                    // streams ending from here on are `draining`

// Observers is where a Session perceives from: ingress.Bindings in production.
type Observers interface { Observer(sessionID string) (events.Observer, bool) }
```

```protobuf
// additions to andara/game/v1 — as built
message SubscribeRequest {
  string session_id    = 1;
  uint64 last_event_id = 2;   // 0: from now
  bool   world         = 3;   // ask for World visibility; requires game_master|operator
}
// EventEnvelope.payload gains two STREAM frames, event_id 0, no EventType:
//   Heartbeat heartbeat = 17;   // tick = last Tick the server has seen
//   Resync    resync    = 18;   // { last_event_id, reason: resume_window_exceeded | no_history }
```

### Drop policy

When a stream trails by more than `egress.buffer`, the **stream** is ended with a typed reason, never
degraded by dropping Events. Dropping individual Events would leave the client with a silently
incorrect view of the world — it would believe a Character is in a room they left. An ended stream is
honest and recoverable — the Session survives, keeps retaining, and the client reopens with
`last_event_id` — while a gap is neither. This is the same reasoning as AC-6's explicit Resync.

A writer blocked inside a Send at that moment is reset from the pump's goroutine
(`gateway.AbortStream`, a write deadline in the past); a reset that has not returned the writer within
`egress.heartbeat_interval` means the client has stopped reading its socket and the connection's
writer is blocked in the kernel with the reset behind it, and the connection is closed
(`gateway.DropConnection`) — the Session is *disconnected*, as AC-4 says, the way a pulled cable would
disconnect it.

### Error taxonomy

`ErrorInfo{domain: andara.stream, reason}` on every stream error.

| Condition | gRPC code | `reason` |
|-----------|-----------|----------|
| stream trailed by more than `egress.buffer` | `RESOURCE_EXHAUSTED` | `buffer_full` |
| a stream already open on the Session | `FAILED_PRECONDITION` | `already_subscribed` |
| `events.max_subscribers` reached | `RESOURCE_EXHAUSTED` | `too_many_subscribers` |
| `world` without `game_master` or `operator` | `PERMISSION_DENIED` | `world_visibility` |
| fan-out shut down | `UNAVAILABLE` | `draining` |
| Session revoked (AW-SRV-008 AC-12) | `PERMISSION_DENIED` | `revoked`, after a final `SubscriberDropped{reason=revoked}` frame |
| gateway draining (AW-SRV-005) | `UNAVAILABLE` | — (`server draining`) |

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `egress.buffer` | `ANDARA_EGRESS_BUFFER` | `1024` | Events a stream may leave unsent; past it the stream is ended `buffer_full` |
| `egress.resume_window` | `ANDARA_EGRESS_RESUME_WINDOW` | `2048` | sent Events retained per Session for a resume; at least `egress.buffer` |
| `egress.heartbeat_interval` | `ANDARA_HEARTBEAT_INTERVAL` | `20s` | silence before a Heartbeat; also the reset-to-disconnect escalation |

`egress.max_sessions` is not a key: `events.max_subscribers` (AW-SRV-004) already bounds the
process's subscriptions, one per subscribed Session, and a second bound on the same thing would be
two numbers that must agree. `events.subscriber_buffer` is the Hub→pump buffer and guards the
process (a starved pump), not the client; `egress.buffer` is the client's.

`resume_window` interacts with ADR-0006's linkdead grace period: a Character held for 180 seconds whose
client reconnects must be able to resume. If the window is too small for the grace period at the
observed Event rate, every linkdead reconnect becomes a resync. Both numbers belong in the same
conversation, and `AW-SRV-015` owns the other one. **Reconciled 2026-09-20:** the retained history is
keyed by Session here; `AW-SRV-015`'s reconnect is a *new* Session selecting the same Character, so
that story moves the key to the Character and keeps the pump alive for `linkdead_grace` after the
stream drops — the ring and the pump are built to be handed over, not rebuilt.

### As built (2026-09-20)

- `server/egress`: `Egress` (implements `gateway.Egress`) — `New(Options{Hub, Observers, Buffer,
  ResumeWindow, HeartbeatInterval, Abort, Disconnect, LastTick, Log, Tracer, Registry})`,
  `Subscribe`, `SubscribeWith(ctx, session, req, Sender)` for a harness, `Rebind`, `Drain`,
  `Metrics`. Per Session: one `events.Subscription`, a pump goroutine, a `history` ring (seq-indexed;
  `resume(last)` → seq or a Resync reason), one `stream` at a time. `Observers`/`ObserverFunc`.
  Errors `ErrBufferFull`, `ErrAlreadySubscribed`, `ErrTooManySubscribers`, `ErrNotPrivileged`,
  `ErrDraining`; the Hub's `ErrClosed`/`ErrNotPrivileged`/`ErrTooManySubscribers` map onto them.
- `server/gateway`: `AbortStream(ctx)` (the request's `http.ResponseController`, stashed in ctx by a
  handler wrapper; a past write deadline resets the stream) and `DropConnection(ctx)` (closes the
  request's `net.Conn`; `connState` tears its Sessions down as for a dropped client);
  `SessionEnder`, the optional interface an Egress implements to be told of a revoked Session before
  its context is canceled (`Egress.EndSession(id, reason)`, bounded at 1 s); `mapSeamError` lets a
  code a seam chose itself, other than `CANCELED`, stand ahead of the session-closed mapping.
- `server/events`: `Hub.LastTick()`.
- `server/command`: `Binding.Room`; `ingress.Bindings` clears it on `CharacterLeft`, sets it on
  `CharacterArrived`, and gains `OnChange func(sessionID)` called after `Bind`/`Unbind`.
- `boot.Runtime.StartEvents` (the Hub, moved out of `StartTickLoop` so the gateway can take the
  egress at construction), `StartEgress` (wires `Bindings.OnChange = Egress.Rebind`),
  `Runtime.Egress`; `main.go`'s `OnDrain` calls `Egress.Drain` before readiness flips.
- Proto: `SubscribeRequest.world`, `EventEnvelope.heartbeat` (17), `.resync` (18), `Heartbeat`,
  `Resync`; `gen/` regenerated.
- Config: `egress.buffer`, `egress.resume_window`, `egress.heartbeat_interval`; `keys.yaml`'s
  pending rows for them corrected to this table (they had `256`, `15s`, `ANDARA_EGRESS_HEARTBEAT_INTERVAL`)
  and `egress.max_sessions` removed. `docs/specs/slo/session-availability.md`,
  `docs/runbooks/sessions-dropping.md`, `SessionsDroppingAtRate` in `alerts.yaml`.

## Data / state impact

Per-Session resume buffers are process-local and are lost on restart — a reconnect after a server
restart always resyncs. That is correct: the client's view is being rebuilt from an authoritative World
that itself just recovered.

Event IDs must not restart after a recovery (`AW-SRV-004`), or a resumed client would see reused IDs
and silently misorder its view.

## Observability requirements

### Metrics
- `andara_stream_subscribers` — gauge: open `Subscribe` streams. (`andara_subscribers` is the
  Hub's, and counts the CLI's tap and a projector too.)
- `andara_stream_events_sent_total` — counter, label `type`. The EventType enum plus `heartbeat`
  and `resync`.
- `andara_session_egress_drops_total` — counter, label `reason` (`buffer_full`, `client_gone`,
  `draining`, `revoked`).
- `andara_sessions_in_drop_state` — gauge: Sessions whose last stream the server ended
  (`buffer_full`, `draining`) and that have neither reopened one nor ended. The SLI integrates it.
- `andara_stream_buffer_depth` — histogram, sampled across Sessions. Not per-Session — that is a
  cardinality bomb.
- `andara_stream_resyncs_total` — counter, label `reason` (`resume_window_exceeded`,
  `no_history`). A rising resync rate means `resume_window` is too small.
- The fan-out duration is `andara_event_fanout_duration_seconds` (AW-SRV-004), measured outside the
  tick; a second series for the same batch is not emitted.

Session ID and Entity ID are rejected as labels.

### Logs
- `warn` on disconnect-for-buffer-full: `session_id`, `buffered`, `last_sent`.
- `info` on privileged world-scope subscribe, with the actor. This is an audit line.
- Required fields: `ts`, `level`, `msg`, `service`, `env`, `session_id`, `trace_id`.

### Traces
- `event.fanout` — one span per batch's fan-out, not per Event (AW-SRV-004): `event_count`,
  `subscriber_count`, `tick`.
- The `Game/Subscribe` RPC span carries `stream.world`, `stream.last_event_id`, and
  `stream.resumed` or `stream.resync`.
- Per-Event and per-Entity spans are explicitly not emitted (CLAUDE.md §7).

### Alerts
This story writes `docs/specs/slo/session-availability.md`:

- **SLI:** fraction of Session-seconds during which the Session was connected and its stream was not
  in a drop state.
- **Target / window:** 99.5% over a rolling 28 days. Proposed as a player experience target before
  an engineering one; **decided by Brian 2026-09-20.**
- **Alert:** `SessionsDroppingAtRate` on `andara_session_egress_drops_total` rate against the SLO,
  paired with `docs/runbooks/sessions-dropping.md`, shipped in this story.

As built: SLI `1 − ∫drop-state Sessions / ∫(streams + drop-state Sessions)` over 28 d, from
`andara_sessions_in_drop_state` (review of PR #37: the first cut divided a drop rate by streams,
which is a frequency, not a fraction); alert `buffer_full`+`draining` above 1 % of open streams
per minute for 5 m.

## Test plan

- **Unit:** scoping delivery for Room, Entity, and World scopes; ordering and ID monotonicity; the
  resume window at, inside, and beyond its boundary; heartbeat emission on an idle world; drop policy
  choosing disconnect over gap.
- **Integration:** 500 subscribers on one Room asserting no tick-duration change (AC-3); a stalled TCP
  receiver over 60 ticking seconds asserting bounded memory (AC-5); Kafka producer failure asserting
  Sessions still receive Events (AC-10); drain asserting typed stream closure.
  As built (`server/egress`, over a real TLS gateway): `TestFanout_500Streams` (Publish under 5 ms
  with 500 real streams, every stream in order), `TestStalledStream_ResetKeepsSession` (a client
  that stops consuming: the flow-control window closes, the stream is reset, the Session stays,
  heap grows by the window and not the 94 MiB produced) and `TestStalledSocket_EndsStream` (a
  client that stops reading its socket: the writer returns either by reset or by the connection
  being closed, heap bounded the same way; `TestEscalation_DisconnectsWhenResetDoesNotReturn`
  pins the escalation), `TestEventsFlowWhenPublisherFails`, `TestDrain_EndsStreamTyped`,
  `TestDropConnection_TearsDownSessions`; and in `server/boot`, `TestStartEgress_MemoryLoopback`
  (Submit → tick → stream, with the binding arriving after the subscribe).
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
- Inherited from `AW-SRV-004`'s §8 pass (2026-09-19): this story is the first in-cluster subscriber
  of `events.Hub`, so §8's backend verification here includes showing `andara_subscribers` above zero,
  `andara_subscriber_drops_total{reason="buffer_full"}` from a deliberately stalled Session, the `warn`
  drop line in Loki with `session_id`, and the `subscribe_world` audit record for a Game Master stream
  — the subscriber-side series `AW-SRV-004` could only exercise in tests and `sim repl`.
  **Restated 2026-09-20:** with the Hub subscription per Session and drained by the egress's own
  goroutine, a stalled Session shows on `andara_session_egress_drops_total{reason="buffer_full"}` and
  the Hub's `buffer_full` is the starved-process case. `andara_subscribers` and `subscribe_world`
  were shown live (verification record); the stalled-Session drop and its warn line need a Session
  that receives Events, and are carried to `AW-SRV-014` with the rest of the Event-delivery record.

### Verification record (2026-09-20, compose stack, image built from this branch)

- `make check` clean; `server/egress` under `-race` ×5.
- **Live, from the running server** (`.local/probe`, an operator Session): a player-scope stream
  and a World-scope stream open together — `andara_stream_subscribers` and `andara_subscribers` both
  2 in Prometheus; heartbeats at 20 s carrying the loop's Tick (`tick=1681390` → `1681590`, 200
  ticks apart at 10/s; the first build reported `tick=0` on a World that emits nothing, which is
  why the loop's `OnTick` now feeds the egress); a resume from `last_event_id=5` opens with
  `Resync{no_history}` and `andara_stream_resyncs_total{reason="no_history"}` 1; a second
  `Subscribe` on the Session is `FAILED_PRECONDITION already_subscribed`; `docker compose restart
  andara-server` under an open stream ends it `UNAVAILABLE server draining` (AC-8). Loki: `stream
  resync: resume point not retained` with `session_id`, `last_event_id`, `reason`, `trace_id`;
  `world-scope subscription: privileged read` with `actor_account_id`. Tempo: the `Game/Subscribe`
  root with `stream.world`, `stream.last_event_id`, and `stream.resync`. `andara.audit.v1`: one
  `subscribe_world` record per World stream, two streams → two records (AC-9, once, not per Event).
  `SessionsDroppingAtRate` loaded by the compose Prometheus (`health: ok`, inactive).
- **Not reachable from the running server until `AW-SRV-014` binds a Character** (§8's
  no-caller rule): Event delivery on a stream (AC-1, AC-2), a stream ended `buffer_full` and its
  `warn` line, a resume that replays retained Events, `andara_stream_buffer_depth` samples,
  `andara_stream_events_sent_total` for an EventType. Each is exercised over a real TLS gateway by
  the tests named in the test plan, and `AW-SRV-014` carries the live observation.
- `andara_session_egress_drops_total{reason="draining"}` was not caught by a scrape: the drain
  ends the process within three seconds. `TestDrain_EndsStreamTyped` asserts it through the
  gateway's `OnDrain`.
- **After the review of PR #37** (image rebuilt at `c9d2b88`+): `andara_sessions_in_drop_state`
  and `andara_session_egress_drops_total{reason="revoked"}` are registered and scraped at zero
  from the running server. The drop state needs a stream ended `buffer_full` — `AW-SRV-014`'s
  observation with the rest; the revoked path needs the bootstrap operator's Account disabled
  under an open stream, which the local stack has no second operator to do from, so it is
  `TestRevoked_StreamEndsWithFrame` through the gateway's recheck loop.
- A revoked Session whose client has stopped reading its socket: `EndSession` returns after its
  second, the writer stays blocked until the close's reset, and there is no `Disconnect` escalation
  on that path — the goroutine lives until the connection dies, as any 005 teardown of a stalled
  client does. Noted, not changed.

## Open questions

- **Resolved 2026-09-20 (re-groom, in-branch).** The seam is `events.Hub`, as inherited; the
  contract above is rewritten against it and `event.proto`. Corrections, in the order the code
  met them:
  1. **Per-Session Hub subscription, not per-stream.** A subscription made at `Subscribe` starts
     at the next tick, so every Event between a stream ending and the next `Subscribe` would be
     lost and AC-6's *no gap* satisfiable only on paper. The subscription outlives the stream; the
     Session's own goroutine retains into the resume window; the stream is a cursor. Consequence:
     the Hub's `buffer_full` now guards the pump (a starved process), not the client, and the
     inherited DoD line about `andara_subscriber_drops_total{buffer_full}` "from a stalled Session"
     is restated: the stalled Session shows on `andara_session_egress_drops_total{buffer_full}`.
  2. **`Heartbeat` and `Resync` are payloads of the existing `EventEnvelope` oneof** (17, 18), with
     `event_id` 0 and no EventType, not a wrapper around `Event`. AC-2's *strictly increasing*
     applies to Events; stream frames do not move a resume point.
  3. **World visibility is asked for** (`SubscribeRequest.world = 3`), not implied by the role: a
     Game Master who does not ask perceives from their Character. Decided by Brian 2026-09-21.
  4. **AC-4's *disconnected* means the stream, then the connection.** A stream that trails is ended
     typed and the Session survives (the SLI counts a Session as connected and not in a drop state;
     `SubscriberDropped` says *resubscribe*); a client that has stopped reading its socket cannot be
     told anything, and its connection is closed after `egress.heartbeat_interval`.
  5. **Metrics reconciled** with `AW-SRV-004`'s: the Hub's subscriber gauge, fan-out histogram and
     span are not duplicated; `andara_stream_subscribers` counts streams; `andara_stream_resyncs_total`
     gains a bounded `reason` label.
  6. **`egress.max_sessions` dropped**; `events.max_subscribers` is the bound. `keys.yaml`'s pending
     rows (`AW-INF-003`) had guessed `256`/`15s`/`ANDARA_EGRESS_HEARTBEAT_INTERVAL`; this table wins.
  7. **A Session's perception can change after it subscribes** (`AW-SRV-014` binds after
     `OpenSession`, and a `Subscribe` may already be open): `ingress.Bindings.OnChange` →
     `Egress.Rebind` replaces the Hub subscription and discards the old perception's history —
     a later resume from before it is `no_history`.
  8. **The resume window and `AW-SRV-015`** reconciled as written under Configuration: keyed by
     Session here, handed to the Character there. One consequence for 015 to carry:
     `events.max_subscribers` now bounds Sessions that have *ever* subscribed — each holding a pump
     goroutine and up to `resume_window` retained envelopes for the Session's life — not concurrent
     streams; linkdead makes Sessions linger, so 015 owns the number.

- **Resolved 2026-09-20 (review of PR #37, four findings):**
  1. `history.reset` kept no floor, so a resume from before a rebind became a silent replay once
     the new perception had delivered. A `floor` now survives resets — the old perception's last
     ID — and a resume at or below it is `no_history`; `resume_window_exceeded` keeps meaning the
     window is too small.
  2. A fan-out drop wedged the Session: every later `Subscribe` reported `buffer_full` with no
     subscription held. A subscription the fan-out ended no longer counts as one; the next
     `Subscribe` subscribes again, history discarded, and a resume from before it is
     `Resync{no_history}`.
  3. The SLI could not measure the decided target (a drop rate over streams is a frequency, not a
     fraction of Session-seconds). `andara_sessions_in_drop_state` is the drop state as a gauge;
     the SLI is the 28-day ratio of integrals; the budget line says 0.5 % of the fleet's
     Session-seconds; the deploy blind spot is stated.
  4. `SubscriberDropped{reason=REVOKED}`, handed here by `AW-SRV-008` AC-12 and never groomed in:
     landed. The gateway's close for outcome `revoked` calls `Egress.EndSession` before the
     cancellation (`gateway.SessionEnder`), the stream's last frame is the notice, the stream ends
     `PERMISSION_DENIED revoked`, and a code a seam chose stands in `mapSeamError` even when the
     Session has since closed. `client_gone` now means the client.
  Also from the review: a World stream is re-audited on every resubscribe — intended, each
  subscription is one privileged read (AC-9's *once* is per subscription, not per Event); the Resync
  frame's failed `Send` is counted like any other end; `Rebind` compares against the subscribe-time
  Observer, not the Room the Hub has since followed the Entity to, so a same-Actor `Bind` after a
  sim-driven move discards history spuriously — rare, noted, and `AW-SRV-014` decides whether its
  `Bind` carries the Room.

- **Inherited from `AW-SRV-004` (2026-09-19 review of PR #32), contract-bearing — confirmed
  2026-09-20.** The seam this story consumes is `events.Hub`, not the `SessionSink.Publish(ScopedEvent)`
  once sketched here: `Hub.Subscribe(ctx, Subscriber{Observer{Entity, Room, World}, Principal,
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

  Both landed on #32 (`Subscription.follow`, `forms.envelope`); `TestClientRefOnlyToOwnSession` and
  `TestObserverFollowsEntity` in `server/events` assert them, and the egress hands the Hub's
  envelope to the wire unedited, so neither needed work here.

- **Resolved 2026-09-20 (review of PR #37, the drop policy accepted as written):** end the stream on
  overflow rather than drop-and-continue, for the reason in the drop policy above. If a lossy mode is
  ever wanted for a spectator or replay client, it is a distinct subscription type, not a degraded
  version of this one.
- **Resolved 2026-09-20 (review of PR #37):** one `Subscribe` stream per Session. Multiplexing
  several would let a client separate chat from combat; nothing here forecloses it. As built: a
  second `Subscribe` is `FAILED_PRECONDITION` (`already_subscribed`); the client ends the first.
- **Resolved 2026-09-21 (Brian): World visibility is opt-in per stream** (`SubscribeRequest.world`),
  never implied by the role. An operator playing a Character sees what the Character sees; the
  privileged view is a deliberate act, audited each time it is taken. The wire contract stands as
  built; `AW-CLI-004` carries the flag.
- **Resolved 2026-09-20 (Brian): the Session availability target is 99.5 % over 28 days**, as
  proposed. `AW-SRV-014` validates it against first measurement.
