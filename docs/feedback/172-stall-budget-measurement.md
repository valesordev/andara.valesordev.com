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
   beside `measure-tick`), so `AW-SRV-006`'s 2026-09-22 25,000-Entity figures can be repeated and
   recorded? If so, it's a `lane: sre` story for PM to groom. I haven't written one.

## For architecture (implementation, 2026-10-03): thread CPU time doesn't hold in the full suite

PR #386 implements the ruling. Its acceptance 1–3 hold (quiet 20/20; with 48 busy processes 20/20,
worst 66.8 ms against 120 ms; with 150 ms of mutator CPU injected, 20/20 fail). The guard still
fails in the **full `-race` suite**, which is the case `make check` runs:

- `go test -race -count=1 ./server/...`, 8 runs at load average 1–17: **1 of 8** failed on
  `TestSnapshotCopyStaysInsideTheStallBudget`, 122.6 ms thread CPU against the 120 ms limit.
- `make check`'s `make test` failed on it once more: 134.6 ms (load average 17), and an earlier
  `make check` failed once in a test I did not capture, probably this one.
- Alone, at load average 1.3–1.7, the same build reads 36.1–50.7 ms, so under the suite's own
  parallel package binaries the thread's CPU time inflates 2.5–3x. The 48 busy loops don't do that;
  the suite does (cache and memory-bandwidth contention, SMT siblings). The ruling anticipated this:
  "memory bandwidth and SMT contention are charged to the thread."

So acceptance 4 ("`make test` under the same load passes 5 of 5") isn't met by the CPU-time
measurement alone, and I haven't loosened the limit. The ruling's own next step is "the guard adds
an allocation bound (`testing.AllocsPerRun`)". Its report-back clause is written for acceptance 2
(the loaded run), which held 20/20; the failure here is acceptance 4, but the same reasoning applies. That changes what the guard asserts, so it is
architecture's call, as is the alternative of a higher race-build factor from a new measurement
(the worst reading in the suite so far is 134.6 ms, about 9x the budget, against 8x today).

**Ask:** which of (a) an allocation bound beside the CPU-time check, (b) a different `stallFactor`
for the race build, from a measurement taken inside the suite, or (c) something else.

Also from PR #386's review (Codex, P2): the ruling's acceptance 5 asks for quiet-machine baselines.
They're recorded beside each factor, taken at load average 1.3–1.7, described as low-load readings
rather than an idle machine.
