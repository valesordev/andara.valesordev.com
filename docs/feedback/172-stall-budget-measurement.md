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

## Implementation's answer to the ruling (2026-10-03)

Built in PR #386 as ruled (both (a) and (b)).

- **Allocation bound.** At the sizing fixture: 74,119 allocations and 8,408,136 bytes per round, the
  same with and without `-race`, and identical across 10 quiet and loaded runs (go1.27.1-X:nodwarf5).
  That is below the 500,000 you asked about, so the 1.05x bound fails the per-Entity mutation: with
  one retained 64-byte allocation per Entity per round it reads 99,119 (+25,000) and fails 20 of 20
  under 48 busy processes with the CPU limit untouched. Recorded in `stallfactor_*_test.go`.
- **CPU limit.** In the full suite, 20 runs per build at 0–18 busy processes, load average 3.5–48 at
  start: race 62.9–96.6 ms (and 66–84 ms in the 20 acceptance runs after), non-race 16.4–21.3 ms. The
  worst reading seen on this change is 134.6 ms (race, `make test`), so the race factor is **14**
  (210 ms, 1.56x that) and the non-race factor is **4** (60 ms, 2.8x its 20-run worst; the race
  build's tail ran 1.39x its 20-run worst, which puts the non-race tail near 30 ms).
- **A second factor.** `stallFactor` also scales `TestContentSwapStaysInsideHalfTheTickBudget`, a
  separate wall-clock guard that this ruling doesn't cover. I left it at 2 and 8 and added
  `snapshotStallFactor` (4 and 14) for the snapshot guard, so the swap test's limit didn't move with
  it. That test still asserts wall-clock inside `make check`; the audit in
  `docs/specs/testing/README.md` is architecture's.
- **Acceptance.** 1: 20/20 (the box was at load average ~9.5 from other sessions, not quiet).
  2: 20/20 under 48 busy processes, worst 58 ms against 210 ms. 3: `go test -race -count=1
  ./server/...` 20/20 consecutive, load average 21–48 at start, and `make test` 5/5 under 48 busy
  processes. 4: 420 ms of mutator CPU fails 20/20 under load. 5: the per-Entity allocation fails
  20/20 on the count.
