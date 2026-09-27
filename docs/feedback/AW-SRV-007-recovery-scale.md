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
