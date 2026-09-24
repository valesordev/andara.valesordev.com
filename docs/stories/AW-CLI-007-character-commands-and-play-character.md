---
id: AW-CLI-007
title: andara-cli character create and list, and play --character
epic: EPIC-03
component: cli
type: feature
status: in-progress
size: S
depends_on: [AW-CLI-004, AW-SRV-014]
blocks: []
lane: implementation
risk: low
---

## Context

`AW-CLI-004` shipped `play` as a Protocol client with no way to choose a body: nothing bound a Character
before `AW-SRV-014`. With 014's `CreateCharacter`, `ListCharacters`, and `SelectCharacter` on the
Game service, `play` needs to select one before its first `look`, and a player needs to create one
first. This is the last piece of M1's gate: `andara-cli play`, a Room, `north`, a different Room.

Creation and selection are roster RPCs, not world Commands. The earlier `AW-SRV-014` sketch had
`> create Aldric` and `> select Aldric` typed into the world; that would make the parser own account
state and the client decide what a line means. They are CLI commands and a flag instead, and the
world's vocabulary stays the sim's.

## User story

As a player, I want to make a character once and then be it every time I play, so that entering the
world is one command.

## Scope

### In scope
- `andara-cli character create <name>` and `andara-cli character list`.
- `andara-cli play --character <name>`; with no flag, the Account's only Character is used; with
  none, or several, a usage error that says what to do.
- The selection's `SubmitResponse` treated as an ack (not game output), the Room arriving as the
  automatic `look`'s answer, and the arrival Event rendered like any other.

### Out of scope
- `character delete` — groomed alongside `AW-SRV-032`.
- Switching Characters inside a session (`/character <name>`) — the same later story.
- Anything typed into the world meaning create or select.

## Acceptance criteria

1. **Given** a logged-in Account with no Characters **when** `andara-cli character create Aldric` runs
   **then** stdout is `Aldric (1 of 5)`, exit 0, and `--output json` carries the `CharacterSummary`.
2. **Given** a name the server refuses (`roster_full`, `name_taken`, `name_invalid`) **when** create
   runs **then** the server's message is printed, exit 1, `error.code` is the reason, and nothing else
   is attempted.
3. **Given** two Characters **when** `andara-cli character list` runs **then** one line each — name,
   `live`/`dormant`, `zone/room` — sorted by name; `--output json` carries the summaries.
4. **Given** `play --character Aldric` **when** the Session opens **then** `SelectCharacter` is called
   before `Subscribe`'s first `look`, the ack is shown only under protocol visibility, and the first
   thing the player reads is the Room Aldric stands in (its title, then its description). `Here:`
   lists the *other* Characters present and never Aldric: the viewer is not an occupant of its own
   description (`server/sim/verbs.go` `describe`), so in a Room Aldric has to themself there is no
   `Here:` line at all. A second client in that Room reads `Here: Aldric` on its next `look`.
   *(Amended 2026-09-24 at architecture's contract review, before implementation started: the text
   said "the Room with `Here: Aldric`", which the sim has never produced for the viewer.)*
5. **Given** `play` with no `--character` and exactly one Character **then** it is selected, and the
   connection notice names it.
6. **Given** `play` with no `--character` and no Characters **then** exit 2 with `error.code`
   `no_character` and the message names `andara-cli character create`; with several, exit 2
   `character_required` listing them. Nothing is subscribed.
7. **Given** `play --character Aldric` at launch while Aldric is live on another Session **then** the
   server's `already_live` message is printed and exit 1 `already_live`; nothing is subscribed.
8. **Given** a reconnect (`AW-CLI-004` AC-7) on a new Session **then** the same Character is selected
   again before the resume, so the player is back in the World without typing anything. The old
   Session's teardown is what frees the Character (`AW-SRV-014`), and the new Session can arrive
   first — so during a reconnect `already_live` is **retried on the reconnect backoff**, announced
   once ("Waiting for your previous session to end."), never fatal; only AC-7's launch case exits.
   Until `AW-SRV-015`, the body is either still present (the crash case, taken where it stands, no
   arrival) or dormant (a clean quit, re-spawned at its position, the Room sees it).

## Interface contract

```
andara-cli character create <name>
andara-cli character list
andara-cli play [--character <name>] …
```

- `error.code` values added, additive: `no_character` (2), `character_required` (2), and the server's
  reasons `roster_full`, `name_taken`, `name_invalid`, `already_live`, `no_such_character` (1).
- `character` commands use the Game client and a Session: they `OpenSession`, call the RPC, and
  `CloseSession`, so every roster action is a Session-correlated audit line on the server.
- Protocol visibility shows `» SelectCharacter session_id=… character_id=…` and the `«` ack.
- The reconnect loop's order is `OpenSession` → `SelectCharacter` (retried on `already_live`) →
  `Subscribe`; a `SelectCharacter` refused for any other reason is fatal with that reason.

## Data / state impact

None. No client-side roster cache: `list` asks the server every time.

## Observability requirements

Per `AW-CLI-001`: output and exit code. `cli.command` root span parents the RPCs as it does for
`play`. No metrics, no alerts.

## Test plan

- **Unit:** the flag resolution table (none/one/several Characters × `--character` given or not);
  `list` formatting golden; error-code mapping for each server reason.
- **Integration:** over the test gateway with a fake roster seam — create, list, `play --character`
  asserting `SelectCharacter` precedes the first `Submit`; reconnect re-selecting the same Character,
  with the fake answering `already_live` twice before `ok` and the transcript showing the wait notice
  once and no exit.
- **Manual/operator:** the M1 gate transcript in `AW-SRV-014`'s test plan, and `scripts/stack_play.sh`
  extended: `character create`, `play --character`, `look`, `north`, assert the two Rooms.

## Definition of done

CLAUDE.md §8, plus: `scripts/stack_play.sh` runs the full M1 gate in the `stack` workflow — the
Room, the move, and a second client seeing the arrival and departure — which closes the inherited
lines `AW-CLI-004` and `AW-SRV-011` left on `AW-SRV-014`.

**Inherited lines, enumerated (2026-09-24, architecture).** `AW-SRV-014`'s record passes these to
this story because each needs `play` driving a bound Character. The scripted gate above closes the
first; architecture observes the rest at this story's §8 on the compose stack with the `play` this
story ships. They add no scripted test here. Each is a live observation that the scripted gate
cannot make, and the §8 record lists every one of them with what it showed:

1. `play`'s own transcript for `AW-CLI-004` AC-1 (Room after the first `look`), AC-2 (Events after
   `north`), AC-3 (a second `play` sees the departure unprompted), AC-4 (a post-log
   `CommandRejected`, e.g. `west` from a Room with no west Exit), and AC-6 (read-only under
   `docker compose stop redpanda`: one message, the prompt kept, recovery on start).
2. `AW-SRV-011`: a stream ended `buffer_full` from a deliberately stalled `play` (SIGSTOP), with its
   `warn` line in Loki (`session_id`, `buffered`, `last_sent`),
   `andara_session_egress_drops_total{reason="buffer_full"}` and `andara_sessions_in_drop_state`
   moving; a resume that replays retained Events (`stream.resumed` on the `Game/Subscribe` span);
   `play`'s AC-8 with `resume_window_exceeded`.
3. `AW-SRV-031`: the ambiguous fates on the running server. `play --client-timeout 50ms` against
   `docker compose pause redpanda`, then `north`, then unpause, gives exactly one
   `CharacterArrived`.
4. `AW-SRV-014`: the first measurement of the Session availability SLO
   (`docs/specs/slo/session-availability.md`), meaning the SLI's two integrals read back from the
   stack's Prometheus over the session above.

A line that cannot be observed on the compose stack is recorded as such, and it names the story
that carries it next. It does not hold this story in `review`.

## Open questions

- `[ASSUMPTION]` `--character` takes the display name, case-insensitively, resolved through
  `ListCharacters` on the client; `character_id` is never typed by a human.
