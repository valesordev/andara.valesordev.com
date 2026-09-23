# AW-INF-011 AC-10 — `measure-tick` against the sizing fixture: paused for architecture

Raised by: implementation lane, 2026-09-22. Updated 2026-09-23 after AW-SRV-006 merged.
Subject: `deploy/helm/andara/measurements.yaml` is still `measured: false`, and `make helm-test`
warns on every run. Implementation was asked to close that out. It is paused, for the reasons
below; §3 is a design decision and is the one that still needs an answer.

**Status on 2026-09-23:** §1 is resolved — the fixture landed. §2 and §3 stand unchanged.

## 1. ~~The premise: the sizing fixture has not landed~~ — resolved 2026-09-23

When this was written, `server/simtest/sizing.go` existed only on the unmerged branch
`aw-srv-006-zone-snapshots`, so generating a `testdata/` fixture from its constants would have
bound it to a branch that could still change.

**Resolved.** AW-SRV-006 merged as PR #46 (`a9428b5`). `server/simtest/sizing.go` is on `main`, and
`server/simtest/sizing_test.go` asserts the fixture is the documented scale, so the constants are
now a stable thing to generate from. §4's plan is unblocked on this axis.

**The scale was amended in the same pass:** `SizingEntities` is **25,000**, up from 10,000 (Brian,
2026-09-22), for headroom as the World grows. `SizingZones` (16), `SizingRooms` (2,000) and
`SizingCharacters` (500) are unchanged — Rooms are topology the boundary copy never touches, and the
Character count is a concurrency assumption rather than a statement about World size. AW-SRV-006's
own feedback doc §7b measured the amended fixture at ~8 ms uncontended against a 15 ms stall budget.
§4's generator reads these constants rather than copying them, so the amendment costs nothing here.

## 2. The lane: AC-10 is AW-INF-011's, and AW-INF-011 is architecture

The request cited AW-INF-003 AC-10. `AW-INF-003` is `done`; on 2026-09-17 its four half-executed
criteria (2, 7, 8, 10) were split out into `AW-INF-011`, which carries them now. `AW-INF-011` is
`lane: architecture`, and its test plan files AC-10 under **Manual/operator**, "recorded in the
verification table with the numbers" — not as an implementation task.

`AW-INF-011` also depends on `AW-SRV-007` (recovery), which is not landed either. AC-7's recovery
measurement and AC-10's tick measurement are meant to be taken and recorded together.

**Needed:** confirmation of who runs the measurement and records it. If architecture wants
implementation to build the machinery and hand it over, that is fine and is scoped in §4 — but the
`measurements.yaml` commit itself is AW-INF-011's deliverable, not ours to make unilaterally.

## 3. The design question: how do 25,000 Entities reach a running server?

This is the real blocker, and it is architecture's call.

`scripts/measure_tick.sh` boots `bin/andara-server` with `ANDARA_CONTENT_SOURCE=dir` against a
content directory. The content directory supplies **Rooms and Templates only**. Verified two ways:
the zone JSON schema (`testdata/content/valid/*.json`) has `rooms` with `exits` and nothing else,
and the Content Language v1 spec has no instance, spawn, or placement concept at all — `entity` is a
template *kind* (`grammar.ebnf:121`), not an authored instance.

`SizingEngine()` does not go through content. It writes Entities straight onto `sim.WorldState`:

```go
e.State().Zones[s.zone].Entities[state.ID] = &state
```

A server booted on a content-dir form of `SizingWorld()` would therefore have 2,000 Rooms and **zero
Entities**. The measurement would be an idle floor again — the exact trap `measurements.yaml`'s
header warns about ("sizing production on it would be worse than this placeholder"). Part of the
fixture without the other part is not worth building; it would produce a number that looks measured
and is not.

### The options, with what each actually costs

**(a) Extend the content format with authored Entity instances.**
A change to the zone JSON schema and to Content Language v1 — `docs/specs/content-language/`. Out of
bounds for implementation under the lane rules, and a v1 format change with a compiler, a corpus,
and `make content-grammar-check` behind it. Worth it only if authored Entity placement is wanted for
its own sake; it should not be driven by a measurement fixture's needs.

**(b) Drive Commands through the memory source.**
Verified there is **no generic spawn Command**. The only path that creates a body is
`BindCharacter` (`server/sim/character.go:95`), which instantiates from `andara.core.Character` at a
spawn Room and requires a Session and a roster binding per body. That reaches at most the 500
Characters, not the 25,000 Entities, and each one costs a session bind. Closing the gap this way
means adding a spawn Command — a protocol change, also architecture's.

Re-verified on `main` 2026-09-23: still no generic spawn Command. What did change is that
`AW-SRV-010` (command ingress) is now `done`, so `helm-test`'s own caveat — "AW-SRV-003's handlers
have no load path until AW-SRV-010, so the placeholder stays on purpose" — is satisfied. A load path
exists now. That strengthens this option without unblocking it: ingress can carry Commands, but
there is still no Command that makes an Entity.

**(c) A debug-only seeding flag on the server.**
Cheapest to build, but it means either importing `server/simtest` into the production binary or
putting the seeding path behind a build tag. That is a new server surface and a decision about
whether test-only code may be reachable from `cmd/andara-server` at all — a call implementation
should not make alone. If this is the answer, please say which form: build tag, or a runtime flag
that is refused unless an explicit debug env var is set.

### One more dependency, whichever option wins

`SizingEngine()` requires the `town.Merchant` template, and it lives in
`testdata/templates/templates/town.Merchant.json` — test data, not `content/core/templates/`.
`testdata/content/valid/templates/` carries only `Entity`, `Character`, `Npc`, `Item`. The
Component-carrying template the fixture leans on (deliberately — the doc comment explains that
Component-less Entities would make the boundary copy look cheaper than it is) has to exist in
whatever content directory the measurement boots against. Either the generator emits it alongside
the zones, or a Component-carrying template moves into the content tree.

## 4. What implementation will do once this is decided

No blocker here, and none of it is started — this is the scope we are ready to pick up:

1. A generator for the content-directory form of `SizingWorld()`, reading the `Sizing*` constants
   from `server/simtest/sizing.go` directly so the two cannot drift — a `go run` target wired into
   the Makefile, writing zone JSON plus the templates the fixture needs, with the output under
   `testdata/` and a check that regenerating is a no-op.
2. The seeding path architecture picks in §3.
3. `scripts/measure_tick.sh` pointed at the generated fixture via `ANDARA_MEASURE_FIXTURE`, with a
   guard that refuses to record when the booted World has no Entities — the same shape as its
   existing refusal when `andara_tick_duration_seconds` is absent. That guard is what stops a future
   run from quietly re-recording an idle floor.
4. `make measure-tick` run, and the result handed to whoever owns the `measurements.yaml` commit
   per §2.

## Notes

- Nothing in the measurement path was changed. `measurements.yaml`, `scripts/measure_tick.sh`, and `testdata/` are untouched;
  this file is the only thing this task added.
- The `Sizing*` constants are an `[ASSUMPTION]` Brian may revise — and did, on 2026-09-22 (§1). Item 1
  of §4 reads them rather than copying them, so that revision stayed one edit, as intended.
- Re-checked against `main` on 2026-09-23: `scripts/measure_tick.sh` and `measurements.yaml` are
  unchanged, and the content-directory format still carries no Entity instances. §3 is open.
  `measurements.yaml`'s `fixture:` string still reads 10,000 Entities; `measure_tick.sh` rewrites
  that field when it runs, so it is left for the real measurement to correct rather than hand-edited.
- This file first reached `main` inside `8f466e7`, an egress commit on `fix-flaky-egress-rebind`,
  rather than through a PR of its own — it landed without the review this lane pauses for.
