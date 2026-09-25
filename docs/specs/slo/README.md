# Service level objectives

Every user-facing service gets an SLO document **before** it gets an alert (`CLAUDE.md` §7).

An SLO document states, by name:

1. **SLI definition** — what is measured, and the exact metric expression that measures it.
2. **Target** — the number.
3. **Window** — rolling, with a stated length.
4. **Error budget** — derived from target and window, stated in absolute terms.
5. **Exhaustion policy** — what changes when the budget is gone. If the answer is "nothing", the
   SLO is decoration.

Alerts are symptom-based and tied to an SLO. No alerts on causes. An alert without a runbook entry
in `docs/runbooks/` is incomplete.

## Current

| File | Status | Owning story | Covers |
|------|--------|--------------|--------|
| `tick-health.md` | proposed targets | `AW-SRV-002` | tick budget adherence, simulation lag |
| `recovery.md` | proposed targets | `AW-SRV-006`, `AW-SRV-007` | RPO and RTO |
| `world-write-availability.md` | proposed targets | `AW-INF-005` | the World accepting Commands |
| `edge-availability.md` | proposed targets | `AW-INF-006` | the edge letting players through: 5xx ratio, certificate validity |
| `session-availability.md` | target decided 2026-09-20 | `AW-SRV-011` | the Event stream staying open: server-ended streams per stream-second |
| `projection-freshness.md` | proposed target | `AW-SRV-019`, shared with `AW-SRV-017`/`018` | index lag while the World ticks; replica integrity (no budget) |
| `content-freshness.md` | proposed target | `AW-SRV-012` | a pointer move served within 30 s, system failures only |

"Proposed targets" means the number was reasoned from the architecture but not yet checked against a
running system. The owning story validates it against first measurement and either confirms it or comes
back with why not.

## Planned

| File | Owning story | Covers |
|------|--------------|--------|
| `kafka-availability.md` | `AW-INF-005` | broker availability underneath write availability |

## The numbers that constrain each other

These targets are not independent, and changing one without the others is how a system acquires a
contradiction:

```
linkdead_grace (180 s)  >  RTO (60 s)          or a restart despawns every player
snapshot cadence (60 s) bounds tail replay      which is the smallest RTO term anyway
tick budget (50 ms)     = ½ tick interval       so overruns warn before lag accrues
write-availability budget ÷ RTO = deploy count  which is what makes sharding urgent
egress resume window    ≳ linkdead_grace        or every reconnect resyncs
```

Tick duration, tick overrun count, and simulation lag are first-class SLIs from the first server story
onward — not retrofitted.
