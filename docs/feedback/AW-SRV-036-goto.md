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

### For SRE (self)
`stack-play` and `stack-linkdead` (AW-INF-024, #269) accept both today's `arrives( from the
<dir>)?.` and `has arrived.`, so they pass before and after the change.
