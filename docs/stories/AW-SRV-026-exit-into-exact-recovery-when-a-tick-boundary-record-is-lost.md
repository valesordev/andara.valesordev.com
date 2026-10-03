---
id: AW-SRV-026
title: Exit into exact recovery when a Tick Boundary Record is lost
epic: EPIC-04
component: server
type: feature
status: review
size: S
depends_on: [AW-SRV-002]
blocks: [AW-SRV-007]
lane: implementation
risk: medium
---

## Context

`AW-SRV-002` shipped one policy for a lost Tick Boundary Record and asked for the other. Today, once
the publisher fails to deliver a boundary (a broker outage longer than the delivery timeout, or a
full buffer), the process **stops publishing boundaries and keeps ticking**: Events keep flowing,
players keep playing, and every tick after the loss is one the next restart cannot replay exactly —
it recovers to the last delivered boundary and re-batches from there. The alternative was to exit
immediately so Kubernetes restarts the pod into `tickloop.Recover`, which is exact by construction.

Brian decided on 2026-09-18: **exit**. The invariant is worth more than the uptime — a World whose
recent history cannot be replayed is a World whose State Hash nobody can check, and ADR-0002's whole
bet is that recovery is exact. The cost is stated honestly: every outage longer than the delivery
timeout restarts the sim, and a long outage restarts it repeatedly (`AndaraServerCrashLooping` fires
after three in ten minutes — that is the symptom, and it is the right one to page on).

## User story

As an operator, I want a server that lost a boundary to restart into exact recovery rather than keep
running on history it cannot replay, so that "the hash matches" is always true and never "probably".

## Scope

### In scope
- `KafkaPublisher.OnBoundaryLost` (exists, unwired) triggers shutdown: the loop completes the
  in-flight tick, emits `SimulationStopped{reason: boundary_lost}`, commits **no** offset past the
  last delivered boundary, and the process exits with a named code.
- The drain path already exists (`AW-SRV-002` AC-15); this story gives it a second trigger and a
  distinct exit code, log line, and counter.
- Recovery on the next boot is unchanged: `tickloop.Recover` replays to the last delivered boundary
  and the loop re-batches from there — asserted end to end.

### Out of scope
- Snapshot-based recovery — `AW-SRV-006`/`AW-SRV-007`; until then the restart replays from the log's
  beginning, which is correct and slow.
- A backoff on restarts. Kubernetes' `CrashLoopBackOff` is the backoff.
- Retrying the boundary. franz-go has already retried within the delivery timeout; the record is
  lost because the broker was unreachable for longer than that.

## Acceptance criteria

1. **Given** the broker unreachable for longer than the delivery timeout **when** the first boundary
   fails delivery **then** within one tick interval the loop stops, `SimulationStopped` carries
   `reason: boundary_lost`, the process exits `5`, and an `error` line names the lost tick and the
   last delivered tick.
2. **Given** that exit **when** the process restarts with the broker back **then** `Recover` replays
   to the last delivered boundary, the hash at that tick equals the recorded one, and the next
   boundary published is that tick + 1 — no gap on the topic, asserted by reading it back.
3. **Given** the loss **when** offsets are inspected **then** nothing past the last delivered
   boundary was committed — the re-applied ticks after restart re-consume the same records.
4. **Given** the compose stack **when** Redpanda is stopped for 90 s (the seventy-second scenario)
   **then** the server exits `5` once, `docker compose` restarts it, and after the broker returns
   `andara_ticks_total` resumes from the recovered tick with the topic gapless. `AW-SRV-002`'s
   eight-second outage test (`AC-9`, shorter than the delivery timeout) still passes: starvation is
   still not an exit.
5. **Given** `sim.source=memory` **when** the loop runs **then** nothing changes; the memory
   publisher cannot lose a boundary.

## Interface contract

| Signal | Value |
|--------|-------|
| exit code | `5` — `boot.ExitBoundaryLost`; `1` remains the drain-timeout exit |
| `SimulationStopped.reason` | `boundary_lost` (new enum value, additive) |
| log | `error` `"tick boundary lost; exiting into recovery"` with `lost_tick`, `last_delivered_tick`, `err` |
| metric | `andara_tick_boundary_lost_total` — counter, no labels; 0 or 1 per process lifetime |

No configuration. The delivery timeout stays franz-go's default (one minute); making it a key is a
different story if an operator ever needs it.

## Data / state impact

None on the topic: the whole point is that the boundary sequence stays gapless. Offsets committed
never pass the last delivered boundary, so a restart re-applies at most the ticks between it and
the loss, deterministically.

## Observability requirements

- **Metrics:** `andara_tick_boundary_lost_total` (above). `andara_tick_publish_failures_total{kind}`
  from `AW-SRV-002` still counts the underlying failures.
- **Logs:** the `error` line above, then `AW-SRV-002`'s `tick loop draining` / `tick loop stopped`.
- **Traces:** the last `sim.tick` span carries `boundary_lost=true`.
- **Alerts:** none new. `AndaraServerCrashLooping` (three restarts in ten minutes) is the symptom of
  a long outage under this policy; `docs/runbooks/server-crashlooping.md` gains a paragraph naming
  exit `5` and pointing at the broker.

## Test plan

- **Unit:** `OnBoundaryLost` → drain → exit code, on the stepped clock with a publisher that fails
  delivery of one boundary; offsets not committed past it (AC-3); memory source unaffected (AC-5).
- **Integration (`make test-integration`, throwaway Redpanda):** AC-1, AC-2 with a broker stopped
  past the delivery timeout, the topic read back gapless.
- **Integration (`stack` workflow):** AC-4, alongside the existing eight-second outage step.
- **Manual/operator:** `make up && docker compose stop redpanda && sleep 90 && docker compose start
  redpanda` — expect one exit `5` in `docker compose logs andara-server`, then ticks resuming.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-002`'s lost-boundary question and `AW-SRV-007`'s inherited note both
point here as resolved; the runbook paragraph exists.

## Open questions

- **Resolved 2026-09-18 (Brian): exit into exact recovery.** The seventy-second outage restarts the
  sim once; a ten-minute outage restarts it until the broker returns, and pages.

## Verification record — 2026-10-02 (implementation; `review` until the §8 checklist passes)

Branch `impl/aw-srv-026-exit-on-boundary-lost`. Questions and the hand-offs to SRE are in
`docs/feedback/AW-SRV-026-boundary-lost-exit.md`.

| AC | Test | What it asserts |
|----|------|-----------------|
| 1 | `TestLoop_BoundaryLostStopsIntoRecovery`, `TestLoop_BoundaryLostBetweenTicks` (unit, stepped clock); `TestKafka_BoundaryLostExitsIntoRecovery` (Redpanda); `TestLoopExit` (boot) | The loop stops after the tick that learned of the loss, which is within one interval when the loss lands between ticks. `SimulationStopped.reason` is `boundary_lost`. The `error` line names `lost_tick` and `last_delivered_tick` and comes before `draining` and `stopped`. The counter reads 1. The `sim.tick` span is marked and kept. A `BoundaryLostError` maps to exit `5`, and the drain timeout still maps to `1` |
| 2 | `TestKafka_BoundaryLostExitsIntoRecovery`; `TestBoundarySeq_*` (unit) | The topic ends at `last_delivered_tick`. The restart recovers to it with its recorded hash and resumes at its offsets. The next boundary is that tick + 1, and the topic reads back gapless from tick 1 and replays whole. The unit tests check that no boundary is handed to the producer while an earlier one is unresolved, and that a loss drops what was held, including a loss in the middle of a hand-over and a full buffer |
| 3 | `TestLoop_BoundaryLostStopsIntoRecovery`, `TestLoop_CheckpointWaitsForDelivery` (unit); `TestKafka_BoundaryLostExitsIntoRecovery` | The committed offsets are those of a delivered boundary, never past the last one. With two ticks of delivery lag, the commit is tick 10's, not tick 13's |
| 4 | SRE's feedback items 1 and 2. `TestBoundarySeq_UnansweredPastTheTimeoutIsLost` covers the publisher's half | A boundary unanswered past the delivery timeout is lost while the broker is still away |
| 5 | `TestLoop_MemoryPublisherUnchanged` | The memory loop drains with `draining` and returns nil. The counter reads 0, and the drain's own checkpoint commits tick 7's offsets, where 7 isn't a checkpoint tick |
| — | `TestWireBoundaries`, `TestLoop_AckAtOrPastTheLossIsIgnored` | The server's wiring: an acknowledgement reaches the checkpoint gate and the lag gauge, and a loss stops the loop. An acknowledgement at or past the lost tick moves nothing |

**Changed beyond the contract's surface.** All three are recorded for architecture in the
feedback file.
- **For AC-2:** the publisher hands a boundary to the producer only once every earlier one is
  acknowledged (`tickloop.boundarySeq`), which closes a race the pre-PR review found that could
  leave a gap. It also declares a boundary lost when it goes unanswered past the delivery
  timeout.
- **For AC-3:** a checkpoint waits for its boundary's acknowledgement
  (`tickloop.Options.AwaitBoundaryAck`, on for `sim.source=kafka`).
- **The drain** flushes the publisher before it commits.

**Measured, not asserted:** four rounds against a stopped Redpanda, with a 3 s delivery timeout
(details in the feedback file). In each, the loss was declared while the broker was down, the
topic stayed gapless, and the restart recovered and continued.

**Outstanding before `done`:**
- AC-4. It needs SRE's compose `restart:` policy and the `stack` workflow step (feedback items 1
  and 2).
- The runbook paragraph (feedback item 3).
- SRE's §8 instrumentation check: `andara_tick_boundary_lost_total` is registered on the server's
  registry and reads 0 until a loss, and its first live 1 is AC-4's.

## §8 instrumentation check — 2026-10-03 (SRE, `sre/aw-srv-026-verify`)

**AC-4 passes on the local stack**, through a new target, `make stack-boundary-lost`, which the
`stack` workflow now runs. Compose's `andara-server` gained `restart: on-failure` (feedback item 1).
The live run, at `main` b543aae plus this branch:

```
stack-boundary-lost: server ready at andara_ticks_total=63; stopping redpanda for 90s
stack-boundary-lost: first exit 5, 71s into the outage
stack-boundary-lost: redpanda started after 90s
stack-boundary-lost: lost tick 1636774; recovered to 1636773; 8 exit(s): 5 1 1 1 1 1 1 1; ticking (8 -> 18)
stack-boundary-lost: read-back recovered to tick 1636795: the boundaries are gapless across the loss
stack-boundary-lost: AW-SRV-026 AC-4 — exited 5 on the lost boundary, recovered to it, gapless — passes
```

- **The exits** come from `docker events`, since `restart:` hides all but the last. There's one `5` at
  71 s (the 60 s delivery timeout plus the drain), then seven `1`s while the broker was still down.
  That's the crash loop the story's Context describes.
- **"Resumes from the recovered tick"**: the `recovered from the log` tick equals the loss line's
  `last_delivered_tick`, and `andara_ticks_total` moves again.
- **"Gapless, read back"**: today's recovery replays every boundary and refuses a gap
  (`sim.ErrBoundaryGap`), so the target restarts the server once more. It requires a recovery
  past the loss that replayed the whole log (`ticks_replayed == tick`; the live read-back was
  `1636795 == 1636795`). The read-back is the product's own reader. Once AW-SRV-007 recovers
  from a snapshot, the full-replay check fails loudly and the read-back needs replacing.
- **AW-SRV-002's short broker outage** (about 30 s at most, well inside the 60 s delivery timeout) runs earlier in the same workflow and still passes. Its
  "no exit" is indirect: a restart would reset `andara_ticks_total` and fail its `td - tb >= 40`,
  and its health check reads `healthy`. This step runs just before the M1 gate, which restarts
  the server anyway, so its restarts reset nothing a later step reads.

**Instruments, each seen on a real backend:**

| §7 instrument | Backend | Observed |
|---|---|---|
| `andara_tick_boundary_lost_total` | Prometheus | `max_over_time(…[20m])` = `1`. The scrape caught the dying process during its drain. Reads `0` on the recovered server |
| `error` `tick boundary lost; exiting into recovery` | Loki, `{service_name="andara-server"}` | `detected_level=error`, `lost_tick=1636774`, `last_delivered_tick=1636773`, `err="tick boundary not acknowledged within the delivery timeout: unanswered for 1m0.099s"` |
| last `sim.tick` span with `boundary_lost=true` | Tempo, `{span.boundary_lost=true}` | trace `cc276805411e6308e698c8b152cedc98`, root `sim.tick`, kept |
| the runbook paragraph | `docs/runbooks/server-crashlooping.md` | exit `5` row, shipped in #364 (feedback item 3) |

**One observation, not holding the story:** the loss line carries no `trace_id`. It's logged on
`context.Background()` (`tickloop.reportLost`). The tick loop isn't a request path, and the `sim.tick`
span is found by its attribute, so §7's correlation rule isn't breached. It's noted for
implementation in the feedback file.

The instrumentation item of CLAUDE.md §8 is met. Architecture runs the rest of §8 and moves the
story.
