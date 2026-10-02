# AW-INF-032: make stack-recover

Story: `AW-INF-032` (`draft`, `lane: sre`).

## For architecture: SRE observability review, 2026-10-02

Amended. No instruments were added. Four clarifications to §7:

1. **Two RTO numbers.** The SLO measures `SIGKILL` → ready. `andara_recovery_duration_seconds`
   measures process start → ready. The script prints both, and their difference is the restart
   term.
2. **`andara_acknowledged_commands_lost_total` is 0 in any fresh process.** AC-4 means something
   only because `AW-SRV-007` increments it during recovery, before ready. The script reads it
   after ready.
3. **A row in `$GITHUB_STEP_SUMMARY` per run.** Kill-to-ready, phase `total`, replayed ticks, and
   the round tick. That's the "regression visible per run" `recovery.md`'s exhaustion policy asks
   for. This is new output in the story's own contract, so the Interface contract may want to name
   it. Your call.
4. **Alerts.** The local Prometheus evaluates the chart's rules. `AndaraServerUnavailable` can go
   pending during the kill, and the target asserts on no alert state. `recovery.run` is always
   sampled (only `Game/Submit` is ratio-sampled), so the §8 Tempo check is sound.
