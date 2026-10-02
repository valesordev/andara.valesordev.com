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
2. **The `stack` workflow step.** Stop Redpanda for 90 s, then start it. Assert that
   `andara-server` exited `5` exactly once and came back, and that `andara_ticks_total` resumed
   from the recovered tick. Assert also that the topic is gapless: tick *n* + 1 follows tick *n*
   across the restart, which `andara-cli` or `rpk` can read back. It sits beside the existing
   "the tick loop survives a broker outage" step, which must still see no exit at 8 s.
3. **The runbook paragraph** in `docs/runbooks/server-crashlooping.md`. Exit `5` means a Tick
   Boundary Record was lost, so look at the broker. The log line is
   `tick boundary lost; exiting into recovery` with `lost_tick` and `last_delivered_tick`, and
   the counter is `andara_tick_boundary_lost_total`.

**The timing the step must expect.** The exit comes when the broker **returns**, not 60 s into
the outage. The producer is idempotent, and franz-go evaluates `RecordDeliveryTimeout` only
before it writes a request or after it reads a response. While Redpanda is stopped it does
neither, so the boundary isn't failed until the first reconnect, when it already has timed out.

I measured this against a throwaway Redpanda v25.1.10 (`docker stop`, then `docker start`, five
rounds, with a 3 s delivery timeout):
- During a 15 s outage, no round exited.
- In every round, the loop stopped with the boundary after the last delivered one lost as soon
  as the broker was back.

For AC-4, that means the 90 s outage ticks starved for 90 s with no exit. Then, once Redpanda is
healthy, there is one exit `5`, the restart, and recovery. The runbook should say the same: a
crash loop with exit `5` follows the broker's return, so a broker that is still down shows as
starvation (`andara_tick_input_starved_total`), not as restarts.

### For architecture: two things the contract doesn't say

- **Checkpoints now wait for the broker's acknowledgement.** AC-3, and the Data/state line that
  "offsets committed never pass the last delivered boundary", needed this. Before, the loop
  committed every `sim.checkpoint_every_ticks` tick's offsets as soon as it had *published* the
  boundary, and the drain committed the engine's current offsets. Either could pass the last
  delivered boundary. Now:
  - A checkpoint is held until that tick's boundary is acknowledged (`Options.AwaitBoundaryAck`,
    set for `sim.source=kafka`).
  - The drain flushes the publisher first, then commits the newest acknowledged boundary.

  `andara_checkpoint_age_ticks` is therefore larger by the acknowledgement round-trip. On
  `sim.source=memory`, `Publish` is the delivery, and nothing changes there (AC-5).
- **A cut connection while a boundary is in flight is not a loss.** With idempotence on,
  franz-go never fails a batch it sent and got no answer for. It retries until the broker
  answers, and then the batch is delivered or deduplicated. So a network partition that strikes
  mid-request delays boundaries rather than losing them: no exit, and no gap. This is safe, but
  "every outage longer than the delivery timeout restarts the sim" (the story's Context) holds
  only for outages where the broker refused connections or answered with errors. For that
  reason, the integration test's outage is a proxy that answers every Produce with the
  retriable `NOT_ENOUGH_REPLICAS`, not a severed socket. A cut socket made the test wait
  forever.

### How each AC is covered

See the story's verification record.
