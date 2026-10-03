# AW-SRV-026: exit into exact recovery when a boundary is lost

Story: `AW-SRV-026` (`review` with this PR). Branch `impl/aw-srv-026-exit-on-boundary-lost`.

## Implementation, 2026-10-02

The server side is built: ACs 1, 2, 3 and 5 are tested under `make test` and
`make test-integration`. AC-4 needs three things in SRE's paths, listed below. The story's
Observability section also gives SRE the runbook paragraph.

### For SRE: what AC-4 needs

1. **A restart policy on `andara-server` in `deploy/compose/docker-compose.yaml`.** The service
   has no `restart:` key today. A server that exits `5` stays down, so "`docker compose` restarts
   it" can't happen. `restart: on-failure` is the narrowest fit, since exit `0` after a
   `SIGTERM` drain should stay down.
2. **The `stack` workflow step.** Stop Redpanda for 90 s, then start it. Assert:
   - `andara-server` exited `5` exactly once;
   - it came back, and `andara_ticks_total` resumed from the recovered tick;
   - the topic is gapless: tick *n* + 1 follows tick *n* across the restart, which `rpk` can
     read back.

   It sits beside the existing "the tick loop survives a broker outage" step, which must still
   see no exit at 8 s.
3. **The runbook paragraph** in `docs/runbooks/server-crashlooping.md`. Exit `5` means a Tick
   Boundary Record was lost, so look at the broker. The log line is
   `tick boundary lost; exiting into recovery` with `lost_tick` and `last_delivered_tick`, and
   the counter is `andara_tick_boundary_lost_total`.

**The timing the step must expect.** The server exits about 60 s into the outage, that is the
delivery timeout plus up to 10 s while the drain's flush runs out. While the broker is still
down, each restart fails to reach it at boot and exits `1`. Its restarts are the crash loop the
story's Context describes. Once Redpanda is healthy, the next boot recovers and ticks. So over
a 90 s outage, expect one exit `5`, then exit `1` until the broker is back. A broker that
returns within the delivery timeout produces no exit at all.

Measured against a throwaway Redpanda v25.1.10 (`docker stop` with a 3 s delivery timeout, four
rounds):
- Every round stopped 13 s into the outage: 3 s, plus the 10 s flush.
- Every round reported the boundary after the last delivered one lost.
- The topic ended at the last delivered boundary, gapless.
- After `docker start`, a second process recovered there and continued, still gapless.

### For architecture: three things the contract doesn't say

The pre-PR review found a race, and closing it changed how a boundary reaches the broker.

- **One acknowledgement round trip per boundary.**
  - **The race.** franz-go reports a failed record on its promise goroutine after it has emptied
    the Partition's buffer. A boundary produced in that window goes into a fresh buffer and can
    be delivered, leaving `N-1, [gap], N+1` on the topic. Recovery then refuses that topic
    (`sim.ErrBoundaryGap`).
  - **What the publisher does now (`boundarySeq`).** It hands a boundary to the producer only
    once every earlier one is acknowledged. Boundaries taken meanwhile are held, then handed
    over together, in order. A loss drops whatever was held.
  - **The cost.** A boundary reaches the topic up to one acknowledgement round trip later. The
    tick never waits on it, and `andara_event_publish_lag_seconds` includes the hold.
- **The publisher declares the loss itself.**
  - **Why.** With idempotence on, franz-go never fails a batch it sent and got no answer for: it
    retries until the broker returns. With one boundary outstanding at a time, that boundary is
    almost always in flight when a broker stops. In four rounds against a stopped Redpanda,
    every outage ended with that boundary delivered on the broker's return: no loss, no exit.
  - **What it does.** It applies the delivery timeout to the boundary it handed over. If the
    boundary is unanswered past the timeout, that boundary is the lost one, while the broker is
    still away, and nothing behind it is ever handed over.
  - **Effect on the topic.** If its write did land unacknowledged, the topic ends at the lost
    tick instead of the one before it. It is gapless either way, and recovery replays to
    whichever is there. So `last_delivered_tick` is the last *acknowledged* boundary, and the
    topic may hold one more.
- **Checkpoints wait for the broker's acknowledgement.** AC-3, and the Data/state line that
  "offsets committed never pass the last delivered boundary", needed this. Before, the loop
  committed a checkpoint tick's offsets as soon as it had *published* the boundary, and the drain
  committed the engine's current offsets. Either could pass the last delivered boundary. Now:
  - A checkpoint is held until its boundary is acknowledged (`Options.AwaitBoundaryAck`, set for
    `sim.source=kafka`).
  - The drain flushes the publisher first, then commits the newest acknowledged boundary.

  On `sim.source=memory`, `Publish` is the delivery, and nothing changes there (AC-5).

### How each AC is covered

See the story's verification record.
