---
id: AW-SRV-003
title: Command pipeline stages split across the log boundary, with look and move
epic: EPIC-03
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-001, AW-SRV-002]
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
- Real authentication and permissions — `AW-SRV-008`. The `authorize` stage exists and is exercised
  against a stub permission model, so the seam is not retrofitted.
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

```go
// CONTRACT SKETCH — not an implementation

package sim

// Intent is untrusted input. It has crossed no trust boundary yet.
type Intent struct {
    SessionID SessionID
    Raw       string
}

// Command is parsed and authorized, ready to be logged.
type Command interface {
    Verb() string
    Actor() EntityID
    Zone() ZoneID     // determines the Partition it is produced to
}

// LoggedCommand is a Command that came out of the log. validate and apply accept
// only this type, so the log boundary cannot be bypassed by accident.
type LoggedCommand struct {
    Command
    Partition int32
    Offset    Offset
}

type MoveCommand struct { actor EntityID; Direction Direction }
type LookCommand struct { actor EntityID }

// --- pre-log, at the Gateway; no World state ---
func Parse(Intent, *VerbTable) (Command, error)
func Authorize(Command, *Session) error

// --- post-log, inside the tick; authoritative World state ---
func Validate(LoggedCommand, *World) error
func Apply(LoggedCommand, *WorldState) ([]Event, error)

type PipelineError struct {
    Stage   Stage    // parse | authorize | validate | apply
    PreLog  bool     // true for parse and authorize
    Code    ErrCode
    Detail  string   // player-safe; must not leak imperceptible world state
}

const (
    ErrUnknownVerb     ErrCode = "unknown_verb"       // pre-log
    ErrMissingArgument ErrCode = "missing_argument"   // pre-log
    ErrIntentTooLarge  ErrCode = "intent_too_large"   // pre-log
    ErrNotAuthorized   ErrCode = "not_authorized"     // pre-log
    ErrNoSuchExit      ErrCode = "no_such_exit"       // post-log
    ErrExitBlocked     ErrCode = "exit_blocked"       // post-log
    ErrActorNotFound   ErrCode = "actor_not_found"    // post-log
    ErrZoneFaulted     ErrCode = "zone_faulted"       // post-log
)
```

**Stage rules, enforced by test:**

- `Parse` reads no World state and no Session state. It knows the verb table.
- `Authorize` reads Session and Account state, never World state.
- `Validate` is read-only with respect to World state; a test asserts the State Hash is unchanged.
- `Apply` is the only mutating stage and runs inside the tick.
- A failure at stage `N` means stages `N+1..5` did not execute, asserted by test.
- Pre-log failures produce no log record. Post-log failures produce a `CommandRejected` Event.
- `PipelineError.Detail` is player-facing and must not reveal state the actor cannot perceive.
  "There is no exit west" is fine; "the door west is locked with key #4471" is an information leak.

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

- `[ASSUMPTION]` Verb abbreviation resolves to the shortest unambiguous prefix, with single-letter
  direction aliases. MUD convention. A richer parser changes `parse` substantially but not the stage
  boundaries.
- `[ASSUMPTION]` The `authorize` stage runs against a stub returning "allowed for a Session bound to a
  Character" until `AW-SRV-008`.
- `[NEEDS BRIAN]` Whether the one-tick cross-Zone delay should be perceptible to the player or masked.
  The delay is architectural; its presentation is a design call.
