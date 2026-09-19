---
id: AW-CLI-004
title: andara-cli play — the text interface as a first-class protocol client
epic: EPIC-03
component: cli
type: feature
status: ready
size: M
depends_on: [AW-CLI-001, AW-SRV-005, AW-SRV-011]
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

## Open questions

- **Inherited from `AW-SRV-029` (2026-09-19), contract-bearing:** an envelope may carry
  `perceived_from` (a Direction) — the perceived-through form of a `CharacterArrived`/`CharacterLeft`
  in an adjacent Room. Render it by that Direction ("someone arrives to the north"), never by the
  payload's movement direction, which is the mover's own and would mislead the observer. Empty
  `perceived_from` is the whole form and renders as the table above says. Additive: this story
  needs no dependency on 029, only the rule.

- `[ASSUMPTION]` The client uses Connect's Go implementation, which speaks gRPC, gRPC-Web, and Connect
  from one generated client — matching what the server serves (ADR-0003). Moved here from
  `AW-CLI-001` on 2026-09-11: that story ships no Protocol client, so the assumption sat in a story
  that could never test it. `AW-SRV-005` has since proven the server side of it —
  `TestOpenSession_AllProtocolsOneHandler` establishes a Session over all three protocols against one
  handler — so what remains open is only the client's half.

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
