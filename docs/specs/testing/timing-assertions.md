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

## 1. A guard measures CPU time, not wall-clock

**Code that does all its work on the calling goroutine** (the case #172 is) is measured on its own
thread. Pin the goroutine (`runtime.LockOSThread`), read `getrusage(RUSAGE_THREAD)` user + system time before
and after, and assert on the difference. Time the thread spent descheduled is not charged to it, and
descheduling is what other processes' load does.

Use the standard library (`syscall.Getrusage`, `syscall.RUSAGE_THREAD`); it needs no new `go.mod`
dependency.

**Code that hands work to other goroutines** isn't measured that way: the locked thread's time would
miss the workers', and arbitrarily expensive off-thread work could be added with the guard still
passing. Such a guard reads the **process's** CPU time (`syscall.RUSAGE_SELF`) instead. That still
excludes other processes' load, and it also counts any other test running in the package at the time,
so the guard isn't `t.Parallel()`. Go holds a package's parallel tests until its sequential tests
finish, so none overlaps it, but goroutines that earlier tests leaked are still counted. The test
says which of the two it uses and why, in its comment. A test can't tell the two cases apart by
looking, so the author states which one the code under test is.

The thread's CPU time doesn't include Go's background GC workers, which run on other threads. That's
right for a copy that spawns no goroutines (`SnapshotAll` spawns none), and it means the CPU
mutation in acceptance 4 must be mutator CPU (a busy loop), not allocation churn. Allocation churn is
gated by the allocation bound (§2), whose mutation is acceptance 5.

Process CPU does include background GC and runtime threads, so a process-CPU guard sees allocation
churn and carries more noise. Its limit comes from process-CPU measurements, not thread ones.

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

**Amended 2026-10-03, from implementation's measurement on PR #386.** A limit set from a quiet reading
isn't the limit the guard runs under. Inside the full `-race` suite, with its parallel package binaries,
the thread's CPU time reads 2.5–3× its low-load figure (36–51 ms alone at load 1.3–1.7, 134.6 ms at worst
in the suite at load 17), because cache, memory-bandwidth and SMT contention are charged to the thread.
So the rule is: **a CPU-time limit is at least 1.5× the worst reading inside the full suite**, over at
least 20 full-suite runs at varied load, and it is recorded beside the factor with the date, the load,
and the allocation count and bytes at that commit. **The factor changes only by an architecture ruling,
and only after the observed allocation count at that commit is within 1.0× of its recorded value**, so a re-measurement
on a tree whose copy has crept can't raise the limit and hide it. For the race build that comes to a factor of about 13–14 or more (134.6 ms × 1.5 ≈ 200 ms), and
implementation sets the figure from its own runs. The same measurement sets the non-race factor.
`make check` runs the guard in the suite, so the suite is the condition that counts.

Two comments are stale. `stallfactor_norace_test.go` pairs 10,000-Entity timings (2.7 ms, 4.8 ms)
with a 5 ms budget. The race file's 2.7 ms and 12.6 ms are 10,000-Entity figures too. The "24.7 ms
against a 5 ms budget" history in the race file and above the test is true as written, and stays.

The guard exists to catch a structural change, such as an encode or a hash moving back inside the
tick (24.7 ms against a 5 ms budget, once), and a copy that starts walking topology. A CPU limit that
loose catches only the gross cases, so **the structural gate is an allocation bound**, which load
can't move: the test asserts the allocation count (`testing.AllocsPerRun`, an integer average, called several times) and the bytes
allocated per round (`runtime`'s `TotalAlloc` before and after) at the sizing fixture. Both counters
are process-wide, so noise from a stray goroutine only adds, and the test takes the **minimum over
its calls and rounds**. **Allocations at most 1.05× the recorded value, bytes at most 1.25×.** The count is
deterministic, so a deeper clone that adds one allocation per Entity (+25,000 on a few hundred
thousand) fails it, and the bytes headroom is for toolchain drift. An encode, a hash buffer or a
deeper clone moving into the tick allocates, so it fails at once. The bound is recorded with the Go
version and the build tag (`-race` allocates differently). A failure prints the recorded Go version and
tag beside the observed ones and says "re-record with a note", so a toolchain drift is a visible
re-record and not a silent loosening. It would miss a streaming, non-allocating hash, which only the
CPU tripwire covers. It doesn't replace the service-level number. The shipped
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

1. **Low load:** `go test -race -count=20 -run TestSnapshotCopyStaysInsideTheStallBudget ./server/simtest`
   passes 20 of 20, and its log line prints the CPU time, the limit, the allocations and the bytes.
2. **Loaded:** with `2 × nproc` CPU-bound processes running, the same command passes 20 of 20.
3. **Suite:** `go test -race -count=1 ./server/...` passes 20 of 20 consecutive runs at varied load
   (load average from the box's idle to 17), and `make test` under the same load passes 5 of 5. This is
   the case `make check` runs, and it is the binding one.
4. **Still catches, on CPU:** with mutator CPU of twice the limit added to `SnapshotAll` in a scratch
   worktree (never committed), the loaded run fails 20 of 20.
5. **Still catches, on allocation:** with one 64-byte allocation kept per Entity per round in the same
   scratch worktree (about +25,000 allocations and +1.6 MB), the **allocation-count** bound fails, with the
   CPU limit untouched, 20 of 20. The log prints the recorded baselines. (A single large buffer, which
   adds one allocation and a few percent of the bytes, would pass and prove nothing.)
6. The stale comments in `stallfactor_norace_test.go` are corrected as §2 says, and the worst in-suite
   CPU time for each build, with the date, the load and the Go version, is recorded beside each factor,
   next to the allocation bound. The low-load alone figures are recorded as low-load, not idle.

If 3 still doesn't hold with the in-suite limit, report back in
`docs/feedback/172-stall-budget-measurement.md`. It means the allocation bound is the guard and the CPU
check is only a tripwire, and that is architecture's call to make, not a loosening to make quietly.

## Revisit when

- CI moves to runners where `RUSAGE_THREAD` isn't available.
- A second timing guard exists. Two is the point to ask whether `internal/` wants a shared
  `cputime` helper; one is not.
