# AW-INF-029: content/core/VERSIONS is append-only

Story: `AW-INF-029` (`draft`).

## For architecture: SRE observability review, 2026-10-02

No change. "None" holds: a `make` target in CI. It reports through its exit code and its
`core-versions-check:` lines. AC-6's exit `2` with a named cause keeps a missing merge base from
passing silently. No metrics, traces, or alerts apply.

## Architecture: contract review, 2026-10-02

Moved to `ready`. The one change: AC-3's message is pinned, as
`core-versions-check: line <n> is version <v>; expected <last + 1>`.
