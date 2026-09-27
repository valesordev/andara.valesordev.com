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

## PM, 2026-09-25 (SPRINT-01 close-out)

### #70: closed

Fixed in PR #93 and closed on 2026-09-25. Nothing to triage.

### 3. For architecture: a relaunch right after a clean quit is refused `already_live`

Found running the SPRINT-01 demo on a fresh stack at `343cdbe`. After `andara-cli play
--character Aldric` quits cleanly (stdin EOF, `CloseSession` answered) from a session that moved,
an immediate second `play --character Aldric` exits `1` with "a character is already live on this
account: Aldric". It happened 4 of 4 times after a session that moved, and 0 of 3 after a session
that only looked. `character list` shows `dormant` on its first poll, about 100 ms later, so the
window is short.

That fits the contract as written. `CloseSessionResponse` returns before the unbind is applied at a
tick, and `AW-CLI-007` AC-7 makes a launch-time `already_live` fatal. But a script that quits and
relaunches trips it, and so will a player on a fast machine. `AW-SRV-015` AC-5 rewrites the quit
path (`UnbindCharacter{QUIT}` → `CharacterDespawned{QUIT}`), so decide there whether
`CloseSession` should answer only after the unbind has applied, or whether the launch case should
retry like the reconnect case does. No story is written for it yet.

## Implementation, 2026-09-26: item 1 delivered

On `impl/aw-srv-014-bind-applied-log`. The line is `character bind applied` at `info`, logged by the
tick loop from the apply's outcome (`tickloop/loop.go`, `Begin`), so `server/sim` still logs
nothing. The bind handler reports a `sim.BindResult` (account, Zone, Room, and what it did) on the
`Outcome` it already returns. Fields: `account_id`, `character_id`, `session_id`, `trace_id`,
`tick`, `zone`, `room`, and `body`:
- `spawned`: a never-bound Character, made at the spawn Room
- `woken`: a dormant body, woken in its old Room
- `present`: a body with no Session, taken where it stands, in this Zone or another
- `rerouted`: a dormant body in another Zone. The line names that Zone and Room, and that Zone's
  apply logs `woken` a tick later.

A rejected bind logs no such line; `command applied` at `debug` carries its code. Tests:
`TestLoop_LogsTheAppliedBind` and `TestBind_OutcomeReportsWhatTheBindDid`.

Review of #98: `character_id` is the body the bind resolved (`BindResult.Character`), not the
record's `actor_id`. A `BindCharacter` with no `actor_id` applies to the payload's `character_id`,
and the line now names that one instead of an empty string. `TestLoop_LogsTheAppliedBind` covers it.

## Architecture's answer to §3 (2026-09-26, contract review of AW-SRV-015)

`CloseSession` answers after the teardown has run, meaning the `UnbindCharacter{QUIT}` is durable and
the Account's live flag is free. It doesn't wait for the unbind to apply: the next `BindCharacter`
goes to the same Zone Partition after the unbind, so log order puts it second. The contract is in
`andara/game/v1/game.proto`'s `CloseSession` comment and `AW-SRV-015` AC-5, and `AW-SRV-015`
implements it because it rewrites that path anyway. `AW-CLI-007` AC-7 stays as written: a
launch-time `already_live` is fatal, and after this change a clean quit never produces one.


## Architecture, 2026-09-26: §8 pass on #98, for implementation

The story stays `review` for one item. The full record is in the story under "§8 pass
(2026-09-26)".

### 5. `character bind applied` points Loki at a trace that doesn't exist

On the compose stack at `3e0adf6`, the line is right on stdout. There, `trace_id` is the
SelectCharacter trace, and Tempo has it. In Loki, the same line's `trace_id` is different, and
Tempo returns 404 for it:

| Where | `trace_id`s seen | Tempo |
|---|---|---|
| stdout (the attribute) | `cb07e806…`, `6c86cd87…`, `fe54d665…` | each is a SelectCharacter trace holding the bind's `command.apply` |
| Loki (the record's) | `495ea54e…`, `e1d1c960…`, `551b6066…` | 404 for each |

`server/tickloop/loop.go` logs it with `l.log.LogAttrs(ctx, …)`, where `ctx` is the loop's. The OTel
log bridge sets the record's trace context from `ctx`, and Loki indexes that, not the
attribute. `sim.tick` is sampled one in a hundred, so the trace it names is almost never exported.

**Fix:**
- Log with `trace.ContextWithSpan(ctx, span)` for the `command.apply` span, as `content swap
  applied` does with `sctx`. The `debug` `command applied` line has the same fault and gets the
  same fix.
- Add an assertion to `TestLoop_LogsTheAppliedBind` that the record's span context is
  `command.apply`'s.

Worth a grep while you're there: any `LogAttrs(ctx, …, "trace_id", X)` where `X` isn't `ctx`'s
span has this fault.

## Implementation, 2026-09-26: item 5 delivered

The work is on `impl/aw-srv-014-apply-log-trace`.

**Item 5 itself.** Two lines now log under the `command.apply` span's context (`actx`):
- `character bind applied`
- `command applied`

Their `trace_id` attribute is read from that same context. `TestLoop_LogsTheAppliedBind` asserts
that each record's span is a `command.apply` span and that the attribute agrees with it.

**The grep turned up three more lines:**

| Line | Fault | Fix | Test |
|------|-------|-----|------|
| `character despawned` at an expiry (AW-SRV-015) | Its trace is `sim.tick`'s, kept one tick in a hundred | A tick that applies a linkdead step with no Command behind it is kept | `TestLoop_KeepsTheTickThatExpiresALinkdeadBody` |
| `session closed` on a dropped connection | No trace context on the record; the attribute fell back to the Session's span | Logs under `session.lifetime` | `TestSessionStore_DroppedCloseLogsUnderTheSessionSpan` |
| The Loader's `swap produced` and bounded-wait lines | No context on the record; the attribute named `content.load` | `InfoContext` / `WarnContext` under `content.load` | `TestLoader_LinesLogUnderContentLoad` |

Every other `trace_id` attribute in `server/` is read from the context its line is logged with.
The audit lines' `rec.TraceId` is also read from that context.
