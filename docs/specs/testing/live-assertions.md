<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Live assertions — asserting on something the system will get around to

> **Status: rule, adopted 2026-09-22.** Written after the third instance of one mistake. It is
> binding on new tests from adoption, and `docs/specs/testing/README.md` tracks the audit of
> existing ones. Amended 2026-10-10 for Grafana Cloud as the backend (ADR-0012): the section
> "Grafana Cloud subjects" and the deadlines below.

A **live assertion** is an assertion whose subject is produced by machinery the test does not
drive directly: a Prometheus series, a registry counter, a projection, an Event on a stream, a
file in an object store, a Grafana alert instance, a series that has reached Grafana Cloud,
anything downstream of a tick. The test causes something, and then some
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

## Grafana Cloud subjects

`ADR-0012` moved the observable backend from a local Prometheus to Grafana Cloud, read through
`gcx` (decision 2). The four rules above bind unchanged. What changes is what the subjects are,
and four consequences of it, none of which is a new rule.

- **A rule that is *defined* is not a rule that is *live*** (rule 2). `gcx alert rules list` showing
  the rule proves it exists. A test about its behaviour waits on the instance itself, or on the
  rule's `lastEvaluation` moving past the window anchor below, not on the rule being listed.
- **Read the wrapper's states, not `gcx`'s strings.** The wrapper (`scripts/gcx.py`, `AW-INF-046`)
  maps instance states to firing, pending or not firing (`Alerting` and the ruler's `firing` are firing;
  `Normal` and `Normal (NoData)` are not; whether `Recovering` under `keep_firing_for` counts as firing is
  open, `AW-INF-047`). Polling for "not firing" is rule 3's case: it also holds before the rule has
  evaluated at all, so the window needs a window anchor (next bullet).
- **An empty result is not a pass, and two different anchors are needed.** `gcx` can print `null` (for
  example for an empty `alert instances list`), `[]` or an empty `result`; the wrapper normalises all of
  them to `[]` (shapes are recorded in the `AW-INF-046` fixtures), and `[]` means "nothing matched",
  which is also what a wrong stack, a wrong `environment` or a dead pipeline returns. A Grafana-managed
  rule with `no_data_state = "OK"` (ADR-0012 decision 1) over no data also evaluates happily and reads
  `Normal (NoData)`, so a rule that is merely listed and evaluating proves nothing about its input.
  - An **existence anchor** rules out the wrong stack and the dead pipeline: a series of the rule's input
    (or the keep-list's `up{job="andara-server"}`; for an `absent()` clause, the sibling series below) for the same `environment` and `namespace` is queryable,
    and the rule is live, not just listed (`health == ok`, `isPaused == false`, `lastEvaluation` within twice
    the group interval; for the legacy ruler, which
    `alert rules list` cannot see, the rule is confirmed through the unfiltered instance list, per
    ADR-0012 decision 2, which only works while the rule has an instance, as `AndaraServerUnavailable`
    does for prod today; otherwise the check fails closed). It says the observation works. It does **not** license an absence assertion,
    because it can be satisfied by a sample from before the window opened.
  - A **window anchor** closes the window (rule 3), and is causal, not an estimate of when data usually
    arrives. Poll the rule's input until a sample timestamped after the cause is queryable, and note the
    test's own clock `t` at the first poll that sees it (not the sample's timestamp: ingestion sits
    between the two, and an evaluation can start after the scrape and still precede the sample's arrival).
    Then require a rule evaluation that started after `t` plus a 5 s clock-skew allowance (the allowance
    tightens: `lastEvaluation` > `t` + 5 s), with `health == ok` on that read, since `lastEvaluation`
    advances on errored evaluations too. For "nothing fired", extend the window by the rule's `for` plus one
    more evaluation, because a rule with `for > 0` is *pending*, not firing, on the first evaluation that
    sees the fault, and pending counts as not-absent. A single read at the end reports the state *now*, not whether the
    alert fired earlier in the window, so the test polls instance state at every poll from the cause to the
    end of the window and fails on the first firing or pending instance it sees; a forbidden state shorter than one
    poll (5 s) is not observable, but a firing or pending instance lasts at least one evaluation interval
    (60 s by default), so it is visible to 5 s polling. State history is not a substitute: ADR-0012 rejects
    it as retrospective and eventually consistent, so an empty history proves nothing. An instance present
    before the cause is a precondition failure, checked first (ADR-0012 decision 2), and is not counted as
    an observation in the window. Only a window with no
    forbidden state observed, closed by both anchors, is evidence of absence. Two kinds of rule need a different input sample. A rule with an `absent()` clause
    (`AndaraServerUnavailable` also has an `up == 0` clause) has no positive `andara-server` sample after
    the cause, so anchor on a series from the same collector and namespace: poll until a sample ingested after the
    cause is queryable, record a **new** local receipt time `t'` at the first poll that sees it, and require
    an evaluation that started after `t'` plus the skew allowance (for the `absent()` clause `t` is never assigned, because there is no positive input sample to
    observe; the `up == 0` clause does have one; a ruler-backed rule has no evaluation time to compare
    with, see the ruler sentence below). That bounds ingestion lag only, and the
    assertion still waits `for` plus one evaluation after that evaluation. A windowed rule (`StateProjectorDiverged`, `[6h]`) is asserted on the instance
    state at the evaluation after `t`, never on the window. For the legacy ruler, which has no
    `lastEvaluation`: if the pinned `gcx` exposes an evaluation time for ruler instances **[verify in
    AW-INF-046]** that is the anchor (`activeAt` is when the alert became active, not an evaluation
    time); otherwise a ruler-backed absence assertion is out of scope until the ruler is removed.
- **A `--context` mistake reads the wrong stack and looks green.** The wrapper always passes the context
  and the routing test proves it (both `AW-INF-046`; `gcx` pinned at v0.2.13 or later, since v0.2.11
  ignores `--context` in some operations). An assertion helper does not call `gcx` any other way.

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
bounded `seq 1 30` retry loop in `scripts/stack_smoke.sh` for shell. For `gcx`-backed assertions
the wrapper `scripts/gcx.py` (`AW-INF-046`) is the only way to call `gcx`; the polling stays in each
language's `eventually`/`wait_for` helper around it, at 5 s (see Deadlines).

A helper's doc comment states **what it waits for and what it does not**. `forgotten` in
`server/egress` is the worked example: it stopped claiming to cover teardown end to end, named the
accounting it leaves out, and pointed at the test that handles it.

## Deadlines

Generous, because the cost of a long deadline is paid only when the test is failing anyway, and
the cost of a short one is paid at random forever. 5–10 s for an in-process signal, 90 s for
anything behind a Prometheus scrape (the default interval is 15 s, and the assertion needs a
scrape that starts *after* the cause).

Behind Grafana Cloud the delays add, and each is named so a deadline can be computed rather than
guessed: the pipeline's scrape interval, plus ingestion into the stack, plus (for rule state) one
or two rule evaluation intervals (60 s by default, ADR-0012 decision 1) plus the rule's `for`. A series to be visible through `gcx metrics query`: start from 2 minutes. A rule's state to reach
firing through `gcx alert instances list`: `scrape + ingest + 2 × evaluation interval + for`, with
2 minutes standing in for `scrape + ingest` until measured. To **clear**, the clock starts when the
condition ends, not at the cause: `scrape + ingest + 1 evaluation interval + keep_firing_for`, which for
`RecoveryStateMismatch` (`for: 0m`, `keep_firing_for: 15m`) is about 18 minutes with the
2-minute stand-in and no lookback, and up to about 23 with the instant query's lookback of up to 5
minutes when no stale marker arrives (the lookback is its own term:
`scrape + ingest + lookback + 1 evaluation + keep_firing_for`); use a 25-minute deadline (worst case plus a 2-minute margin, recomputed when measured figures arrive), not 3. These are starting values. SRE records the figures the first drills observe
(`AW-INF-046`/`047`) on the story and in the runbook it owns; architecture folds them into this file at
its §8 review of those stories. Poll every 5 s, not faster: `gcx` is a process per call.

## Enforcement

By review, and by this document existing to point at. A grep for the shape produces too many
false positives to gate a build on, and a check that has to be argued with gets disabled. If
someone finds a reliable detector it becomes a `make check` target under `CLAUDE.md` §9; until
then, a live assertion in a diff is a thing a reviewer asks about.

## Scope

This is about **ordering**, not about test quality generally. A unit test over a pure function
asserts once, reads once, and none of this applies. The rules bind where the test and the
observation are separated by a goroutine, a tick, a scrape, or a broker.
