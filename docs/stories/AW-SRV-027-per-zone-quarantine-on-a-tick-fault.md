---
id: AW-SRV-027
title: Per-Zone quarantine on a tick fault
epic: EPIC-02
component: server
type: feature
status: ready
size: S
depends_on: [AW-SRV-002]
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
  Records for other Zones on the same Partition are applied.
- `Step` no longer refuses input for a Partition with a faulted Zone; `Source.Poll` no longer takes
  a frozen-Partition list. `Engine.FaultedPartitions()` becomes `FaultedZones()`.
- `ZoneState.Faulted` stays in the State Hash; a replayed fault hashes identically, including the
  rejections that follow it.
- `ErrZoneFaulted` from `AW-SRV-002`'s taxonomy is retired from `Step` (it can no longer refuse a
  Step) and kept as the `RejectError` code a handler sees on a cross-Zone `Produce` into a faulted
  Zone: the produced Command is logged, consumed, and rejected on the target's tick like any other.

### Out of scope
- Un-faulting a Zone at runtime. A faulted Zone stays faulted until restart; `AW-SRV-012`'s content
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
   Command is applied on A's Partition **then** it is rejected `zone_faulted` and C's state is
   unchanged by the rejection.
5. **Given** `AW-SRV-002`'s `TestStep_ZoneFaultIsContained` and `TestLoop_ZoneFault` **when** this
   story lands **then** they are rewritten to assert the new rule, not deleted; the golden hash
   sequence (`AC-1` of 002) is regenerated only if the fixture log contains a fault, and the PR says
   which.

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

- **Metrics:** `andara_tick_zone_faults_total{zone}` unchanged (one per fault).
  `andara_tick_zone_faulted_rejections_total{zone}` — counter, cardinality Zones: how much a dead
  Zone is still being asked to do.
- **Logs:** `error` on the fault (exists); `warn` once per Zone per second at most while rejections
  continue — not per rejection.
- **Traces:** `sim.zone_tick` for a faulted Zone carries `faulted=true` and no child work.
- **Alerts:** none new. A faulted Zone is a cause; the symptom is players in it getting
  `zone_faulted`, and the Session availability SLO (`AW-SRV-011`) is where that surfaces.

## Test plan

- **Unit:** AC-1 through AC-4 on the stepped clock; the replay negative — a replay that re-applied
  instead of re-rejecting after the fault would hash differently.
- **Integration (`make test-integration`):** a fault on the broker, then `Recover` reproducing the
  same hash.
- **Manual/operator:** none beyond `make check`; there is no verb that panics on purpose outside a
  test fixture.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-002`'s Zone-fault question points here as resolved.

## Open questions

- **Resolved 2026-09-18 (Brian): quarantine the Zone, not the Partition.**
- `[ASSUMPTION]` A faulted Zone stays faulted until restart. Re-arming is `AW-SRV-012`'s reload, if
  it wants it; nothing here prevents it.
