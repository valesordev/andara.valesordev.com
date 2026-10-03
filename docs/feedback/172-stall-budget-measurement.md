# #172: how the snapshot stall-budget test measures

Ruling: `docs/specs/testing/timing-assertions.md` (architecture, 2026-10-03). The guard measures
**CPU time of its own thread**, not wall-clock, and the wall-clock number moves to
`BenchmarkSnapshotAllAtSizingScale`, run serially and recorded. "Run it alone" was rejected: the
failures came from load the suite doesn't make (other sessions, the local `kind` clusters).

## For implementation

SPRINT-04 item 6. The change is `server/simtest` only: a `linux` CPU-time helper
(`runtime.LockOSThread` + `getrusage(RUSAGE_THREAD)`), a wall-clock fallback elsewhere, and the stale
`stallfactor_*_test.go` comments corrected. The five acceptance commands are at the foot of the
ruling. If its command 2 (loaded, 20 of 20) fails, report here rather than loosening the limit.

## For SRE

Two questions, neither blocking implementation:
1. Does the `check` job's runner allow `getrusage(RUSAGE_THREAD)`? It's plain Linux, so I expect so.
2. Should the wall-clock benchmark have a target of its own (for example `make measure-snapshot`,
   beside `measure-tick`), so `AW-SRV-006`'s extrapolated 25,000-Entity timings get replaced by a
   recorded measurement? If so, it's an `lane: sre` story for PM to groom. I haven't written one.
