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
