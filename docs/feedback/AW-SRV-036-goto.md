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
