---
id: AW-INF-017
title: make stack-linkdead — the linkdead gate scripted against the running stack
epic: EPIC-08
component: infra
type: infra
status: done
size: S
depends_on: [AW-SRV-015, AW-CLI-007, AW-CLI-008]
blocks: [AW-INF-032]
lane: architecture
risk: low
---

## Context

SPRINT-02's demo goal is the linkdead half of the M2 gate: a dropped connection is survivable. To
show it, a client's stream has to drop without the client quitting. A clean quit sends
`CloseSession`, and `AW-SRV-015` AC-5 despawns on that at once, so a quit proves nothing. The only
way an operator can drop a stream today is to kill the `andara-cli play` process by hand
(`AW-SRV-015`'s manual test: "kill the client"). A hand-typed `kill -9 <pid>` in a demo is a §9
defect. This story makes the gate a target, the way `make stack-play` made M1 one.

The target also carries `AW-SRV-015`'s instruments into a live observation from the running server,
which CLAUDE.md §8's "verified against a real backend" needs once a caller exists.

## User story

As an operator, I want one make target that drops a player's connection on the running stack and
proves the Character survives it, so that the linkdead gate is checked on every merge and a demo can
cite it.

## Scope

### In scope
- `scripts/stack_linkdead.sh` and `make stack-linkdead`, needing `make up` and `make build`.
- Two throwaway player Accounts made with `andara-cli account create`, as `stack_play.sh` makes
  them, each with a Character in `town/plaza`.
- The drop is the script killing its own `andara-cli play` child with `SIGKILL`, so no
  `CloseSession` is sent.
- A step in `.github/workflows/stack.yaml` after `stack-play`.

### Out of scope
- Grace expiry (`AW-SRV-015` AC-3/4) and the combat extension: covered by `AW-SRV-015`'s
  integration tests on Ticks. A live wait of 180 s per CI run is not worth it.
- Restart within RTO (`AW-SRV-015` AC-9): needs `AW-SRV-007`.
- An operator command that disconnects a Session: none exists, and none is proposed here.

## Acceptance criteria

1. **Given** the stack up **when** `make stack-linkdead` runs **then** A (the dropped player) and B
   (the bystander) both enter `town/plaza` with `play --character`, and B's transcript shows A's
   arrival.
2. **Given** both playing **when** the script sends `SIGKILL` to A's `play` **then**, polled to a
   deadline of `session.linkdead_detect` + 10 s per `live-assertions.md`, B's transcript shows
   `<A> goes linkdead.`, and B's `look` lists `<A> (linkdead)` under `Here:`.
3. **Given** A linkdead **when** the script runs `andara-cli play --character <A>` for A again
   **then** A's new transcript has no `already_live` refusal and exit 0 at quit, B's transcript shows
   `<A> reconnects.`, and A's `look` shows `town/plaza`'s Room. The body never left: B's transcript
   has no `leaves the world` or `fades from the world` line for A between the kill and the
   reconnect.
4. **Given** A back **when** A quits cleanly **then** B's transcript shows `<A> leaves the world.`,
   and `character list` for A shows `dormant  town/plaza`.
5. **Given** the run **when** it completes **then** the server's `/metrics` shows
   `andara_linkdead_outcomes_total{outcome="reconnected"}` and
   `andara_character_unbinds_total{reason="quit",outcome="ok"}` each risen by at least 1, and
   `andara_sessions_linkdead` (summed over `in_combat`) back to its value before the run, all polled
   to a deadline. *(Amended at architecture's contract review, 2026-09-26: the draft read
   `andara_linkdead_outcomes_total{outcome="quit"}`, which counts a linkdead Session that is closed
   while linkdead. A's quit comes after the reconnect, from a live Session, so that series doesn't
   move.)*
6. **Given** any assertion failing **when** the script exits **then** it exits 1 with
   `stack-linkdead: <what failed>`, prints both transcripts, and leaves no `andara-cli` child
   running.

## Interface contract

- `make stack-linkdead`: `## stack-linkdead: the linkdead gate scripted — drop a player's stream,
  reconnect, and a bystander sees both — needs make up and make build`.
- Environment it reads, all existing: `ANDARA_HTTP_PORT`, `ANDARA_GRPC_PORT`,
  `ANDARA_BOOTSTRAP_OPERATOR`, `ANDARA_TLS_CA_FILE`. None added.
- Exit codes: `0` all assertions held; `1` an assertion failed or a precondition is missing
  (`no .local/cli.yaml; run make up first`, `no bin/andara-cli; run make build first`).
- Output: `stack-linkdead: <step>` progress lines as in `stack_play.sh`, ending
  `stack-linkdead: linkdead gate — dropped, marked, reconnected, quit — passes`.
- Credentials in an isolated `$XDG_CONFIG_HOME`, as `stack_play.sh` does, so a developer's own
  credential file is never written.

## Data / state impact

Two Accounts and two Characters per run with random suffixes, as `stack-play` leaves them.
`make down VOLUMES=1` clears them.

## Observability requirements

- **Metrics:** none added; AC-5 reads `AW-SRV-015`'s series from the server.
- **Logs:** the script's progress lines. On failure, the server's last 50 lines through
  `make logs SVC=andara-server` are printed too.
- **Traces / Alerts:** none.

## Test plan

- **Unit:** none; the script is exercised by running it.
- **Integration:** the `stack` workflow runs `make stack-linkdead` after `make stack-play`, on every
  PR and merge to `main`.
- **Manual/operator:**
  ```
  make up && make build
  make stack-linkdead     # ends "linkdead gate … passes"
  ```

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-015`'s "live, from the running server" §8 line is recorded against this
target's run.

## Open questions

- **Settled at contract review (2026-09-26): `SIGKILL` exercises one of `AW-SRV-015`'s two drop
  paths, not both.** The kernel closes the dead client's socket, so the server sees the stream end
  at once. A partition with no close is the keepalive path (`session.linkdead_detect`), and that
  path is covered by `AW-SRV-015`'s integration test through a proxy that stops forwarding. The
  AC-2 deadline of `linkdead_detect` + 10 s covers either path, so the target stays correct if a
  later change moves detection. A demo citing this target claims the transport-close path only.

## Verification (architecture, 2026-09-26)

On `arch/aw-inf-017-stack-linkdead`: `scripts/stack_linkdead.sh`, `make stack-linkdead`, and a
`stack` workflow step after `make stack-play` (and before the alert step, which stops the server).
Run on a local stack freshly built by `make up` from `main` at `5c5d83c` (#114 and #115 merged):

```
  B| Droppedrlnwobrm arrives.
  B| Droppedrlnwobrm goes linkdead.
  B| Market Plaza
  B| A dusty square of packed earth.
  B| Exits: east, north, south
  B| Here: Droppedrlnwobrm (linkdead)
  B| Droppedrlnwobrm reconnects.
  B| Droppedrlnwobrm leaves the world.
stack-linkdead: linkdead gate — dropped, marked, reconnected, quit — passes
```

| AC | Evidence |
|----|----------|
| 1 | B is subscribed and has read the plaza before A launches; B's transcript shows `<A> arrives.` |
| 2 | `SIGKILL` to A's `play`; `<A> goes linkdead.` polled to `linkdead_detect` + 10 s (15 s by default, from `ANDARA_LINKDEAD_DETECT` if set). B's `look` is sent after that line, so the `Here: <A> (linkdead)` it waits for can only be its answer. `andara_sessions_linkdead` is read up by one at that point |
| 3 | A's second `play` connects playing A, reads the plaza, and its stderr has no `already live`. B reads `<A> reconnects.`. Absence is anchored (`live-assertions.md` rule 3): B's stream is ordered, so once the reconnect line is in B's transcript, no `leaves/fades from the world` line between the kill and it means none was sent |
| 4 | EOF on A's stdin is `play`'s quit: exit 0, B reads `<A> leaves the world.`, and `character list` shows `<A>  dormant  town/plaza` |
| 5 | Polled to 15 s from the server's `/metrics`: `andara_linkdead_outcomes_total{outcome="reconnected"}` +1, `andara_character_unbinds_total{reason="quit",outcome="ok"}` +1, `andara_sessions_linkdead` summed over `in_combat` back to its value before the run. Samples match by label set, not label order |
| 6 | Run with `ANDARA_HTTP_PORT=1` (no metrics reachable): exit 1, `stack-linkdead: andara_sessions_linkdead did not count <A>`, both transcripts and the server's last 50 lines printed, and no `andara-cli` process left (`pgrep`). The `EXIT` trap `SIGKILL`s any child still running |

**Deviation:** the server's last 50 lines come from `docker compose logs --tail 50 andara-server`,
not `make logs SVC=andara-server` as the Observability section says. `make logs` follows (`-f`),
so it would never return on a failing run.

**For `AW-SRV-015`'s §8:** this run is the live observation of
`andara_linkdead_outcomes_total{outcome="reconnected"}` and `andara_sessions_linkdead` from the
running server that its Definition of done names.

## §8 review (architecture, 2026-09-26)

On `arch/sprint-02-review-4`. **`done`.**

| §8 item | Holds? | Evidence |
|---------|--------|----------|
| Every AC passes | yes | The verification record above, and the `stack` workflow's "the linkdead gate against the stack" step, green on `main` at `8ab863a` (the merge of #119) and after. Passed again locally against a stack built from `6561cde` |
| Tests run in CI | yes | The `stack` workflow step, on every PR and merge that touches the stack or the Go tree |
| `make check` | yes | clean |
| Instrumentation | yes | none added; AC-5 reads `AW-SRV-015`'s series from the running server |
| Config documented | yes | no new config; the target is in `make help` |
| Migrations | n/a | none |
| Glossary | yes | no new domain term |
| No `[ASSUMPTION]` | yes | none |

Review of #119 fixed `ANDARA_LINKDEAD_DETECT` parsing: it now takes any Go duration (`2754a3c`).
The story-specific line holds: `AW-SRV-015`'s live observation is recorded against this target.
