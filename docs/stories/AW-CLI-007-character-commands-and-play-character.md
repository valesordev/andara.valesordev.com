---
id: AW-CLI-007
title: andara-cli character create and list, and play --character
epic: EPIC-03
component: cli
type: feature
status: done
size: S
depends_on: [AW-CLI-004, AW-SRV-014]
blocks: [AW-INF-017]
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
   before `Subscribe`'s first `look`, the ack is shown only under protocol visibility, and the
   automatic `look`'s answer is the Room Aldric stands in (its title, then its description).
   Aldric's own arrival may be read just before it. The `BindCharacter` and the `look` apply in the
   same tick, and the arrival is Room-scoped (`AW-SRV-014` AC-5), so it reaches the Session. The
   client renders it like any other Event and does not suppress it. What a player perceives is the
   server's to decide (CLAUDE.md §1). *(Amended again 2026-09-25 at PR #72, with the live transcript
   in hand: "the first thing the player reads is the Room" did not hold.)* `Here:`
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

- **Resolved 2026-09-26 (§8):** `--character` takes the display name, case-insensitively, resolved
  through `ListCharacters` on the client; `character_id` is never typed by a human. As built and as
  `stack_play.sh` drives it (lower-case resolution).

## Verification record — 2026-09-24 (implementation; `review` until the §8 checklist passes)

PR [valesordev/andara.valesordev.com#72](https://github.com/valesordev/andara.valesordev.com/pull/72),
branch `impl/aw-cli-007-character-commands`, commit `f85513e`. Handoff and deviations:
`docs/feedback/AW-CLI-007-character-commands.md`.

| AC | How | Result |
|----|-----|--------|
| 1 | `TestCharacter_Create`: `Aldric (1 of 5)`, exit 0; JSON carries the `CharacterSummary`, `count`, `max_per_account`; the Session is closed (`andara_sessions_total{outcome="closed"}` +1). Live: `Implaevludn (1 of 5)` | pass |
| 2 | `TestCharacter_CreateRefused` for `roster_full`, `name_taken`, `name_invalid`: the server's message, exit 1, `error.code` = reason, no call after the refused `CreateCharacter`. Live: `name_taken` for another Account's name in upper case, `name_invalid` for a name with digits | pass |
| 3 | `TestCharacter_List`: aligned lines sorted by name, JSON summaries, asked every time; `TestFormatRosterGolden` (`testdata/character/list.txt`). Live: `dormant town/plaza`, then `town/hall` after the walk | pass |
| 4 | `TestPlay_SelectsBeforeSubscribe`: `ListCharacters` < `SelectCharacter` < `Subscribe` < `Submit look`; the ack only under `--show-protocol`; with no arrival ahead of it, the Room is the first game output. Live: the same order, the plaza read with `Here:` naming only the other Character, as AC-4 amended in #68 says. **One part still doesn't hold on the live stack**: the Character's own bind arrival shares the `look`'s tick and can print before the Room (feedback §2) | pass as amended, except arrival order (to architecture) |
| 5 | `TestPlay_OnlyCharacter`: selected, and the notice reads `…as oper, playing Aldric (session …` | pass |
| 6 | `TestPlay_NoOrSeveralCharacters`: exit 2 `no_character` naming `andara-cli character create`; exit 2 `character_required` listing `Aldric, Brin`; no `SelectCharacter`, `Subscribe` or `Submit`; the Session closed. Live: `no_character` | pass |
| 7 | `TestPlay_AlreadyLiveAtLaunch`: the server's message, exit 1 `already_live`, nothing subscribed. Live: `a character is already live on this account: Implbevludn`, exit 1 | pass |
| 8 | `TestPlay_ReconnectWaitsOutAlreadyLive`: Session closed behind the client's back, `already_live` twice then ok: the wait notice once, no exit, `ch-aldric` selected four times, the resume subscribed only after the successful select. `TestPlay_ReconnectRefusedIsFatal`: `no_such_character` on re-selection exits 1. Not run live: it needs a server restart on a stack this lane doesn't own | pass (integration) |

**Observability.** `TestCharacter_SpanParentsTheRPCs`: with `trust_inbound_traceparent`, the
server-side `ListCharacters` runs in the `cli.command` span's trace. No metrics or alerts (per the
story).

**Live, against the compose stack** (`make up` on `033f2c6`, this branch's `bin/andara-cli`, two
fresh Accounts): A created, listed, and ran `play --character` in lower case. It selected, subscribed,
read `Market Plaza` with `Here: <B>`, and walked `north`. B's transcript showed A arrive and leave.
A second `play` as B exited 1 with `already_live`.

**`make check`**: clean. `go test -race -count=5 ./admin/cli/`: clean.

**Outstanding before `done`:**
- The Definition-of-done line: `scripts/stack_play.sh` running the full M1 gate in the `stack`
  workflow. `scripts/` is architecture's, so the change is handed over in feedback §1. Until it
  lands, the `stack` job's `make stack-play` fails on this branch and on `main`: its play half runs
  with no Character, which AC-6 makes exit 2. That same change closes the inherited lines
  `AW-CLI-004` and `AW-SRV-011` left on `AW-SRV-014`.
- AC-4's wording (feedback §2) and the demo's `north` → `look` (feedback §3, PM).

## §8 pass — 2026-09-26 (architecture, SPRINT-02) — `done`

Against `main` at `cbe409e`, on the compose stack with the server image built from that tree
(`org.opencontainers.image.revision` = `cbe409e…`, `andara_build_info{commit="cbe409e"}`; built
explicitly, #73). `bin/andara-cli` is from the same tree.

| §8 item | Result |
|---|---|
| Every AC demonstrably passes | Pass. ACs 1–8 are covered by the tests in the verification record. AC-1–7 were live in `make stack-play`, and AC-8 in its restart section, which re-selects the Character on the new Session. |
| Tests from the test plan run in CI | Pass. `go test ./admin/cli/...` runs in `ci.yaml`, and `make stack-play` in `stack.yaml`. |
| `make check` clean | Pass, on `arch/sprint-02-s8-cli-007-review`. |
| Instrumentation verified against a real backend | Pass. `TestCharacter_SpanParentsTheRPCs`, plus the server series in the inherited lines below, scraped from the running server. The story adds no metrics of its own. |
| Config documented | Pass. No config keys. `admin/README.md` lists `character create`/`list` and `play --character`. |
| Migrations | None. |
| Glossary | Pass. Character, Roster, Session and Linkdead are all present. |
| No `[ASSUMPTION]` unresolved | Pass. The one is resolved above. |

**Definition-of-done line:** `scripts/stack_play.sh` runs the full M1 gate. Its play half has been
on `main` since `d36a914`/`8b86212`. This pass removes its pre-007 transitional path and asserts
`rejected_authz` unchanged for a bound Character. With that script, `make stack-play` passes on a
fresh stack at `cbe409e`.

**Inherited lines, each observed live:**

1. **`AW-CLI-004`'s transcript.**
   - AC-1–4 come from `make stack-play`'s transcript: the plaza on the first `look`, `leaves north`
     and the hall after `north` and `look`, B reading A arrive and leave unprompted, and
     `there is no exit west` as prose.
   - **AC-6:** with `docker compose stop redpanda`, two `look`s each printed "The world is
     read-only for a moment: your command was not taken. Try it again shortly." The prompt was kept
     and the Session was the same one throughout. After `start` (ready in 1 s), `look` answered with
     the Room on that Session. Exit 0 at quit.
2. **`AW-SRV-011`.**
   - A `play` stopped with SIGSTOP in the plaza while 12 movers walked through it. The client's
     4 MB HTTP/2 stream window (plus docker-proxy's queue) sits in front of `egress.buffer`, so the
     drop came after about 8 minutes.
   - `andara_session_egress_drops_total{reason="buffer_full"}` went 0 → 1 and
     `andara_sessions_in_drop_state` 0 → 1.
   - Loki had the `warn` "stream ended: client not reading, buffer full" with `session_id`,
     `buffered=1024`, `last_sent=115183`, `tick` and `trace_id=0139a9d4…`.
   - **Resume with retained Events:** after SIGCONT the client resubscribed from `last_sent`, and
     the `Game/Subscribe` span in the same trace (`0139a9d4…`) carries `stream.resumed=true`.
     `in_drop_state` went back to 0.
   - **`resume_window_exceeded`:** a second run kept the movers going 60 s past the drop. After
     SIGCONT, `andara_stream_resyncs_total{reason="resume_window_exceeded"}` went 0 → 1, and `play`
     printed "You may have missed some events; the world continues from here." and then the Room
     (`AW-CLI-004` AC-8).
   - What the client reports as the end is `code=internal … INTERNAL_ERROR; received from peer`,
     not `buffer_full`. The writer was blocked behind the full window, so the server reset the
     stream, as `server/README.md`'s writer-return rule says. The player sees one `warn` line on
     stderr and no prose. That is accepted; a friendlier line would be a `play` change for a later
     story.
3. **`AW-SRV-031`.** Ran `play --client-timeout 50ms` with `docker compose pause redpanda`, then
   `south`, then unpause.
   - Four Submits carried the same `client_ref` (`11a03c9e-2`), each `deadline_exceeded` at the
     client. The player read "The world has not answered that yet; it may still take it."
   - The server counted `andara_ingress_submits_total{outcome="deadline"}` +3.
   - After the unpause the transcript has exactly one `arrives from the north` and one
     `leaves south`: the Command applied once.
4. **`AW-SRV-014`, Session availability SLO:** first measurement, from the stack's Prometheus over
   the hour of these runs.
   - `sum_over_time(andara_sessions_in_drop_state[1h])` = 1 (one 5 s sample).
   - The denominator is 1317 Session-samples, so the SLI reads 0.99924.
   - Measuring it found a units defect in the spec's query: the numerator summed raw samples
     and the denominator subquery points. Those are equal only while scrape and rule-evaluation
     intervals are equal, which they are locally and nothing guarantees in Grafana Cloud.
     `docs/specs/slo/session-availability.md` now uses `[28d:1m]` on both sides.

**Also in this pass:**
- **#101:** `TestKafka_ConcurrentSubmitsOrdered` flaked on `main`'s `stack` run at `343cdbe`. Filed
  for implementation. It doesn't bear on this story.
- A stack whose log holds about 125k Commands (these floods) recovered in 73 s, beyond
  `stack_play.sh`'s 60 s reconnect window. That's #74's full-log replay, on a developer's
  long-lived stack only. CI's stack is fresh. The gate passed after `make down VOLUMES=1`.

Feedback §4 (`count` on `CreateCharacterResponse`) is declined for now; the reasoning is in the
feedback file.

