---
id: AW-CLI-004
title: andara-cli play — the text interface as a first-class protocol client
epic: EPIC-03
component: cli
type: feature
status: in-progress
size: M
depends_on: [AW-CLI-001, AW-SRV-005, AW-SRV-011, AW-SRV-031]
blocks: []
lane: implementation
risk: medium
---

## Context

ADR-0003 chose gRPC, and a human cannot telnet into a gRPC server. The Phase 1 Text Interface is
therefore `andara-cli play`: a real Protocol client that renders Events as prose in a terminal.

This is a better outcome than a second listener. There is exactly one protocol from day one, the CLI is
exercised by every play session rather than only by Operators, and the Phase 2 WebGL client becomes a
different renderer against a contract that has been in daily use for months.

The cost is real and named in ADR-0003: onboarding a playtester now means shipping them a binary. For a
closed launch (ADR-0006) that is acceptable.

**Resolved 2026-09-07 (Brian):** the Text Interface is **permanent, and it is an operator and
developer tool rather than a player product.** `andara-cli` is text-only for its whole life and will
never render anything; the rendered client is `CLT`'s job and always will be. That settles ADR-0003's
open question and it changes this story's centre of gravity: `play` is how a human drives the world
*and inspects the protocol while doing it* — seeing the Intents and Events going back and forth at a
technical level is a first-class feature here, not a debugging afterthought.

Two consequences worth stating, because they pull in opposite directions from the obvious reading:

- Polish that only serves players — prose quality, immersion, colour — is worth less than clarity
  about what the protocol did. A readable transcript still matters, because a human has to play
  through it for the Phase 1 exit criteria, but it is not the product.
- Protocol visibility is worth more than it looks. This is the only client that exists for months, so
  it is also the only way anyone sees a malformed Event, a missed `last_event_id`, or a Session that
  resynced without saying so.

M1's gate is this command working.

## User story

As a player, I want to run one command and be in the world, so that Andara is playable before any
pixel is rendered.

## Scope

### In scope
- `andara-cli play`: connect, `OpenSession`, `Subscribe`, and a read-eval loop submitting Intents.
- Event rendering: turning structured Events into readable prose in a terminal.
- Reconnect with `last_event_id`, and a clear resync when the resume window is exceeded.
- Local echo, line editing, history, and Ctrl-C semantics that do not surprise anyone.
- Clear presentation of the three states a player can be in: connected, world read-only, disconnected.
- `--output json` emitting the raw Event stream instead of prose, for debugging and for scripted tests.
- **Protocol visibility.** A mode that shows the Intents sent and the Events received alongside the
  prose — message name, Session correlation ID, `last_event_id`, and tick where the Event carries one.
  Toggleable during a session, not only at launch, because the moment you want it is after something
  looked wrong.

### Out of scope
- Authentication UX — `AW-SRV-008` owns the login flow. `--as` is **not** anonymous; see below.
- Any game logic. This client renders and submits; it decides nothing.
- Client-side prediction. The world is server-authoritative and this client shows what the server said,
  even when that is one tick behind.
- Colour themes, panes, status bars, and any full-screen TUI. A scrolling transcript is the whole
  presentation, permanently — not a placeholder for something richer.
- Rendering of any kind, ever. Settled above.

## Acceptance criteria

1. **Given** a running local stack **when** a player runs `andara-cli play` **then** it connects over
   TLS, opens a Session, subscribes, prints the current Room, and presents a prompt.
2. **Given** a connected player **when** they type `north` **then** the Intent is submitted, the ack is
   not printed as game output, and the resulting Events are rendered as prose when they arrive.
3. **Given** two players in the same Room **when** one moves **then** the other's terminal shows the
   departure without either having typed anything.
4. **Given** a Command rejected post-log **when** the `CommandRejected` Event arrives **then** the
   player sees the human-readable `Detail` and nothing about stages, offsets, or partitions.
5. **Given** a Command rejected pre-log **when** the RPC returns **then** the player sees the same class
   of message. A player must not be able to tell where in the pipeline a rejection happened.
6. **Given** the server enters read-only mode **when** the player submits **then** they see a clear
   statement that the world is not accepting commands, the session stays open, and Events keep arriving.
7. **Given** the connection drops **when** the client notices **then** it says so, retries with backoff,
   resumes with `last_event_id` on success, and prints nothing false in between.
8. **Given** a resume that exceeds the server's window **when** the server returns `resync_required`
   **then** the client resyncs and tells the player it may have missed events. It never silently
   presents a gapped transcript.
9. **Given** an idle world **when** heartbeats arrive **then** nothing is printed, and the client's
   connection indicator stays healthy.
10. **Given** `--output json` **when** the client runs **then** stdout carries only the Event stream as
    JSON, per the `AW-CLI-001` output contract, and all prose goes to stderr.
11. **Given** Ctrl-C **when** pressed **then** the client closes the Session cleanly and exits 0. A
    player quitting must not leave a linkdead Character behind.
12. **Given** a server whose Protocol version is outside the client's range **when** connecting **then**
    the client prints both ranges and exits 3, per the `AW-CLI-001` exit-code taxonomy.

## Interface contract

### Command

```
andara-cli play [--as <character-name>] [--reconnect] [--output human|json]
```

Global flags from `AW-CLI-001` apply. `--server-address`, `--config`, and `--timeout` behave as
everywhere else; `--timeout` bounds connection establishment, not the play session.

### Rendering contract

The renderer is a pure function from `Event` to lines. This matters more than it looks: it means the
rendering can be tested with golden files against a recorded Event stream, and it means the Phase 2
client is substituting a different pure function against the same input.

| Event | Rendered as |
|-------|-------------|
| `RoomDescribed` | title, description, exits, entities present |
| `CharacterArrived` / `CharacterLeft` | one line naming the Character and the direction |
| `CommandRejected` | the `Detail` string, verbatim |
| `Heartbeat` | nothing |
| `Resync` | a notice that events may have been missed |
| unknown type | a single generic line, never a crash |

The unknown-type row is a requirement, not politeness: a newer server will emit Event types this
client does not know, and ADR-0007's additive evolution only helps if readers actually tolerate what
they do not recognise.

### Exit codes

Per `AW-CLI-001`: `0` clean quit, `1` server-side failure, `2` usage, `3` connection or version
mismatch, `4` timeout.

## Data / state impact

Client-side only. History file under `$XDG_STATE_HOME/andara/history`, opt-out with `--no-history`,
and it never records anything typed at an authentication prompt once `AW-SRV-008` adds one.

No credentials are stored by this story. When `AW-SRV-008` lands, token storage is that story's
decision, not this one's improvisation.

## Observability requirements

Per `AW-CLI-001`, a CLI's observability is its output and its exit code.

### Metrics
None. Explicitly none — this is a short-lived interactive process.

### Logs
- Client diagnostics to stderr at `--log-level`, never mixed into game output. This separation is what
  makes AC-10 possible.
- Never log Event payloads at default level; a transcript is the player's, not the log's.

### Traces
- `cli.command` root span from `AW-CLI-001`, propagated in gRPC metadata so it parents the server's
  `session.lifetime`. One trace from keystroke to Event, which is the property that makes `AW-INF-002`'s
  acceptance criterion 6 demonstrable.

### Alerts
None.

## Test plan

- **Unit:** golden-file rendering over a recorded Event stream covering every row of the rendering
  table, including the unknown-type row with a synthetic future Event; exit-code mapping; the
  stdout/stderr split under `--output json`.
- **Integration:** against the local stack — the full M1 gate, scripted: connect, `look`, `north`,
  assert the transcript. Two concurrent clients asserting AC-3. Broker stop asserting AC-6. Server
  restart asserting reconnect and resync messaging.
- **Manual/operator:** this is M1's gate, executed by a human:
  ```
  make up
  andara-cli play
  > look
  > north
  > west
  ^C
  ```
  Expected: a room, a different room, a refusal that reads like prose, and a clean exit.

## Definition of done

CLAUDE.md §8, plus:
- The scripted M1 gate runs in CI against the local stack.
- The renderer is a pure function with golden-file coverage, so Phase 2 can replace it rather than
  reverse-engineer it.
- A player cannot tell from the output whether a rejection was pre-log or post-log.
- **Inherited from `AW-SRV-010` (Brian, 2026-09-19):** server-side rejection text is *system voice* —
  plain, out of fiction, says what happened and what to do, never why. `play` prints the server's
  message as given for every typed error (`INVALID_ARGUMENT`, `PERMISSION_DENIED`, `UNAVAILABLE`
  with `world_read_only` or `in_transit`, `RESOURCE_EXHAUSTED`) and switches on
  `ErrorInfo.reason` only for behavior (retry after `RetryInfo`, hold the prompt during
  `in_transit`), not for wording. Changing the voice is a design decision, not a client one.
- **Inherited from `AW-SRV-031` (2026-09-21, rewritten on the review of PR #38):** `play` always
  sends a `client_ref` — one fresh value per typed line — and switches on `ErrorInfo.reason` under
  `DEADLINE_EXCEEDED`: `produce_deadline` → retry with the *same* `client_ref` (the retry waits for
  the record's fate, bounded by the client's own patience) and the server answers it with the
  original outcome; `outcome_unknown` → stop; tell the player the World may or may not have taken
  the command and to `look`. The Events the Command causes carry the `client_ref`, so a client
  watching its stream learns the truth. A `client_ref` is never reused for a different line
  (`duplicate_client_ref` is a client bug). An empty `client_ref` is not deduplicated.

## As built (2026-09-21)

`admin/cli/play.go`, `playcmd.go`, `render.go`, `history.go`; the Game client in `client.go`;
`scripts/stack_play.sh` behind `make stack-play`, run by the `stack` workflow after `stack-smoke`.

- **Command:** `andara-cli play [--as <account-id>] [--reconnect] [--world] [--show-protocol]
  [--no-history] [--client-timeout <d>]`. `--as` takes an **account ID** — `act_as_account_id`
  (`AW-SRV-008` AC-10) — not a Character name: nothing binds a Character before `AW-SRV-014`, and
  acting-as is an account relation either way. `--reconnect` defaults **true** (AC-7 has no
  precondition); `--reconnect=false` makes a drop exit 3 with `error.code` `disconnected`, for
  scripts. `--world` carries `AW-SRV-011`'s opt-in flag. `--client-timeout` (default `--timeout`)
  bounds one `Submit`; `--timeout` bounds `OpenSession`; the session itself has no deadline — the
  Game client is built without `http.Client.Timeout` for that reason.
- **Client commands** are lines beginning with `/`, never sent: `/protocol [on|off]` toggles
  protocol visibility mid-session, `/help`, `/quit`. Protocol lines are indented and marked `»`
  (sent) / `«` (received) with message name, `session_id`, `client_ref`, `event_id`, tick, and for
  errors the code, `ErrorInfo.reason`, and the `stage`/`pre_log` metadata. Under `--output json`
  they go to stderr verbatim, unfiltered by `--log-level` — the player asked for them.
- **Rendering** is `renderEvent(*EventEnvelope) []string`, pure, golden-filed over
  `admin/cli/testdata/play/events.jsonl` → `transcript.txt` (every row of the table, plus
  `ZoneFaulted`/`SubscriberDropped`/`SimulationStopped`, plus a synthetic field-40 payload for the
  unknown row — built as bytes, because no Go type for it exists, which is the point).
- **Sequencing:** the client `look`s once the first `Subscribe` has its headers back (AC-1) and
  again on every `Resync` frame (AC-8); typed lines wait for that first Room. On a pipe rather
  than a terminal there is no prompt, and each line is held until the previous one's Events have
  arrived and the stream has been quiet 50 ms — bounded at 3 s — and end of input lingers the
  same, so a scripted transcript reads in the order it was typed. A person's lines are never held.
- **Refusals** print the server's message and nothing else, from `Submit` or from
  `CommandRejected` alike (AC-4, AC-5). Behavior switches on `ErrorInfo.reason` only:
  `world_read_only` / `in_transit` hold the prompt for `RetryInfo` (500 ms without one) and retry
  the same line up to three times before printing the message (AC-6); `produce_deadline` — or the
  client's own deadline — retries with the same `client_ref` up to four attempts; `outcome_unknown`
  stops and tells the player to `look`; `UNAUTHENTICATED` on `Submit` drops the line and hands the
  reconnect to the stream loop. `client_ref` is `<8 hex per process>-<n>`: fresh per typed line,
  never reused, unique across the Sessions one process opens.
- **Reconnect** (AC-7): the stream loop owns it. A stream that ends `UNAUTHENTICATED`,
  `UNAVAILABLE`, `CANCELED`, or without a Protocol answer is announced once (`-- Connection lost;
  reconnecting.`), then `OpenSession` is retried on a jittered 1 s → 15 s backoff — a refused
  connection is logged at `debug`, nothing false is printed — and `Subscribe` resumes from the
  last `event_id`. A Session dies with its connection until `AW-SRV-015`, so today every
  reconnect is a new Session and the resume is answered `Resync{no_history}`: announced, then
  `look`. `buffer_full` resubscribes on the same Session without the loss notice. A
  `PERMISSION_DENIED` stream end (revoked) is exit 1.
- **Exit codes** per `AW-CLI-001`: 0 on Ctrl-C, Ctrl-D, `/quit`, or end of input — `CloseSession`
  is sent on each (AC-11); 2 with `not_logged_in` when no credential is stored; 3 with
  `protocol_version` naming both ranges (AC-12, read from the `PreconditionFailure` detail, the
  message as fallback), `unauthenticated`, `connect_failed`, or `disconnected`; 4 when
  `OpenSession` times out.
- **Output** (AC-10): `--output json` writes every frame — heartbeats included; it is the raw
  stream — as one `protojson` object per line on stdout; notices and refusals are `AW-CLI-001`
  log lines on stderr (`info` connected, `warn` lost/resynced/refused).
- **Terminal:** when stdin and stdout are terminals, `x/term` raw mode with line editing, history,
  and asynchronous Events placed above the prompt; Ctrl-C and Ctrl-D at an empty line are the
  quit. History at `$XDG_STATE_HOME/andara/history`, 1000 lines, `0600`, `--no-history` opts out.
- **Trace:** `client.go`'s interceptor now injects the `cli.command` context on streaming calls
  too, so `Subscribe` is parented like `Submit`; the compose stack (trust-inbound on) shows one
  trace holding `OpenSession`, both `Submit`s, and `Subscribe`.

### Verification record (2026-09-21, compose stack, `main` at `fb4902e`)

- `make check` clean. `admin/cli` under `-race` ×5: `TestPlay_*` (transcript, JSON split,
  read-only hold, same-ref deadline retry, reconnect + resync against a server stopped and
  started again on the same address, `--reconnect=false`, `buffer_full` resubscribe, version
  mismatch, not logged in, protocol visibility, Session closed behind the client's back) over an
  in-process gateway with fake seams speaking the wire contract; `TestRender_*` goldens.
- **Live** (`make stack-play`, and by hand): `auth login` as the bootstrap operator; `play` opens
  a Session over TLS, subscribes, `look`/`north`/`west` are refused before the log with "you are
  not in the world" — printed as prose, nothing about stages or offsets — `frobnicate` with the
  parser's message; `--output json` carries only JSON on stdout with the prose as stderr log
  lines; `CloseSession` on quit, `andara_sessions_total{outcome="closed"}` +1,
  `andara_ingress_submits_total{rejected_authz}` +3 and `{rejected_parse}` +1 on the server;
  a pty-driven session with history recall and Ctrl-C / Ctrl-D exiting 0; `docker compose restart
  andara-server` mid-session → `server draining` on the stream, `-- Connection lost;
  reconnecting.`, a new Session within the backoff, the automatic `look`; a `Heartbeat` frame 20 s
  later printing nothing (AC-9); Tempo holding the CLI's trace across `OpenSession`, `Submit`,
  and `Subscribe`.
- **Not observable live until `AW-SRV-014` binds a Character**, and carried by that story as an
  inherited Definition-of-done line: the Room after `look` (AC-1), Events after `north` (AC-2), a
  second client seeing the departure (AC-3), a post-log `CommandRejected` (AC-4), read-only under a
  broker stop (AC-6), and a resume the server retains history for (AC-8 with
  `resume_window_exceeded`). Each is asserted in `TestPlay_*` against the wire shapes the server
  emits; `scripts/stack_play.sh` says in its header which assertions `AW-SRV-014` extends.

## Open questions

- **Inherited from `AW-SRV-029` (2026-09-19), contract-bearing:** an envelope may carry
  `perceived_from` (a Direction) — the perceived-through form of a `CharacterArrived`/`CharacterLeft`
  in an adjacent Room. Render it by that Direction ("someone arrives to the north"), never by the
  payload's movement direction, which is the mover's own and would mislead the observer. Empty
  `perceived_from` is the whole form and renders as the table above says. Additive: this story
  needs no dependency on 029, only the rule. **As built 2026-09-21:** `event.proto` has no
  `perceived_from` yet, so the renderer has nothing to read; `AW-SRV-029` adds the field and the
  row to `renderEvent` and the golden recording in the same pass — the rule stands, the code
  waits for the field.

- **Resolved 2026-09-21 (by the implementation):** the client is Connect's generated Go client
  (`gamev1connect.GameClient`) over HTTP/2 with TLS, speaking gRPC (`connect.WithGRPC()`), the same
  code `auth` and `account` already used and the smoke tests dial with. Both the unary calls and
  the `Subscribe` server stream run through it, against the compose stack, with the `cli.command`
  trace propagated on each. Moved here from `AW-CLI-001` on 2026-09-11: that story shipped no
  Protocol client, so the assumption sat in a story that could never test it. `AW-SRV-005` had
  proven the server side (`TestOpenSession_AllProtocolsOneHandler`); this closes the client's half.

- **Resolved 2026-09-07 (Brian):** the Text Interface is permanent, text-only, and operator/developer
  facing. It never renders. The scrolling-transcript assumption is now a decision, and protocol
  visibility is in scope. See Context.
- **Withdrawn, not resolved:** an earlier `[ASSUMPTION]` had `--as <name>` opening an anonymous
  throwaway Character until `AW-SRV-008` landed. That assumption is **wrong** under Brian's decision
  that acting-as must record who was really acting: an anonymous `--as` cannot name the real caller,
  so there is nobody to record. `--as` therefore requires a stored credential from the first version
  that has it, and until `AW-SRV-008` lands, `play` has no `--as` at all rather than an anonymous one.
  The alternative — ship anonymous now, add identity later — means an audit trail with a hole in it
  exactly where the early, least careful commands live.
