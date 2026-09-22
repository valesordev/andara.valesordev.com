<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Live assertions — asserting on something the system will get around to

> **Status: rule, adopted 2026-09-22.** Written after the third instance of one mistake. It is
> binding on new tests from adoption, and `docs/specs/testing/README.md` tracks the audit of
> existing ones.

A **live assertion** is an assertion whose subject is produced by machinery the test does not
drive directly: a Prometheus series, a registry counter, a projection, an Event on a stream, a
file in an object store, anything downstream of a tick. The test causes something, and then some
*other* goroutine, tick, scrape or broker makes the consequence visible.

Every flaky test this repository has had has been a live assertion that treated "the cause has
happened" as "the effect is visible." They are not the same instant, and on a loaded CI runner
they are not the same second.

The four rules below are each written from a real failure, named.

---

## 1. Poll to a deadline. Never read once.

A single sample of a lagging observation asserts that the effect had landed *by the moment you
looked*, which is a claim about scheduling, not about behaviour.

> `internal/smoke/m1_test.go` scraped `/metrics` once and asserted two gauges from that one
> sample. `andara_sessions_bound` is written gateway-side at bind time;
> `andara_characters_total{state="present"}` is written sim-side once the tick applies the
> `BindCharacter`. A scrape landing between them read `bound=2, present=1` — internally
> consistent, and half a tick early. (Issue #48.)

**Do:** loop until the predicate holds or a deadline passes; fail with what the value actually
was, not just that it was wrong.

**Do not** substitute a fixed `sleep`. A sleep long enough to be reliable on a loaded runner is
long enough to dominate the suite, and it will still be too short on the day it matters.

## 2. Wait on the assertion itself, not on a proxy for it.

This is the rule that looks satisfied and is not. A wait on a *different* signal, chosen because
it is convenient or because it usually arrives later, passes while the thing you are about to
assert is still stale — and it does so while looking deliberate, which is worse than no wait at
all. The next person reads the wait, believes the test is synchronised, and hunts elsewhere.

> `TestRebind` and `TestResume` in `server/egress` waited on `Egress.Sessions()` reaching zero and
> then asserted on a Hub counter. `forget` deletes the map entry, releases the lock, and only then
> calls `s.close()` — so `Sessions()` reads zero while the subscription is still live and its drop
> is uncounted. As `PR #45` put it: *"The tests were reading the second thing before the first had
> caused it."* The fix closed an outer window and left a nested one — `Hub.end` sets `Subscribers`
> inside its lock and increments `Drops` after releasing it — so the same mistake had to be fixed
> twice in one branch.

> `scripts/stack_smoke.sh` waited for `andara_grpc_requests_total` to have series, then asserted on
> the value of `andara_sessions_total{outcome="rejected_version"}`. The first gains its series on
> the run's first RPC; the rejection is several tests later. A scrape inside the ~2 s test window
> satisfied the wait carrying a stale zero. (Fixed in `PR #47`.)

**Do:** poll the exact expression you are about to assert on.

**If you must wait on a different signal**, the doc comment says why it is a valid ordering — that
the signal is set *by the same code path, before* the thing asserted — and a helper that waits for
less than its name implies says so. From `PR #45`: *"an overclaiming helper is how the next person
rebuilds this bug."*

## 3. Absence is anchored, not polled.

You cannot wait for something never to happen; waiting only tells you it has not happened *yet*.
A test that asserts nothing occurred is asserting about a window, so the window has to be closed
by something observable.

**Do:** establish a positive checkpoint that is causally *after* the window, then assert absence
once. The checkpoint must be one the code sets after the thing you claim did not happen, not
merely one that usually arrives later — rule 2 applies here too.

> `TestRebind` claims twenty `Rebind` calls after a Session ended add no unsubscribes. It waits for
> the count to reach two, then asserts it is exactly two. `PR #45` records why that is not
> circular: *"the wait fails if the second unsubscribe never lands, the assertion fails if a
> twenty-first one did, and nothing is subscribed by then so no later increment is possible."*

That last clause is the anchor. Without it the assertion is "no twenty-first has arrived yet."

## 4. Prove the fix by widening the window.

A race fixed by reasoning is a race you believe you fixed. Make it deterministic first.

**Do:** insert a delay into the gap you think is open — inside the teardown, between the unlock
and the increment, wherever the window is — and confirm the test fails **every** run. Then apply
the fix and confirm it passes with the delay still in. Then remove the delay.

> `PR #45` pinned both windows this way: *"a 20ms sleep between that unlock and `Drops.Inc()`
> fails TestRebind on every run, and TestResume does not."* The same technique showed which test
> was genuinely correct and which was lucky — `TestResume` asserts on `Subscribers`, which
> `Hub.end` sets before the drop, so waiting on it *is* a valid signal for what it claims.

A test that cannot be made to fail on demand has not been shown to test anything about ordering.
Record the widened-window result in the commit message; a re-run count alone ("37 runs green") is
evidence of absence, not absence of the race.

---

## Helpers

**One helper per language, reused.** A polling helper reinvented per test is four subtly different
deadlines and four different failure messages.

The shape, not the implementation — the helper itself is the implementation lane's:

```go
// CONTRACT SKETCH — not an implementation
// eventually polls until want returns true or the deadline passes, then fails
// with what the value was. `what` appears in the failure, so it names the
// assertion rather than the wait.
func eventually(t *testing.T, d time.Duration, what string, want func() bool)
```

Existing instances to converge on rather than duplicate: `waitFor` in `server/egress`, and the
bounded `seq 1 30` retry loop in `scripts/stack_smoke.sh` for shell.

A helper's doc comment states **what it waits for and what it does not**. `forgotten` in
`server/egress` is the worked example: it stopped claiming to cover teardown end to end, named the
accounting it leaves out, and pointed at the test that handles it.

## Deadlines

Generous, because the cost of a long deadline is paid only when the test is failing anyway, and
the cost of a short one is paid at random forever. 5–10 s for an in-process signal, 90 s for
anything behind a Prometheus scrape (the default interval is 15 s, and the assertion needs a
scrape that starts *after* the cause).

## Enforcement

By review, and by this document existing to point at. A grep for the shape produces too many
false positives to gate a build on, and a check that has to be argued with gets disabled. If
someone finds a reliable detector it becomes a `make check` target under `CLAUDE.md` §9; until
then, a live assertion in a diff is a thing a reviewer asks about.

## Scope

This is about **ordering**, not about test quality generally. A unit test over a pure function
asserts once, reads once, and none of this applies. The rules bind where the test and the
observation are separated by a goroutine, a tick, a scrape, or a broker.
