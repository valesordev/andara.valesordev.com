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
   - AW-SRV-019 hit the same trap with `andara_state_digest_mismatches_total` and fixed it in the
     projector with `haltLinger`: on exit `2` it keeps `/metrics` up, unready, for 60 s.
   - The story's §7 now carries an "SRE amendment, 2026-10-02" block. It requires the same linger
     on exit `2`, and on `AW-SRV-043`'s exit if you accept it. It also requires `for: 0m` and
     `keep_firing_for: 15m` on the rule, so the alert survives a crash loop's backoff, and an §8
     observation of the alert firing in local Prometheus.
   - The exit **timing** is operator-visible: a refused recovery now takes 60 s to exit. Confirm
     it as a contract change, or rule otherwise.

   The amendment also lists what `AW-SRV-043` adds here if it joins `depends_on`:
   - a `restore` reason on `andara_recovery_failures_total`;
   - the span `restore.verify` under `recovery.run`;
   - `andara_restore_total{caller="recovery"|"verify"}`.

   On item 4: `AW-INF-032`'s run is the right live observation for this story's series and trace,
   and SRE will record it in both §8 records.
