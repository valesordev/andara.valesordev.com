# SLO — Recovery: RPO and RTO

> **Status: proposed targets, 2026-09-07.** Recommendations requested by Brian. `AW-SRV-006` and
> `AW-SRV-007` validate these against first measurement and either confirm them or come back with why
> not. A target set before measurement is a hypothesis with a number on it; this document says so
> rather than pretending otherwise.

## RPO — Recovery Point Objective

### Target: **zero acknowledged actions lost. Ever.**

Not "a few seconds." Zero. This is not optimism — it falls out of ADR-0002's structure:

1. The Gateway produces a Command to `andara.commands.v1` with `acks=all` and
   `min.insync.replicas=2`, so the record is on at least two brokers before the produce returns.
2. Only then is the player acked (`AW-SRV-010` AC-1).
3. The tick only ever applies records it consumed from the log.

A player is therefore never told "accepted" about something that did not survive. In-memory World state
is entirely derived from the log, so there is no state that exists only in a process's memory and could
be lost with it.

### What *can* be lost

Commands submitted but not yet acknowledged — in flight when a failure hits. The player learns this
immediately: they get `UNAVAILABLE`, `DEADLINE_EXCEEDED`, or a dropped stream. That is "your command did
not go through," not "you lost progress," and it is the honest and correct behavior.

### The three configuration lines this target depends on

| Setting | Required value | If wrong |
|---------|----------------|----------|
| `acks` | `all` | the ack outruns durability; acknowledged actions can vanish |
| `min.insync.replicas` | `2` (with RF=3) | `acks=all` degenerates to one replica and a single disk loses data |
| `unclean.leader.election.enable` | **`false`** | an out-of-sync replica can be elected leader and silently truncate acknowledged records |

The third line is the one that gets missed. With unclean leader election enabled, `acks=all` does **not**
guarantee durability — Kafka will prefer availability over correctness and discard acknowledged writes.
An RPO of zero is a claim about all three settings together, and `AW-INF-005` asserts them against
running processes rather than trusting a values file.

### SLI

`andara_acknowledged_commands_lost_total` — a counter that must remain 0. It is incremented by the
recovery path when a Command that was acknowledged to a client is absent from the log after recovery
(detectable because `Submit` returns the assigned partition and offset, so the client's ack references a
specific log position).

**Any non-zero value is an incident, not a budget burn.** There is no error budget for this SLI.

---

## RTO — Recovery Time Objective

### Targets

| Milestone | Target | Measured as |
|-----------|-------:|-------------|
| M2 (first achievement) | **120 s p99** | `SIGKILL` → accepting connections with a verified State Hash |
| Phase 1 exit | **60 s p99** | same |

Two numbers deliberately: 120 s is honest for an unoptimized path and is the number to beat; 60 s is what
Phase 1 exit criterion 3 should be judged against.

### Where the time goes

| Phase | Expected | Dominated by |
|-------|---------:|--------------|
| failure detection | 5–15 s | liveness probe period × threshold |
| pod reschedule and start | 10–30 s | scheduler, image pull |
| load newest snapshot | 1–5 s | state size, object storage |
| replay log tail | **1–3 s** | snapshot cadence ÷ replay speed |
| State Hash verify | < 1 s | — |
| **total** | **~20–55 s** | Kubernetes, not the simulation |

The important observation for anyone about to optimize this: **tail replay is the smallest term.** With a
60 s snapshot cadence and replay running at roughly 100× realtime, there are at most ~600 ticks to replay.
The dominant costs are Kubernetes failure detection and pod startup. Optimizing recovery therefore means
tuning probes and pre-pulling images (`AW-INF-003`), not making the simulation faster.

### Derived: snapshot cadence

**Recommended: a snapshot every 60 seconds of wall time, per Zone.**

It bounds tail replay to ~600 ticks at 10 Hz (ADR-0008), which keeps that term negligible. It also bounds
the rebalance stall when ADR-0001's sharding is eventually activated, since a newly-assigned partition
recovers by the same path — a second consumer of this number, and a reason not to tune it purely for RTO.

### The constraint that links this to ADR-0006

```
linkdead_grace  >  RTO  ,  with margin
    180 s       >  60 s          3×
```

On a restart every Session drops and every Character goes linkdead. If the grace period does not outlast
recovery plus the client's reconnect backoff, a routine restart despawns every player in the world.

At 180 s grace and 60 s RTO there is 3× margin. **Neither number may be changed without checking the
other**, and `AW-SRV-015` asserts the invariant at startup rather than leaving it to whoever next edits a
values file.

### SLI

- `andara_recovery_duration_seconds` — histogram, from process start to ready.
- `andara_recovery_state_hash_match` — gauge, 0 or 1. Must be 1.

**Target:** 99% of recoveries within the milestone's RTO, measured per incident rather than over a window
— recoveries are rare enough that a rolling percentage is meaningless.

**Alert:** `RecoveryStateMismatch` on `andara_recovery_state_hash_match == 0`. This is the one alert in
the system that must page: a World that recovered to the wrong state must never take traffic.

### Error budget and exhaustion policy

RTO has no error budget in the usual sense; it is a per-incident target. The policy when it is missed:
**the next change to the simulation core or the persistence adapters is the RTO fix.** Recovery time
regressions are published as a CI artifact (`AW-SRV-007`) so a regression is visible in a diff rather
than discovered during an outage.

---

## Open questions

- **What players see during recovery.** The engineering targets above say nothing about whether a player
  sees a countdown, a graceful notice, or silence-and-reconnect. `[NEEDS BRIAN]` — carried from
  `AW-INF-007`.
- **Whether the World refuses to start or rolls back on a hash mismatch.** Refusing is safer; rolling
  back to the last matching tick is more playable. `[NEEDS BRIAN]` — carried from `AW-SRV-007`.
