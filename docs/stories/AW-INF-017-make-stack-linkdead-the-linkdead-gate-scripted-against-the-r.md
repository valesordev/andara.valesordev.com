---
id: AW-INF-017
title: make stack-linkdead — the linkdead gate scripted against the running stack
epic: EPIC-08
component: infra
type: infra
status: ready
size: S
depends_on: [AW-SRV-015, AW-CLI-007, AW-CLI-008]
blocks: []
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
