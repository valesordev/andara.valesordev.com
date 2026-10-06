---
id: AW-SRV-028
title: Durable cross-Zone handoff — in-transit state, acknowledgement, and tick-driven retry
epic: EPIC-02
component: server
type: feature
status: done
size: M
depends_on: [AW-SRV-003]
blocks: [AW-SRV-007, AW-SRV-027, AW-SRV-047, AW-SRV-048, AW-SRV-049, AW-SRV-050, AW-SRV-051]
lane: implementation
risk: high
---

## Context

`AW-SRV-003` AC-9 said a cross-Zone move is "produced as a Command to the target Zone's Partition"
and stopped there — a grooming gap, found on the 2026-09-18 review of PR #30. As built, exactly to
that contract: `applyMove` deletes the Entity from the source Zone, `Produce` hands the `Arrive` to
an asynchronous producer, and `Replay` discards a replayed tick's outbound Commands. So a process
that dies after publishing tick `T`'s boundary but before the `Arrive` is acknowledged, or a broker
outage longer than the delivery timeout, removes a Character from the World with no record of it
anywhere. `AW-SRV-026`'s exit-on-lost-boundary does not cover it — an `Arrive` is not a boundary —
and the fix cannot be "wait for the ack inside the tick", because `AW-SRV-002`'s rule that a broker
stall is not a tick stall is what keeps the loop deterministic.

The design that satisfies both is a handshake driven entirely by the log, in tick-space: the source
keeps the Entity, inert, until the target's acknowledgement arrives as a Command; it retries after a
number of *ticks*, not seconds, so replay re-derives every retry; the target deduplicates with state
that is hashed, deterministic, and bounded. Nothing waits on the broker, and a crash anywhere in the
exchange leaves the Entity in exactly one authoritative place with a path forward.

## User story

As a player, I want walking across a Zone boundary to be as safe as walking across a Room, so that a
server restart or a broker hiccup never makes my Character disappear.

## Scope

*Contract amended 2026-10-04 (architecture), from implementation's five questions in
`docs/feedback/AW-SRV-028-handoff-contract.md` and two adversarial reviews. The rulings are in that file,
"Architecture: the rulings". What changed: `Departures` and its time window are replaced by a per-Zone
high-water mark of decided handoff sequences, with no third record and no ordering assumption; the retry
schedule leaves hashed state; `HandoffRejected` moves to `AW-SRV-027`; every Command for an Entity in
transit is rejected `in_transit`, and what that asks of the Gateway and the roster is stated; a linkdead
body can't depart; `Goto` is in scope.*

### In scope
- **Transit.** `ZoneState.Transit`: Entities that have left this Zone and are not yet acknowledged,
  keyed by Entity ID, hashed, sorted. An Entity in transit is not in `Entities`. Each record holds the
  Entity as it will arrive (with its `handoff_seq` already incremented), the target Zone and Room, and the
  Direction (empty for a `Goto`). **It holds no retry schedule**: when the next `Arrive` is due comes from
  config, config never enters hashed state (`entity.go` says the same of the linkdead durations), so the
  engine keeps it in memory (below).
- **Every cross-Zone departure goes through the handshake**: a `Move` and a `Goto` (`AW-SRV-036`) alike.
- **A linkdead body can't depart.** `Move` and `Goto` for a linkdead actor are rejected `actor_linkdead`
  (validate). `locate` accepts it today, and `log.v1.Entity` doesn't carry the linkdead fields, so a body
  that left would arrive with its despawn timer gone. A dormant body is already not present. A Move
  that applies before the `MarkLinkdead` is fine: the body is then in transit, and the `MarkLinkdead` is
  rejected `in_transit` (below).
- **Arrive and ack.** `Arrive` carries `handoff_seq` (per-Entity, monotonic, stored on the Entity,
  incremented by the source when it leaves, so the first is 1). The target decides it (below), places the
  Entity, and produces `HandoffAck{entity_id, handoff_seq}` to the source Zone's Partition
  (`Arrive.origin_zone_id` names it). The source drops its transit record when it applies an ack whose
  entity and sequence match the record. **An ack with another sequence, or none, is ignored**: in
  A→B→C→B a late `ack(e, 1)` can reach a source that holds `Transit(e, 3)`, and matching by Entity alone
  would drop the record and lose the Entity if `Arrive(e, 3)` was lost.
- **The dedup rule: marks, which depend on no time and no ordering.** `ZoneState.Placed`: for each Entity
  that has been decided in this Zone by handoff, the highest `handoff_seq` decided here, hashed, sorted, kept
  for good. A handoff sequence only grows along an Entity's life, so an `Arrive(e, s)` is decided by this
  Zone's own state, **in this order, and the first case that matches decides it**:
  1. **Malformed:** `s` is 0, or the `Arrive`'s `handoff_seq` differs from its `entity.handoff_seq`, or it has
     no `entity` or an empty `entity.id`: rejected `invalid_arrival`, nothing placed.
  2. **By the mark, whatever else the Zone holds** (`s` at or below `Placed[e]`):
     - `s` equals the mark and the mark records a **rejection** (`AW-SRV-027`): a retry of a handoff already
       rejected. Reissue the same `HandoffRejected`, never an ack, and place nothing. Acking it would make the
       source drop a record whose Entity the target never placed and the source never restored.
     - the Zone holds `e` with stored `handoff_seq == s`: a retry of a handoff already placed. Re-ack, change
       nothing.
     - otherwise: stale, a retry that overtook the Entity's next move, however late it comes. Ack it, place
       nothing, count it. It emits no Event and no `CommandRejected`. **This applies while the Zone holds `e` in
       its own `Transit` too**: after A→B (seq 1), B placed `e` and acked, the ack was lost, and B moved `e` on
       (`Transit(e, 2)`), A's late retry of seq 1 is stale and acked, not rejected. The ack is what lets A drop
       its record.
  3. **Above the mark** (`s` is above `Placed[e]`, or `e` has no mark):
     - **3a.** the Zone holds `e` in `Entities`, or in its own `Transit` with a sequence at or above `s`: an invariant
       violation, rejected `entity_present` (post-log), with an `error` logged. Only a misrouted `Arrive`
       reaches it, since sequences only grow and a Zone that held `e` at or above `s` has a mark for it
       unless `e` began there;
     - **3b.** the Zone holds `e` in its own `Transit` with a lower sequence: the implicit ack below, and then the
       arrival is decided again, once: the second pass can only be a new handoff, since `Entities` and `Transit`
       are exclusive within a Zone;
     - **3c.** otherwise a new handoff: place it, set `Placed[e] = {s, rejected: false}`, ack.
  `AW-SRV-027`'s rejection of a new handoff by a faulted Zone also sets the mark and records the rejection,
  so a retry that follows gets the same `HandoffRejected` and a restore at home can't be undone by one. A
  placement sets `Placed[e]` to `{s, rejected: false}`, so the flag always describes the current mark. The mark is kept for good, so a retry produced by a
  restarted source after the original ack landed is still stale: nothing here relies on how records on
  different Partitions or from different process lives are ordered. The cost is one entry per Entity that has
  been decided in a Zone by handoff, hashed with the Zone and copied by every in-tick snapshot; at the sizing
  fixture's 500 Characters that is nothing, and it grows with the Entities that cross Zones.
  `andara_handoff_placed_entries` shows it, and the runbook sets a threshold past which pruning is wanted.
  Pruning by proof is deferred (below).
- **Implicit ack.** An `Arrive(e, s)` into a Zone that still holds `e` in its own `Transit` with a lower
  `handoff_seq` proves the target placed `e` and moved it on. The Zone drops that transit record and then
  decides the arrival by the rule above. It applies only to an `Arrive` above the mark (case 3); at or below
  the mark the Arrive is stale and no such record can exist.
- **Retry with backoff, held in memory.** The live tick loop calls `Engine.DueHandoffs(tick)` after `Step`,
  and produces what it returns: the `Arrive` for the transit records that are due, at most
  `sim.handoff_retry_batch` of them per call (default 50), earliest due first (a record with no entry sorts
  first) and then in Entity-ID order, so a restart with many in-flight handoffs doesn't fill a tick's
  `sim.max_per_tick` budget and defer players' Commands. The interval after the n-th attempt is
  `min(sim.handoff_retry_ticks × 2^min(n-1, 16), sim.handoff_retry_max_ticks)`: the exponent saturates, so a
  long-stuck handoff never wraps to a zero interval. **The mechanism:** the engine keeps
  `{attempts, last attempt}` in memory per `(Entity, handoff_seq)`, not in the hash, so replay under a
  retuned config produces the same state. A record with no entry is due, and a retry then writes `(1, T)`, so
  a restart resets the backoff. `applyMove` and `applyGoto` write `(1, T)` for a **live** departure, so the
  `Arrive` they produce isn't re-sent on the same tick, and write **nothing while the engine is replaying**
  (`ReplayEach`): a record whose departure is replayed has no entry, like one restored from a snapshot, so
  every record found after a recovery is due on the first live call. The mechanism is an `Engine` field set
  at the top of `ReplayEach` and cleared by `defer`, so its early-error and hash-mismatch returns clear it too;
  a test that a replay error followed by live use leaves it unset is part of AC-6. **`DueHandoffs` is never called by
  replay.** Recovery goes
  through `ReplayEach` (`tickloop.RecoverFrom`, and the projector's engine), which runs the same `Step` as the
  live loop, so the pass lives outside `Step`: replay produces no retries, counts none and logs none, and the
  projector's engine, which must never produce, can't. An engine built by `RestoreEngine` or `NewEngine` has
  no entries, so **a record found after a recovery is due on the first live call**, and is retried within
  `ceil(n / sim.handoff_retry_batch)` ticks for `n` records. **The engine deletes the entry whenever a `Transit` record is
  dropped** (the ack, the implicit ack, and `AW-SRV-027`'s rejection), so the map holds one entry per record
  in transit. The three config values reach the `Engine` through its `Config`, as the sim reads no other
  config; a zero value means the default. **No retry is produced while the source Zone is faulted or its
  Partition is frozen** (`AW-SRV-002`): the ack couldn't be applied. A retried `Arrive` carries the same
  `handoff_seq` and Entity bytes as the first, with `actor_id` the Entity ID and no session or client ref,
  so if the first `Arrive` was lost the arrival Events lose the mover's `client_ref` echo (the Binding still
  settles, since it routes by the Entity).
- **Every Command for an Entity in transit is rejected `in_transit`**, after logging (it parsed and was
  authorized): the verbs, `UnbindCharacter` and `MarkLinkdead`. **`BindCharacter`** for a Character whose
  Entity is in any Zone's `Transit` is rejected `in_transit` too, and creates no body. Its search is unchanged
  first, every Zone's `Entities`, and then every Zone's `Transit`: for the few ticks between the target
  placing the Entity and the source applying the ack the Entity is in both, and the Bind finds it in
  `Entities`, where it is. It keeps the single-process assumption the existing cross-Zone search has, as does
  `id_reused`'s scan of every Zone's marks, which the sharding ADR lifts for all of them.
  **The first retry comes inside the Gateway's hold.** `ReleaseSession` already waits for a crossing to
  settle, bounded by `ingress.transit_hold` (default 2 s), so the first retry of a lost `Arrive` should land
  inside it: `sim.handoff_retry_ticks` defaults to 10 ticks (1 s at 10 Hz), and server startup logs a `warn`
  when `handoff_retry_ticks / sim.tick_rate` is not below a non-zero hold. It is a warning, not a refusal,
  because the default is fixed in ticks and `sim.tick_rate` is configurable (1 to 100). This holds for the
  first retry only: after an outage the interval has grown, to at most `sim.handoff_retry_max_ticks`
  (10 s), and a Character is then in limbo up to that long after the broker is back.
  **What outlasts the hold, and what it does to a Session**, from the roster's code (`roster.go`):
  - A teardown produced after the hold, `UnbindCharacter` or `MarkLinkdead`, is rejected `in_transit`
    post-log, so the produce returned nil. For a linkdead end the roster then holds the Character's live
    flag waiting for a `LinkdeadEnded` that never comes, because no body is linkdead, so the Account's
    other Characters are `already_live` until the same Character is selected again. (A `MarkLinkdead` that
    finds no body leaves the same state today, and the roster's comment calls holding the flag right; what
    is new is that the body is absent only for the transit window.)
  - A `BindCharacter` rejected post-log leaves `SelectCharacter` returning OK, with the Session bound and
    confirmed and no body. A reselect is `already_live`. It heals when the `Arrive` lands and its
    `CharacterArrived` reaches the Session, and if the target is stuck it doesn't.
  So the Gateway and roster need three things this story doesn't build, and `docs/feedback/AW-SRV-028-handoff-contract.md`
  asks PM for a story: `SelectCharacter` waits for the Character's crossing to settle, as release does;
  the roster frees a linkdead hold unless `LinkdeadEntered` is observed within the produce deadline; and a
  teardown rejected `in_transit` is retried once the Binding settles, as a failed produce is dropped and
  retried. Until then the above is the accepted outcome of a lost `Arrive` that outlasts two seconds.
- **`Replay` keeps discarding outbound Commands**: the retries are re-derived by the live loop from the
  recovered `Transit`, which is the point.
- A round-trip test `EntityState → log.v1.Entity → EntityState` over every field `log.v1.Entity` carries.
  The fields it deliberately doesn't carry are `room_id` (the `Arrive` names the Room), the dormant fields
  and the linkdead fields (such a body never moves). Anything else added to `EntityState` and not to the
  proto fails the test naming the field.
- Every place that builds or copies a `ZoneState` carries `Transit` and `Placed`: a swapped-in Zone
  (`content.go`), `state.go`'s constructor, `snapshot_codec.go`'s restore, and `Clone` in `snapshot.go`, so
  the in-tick snapshot copy holds them.

### Out of scope
- Holding or queueing a player's Commands during transit: presentation (`AW-SRV-011`, `AW-SRV-010`).
  This story rejects `in_transit`; a Gateway that masks it does so on top.
- **`HandoffRejected` and the target answering `zone_faulted`: `AW-SRV-027`.** Under today's freeze
  (`AW-SRV-002`) a faulted target never answers, so an Entity in transit to it stays safely in the
  source's `Transit`, retrying on its backoff. `AW-SRV-027` adds the rejection and the restore at home
  (`log.v1` field 14, `HandoffRejected`, is pinned in `log.proto` for it and not used here). **There is no operator action to release a stuck Entity before
  that**: its Character gets `in_transit` on every Bind until the fault is resolved, which is `AW-SRV-027`'s
  to give a path to. Until then it is accepted, and `docs/runbooks/simulation-lagging.md` will say so when
  this story's runbook change lands.
- Pruning marks by proof. A `HandoffClosed` record, produced by the source after the ack and applied by the
  target, could drop a mark, but its proof is that the record follows every retry on the target's Partition,
  which doesn't hold across a restart of the source (a new life can produce a retry after the old life's
  record landed) without also holding retries until the Partition has caught up on recovery. It is a story
  of its own if `andara_handoff_placed_entries` passes the runbook's threshold.
- Relocation to `fallback_room`: `AW-SRV-012` has it, and `applyArrive` already places an arrival whose
  Room is gone in the fallback Room with `EntityRelocated{room_removed}`.
- Whether a restart clears a Zone's fault. The repo is inconsistent (`server/sim/state.go` and
  `AW-SRV-027` say "until restart" while the snapshot carries `Faulted` and nothing resets it on
  recovery), and it is `AW-SRV-027`'s to settle.
- Multi-process ownership: the protocol is correct across processes because every step is a logged
  Command and the dedup rule reads only the target's own state. The `BindCharacter` search is the one thing
  here that isn't.
- **The state projector omits an Entity in transit**: it computes records from `Entities` only
  (`server/projector/projector.go`), so a Character stuck in `Transit` is absent from the state topic and
  any index built from it until it lands. That is accepted here, and `AW-SRV-019`'s projector is the place
  to revisit it if stuck handoffs become common.
- **A constraint on later stories: an Entity ID is never reused.** Marks are kept for good, so an Entity
  deleted and created again under the same ID would start at `handoff_seq` 0 and have its handoffs read as
  stale: it would be lost. Today only `BindCharacter`'s never-bound path creates an Entity at seq 0, with the
  `character_id` the roster mints, and the only deletions are a cross-Zone departure. It is guarded here:
  that creation is refused (`id_reused`) when any Zone holds a mark for the ID. Later stories keep the
  constraint: `AW-SRV-032`'s `PurgeCharacter` removes a body and the roster never reissues its
  `character_id`; `AW-SRV-047`'s Item Instances, which are removed and placed again, take IDs that are never
  reused. Clearing marks in every Zone is not an option, since it would cross Partitions and reopen the
  stale-retry hole.

## Acceptance criteria

1. **Given** a Character moving A→B **when** the `Arrive` is never delivered (producer failure
   injected) **then** the Character stays in A's `Transit`, `in_transit` answers its Commands, and
   after `sim.handoff_retry_ticks` ticks the `Arrive` is produced again; on delivery B places it,
   acks, and A's transit record is gone on the tick that applies the ack, with its schedule entry deleted.
   The retried `Arrive` has the same `handoff_seq` and Entity bytes as the first.
2. **Given** the source process killed after publishing tick `T`'s boundary and before the
   `Arrive` is acknowledged **when** it recovers **then** `Transit` still holds the Character at the
   recovered hash, the first live call to `DueHandoffs` retries it, and the Character arrives in B exactly
   once. This is asserted through `tickloop.RecoverFrom`, which uses `ReplayEach`, and not only through
   `Replay`.
3. **Given** a retried `Arrive` delivered twice **when** B applies the second **then** it re-acks, emits no
   Event, and B's `ZoneCanonicalBytes` and `Placed` are unchanged by it.
4. **Given** the Character moved A→B→C and a retry of `Arrive(seq k)` reaches B after it left with
   `seq k+1` **when** B applies it **then** it is stale: nothing is placed and it is acked. **And given** that
   retry reaches B after a thousand ticks more than any retry window, or from a source process that
   restarted after B's first ack **then** the outcome is the same, because B's mark for the Entity is
   `k` and nothing prunes it. **And given** B moved the Entity on and so holds `Transit(e, k+1)` when the
   retry of `k` arrives (the ack was lost) **then** it is stale-acked, with no `entity_present` rejection, no
   `error` log and no Event, and A drops its record.
5. **Given** the Character moved A→B→C→B (seq `k`, `k+1`, `k+2`) **when** a retry of `Arrive(seq k)` reaches
   B while it holds the Entity at `k+2` **then** it is stale. **And given** two Entities, X decided in B at
   seq 5 and Y arriving at B at seq 1 for the first time **then** Y is placed: a mark is per Entity, not per
   Zone.
6. **Given** the same records replayed from boundaries **when** the exchange is replayed **then** every hash
   matches. **And given** the node recovers with a different `sim.handoff_retry_ticks` or
   `sim.handoff_retry_max_ticks` **then** the hashes still match, since the schedule isn't in the hash.
   **And given** a replay that ends with `n` records in `Transit` **when** the first live ticks run **then**
   every record is retried within `ceil(n / sim.handoff_retry_batch)` ticks, none on the departure tick, and
   no more than the batch on any tick, and **the replay itself produced no retry, wrote no schedule entry and
   counted none**, including for a record whose departure was in the replayed range.
7. **Given** a source Zone that is faulted, or whose Partition is frozen **when** ticks pass **then** no
   `Arrive` is produced for its transit records, and **when** it can apply again **then** the first tick
   retries. **And given** an unanswered handoff **then** the gaps between attempts double from
   `sim.handoff_retry_ticks` up to `sim.handoff_retry_max_ticks` and stay there, including at attempt counts
   of 64 and beyond, asserted on the Commands produced.
8. **Given** a Character in transit **when** a record for one of its Commands (hand-built, since a
   well-behaved Gateway holds it before the log) applies **then** it is rejected `in_transit` and nothing
   is applied. This holds for a verb, `UnbindCharacter` and `MarkLinkdead`. **And given** a linkdead
   actor **when** a `Move` or `Goto` applies **then** it is rejected `actor_linkdead` and the body stays.
9. **Given** an `Arrive` for an Entity the Zone holds, with `s` above the Zone's mark for it **then** it is
   rejected `entity_present`, and so is one with `s` above the mark for an Entity the Zone holds in its own
   `Transit` at a sequence at or above `s`; **and given** `s` at or below the mark with the Zone holding the
   Entity in its own `Transit` **then** it is stale-acked, not rejected (AC-4); **and given** `s == 0`, an `Arrive.handoff_seq` that differs from its
   `entity.handoff_seq`, or no `entity` or an empty `entity.id` **then** it is rejected `invalid_arrival`;
   all place nothing.
10. **Given** `EntityState` gains a field in a later story **when** the round-trip test runs without the
    proto gaining it **then** the test fails naming the field.
11. **Given** a Character in transit **when** a `BindCharacter` for it applies, at the source **then** it is
    rejected `in_transit` and no body is created. **And given** the target has placed the Entity but the
    source hasn't applied the ack **then** the Bind finds it in `Entities`, where it is. Across the whole
    exchange no Zone pair holds two `Entities` with one ID, asserted by a scan, and the `Transit` and
    `Entities` copies coexist only until the ack is applied.
12. **Given** an `Arrive(e, s)` with `s` above the Zone's mark for `e`, at a Zone that holds `e` in its own
    `Transit` with a lower seq **when** applied **then** the transit record is dropped and the arrival is
    decided by the dedup rule, once.
13. **Given** a Zone with `Transit` records and `Placed` marks **when** it is snapshotted and restored **then**
    `HashZone` is identical, and `BodyStateHash` refuses a body with an unsorted or duplicated `transit` or
    `placed`, a `placed` mark of 0, a `rejected` mark whose Entity is also held at that sequence, an Entity in both `entities` and `transit` of one Zone, or a `transit`
   Entity that is dormant or linkdead. **And given** a mark with `rejected` set **then** it round-trips and is
   in the hash: restoring it with the flag lost changes `HashZone`.
14. **Given** A holds `Transit(e, 3)` after A→B→C→B **when** a late `HandoffAck(e, 1)` applies **then** the
    record stays, and **when** `HandoffAck(e, 3)` applies **then** it is dropped.
15. **Given** a `Goto` into another Zone **when** it applies **then** the Entity goes through the same
    `Transit`, `Arrive` and `HandoffAck` path as a `Move`, with an empty `Direction`.
16. **Given** a `BindCharacter` that would create a body for an ID some Zone holds a mark for **then** it is
    refused `id_reused` and nothing is created.

## Interface contract

The wire and snapshot shapes are in the protos, pinned by architecture:
`docs/specs/protocol/andara/log/v1/log.proto` (`Arrive.handoff_seq = 6`, `Entity.handoff_seq = 6`,
`HandoffAck` at `LoggedCommand` 13, and 14 `HandoffRejected` pinned for `AW-SRV-027`) and
`docs/specs/protocol/andara/state/v1/zone_state.proto` (`ZoneState.transit = 9`, `placed = 10`,
`EntityState.handoff_seq = 13`, and `TransitRecord` and `PlacedArrival`). A transit record embeds
`state.v1.EntityState`, the hashed shape, and `entity.room_id` is the origin Room. `HandoffAck` is not a
verb: the table refuses to bind it, as it refuses `Arrive`, and the ingress accepts only `Intent`, never
a `LoggedCommand` (`AW-SRV-010`).

```go
// CONTRACT SKETCH — server/sim, not an implementation
type TransitRecord struct {
    Entity    EntityState // as it will arrive; Entity.HandoffSeq is the handoff's sequence
    To        ZoneID
    Room      RoomID      // the Room in To
    Direction Direction   // empty for a Goto
}
type ZoneState struct { /* … */ Transit map[EntityID]TransitRecord; Placed map[EntityID]uint64 }
// Engine, not hashed: a schedule keyed by (EntityID, handoff_seq) holding {attempts, last attempt}.
// A missing entry is due. applyMove and applyGoto write (1, T) for a live departure and nothing while
// replaying (ReplayEach). The entry is deleted when the Transit record is dropped.
// Engine.DueHandoffs(tick) []*logv1.LoggedCommand, called by the live loop after Step and never by replay: the due
// records, earliest first then Entity-ID order, at most sim.handoff_retry_batch, skipped while the Zone is
// faulted or its Partition is frozen.
```

**Canonical bytes.** Each hashed record is written only when present, as `entity_dormant` and
`entity_linkdead` are: a `transit` and a `placed` record per entry, with the `rejected` flag written only when
true, and `entity_handoff` only for a non-zero `handoff_seq`. The tag names are implementation's, and the round-trip and hash tests pin them.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `sim.handoff_retry_ticks` | `ANDARA_HANDOFF_RETRY_TICKS` | `10` | 1 s at 10 Hz; the first retry interval. Greater than 0 (validated at startup in `server/config`, beside its other checks). Should exceed the broker round trip and stay below `ingress.transit_hold` once converted by `sim.tick_rate`: a startup `warn`, never a refusal |
| `sim.handoff_retry_max_ticks` | `ANDARA_HANDOFF_RETRY_MAX_TICKS` | `100` | 10 s at 10 Hz; the longest gap between attempts. At least `sim.handoff_retry_ticks`; validated at startup |
| `sim.handoff_retry_batch` | `ANDARA_HANDOFF_RETRY_BATCH` | `50` | the most `Arrive` retries produced in one tick. Greater than 0; validated at startup |

### Error taxonomy

`in_transit` (validate; the actor is between Zones, and a `BindCharacter` for a Character whose Entity is),
`actor_linkdead` (validate; a `Move` or `Goto` by a linkdead body, in the Zone or across one), `stale_arrival` (consumed, acked, nothing
placed, no Event), `entity_present`, `invalid_arrival` and `id_reused` (validate; nothing placed).
The new rejection codes (`in_transit` already exists, plus `actor_linkdead`, `entity_present`,
`invalid_arrival` and `id_reused`) are added to `sim.RejectCodes()`, the closed label set of the rejection
metric. `stale_arrival` is not a rejection: it is counted by `andara_handoff_stale_arrivals_total`.
`HandoffRejected` and its codes are `AW-SRV-027`'s. The `actor_not_found` a `Move` in flight used to get is
now `in_transit`, and that test is rewritten.

## Data / state impact

`Transit`, `Placed` and `EntityState.HandoffSeq` are hashed Zone state, carried by the snapshot body
(`ZoneState.transit`, `placed`, `EntityState.handoff_seq`) so a restore reproduces the hash. Each is written
only when present, so a World with nothing in transit and no handoff history hashes as it did before, and
`sim.StateVersion` doesn't move: the fields are additive. The retry schedule is not in hashed state.

**A log written before this story that contains a cross-Zone move does not replay.** The old code deleted
the Entity at the source; the new code leaves it in `Transit`, so the hash at that tick differs from the
recorded one and recovery exits `6` there. `dev` has such logs. Pre-launch the remedy is
`make world-reset ENV=dev CONFIRM=andara-dev` with the deploy that carries this story (it destroys `dev`'s
Characters and Accounts), and a fresh local stack for the planned
`AW-INF-032`. The PR says so, and says whether the golden fixture log contains a cross-Zone move.
`andara.commands.v1` gains one record kind (13), tick-produced. **It isn't reversible once a handoff has
been logged:** a binary from before this story meets `HandoffAck` as an unsupported Command and drops
`Transit` from snapshots as unknown fields, so its replay diverges and recovery exits `6`. The recovery is
the same reset, `make world-reset ENV=dev CONFIRM=andara-dev`, and the PR says so.

## Observability requirements

*SRE review, 2026-10-05: amended after architecture's contract (#403). The log bound below is a requested
change: architecture's reply accepting it is a message that isn't in the repository, and it isn't confirmed there
yet. The reasoning is in `docs/feedback/AW-SRV-028-handoff-contract.md`.*

- **Metrics:** `andara_handoffs_in_transit` (gauge, no labels); `andara_handoff_retries_total`
  (counter); `andara_handoff_stale_arrivals_total` (counter); `andara_handoff_placed_entries` (gauge, no
  labels: marks summed over Zones). No Entity or Zone labels: four series in all. *(`andara_handoff_rejected_total`
  moves to `AW-SRV-027` with the rejection.)* **The two gauges are derived from the Engine's state on every
  tick** (the `len` of each Zone's `Transit` and `Placed`, so the cost is the number of Zones, not the number
  of marks) and never incremented, so a restart or a restore reads the restored values from its first tick.
  The counters start at 0 after a restart.
- **Logs:** **at most one summary `warn` per `sim.handoff_retry_ticks` window**, aggregating the window's
  retries, with the fields `retries` (the count over the window), `oldest_attempt`, `tick`, `in_transit` and
  `trace_id`. *(Confirmed 2026-10-05 by architecture, §8 review below; not built: the code still logs one per tick,
  built in #409 (`flushRetries`, `TestHandoffLoop_RetriesAreSummarizedOncePerWindow`).)* Under a sustained broker outage the retries land on most ticks, so one `warn` per tick that produced
  retries is about ten lines a second. One `debug` per retry, with `entity_id`, `from_zone`, `to_zone`, `seq`
  and `attempt`, so a restart with many stuck handoffs doesn't write a line each; that line is how a retry is
  correlated to its handoff. `error` on `entity_present`, `invalid_arrival` and `id_reused`. **`trace_id` on the
  retry lines is the tick's trace:** a retry carries no trace of the originating `Move`, live or after a recovery.
- **Traces:** the first `Arrive` and the `HandoffAck` carry the originating `Move`'s trace id, so a handoff that
  settles first time is one trace from keystroke to ack. **A retry carries no trace id.** It is produced in the *producing* tick's context and published with that tick's
  other cross-Zone Commands, and gets no span at production; the retry log lines carry that tick's trace. It is
  applied later, by the target Zone's tick after the broker round trip, and with no trace id for `command.ParentFrom`
  to extract (`server/command/pipeline.go`: an empty value returns the context unchanged), the retried `Arrive`'s
  `command.apply` is a direct child of the *applying* tick's `sim.tick` span (the loop also links it to `sim.tick`,
  which here is its own parent). So it sits neither in the originating `Move`'s trace nor in a trace root of its
  own, and the log lines and the span are in two different traces. It has no Gateway trace to inherit a sampling
  decision from, so `Loop.Begin` marks it for `SpanFilter`, and it is exported only when the applying tick's number
  is a multiple of 100 (`server/telemetry/sampling.go`), never an always-sampled root. An overrun keeps `sim.tick`
  but not this span, so an overrun tick's exported `sim.tick` has no retry child. A restart with many stuck
  handoffs must not export a trace per retried Entity. *(`DueHandoffs`' comment says a retry "starts a trace of its
  own", and the implementation record's Deviations says "a retry starts a new trace"; what happens is the above.)* A retry can carry none because the
  `Transit` record is hashed and holds no trace (`TransitRecord{Entity, To, Room, Direction}`), so there is nothing
  to copy one from. #403's text said a retry carries the `Move`'s traceparent where it is known; this departs from
  it, and the implementation record's Deviations records that as agreed with architecture on 2026-10-04. That
  record's phrase "as the Observability section says it should" was true of #403's wording, and this section now
  says what is built.
- **Alerts:** none. `andara_handoffs_in_transit` above 0 is ordinary traffic. Sustained for minutes with
  `rate(andara_handoff_retries_total[5m])` rising, it means the broker or the target Partition: a handoff in
  flight longer than `sim.handoff_retry_ticks` retries, so the retry rate is the age signal and no age gauge is
  needed. It has a dashboard panel (`andara-tick-health`, *Cross-Zone handoffs*) and a diagnostic step
  (`docs/runbooks/simulation-lagging.md`, Diagnose step 7). The same runbook says an Entity stuck in transit to
  a faulted Zone has no release before `AW-SRV-027`, and sets the thresholds on `andara_handoff_placed_entries`
  past which pruning is wanted (25,000 and 100,000, both unmeasured). No SLO covers handoffs, and before
  `AW-SRV-027` an operator has no action to take on one, so an alert would page for nothing. Revisit with
  `AW-SRV-027`: a ticket on sustained retries, which needs an SLO that Brian sets.

## Test plan

- **Unit:** AC-1, 3 to 9 and 11 to 16 on the stepped clock with an injectable producer; AC-4 with a retry
  delivered after a thousand ticks and from a source that restarted; AC-5's A→B→C→B and its two-Entity case;
  AC-6 recovering under a changed config; the backoff schedule exactly, at attempt counts of 64 and
  beyond; the round-trip test (AC-10); the snapshot round trip (AC-13).
- **Integration (`make test-integration`):** AC-2 with `SIGKILL` between boundary and ack, on the broker.
- **Sizing fixture with marks (SRE's ask, agreed in architecture's message of 2026-10-04, which isn't in the repository; a follow-up, not a Definition-of-done
  item of this story):** give the snapshot sizing fixture (`server/simtest/sizing.go`: 16 Zones, 25,000
  Entities, the 15 ms `snapshot.max_stall_ms`) a population of `Placed` marks, at 25,000 and at 100,000, and
  record the in-tick copy and the State Hash cost with them, so the two thresholds in
  `docs/runbooks/simulation-lagging.md` ("The handoff marks") can be replaced by measured ones. The fixture's
  copy already takes about 12 of its 15 ms without marks, so that is where the headroom shows.
- **Manual/operator:** none in this story. `sim repl` has no failure-injection flag, so a manual walk
  through a lost `Arrive` isn't expressible as a product command (CLAUDE.md §9); AC-2's integration test
  stands for it. A flag is a follow-up, with the player-facing text for `in_transit`.

Mutation checks to record: dropping the mark check makes AC-4 fail; deciding the own-`Transit` case before
the mark makes AC-4's lost-ack case fail; treating a mark as removable by time
makes AC-4's second half fail; a per-Zone scalar in place of the per-Entity mark makes AC-5's two-Entity
case fail; matching an ack by Entity alone makes AC-14 fail; letting a Bind search only `Entities`, or only
`Transit`, makes AC-11 fail; producing retries while the source is faulted makes AC-7 fail; dropping the
implicit ack makes AC-12 fail; letting the exponent overflow makes AC-7's 64-attempt case fail; hashing the
schedule makes AC-6's changed-config case fail; routing `Goto` around the handshake makes AC-15 fail; dropping
the `id_reused` guard makes AC-16 fail; dropping `actor_linkdead` makes AC-8's second half
fail; not rejecting `UnbindCharacter` or `MarkLinkdead` in transit makes AC-8's first half fail; registering
no entry at a live departure (so the departure tick re-sends) makes AC-6's last case fail, and writing entries
while replaying (so a replayed departure isn't due on the first live call) makes AC-6's replay case fail; running the retry pass inside `Step` (so replay produces retries) makes AC-6's
replay case and AC-2's `RecoverFrom` case fail; not deleting the entry when the record is dropped makes AC-1's
schedule assertion fail; dropping the batch cap, or ordering by Entity ID alone, makes AC-6's batch case
fail; dropping a field from the proto makes AC-10 fail.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-003`'s record notes that `AW-SRV-012` replaced the one-hop bounce (done
2026-10-04); the PR states the `make world-reset ENV=dev CONFIRM=andara-dev` consequence and whether the
golden fixture contains a cross-Zone move; `docs/runbooks/simulation-lagging.md` has the handoff step.

## Open questions

- ~~`[ASSUMPTION]`~~ **Resolved 2026-10-05 (architecture):** the defaults ship as built. `handoff_retry_ticks` = 10, `handoff_retry_max_ticks` = 100 and `handoff_retry_batch` = 50.
  A retry is cheap and idempotent; too short doubles broker traffic under a slow broker, too long is a player
  stuck "between places". Tune on the stack, with `ingress.transit_hold` in view.
- **Resolved 2026-09-18 (Brian):** the delay stays perceptible in the Events and the Gateway holds
  a Session's Commands during transit (`AW-SRV-010`). The sim still rejects `in_transit`; the hold
  is what keeps a well-behaved Gateway from ever reaching that path.
- **Resolved 2026-10-04 (architecture):** the `Departures` window is gone, replaced by marks that depend on
  no time and no ordering.

## Verification record — 2026-10-04 (implementation; `review` until the §8 checklist passes)

Branch `impl/aw-srv-028-durable-handoff`. The contract questions and the hand-offs to SRE are in
`docs/feedback/AW-SRV-028-handoff-contract.md`.

| AC | Test | What it asserts |
|----|------|-----------------|
| 1 | `TestHandoff_ALostArriveIsRetriedAndThenAcknowledged` (`server/sim`); `TestHandoffLoop_ALostArriveIsRetriedAndTheWorldConverges`; `TestHandoffLoop_RetriesAreSummarizedOncePerWindow` | No re-send on the departure tick; `in_transit` answers a Command; the retry comes at departure + `handoff_retry_ticks` with the same `handoff_seq` and Entity bytes, no session, client ref or trace; the record and its schedule entry go on the tick that applies the ack. Through the loop: the retry is counted, the gauge returns to 0, at most one summary `warn` per `sim.handoff_retry_ticks` window (`retries`, `oldest_attempt`, `tick`, `in_transit`, `trace_id`; #409) and one `debug` per retry. Three windows of retries on most ticks give three `warn`s, every retry still counted and logged at `debug` |
| 2 | `TestKafka_AHandoffSurvivesASIGKILLWithTheArriveLost` (Redpanda); `TestHandoffLoop_RecoveryRetriesFromTheRecoveredTransit` (through `tickloop.RecoverFrom`) | A real `SIGKILL` after the boundaries and with every `Arrive` lost: `Transit` holds alice at the recovered hash, the recovered process retries within its first ticks, and she lands in wilds once, at sequence 1 |
| 3 | `TestHandoff_ADuplicateArriveIsReAckedAndChangesNothing` | Re-acked, no Event, the Zone's canonical bytes unchanged, not counted stale |
| 4 | `TestHandoff_ALateRetryIsStaleHoweverLateItComes`; `TestHandoff_ALostAckWindowIsStaleAckedAndTheSourceDropsItsRecord` (B holds `Transit(e, 2)` with mark 1, A's retry of seq 1 is stale-acked with no rejection and A drops its record) | A retry of seq 1 after the Entity moved on, delivered 1,000 ticks later: stale, acked, nothing placed. The mark is kept |
| 5 | `TestHandoff_AMarkIsPerEntity` | A→B→C→B: a retry of seq 1 is stale at seq 3. X decided at 5 doesn't stale Y's first arrival at 1 |
| 6 | `TestHandoff_ReplayMatchesAndTheScheduleIsNotHashed` (also: replay wrote no schedule entry, even for a departure in the replayed range, and the first live call retries it); `TestHandoff_AFailedReplayLeavesTheEngineLive` (a gap and a hash mismatch both leave the engine live: the next live departure writes its entry); `TestHandoff_ARecoveredWorldRetriesEveryRecordWithinTheBatchCap`; `TestHandoffLoop_RecoveryRetriesFromTheRecoveredTransit` | Replay hashes match, also under a retuned config. A restored World with 7 records retries them all within `ceil(7/3)` ticks, at most the batch per tick, once each. Recovery produces and counts no retry |
| 7 | `TestHandoff_NoRetryWhileTheSourceIsFaulted`; `TestHandoff_NoRetryWhileThePartitionIsFrozen` (a healthy Zone on a Partition another Zone's fault froze); `TestHandoff_TheBackoffDoublesToTheMaximumAndStaysThere`; `TestHandoff_DueRecordsAreOrderedEarliestFirstThenByEntityID` | Nothing while the Zone is faulted, a retry on the first tick it can apply. Gaps 1, 2, 4, 5, 5… and still 5 past attempt 70. Earliest due first, then Entity ID |
| 8 | `TestHandoff_CommandsForAnEntityInTransitAreRejected`; `TestHandoff_ALinkdeadBodyCannotDepart` | `look`, `move`, `goto`, `UnbindCharacter`, `MarkLinkdead` and `BindCharacter` for an Entity in transit are rejected `in_transit` with the Zone unchanged. A linkdead body's `move` (in or across a Zone) and `goto` are `actor_linkdead` |
| 9 | `TestHandoff_ImpossibleArrivesAreRejected` | `entity_present` (above the mark with the Entity held; in the Zone's own Transit at the same or a higher sequence); `invalid_arrival` (seq 0, mismatched, no entity, empty id). Nothing placed or produced |
| 10 | `TestEntityState_SurvivesTheWire` | Every field `log.v1.Entity` carries round-trips; a field that doesn't fails naming it (checked by dropping `handoff_seq` from `Proto`) |
| 11 | `TestHandoff_ABindNeverMakesASecondBody` | In transit at the source: `in_transit`, no body. Target placed, ack not applied: the Bind finds it in `Entities`. No two Zones hold one ID in `Entities` at any step |
| 12 | `TestHandoff_AnArriveIsAnImplicitAckOfALowerTransitRecord`; `TestHandoff_AnArriveAtTheMarkIsNotAnImplicitAck` (only above the mark) | The record and its schedule entry are dropped, then the arrival is placed and marked |
| 13 | `TestHandoff_SnapshotCarriesTransitAndMarks`; `TestHandoff_BodyStateHashRefusesWhatItWouldReadDifferently`; `TestHandoff_ARejectedMarkIsCarriedHashedAndNeverAcked`; `TestBodyHashCoversEveryProtoField` (the fixture now has a Transit record and two marks, one rejected, and the tripwire corrupts their nested fields) | `HashZone` identical after a restore. `BodyStateHash` refuses an unsorted or duplicated `transit` or `placed`, a mark of 0, a `rejected` mark whose Entity is held at that sequence, an Entity in both `entities` and `transit`, an Entity in transit that is dormant or linkdead or carries any of their fields. A `rejected` flag round-trips, is in the hash, and a restore that loses it changes `HashZone`. Dropping `placed`, `transit` or the transit Entity from the hash, or writing the mark as a constant, now fails the tripwire |
| 14 | `TestHandoff_AnAckMatchesBySequenceNotJustEntity` | A late `ack(e, 1)` leaves `Transit(e, 3)`; `ack(e, 4)` too; `ack(e, 3)` drops it |
| 15 | `TestHandoff_AGotoUsesTheSameHandshake` | `Transit` with an empty Direction, the `Arrive`, the `HandoffAck` |
| 16 | `TestHandoff_AnEntityIDIsNeverReused` | A Bind for an ID some Zone holds a mark for: `id_reused`, nothing created |

Also: `TestHandoff_AnArriveAtTheFallbackSetsTheMark` (a landing in the fallback Room sets the mark, so a
duplicate is re-acked and not rejected). Loop: `TestHandoffLoop_AStaleArriveIsCountedAndAnImpossibleOneIsLoggedAtError`
(`andara_handoff_stale_arrivals_total`, and the `error` line on `invalid_arrival`). Config:
`TestParse_HandoffRetryKeys` (defaults, env, flags, file, the three refusals), `TestHandoffRetryOutsideHold`
(the startup `warn`) and `TestEngineConfigCarriesTheHandoffRetryKeys` (the keys reach the Engine).

**Mutation checks**, each run in a scratch worktree and each failing the case the story names: the mark
check dropped (AC-3, 4, 5); marks pruned by time (AC-4); a per-Zone scalar mark (AC-5); ack by Entity alone
(AC-14); a Bind that searches only `Entities`, and only `Transit` (AC-11 and the Bind tests); retries while
faulted (AC-7); no implicit ack (AC-12); the exponent unsaturated (AC-7); the schedule in the hash (AC-6
changed-config); `Goto` around the handshake (AC-15); no `id_reused` (AC-16); no `actor_linkdead` (AC-8); no
`in_transit` for `UnbindCharacter` and for `MarkLinkdead` (AC-8); no schedule entry at departure (AC-1); the
retry pass run during replay (the recovery test, within its first ticks); the entry not deleted with the
record (AC-1, 12); no batch cap, and ordering by Entity ID alone (AC-6, 7); the proto field dropped (AC-10).

**Changed beyond the contract's surface.** `Engine.HandoffScheduleSize` (read-only, for the schedule-deletion
assertions), the loop's `error` log on `entity_present`, `invalid_arrival` and `id_reused`, and
`ingress/bindings.go`'s comment now naming `AW-SRV-027`.

**Deviations, agreed with architecture (2026-10-04):** the retry doesn't carry the Move's `trace_id`, as the
Observability section says it should: the Transit record is hashed and can't hold one, so a retry starts a new
trace and carries `entity_id`. **Not built:** the roster and Gateway consequence of a rejected teardown (a PM
story), and `HandoffRejected` (`AW-SRV-027`), including the reissue of a rejection for a retry of a rejected
handoff: `Placed` carries the `rejected` flag in the codec and the hash, and nothing here sets it.

**The golden fixture log holds no cross-Zone move** (`simtest.Script`'s directions are `dir-N` and `nowhere`),
so `server/tickloop/testdata/golden_hashes.txt` is unchanged. **Deploying this needs `make world-reset
ENV=dev CONFIRM=andara-dev`** (a log with a cross-Zone move from before doesn't replay; a binary from before
can't read a `HandoffAck`), and a fresh local stack for `AW-INF-032`.

**Outstanding before `done`:**
- ~~`pendingFields`~~ Done: the map is empty on `main` (#407).
- ~~SRE: the three Helm keys, the runbook step, the dashboard panel and the `andara_handoff_placed_entries`
  threshold.~~ Done: #404 (keys, runbook step, thresholds) and #406 (dashboard). The two thresholds are
  unmeasured, as the runbook says.
- ~~Architecture: `AW-SRV-003`'s record.~~ Done 2026-10-04: it now says `AW-SRV-012` replaced the bounce.
- ~~SRE's §8 instrumentation check~~ Done 2026-10-05 (below): `andara_handoffs_in_transit`, `andara_handoff_retries_total`,
  `andara_handoff_stale_arrivals_total` and `andara_handoff_placed_entries` are registered and read 0 until a
  handoff; the retry counter's first live observation is on a stack with a lost `Arrive`.

## §8 instrumentation check — 2026-10-05 (SRE, `sre/aw-srv-028-observability-amendment`)

**The four series, the gauges' restore behavior and the dashboard and runbook all hold live. The summary
`warn` is the one deviation from the amended §7 (issue #409), and four observations are covered by tests
only.** Run on the local stack at `main` bc4badf (`make up` rebuilt the server image from this tree with
#407; the server reports revision `bc4badf`):

| Observation | Result |
|-------------|--------|
| The series on a fresh scrape | `andara_handoff_placed_entries`, `andara_handoff_retries_total`, `andara_handoff_stale_arrivals_total` and `andara_handoffs_in_transit` are all present at 0, pre-registered, with no labels |
| One Character walks Purgatory to Market Plaza (`andara-cli play`, `out`) | `placed_entries` 4 → 5; `in_transit` 0; `retries_total` 0; `stale_arrivals_total` 0. The handoff acknowledged inside a tick or two, so its transit window is too short to scrape |
| `docker compose restart andara-server`, ready after about 90 s | `placed_entries` **5**, `in_transit` 0, both counters 0. The mark count survived the restart because the gauge is rebuilt from the recovered state, and the counters restarted at 0, as the amended §7 says |
| Prometheus and Grafana | Prometheus scrapes all four series (5, 0, 0, 0). `andara-tick-health` has the two *Cross-Zone handoffs* panels (#406), whose four queries now return data |

**Covered by tests, not run live** (`go test -race -run TestHandoffLoop_ ./server/tickloop/`, four tests,
all pass; they read the metric objects, per CLAUDE.md §8): a lost `Arrive` is retried and acknowledged
(`HandoffsInTransit` 1 → 0, `HandoffRetries` 0 → 1, `HandoffPlacedEntries` 1, a `warn` carrying
`oldest_attempt`); a recovery retries from the recovered `Transit` and counts nothing for the replay; a stale
`Arrive` is counted and an impossible one is logged at `error`. A live retry needs an unacknowledged handoff,
which on this stack means breaking the broker mid-move: `sim repl` has no failure-injection flag (the story's
own Test plan), so `retries_total` above 0, `stale_arrivals_total` above 0, `in_transit` above 0 on a scrape,
and the `warn`, `debug` and `error` lines have not been emitted by the running server. The first story that can
make that observation carries it. No story arranges one yet: AW-INF-032's `make stack-recover` kills the server
and may catch a handoff in flight by chance, but it doesn't inject loss or assert a retry, so a failure-injection
flag (the follow-up the Test plan above names) is what would make the observation repeatable.

**The amended §7's summary `warn` is built (#409, architecture confirmed it in the §8 review).** The code first
logged one `warn` per tick that produced retries, with `count` (#407, right by #403's text). It now logs at most
one per `sim.handoff_retry_ticks` window, with `retries`, `oldest_attempt`, `tick`, `in_transit` and `trace_id`
(`server/tickloop/loop.go`, `noteRetries` and `flushRetries`; `Engine.HandoffRetryWindow`), pinned by
`TestHandoffLoop_RetriesAreSummarizedOncePerWindow`. A window still open when the loop stops is not flushed: its
retries are in the counter and the `debug` lines. The `debug` line per retry and the `error` lines are unchanged.
The §7 Logs bullet's "requested, not built" note and `docs/feedback/AW-SRV-028-handoff-contract.md` still describe
the per-tick warn; both are architecture's to update.

**A difference from #403's text that is already agreed: a retry's trace.** #403's §7 said a live retry's `trace_id`
is the originating `Move`'s and that retries carry its traceparent. The code gives a retry no trace id, because the
`Transit` record is hashed and holds no trace to copy one from, and this story's implementation record (Deviations)
records that as agreed with architecture on 2026-10-04. The amended §7 says what is built. It is also what keeps
retries at the tick's one-in-a-hundred.

**Met by construction, and read from the code only.** The gauges are derived from the Engine's state each tick
(the `len` of each Zone's maps, so the cost is the number of Zones), never incremented: tested, and observed live
across a restart. **A retry gets no span at production, and its `command.apply` is a direct child of the applying tick's `sim.tick`
span** (`command.ParentFrom` returns the context unchanged for an empty trace id, and `Loop.Begin` starts the span
from the tick's context), exported only when the applying tick's number is a multiple of 100, because it carries
no trace id (`Loop.Begin`, `DueHandoffs`): read from the code, with no test and no live observation of it. The first `Arrive` and the `HandoffAck` carry the `Move`'s trace id
(`server/sim/verbs.go`), also read from the code.

**An environment note, not a defect of this story.** `make stack-play` failed on this stack: it restarts the
server under an open session and waits 60 s, and recovery took about 90 s, because until AW-SRV-007 lands
recovery replays the whole log and this stack's log is about three days old (2.77 million ticks). A fresh
stack recovers in seconds, as CI's does. It's for AW-INF-032 and AW-SRV-007, and it's why a fresh local stack
is the right one for `make stack-recover`.

**The dev reset** (`make world-reset ENV=dev CONFIRM=andara-dev`) is not run. It needs Brian's go in the
session that runs it, at the time the deploy carrying this story reaches `dev`.

## §8 review (architecture, 2026-10-05): stays at `review`, two items open

Run after #433. Everything else holds:
- **ACs 1-16** each map to a named test in the verification record, all of which exist
  (`server/sim/handoff_test.go`, `server/tickloop/handoff_test.go`, `handoff_integration_test.go`,
  `server/config/config_test.go`, `server/sim/entity_roundtrip_test.go`); `make check` is green.
- **Config** is in `server/README.md`, `deploy/helm/andara/values.schema.json` and `keys.yaml`, and the
  `simulation-lagging.md` runbook has the handoff step. The glossary has **Handoff** and **Transit**.
- **Migration:** no schema change; deploying needs `make world-reset ENV=dev CONFIRM=andara-dev`, stated in the
  record and still waiting on Brian's go in SRE's session.
- **The one `[ASSUMPTION]`** (retry defaults 10 / 100 / 50) is resolved: the values are config, tuned without a
  contract change, and the runbook marks its thresholds unmeasured.
- **The summary `warn` bound is confirmed** as the contract: at most one per `sim.handoff_retry_ticks` window,
  carrying `retries`, `oldest_attempt`, `tick`, `in_transit` and `trace_id`.

**Closed 2026-10-05, third pass (architecture):**
1. **The `warn` is built.** #409 (#442) added `flushRetries` (`server/tickloop/loop.go`): one summary `warn` once the
   open window is `sim.handoff_retry_ticks` old, with `retries`, `oldest_attempt`, `tick`, `in_transit` and
   `trace_id`, asserted by `TestHandoffLoop_RetriesAreSummarizedOncePerWindow`. A window still open when the loop
   stops isn't flushed, as the verification record says; the stopping process's last lines aren't a retry signal.
2. **The carrier is `AW-INF-036`** (`lane: sre`, `make stack-handoff-fault`, depends on `AW-SRV-051`'s failure-injection
   mode). Its Definition of done carries the live observation of `andara_handoff_retries_total` and
   `andara_handoff_stale_arrivals_total` above 0, `andara_handoffs_in_transit` above 0 on a scrape, and the `warn`
   and `debug` lines, and records the run for this story's §8. The `error` line (AC-9) has no live carrier: a
   malformed `Arrive` can't arise in a deployed system, so it stays on
   `TestHandoffLoop_AStaleArriveIsCountedAndAnImpossibleOneIsLoggedAtError` (ruled in
   `docs/feedback/AW-SRV-051-failure-injection-surface.md`). The story moves from `review` to `done`.
