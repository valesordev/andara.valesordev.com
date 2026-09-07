---
id: EPIC-08
title: Identity, accounts, and sessions
phase: 1
component: server
milestone: M2
status: ready
adr_gates: []
adr_refs: [ADR-0006, ADR-0002]
---

## Goal
A human authenticates, picks one of up to five Characters, plays, drops connection, reconnects within
the linkdead grace period, and finds the World as they left it.

## Why now
Phase 1 exit criterion 1. Sequenced after `EPIC-03` and `EPIC-04` deliberately: this is the one epic
with a real security surface, and everything it touches should be stable before credentials exist.

## In scope
- Account records on `andara.accounts.v1`, Argon2id credentials, projected to Postgres.
- Registration modes: `closed`, `invite`, `open`, switchable without a deploy, every change audited.
- Invite codes: single-use, expiring, revocable, issuance and redemption audited.
- Character roster (up to five owned, one live), globally unique immutable names, soft delete with
  name reservation.
- Session lifecycle: authenticate → select → play → linkdead → reconnect or despawn.
- Roles (`player`, `builder`, `game_master`, `operator`, `agent`) checked in `authorize`, before the
  Command reaches the log.
- Audit Events for every privileged action.

## Out of scope
- Email flows, including password reset. A known cliff before the `open` transition; it gets its own
  story then.
- OIDC. A second credential type against the same Account record, if it is ever wanted.
- Multi-live Characters. `[ASSUMPTION]` one live at a time.

## Done when
A Session survives a reconnect across a server restart, credential material appears in no log, metric,
or span, and every privileged action in that window is in the audit topic with an actor and a Session
correlation ID.

## Stories
`AW-SRV-008`, `AW-SRV-014`, `AW-SRV-015`
