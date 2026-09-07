# Runbooks

One file per alert. An alert that can fire without a runbook that resolves it is an incomplete
story, not a follow-up (`CLAUDE.md` §7).

A runbook states:

1. **What fired, and what the player is experiencing.** Symptom first — that is what the alert is on.
2. **How to confirm** it is real, with the exact commands or dashboard panels.
3. **How to mitigate** — the fastest safe action, which is usually not the fix.
4. **How to diagnose** — the ordered list of things to check.
5. **When to escalate**, and to what.

Every step must be executable through `andara-cli` or standard infrastructure tooling. A step that
says "connect to the database" is a gap in the CLI (`CLAUDE.md` §10).

## Planned

| File | Story | Alert |
|------|-------|-------|
| `simulation-lagging.md` | `AW-SRV-002` | `SimulationLagging` |
| `recovery-state-mismatch.md` | `AW-SRV-007` | `RecoveryStateMismatch` |
| `server-unavailable.md` | `AW-INF-003` | `AndaraServerUnavailable` |
| `server-crashlooping.md` | `AW-INF-003` | `AndaraServerCrashLooping` |
| `snapshot-stale.md` | `AW-INF-003` | `AndaraSnapshotStale` |
