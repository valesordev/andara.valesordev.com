<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Testing specifications

Conventions that bind test *construction*, as distinct from a story's test plan, which says what
must be covered. A story says "assert the round is incomplete"; these say how to assert it without
writing a race.

They live here rather than in `CLAUDE.md` because they are long enough to need worked examples,
and because each one exists in response to a specific failure that is worth keeping attached to
the rule.

## Current

| File | Status | Covers |
|------|--------|--------|
| `live-assertions.md` | rule, adopted 2026-09-22 | asserting on metrics, projections, streams and objects — anything the test does not make visible itself |

## Audit state

`live-assertions.md` binds new tests from adoption. The sweep of existing ones is tracked as a
GitHub issue and is the implementation lane's; this table records the outcome when it lands.

| Area | State |
|------|-------|
| `server/egress` | `TestRebind`, `TestResume` fixed in `PR #45`, and the source of rules 2–4 |
| `scripts/stack_smoke.sh` | fixed in `PR #47` |
| `internal/smoke` | open — issue #48 |
| everything else | not yet swept |
