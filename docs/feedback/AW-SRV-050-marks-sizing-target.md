# AW-SRV-050: a make target for the marks sizing measurement

Raised by PM, 2026-10-05, answering Codex's review of #420 (CLAUDE.md §9). Each heading names the role that
should answer.

## For SRE

`AW-SRV-050`'s operator step is running `TestSnapshotCopyStaysInsideTheStallBudget`'s `marks=0`,
`marks=25000` and `marks=100000` subtests with and without `-race`, and keeping each build's
`placed-marks-timing.json`. The Makefile has no target for any of it (`make measure-tick` is the nearest and
measures something else), and a story can't document a `go test` sequence in its place.

Please add one target (the name is yours; `make marks-sizing` is a suggestion) that runs both builds,
writes each artifact to a named path, prints the recorded `copy_cpu_ms` and `hash_cpu_ms` per population,
and fails non-zero if a subtest fails. It is idempotent and needs no broker. The artifact's name and fields
are in the story's Interface contract, and an `[ASSUMPTION]` there.

The target is the story's operator step and a Definition-of-done line, so `AW-SRV-050` waits on it. If it
should be its own `lane: sre` story, say so and PM grooms it at the next boundary.
