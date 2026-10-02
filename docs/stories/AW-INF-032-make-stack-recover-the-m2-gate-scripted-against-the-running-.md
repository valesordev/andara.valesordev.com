---
id: AW-INF-032
title: make stack-recover — the M2 gate scripted against the running stack
epic: EPIC-04
component: infra
type: infra
status: draft
size: S
depends_on: [AW-SRV-007, AW-INF-017]
blocks: []
lane: sre
risk: medium
---

## Context

SPRINT-04's demo goal is the M2 gate (`docs/roadmap.md`): "`kill -9` the server; the World returns
within 120 s with a matching State Hash, and every linkdead Character rebinds rather than
despawning." `AW-SRV-007` builds the recovery and proves it in CI with its kill-and-recover
integration test. That test runs against a throwaway Redpanda and the filesystem store, not the
running stack. Its operator test plan is a hand-typed `make up && andara-server &` and `kill -9 %1`.
A demo step like that is a §9 defect. This story makes the gate a target, the way `make stack-play`
made M1 one and `make stack-linkdead` (`AW-INF-017`) made the linkdead half of M2 one.

The target also carries `AW-SRV-007`'s instruments into a live observation from the running server
(`andara_recovery_state_hash_match`, `andara_recovery_duration_seconds`,
`andara_acknowledged_commands_lost_total`). CLAUDE.md §8's "verified against a real backend" needs
that once a caller exists, and the RTO in `docs/specs/slo/recovery.md` is measured the way that SLO
defines it: from `SIGKILL` to accepting connections with a verified State Hash.

## User story

As an operator, I want one make target that kills the server mid-play on the running stack and
proves the World comes back as it was, so that the M2 gate is checked on every merge and a demo can
cite it.

## Scope

### In scope
- `scripts/stack_recover.sh` and `make stack-recover`, needing `make up` and `make build`.
- Two throwaway player Accounts made as `stack_linkdead.sh` makes them: A (the player who's moved)
  and B (the bystander), each with a Character.
- A moves out of its spawn Room before the kill, so that "as they left it" is a position, not the
  spawn.
- The script waits for a complete Snapshot Round newer than A's move (`andara-cli snapshot list`),
  so that the recovery under test is from a snapshot plus log tail, not a replay from offset zero.
- The kill is `SIGKILL` to the compose `andara-server` container, then a start, both by the script.
- A step in `.github/workflows/stack.yaml` after `stack-linkdead`.

### Out of scope
- Killing a pod on `dev`, or any Kubernetes restart path. Deploy-time recovery and probes are
  `AW-INF-007` and `AW-INF-011`.
- The failure modes: hash mismatch, log gap, incomplete round, and state version. `AW-SRV-007`'s
  integration tests cover each one. The target proves only the passing path.
- 50 Sessions. `AW-SRV-007`'s inherited line from `AW-SRV-015` runs 50 in CI; two are enough to
  demo.
- The 60 s Phase 1 exit RTO. This target asserts M2's 120 s.

## Acceptance criteria

1. **Given** the stack up **when** `make stack-recover` runs **then** A and B both enter play with
   `play --character`. A then moves one Exit away from the spawn Room, and B stays in the spawn Room.
   A's transcript shows the destination Room's title.
2. **Given** A's move **when** the script polls `andara-cli snapshot list` to a deadline of
   `snapshot.interval` + 30 s, per `live-assertions.md` **then** a round with `complete=true` and a
   tick after A's move is listed. If none is, the script exits 1 naming the newest round it saw.
3. **Given** that round **when** the script sends `SIGKILL` to `andara-server` and starts it again
   **then** `/readyz` returns 200 within 120 s of the kill. The script prints the measured kill-to-ready
   seconds.
4. **Given** the server ready **when** the script reads its metrics, polled to a deadline **then**:
   - `andara_recovery_state_hash_match` is `1`;
   - `andara_recovery_round_tick` is at or after the round from AC-2, so the recovery used a snapshot;
   - `andara_acknowledged_commands_lost_total` is `0`.
5. **Given** the recovery **when** A's and B's `play` clients reconnect on their own (`AW-CLI-007`'s
   reconnect) **then** neither transcript has an `already_live` refusal. A's `look` shows the Room A
   moved to in AC-1, not the spawn Room. B's `look` shows B still in the spawn Room. Neither
   transcript has a `leaves the world` or `fades from the world` line for either Character between
   the kill and the reconnect. The bodies rebound; they didn't despawn.
6. **Given** both back **when** A and B quit cleanly **then** `character list` shows each `dormant`
   in the Room from AC-5.
7. **Given** any assertion failing **when** the script exits **then** it exits 1 with
   `stack-recover: <what failed>`, prints both transcripts and the server's last 50 lines, and leaves
   no `andara-cli` child running. A server that exits non-zero during recovery is reported with its
   exit code, and `AW-SRV-007`'s exit-code table names the cause.

## Interface contract

- `make stack-recover`: `## stack-recover: the M2 gate scripted — kill -9 the server mid-play,
  recover from a snapshot within 120 s with a matching State Hash, and both players rebind — needs
  make up and make build`.
- Environment it reads, all existing: `ANDARA_HTTP_PORT`, `ANDARA_GRPC_PORT`,
  `ANDARA_BOOTSTRAP_OPERATOR`, `ANDARA_TLS_CA_FILE`. One added: `STACK_RECOVER_RTO` (seconds,
  default `120`), the AC-3 deadline. Phase 1 exit lowers it to 60 by changing the default, not the
  script.
- Exit codes: `0` all assertions held; `1` an assertion failed or a precondition is missing
  (`no .local/cli.yaml; run make up first`, `no bin/andara-cli; run make build first`).
- Output: `stack-recover: <step>` progress lines as in `stack_linkdead.sh`, including
  `stack-recover: ready <N>s after the kill (RTO 120s), round <tick>, hash match`, and ending
  `stack-recover: M2 gate — killed, recovered from a snapshot, hash matched, both rebound — passes`.
- Credentials in an isolated `$XDG_CONFIG_HOME`, as `stack_play.sh` does, so a developer's own
  credential file is never written.

## Data / state impact

Two Accounts and two Characters per run with random suffixes, as `stack-linkdead` leaves them.
`make down VOLUMES=1` clears them. The kill is the test: the stack is left running and ready, so the
`stack` workflow's later steps run against a recovered server.

## Observability requirements

- **Metrics:** none added. AC-4 reads `AW-SRV-007`'s series from the running server.
- **Logs:** the script's progress lines, and on failure the server's last 50 lines through
  `make logs SVC=andara-server`.
- **Traces:** none added. The run produces one `recovery.run` trace (`AW-SRV-007`). The script
  prints its `trace_id` from the server's ready line, so the §8 check can resolve it in Tempo.
- **Alerts:** none.

## Test plan

- **Unit:** none; the script is exercised by running it.
- **Integration:** the `stack` workflow runs `make stack-recover` after `make stack-linkdead`, on
  every PR and merge to `main`.
- **Manual/operator:**
  ```
  make up && make build
  make stack-recover     # ends "M2 gate … passes"
  ```

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-007`'s "live, from the running server" §8 line, and its
`andara_recovery_*` series, are recorded against this target's run.

## Open questions

- `[ASSUMPTION]` After a restart, a reconnecting client's Character is rebound through
  `AW-SRV-015`'s recovered-linkdead path (`Roster.SeedLinkdead`). B may read `<A> reconnects.` for
  A, but AC-5 doesn't assert it: whether a bystander's own reconnect races A's announcement is
  `AW-SRV-007`'s behaviour, not this target's. Architecture adds it to AC-5 at contract review if
  `AW-SRV-007` makes it deterministic.
- `[ASSUMPTION]` `snapshot.interval` is the existing 60 s, so the AC-2 wait adds up to 90 s per CI
  run. If that's too slow for the `stack` workflow, the script may set a shorter interval on the
  compose server only. That's SRE's call when building, and it's recorded in the story.
