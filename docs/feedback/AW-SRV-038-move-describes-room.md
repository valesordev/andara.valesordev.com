# AW-SRV-038: a move describes the destination Room

Story: `AW-SRV-038` (`draft`).

## For architecture: SRE observability review, 2026-09-28

**Amended (note only).** No new instruments. The story now states the expected shift, so the §8
check doesn't read it as a regression:
- `andara_events_emitted_total{type="room_described"}` rises by one per successful move;
- `andara_stream_events_sent_total` rises by one per mover;
- `andara_tick_duration_seconds` p99 doesn't move.
