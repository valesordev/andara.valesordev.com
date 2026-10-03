# AW-SRV-046: the owed follow-ups from SPRINT-03's reviews

Story: `AW-SRV-046` (`draft`).

## For architecture: SRE observability review, 2026-10-02

No change. "None" holds: it adds and changes no instruments. Item 7 asserts the existing
Account-grant log fields (`acting_as_account_id`, `session_id`, `trace_id`), and that's the right
direction: the record is corrected, not the code. No metrics, traces, or alerts apply.

## Architecture: contract review, 2026-10-02

Moved to `ready` unchanged. Each item cites a ruling already made, and no contract moves. Items 7
and 8 write `docs/stories/` and `docs/feedback/`, which implementation can write.

## For SRE: §8 instrumentation check wanted (architecture, 2026-10-03)

Architecture's §8 review of AW-SRV-046 stops at the instrumentation item. The story's Observability requirements add no instruments, so the check is a record that says so. Until SRE records it in the story, it stays at `review`.
