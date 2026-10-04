---
id: AW-SRV-028
title: Durable cross-Zone handoff — in-transit state, acknowledgement, and tick-driven retry
epic: EPIC-02
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-003]
blocks: [AW-SRV-007, AW-SRV-047]
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
`docs/feedback/AW-SRV-028-handoff-contract.md`. The rulings are in that file, "Architecture: the
rulings". What changed: `Departures` and its time window are replaced by open arrivals and a third
record, `HandoffClosed`; `HandoffRejected` and its AC move to `AW-SRV-027`; retries back off and skip a
faulted source; Bind, Unbind and MarkLinkdead are specified for an Entity in transit; `Goto` is in scope.*

### In scope
- **Transit.** `ZoneState.Transit`: Entities that have left this Zone and are not yet acknowledged,
  keyed by Entity ID, hashed, sorted. An Entity in transit is not in `Entities`. Each record holds the
  Entity as it will arrive (with its `handoff_seq` already incremented), the target Zone and Room, the
  Direction (empty for a `Goto`), the last attempt's Tick, the attempt count, and at most one pending
  Command (below).
- **Every cross-Zone departure goes through the handshake**: a `Move` and a `Goto` (`AW-SRV-036`) alike.
- **Arrive and ack.** `Arrive` carries `handoff_seq` (per-Entity, monotonic, stored on the Entity,
  incremented by the source when it leaves). The target places the Entity, opens the handoff in its
  open arrivals, and produces `HandoffAck{entity_id, handoff_seq}` to the source Zone's Partition
  (`Arrive.origin_zone_id` names it). The source drops its transit record when it applies the ack, and
  then produces `HandoffClosed{entity_id, handoff_seq}` to the target's Partition.
- **Open arrivals (the dedup rule, which doesn't depend on time).** `ZoneState.Arrivals`: for each
  Entity, the `handoff_seq`s of handoffs into this Zone whose `HandoffClosed` has not been applied,
  hashed, sorted. An `Arrive(e, s)` is decided like this:
  - `s` is in `e`'s open arrivals and the Zone holds `e` with stored `handoff_seq == s`: a retry of a
    handoff already placed. Re-ack, change nothing.
  - `s` is in `e`'s open arrivals and the Zone does not hold `e` (or holds it with a different seq):
    stale, a retry that overtook the Entity's next move. Ack it, place nothing, count it.
  - `s` is not in `e`'s open arrivals: a new handoff. Place it, add `s` to the open arrivals, ack.
  - A new handoff for an `e` the Zone already holds is an invariant violation: the Command is rejected
    `entity_present` and an `error` is logged. It can't happen with sequences that only grow.
  `HandoffClosed(e, s)` removes `s` from `e`'s open arrivals (and the entry once it is empty). Nothing
  prunes by time: an entry is removed by proof or it stays. **The proof relies on one ordering:**
  `HandoffClosed` is produced after every `Arrive` the source produced for that handoff, to the same
  Partition by the same producer, so the target applies every retry first. `AW-INF-005`'s client
  contract (idempotent producer, `max.in.flight.requests.per.connection ≤ 5`, one producer per process)
  gives per-Partition order, and a record the client reports as failed is never appended later. This
  story adds the second clause to that contract's list of claims.
- **Implicit ack.** An `Arrive(e, s)` into a Zone that still holds `e` in its own `Transit` with a lower
  `handoff_seq` proves the target placed `e` and moved it on. The Zone drops that transit record,
  produces `HandoffClosed` for it, and then places the arrival.
- **Retry with backoff.** Each tick, after applying records, the source re-produces `Arrive` for every
  transit record whose next attempt is due, in Entity-ID order. The interval after the n-th attempt is
  `min(sim.handoff_retry_ticks × 2^(n-1), sim.handoff_retry_max_ticks)`. It is in ticks, held in the
  hashed record, so replay re-derives every retry. **No retry is produced while the source Zone is
  faulted or its Partition is frozen** (`AW-SRV-002`): the ack couldn't be applied, so a retry would
  only draw duplicate acks. The record keeps its due time and retries on the first tick after the
  Zone can apply again.
- **Verbs in transit.** A Command whose actor is in `Transit` is rejected `in_transit` after logging
  (it parsed and was authorized). `BindCharacter` for a Character whose Entity is in any Zone's `Transit`
  is rejected `in_transit`, and creates no body. It uses the same cross-Zone lookup `BindCharacter`
  already uses, so it carries that lookup's single-process assumption, and the sharding ADR owns lifting
  it for both. A rejected Bind is the Gateway's existing path for a rejected Bind: nothing is bound and
  the client selects again.
- **Unbind and MarkLinkdead in transit are deferred on the record.** `UnbindCharacter` or `MarkLinkdead`
  for an Entity in `Transit` stores the Command's parameters in the record's `pending` slot and changes
  nothing else. When the handoff resolves, the Command is carried out:
  - on the ack (or implicit ack): the source produces the equivalent Command to the target's Partition,
    after the `HandoffClosed`, where the Entity now is;
  - on a rejection (`AW-SRV-027`'s): the source applies it at home after restoring the Entity.
  `UnbindCharacter` outranks `MarkLinkdead`: if both arrive, `UNBIND` is kept, and a `MarkLinkdead`
  after an `UNBIND` is ignored. Without this a Session that dropped mid-handoff would leave a body
  standing in the target with no linkdead timer.
- **`Replay` keeps discarding outbound Commands**: the retries, the `HandoffClosed` and the deferred
  Command are re-derived by the live loop from the recovered `Transit` and `Arrivals`, which is the
  point.
- A round-trip test `EntityState → log.v1.Entity → EntityState` over every field, so a field added to
  one and not the other fails `make test` rather than hashing differently after a boundary.

### Out of scope
- Holding or queueing a player's Commands during transit: presentation (`AW-SRV-011`, `AW-SRV-010`).
  This story rejects `in_transit`; a Gateway that masks it does so on top.
- **`HandoffRejected` and the target answering `zone_faulted`: `AW-SRV-027`.** Under today's freeze
  (`AW-SRV-002`) a faulted target never answers, so an Entity in transit to it stays safely in the
  source's `Transit`, retrying on its backoff, until the fault is dealt with. `AW-SRV-027` adds the
  rejection and the restore at home (`log.v1` field 14 is held for it) and `AW-SRV-028` doesn't wait on it.
- Relocation to `fallback_room`: `AW-SRV-012` has it, and `applyArrive` already places an arrival whose
  Room is gone in the fallback Room with `EntityRelocated{room_removed}`. A rejection never carries
  `unknown_room`.
- Whether a restart clears a Zone's fault. The repo is inconsistent (`server/sim/state.go` and
  `AW-SRV-027` say "until restart" while the snapshot carries `Faulted` and nothing resets it on
  recovery), and it is `AW-SRV-027`'s to settle. Until it does, an Entity in transit to or from a
  faulted Zone stays stuck across restarts.
- Multi-process ownership: the protocol is correct across processes because every step is a logged
  Command. The `BindCharacter` lookup above is the one thing here that isn't.

## Acceptance criteria

1. **Given** a Character moving A→B **when** the `Arrive` is never delivered (producer failure
   injected) **then** the Character stays in A's `Transit`, `in_transit` answers its Commands, and
   after `sim.handoff_retry_ticks` ticks the `Arrive` is produced again; on delivery B places it,
   acks, and A's transit record is gone within one tick of the ack.
2. **Given** the source process killed after publishing tick `T`'s boundary and before the
   `Arrive` is acknowledged **when** it recovers **then** `Transit` still holds the Character at the
   recovered hash, the live loop retries, and the Character arrives in B exactly once.
3. **Given** a retried `Arrive` delivered twice **when** B applies the second **then** it re-acks and
   B's State Hash is unchanged by it.
4. **Given** the Character moved A→B→C and a retry of `Arrive(seq k)` reaches B after it left with
   `seq k+1` **when** B applies it **then** it is `stale_arrival`, nothing is placed, and it is acked.
   **And given** that retry reaches B after a thousand more ticks than any retry window **then** the
   outcome is the same, because B's open arrival for `k` is removed only by `HandoffClosed(e, k)`.
5. **Given** the same records replayed from boundaries **when** the exchange is replayed **then**
   every hash matches, including ticks on which a retry or a `HandoffClosed` was produced.
6. **Given** a source Zone that is faulted, or whose Partition is frozen **when** ticks pass **then** no
   `Arrive` is produced for its transit records, and **when** it can apply again **then** the first tick
   retries. **And given** an unanswered handoff **then** the gaps between attempts double from
   `sim.handoff_retry_ticks` up to `sim.handoff_retry_max_ticks` and stay there, asserted by the
   produced ticks.
7. **Given** a Character in transit **when** its Session submits `look` **then** the Command is
   logged (it parsed and was authorized) and rejected post-log with `in_transit`; nothing is applied.
8. **Given** an `Arrive` for an Entity the Zone holds with a different `handoff_seq`, with the arrival's
   `s` in its open arrivals **when** applied **then** it is stale (AC-4). **And given** a new handoff
   for an Entity the Zone holds **then** it is rejected `entity_present`.
9. **Given** `EntityState` gains a field in a later story **when** the round-trip test runs
   without the proto gaining it **then** the test fails naming the field.
10. **Given** a Character in transit **when** a `BindCharacter` for it applies, at the source or the
    target **then** it is rejected `in_transit`, no body is created, and **when** the handoff has
    resolved **then** a Bind finds the Entity where it is. Across the whole exchange the World never
    holds two Entities with one ID, asserted by hash and by a scan of every Zone.
11. **Given** a Character in transit **when** an `UnbindCharacter` applies for it **then** the Entity
    arrives in B and is then unbound (dormant) exactly as if the Unbind had applied there; **and given**
    a `MarkLinkdead` instead **then** it arrives and is then linkdead with the carried tick counts;
    **and given** both **then** the Unbind wins.
12. **Given** an `Arrive(e, s)` at a Zone that holds `e` in its own `Transit` with a lower seq **when**
    applied **then** the transit record is dropped, a `HandoffClosed` is produced for it, and the
    arrival is placed.
13. **Given** a `HandoffClosed` that is never delivered **then** B's open arrival stays, nothing is
    ever duplicated or lost for it, and `andara_handoff_arrivals_open` reads non-zero.

## Interface contract

The wire and snapshot shapes are in the protos, pinned by architecture:
`docs/specs/protocol/andara/log/v1/log.proto` (`Arrive.handoff_seq = 6`, `Entity.handoff_seq = 6`,
`HandoffAck` and `HandoffClosed` in the `LoggedCommand` oneof at 13 and 20, 14 held for `AW-SRV-027`)
and `docs/specs/protocol/andara/state/v1/zone_state.proto` (`ZoneState.transit = 9`, `arrivals = 10`,
`EntityState.handoff_seq = 13`, and `TransitRecord`, `TransitPending`, `OpenArrivals`). A transit record
embeds `state.v1.EntityState`, the hashed shape, and `entity.room_id` is the origin Room. Neither new
record is a verb: the table refuses to bind them, as it refuses `Arrive`.

```go
// CONTRACT SKETCH — server/sim, not an implementation
type TransitRecord struct {
    Entity      EntityState // as it will arrive; Entity.HandoffSeq is the handoff's sequence
    To          ZoneID
    Room        RoomID      // the Room in To
    Direction   Direction   // empty for a Goto
    LastAttempt Tick
    Attempts    uint32
    Pending     TransitPending
}
type ZoneState struct { /* … */ Transit map[EntityID]TransitRecord; Arrivals map[EntityID][]uint64 }
// Step, after applying records: produce the retries that are due (Entity-ID order, skipped while the
// Zone is faulted or its Partition is frozen). Both maps are hashed in sorted order.
```

**Canonical bytes.** Each hashed record is written only when present, as `entity_dormant` and
`entity_linkdead` are, so a World with nothing in transit and no open arrival hashes as it did before:
`transit` and `arrival` records per entry, and `entity_handoff` only for a non-zero `handoff_seq`.
The tag names are implementation's, and the round-trip and hash tests pin them.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `sim.handoff_retry_ticks` | `ANDARA_HANDOFF_RETRY_TICKS` | `20` | 2 s at 10 Hz; the first retry interval. Must exceed the broker round trip in ticks |
| `sim.handoff_retry_max_ticks` | `ANDARA_HANDOFF_RETRY_MAX_TICKS` | `600` | 1 minute at 10 Hz; the longest gap between attempts. At least `sim.handoff_retry_ticks` |

### Error taxonomy

`in_transit` (validate; the actor is between Zones, and a `BindCharacter` for a Character whose Entity
is), `stale_arrival` (consumed, acked, nothing placed), `entity_present` (validate; a new handoff for an
Entity the Zone already holds). `HandoffRejected` and its codes are `AW-SRV-027`'s.

## Data / state impact

`Transit`, `Arrivals` and `EntityState.HandoffSeq` are hashed Zone state, carried by the snapshot body
(`ZoneState.transit`, `arrivals`, `EntityState.handoff_seq`) so a restore reproduces the hash. **No
`state_version` bump:** the fields are additive, and each contributes nothing to the hash when empty or
zero, so existing rounds and every `TickCompleted` on the log are what they were. The golden sequence
changes only if its fixture log contains a cross-Zone move (which now leaves something in `Transit`), and
the PR says which. `andara.commands.v1` gains two record kinds (13, 20), both tick-produced.

Open arrivals grow by one entry per handoff whose `HandoffClosed` is lost, including one lost to a
crash between applying the ack and delivering the record. Each entry is an Entity ID and a sequence.
`andara_handoff_arrivals_open` shows it, and nothing but a delivered `HandoffClosed` removes one.

## Observability requirements

- **Metrics:** `andara_handoffs_in_transit` (gauge, no labels); `andara_handoff_retries_total`
  (counter); `andara_handoff_stale_arrivals_total` (counter); `andara_handoff_arrivals_open` (gauge, no
  labels: handoffs whose `HandoffClosed` hasn't been applied, summed over Zones). No Entity or Zone
  labels. *(`andara_handoff_rejected_total` moves to `AW-SRV-027` with the rejection.)*
- **Logs:** `warn` per retry with `entity_id`, `from_zone`, `to_zone`, `seq`, `attempt`; `error` on
  `entity_present`. `trace_id` from the originating `Move` for a live retry; a retry after a recovery
  starts a new trace and carries `entity_id`.
- **Traces:** the `Arrive`, `HandoffAck`, `HandoffClosed` and retries carry the original `Move`'s
  traceparent where it is known, so a handoff is one trace from keystroke to ack.
- **Alerts:** none. `andara_handoffs_in_transit` sustained above 0 is a dashboard panel and a
  diagnostic step in `docs/runbooks/simulation-lagging.md` (a stuck handoff means the broker or the
  target Partition is), and so is `andara_handoff_arrivals_open` rising (Closed records are being lost).

## Test plan

- **Unit:** AC-1, 3, 4, 5, 6, 7, 8, 10, 11, 12 and 13 on the stepped clock with an injectable producer;
  AC-4's second half with a retry delivered after a thousand ticks; the backoff schedule exactly; the
  round-trip test (AC-9).
- **Integration (`make test-integration`):** AC-2 with `SIGKILL` between boundary and ack, on the broker.
- **Manual/operator:** `andara-cli sim repl` with two Zones and a scripted producer failure: expect
  "you are between places" for `look`, then arrival after the retry.

Mutation checks to record: dropping the open-arrival check makes AC-4 fail; pruning an open arrival
by time makes AC-4's second half fail; producing `HandoffClosed` before the ack is applied makes AC-4
fail; letting a Bind search only `Entities` makes AC-10 fail.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-003`'s record notes that `AW-SRV-012` replaced the one-hop bounce (it still
says this story retires it); `AW-SRV-007`'s recovery test includes a handoff in flight; the second claim
(a failed record is never appended later) is in `AW-INF-005`'s client contract.

## Open questions

- `[ASSUMPTION]` `handoff_retry_ticks` = 20 and `handoff_retry_max_ticks` = 600. A retry is cheap and
  idempotent; too short doubles broker traffic under a slow broker, too long is a player stuck "between
  places". Tune on the stack.
- **Resolved 2026-09-18 (Brian):** the delay stays perceptible in the Events and the Gateway holds
  a Session's Commands during transit (`AW-SRV-010`). The sim still rejects `in_transit`; the hold
  is what keeps a well-behaved Gateway from ever reaching that path.
- **Resolved 2026-10-04 (architecture):** the `Departures` window is gone, replaced by open arrivals and
  `HandoffClosed`, so no dedup rule depends on time.
