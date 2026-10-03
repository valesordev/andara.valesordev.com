<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Timing assertions — a regression guard measures CPU, and a service level is measured on a quiet machine

> **Status: rule, adopted 2026-10-03.** Written for #172, `TestSnapshotCopyStaysInsideTheStallBudget`
> failing `make check` on most runs at load average 4–11. It is binding on new tests from adoption.
> `docs/specs/testing/README.md` tracks the audit.

A **timing assertion** compares a measured duration with a limit. This repository has two uses for
one, and they need different measurements.

| Kind | Question | Measure | Where it runs |
|---|---|---|---|
| **Regression guard** | Did a change put work on a path that must stay cheap? | **CPU time** of the code under test | `make check`, in CI and locally |
| **Service-level measurement** | Does the system meet its number on this hardware? | **Wall-clock**, on a machine the measurement owns | A serial, non-gating target (benchmark or `make measure-*`) |

A wall-clock assertion inside `make check` is neither. It judges the machine's load rather than the
code, and a loaded machine is the normal state of a developer box that runs `kind` and other
sessions, and of a shared CI runner.

## 1. A guard measures the CPU time of its own thread

Pin the goroutine (`runtime.LockOSThread`), read `getrusage(RUSAGE_THREAD)` user + system time before
and after, and assert on the difference. Time the thread spent descheduled is not charged to it, and
descheduling is what other processes' load does.

Use the standard library (`syscall.Getrusage`, `syscall.RUSAGE_THREAD`); it needs no new `go.mod`
dependency.

The thread's CPU time doesn't include Go's background GC workers, which run on other threads. That's
right for a copy that spawns no goroutines (`SnapshotAll` spawns none), and it means the regression
injected in acceptance 3 must be mutator CPU (a busy loop), not allocation churn.

Keep what #172's test already does right: **worst of several rounds, not the mean.** A stall is felt
when it happens, so the worst round is the one that matters.

Build tags: `linux` takes the CPU-time path. Other platforms fall back to wall-clock with the same
limit, and say so in the test's log line. They aren't in CI, and a developer who hits the fallback
under load reruns the test alone.

**Not retries.** A guard that reruns until it passes asserts that *some* round passed, and it will
pass the regression it exists to catch.

## 2. The limit stays a multiple of the budget, and the measurement justifies the factor

`limit = stallFactor × snapshot.max_stall_ms` (15 ms) stays: 30 ms without `-race` and 120 ms with it
(`stallFactor` 2 and 8, in `server/simtest/stallfactor_*_test.go`). `AW-SRV-006` measured the copy at
25,000 Entities on 2026-09-22: 7.4–8.7 ms uncontended and 34.7 ms under `-race`. So each limit is
about 3.5× the quiet figure, and the factor's comment says so, with the date and machine.

Two comments are stale. `stallfactor_norace_test.go` pairs 10,000-Entity timings (2.7 ms, 4.8 ms)
with a 5 ms budget. The race file's 2.7 ms and 12.6 ms are 10,000-Entity figures too. The "24.7 ms
against a 5 ms budget" history in the race file and above the test is true as written, and stays.

The guard exists to catch a structural change, such as an encode or a hash moving back inside the
tick (24.7 ms against a 5 ms budget, once), and a copy that starts walking topology. That is far
outside any limit this measurement yields. It doesn't replace the service-level number. The shipped
SLI is the server's own `andara_snapshot_tick_stall_seconds`, wall-clock, in the tick.

## 3. A service-level measurement runs where it can be trusted

`BenchmarkSnapshotAllAtSizingScale` (`server/simtest/sizing_test.go`) is the wall-clock number for the
copy. `AW-SRV-006` has measured 25,000-Entity figures from 2026-09-22 (above), and the extrapolation
held. The benchmark is how that measurement is repeated: serially, on a quiet machine, recorded
beside those figures rather than asserted. If it comes in materially off them, `max_stall_ms` moves
as `AW-SRV-006`'s rule says (twice the uncontended figure), and that is an architecture decision.

## 4. Why not "run it alone"

Running the test in its own `go test` invocation, after the parallel packages, removes the load the
suite makes. It doesn't remove the load the machine already has, which is #172's actual cause: the
reported failures came at load average 4–11 from other sessions and the local `kind` clusters, and
the first one from a clean `origin/main` worktree. Serializing also costs a second package build in
every `make check`, and it leaves a test in `make check` that fails whenever the box is busy. It's the
right move for a *measurement* (§3), and the wrong one for a guard.

## Acceptance for the change

Implementation's, in one PR (SPRINT-04 item 6). Each is a command whose output goes in the PR:

1. **Quiet:** `go test -race -count=20 -run TestSnapshotCopyStaysInsideTheStallBudget ./server/simtest`
   passes 20 of 20, and its log line prints the CPU time and the limit.
2. **Loaded:** with `2 × nproc` CPU-bound processes running, the same command passes 20 of 20. This
   is the case the test fails today.
3. **Still catches:** with 150 ms of CPU work added to `SnapshotAll` in a scratch worktree (never
   committed), the same loaded run fails 20 of 20.
4. **Suite:** `make test` under the same load passes 5 of 5.
5. The stale comments in `stallfactor_norace_test.go` are corrected as §2 says, and the quiet-machine
   CPU time (per build) is recorded beside each factor, with the date.

If 2 doesn't hold, CPU time isn't enough on that box (memory bandwidth and SMT contention are
charged to the thread), and the guard adds an allocation bound (`testing.AllocsPerRun`): report back in
`docs/feedback/172-stall-budget-measurement.md` rather than loosening the limit.

## Revisit when

- CI moves to runners where `RUSAGE_THREAD` isn't available.
- A second timing guard exists. Two is the point to ask whether `internal/` wants a shared
  `cputime` helper; one is not.
