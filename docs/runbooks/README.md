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

## Shipped

| File | Story | Alert |
|------|-------|-------|
| `server-unavailable.md` | `AW-INF-003` | `AndaraServerUnavailable` |
| `server-crashlooping.md` | `AW-INF-003` | `AndaraServerCrashLooping` |
| `certificate-expiring.md` | `AW-INF-006` | `CertificateExpiringSoon` |
| `ingress-error-rate.md` | `AW-INF-006` | `IngressErrorRateHigh` |
| `simulation-lagging.md` | `AW-SRV-002` | `SimulationLagging` |
| `world-read-only.md` | `AW-SRV-010`, completed by `AW-INF-005` | `WorldReadOnly` |
| `sessions-dropping.md` | `AW-SRV-011` | `SessionsDroppingAtRate` |
| `snapshot-stale.md` | `AW-SRV-006` | `SnapshotStale` |
| `state-projector-diverged.md` | `AW-SRV-019` | `StateProjectorDiverged` |
| `projection-stale.md` | `AW-SRV-019`, shared with `AW-SRV-017`/`018` | `ProjectionStale` |
| `content-load-failing.md` | `AW-SRV-012` (written by architecture) | `ContentLoadFailing` |

## Planned

| File | Story | Alert |
|------|-------|-------|
| `recovery-state-mismatch.md` | `AW-SRV-007` | `RecoveryStateMismatch` |
| `simulation-consumer-lagging.md` | `AW-INF-005` | `SimulationConsumerLagging` |
| `deploy-and-rollback.md` | `AW-INF-007` | — (procedure, not an alert) |
| `npcs-unattended.md` | `AW-SRV-009` | `NPCsUnattended` |
