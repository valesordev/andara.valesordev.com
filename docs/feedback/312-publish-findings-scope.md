# #312: what the publish gate reports

Ruling: `docs/specs/content-language/v1/errors.md` §1 rule 10 (architecture, 2026-10-03). The cause of
the cascade was ordering: the gate treated the *incumbent's* Zone as the duplicate, dropped it, and
every Exit into it then failed. With the publisher's Zone losing, there is no cascade to filter. The
`admin.proto` comments are updated and `gen/` regenerated (comments only; no wire change).

## For implementation

SPRINT-04 item 8. Server side (`server/sim/build.go` input order, the gate's filtering and message,
the counter) and the CLI's printing. The tests, each Given/When/Then:

1. **Given** active pack `A` with Zone `town` (Rooms `plaza`, …) **when** pack `B` publishes a Zone
   `town` **then** the refusal carries exactly one finding: `duplicate_zone`, in `B`'s blob, with
   message `ZoneID town declared in pack B and in active pack A@N`. `content publish` prints it on
   `B`'s source (`z.aw:1:1` form) and exits `1`. `validation_failures_total{duplicate_zone}` is `1`,
   every other code is `0`, and the audit `findings_count` is `1`.
2. **Given** (1), with another Zone of `A`'s that has an Exit into `town.plaza` **then** it is still
   one finding.
3. **Given** (1), with another Zone of `B`'s that has an Exit into `town.plaza` **then** it is still
   one finding, and **given** `B` renames its `town` **then** that Exit's own finding, if any,
   appears.
4. **Given** `B`'s two files declaring the same Zone and a Room twice **then** `duplicate_zone` and
   `duplicate_room` are both reported, as today (rule 10.3).
5. **Given** `A` has a `missing_reverse_exit` warning (the dev fixture's `purgatory`) **when** `B`
   publishes a valid pack **then** `warnings` is empty and `content publish` prints no line from `A`.
6. **Given** `B` has its own `orphan_room` **then** that warning is still printed, on `B`'s source.
7. **Given** a new version of `B` that removes a Zone another pack `C` exits into **then** the
   publish is refused, the finding's message begins `in pack C:`, its `line` and `col` are `0`, and it
   isn't printed as `B`'s file.
8. Mutation check: with the incumbent-first ordering reverted, test 2 fails; with the file filter
   removed, test 5 fails.

On merge, `docs/builders/04-your-first-zone.md` loses its "ignore it" paragraph about the
`purgatory.json` warning. That edit is architecture's (a docs/builders path), so say so on the PR.

## For SRE

None. The Observability section's names and labels don't change; only what the counter counts.
`AW-INF-021`'s §8 record cites `unknown_room` 3 beside `duplicate_zone` 1 for the old behavior, and
that record stands as written.
