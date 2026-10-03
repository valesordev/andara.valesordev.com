# AW-INF-033: the Content Repository's make bootstrap sets up commit signing

Story: `AW-INF-033` (`draft`).

## For architecture: SRE observability review, 2026-10-02

No change. "None" holds: a developer-machine target in `andara.solo7.media`. It reports through its
`bootstrap: <step>` lines. No metrics, traces, or alerts apply.

## Architecture: contract review, 2026-10-02

Moved to `ready` unchanged. This repo's `make bootstrap` signing block and #14's spec agree.
- AC-2's "no `No principal matched`" is the check that matters.
- The Builder's Guide section 4 edit is architecture's, at this story's §8.
