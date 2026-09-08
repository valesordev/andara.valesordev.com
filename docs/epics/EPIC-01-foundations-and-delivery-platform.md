---
id: EPIC-01
title: Foundations and delivery platform
phase: 1
component: infra
milestone: M0-M2
status: in-progress
adr_gates: []
adr_refs: []
---

## Goal
A new machine goes from clone to running local stack to green check with three commands, and CI runs
the same check the developer does. No game code.

## Why now
Every other epic assumes this exists. It has no ADR gate, so it is the only work that can start
without a decision, and it is the cheapest place to establish the conventions (make targets, story
validation, CI) that keep the rest of the repo honest.

## In scope
- Repo scaffold, Go module layout, editor/lint config.
- Makefile as the single automation surface (CLAUDE.md §9).
- Story and ADR tooling: scaffold, validate, backlog generation, dependency graph.
- Local stack: server, datastores, observability, one command up.
- CI skeleton running `make check`.

## Out of scope
- The Kafka layer itself — `EPIC-10`. This epic gets the repo and the local stack shell; the log,
  its topics, and its schema registry are their own epic because ADR-0002 put them in the critical
  path of the first playable tick.
- Image publishing and environment promotion.
- Any simulation code.

## Done when
`make bootstrap && make up && make check` succeeds on a clean machine with no wiki, and CI runs the
identical `make check`.

## Stories
`AW-INF-001`, `AW-INF-002`, `AW-INF-003`, `AW-INF-006`, `AW-INF-007`

## Status — 2026-09-07

`AW-INF-001` and `AW-INF-002` are implemented and in `review`, with every acceptance criterion
executed rather than reasoned about; each story carries its own verification record, including the
criteria that cannot close until a server exists. The M0 gate — `make bootstrap && make up && make
check` on a clean machine — holds.

`AW-INF-003`, `AW-INF-006`, and `AW-INF-007` remain `draft` and are **not** implemented. This is
deliberate, not deferral: all three carry `[NEEDS BRIAN]` questions about the target platform, and
two of them depend on server stories (`AW-SRV-005`, `AW-SRV-007`) that have no code behind them yet.
Writing a Helm chart before the platform is chosen would mean inventing the answer to a question that
is Brian's, and writing probes for a process whose readiness semantics do not exist would mean
guessing at the one thing `AW-INF-003` says must not be guessed. They are groomed when the platform
is known and the server is measurable.

The epic's "Done when" is met for everything that does not require a running `andara-server`.
