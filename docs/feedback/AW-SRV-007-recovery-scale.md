# AW-SRV-007: the Entity count behind 120 s, and SeekAfter

Story: `AW-SRV-007` (recovery from snapshot and log tail), which is `ready`.
Raised: 2026-09-27, PM, at the SPRINT-02 close-out, from `AW-SRV-019`'s §8 feedback
(`docs/feedback/AW-SRV-019-state-projector.md`, "For PM: `AW-SRV-007`'s grooming").

M2's server-kill slice, including `AW-SRV-007`, moves to SPRINT-04 (Brian, 2026-09-27). These go to
architecture for that sprint's contract review.

## For architecture

1. **State the Entity count the 120 s is measured at.** `AW-SRV-019` measured tail replay at about
   65 ms per tick at 25,000 Entities, against a 50 ms Tick Budget. At one snapshot interval (600
   ticks) that's about 39 s, inside 120 s. But a World that overruns its budget can't replay faster
   than it ran, so the target means nothing without a scale.
2. **Decide whether `tickloop.Recover` uses `SeekAfter`.** Without the seek, the recovery scan grows
   with retention, as the projector's did (6.7 s at 24 h, 0.13 s after `BoundaryReader.SeekAfter`).
   Implementation's note calls this `AW-SRV-007`'s open question, but the story doesn't list it.

## For architecture: SPRINT-04's contract review adds two (PM, 2026-10-02)

Items 1 and 2 above are still open, and SPRINT-04 plans `AW-SRV-007`, so they're in that sprint's
contract review. Two more, from the SPRINT-03 close-out:

3. **`AW-SRV-043` joins `depends_on`.** A restore that proves the round's recorded State Hash before
   replay is what M2's "matching State Hash" rests on (`AW-SRV-043`'s Context). The draft's Open
   question 2 asks for this edge. PM didn't add it, because `AW-SRV-007` is `ready` and its
   frontmatter is yours. If you accept `AW-SRV-043`, add the edge when you move it to `ready`.
4. **The operator test is now a target.** The Test plan's "Manual/operator" block is a hand-typed
   `andara-server &` and `kill -9 %1`, a §9 defect in a demo. `AW-INF-032` (`make stack-recover`,
   draft) scripts the M2 gate on the running stack, with a hard dependency on this story. When it
   reaches `ready`, the block can point to it. `AW-INF-032` is also the first in-cluster caller of
   this story's `andara_recovery_*` series and `recovery.run` trace. Decide whether this story
   inherits that live observation as a Definition-of-done line, as CLAUDE.md §8 allows.

## For architecture: SRE observability review, 2026-10-02

5. **`RecoveryStateMismatch` can't fire as the story is written.**
   - `andara_recovery_state_hash_match` is set to `0` by a process that exits `2` at once. The
     restart sets nothing until its own recovery ends, so no scrape ever reads the `0`.
   - **In compose,** a linger like the projector's `haltLinger` fixes it. The server starts
     serving `/metrics` for 60 s before it exits, and compose scrapes a static target every 5 s.
     The gauge is never pre-seeded, so a normal boot never fires the alert.
   - **On the cluster, it doesn't.** The annotation scrape keeps only Ready pods, and a refused
     recovery is never Ready. There, `AndaraServerUnavailable` pages on the symptom.
     `RecoveryStateMismatch` needs a signal that doesn't depend on readiness, and that work is
     routed to PM (`docs/feedback/AW-INF-009-recovery-state-mismatch-cluster.md`). PM decided
     on 2026-10-02: `AW-INF-009` carries it in SPRINT-05, and you amend that story's contract and
     add the inherited Definition-of-done line. That file's "For architecture" section has both.
   - The story's §7 now carries an "SRE amendment, 2026-10-02" block. It requires:
     - the linger, behind a new key, `recovery.mismatch_linger`, which defaults to `0s` and which
       compose sets to `60s`. It applies on boot recovery only: exit `2`, and the
       restore-mismatch exit if accepted. It doesn't apply to the one-shot `recover --verify`;
     - `for: 0m` and `keep_firing_for: 15m` on the rule;
     - an §8 observation, compose-only, of the alert firing on a corrupt round and never in
       `ALERTS`, at neither `alertstate`, through a normal recovery.
   - **Confirm the new config key and the exit timing as contract changes, or rule otherwise.**
     With the linger on, a refused boot recovery takes 60 s to exit.
   - **AC-5 conflicts with the linger.** AC-5 says a server that exits `2` "never accepts a
     connection", but the linger serves `/metrics` and `/livez`. SRE proposes rewording AC-5 to
     "never binds `grpc.listen`", which is AC-10's wording and checkable by binding the port in a
     test. Operator HTTP would be allowed while `recovery.mismatch_linger` is above `0s` (raised
     again by Codex on #352).

   The amendment also lists what `AW-SRV-043` adds here if it joins `depends_on`:
   - a `restore` reason on `andara_recovery_failures_total`;
   - the span `restore.verify` under `recovery.run`;
   - `andara_restore_total{caller="recovery"|"verify"}`, wired and pre-seeded here.

   SRE proposes that `AW-SRV-043`'s restore-mismatch exit also sets the hash-match gauge to `0`
   and lingers under `recovery.mismatch_linger`. The operator's response is the same as for exit
   `2`: choose an older round.

   On item 4: `AW-INF-032`'s run is the right live observation for this story's series and trace,
   and SRE will record it in both §8 records.

6. **What increments `andara_acknowledged_commands_lost_total` after a restart?**
   - `recovery.md` says recovery increments it when an acknowledged Command is missing from the
     log. The acks, though, lived in the killed process's memory. The contract lists the counter
     (Metrics) and AC-8 says it stays `0`, but it never says what a fresh process compares the log
     against.
   - If nothing, the counter is `0` by construction after every restart, and both AC-8 and
     `AW-INF-032`'s AC-4 pass vacuously.
   - Name the source, for instance acked offsets recorded somewhere that outlives the process. Or
     say that the SLI is measured elsewhere, such as a client-side comparison in the test harness.

## Architecture: contract review, 2026-10-02

The story stays `ready`. It's amended in its body, and a dated block under Context lists every
change. Implementation hasn't started.

1. **Entity count.** AC-7 is measured at the sizing fixture: 25,000 Entities, a 600-tick tail, and
   `total` under 90 s. The old bound, `replay` under 5 s, contradicted `AW-SRV-019`'s measured
   ~65 ms per tick, which is about 39 s for that tail. `recovery-timing.json` also records the
   tail length, the Entity count, and peak RSS.
2. **`SeekAfter`.** Yes. It moves to `server/tickloop` and the projector and recovery share it.
   `Recover` streams boundaries and never loads the topic, which closes PR #32's memory question.
   AC-12 bounds history's cost, using `AW-SRV-019` AC-6's 24 h fixture.
3. **`AW-SRV-043`.** It's in `depends_on`. Its refusal is exit `6`, AC-13. The incomplete-round
   exit moves from `5` to `7`, because `AW-SRV-026` holds `5`.
4. **The operator test** is `make stack-recover`. This story's §8 cites `AW-INF-032`'s run, as an
   added Definition-of-done line.
5. **SRE's amendment, accepted:**
   - The key is `recovery.mismatch_linger`, default `0s`. It covers boot recovery's exits `2` and
     `6`, and never `recover --verify`.
   - AC-5 is reworded to "never binds `grpc.listen`". AC-14 states the linger, including a signal
     ending it.
   - The gauge is registered on first set, which is SRE's Test-plan line.
   - Exit `6` also sets the gauge to `0`.
   - The compose-only §8 framing holds. "Fires on the cluster" is `AW-INF-009`'s, inherited.
   - On SRE's question of whether the deferral rule fits: it does. The rule is in the file, and
     the evaluator that can run its cluster clause, Grafana Cloud's ruler, arrives with
     `AW-INF-009`. That's "no caller yet". `AW-INF-009` adds a clause to the same rule, not a new
     alert. See that story's feedback file.
6. **`andara_acknowledged_commands_lost_total` is withdrawn.**
   - The ack is held by the client, and a restarted process has nothing to compare with the log.
     The counter would be `0` by construction.
   - AC-8 now asserts the RPO in the kill-and-recover test. The test's clients record each
     `SubmitResponse` partition and offset, then check the log after recovery.
   - In `make stack-recover`, AC-5's tail move is the RPO evidence.
   - No server-side RPO SLI replaces it. The production guarantee is the three broker settings in
     `recovery.md`, which `AW-INF-005` asserts against running brokers.

### For SRE
- **`docs/runbooks/server-crashlooping.md` line 59** says an incomplete round with
  `recovery.require_snapshot=true` is exit `5`. It's now `7`, and `5` is `AW-SRV-026`'s lost
  boundary. Please fix it with `AW-SRV-026`'s runbook paragraph (its feedback file, item 3).
- **`docs/specs/slo/recovery.md`'s RPO SLI** names `andara_acknowledged_commands_lost_total` as
  "incremented by the recovery path". That counter is withdrawn. Please restate the SLI:
  - asserted by the clients that hold acks (`AW-SRV-007` AC-8, `AW-INF-032` AC-5);
  - guaranteed in production by the three broker settings;
  - with no server-side counter.
- **The linger's deploy half** is unchanged from your amendment: compose `60s`, the chart `0s` and
  values schema, and the rule's `for`/`keep_firing_for`, in your §8 ops commit.

### For PM
- **Phase 1's 60 s RTO.** At ~65 ms per tick, a 600-tick tail replays in about 39 s. That fits
  M2's 120 s. Once restart is counted, it leaves almost nothing of 60 s. Before Phase 1's exit, a
  story needs to either lower replay cost or shorten `snapshot.interval`, and the second costs
  stall budget. It's not SPRINT-04's.
- **`AW-INF-005` AC-3 and its metrics list** use the withdrawn counter. The SPRINT-05 split
  (`docs/feedback/AW-INF-005-007-split.md`) should replace that with a check at the offset the ack
  named, which the AC already reads.

## Architecture: the pre-PR review's changes, 2026-10-02

Same PR as the contract review above.
- **The hash mismatch moves from exit `2` to `8`.**
  - Go exits `2` on an unrecovered panic or a runtime fatal error. On the cluster, the
    last-terminated exit code is the only signal that outlives the process, so `2` would have paged
    `RecoveryStateMismatch` on every panic.
  - `2` is now never assigned. `recover --verify` exits `8` on a mismatch.
  - Where this file and the SRE amendment say recovery "exit `2`", read `8`.
- **Exit `6` also covers content refusals:** a `content_digest` mismatch, or a round Zone the
  content doesn't have. It logs `reason=content`. `VerifyOutcome` gains `CONTENT_MISMATCH = 5`.
- **`snapshot verify`** follows `AW-CLI-001`'s exits. A mismatch and a refusal are both `1`, and
  `outcome` or the error code tells them apart.
- **The Test plan:**
  - The `prng_state` flip now asserts exit `6`.
  - A rewritten tail `TickCompleted.state_hash` asserts exit `8`.
  - The AC-7 run proves its tail is 600 ticks from `tail_ticks`.

### For SRE (adds to the list above)
- **`server-crashlooping.md`'s exit table** gains rows for `6` (restore, seed or content mismatch:
  `recovery-state-mismatch.md`), `7` (incomplete round with `require_snapshot`), and `8` (hash
  mismatch). Its row for `2` becomes "a Go panic or runtime fatal error: read the stack trace", and
  the line-59 fix stands.
- **`server-unavailable.md`** lines 43 and 62 cite recovery's exit `2`, which is now `8`.
- **AW-INF-009** now depends on `AW-SRV-007`, and its AC-5 needs a `make` target that makes `dev`
  exit `6` on a corrupted round. It's yours to name when you build it.

## SRE: a correction to the amendment's compose premise (2026-10-05)

For architecture. Item 5's amendment, copied into the story's "SRE amendment, 2026-10-02", says "Compose has no
restart policy, so after the 60 s linger the target goes stale". That predates `AW-SRV-026`, which gave the
compose server `restart: on-failure` (`deploy/compose/docker-compose.yaml`). A refused recovery therefore
loops on compose: each cycle recovers, mismatches, lingers 60 s and exits, and its target is stale between
lingers. `keep_firing_for: 15m` is still right, but for a different reason: it bridges those gaps, and holds
the page for 15 minutes after the loop is stopped. The rule's comment and `recovery-state-mismatch.md` say so.
The story's amendment is yours to correct; nothing in its ACs changes.

## SRE: the corrupt-round run is a seed mismatch, and the alert was observed firing (2026-10-05)

For architecture, from the §8 ops commit. `make stack-recover-mismatch` is the target the story left to SRE.
- **What it does.** It restarts the compose server on `ANDARA_SIM_SEED=1`, so recovery refuses the newest round
  with exit `6`, `reason=seed`. It then checks the linger (the gauge reads `0`, `/livez` 200, `/readyz` not, the
  error line), `RecoveryStateMismatch` firing in the local Prometheus, the exit (docker's own die event: `6`),
  and, with the restart loop stopped, the target stale and the alert still firing. The server's own seed then
  recovers the same round with a matching State Hash. It ran green against the stack on `main` at `63efbf9`.
- **Deviation.** The story says "a deliberately corrupted round". A byte-flipped round that still verifies has to be
  re-signed, which needs the Go snapshot codec, and SRE doesn't write Go. A seed mismatch is exit `6`, which
  lingers and sets the gauge to `0` the same way a hash or content mismatch does (`boot.Lingers`), and it changes
  nothing in the snapshot store. If the §8 wants the byte-flipped variant too, that's an integration test in
  `server/recovery` that already flips `prng_state` and re-signs; its assertion would be the exit, not the alert.
- **Normal recovery.** `make stack-recover` now also checks that `RecoveryStateMismatch` is absent from `ALERTS`
  through its recovery, anchored on Prometheus having scraped the recovered `1` (AW-INF-032's record).
- **Both are in the `stack` workflow**, the normal one first, since the mismatch run leaves the alert in `ALERTS`.
- **Compose** gains `ANDARA_SIM_SEED: "${ANDARA_SIM_SEED:-0}"` (0 derives the seed, as before).
- **AC-6 was not met in CI.** `make test-integration` didn't list `./server/recovery/`, so the kill-and-recover
  tests skipped everywhere (`ANDARA_KAFKA_BROKERS` unset). It does now: 8.5 s against the stack's broker, all
  passing. The `stack` workflow's `make test-integration` step runs on every `server/**` change.
- **AC-7's job exists.** `.github/workflows/recovery-timing.yaml` runs `make recovery-timing` on a change to
  `server/sim`, `store`, `recovery`, `simtest` or `tickloop`, weekly, and by hand. It uploads
  `recovery-timing.json` and `make recovery-timing-summary` compares `replay` with the newest successful run on
  main in the job summary (`scripts/recovery_timing.py`, 20 unit tests). Locally against the stack's broker: `total`
  46.79 s (`replay` 46.48), tail 603 ticks from round 781, peak RSS 93 MB, 157 s for the whole test, and no
  `-race` (the bound is wall-clock). Its first CI run has no previous run to compare with.
- **Still SRE's, and open:** the §8 instrumentation check itself, on an `sre/aw-srv-007-verify` branch.

