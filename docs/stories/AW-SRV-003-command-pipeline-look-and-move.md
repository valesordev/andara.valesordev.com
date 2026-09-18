---
id: AW-SRV-003
title: Command pipeline stages split across the log boundary, with look and move
epic: EPIC-03
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-001, AW-SRV-002, AW-SRV-008]
blocks: [AW-SRV-004, AW-SRV-010]
lane: implementation
risk: medium
---

## Context

CLAUDE.md §10 fixes the command pipeline as five stages. ADR-0002 puts the Kafka log in the middle
of them:

```
Gateway:  parse ──▶ authorize ──▶ [ produce to andara.commands.v1 ] ──▶ ack "accepted"
Tick:     [ consume ] ──▶ validate ──▶ apply ──▶ emit events
```

The split is not arbitrary. `parse` is stateless. `authorize` needs only Session and Account
knowledge, which the Gateway has. Both can therefore run before the produce — which also keeps a
hostile client's garbage out of a log we intend to retain for a long time. `validate` and `apply` need
authoritative World state, which only the owning tick has, so both run after the consume.

The consequence a player feels: an ack means *accepted and ordered*, not *succeeded*. Success or
rejection arrives later, as an Event on their stream.

## User story

As a player, I want to type `north` and either move or be told specifically why I cannot, so that the
world's rules are legible to me rather than mysterious.

## Scope

### In scope
- The five stages as separable, individually testable units with explicit inputs and outputs.
- The stage/log boundary and its test: `validate` and `apply` must be unreachable without a consumed
  record.
- Verb table and Intent parsing: verb resolution, abbreviation, argument binding.
- A typed error taxonomy distinguishing pre-log rejection from post-log rejection.
- `look` — describe the current Room, its Exits, and the Entities present.
- `move <direction>` — traverse an Exit, including across a Zone boundary.

### Out of scope
- gRPC transport, Session establishment, and the actual produce — `AW-SRV-005` and `AW-SRV-010`. This
  story is driven by an in-process harness that stands in for the Gateway and a fake record source
  that stands in for the consumer.
- Event delivery to Sessions — `AW-SRV-011`.
- Authentication, roles, and the audit record — `AW-SRV-008`, which shipped `auth.Authorizer`. This
  story calls it; it does not extend the role model.
- Every other verb.

## Acceptance criteria

1. **Given** a Character in a Room with three Exits **when** `look` is applied **then** exactly one
   `RoomDescribed` Event is emitted naming the Room title, description, all three Exit directions, and
   every other Entity present.
2. **Given** a Character in a Room with a `north` Exit **when** `move north` is applied on tick `T`
   **then** the Character's Room is the target Room at the end of tick `T`, and `CharacterLeft` and
   `CharacterArrived` Events are emitted for source and target Rooms.
3. **Given** a Character in a Room with no `west` Exit **when** `move west` is applied **then** no
   state changes and `validate` returns `ErrNoSuchExit` with the attempted direction.
4. **Given** an Intent whose verb is not in the verb table **when** it is parsed **then**
   `ErrUnknownVerb` is returned at `parse`, **and nothing is produced to the log**.
5. **Given** an Intent `n` and a verb table where `north` is abbreviable **when** parsed **then** it
   resolves to the same Command as `move north`.
6. **Given** an Intent `move` with no direction **when** parsed **then** `ErrMissingArgument` is
   returned at `parse`, naming the missing argument, and nothing is produced.
7. **Given** a Command from a Session bound to no Character **when** it reaches `authorize` **then**
   `ErrNotAuthorized` is returned **and nothing is produced to the log**. An unauthorized Command must
   not consume a log offset.
8. **Given** a Command rejected at `validate` **when** the rejection occurs **then** it *has* consumed
   an offset — it was legitimately ordered and turned out to be illegal — a `CommandRejected` Event is
   emitted to the actor, and World state is unchanged. Rejection is never partial.
9. **Given** a Character moving into a Room in a different Zone **when** the Command is applied **then**
   a Command is produced to the target Zone's Partition and the arrival resolves on a later tick, per
   ADR-0001 §4 — never as a synchronous call, including when both Zones are in this process.
10. **Given** any rejection at any stage **when** it occurs **then**
    `andara_command_rejected_total{stage, code}` increments, and the `stage` label distinguishes
    pre-log from post-log.
11. **Given** the `validate` and `apply` functions **when** a test attempts to call them on a Command
    that did not come from a consumed record **then** the type system or an explicit guard prevents it.
    The log boundary is structural, not a convention.
12. **Given** an Intent containing 64 KB **when** parsed **then** it is rejected with
    `ErrIntentTooLarge` before allocation proportional to its length beyond the configured limit.

## Interface contract

*Re-groomed 2026-09-18 to the seams `AW-SRV-002` and `AW-SRV-008` shipped; the original sketch
predated both and named types that do not exist. The stage rules and ACs are unchanged.*

**Shipped types this story implements against** (not sketches — read the code):

| Seam | Where | What this story does with it |
|------|-------|------------------------------|
| `andara.log.v1.LoggedCommand` with `Look` (10) and `Move` (11) arms, `zone_id`, `actor_id`, `session_id`, `client_ref` | `docs/specs/protocol/andara/log/v1/log.proto` | the only post-log input; `parse` produces one, `apply` consumes one |
| `sim.Record{Partition, Offset, Command}` | `server/sim/engine.go` | the consumed form; `validate`/`apply` are reachable only through it (AC-11) |
| `sim.Apply func(*sim.ApplyContext, *logv1.LoggedCommand) error`, registered in `sim.Config.Handlers[sim.CommandKind]` | `server/sim/engine.go` | one handler per verb: `"look"`, `"move"`; unregistered kinds are already rejected `unsupported_command` |
| `sim.ApplyContext{Tick, World, Templates, Zone, State, RNG, Record}` with `Emit(env)` and `Produce(cmd)` | same | `Emit` for `RoomDescribed`/`CharacterLeft`/`CharacterArrived`; `Produce` for the cross-Zone arrival (AC-9) |
| `*sim.RejectError{Code, Message}` → `CommandRejected{code, message}` to the actor | same | the post-log rejection path (AC-8); `Message` is `Detail` below |
| `auth.Authorizer.Authorize(ctx, verb, auth.Principal, sessionID) error`, `auth.VerbRoles map[verb]Role`, `auth.ErrNotAuthorized` | `server/auth/authorize.go` | the `authorize` stage; the verb table's role column is `VerbRoles` |

```go
// CONTRACT SKETCH — not an implementation; the parts that do not exist yet
package command   // server/command: pre-log, imported by the Gateway, never by sim

type Intent struct { SessionID string; Raw string }          // untrusted
type Verb struct { Name string; Role auth.Role; Args []ArgSpec; Abbrev bool }
type VerbTable struct { /* built in; command.verb_table_path overrides */ }
// Parse binds an Intent to a LoggedCommand arm. No World, no Session state.
func Parse(in Intent, t *VerbTable) (*logv1.LoggedCommand, string /*verb*/, error)
// Authorize is auth.Authorizer.Authorize; this package adds the Character-bound
// check: a Session with no bound Entity is ErrNotAuthorized (AC-7).

package sim
// validate reads state and never mutates it; apply mutates. Both are called
// by the registered handler, in that order, inside applyOne.
func validateMove(a *ApplyContext, m *logv1.Move) error   // ErrNoSuchExit, ErrActorNotFound
func applyMove(a *ApplyContext, m *logv1.Move)            // in-Zone; or Produce for cross-Zone
// EntityState gains position: Room RoomID — hashed, Zone-owned (Data / state impact).
```

### Stage rules, enforced by test

- `Parse` reads no World state and no Session state. It knows the verb table.
- `Authorize` reads Session and Account state (`auth.Principal`), never World state.
- `Validate` is read-only with respect to World state; a test asserts the State Hash is unchanged.
- `Apply` is the only mutating stage and runs inside the tick, through `sim.Config.Handlers`.
- A failure at stage `N` means stages `N+1..5` did not execute, asserted by test.
- Pre-log failures produce no log record. Post-log failures produce a `CommandRejected` Event with a
  stable code and a player-safe message, via `*sim.RejectError`.
- The message is player-facing and must not reveal state the actor cannot perceive. "There is no
  exit west" is fine; "the door west is locked with key #4471" is an information leak.

### Error taxonomy

| Code | Stage | Pre-log | Where it lives |
|------|-------|:-------:|----------------|
| `unknown_verb`, `missing_argument`, `intent_too_large` | parse | yes | `command` |
| `not_authorized` | authorize | yes | `auth.ErrNotAuthorized` (exists) |
| `no_such_exit`, `exit_blocked`, `actor_not_found` | validate | no | `sim.RejectError` codes |
| `zone_faulted` | apply | no | `sim` (exists); its scope — Zone, not Partition — is `AW-SRV-027`'s, and this story adds no handling |
| `unsupported_command` | apply | no | exists in `sim` — a binary behind its content |

### Configuration

| Key | Env | Default |
|-----|-----|---------|
| `command.max_intent_bytes` | `ANDARA_MAX_INTENT_BYTES` | `4096` |
| `command.verb_table_path` | `ANDARA_VERB_TABLE` | built in |

## Data / state impact

Introduces Character position as mutable state — an `EntityID → RoomRef` mapping owned by the Zone
holding the Room, and therefore by the Partition holding the Zone. Position is part of `StateHash` and
so part of every future snapshot.

The pre-log/post-log split has a durable consequence worth stating: **the log contains only Commands
that parsed and were authorized.** It is a record of legitimate intent, not of everything anyone typed.
Unauthorized attempts go to `andara.audit.v1` instead, which is where a security question would be
asked anyway.

## Observability requirements

### Metrics
- `andara_commands_total` — counter, label `verb`. Cardinality bounded by the verb table. Unknown verbs
  never reach this metric with their raw text.
- `andara_command_rejected_total` — counter, labels `stage`, `code`, `pre_log`. Cardinality: 4 × enum × 2.
- `andara_command_duration_seconds` — histogram, label `verb`, label `phase` (`pre_log`, `post_log`).

Session ID, Entity ID, Room ID, and raw Intent text are rejected as labels. Raw Intent text as a label
is both a cardinality bomb and an injection vector.

### Logs
- `debug` per Command applied: `verb`, `actor`, `tick`, `partition`, `offset`, `duration_ms`.
- `info` per `authorize` rejection — unauthorized attempts are worth seeing at default level.
- Required fields in the command path: `ts`, `level`, `msg`, `service`, `env`, `session_id`,
  `trace_id`, `verb`, and `tick`/`partition`/`offset` post-log. `session_id` is mandatory here.
- Raw Intent text at `debug` only, escaped.

### Traces
- `command.execute` — span per Command, spanning the log boundary. Attributes: `verb`, `pre_log`,
  `stage_failed`, `partition`, `offset`.
- Child spans: `command.parse`, `command.authorize` (pre-log), `command.validate`, `command.apply`
  (post-log, child of `sim.tick`). The trace therefore shows the queue time in the log, which is the
  latency a player actually feels.

### Alerts
None here. `andara_command_rejected_total{stage="authorize"}` is a security dashboard panel; alerting
on it needs a defined normal rate, which does not exist until there are real Sessions (`AW-SRV-010`).

## Test plan

- **Unit:** every `ErrCode`. Stage isolation: `Parse` compiles without a World; State Hash unchanged
  after `Validate`; stages `N+1..5` skipped after a stage-`N` failure. The structural guard from AC-11.
  Verb abbreviation and ambiguity. Argument binding. Intent size limit. An information-leak fixture
  asserted against `Detail`.
- **Integration:** two Characters in one Room, one moves, assert both Event streams. Cross-Zone move
  asserting a produce to the target Partition rather than a direct mutation. A pre-log rejection
  asserting the log is empty afterward. A post-log rejection asserting the offset advanced and a
  `CommandRejected` Event was emitted.
- **Manual/operator:**
  ```
  andara-cli sim repl --content ./testdata/content/valid
  > look
  > north
  > west          # expect: no_such_exit, post-log
  > frobnicate    # expect: unknown_verb, pre-log, nothing in the log
  ```
  `sim repl` drives the pipeline in-process with a fake log, which is how it is exercised before
  `AW-SRV-010` exists.

## Definition of done

CLAUDE.md §8, plus:
- Stage isolation and the log-boundary guard are asserted by test, not by code review.
- `Command`, `Intent`, and `Command Pipeline` glossary entries reflect the split (already updated).
- The information-leak assertion on `Detail` exists with at least one real case.

## Open questions

- **Re-groomed 2026-09-18 (review pass).** The original contract named `Command` and
  `LoggedCommand` Go types and a stub `authorize` that `AW-SRV-002` and `AW-SRV-008` had since
  replaced; an implementation written to it would have had nothing to be checked against. The
  Interface contract now names the shipped seams. Back to `ready` in the same pass.
- `[ASSUMPTION]` Verb abbreviation resolves to the shortest unambiguous prefix, with single-letter
  direction aliases. MUD convention. A richer parser changes `parse` substantially but not the stage
  boundaries.
- **Resolved 2026-09-18:** `authorize` is `auth.Authorizer` from `AW-SRV-008`, not a stub; the
  Character-bound check is the one rule this story adds to it. `depends_on` gains `AW-SRV-008`.
- `[NEEDS BRIAN]` Whether the one-tick cross-Zone delay should be perceptible to the player or masked.
  The delay is architectural; its presentation is a design call.
