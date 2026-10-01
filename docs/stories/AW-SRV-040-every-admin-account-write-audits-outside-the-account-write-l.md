---
id: AW-SRV-040
title: Every Admin account write audits outside the Account write lock
epic: EPIC-08
component: server
type: chore
status: draft
size: S
depends_on: [AW-SRV-035]
blocks: []
lane: implementation
risk: medium
---

## Context

`auth.Store` serializes Account writes on one write lock (`wmu`). Login and refresh take the same
lock. Codex's review of #266 found that `SetBuilderPacks` wrote its audit record while holding the
lock. On a slow audit topic, any signed-in Account could then make every Account writer (login and
refresh included) wait out the audit timeout on each call. 5bebac0 fixed it for `SetBuilderPacks`
only. A grep of `server/auth` (2026-09-30) finds `audit.Record` reached with `wmu` held in:
- `admin.go`: `Bootstrap`, `CreateAccount`, `CreateAgentAccount`, `IssueInvite`, `ResetPassword`,
  `RevokeInvite`, `SetAccountStatus`, `SetRegistrationMode` and `SetRoles`;
- `ops.go`: `Register`, `Refresh` and `Revoke`.

Not every hit is necessarily a real defect. It's a candidate list, and this story settles it. Source:
`docs/feedback/AW-SRV-035-pack-grants.md`, "For PM". It isn't on the demo path.

## User story

As an operator, I want a slow audit topic to slow down only the call being audited, so that logins
and refreshes don't queue behind one Admin write.

## Scope

### In scope
- Every `auth.Store` method that takes `wmu` writes its audit records after releasing it, in the
  shape 5bebac0 gave `SetBuilderPacks`.
- A refusal that needs no state (for example, a missing role) never takes `wmu`.

### Out of scope
- The audit topic's own latency or timeout.
- Read-only methods, which don't take `wmu`.

## Acceptance criteria

1. **Given** an audit sink that blocks for its whole timeout **when** any Account write in `auth.Store`
   runs concurrently with a `Login` **then** the `Login` completes without waiting on the audit.
   This holds for each method in the Context list, and for any other method that takes `wmu`.
2. **Given** each such method **then** exactly one audit record is written per outcome (ok, denied,
   failed), with the same fields as before the change.
3. **Given** a refusal decided from the caller's roles alone **then** `wmu` is never taken. The test
   asserts this with a lock-observing hook.
4. **Given** a static check in `make check` (a test over `server/auth`) **then** no function reaches
   `audit.Record` while holding `wmu`.

## Interface contract

No API change. The ordering invariant: within `auth.Store`, `audit.Record` is never called with `wmu`
held. Audit records keep their current fields and outcomes.

## Data / state impact

None.

## Observability requirements

- **Metrics:** none new. The existing audit metrics keep their counts (AC-2).
- **Logs:** none new.
- **Traces:** none new. The existing Admin spans now end before audit latency stops being charged to
  other callers.
- **Alerts:** none.

## Test plan

- **Unit:** a blocking audit sink, with `Login` racing each write (AC-1). Audit-record equality, before
  and after (AC-2). The no-lock refusal (AC-3). The static check (AC-4).
- **Integration:** none beyond `make check`.
- **Manual/operator:** none.

## Definition of done

CLAUDE.md §8.

## Open questions

- `[ASSUMPTION]` `Refresh` and `Revoke` are in scope although they aren't Admin RPCs. They take the
  same lock, so they're the same defect.
