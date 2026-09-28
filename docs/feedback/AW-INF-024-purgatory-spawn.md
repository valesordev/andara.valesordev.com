# AW-INF-024: every environment spawns in Purgatory

Story: `AW-INF-024` (`draft`).

## For architecture: SRE observability review, 2026-09-28

No change. A values and script change. AC-5's boot failure uses `AW-SRV-014`'s existing `error`
line and exit code, and that's the only signal the story needs.
