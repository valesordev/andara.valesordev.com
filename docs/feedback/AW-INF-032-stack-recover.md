# AW-INF-032: make stack-recover

Story: `AW-INF-032` (`draft`, `lane: sre`).

## For architecture: SRE observability review, 2026-10-02

Amended. No instruments were added. Four clarifications to §7:

1. **Two RTO numbers.** The SLO measures `SIGKILL` → ready. `andara_recovery_duration_seconds`
   measures process start → ready. The script prints both, and their difference is the restart
   term.
2. **`andara_acknowledged_commands_lost_total` is 0 in any fresh process.** `AW-SRV-007` doesn't
   say what increments it after a restart (its feedback file, item 6). Until that's answered,
   AC-4's counter line is presence, not evidence. AC-5's Room 2 carries the RPO assertion.
3. **A row in `$GITHUB_STEP_SUMMARY` per run.** Kill-to-ready, phase `total`, replayed ticks, and
   the round tick. That's the "regression visible per run" `recovery.md`'s exhaustion policy asks
   for. This is new output in the story's own contract, so the Interface contract may want to name
   it. Your call.
4. **Alerts.** The local Prometheus evaluates the chart's rules. `AndaraServerUnavailable` can go
   pending during the kill, and the target asserts on no alert state. `recovery.run` is always
   sampled (only `Game/Submit` is ratio-sampled), so the §8 Tempo check is sound.

## Architecture: contract review, 2026-10-02

Moved to `ready`.

- **Item 2:** the counter is withdrawn (`AW-SRV-007`, feedback item 6), so AC-4's line is removed.
  AC-5's Room 2 carries the RPO, as your §7 says.
- **Item 3:** the `$GITHUB_STEP_SUMMARY` row is now in the Interface contract.
- **Items 1 and 4:** accepted as written.
- **The DoD's open decision:** `AW-SRV-007` inherits this run as its live observation. The read
  list here swaps the withdrawn counter for `andara_restore_total{caller="recovery"}`.
- The `<A> reconnects.` assumption stays as written. `AW-SRV-007` doesn't make the bystander's
  line deterministic, so AC-5 doesn't assert it.

## For architecture: AC-5's `already_live` check can't fail (PM, 2026-10-03)

Found while grooming `AW-INF-034`, which reuses this story's sequence. AC-5 says neither transcript
has an `already_live` refusal. But on a reconnect, `play` never prints one. It waits out
`CodeAlreadyLive`, and shows only the waiting line, "Waiting for your previous session to end."
(`admin/cli/play.go`, around line 250). The reason, `reason=already_live`, appears only in the protocol view, which
`--show-protocol` turns on (`stack_play.sh` runs `play` with it for that reason). As written, the
assertion passes whatever happens.

The fix `AW-INF-034` takes: both clients run with `--show-protocol`, and the check is that neither
`reason=already_live` nor the waiting line appears. This story is `ready`, so PM can't amend it.
Please amend AC-5 the same way, or record why it doesn't need to be.

## For SRE (self), 2026-10-03: compose restarts only an internal failure

`deploy/compose/docker-compose.yaml`'s `andara-server` has `restart: on-failure` (AW-SRV-026 AC-4).
Docker restarts the container only when the process exits non-zero **on its own**. A `docker kill`
or `docker compose kill -s KILL`, and a `stop`, are never undone: the container stays exited with
137 and `RestartCount` unchanged (checked on a throwaway container, 2026-10-03). So the story's plan
stands as written: `SIGKILL`, then the script's own `start`, with no race against Docker.

## For architecture: the script's deviations from the contract, 2026-10-04

`scripts/stack_recover.sh` is built and passes against the local stack. Three points are yours; the full list
is in the story's verification record.
1. **AC-5's `already_live` check.** Built the way PM proposed above: `--show-protocol` on both clients, and
   neither `reason=already_live` nor "Waiting for your previous session to end." after the kill. Please amend
   AC-5 to say so, since the story text still describes a check that couldn't fail.
2. **AC-6's Rooms, and B's.** The roster's Room is written at the unbind, so before the quit `character list`
   still shows A in the Plaza after the move to the Town Hall. The script asserts `dormant town/hall` for both
   after the clean quit. B waits in the Town Hall, not the spawn Room: a despawn goes to the Room's occupants, so
   with A and B apart the "no despawn line" check in AC-5 can't see anything (found at the pre-PR review).
   Please amend AC-5's "B stays in the spawn Room" to match.
3. **"The server's ready line" carries no `trace_id`.** `recovery complete` does, so the script prints that
   one, and the §8 check resolves it in Tempo.

AW-SRV-007's §8 can cite this run: the series, the trace and the 2.2 s kill-to-ready are in the story's record.
`RecoveryStateMismatch`'s firing observation still needs a corrupt-round run, which has no target yet (a §9 gap
AW-SRV-007 assigns to SRE; it's the next piece after this PR).

## For SRE: two items from architecture's §8 review, 2026-10-05

Both are from Codex on PR #429, checked against the tree. The story stays at `review` until they land.
1. **`scripts/stack_recover.sh` line 159** (`andara-server isn't ready`) exits directly, so the failure prints no
   container status and no server log. Route it through `fail`, which prints both transcripts (empty at that point),
   the container's status and exit code, and the last 50 server lines. The two file checks and the RTO check
   before it stay as they are: nothing exists yet to dump or clean, and the contract now says so.
2. **`.github/workflows/stack.yaml`'s `paths` lists** (`push` and `pull_request`) omit `testdata/**`, which compose
   mounts as the content (`deploy/compose/docker-compose.yaml`, `../../testdata/content/valid`) and whose Rooms
   and exits the script hard-codes. Add it to both. While there, check the other inputs the stack bakes in or
   mounts the same way (`content/**` is the one I'd look at first) and add what a stack run depends on. The Test
   plan now says "every PR and merge that touches the stack's inputs, and weekly".

