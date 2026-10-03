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
**The outcome, against the contract that already exists:** `play`'s automatic `look` gets its
answer, the Room, on its stream every time (`AW-CLI-007` AC-4), and the test asserts the order
deterministically (#117). No spec says what a Subscribe's response headers guarantee today. The only
statements are code comments, and both are wrong: `play.go` ("the headers are back, so the gateway
has the stream") and `game.go` ("a client's Subscribe call does not return until they arrive").

**Rule what a client may rely on before its first command.** The options the issues and the code
offer:
1. Split the Egress seam into register and run, so the gateway flushes headers after the stream
   attaches. This changes the seam's signature.
2. The egress flushes the headers itself after `attach`. The signature stays, but the seam's
   contract changes (every implementer flushes), and `SubscribeWith`'s `Sender` needs a headers-only
   send.
3. The gateway sends a first frame on subscribe, and the client waits for it (#117).
4. Ordering is guaranteed only per tick, and the test or the fake records `Subscribe` from the
   gateway's side (#116's options). This changes `AW-CLI-007`'s verified order.

A CLI-only fix (waiting for headers) narrows the window but doesn't close it. The flaking test uses
the real gateway with a fake Egress (`admin/cli/play_test.go`), which records `Subscribe` only when
`Egress.Subscribe` is entered, after the headers flush. So any ruling also needs the test or the fake
made deterministic. Implementation's item 13 waits on this ruling.
