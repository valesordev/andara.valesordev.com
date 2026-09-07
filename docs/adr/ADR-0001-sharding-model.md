---
id: ADR-0001
title: World simulation sharding model
status: accepted
date: 2026-09-07
deciders: [brian]
gates: []
---

## Context

The world simulation is server-authoritative and advances on a Tick Loop. The question is how many
processes hold authority over that Tick Loop, and over what partition of the World.

The draft of this ADR framed three options — one process, zone-sharded with a bespoke handoff
protocol, or one process with hand-held "shard-ready seams" enforced by lint and discipline. ADR-0002
has since made that third option concrete in a way discipline never could: with Kafka as the ordering
authority, the seam is not a convention we maintain, it is a partition boundary that physically
exists whether we shard or not.

That changes the decision from "which architecture" to "how many processes consume the partitions we
already have."

## Decision

**One authoritative simulation process at launch, owning all partitions. Sharding is a consumer-group
rebalance, not a migration.**

Concretely:

1. `andara.commands.v1` has **64 partitions** (ADR-0002 §6). Zones map to partitions by
   `hash(ZoneID) % 64`. A partition is the unit of simulation authority.
2. At launch, one `andara-server` process is assigned all 64 partitions. Deployed as a
   `StatefulSet`, not a `Deployment` — pods carry partition-assignment identity and stable storage
   for snapshots, and choosing this now avoids a topology migration later.
3. **A Zone's state is mutated only by the process that consumes that Zone's partition.** There is no
   other write path. This is not a rule we enforce by review; there is no API through which to break
   it.
4. **Cross-Zone effects are Commands produced to the target Zone's partition** — always, including
   when both Zones are consumed by the same process. Never a synchronous in-process call. Behavior
   must be identical whether the target Zone is in this process or another one; a same-process
   shortcut would make sharded and unsharded deployments differ, which is the exact bug class this
   design exists to prevent.
5. Cross-Zone effects therefore resolve on a later tick — at minimum the next one. This is a stated
   world-behavior rule, not an implementation detail, and it belongs in the player-facing design.
6. The Gateway is a separate concern in the same binary for Phase 1, behind an interface that permits
   extracting it to its own `Deployment` without touching the simulation. Because the Gateway's only
   write is a Kafka produce, extracting it later requires no change to the simulation at all.

Sharding, when it happens, is: increase the replica count, let the consumer group rebalance, let each
process recover its newly-assigned partitions from snapshot plus log tail. There is no handoff
protocol to design, because there is no state transfer — the new owner reads the same log the old
owner read.

## Consequences

**Entity handoff dissolves as a problem.** A Character moving from a Zone on partition 7 to a Zone on
partition 41 produces a Command to partition 41 and is removed from partition 7's state. If those
partitions are on different processes, the mechanism is unchanged. This is the single largest
simplification the ADR-0002 decision buys, and it is why the earlier draft's Option B — with its
freeze-transfer-resume protocol and exactly-once handoff semantics — is not on the table.

**Rebalance is a partial-World interruption.** During a consumer-group rebalance, the affected
partitions are not ticking. Sessions bound to Characters in those Zones stall for the rebalance plus
recovery duration. This is much better than the full-World restart the earlier draft accepted, but it
is not free, and it means snapshot cadence directly determines rebalance pain.

**Tick budget is per-process, shared across the partitions it owns.** At launch that is all 64, so
one pathological Zone can still starve the World. Per-Zone tick duration is a mandatory metric from
the first server story precisely so this is visible before it is felt — and now the remedy is cheap:
move that Zone's partition to its own process.

**A process panic drops every Session it served.** At launch that is all of them. RTO is a first-class
SLO from M2.

**Every zone crossing costs a log round trip.** At MUD tick rates this is absorbed by the tick
interval. It would not be at 60 Hz.

**We are foreclosing** any design where a process mutates World state it did not consume from the log,
and any design where cross-Zone interaction is a synchronous call. A story that reaches for either is
malformed, not a judgment call.

## Revisit when

Any one of:

- A single partition's consume-and-apply rate approaches the tick budget. This is the direct signal
  to raise the replica count, and it is measurable from M1.
- Median tick duration exceeds 40% of the tick budget at expected peak concurrency.
- A single Zone accounts for more than 30% of total tick duration and cannot be optimized.
- 64 partitions stops being enough headroom — which, given a partition can hold many Zones, means the
  World has grown well past anything currently contemplated.
