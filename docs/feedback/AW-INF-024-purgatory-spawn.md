# AW-INF-024: every environment spawns in Purgatory

Story: `AW-INF-024` (`draft`).

## For architecture: SRE observability review, 2026-09-28

No change. A values and script change. AC-5's boot failure uses `AW-SRV-014`'s existing `error`
line and exit code, and that's the only signal the story needs.

## For implementation: `TestLive_M1Gate` assumes the plaza spawn (SRE, 2026-09-30)

`internal/smoke/m1_test.go`'s `TestLive_M1Gate` runs in both `make stack-smoke` and `make stack-play`.
It asserts that a new Character's own arrival is in `plaza` (line 108). It later expects `A`
to be in the plaza with `B` for the `north` walk, and to be freed dormant `in the plaza` (line 182).
With `ANDARA_CHARACTER_SPAWN_ROOM=purgatory/start`, the stack's server spawns in
`purgatory/start`, and the test fails at line 108:
`B's own arrival: zone_id:"purgatory" room_id:"start"`. That was observed on the compose stack on
`sre/aw-inf-024-purgatory-spawn`.

AW-INF-024's scope names the two shell gates but not this test, and `internal/` is implementation's.
So this story can't merge green without an implementation change. The ask:
- **Make the test spawn-agnostic, and land it first.** When a Character's own arrival is in
  `purgatory/start`, it submits `out` and reads its `character_arrived` in `town/plaza`. Otherwise
  it continues as today. Then it passes against both the current `main` and this branch, and
  `main` stays green in either merge order. B walks out before A, as the shell gate does, so B is in
  the plaza to see A arrive.
- **The arrival's `from_direction` out of Purgatory is `in`.** The Exit is one-way, so the existing
  rule names the reverse of `out`. That's AW-SRV-037 AC-3 as written, so assert the arrival's Room,
  not its `from_direction`.

The two shell gates are changed on the branch. With the Go half removed, `stack-play` passes, and
`stack-linkdead` passes whole, on a fresh compose stack.

## For architecture: the contract missed `internal/smoke` (SRE, 2026-09-30)

AW-INF-024's scope named `stack_play.sh` and `stack_linkdead.sh` as "every script that asserts on"
the spawn Room. It missed the Go smoke test those scripts run, which asserts it too. Nothing to
decide: the item above routes the fix. For later contracts that move a spawn or a fixture, `grep`
`internal/smoke` along with `scripts/`.

## For Brian: `<name> arrives from the in.` (answered 2026-09-30)

Walking `out` of Purgatory, a bystander in the plaza reads `Walkerupharojk arrives from the in.`.
The move rule names the reverse of the Exit's direction, and `out` reverses to `in`, even though
the plaza has no `in` Exit. The gates accept it, as AW-SRV-037 AC-3 says to, but it's player-facing
prose. Whether an arrival over a one-way Exit names a direction at all is a design call.

**Answered (Brian, 2026-09-30):** an arrival with no way back, including this one, a `goto` and a
first bind in Purgatory, reads `<name> has arrived.`. It's routed in `docs/feedback/AW-SRV-036-goto.md`.
The gates accept both texts.
