# AW-SRV-011: the first `look` can reach the server before the stream attaches (#116, #117)

Story: `AW-SRV-011` (Event egress, `done`). Related: `AW-CLI-007` (`done`), whose verification row 4
records `Subscribe` before `Submit look`. Raised: 2026-10-03, PM, planning #116 and #117 into
SPRINT-04 (implementation item 13; Brian, 2026-10-03).

## The race
`TestPlay_SelectsBeforeSubscribe` has failed CI on #107 and #108, two PRs that don't touch the CLI
or the gateway. The issues describe its two halves:
- **The CLI (#117).** `admin/cli/play.go` sends its first `look` as soon as `Subscribe` returns. But
  connect-go's `CallServerStream` returns before the response headers arrive, despite the comment in
  `play.go`.
- **The gateway (#116).** `server/gateway/game.go` flushes the headers (`stream.Send(nil)`) and then
  calls `Egress.Subscribe`, a single blocking call. Inside it, `session()`, `attach` and `run` happen
  without returning control. So even a client that waits for headers can send a `look` before the
  stream attaches. On a first Subscribe (`last_event_id` 0), the cursor starts at the ring's end
  at `attach` (`history.go`), and an Event published before then is never sent.

## For architecture
The outcome SPRINT-04 wants: **a client that waits for the Subscribe response's headers can't send a
command whose Events its stream then misses.** In other words, headers mean the stream is attached,
the point the `Streams` gauge counts.

The gateway half changes `AW-SRV-011`'s Egress seam (`type Egress interface { Subscribe(...) error }`,
including `HoldingEgress` and the test fakes). Either the seam splits into register and run, or the
egress flushes the headers itself after `attach`. Rule which, or rule that the CLI half alone is enough
and record why. Implementation's item 13 waits on this. #116 offers a third option, documenting that
the ordering holds only per tick. That would change `AW-CLI-007`'s verified order, so it's a contract
change too.
