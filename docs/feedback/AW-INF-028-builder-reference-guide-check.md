# AW-INF-028: make builder-reference and make guide-check

Story: `AW-INF-028` (`draft`).

## For architecture: SRE observability review, 2026-10-02

No change. "None" holds: `make` targets in CI with no service. They report through exit codes and
their `builder-reference:`/`guide-check:` lines. No metrics, traces, or alerts apply.

## Architecture: contract review, 2026-10-02

Moved to `ready`.
- **Open question 1 is resolved:** `reference.md` is generated. It's in `_repo.md`'s list and in
  CLAUDE.md §2. The DoD line waiting on it is removed.
- **AC-9** now uses a fixture for the empty-guide case. The real guide exists on `main`, so the
  merging PR runs `guide-check` on it. Any finding comes here, for architecture to fix in
  `docs/builders/`.
- The 07-reference removal stays architecture's, at this story's §8.

**Amended after pre-PR review:** AC-9 is one assertion. `guide-check` exits `0` on `main`'s guide
at merge. A finding comes here, and this story's PR waits on architecture's fix.
