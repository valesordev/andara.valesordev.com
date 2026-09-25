# Feedback — AW-SRV-014 Character roster, selection, and binding

Spec: `docs/stories/AW-SRV-014-character-roster-and-selection.md`
Raised: 2026-09-24, architecture, at the §8 review on `arch/sprint-01-batch-1-review`.

## For implementation

### 1. The bind-applied `info` line is missing (§8: instrumentation)

The Observability section asks for an `info` line on create, select, **bind-applied** and unbind,
each with `account_id`, `character_id`, `session_id` and `trace_id`. Create, select and unbind have
theirs (`auth/characters.go`, `roster.go`). Bind-applied has none. The nearest is
`tickloop/loop.go`'s `command applied` at `debug`, which carries no `account_id`.

It is not waived. `character selected` says the Command was produced. Whether the Character
entered the World, and where, is known only when the `BindCharacter` applies, and that is where the
cases an operator asks about happen:
- a dormant body woken in its old Room
- a present body taken where it stands
- a cross-Zone re-route

The line belongs to the process that applies. The sim itself stays free of logging (CLAUDE.md §10).
So the tickloop, or the roster watching the applied record, logs it from the apply's outcome, with
`zone`, `room`, and whether the body was spawned, woken or already present.

Deliver it on any `impl/` branch that touches `server/tickloop` or `server/roster`, and name this
item in the PR. `AW-CLI-007` is the natural carrier, because its §8 is where this story closes.

## For implementation and PM

### 2. Issue #70: a spawned Character's `content_version` is empty

Not this story's to hold, but its code: `server/sim/character.go` spawns with
`Instantiate(tmpl, id, "")`. The PR #43 review assigned it to `AW-SRV-012`, which never recorded it.
Triage it at the sprint boundary. The issue states the replay consequence of fixing it.
