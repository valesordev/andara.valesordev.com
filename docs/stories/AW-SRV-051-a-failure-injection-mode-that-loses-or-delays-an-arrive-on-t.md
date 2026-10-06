---
id: AW-SRV-051
title: A failure-injection mode that loses or delays an Arrive on the running server
epic: EPIC-02
component: server          # server | cli | infra | client
type: feature              # feature | infra | spike | chore | bug
status: draft              # draft | ready | in-progress | review | done | blocked
size: M                    # S | M | L  — L means "split it"
depends_on: [AW-SRV-028]
blocks: [AW-INF-036]
lane: implementation       # architecture (contracts) | sre (infra, ops) | implementation (source)
risk: medium               # low | medium | high
---

## Context

`AW-SRV-028`'s §8 instrumentation check found four observations that tests cover and the running server has
never emitted: `andara_handoff_retries_total` above 0, `andara_handoff_stale_arrivals_total` above 0,
`andara_handoffs_in_transit` above 0 on a scrape, and the `warn`, `debug` and `error` lines. A live retry
needs a handoff whose `Arrive` was lost. `sim repl` drives the pipeline in-process against a fake log, so it
has no running server and no `/metrics`, and the stack has no way to lose an `Arrive` short of breaking the
broker mid-move. Architecture's §8 review (#434) held `AW-SRV-028` at `review` until a tracked story carries
that observation, per CLAUDE.md §8's deferral rule, and asked PM for it
(`docs/feedback/AW-SRV-028-handoff-contract.md`, "For PM: the failure-injection story"). This is the server
half: the mode that makes loss repeatable. `AW-INF-036` is the stack half: the target that runs it and reads
the result, because the Makefile is SRE's (§2) and a procedure that isn't a target is a §9 defect.

The mode is test machinery in a server that serves players, so its surface is architecture's (Open question
1). The story stays `draft` until that is ruled.

## User story

As an operator, I want to make the server lose or delay a cross-Zone `Arrive` on purpose, so that I can watch
a handoff retry, a stale arrival and a stuck crossing show up on the metrics and logs of the running server
instead of trusting tests alone.

## Scope

### In scope
- A mode, off by default, that **drops** the next `n` `Arrive` productions (the source's retry then delivers),
  and one that **delays** an `Arrive` by `d` ticks so that its retry overtakes it and the late original is
  stale.
- It acts only on `Arrive` productions for a handoff, at the production seam `AW-SRV-028` already has.
- It is visible: the server logs once at `warn` when the mode is on, and reports it in a gauge.
- Tests that read the metric objects with the mode on (CLAUDE.md §8, no in-cluster caller).

### Out of scope
- The target that runs it on the stack and asserts the series and lines — `AW-INF-036`.
- Injecting an invalid or impossible `Arrive` to reach the `error` line (Open question 1c).
- Any production use: the mode is refused outside the local stack (Open question 1b).
- Failure injection for anything but `Arrive` (`HandoffAck`, snapshots, the broker).
- The summary `warn`'s bound, which is #409.

## Acceptance criteria

1. **Given** the mode off **when** the server runs **then** no `Arrive` is dropped or delayed, and the mode's
   gauge reads 0.
2. **Given** `drop` with `n = 1` **when** a Character crosses Zones **then** the first `Arrive` is not
   produced, `andara_handoffs_in_transit` is 1 until the retry, the retry is produced after
   `sim.handoff_retry_ticks` (1 s at the default 10 ticks and 10 Hz, so a scrape must poll faster than that),
   the Character is placed, and `andara_handoff_retries_total` is 1.
3. **Given** `drop` with `n = 3` **when** a Character crosses **then** exactly three productions are lost
   (the original and two retries), `andara_handoff_retries_total` is at least 3, and the Character is placed
   by the fourth.
4. **Given** `delay` with `d` greater than `sim.handoff_retry_ticks` **when** a Character crosses **then** the
   original `Arrive` is held `d` ticks and released on tick `d` even if that tick produces nothing else
   (only the first production of each handoff is held; its retries are not), the retry is applied first, the Character is placed once, the delayed original is stale-acked, and
   `andara_handoff_stale_arrivals_total` is 1. No second body exists.
5. **Given** the mode on **when** the server starts **then** it logs one `warn` naming the mode and its
   arguments, and the gauge reads 1.
6. **Given** `drop` with `n = 1` on, and a kill (`SIGKILL`) between the dropped `Arrive` and its retry
   **when** the server restarts with the mode off **then** the first `DueHandoffs` call retries the `Transit`
   record from the recovered state and the Character is placed (`AW-SRV-028` AC-2). The drop count is per
   process: a server restarted with the mode on starts its count again, and drops its first recovered retry
   too.
7. **Given** the mode's arguments out of range (`n` below 1, `d` below 1, or an unknown mode) **when** the
   server starts **then** it exits 1 and names the key.
8. **Given** the mode on **when** it affects a production **then** the injection is logged at `debug` with
   the Entity ID and the handoff sequence, and counted on a counter.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
// The seam is AW-SRV-028's: where the tick loop produces an Arrive for a Transit record.
// The seam is the tick loop's Publisher (outside the Engine, CLAUDE.md §10), which carries the original
// Arrive and its retries. The surface (config key, env var, CLI flag, admin RPC) is Open question 1.
type ArriveFault interface {
    // Produce reports whether this Arrive production goes out now, or is lost, or is held.
    Produce(entity sim.EntityID, seq uint64, tick sim.Tick) Decision
    // Release is called on EVERY tick, whether or not the tick produced anything: the loop calls
    // Publisher.Produce only when its outbound list is non-empty, so a held Arrive would otherwise
    // sit buffered through quiet ticks and never go out. It returns the held Arrives now due.
    Release(tick sim.Tick) []*logv1.LoggedCommand
}
type Decision int // Send | Drop | Hold(until tick)
```

- **Held Arrives** live in the fault, not in the Engine, and aren't hashed. A restart loses them, which is
  safe because the source's retry still stands (`AW-SRV-028` AC-2).
- **Modes:** `drop` with `n` productions (counted per process, across handoffs, from the first), `delay` with
  `d` ticks (the first production of each handoff only). Off by default. The argument syntax is part of the
  surface (Open question 1a).
- **Metrics:** `andara_handoff_fault_active` — gauge, 0 or 1, no labels, pre-registered at 0.
  `andara_handoff_fault_injected_total` — counter, label `mode` (`drop`, `delay`). Two series, pre-registered.
- **Surface:** `[ASSUMPTION]` a server config key set in the local stack's compose file, for example
  `sim.handoff_fault` with `ANDARA_SIM_HANDOFF_FAULT`. Not final: Open question 1.
- **Exit codes:** `1` for AC-7's refusal (the server's existing configuration-error code).

## Data / state impact

None. The mode acts only on production, never on `Transit`, `Placed` or the hash, so a World that ran with it
on replays identically with it off. `[ASSUMPTION]` the Engine never sees the fault: it lives at the tick
loop's production seam, outside the deterministic core (CLAUDE.md §10: the core has no clock or network
reaching into it).

## Observability requirements
`[ASSUMPTION]` for SRE's §7 review, which owns this section.

### Metrics
- `andara_handoff_fault_active` and `andara_handoff_fault_injected_total{mode}`, as in the contract above.
  Cardinality: 1 and 2 series.

### Logs
- `warn` once at start when on: `mode`, arguments, plus the required fields.
- `debug` per injected production: `entity_id`, `handoff_seq`, `mode`, `tick`, `trace_id`. The stack logs at
  `info` by default (`ANDARA_LOG_LEVEL`, `deploy/compose/docker-compose.yaml`), so the debug lines show only
  at `ANDARA_LOG_LEVEL=debug` (`AW-INF-036` sets it).

### Traces
- None. An injected loss has no span; the retry's `command.apply` is as `AW-SRV-028` describes.

### Alerts
- None. A server with the mode on is a local-stack condition; it is refused elsewhere (Open question 1b).

## Test plan
- **Unit:** the decision function for `drop` and `delay` over a stepped clock (AC-1 to AC-4), including a
  held `Arrive` released on a tick with no other outbound; argument
  validation (AC-7); the gauge and counters (AC-5, AC-8). Reads the metric objects.
- **Integration:** a two-Zone engine with the tick loop, a fake `Log`, and the mode on, asserting AC-2 to AC-4
  from the metric objects; a broker-level kill between the dropped `Arrive` and its retry (AC-6), beside
  `AW-SRV-028`'s AC-2 test.
- **Manual/operator:** `make stack-handoff-fault` (`AW-INF-036`). `§9 defect → AW-INF-036` until it exists.

## Definition of done
CLAUDE.md §8, plus:
- Inherited from `AW-SRV-028`'s §8 instrumentation check: the series and lines the running server hasn't
  emitted (`andara_handoff_retries_total` above 0, `andara_handoff_stale_arrivals_total` above 0,
  `andara_handoffs_in_transit` above 0 on a scrape, and the `warn`, `debug` lines) are produced by this mode
  in the tests above. Their **live** observation on the running stack is `AW-INF-036`'s.
- The server README documents the mode, its arguments, and that it is refused outside the local stack.

## Open questions
1. **The surface.** *(Architecture's, at contract review.)*
   a. **Where does the mode live?** A config key on the server (set in compose), an admin RPC, or a `sim
      repl` flag. `sim repl` can't reach the running server's metrics, so a flag there wouldn't carry the
      observation; architecture's request said "for `sim repl` or the stack".
   b. **How is it kept out of production?** Refuse the key unless the environment is the local stack, or a
      build tag, or both. §10 allows no privileged back door into a running process, so an admin RPC needs an
      ADR-level answer.
   c. **The `error` line.** Dropping or delaying an `Arrive` can't produce the "impossible `Arrive`" `error`
      log (`AW-SRV-028` AC-9's rejections, `entity_present` and `invalid_arrival`, which its §7 Logs say are logged at
      `error`). Is that line carried here by a third mode (a malformed `Arrive`),
      or left to tests with the §8 record saying so?
2. `[ASSUMPTION]` Size M: two modes, two metrics, a recovery test, and a startup guard.
