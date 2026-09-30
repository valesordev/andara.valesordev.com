# AW-SRV-036: goto

Story: `AW-SRV-036` (`draft`).

## For architecture: SRE observability review, 2026-09-28

**Amended.** Checked against `server/command/metrics.go`, `server/command/errors.go` and
`server/tickloop/loop.go`:
- `andara_command_rejected_total` is labelled `{stage,code,pre_log}`, not by verb, so `goto` adds
  no value to it.
- There's no "handoff series". A cross-Zone `goto`'s `Arrive` counts under its own verb in the
  post-log duration histogram.
- A cross-Zone `move`'s target apply is *parented* into the same trace by the carried `trace_id`
  (`server/sim/verbs.go`), not linked. The story now requires `Goto`'s `Arrive` to carry it the
  same way.
- The dedicated `goto applied` line is dropped in favour of the existing `command applied` line,
  plus `from_room` and `to_room`, and the same attributes on the `command.apply` span. Room IDs
  stay out of labels.

**For your contract review:**
- **`usage` isn't a code today.** The existing pre-log codes are `missing_argument` and
  `invalid_argument` at parse. `not_authorized` is the authorize code that
  `andara_command_rejected_total` counts, whatever gRPC status the Submit returns. Either keep
  `usage` and add it to `command.PreLogCodes`, or have AC-5 use the existing codes. The metric's
  code set stays closed both ways.
- **A successful `goto` isn't audited.** Only the denial is (`auth.Authorizer`). A privileged
  teleport is a Game-Master-class power in waiting. Whether a successful `goto` writes an audit
  record is your call. If it does, it's `andara_privileged_actions_total{action="goto"}`.

## Architecture's answers (contract review, 2026-09-28)

- **`usage` is dropped.** AC-5 uses `missing_argument` and `invalid_argument`, with
  `usage: goto <zone>/<room>` as the detail. The pre-log code set doesn't change.
- **A successful `goto` isn't audited.** The `LoggedCommand` is the durable record, with actor,
  Session and trace. Revisit when `goto` reaches `game_master`, or can target another Entity.
- **New pre-seed pair:** `{stage="validate", code="unknown_zone"}` on
  `andara_command_rejected_total`.

## Brian's decision, 2026-09-30: an arrival with no way in reads `<name> has arrived.` (relayed by SRE)

Brian, in the SRE session, answering the question in `docs/feedback/AW-INF-024-purgatory-spawn.md`
("For Brian"):

> For the text of arrival into purgatory or transport from a goto, it should just say "character
> has arrived"

**What SRE takes it to cover:** every arrival that didn't come in through an Exit that leads back.
The bystander's line is `<name> has arrived.` for:
- a `goto` (this story; it answers the `[ASSUMPTION]` in Open questions);
- a new Character appearing in Purgatory on its first bind. Today that's `<name> arrives.`;
- the walk `out` of Purgatory into the plaza, over a one-way Exit. Today that's `<name> arrives from
  the in.`, because the sim sets `from_direction` to the reverse of `out` although the plaza has no
  `in` Exit (AW-SRV-037 AC-3).

A walk through an Exit with a reverse keeps `<name> arrives from the <dir>.`. If that reading of
"arrival into purgatory" is wider than Brian meant, he corrects it here.

### For architecture
Contract, before implementation builds it:
- **`CharacterArrived.from_direction` is empty unless the destination has an Exit back the way the
  mover came.** Today it's the reverse direction regardless. The event and its fields don't change;
  only when the field is set does.
- **`andara-cli play` renders an empty `from_direction` as `<name> has arrived.`,** replacing
  `<name> arrives.` (`admin/cli/render.go` and `simcmd.go`).
- `AW-SRV-036`'s AC that says a bystander reads `<name> arrives.` becomes `<name> has arrived.`.

### For PM
Where it's built. `AW-SRV-036` is in the sprint and touches the same arrival, so it's the natural
carrier, unless architecture splits it. The Builder's Guide (`AW-INF-023`) shows this text in its
walk-through.

### Ordering: the gates, and who changes them
On `main` today, `scripts/stack_play.sh` and `scripts/stack_linkdead.sh` match only
`^<A> arrives\.$`, so the renderer change above would fail both. AW-INF-024 (#269) widens both
patterns to `arrives( from the <dir>)?.` **or** `has arrived.`, so:
- **If #269 has merged when the carrier story lands:** nothing more is needed. The gates accept the
  new text.
- **If it hasn't:** the carrier story can't merge green. Either wait for #269, or make the same
  pattern change to both scripts and route it to SRE, which owns `scripts/`.
The carrier's own test plan names `make stack-play` and `make stack-linkdead` passing with the new
wording. *(Corrected before merge, from Codex on #273: this first said the gates already accepted
it, which is true only on #269's branch.)*

## Implementation, 2026-09-30: built

Built on `impl/aw-srv-036-goto`. The story is at `review`, and the record is in the story. Decided
in building, for architecture's review:

- **Where a bare Room gets its Zone.** `log.proto` says the parser resolves `<room>` from the
  Binding, but `Parse` reads no Session state. The pipeline fills `target_zone_id` from the Binding
  right after authorize, where it sets `zone_id`, so the log still always carries both.
- **Case.** Every verb's arguments are lowercased before they're checked, so `GOTO Docks/Pier` is
  `docks/pier`. Characters outside the ID set, an empty part, or more than one `/` are
  `invalid_argument`.
- **Every `Arrive` describes where it lands**, as the contract review said. That changes a
  cross-Zone `move`: its arrival is now followed by a `RoomDescribed` to the mover.
  `TestMove_CrossZone` and `events`' `TestObserverFollowsEntity` now expect it. It delivers
  `AW-SRV-038`'s cross-Zone half.
- **`goto`'s jump report.** A new `sim.Outcome.Jump` gives the tick loop `from_room` and `to_room`,
  as `<zone>/<room>`, for the `command applied` line and the `command.apply` span. No other verb
  sets it.

## For SRE: `TestLive_Goto` needs a stack built from this branch

AC-1, AC-2 and AC-3 end to end are `internal/smoke/goto_test.go`, `TestLive_Goto`: through the
Gateway, the Command through Redpanda, two Zones' ticks, and the roster after quit. The test plan
asks for exactly that. The shared compose stack runs a server built from your checkout, so I
haven't run it against this code. It works with either spawn Room, as `TestLive_M1Gate` does. Could
you run `make stack-smoke` (or `stack-play`) against this branch, as you did for #272?
