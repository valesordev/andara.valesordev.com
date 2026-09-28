# AW-INF-025: operating the state projector

Story: `AW-INF-025` (`draft`). Issue #80.

## For architecture: SRE observability review, 2026-09-28

**Amended.** The draft said "none new". But enabling the projector on `dev` is the trigger
`projection-freshness.md` deferred its absence rule to ("deferred until the chart enables the
projector anywhere"). So this story now carries:
- **`StateProjectorDown`**, a `ticket` with `slo: projection-freshness`. It keys on the
  Deployment's desired replicas and on no Ready projector being scraped, per namespace. So a
  planned `projector-stop` or `projector-rebuild` (scaled to 0) never fires it, and a crash loop
  does. It uses no `absent()` over every namespace.
- **Its runbook**, `docs/runbooks/state-projector-down.md`. The SLO's *Known gaps* bullet is
  replaced, and there's a `promtool` test for fires, silent-at-0, and silent-in-compose.
- **Target output**, with a final line naming the outcome and elapsed time per target.
- **The rebuild Job is unscraped**, deliberately. A second target under the same job would read as
  a second writer, so the rebuild's duration comes from its log line, and
  `andara_state_rebuild_duration_seconds` isn't used.
- **AC-4's evidence** is the `state projector started` line's `round_tick`, not the lag gauge. The
  gauge reads 0 before the first verified batch (SLO *Known gaps*).
- **`ProjectionStale` during a long rebuild** is left to fire. The SLO counts rebuilds against the
  budget on purpose. `projection-stale.md` gets one line telling the operator to check the rebuild
  first.

Nothing here depends on how you answer the snapshot-source question. Whichever source you choose,
AC-4 is observed the same way.

Alert evaluation in Grafana Cloud waits on `AW-INF-009`, which isn't in SPRINT-03. Until it lands,
the §8 check covers the rule under `promtool` and the series on the real backend.
