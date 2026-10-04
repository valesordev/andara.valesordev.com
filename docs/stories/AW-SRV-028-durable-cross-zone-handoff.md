---
id: AW-SRV-028
title: Durable cross-Zone handoff — in-transit state, acknowledgement, and tick-driven retry
epic: EPIC-02
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-003]
blocks: [AW-SRV-007, AW-SRV-027, AW-SRV-047]
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
  Zone's own state:
  - `s` is at or below `Placed[e]` and the Zone holds `e` with stored `handoff_seq == s`: a retry of a handoff
    already placed. Re-ack, change nothing.
  - `s` is at or below `Placed[e]` otherwise: stale, a retry that overtook the Entity's next move, however
    late it comes. Ack it, place nothing, count it. It emits no Event and no `CommandRejected`.
  - `s` is above `Placed[e]` and the Zone holds `e`: an invariant violation, rejected `entity_present`
    (post-log), with an `error` logged. It can't happen with sequences that only grow.
  - `s` is above `Placed[e]`, or `e` has no mark: a new handoff. Place it, set `Placed[e] = s`, ack.
  - `s` is 0: malformed, rejected `invalid_arrival`. So is an `Arrive` whose `handoff_seq` differs from its
    `entity.handoff_seq`.
  `AW-SRV-027`'s rejection of a new handoff by a faulted Zone also sets the mark, so a retry that follows is
  stale and a restore at home can't be undone by one. The mark is kept for good, so a retry produced by a
  restarted source after the original ack landed is still stale: nothing here relies on how records on
  different Partitions or from different process lives are ordered. The cost is one entry per Entity that has
  been decided in a Zone by handoff, hashed with the Zone and copied by every in-tick snapshot; at the sizing
  fixture's 500 Characters that is nothing, and it grows with the Entities that cross Zones.
  `andara_handoff_placed_entries` shows it, and the runbook sets a threshold past which pruning is wanted.
  Pruning by proof is deferred (below).
- **Implicit ack.** An `Arrive(e, s)` into a Zone that still holds `e` in its own `Transit` with a lower
  `handoff_seq` proves the target placed `e` and moved it on. The Zone drops that transit record and then
  decides the arrival by the rule above.
- **Retry with backoff, held in memory.** Each tick, after applying records, the source re-produces
  `Arrive` for every transit record whose next attempt is due, in Entity-ID order. The interval after the
  n-th attempt is `min(sim.handoff_retry_ticks × 2^min(n-1, 16), sim.handoff_retry_max_ticks)`: the exponent
  saturates, so a long-stuck handoff never wraps to a zero interval. The engine holds each record's attempt
  count and last attempt in memory, not in the hash, so replay under a retuned config produces the same
  state, and **a record found after a recovery is due on the first live tick** (replay discards outbound
  Commands anyway, so this is also what heals a handoff after a restart). **No retry is produced while the
  source Zone is faulted or its Partition is frozen** (`AW-SRV-002`): the ack couldn't be applied.
- **Every Command for an Entity in transit is rejected `in_transit`**, after logging (it parsed and was
  authorized): the verbs, `UnbindCharacter` and `MarkLinkdead`. **`BindCharacter`** for a Character whose
  Entity is in any Zone's `Transit` is rejected `in_transit` too, and creates no body. Its search is unchanged
  first, every Zone's `Entities`, and then every Zone's `Transit`: for the few ticks between the target
  placing the Entity and the source applying the ack the Entity is in both, and the Bind finds it in
  `Entities`, where it is. It keeps the single-process assumption the existing cross-Zone search has, which
  the sharding ADR lifts for both.
  **The first retry comes inside the Gateway's hold.** `ReleaseSession` already waits for a crossing to
  settle, bounded by `ingress.transit_hold` (default 2 s), so `sim.handoff_retry_ticks` must give a first
  retry that lands inside it: default 10 ticks (1 s), validated at startup as less than the hold. A lost
  `Arrive` then heals before the hold runs out.
  **What outlasts the hold, and what it does to a Session**, from the roster's code (`roster.go`):
  - A teardown produced after the hold, `UnbindCharacter` or `MarkLinkdead`, is rejected `in_transit`
    post-log, so the produce returned nil. For a linkdead end the roster then holds the Character's live
    flag waiting for a `LinkdeadEnded` that never comes, because no body is linkdead, so the Account's
    other Characters are `already_live` until the same Character is selected again.
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
- A swapped-in Zone (`content.go`'s `&ZoneState{...}`) initialises `Transit` and `Placed`.

### Out of scope
- Holding or queueing a player's Commands during transit: presentation (`AW-SRV-011`, `AW-SRV-010`).
  This story rejects `in_transit`; a Gateway that masks it does so on top.
- **`HandoffRejected` and the target answering `zone_faulted`: `AW-SRV-027`.** Under today's freeze
  (`AW-SRV-002`) a faulted target never answers, so an Entity in transit to it stays safely in the
  source's `Transit`, retrying on its backoff. `AW-SRV-027` adds the rejection and the restore at home
  (`log.v1` field 14 is held for it). **There is no operator action to release a stuck Entity before
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
   acks, and A's transit record is gone within one tick of the ack.
2. **Given** the source process killed after publishing tick `T`'s boundary and before the
   `Arrive` is acknowledged **when** it recovers **then** `Transit` still holds the Character at the
   recovered hash, the first live tick retries, and the Character arrives in B exactly once.
3. **Given** a retried `Arrive` delivered twice **when** B applies the second **then** it re-acks, emits no
   Event, and B's `ZoneCanonicalBytes` and `Placed` are unchanged by it.
4. **Given** the Character moved A→B→C and a retry of `Arrive(seq k)` reaches B after it left with
   `seq k+1` **when** B applies it **then** it is stale: nothing is placed and it is acked. **And given** that
   retry reaches B after a thousand ticks more than any retry window, or from a source process that
   restarted after B's first ack **then** the outcome is the same, because B's mark for the Entity is
   `k` and nothing prunes it.
5. **Given** the Character moved A→B→C→B (seq `k`, `k+1`, `k+2`) **when** a retry of `Arrive(seq k)` reaches
   B while it holds the Entity at `k+2` **then** it is stale. **And given** two Entities, X decided in B at
   seq 5 and Y arriving at B at seq 1 for the first time **then** Y is placed: a mark is per Entity, not per
   Zone.
6. **Given** the same records replayed from boundaries **when** the exchange is replayed **then** every hash
   matches. **And given** the node recovers with a different `sim.handoff_retry_ticks` or
   `sim.handoff_retry_max_ticks` **then** the hashes still match, since the schedule isn't in the hash, and
   the first live tick retries every record in `Transit`.
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
   rejected `entity_present`; **and given** `s == 0`, or an `Arrive.handoff_seq` that differs from its
   `entity.handoff_seq` **then** it is rejected `invalid_arrival`; both place nothing.
10. **Given** `EntityState` gains a field in a later story **when** the round-trip test runs without the
    proto gaining it **then** the test fails naming the field.
11. **Given** a Character in transit **when** a `BindCharacter` for it applies, at the source **then** it is
    rejected `in_transit` and no body is created. **And given** the target has placed the Entity but the
    source hasn't applied the ack **then** the Bind finds it in `Entities`, where it is. Across the whole
    exchange no Zone pair holds two `Entities` with one ID, asserted by a scan, and the `Transit` and
    `Entities` copies coexist only until the ack is applied.
12. **Given** an `Arrive(e, s)` at a Zone that holds `e` in its own `Transit` with a lower seq **when**
    applied **then** the transit record is dropped and the arrival is decided by the dedup rule.
13. **Given** a Zone with `Transit` records and `Placed` marks **when** it is snapshotted and restored **then**
    `HashZone` is identical, and `BodyStateHash` refuses a body with an unsorted or duplicated `transit` or
    `placed`, a `placed` mark of 0, or a `transit` Entity that is dormant or linkdead.
14. **Given** A holds `Transit(e, 3)` after A→B→C→B **when** a late `HandoffAck(e, 1)` applies **then** the
    record stays, and **when** `HandoffAck(e, 3)` applies **then** it is dropped.
15. **Given** a `Goto` into another Zone **when** it applies **then** the Entity goes through the same
    `Transit`, `Arrive` and `HandoffAck` path as a `Move`, with an empty `Direction`.
16. **Given** a `BindCharacter` that would create a body for an ID some Zone holds a mark for **then** it is
    refused `id_reused` and nothing is created.

## Interface contract

The wire and snapshot shapes are in the protos, pinned by architecture:
`docs/specs/protocol/andara/log/v1/log.proto` (`Arrive.handoff_seq = 6`, `Entity.handoff_seq = 6`,
`HandoffAck` at `LoggedCommand` 13, 14 held for `AW-SRV-027`) and
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
// Engine, not hashed: attempts[EntityID] and lastAttempt[EntityID], reset at recovery.
// Step, after applying records: produce the retries that are due (Entity-ID order, skipped while the
// Zone is faulted or its Partition is frozen).
```

**Canonical bytes.** Each hashed record is written only when present, as `entity_dormant` and
`entity_linkdead` are: a `transit` and a `placed` record per entry, and `entity_handoff` only for a non-zero
`handoff_seq`. The tag names are implementation's, and the round-trip and hash tests pin them.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `sim.handoff_retry_ticks` | `ANDARA_HANDOFF_RETRY_TICKS` | `10` | 1 s at 10 Hz; the first retry interval. Greater than 0, at least the broker round trip in ticks, and **less than `ingress.transit_hold`** so a lost `Arrive` heals inside the Gateway's hold; validated at startup |
| `sim.handoff_retry_max_ticks` | `ANDARA_HANDOFF_RETRY_MAX_TICKS` | `600` | 1 minute at 10 Hz; the longest gap between attempts. At least `sim.handoff_retry_ticks`; validated at startup |

### Error taxonomy

`in_transit` (validate; the actor is between Zones, and a `BindCharacter` for a Character whose Entity is),
`actor_linkdead` (validate; a `Move` or `Goto` by a linkdead body), `stale_arrival` (consumed, acked, nothing
placed, no Event), `entity_present`, `invalid_arrival` and `id_reused` (validate; nothing placed).
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
`make world-reset ENV=dev` with the deploy that carries this story, and a fresh local stack for the planned
`AW-INF-032`. The PR says so, and says whether the golden fixture log contains a cross-Zone move.
`andara.commands.v1` gains one record kind (13), tick-produced.

## Observability requirements

- **Metrics:** `andara_handoffs_in_transit` (gauge, no labels); `andara_handoff_retries_total`
  (counter); `andara_handoff_stale_arrivals_total` (counter); `andara_handoff_placed_entries` (gauge, no
  labels: marks summed over Zones). No Entity or Zone labels. *(`andara_handoff_rejected_total` moves to
  `AW-SRV-027` with the rejection.)*
- **Logs:** `warn` per retry with `entity_id`, `from_zone`, `to_zone`, `seq`, `attempt`; `error` on
  `entity_present`, `invalid_arrival` and `id_reused`. `trace_id` from the originating `Move` for a live
  retry; a retry after a recovery starts a new trace and carries `entity_id`.
- **Traces:** the `Arrive`, `HandoffAck` and retries carry the original `Move`'s traceparent where it is
  known, so a handoff is one trace from keystroke to ack.
- **Alerts:** none. `andara_handoffs_in_transit` sustained above 0 is a dashboard panel and a diagnostic
  step in `docs/runbooks/simulation-lagging.md` (a stuck handoff means the broker or the target Partition
  is). This story adds that step, and a line that an Entity stuck in transit to a faulted Zone has no
  release before `AW-SRV-027`, and a threshold on `andara_handoff_placed_entries` past which pruning is wanted.

## Test plan

- **Unit:** AC-1, 3 to 9 and 11 to 16 on the stepped clock with an injectable producer; AC-4 with a retry
  delivered after a thousand ticks and from a source that restarted; AC-5's A→B→C→B and its two-Entity case;
  AC-6 recovering under a changed config; the backoff schedule exactly, at attempt counts of 64 and
  beyond; the round-trip test (AC-10); the snapshot round trip (AC-13).
- **Integration (`make test-integration`):** AC-2 with `SIGKILL` between boundary and ack, on the broker.
- **Manual/operator:** `andara-cli sim repl` with two Zones and a scripted producer failure: expect
  "you are between places" for `look`, then arrival after the retry.

Mutation checks to record: dropping the mark check makes AC-4 fail; treating a mark as removable by time
makes AC-4's second half fail; a per-Zone scalar in place of the per-Entity mark makes AC-5's two-Entity
case fail; matching an ack by Entity alone makes AC-14 fail; letting a Bind search only `Entities`, or only
`Transit`, makes AC-11 fail; producing retries while the source is faulted makes AC-7 fail; dropping the
implicit ack makes AC-12 fail; letting the exponent overflow makes AC-7's 64-attempt case fail; hashing the
schedule makes AC-6's changed-config case fail; routing `Goto` around the handshake makes AC-15 fail; dropping
the `id_reused` guard makes AC-16 fail.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-003`'s record notes that `AW-SRV-012` replaced the one-hop bounce (it still
says this story retires it); the PR states the `make world-reset ENV=dev` consequence and whether the
golden fixture contains a cross-Zone move; `docs/runbooks/simulation-lagging.md` has the handoff step.

## Open questions

- `[ASSUMPTION]` `handoff_retry_ticks` = 10 and `handoff_retry_max_ticks` = 600. A retry is cheap and
  idempotent; too short doubles broker traffic under a slow broker, too long is a player stuck "between
  places". Tune on the stack, inside the `ingress.transit_hold` constraint.
- **Resolved 2026-09-18 (Brian):** the delay stays perceptible in the Events and the Gateway holds
  a Session's Commands during transit (`AW-SRV-010`). The sim still rejects `in_transit`; the hold
  is what keeps a well-behaved Gateway from ever reaching that path.
- **Resolved 2026-10-04 (architecture):** the `Departures` window is gone, replaced by marks that depend on
  no time and no ordering.
