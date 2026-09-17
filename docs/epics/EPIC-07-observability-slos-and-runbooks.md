---
id: EPIC-07
title: Observability, SLOs, and runbooks
phase: 1
component: infra
milestone: M1-M4
status: ready
adr_gates: []
adr_refs: []
---

## Goal
Tick duration, tick overrun, and simulation lag are measured, dashboarded, and alerted against an
SLO with a runbook, from the first server story onward.

## Why now
No ADR gate. Retrofitted tick SLIs are tick SLIs that never land (CLAUDE.md §7). This epic starts
alongside `EPIC-02`, not after it.

## In scope
- Metric, log, and trace conventions: naming, required fields, cardinality bounds.
- Local observability stack in `make up` so instrumentation is verified against a real backend
  during development, not just registered (CLAUDE.md §8).
- Kafka's own signals — broker health, consumer lag, ISR — because ADR-0002 makes log availability
  bound World availability, so the log is now inside the observability perimeter.
- SLO documents in `docs/specs/slo/` — SLI definition, target, window, error budget, exhaustion
  policy.
- Symptom-based alerts only, each paired with a runbook entry in the same story.

## Out of scope
- Alerts on causes. Rejected on sight.
- Per-entity or per-room metric labels. Unbounded cardinality is rejected in grooming.

## Done when
An Operator can answer "is the World healthy" from one dashboard, and every alert that can fire has
a runbook that resolves it.

## Stories
`AW-INF-008` (the chart's signals reach Grafana Cloud), `AW-INF-009` (the chart's rules are evaluated
there). Added 2026-09-17: until then this epic owned no stories by design, its work being carried
inside the stories that create the thing being observed (CLAUDE.md §7 makes instrumentation part of
acceptance criteria rather than a follow-up). The wiring from the chart to a backend that nobody's
story owned is the gap that design left, and these two are it. What the epic is accountable for
beyond them:

| Document | Written by |
|----------|------------|
| `slo/tick-health.md`, `runbooks/simulation-lagging.md` | `AW-SRV-002` |
| `slo/session-availability.md`, `runbooks/sessions-dropping.md` | `AW-SRV-011` |
| `slo/recovery.md`, `runbooks/recovery-state-mismatch.md` | `AW-SRV-007` |
| `slo/kafka-availability.md`, `runbooks/world-read-only.md` | `AW-INF-005`, `AW-SRV-010` |
| `runbooks/kafka-topic-drift.md` | `AW-INF-004` |
| `runbooks/snapshot-stale.md` | `AW-SRV-006` |
| `runbooks/server-unavailable.md`, `runbooks/server-crashlooping.md` | `AW-INF-003` |

An epic with no stories is a reporting gap, not a scope gap. If these documents are missing at M4,
this epic is what names them.
