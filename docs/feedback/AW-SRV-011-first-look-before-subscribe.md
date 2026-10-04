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

## Architecture: the ruling (2026-10-03)

**Option 3, in-band: every `Subscribe` stream's first frame is `Attached`, written by the egress after
it positions the stream's cursor.** Options 1 and 2 aren't taken, and option 4 is rejected.

**What a client may rely on.**
- **Before the first frame: nothing.** The response headers carry no guarantee. They aren't a signal that
  the stream is open, and `play.go`'s and `game.go`'s comments saying otherwise are wrong.
- **After `Attached`:** every Event the Session perceives from the cursor on is delivered, including the
  Events a Command submitted after the frame arrived causes. The cursor is set before the frame is
  written, so a Command submitted after the frame enters the log after the cursor and its Events are past it.
  This holds on a first Subscribe (`last_event_id` 0) as on a resume. **It lasts until a `Rebind`** (the
  Session's own bind or unbind, `egress.Rebind`, `AW-SRV-011`), which resets the history and re-bases the cursor and sends no
  new `Attached`. `play` is safe, since `SelectCharacter` binds before `Subscribe`. A client that selects a
  Character after subscribing can't rely on the Events that bind causes.
- **Order of frames:** `Attached`, then `Resync` when the resume can't hold, then Heartbeats and Events.
  `Attached{cursor_event_id}` is the **newest Event ID retained for the Session** (`history.newest`, `0` if
  none), or `last_event_id` when a resume holds. On a resume that can't hold it is the newest retained
  ID, and the `Resync` that follows is read against it. The first Event the stream delivers has a greater
  ID. (It is an Event ID, not the history's sequence number.)

**Why this one.**
- **Not option 4.** Guaranteeing order only per tick, and moving the test, hides a real loss: on a first
  Subscribe an Event published before attach is never sent, so a player's first `RoomDescribed` could be
  dropped on a faster path (an in-process source, a fast tick).
- **Not option 1.** Splitting the seam into register and run changes the signature of a `done` story's
  seam, and `AW-SRV-030`'s event-topic egress is a second implementer who'd pay for it. It still gives
  the client only the headers as its signal.
- **Not option 2.** Having the egress flush the headers after attach keeps the signature but makes
  every implementer flush, needs a headers-only send on `Sender`, and still leaves the signal as HTTP
  header timing. connect-go's `CallServerStream` returns before the headers arrive (#117), and the same
  handler serves Connect, gRPC and gRPC-Web (ADR-0003), so the open signal can't be one transport's.
- **In-band** is transport-independent, adds no seam change (the egress already writes `Heartbeat` and
  `Resync` from inside `Subscribe`), and gives the test a deterministic order.

**The wire.** `Attached attached = 23` in `EventEnvelope.payload`, with `message Attached { uint64
cursor_event_id = 1; }`. Event ID 0, no `EventType`, like `Heartbeat` and `Resync`. It's additive, and
`make proto-check` shows no breaking change. `gen/` is regenerated.

**The consequence we won't like.** A client built against this waits for `Attached`, so against a server
without it `play` would wait forever. It waits at most `--timeout` (30 s) and then fails with exit `4`,
`error.code` `timeout` (`AW-CLI-001`'s timeout, not its exit `3`). It never proceeds silently, and
`streamLoop` doesn't retry it, with or without `--reconnect`: a server that doesn't send `Attached` won't
start. Old clients on a new server print "something happened here that this client cannot describe"
for `Attached`, which is accepted. The CLI and the server ship together in this repo, so the skew window
is one deploy.

## For implementation

SPRINT-04 item 13. Server and CLI, in one PR if it fits.
1. **Egress:** write `Attached` from `Subscribe` after `attach` positions the cursor, before `Resync`,
   Heartbeats and Events, on every stream (first Subscribe, resume that holds, resume that can't), exactly
   one per stream, Event ID 0, leaving `lastSent` and `lastEvent` untouched. `attach` reads the cursor
   under the session lock, as it does today: after a `reset()` `newest` is 0 while the floor is above 0,
   and the first Event delivered still has a greater ID only if that holds. Add `TypeAttached` and the
   pre-created `type="attached"` series on `andara_stream_events_sent_total` (`metrics.go`), and the row in
   `server/README.md`. Pin the ordering in `egress_test.go`.
2. **Existing readers.** Every consumer that assumes the first frame is an Event, or counts frames,
   changes. Name them all in the PR: the `next()` and `ids(...)` helpers in `server/egress/egress_test.go`,
   `server/egress/gateway_test.go` and `server/egress/linkdead_test.go`, `cmd/andara-server/linkdead_test.go` (`len(got) < 4`) and
   `m1_test.go`, `internal/smoke/m1_test.go` and `soak_test.go`, and the scripts that read the stream:
   `scripts/stack_play.sh`, `scripts/stack_linkdead.sh`, `scripts/stream_soak.sh`. One Attached-first read
   helper is the place that changes.
3. **Gateway:** `game.go`'s headers-only `stream.Send(nil)` may stay or go. Fix its comment either way.
4. **CLI:** `play.go` `stream()` waits for `Attached` before its first `look`, and fixes its comment.
   `Receive` isn't context-bounded per call, so a timer cancels the stream's context at `--timeout`, and
   the failure is exit `4`, `error.code` `timeout`. `stream()` records that the timer fired, since
   cancelling the context otherwise ends `Receive` with `CodeCanceled`, and `streamLoop` gains a case before
   its connection-lost path that sends `AppError{ExitTimeout, CodeTimeout}` to `p.fatal` and doesn't retry
   (today it would go to `ExitConnect`, or reconnect under `--reconnect`). `render.go`
   renders `Attached` as nothing in human output, `--output json` carries it, and `admin/README.md` (line 417) and
   `AW-CLI-004` (line 168) have the `jq` filter, which becomes `select(.heartbeat == null and .attached == null)`. The fake Egress
   in `admin/cli/play_test.go` writes `Attached` after it records `Subscribe`, so
   `TestPlay_SelectsBeforeSubscribe` asserts `Subscribe` < `Attached` < `Submit:look` deterministically.
5. **Tests, each Given/When/Then:**
   - **Given** a first Subscribe with `last_event_id` 0 **when** a Command is submitted after `Attached`
     arrives **then** its Event is delivered. The test hook for the `Attached` write waits until the
     history holds the Event, or the Hub has drained, before it returns, so the test doesn't depend on the
     pump's timing. The mutation is "move the `Attached` write above `attach`", run at `-count=50`.
   - **Given** a resume that holds **when** the stream opens **then** the frames are `Attached`
     (`cursor_event_id` = `last_event_id`), then Events.
   - **Given** a resume that can't hold **then** the frames are `Attached` (the newest retained ID), then
     `Resync`.
   - **Given** a stream **then** it carries exactly one `Attached`, with Event ID 0, and a client's resume
     point is unchanged by it.
   - **Given** a server that never sends `Attached` **when** `play` runs, with and without `--reconnect`
     **then** it exits `4` `timeout` at `--timeout`, sends no `look`, and does not retry.
6. `AW-SRV-030`'s egress inherits the AC when it's next touched; nothing to do here.

## For SRE

One new label value, `type="attached"` on `andara_stream_events_sent_total` (bounded, pre-created, no consumer in `deploy/`). No new instrument, and `andara_stream_resyncs_total` and the rest are unchanged.
