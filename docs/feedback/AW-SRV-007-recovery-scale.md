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
     routed to PM (`docs/feedback/AW-INF-009-recovery-state-mismatch-cluster.md`).
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
     connection", but the linger serves `/metrics` and `/livez`. SRE proposes scoping AC-5 to
     Protocol connections on `grpc.listen`, as AC-10 already words it, and allowing operator HTTP
     while `recovery.mismatch_linger` is above `0s`.

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
