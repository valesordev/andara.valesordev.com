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

`live-assertions.md` binds new tests from adoption. The sweep of existing ones ran as issue #50
and landed in `PR #51`: 70 test files, 4 scripts and 1 workflow, each hit classed as *already
polls its own subject*, *a unit assertion over what the test drives*, or *a violation*. Every
violation was widened first (rule 4), fixed, and re-run with the delay still in. The evidence
behind each row — including the widened-window result and the reads deliberately left alone with
the ordering that makes them safe — is `docs/feedback/live-assertions.md`.

| Area | State |
|------|-------|
| `server/egress` | `TestRebind`, `TestResume` fixed in `PR #45`, and the source of rules 2–4; `TestHeartbeat` fixed in the sweep (the counter is written after the frame) |
| `scripts/stack_smoke.sh` | fixed in `PR #51`: waits on the value it asserts. `PR #47` carried the same change and deferred to this one |
| `internal/smoke` | `TestLive_M1Gate` polls the instruments as one predicate (issue #48); the roster and audit waits converge on `internal/eventually` |
| `server/gateway` | `TestConnectionDrop_TearsDownSessions`, `TestRecheck_ClosesRevokedSessions` fixed — both waited on the store and asserted on the counters |
| `server/tickloop`, `server/roster`, `server/ingress` | one sleep-as-proxy each, fixed; `idempotency_test.go`'s windows recorded as false-pass-only |
| `cmd/andara-server` | `/readyz` single read fixed; `TestRun_M1Gate`'s dormant read folded into its poll |
| `server/telemetry` | reads after `ForceFlush`'s fixed sleep replaced by polls |
| `server/events`, `server/boot`, `server/auth`, `admin/cli`, `scripts/stack_play.sh`, `scripts/stream_soak.sh` | swept; single reads are ordered by a synchronous write or a `Flush` barrier |
| `.github/workflows/stack.yaml` | swept 2026-09-22: the broker-outage step's "starvation stopped" was absence over a fixed sleep. Now anchored on ticks having run, both counters from one scrape |
| helpers | `internal/eventually` is the one Go implementation; every package wrapper delegates to it |

## Known windows, accepted

Two claims have no observable to anchor on, and are recorded here rather than left as a comment
someone deletes. Both are false-pass-only: they cannot flake, they can only fail to catch.

- **`AW-SRV-031` AC-5**, "the retry does not take a place in the Session's queue". The ingress
  exposes no signal that a retry has parked on an in-flight key, so the test keeps a 20 ms
  window. Accepted rather than instrumented: an instrument that exists only to be asserted on is
  one more thing to keep true, and every property the window guards is also asserted after the
  calls return. If the in-flight set ever gains a gauge for operational reasons, the test should
  move to it.
- **`server/egress` `TestEscalation_DisconnectsWhenResetDoesNotReturn`**, "a reset that returned
  in time escalates nothing". This is a race against the 50 ms heartbeat interval by
  construction. Making it exact needs a clock seam on the egress — a production interface widened
  for a test, which wants its own story and a reason beyond this one.
