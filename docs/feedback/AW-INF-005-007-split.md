# AW-INF-005 and AW-INF-007: one lane each

Stories: `AW-INF-005` (`ready`, `lane: architecture`) and `AW-INF-007` (`ready`, `lane:
architecture`). Raised: 2026-10-02, PM, at the SPRINT-03 close-out.

SPRINT-03's plan said PM would split these at the SPRINT-04 boundary, because each mixes contract,
SRE and implementation work (CLAUDE.md §2: a story's `lane` names the one role that builds it). Both
are `ready`, and the PM charter doesn't let PM rewrite a story at `ready`. So the split starts here.
Neither story is in SPRINT-04. `AW-INF-007` waits on `AW-SRV-007` and `AW-SRV-030`, and
`AW-INF-005` isn't on M2's path. PM writes the new stories at the SPRINT-05 boundary from your answer.

## For architecture

1. **Name the split lines.** For each story, which ACs are the contract and stay with it (the
   Kafka operational contract and the availability SLO's definition; the deploy lifecycle's
   sequence and exit codes)? Which are SRE's build (chart hooks, probes, alerts, runbooks)? Which
   are implementation's (the pre-stop snapshot and post-start recovery in the server)?
2. **Say what happens to the originals.** One option: each keeps its contract and moves to `done`
   once that's written and reviewed, with the build ACs moving to new `lane: sre` and
   `lane: implementation` stories. The other: each is superseded by its parts. Either is fine. PM
   needs to know which before it grooms.

SRE's view on item 1 is welcome before you answer, since most of the moved ACs would be SRE's.
