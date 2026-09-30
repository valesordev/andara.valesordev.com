# AW-SRV-038: a move describes the destination Room

Story: `AW-SRV-038` (`draft`).

## For architecture: SRE observability review, 2026-09-28

**Amended (note only).** No new instruments. The story now states the expected shift, so the §8
check doesn't read it as a regression:
- `andara_events_emitted_total{type="room_described"}` rises by one per successful move;
- `andara_stream_events_sent_total` rises by one per mover;
- `andara_tick_duration_seconds` p99 doesn't move.

## For SRE (contract review, 2026-09-28)

`scripts/stack_play.sh` passes unchanged after this story, because its walk assertion searches
forward. Its comment "A move describes no Room, so the second one is the answer to the `look`
after it" goes stale when this story merges. Updating it is yours, and so is tightening the walk
to find `Town Hall` before the `look`, if you want the transcript to prove it.

## Implementation, 2026-09-30: built

Built on `impl/aw-srv-038-move-describes`. The story is at `review`, and the record is in the story.

### For SRE
- **AC-6 needs `make stack-play` on a stack built from this branch.** The shared stack runs a
  server built from your checkout, so I haven't run it. `internal/smoke`'s `TestLive_M1Gate` and
  `TestLive_Goto` read the stream by Event type and skip what they don't ask for, so they don't
  depend on the extra description. The in-process tests show both would pass.
- **`scripts/stack_play.sh`'s comment "A move describes no Room" is now stale**, as the story says.
  The script's forward search still passes.
- **The §8 before and after rates.** Each successful `move` adds one `room_described` to
  `andara_events_emitted_total`, and one to `andara_stream_events_sent_total` for the mover.
  Measuring that on `make stack-play`, with `andara_tick_duration_seconds` p99, is the §8 check's.

### Not changed, and why
`admin/cli/testdata/play/events.jsonl` is a catalogue of Event kinds for the renderer's golden, not
a recording of one mover's stream, and its Event IDs are pinned in `transcript.txt`. The renderer is
unchanged by this story, so it stays as it is.
