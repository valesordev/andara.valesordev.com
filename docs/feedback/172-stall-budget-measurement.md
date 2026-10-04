# #172: how the snapshot stall-budget test measures

Ruling: `docs/specs/testing/timing-assertions.md` (architecture, 2026-10-03). The guard measures
**CPU time of its own thread**, not wall-clock, and the wall-clock number moves to
`BenchmarkSnapshotAllAtSizingScale`, run serially and recorded. "Run it alone" was rejected: the
failures came from load the suite doesn't make (other sessions, the local `kind` clusters).

## For implementation

SPRINT-04 item 6. The change is `server/simtest` only: a `linux` CPU-time helper
(`runtime.LockOSThread` + `getrusage(RUSAGE_THREAD)`), a wall-clock fallback elsewhere, and the stale
`stallfactor_*_test.go` comments corrected. The six acceptance commands are at the foot of the
ruling, and command 3 (the full suite, 20 of 20) is the binding one. If it fails, report here rather
than loosening the limit.

## For SRE

Two questions, neither blocking implementation:
1. Does the `check` job's runner allow `getrusage(RUSAGE_THREAD)`? It's plain Linux, so I expect so.
2. Should the wall-clock benchmark have a target of its own (for example `make measure-snapshot`,
   beside `measure-tick`), so `AW-SRV-006`'s 2026-09-22 25,000-Entity figures can be repeated and
   recorded? If so, it's a `lane: sre` story for PM to groom. I haven't written one.

## For implementation: architecture's reply to PR #386's measurement (2026-10-03)

Implementation reported that thread CPU time reads 2.5–3× higher inside the full `-race` suite (36–51 ms
alone, 122.6 and 134.6 ms in the suite against the 120 ms limit), and asked whether to add an allocation
bound, raise the race factor, or something else. **Both, and neither alone.** `timing-assertions.md` §2
now says:
1. **The CPU limit comes from the worst in-suite reading**, at least 1.5× it, over at least 20 full-suite
   runs at varied load (a race factor of about 13–14 or more). It moves only by an architecture ruling,
   and only when the observed allocation count at that commit is within 1.0× of its recorded value.
2. **The structural gate is an allocation bound:** the count at most 1.05× its recorded value and the
   bytes at most 1.25×, the minimum over the rounds, recorded with the Go version and the build tag.
   CPU time is a tripwire for work that doesn't allocate.
3. **Acceptance 3 (the suite, 20 of 20 and `make test` 5 of 5) is binding**, and 4 and 5 are the two
   mutations: twice the limit in mutator CPU, and one 64-byte allocation per Entity per round (a single
   large buffer would pass the bound and prove nothing).
4. The quiet figures are recorded as low-load, as Codex's P2 on #386 asked.

The PR is mergeable without this, as you said. Take it into the same PR if it fits, or as its own.
