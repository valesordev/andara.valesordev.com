---
id: AW-SRV-028
title: Durable cross-Zone handoff — in-transit state, acknowledgement, and tick-driven retry
epic: EPIC-02
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-003]
blocks: [AW-SRV-007]
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

### In scope
- `ZoneState.Transit`: Entities that have left this Zone and are not yet acknowledged, keyed by
  Entity ID, hashed, sorted. An Entity in transit is not in `Entities`; a Command naming it is
  rejected `in_transit`.
- `Arrive` carries `handoff_seq` (per-Entity monotonic, stored on the Entity, incremented per
  handoff). The target places the Entity and produces `HandoffAck{entity_id, handoff_seq}` to the
  source Zone's Partition. Receiving an `Arrive` for an Entity it already holds with the same
  `handoff_seq` re-acks and changes nothing.
- `ZoneState.Departures`: Entity → `{handoff_seq, tick}` recorded when an Entity leaves; an `Arrive`
  whose `handoff_seq` ≤ the recorded one is `stale_arrival` — the copy from a retry that overtook
  the Entity's next move — and is acked without placing anything. Pruned at each tick boundary
  after `2 × sim.handoff_retry_ticks + 1` ticks, so the map is bounded by departures inside that
  window and the prune is deterministic.
- Retry: each tick, the source re-produces `Arrive` for every transit record whose last attempt is
  `sim.handoff_retry_ticks` or more ticks old, in Entity-ID order.
- Rejection on the target (Room gone and no `fallback_room` yet — `AW-SRV-012` adds it): produce
  `HandoffRejected{entity_id, handoff_seq, code}` to the source; the source restores the Entity to
  its origin Room and emits `CharacterArrived{from_direction: reverse}`. This replaces `AW-SRV-003`'s
  one-hop bounce, which carried the Entity on a second unacknowledged produce.
- `Replay` keeps discarding outbound Commands: retries are re-derived by the live loop from the
  recovered `Transit` state, which is the whole point.
- A round-trip test `EntityState → log.v1.Entity → EntityState` over every field, so a field added
  to one and not the other fails `make test` rather than hashing differently after a boundary.

### Out of scope
- Holding or queueing a player's Commands during transit — that is presentation (`AW-SRV-011`) and
  waits on Brian's call on the perceptible delay (`AW-SRV-003` open question). This story rejects
  `in_transit`; a Gateway that masks it does so on top.
- Relocation to `fallback_room` — `AW-SRV-012`, which turns most `HandoffRejected` cases into a
  placement instead.
- Multi-process ownership — the protocol is already correct across processes because every step
  is a logged Command; nothing here assumes one process.

## Acceptance criteria

1. **Given** a Character moving A→B **when** the `Arrive` is never delivered (producer failure
   injected) **then** the Character stays in A's `Transit`, `in_transit` answers its Commands, and
   after `sim.handoff_retry_ticks` ticks the `Arrive` is produced again; on delivery B places it,
   acks, and A's transit record is gone within one tick of the ack.
2. **Given** the source process killed after publishing tick `T`'s boundary and before the
   `Arrive` is acknowledged **when** it recovers **then** `Transit` still holds the Character at the
   recovered hash, the live loop retries, and the Character arrives in B exactly once.
3. **Given** a retried `Arrive` delivered twice **when** B applies the second **then** it re-acks
   and B's State Hash is unchanged by it.
4. **Given** the Character moved A→B→C and a stale `Arrive(seq k)` reaches B after it left with
   `seq k+1` **when** B applies it **then** `stale_arrival`, nothing is placed, and B's `Departures`
   entry is pruned `2 × retry_ticks + 1` ticks after the departure, asserted by hash.
5. **Given** the same records replayed from boundaries **when** the exchange is replayed **then**
   every hash matches, including ticks on which a retry was produced.
6. **Given** B's target Room removed and no `fallback_room` **when** the `Arrive` is applied
   **then** `HandoffRejected` reaches A within one hop, A restores the Character to the origin Room
   with `CharacterArrived`, and `andara_handoff_rejected_total{code="unknown_room"}` increments.
7. **Given** a Character in transit **when** its Session submits `look` **then** the Command is
   logged (it parsed and was authorized) and rejected post-log with `in_transit`; nothing is applied.
8. **Given** an `Arrive` with a `handoff_seq` lower than the Entity's own **when** applied **then**
   it is treated as stale (AC-4) — the Entity's stored `handoff_seq` is the source of truth when it
   is present.
9. **Given** `EntityState` gains a field in a later story **when** the round-trip test runs
   without the proto gaining it **then** the test fails naming the field.

## Interface contract

```protobuf
// CONTRACT SKETCH — additions to andara/log/v1/log.proto
message Arrive { /* existing */ uint64 handoff_seq = 6; }
message Entity { /* existing */ uint64 handoff_seq = 5; }
message HandoffAck      { string entity_id = 1; uint64 handoff_seq = 2; }
message HandoffRejected { string entity_id = 1; uint64 handoff_seq = 2; string code = 3; }
// LoggedCommand oneof: HandoffAck handoff_ack = 13; HandoffRejected handoff_rejected = 14;
// Neither is a verb; the table refuses to bind them, as it refuses Arrive.
```

```go
// CONTRACT SKETCH — server/sim
type TransitRecord struct { Entity EntityState; To ZoneID; Room RoomID; Direction Direction; Seq uint64; LastAttempt Tick }
type ZoneState struct { /* … */ Transit map[EntityID]TransitRecord; Departures map[EntityID]Departure }
// Step, after applying records: retry due transit records (sorted), prune Departures. Both hashed.
```

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `sim.handoff_retry_ticks` | `ANDARA_HANDOFF_RETRY_TICKS` | `20` | 2 s at 10 Hz; must exceed the broker round trip in ticks |

### Error taxonomy

`in_transit` (validate; the actor is between Zones), `stale_arrival` (validate; consumed, acked,
nothing placed), `HandoffRejected.code` ∈ {`unknown_room`, `zone_faulted`}. `AW-SRV-003`'s
`unknown_room` on `Arrive` becomes the rejection's code rather than a player-facing rejection.

## Data / state impact

`Transit`, `Departures`, and `EntityState.HandoffSeq` are hashed Zone state. There is no
`state_version` in effect yet (`AW-SRV-022` correction), so no bump; the golden sequence changes
only if the fixture log contains a cross-Zone move, and the PR says which. `log.proto` changes are
additive; `andara.commands.v1` gains two record kinds, both tick-produced.

## Observability requirements

- **Metrics:** `andara_handoffs_in_transit` (gauge, no labels); `andara_handoff_retries_total`
  (counter); `andara_handoff_rejected_total{code}` (bounded by the code set);
  `andara_handoff_stale_arrivals_total` (counter). No Entity or Zone labels.
- **Logs:** `warn` per retry with `entity_id`, `from_zone`, `to_zone`, `seq`, `attempt`; `info`
  on rejection-restore. `trace_id` from the originating `Move`.
- **Traces:** the `Arrive`, `HandoffAck`, and retries carry the original `Move`'s traceparent, so a
  handoff is one trace from keystroke to ack.
- **Alerts:** none. `andara_handoffs_in_transit` sustained above 0 is a dashboard panel and a
  diagnostic step in `docs/runbooks/simulation-lagging.md` (a stuck handoff means the broker or
  the target Partition is), not a cause alert.

## Test plan

- **Unit:** AC-1, 3, 4, 5, 7, 8 on the stepped clock with an injectable producer; the prune
  boundary exactly; the round-trip test (AC-9).
- **Integration (`make test-integration`):** AC-2 with `SIGKILL` between boundary and ack, on the
  broker; AC-6.
- **Manual/operator:** `andara-cli sim repl` with two Zones and a scripted producer failure: expect
  "you are between places" for `look`, then arrival after the retry.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-003`'s one-hop bounce is removed and its story records the
replacement; `AW-SRV-007`'s recovery test includes a handoff in flight.

## Open questions

- `[ASSUMPTION]` `handoff_retry_ticks` = 20. A retry is cheap and idempotent; too short doubles
  broker traffic under a slow broker, too long is a player stuck "between places". Tune on the
  stack.
- **Resolved 2026-09-18 (Brian):** the delay stays perceptible in the Events and the Gateway holds
  a Session's Commands during transit (`AW-SRV-010`). The sim still rejects `in_transit`; the hold
  is what keeps a well-behaved Gateway from ever reaching that path.
- `[ASSUMPTION]` `Departures` window `2 × retry_ticks + 1`. A stale copy can only come from a retry
  issued before the ack landed, and the ack is one hop; the window covers two retries with slack.
