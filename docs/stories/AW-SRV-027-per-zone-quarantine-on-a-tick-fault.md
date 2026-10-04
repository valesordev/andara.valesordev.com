---
id: AW-SRV-027
title: Per-Zone quarantine on a tick fault
epic: EPIC-02
component: server
type: feature
status: ready
size: S
depends_on: [AW-SRV-002, AW-SRV-028]
blocks: []
lane: implementation
risk: medium
---

## Context

`AW-SRV-002` AC-12 contains a panic inside one Zone's tick by marking the Zone faulted and **freezing
its whole Partition**: offsets stop advancing, so every other Zone that hashes to the same Partition
waits too. With 64 Partitions and a handful of Zones that is rarely more than one Zone — but it is a
rule that gets worse as the World grows, and it was flagged for Brian's call.

Brian decided on 2026-09-18: **quarantine the Zone, not the Partition.** A faulted Zone's records are
consumed and rejected; the Partition keeps moving for everyone else. Determinism survives because the
fault is itself deterministic — the same record panics at the same point on replay, and so the same
records are rejected after it.

## User story

As a player in a healthy Zone, I want a crash in someone else's Zone to leave mine ticking, so that
one bad Behavior cannot take a region of the world down with it.

## Scope

### In scope
- `Engine.Step`: after a fault, records for the faulted Zone are consumed and rejected with
  `zone_faulted` (a `CommandRejected` to the actor), and the Partition's offset advances past them.
  **An `Arrive` is first decided by `AW-SRV-028`'s dedup rule** (see the amendment at the end): only an
  `Arrive` that would be a new handoff is rejected: a faulted Zone applies 028's cases 1, 2 and 3a as
  written, and the implicit ack of 3b still drops a transit record in a Zone that is also a source; only
  case 3c becomes `HandoffRejected`. A `HandoffAck` addressed to a faulted source Zone is
  consumed with no Event, and its transit record stays by design (the Zone's state is frozen), which is why
  `AW-SRV-028` produces no retry while its source is faulted.
  Records for other Zones on the same Partition are applied.
- `Step` no longer refuses input for a Partition with a faulted Zone; `Source.Poll` no longer takes
  a frozen-Partition list. `Engine.FaultedPartitions()` becomes `FaultedZones()`.
- `ZoneState.Faulted` stays in the State Hash; a replayed fault hashes identically, including the
  rejections that follow it.
- `ErrZoneFaulted` from `AW-SRV-002`'s taxonomy is retired from `Step` (it can no longer refuse a
  Step) and kept as the `RejectError` code a handler sees on a cross-Zone `Produce` into a faulted
  Zone: the produced Command is logged, consumed, and rejected on the target's tick like any other.

### Out of scope
- Un-faulting a Zone at runtime. **This story settles whether a fault survives a restart**, which the repo
  disagrees with itself on (the snapshot carries `Faulted` and nothing resets it on recovery) and which an
  Entity stuck in transit depends on (`AW-SRV-028`). A faulted Zone stays faulted until restart; `AW-SRV-012`'s content
  reload is where a fixed Zone could be re-armed, and that is its call.
- Crash-and-recover instead of quarantine — `AW-SRV-007` weighs it once exact recovery exists.

## Acceptance criteria

1. **Given** Zones A and B on one Partition and a handler that panics on A's record **when** the
   tick runs **then** A is faulted, `ZoneFaulted` is emitted, B's records on the same tick are
   applied, and the Partition's offset advances past every record in the input.
2. **Given** A faulted **when** later records for A arrive **then** each is consumed and its actor
   receives `CommandRejected{code: zone_faulted}`; `andara_tick_zone_faults_total{zone="A"}` counted
   the fault once, not per rejection.
3. **Given** the fault **when** the same records are replayed from boundaries **then** the State
   Hash sequence is identical, including the ticks after the fault.
4. **Given** a handler in Zone C that `Produce`s a Command into faulted Zone A **when** the produced
   Command is applied on A's Partition **then** it is rejected `zone_faulted`, and C's state is
   unchanged by the rejection, except that an `Arrive` is decided by the dedup rule first and a new
   handoff's rejection is `HandoffRejected` (ACs 6 to 9).
5. **Given** `AW-SRV-002`'s `TestStep_ZoneFaultIsContained` and `TestLoop_ZoneFault` **when** this
   story lands **then** they are rewritten to assert the new rule, not deleted; the golden hash
   sequence (`AC-1` of 002) is regenerated only if the fixture log contains a fault, and the PR says
   which.
6. **Given** a faulted B that already placed `Arrive(e, s)` **when** a retry of it applies **then** it is
   acked (or stale-acked), no `HandoffRejected` is produced, and the source drops its record.
7. **Given** a faulted B and a new handoff `Arrive(e, s)` **when** it applies **then** `HandoffRejected{e, s,
   zone_faulted}` is produced to the source Zone, B's mark for `e` is `s`, and the source restores `e` at its
   origin Room with `CharacterArrived{from_direction: reverse}`, keeping the incremented `handoff_seq`.
8. **Given** the rejected handoff of AC-7 **when** its `HandoffRejected` is lost and a retry of the `Arrive`
   applies, whether B is still faulted or healthy again **then** B produces the same `HandoffRejected`, not
   an ack, nothing is placed, and the source restores the Entity when it applies it. A replay of the
   exchange hashes identically, the mark and its outcome included.
10. **Given** a source that restored `e` at home after a rejection and departed again with `s+1`, so it holds
    `Transit(e, s+1)` **when** a late or duplicate `HandoffRejected(e, s)` applies **then** it is consumed with
    no Event, and `Transit(e, s+1)`, the body and the State Hash are unchanged. **And given** a
    `HandoffRejected` for an Entity in a faulted source's `Transit` **then** it is consumed and the record stays.
9. **Given** a `HandoffAck` for an Entity in a faulted source Zone's `Transit` **when** it applies **then** it
   is consumed with no Event and the record stays.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package sim
func (e *Engine) FaultedZones() []ZoneID          // replaces FaultedPartitions
// Step: no ErrZoneFaulted refusal; a faulted Zone's record → rejected("zone_faulted", …)

package tickloop
type Source interface { Poll(max int) ([]sim.Record, error); /* … unchanged … */ }  // frozen list dropped
```

`AW-SRV-002`'s configuration and error taxonomy are otherwise unchanged. `ErrZoneFaulted` remains the
code string `zone_faulted`.

## Data / state impact

`ZoneState.Faulted`/`FaultedTick` already hash. The rejections after a fault emit Events and consume
Event IDs, which are in the hash too — so a World that faulted and one that did not diverge in hash
exactly as they diverge in history, which is correct.

## Observability requirements

- **Metrics:** `andara_handoff_rejected_total{code}` (counter; `code` is `zone_faulted`, so no cardinality)
  for a new handoff a faulted Zone rejects. `andara_tick_zone_faults_total{zone}` unchanged (one per fault).
  `andara_tick_zone_faulted_rejections_total{zone}` — counter, cardinality Zones: how much a dead
  Zone is still being asked to do.
- **Logs:** `error` on the fault (exists); `warn` once per Zone per second at most while rejections
  continue — not per rejection.
- **Traces:** `sim.zone_tick` for a faulted Zone carries `faulted=true` and no child work.
- **Alerts:** none new. A faulted Zone is a cause; the symptom is players in it getting
  `zone_faulted`, and the Session availability SLO (`AW-SRV-011`) is where that surfaces.

## Test plan

- **Unit:** AC-1 through AC-10 on the stepped clock; the replay negative — a replay that re-applied
  instead of re-rejecting after the fault would hash differently.
- **Integration (`make test-integration`):** a fault on the broker, then `Recover` reproducing the
  same hash.
Mutation checks to record: acking a retry of a rejected handoff (or leaving `rejected` out of the hash) makes
AC-8 fail; matching a `HandoffRejected` by Entity alone makes AC-10 fail; rejecting a retry of a placed
handoff makes AC-6 fail.

- **Manual/operator:** none beyond `make check`; there is no verb that panics on purpose outside a
  test fixture.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-002`'s Zone-fault question points here as resolved.

- **Inherited from `AW-SRV-019` (2026-09-24):** the state projector replays through a faulted tick.
  Today it cannot, any more than recovery can: the panicking record stays unapplied, the boundary's
  offset excludes it, and a replay diverges at that tick. `server/projector`'s
  `TestFaultRendersTheZoneWhole` renders the live engine's fault tick directly for that reason. When
  this story lands, the same case also runs through `Projector.Replay` and verifies every boundary.

## Open questions

- **Resolved 2026-09-18 (Brian): quarantine the Zone, not the Partition.**
- `[ASSUMPTION]` A faulted Zone stays faulted until restart. Re-arming is `AW-SRV-012`'s reload, if
  it wants it; nothing here prevents it.

## HandoffRejected (architecture, 2026-10-04)

From `AW-SRV-028`'s contract review (`docs/feedback/AW-SRV-028-handoff-contract.md`). `AW-SRV-028`
builds the durable cross-Zone handoff and does **not** add a rejection: under today's freeze a faulted
target never answers, and the Entity waits safely in the source's `Transit`. This story's rule, that a
faulted Zone's records are consumed and rejected, is what lets the target answer, so the rejection is this
story's:
- **A faulted Zone decides an `Arrive` by `AW-SRV-028`'s dedup rule first**, from its frozen state: a retry
  of a handoff it already placed (`handoff_seq` at or below its mark for the Entity) is acked or stale-acked
  like any other, never rejected. Rejecting it would make the source restore the Entity at home while the
  frozen Zone still holds it, and a restart would bring it back: two bodies. Only an `Arrive` that would be a
  new handoff is rejected;
- a new handoff consumed and rejected for a faulted Zone **also sets the Zone's mark for the Entity to that
  sequence and records that the outcome was a rejection** (`PlacedArrival.rejected`), so a retry the source
  produced before it applied the rejection, or after a restart, **gets the same `HandoffRejected` again**,
  never an ack: an ack would make the source drop a record whose Entity was never placed and never
  restored, and the Entity would be lost. A retry can't place a second body once the Zone is healthy again
  either. The first rejection produces
  `HandoffRejected{entity_id, handoff_seq, code: "zone_faulted"}` to the source Zone's Partition
  (`andara.log.v1.LoggedCommand` field 14, `HandoffRejected`, pinned in `log.proto`), and `andara_handoff_rejected_total{code}` counts each `HandoffRejected` produced, reissues included;
- the source applies it **only when its `Transit[e].handoff_seq` equals the rejection's**, as it does an ack:
  with no record, or another sequence, the rejection is a duplicate and is consumed with no Event and no
  state change; a faulted source consumes it too and its record stays. When it matches, the source drops
  the transit record and restores the Entity to its origin Room
  (`TransitRecord.entity.room_id`; the Zone's fallback Room with `EntityRelocated{room_removed}` if that Room
  is gone), emitting `CharacterArrived{from_direction: reverse of the move's direction}`. The restored
  Entity keeps its incremented `handoff_seq`, so its next departure takes a sequence above the mark.
It needs an AC for each of those, including the retry-of-a-placed-handoff case, and the size may want
revisiting.
