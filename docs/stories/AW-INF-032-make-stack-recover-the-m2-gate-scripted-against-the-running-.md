---
id: AW-INF-032
title: make stack-recover — the M2 gate scripted against the running stack
epic: EPIC-04
component: infra
type: infra
status: done
size: S
depends_on: [AW-SRV-007, AW-INF-017]
blocks: [AW-INF-034]
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
`andara_recovery_round_tick`). CLAUDE.md §8's "verified against a real backend" needs
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
- A moves out of its spawn Room, and the script waits for a complete Snapshot Round newer than that
  move (`andara-cli snapshot list`), so that recovery starts from a snapshot, not a replay from
  offset zero.
- A then moves a second time, after that round and before the kill. The second move exists only in
  the log tail, so A's final Room proves the tail was replayed, not just the snapshot loaded.
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

1. **Given** the stack up **when** `make stack-recover` runs **then** the script records the tick
   of the newest round whose `complete` column is `true` in `andara-cli snapshot list`'s table
   (`AW-SRV-007`'s CLI contract), or none. A and B then both enter play with `play --character`. A
   moves one Exit away from the spawn Room (Room 1), and B walks to Room 2, through Room 1, and stays
   there. A's transcript shows Room 1's title. *(Amended 2026-10-05: B used to stay in the spawn Room.)*
2. **Given** A in Room 1 **when** the script polls `andara-cli snapshot list` to a deadline of
   `snapshot.interval` + 30 s, per `live-assertions.md` **then** a round complete with a tick
   strictly greater than AC-1's recorded tick is listed, and the script records it as `R`. If none
   is, the script exits 1 naming the newest round it saw.
3. **Given** round `R` **when** A moves through a second Exit (Room 2, which isn't the spawn Room or
   Room 1), and A's transcript shows Room 2's title, so the move was applied and acknowledged **then**
   the script reads `snapshot list` once more. If its newest complete round isn't `R` (a round
   completed after the second move), it exits 1 with `stack-recover: a round completed after the
   tail move; the run proves nothing, rerun`. Otherwise it sends `SIGKILL` to `andara-server` at
   once and starts it again, and `/readyz` returns 200 within 120 s of the kill. The script prints the
   measured kill-to-ready seconds.
4. **Given** the server ready **when** the script reads its metrics, polled to a deadline **then**:
   - `andara_recovery_state_hash_match` is `1`;
   - `andara_recovery_round_tick` equals `R`, so the recovery used that snapshot, and Room 2 can
     only have come from the tail. If it's greater than `R` (a round completed between AC-3's re-read
     and the kill), the run is inconclusive, and the script exits as AC-3's rerun case.

   *(Amended at contract review, 2026-10-02: the line asserting
   `andara_acknowledged_commands_lost_total` is `0` is removed. `AW-SRV-007` withdrew that counter,
   because a restarted process reads `0` by construction. The RPO evidence is AC-5: A's second move
   was acknowledged before the kill, and only the log tail holds it.)*
5. **Given** the recovery **when** A's and B's `play` clients reconnect on their own (`AW-CLI-007`'s
   reconnect) **then** neither client's `--show-protocol` output has `reason=already_live` and neither prints "Waiting for
   your previous session to end." A's `look` shows Room 2: the move that only the log tail held was
   replayed. B waits in Room 2 with A, so each is the other's witness to a despawn line, and B's `look` shows
   B there with A. Neither
   transcript has a `leaves the world` or `fades from the world` line for either Character between
   the kill and the reconnect. The bodies rebound; they didn't despawn. The script also asserts it on the server: `andara_events_emitted_total{type="character_despawned"}`
   is `0` since the recovery, and `andara_linkdead_outcomes_total{outcome="reconnected"}` is at least `2`, one per
   player, because the transcripts can't see a despawn emitted before a client resubscribes.
6. **Given** both back **when** A and B quit cleanly **then** `character list` shows each `dormant`
   in Room 2 (the roster writes a Room at the unbind, so before the quit it still shows A in Room 1).
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
  default `120`), the AC-3 deadline. The default lives in the Makefile, and Phase 1 exit lowers it
  to 60 there.
- Exit codes: `0` all assertions held; `1` an assertion failed, a precondition is missing
  (`no .local/cli.yaml; run make up first`, `no bin/andara-cli; run make build first`), or the run
  was inconclusive (AC-3 and AC-4's rerun case). AC-7's dump and cleanup apply to all three.
- Job summary: when `$GITHUB_STEP_SUMMARY` is set, one appended table row with kill-to-ready
  seconds, `andara_recovery_duration_seconds_sum{phase="total"}`, `andara_recovery_replayed_ticks`,
  and the round tick. With it unset, nothing is appended. *(Named in the contract 2026-10-02, from
  SRE's §7 item 3.)*
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

*SRE review, 2026-10-02: amended. The changes are recorded in
`docs/feedback/AW-INF-032-stack-recover.md`.*

- **Metrics:** none added. AC-4 reads `AW-SRV-007`'s series from the running server.
  - **Two RTO numbers, both printed.** `docs/specs/slo/recovery.md` measures RTO from `SIGKILL`
    to ready. `andara_recovery_duration_seconds` measures from process start to ready. The script
    prints its wall-clock kill-to-ready, which is the AC-3 assertion, next to
    `andara_recovery_duration_seconds_sum{phase="total"}`. The difference is the restart term,
    and the SLO says that term dominates. Printing both shows where a regression lives.
  - **`andara_acknowledged_commands_lost_total` is withdrawn** (`AW-SRV-007`, 2026-10-02, feedback
    item 6). A fresh process has no acks to compare with. The RPO evidence is AC-5: A's `look`
    shows Room 2, which only the log tail held.
- **Logs:**
  - The script's `stack-recover:` progress lines.
  - On failure, the server's last 50 lines, through `make logs SVC=andara-server`.
  - When `$GITHUB_STEP_SUMMARY` is set, the script appends one table row: kill-to-ready seconds,
    phase `total`, `andara_recovery_replayed_ticks`, and the round tick. The SLO's exhaustion
    policy wants recovery-time regressions visible per run, and this puts one on every `stack`
    workflow run. With the variable unset, the script appends nothing.
- **Traces:** none added. The run produces one `recovery.run` trace (`AW-SRV-007`). That root is
  always sampled (`server/telemetry/sampling.go` ratio-samples only `Game/Submit`). The script
  prints the `trace_id` from the server's `recovery complete` line (the contract said "ready line"), so the §8 check can resolve the trace in
  local Tempo. Spans the killed process had buffered are lost with it, and nothing asserts on them.
- **Alerts:** none added, and the target asserts on no alert state.
  - The local Prometheus evaluates the chart's rules (`deploy/compose/docker-compose.yaml`).
    `AndaraServerUnavailable` (`for: 2m`) can go pending during the kill, and that's expected.
  - The script reads `andara_recovery_state_hash_match` from the server's `/metrics` directly.
    Whether `RecoveryStateMismatch` can see a `0` is `AW-SRV-007`'s (see its feedback file).

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

CLAUDE.md §8, plus: the §8 record shows `andara_recovery_state_hash_match`,
`andara_recovery_duration_seconds{phase}`, `andara_recovery_round_tick` and
`andara_restore_total{caller="recovery"}` read from the running server's `/metrics` after this
target's kill, and the `recovery.run` trace resolved in Tempo by its printed `trace_id`. This is the
first running-server caller of `AW-SRV-007`'s instruments (CLAUDE.md §8, "verified against a real
backend"). **Decided 2026-10-02 (architecture):** `AW-SRV-007`'s §8 record cites this run, and its
Definition of done says so.

## Open questions

- ~~`[ASSUMPTION]`~~ *Resolved 2026-10-05 (architecture, §8): `AW-SRV-007` didn't make a bystander's `<A> reconnects.`
  deterministic, so AC-5 doesn't assert it, and the script doesn't.* After a restart, a reconnecting client's Character is rebound through
  `AW-SRV-015`'s recovered-linkdead path (`Roster.SeedLinkdead`). B may read `<A> reconnects.` for
  A, but AC-5 doesn't assert it: whether a bystander's own reconnect races A's announcement is
  `AW-SRV-007`'s behaviour, not this target's. Architecture adds it to AC-5 at contract review if
  `AW-SRV-007` makes it deterministic.
- ~~`[ASSUMPTION]`~~ *Resolved 2026-10-05: `AW-SRV-007` merged and ships the grouped table; the script reads it.* `snapshot list`'s round table, with a `complete` column, is `AW-SRV-007`'s contract.
  Today the command needs `--zone` and lists per-Zone objects (`admin/cli/snapshotcmd.go`). The
  script uses the table `AW-SRV-007` ships, which is why that story is a hard dependency.
- ~~`[ASSUMPTION]`~~ *Resolved 2026-10-05: the interval stays 60 s, and the script's AC-2 deadline is the running server's `andara_snapshot_interval_seconds` plus 30 s (recorded above). The CI step takes about a minute.* `snapshot.interval` is the existing 60 s, so the AC-2 wait adds up to 90 s per CI
  run. If that's too slow for the `stack` workflow, the script may set a shorter interval on the
  compose server only. That's SRE's call when building, and it's recorded in the story.

## Verification record — 2026-10-04 (SRE; `review` until the §8 checklist passes)

`make stack-recover` against the local stack on `main` at `60ced80` (AW-SRV-007 merged), twice, both green:
kill-to-ready 2.2 s of a 120 s RTO, `andara_recovery_round_tick` equal to R, `andara_recovery_state_hash_match`
1, `andara_recovery_replayed_ticks` 29 and 39, `andara_restore_total{caller="recovery",outcome="ok"}` 1,
`andara_recovery_duration_seconds_sum{phase="total"}` 0.74 s. The printed `recovery.run` trace resolved in
Tempo: the root, four `recovery.load_snapshot`, `recovery.seek`, `restore.verify` (`round_tick` 2858998,
`outcome=ok`), `recovery.replay` and `recovery.verify`.

**Where the script differs from the contract above**, each recorded in
`docs/feedback/AW-INF-032-stack-recover.md`:
- **Rooms.** Room 1 is the Market Plaza (`out` from Purgatory) and Room 2 the Town Hall (`north`). B walks
  `out` and `north` to the Town Hall and waits there, instead of staying in Purgatory: a despawn is addressed
  to the Room's occupants, so with the two in different Rooms neither could have read the other's despawn line,
  and AC-5's absence check would pass whatever happened. In the shared Room each is the other's witness, and B
  also reads A arrive from the south.
- **AC-5's `already_live` check.** Both clients run with `--show-protocol`, and the assertion is that neither
  `reason=already_live` nor the waiting line appears after the kill, as PM proposed for `AW-INF-034`.
  Amended 2026-10-05.
- **AC-6's Rooms.** `character list` records a body's Room at its unbind, so a list taken before the quit still
  shows the Plaza for A. The script asserts the Room the post-recovery looks read: `dormant town/hall` for both.
- **The trace id** is printed from the `recovery complete` line, which carries `trace_id`. The contract said
  "ready line"; amended 2026-10-05.
- **AC-5's despawn check also reads the server.** The transcripts can't see a despawn emitted before a client
  resubscribes (the hub replays nothing to a new subscription), so before the quits the script asserts
  `andara_events_emitted_total{type="character_despawned"}` is 0 since the recovery, and
  `andara_linkdead_outcomes_total{outcome="reconnected"}` is at least 2, one per player.
- **The AC-2 deadline** is the running server's `andara_snapshot_interval_seconds` plus 30 s, not an environment
  variable the compose server doesn't receive (PR review). The script reads no `ANDARA_SNAPSHOT_INTERVAL`.
- **AC-4 requires every series it prints.** `andara_recovery_replayed_ticks`,
  `andara_recovery_duration_seconds_sum{phase="total"}` and `andara_restore_total{caller="recovery",outcome="ok"}`
  (at least 1) fail the gate when absent, since this run is the live verification of those instruments (PR
  review). A renamed series fails with `andara_recovery_replayed_ticks is absent after recovery`.
- **AC-7's exit code.** A failing run prints the server container's status, exit code and restart count
  (`docker inspect`) before its last 50 log lines, so a server that exits non-zero during recovery is reported
  with its code, under `restart: on-failure` too.
- **A failed run** starts the server again if its kill left it down, so the steps after it in the `stack`
  workflow still have a server. The job summary gets a three-line table: the header, the separator and the row.

**Added for AW-SRV-007's §8 (2026-10-05):** after the recovery and before the quits, the script asserts that
`RecoveryStateMismatch` is loaded in Prometheus, that Prometheus has scraped the recovered process's
`andara_recovery_state_hash_match` at 1, and that twelve seconds later (two evaluation intervals) the alert has no series in
`ALERTS` at either state. If an earlier `make stack-recover-mismatch` left the alert in `ALERTS` (it stays for
`keep_firing_for`, 15 m, and the instant query's 5-minute lookback adds to that), the absence can't be told from
that, so the check is skipped and says so.

**Mutations, run live:** `STACK_RECOVER_RTO=1` fails with `/readyz did not return 200 within 1s of the kill`
and leaves the server ready; expecting the Market Plaza instead of the Town Hall in A's post-recovery look fails
with "the tail move was not replayed". A first run asserted AC-6 against a pre-kill `character list` and failed
on exactly that Room, which is how the list's behaviour was found.

**Deferred from the pre-PR review (P3):** the line marks before the post-recovery `look` are taken without a
sentinel, so a late line from before the kill could in principle satisfy that poll; a reconnect's automatic
look could satisfy it instead of the explicit one. Both still show a Room read after the reconnect.

**Not observed:** the 60 s Phase 1 exit RTO (out of scope), and the CI run itself, which this PR's `stack`
workflow supplies.

## §8 close (architecture, 2026-10-05): done

Run from the `stack` workflow's passing run on `main` at `0b43f76` (run 37333182657), and from the script and the
Makefile on that tree, since the target needs a running stack and CI is a second environment from SRE's own.

- **Every acceptance criterion passes.** The `the M2 gate against the stack — kill -9, recover from a snapshot,
  rebind` step passed in 63 s. Its log has `stack-recover: ready 1.4s after the kill (RTO 120s), round 4164, hash
  match` and the closing `M2 gate — killed, recovered from a snapshot, hash matched, both rebound — passes`. That
  covers AC-1 to AC-6, with AC-5's and AC-6's amended text matching what the script asserts (B in Room 2 with A,
  `--show-protocol` for `already_live`, `dormant town/hall`). AC-7's failure path is SRE's mutations run live in the
  record above (`STACK_RECOVER_RTO=1`, the wrong Room), and the script's precondition exits are in its first lines
  (`no .local/cli.yaml; run make up first`, `no bin/andara-cli; run make build first`).
- **Tests in CI.** The `stack` workflow runs `make stack-recover` after `make stack-linkdead` on every PR and merge
  to `main`, as the Test plan says. It isn't in `make check`'s targets, like the other stack targets: it needs a
  running stack.
- **`make help`** lists `stack-recover`, with the contract's text; `STACK_RECOVER_RTO ?= 120` is in the Makefile and
  the job summary is written only when `$GITHUB_STEP_SUMMARY` is set.
- **Instrumentation** is SRE's own check and this story's Definition of done: the record above has the series read
  from the running server (`andara_recovery_state_hash_match`, `_duration_seconds{phase}`, `_round_tick`,
  `andara_restore_total{caller="recovery"}`) and the `recovery.run` trace resolved in Tempo. `AW-SRV-007`'s §8 cites
  this run, as decided.
- **The three `[ASSUMPTION]`s** are marked resolved above. **No config key, migration or domain term** is added.
- **Carried, not blocking:** the P3 deferred from the pre-PR review (the line marks before the post-recovery `look`
  have no sentinel) stays as recorded; and the 60 s Phase 1 exit RTO is out of scope, lowered in the Makefile at that exit.

