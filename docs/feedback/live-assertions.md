<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Live-assertion sweep (#50): the record, for `docs/specs/testing/README.md`

Raised by: implementation lane, 2026-09-22. Branch `impl/live-assertions-audit`.
Subject: every live assertion in the tree, swept against `docs/specs/testing/live-assertions.md`.
The spec's audit table lives under `docs/specs/`, which the implementation lane does not edit,
so the rows are here, ready to paste, with the evidence behind each. Two items below need a
decision from architecture (§5).

## 1. What was swept

Issue #50's recipe (`scrape(`, `Gather(`, `/metrics`, `.Metrics()`, `.next(`, `.Recv(` in Go
tests; `api/v1/query` and `/metrics` in `scripts/`), plus every `time.Sleep` and every
hand-rolled deadline loop in a `_test.go`, plus the `.github/workflows/stack.yaml` steps that
read `/metrics` — 70 test files, 4 scripts, 1 workflow. Each hit was classed as one of: polls
its own subject already; a unit assertion over something the test fully drives; a violation.
Every violation was widened first (rule 4), fixed, and re-run with the delay still in.

## 2. Fixed

| Where | Rule | What was wrong | Widened window → result | Commit |
|---|---|---|---|---|
| `internal/smoke/m1_test.go` `TestLive_M1Gate` (#48) | 1 | one `/metrics` scrape, asserted twice | 500 ms before the loop writes the body gauges: the single read lands 500 ms stale on every run (measured: arrivals in hand at +2 ms, `present` moves at ~+600 ms). On the dev stack a stray present body from an earlier run makes the stale value 2, so `>= 2` passed by accident there; with the threshold at the value the stack reaches (3) the old read fails 3/3 and the poll passes 3/3 with the delay in. On CI's fresh stack the stale read is 1 — #48's failure. | `TestLive_M1Gate: poll the instruments as one predicate` |
| `server/gateway/server_test.go` `TestConnectionDrop_TearsDownSessions` | 2 | waited on `SessionCount()==0`, read `sessions_active`/`sessions_total` once; `close` deletes the map entry first, counts last | 200 ms after the store unlock in `close`: fails 3/3 (`sessions_active` 114–117, dropped 884–890 of 1001); fixed passes 3/3 with the delay in | 513e889 |
| `server/gateway/auth_test.go` `TestRecheck_ClosesRevokedSessions` | 2, 3 | same wait/read shape for `sessions_total{revoked}`; a 60 ms sleep for "a clean recheck closes nothing" | 20 ms: fails 5/5 (`revoked = 1`); fixed passes with 200 ms in. The absence is anchored on a recheck pass having run | 513e889 |
| `server/egress/egress_test.go` `TestHeartbeat` | 2 | `Sent{heartbeat}` read once after the frame; `stream.send` counts after `out.Send` returns | 20 ms before `Sent.Inc()`: fails 10/10 (`sent = 1`); fixed passes 10/10 with the delay in | 14e7614 |
| `server/tickloop/loop_test.go` `TestLoop_DrainTimeout` | 2 | 20 ms sleep as a proxy for "the tick is in the wedged handler" | 50 ms at the top of `Loop.run`: fails 5/5 (`got <nil>`); fixed passes 5/5 with the delay in | fafce69 |
| `server/roster/roster_test.go` `TestRoster_ReleaseDuringSelect` | 2 | 10 ms sleep as a proxy for "the select is mid-produce" | 30 ms before the `SelectCharacter` call: fails 5/5 (live flag left set — the leak the test exists to catch, reported by a run that never raced); fixed passes 5/5 with the delay in | b9943f2 |
| `cmd/andara-server/gateway_test.go` `TestRun_ServesAndDrainsOnSIGTERM` | 1 | `/readyz` read once after the gRPC side answered; the operator listener starts later, on its own goroutine | found by a 500 ms delay inserted for the smoke experiment: fails 4/5 (connection refused); fixed passes 5/5 with the delay in | 75ad29f |
| `cmd/andara-server/m1_test.go` `TestRun_M1Gate` | 2 (shape) | dormant gauge read once after polling its siblings; both are written by the loop, one after the other | 500 ms between the two writes does *not* fail it in 3 runs: the `look` after waking makes the loop write both again, and its `room_described` is causally after the bind tick's writes. Folded into the poll to remove the dependence on that ordering | 3aac318 |
| `server/ingress/ingress_test.go` `TestBindings_Replace` | 2 (false pass) | 10 ms sleep as a proxy for "the waiter is in the hold" | 30 ms before the `Binding` call: *passes* 5/5 — the Unbind lands first and the call returns `ErrNoBinding` without waiting. Now waits on the `Held` gauge | edef2b4 |
| `server/telemetry/logexport_test.go` (two tests) | 1 | reads after `ForceFlush`, which is a fixed `LogExportInterval + 100 ms` sleep in production code | not widened (a fixed sleep is the finding); each read is now a poll on the thing asserted | f045e30 |

Helpers: `internal/eventually` is the one implementation (`True` in the spec's shape, `Observed`
for a failure that says what the value was); the six per-package `waitFor`/`waitUntil` delegate
to it, and the remaining inline deadline loops in `server/auth`, `server/tickloop` (broker
tests), `admin/cli`, and `internal/smoke` go through it (a0461b3, b9894b1, 602d784).

## 3. Judged correct, and why

Single reads left alone because the write is ordered before the signal the test already
waited on — the reasoning rule 2 asks for, recorded so nobody re-derives it:

- `server/events/hub_test.go`: every counter read follows a `Flush`, which the fan-out
  goroutine processes after the `end` that did the counting — a same-goroutine barrier.
  `TestHubDrop` in egress relies on the same `Flush` (in `emit`).
- `server/egress`: `Drops{…}` reads after a stream's error — `subscribe` increments before it
  returns; `Resyncs` after the live frame — incremented before the resync frame is sent;
  `TestRebind`'s no-op read — `Hub.Unsubscribe` is synchronous, so `Rebind` returning is the
  anchor. `server/egress/gateway_test.go` `Streams` after `Drops{buffer_full}` — `Streams.Dec()`
  precedes `Drops.Inc()` on the same path.
- `server/gateway`: `session_test.go` — `connClosed` closes synchronously; `server_test.go`
  reads after `CloseSession`/`Shutdown` — `close`/`closeAll` run inside the call.
- `server/roster`: reads after `Roster.Wait()` — the gauge and counter move before the
  release goroutine's `Done`.
- `server/tickloop/kafka_integration_test.go`: `ReadBoundaries` after `Run` returns — the drain
  checkpoints and `Publisher.Close()` flushes first. The 300 ms pause before the drain is
  shaping, not a wait for an assertion; left in.
- `server/telemetry/logexport_test.go` `Dropped`/`QueueSize` — counted synchronously in
  `OnEmit`. `server/auth/audit_test.go` — the prompt-write read follows `Record` returning,
  which waited on the append.
- `scripts/stack_play.sh`: the three counters read after `play` exits are incremented in the
  handlers before the RPCs answer. `scripts/stream_soak.sh`: the Traefik counter is minutes old
  by the time it is read.
- `admin/cli/play_signal_test.go`: `sessions_total{closed}` after `play` exits —
  `CloseSession` counts before it answers.
- Everything under `server/boot`, `server/command`, `server/auth/store_test.go`,
  `server/sim`, `server/content`, `server/config`: unit assertions over what the test drives.

## 4. Left alone, with the reason

- `server/ingress/idempotency_test.go` sleeps (lines ~112, 239, 395, and the two
  `time.After(20ms)` selects): absence over a window, but each can only false-pass, never
  flake, and every property they guard is also asserted after the calls return
  (`records == 1`, `deduplicated == n`). One claim has no anchor the ingress exposes — see §5.
- `server/egress/egress_test.go` `TestEscalation_DisconnectsWhenResetDoesNotReturn`'s closing
  150 ms window: a wall-clock timing test by design (the heartbeat interval decides), not an
  ordering problem. Making it deterministic means injecting a clock into the egress — see §5.
- `server/ingress/ingress_test.go` `until`: an unbounded spin, used from goroutines that may
  not fail the test. Bounding it needs a `testing.TB` those goroutines cannot `Fatal` on.
- `server/events/hub_test.go:535`, `admin/cli/play_test.go:544`: sleeps that shape a race or a
  backoff, with no assertion resting on them.

## 5. For architecture

1. **`scripts/stack_smoke.sh` is still wrong on `main`.** The spec and the README record it as
   fixed in PR #47, but #47 is open (branch `arch/aw-srv-006-key-and-contract-amendments`),
   and `main` still waits on `andara_grpc_requests_total` having series before asserting the
   value of `andara_sessions_total{outcome="rejected_version"}`. A second copy of the same fix
   sits on the local branch `fix-flaky-stack-smoke` (no PR). This sweep did not touch the
   script: a third copy would only conflict. One of the two should land.
2. **`.github/workflows/stack.yaml`, "the tick loop survives a broker outage"**: fixed sleeps
   with metric samples either side, and `starved_settled == starved_after` is absence over a
   5 s window. It is a rate measurement, and CI config is architecture's; noted, not changed.
3. **`AW-SRV-031 AC-5`'s "the retry does not take a place in the Session's queue"** has no
   observable to anchor on: the ingress exposes no signal that a retry has parked on an
   in-flight key. The test keeps its 20 ms window. Either the claim gets an instrument, or the
   test plan accepts the window as a false-pass-only check.
4. **`TestEscalation`'s "a reset that returned in time escalates nothing"** is a race against
   the 50 ms heartbeat by construction. A clock seam on the egress would make it exact.

## 6. Rows for the README's audit table

| Area | State |
|------|-------|
| `server/egress` | `TestRebind`, `TestResume` fixed in `PR #45`; `TestHeartbeat` fixed in the sweep (counter written after the frame) |
| `scripts/stack_smoke.sh` | fix in `PR #47`, **not on `main`** as of 2026-09-22 (§5.1) |
| `internal/smoke` | `TestLive_M1Gate` polls the instruments as one predicate (issue #48); the roster and audit waits converge on `internal/eventually` |
| `server/gateway` | `TestConnectionDrop_TearsDownSessions`, `TestRecheck_ClosesRevokedSessions` fixed (waited on the store, asserted on the counters) |
| `server/tickloop`, `server/roster`, `server/ingress` | one sleep-as-proxy each, fixed; `idempotency_test.go`'s windows recorded as false-pass-only (§5.3) |
| `cmd/andara-server` | `/readyz` single read fixed; `TestRun_M1Gate`'s dormant read folded into its poll |
| `server/telemetry` | reads after `ForceFlush`'s fixed sleep replaced by polls |
| `server/events`, `server/boot`, `server/auth`, `admin/cli`, `scripts/stack_play.sh`, `scripts/stream_soak.sh` | swept; single reads are ordered by a synchronous write or a `Flush` barrier (§3) |
| `.github/workflows/stack.yaml` | not swept: architecture's (§5.2) |
| helpers | `internal/eventually` is the one Go implementation; every package wrapper delegates to it |
