---
id: AW-SRV-020
title: Protobuf schema as the wire, log, snapshot, and content contract
epic: EPIC-03
component: server
type: feature
status: review
size: M
depends_on: [AW-INF-001]
blocks: [AW-SRV-001, AW-SRV-005]
lane: architecture
risk: high
---

## Context

ADR-0007 makes protobuf the single schema authority for the wire, the Kafka log, snapshots, and
content. That schema is an interface contract, so CLAUDE.md §2 puts it on Claude Code's side of the
line: it *specifies*, it does not run in the game. It was originally folded into `AW-SRV-005`
alongside the gateway implementation, which put a contract and its consumer in one story assigned to
the implementation lane. This story is that half, split out.

The split is not bookkeeping. Field numbers assigned here are permanent — ADR-0002 makes the log the
authority for all World state, so a record written today must be readable by a binary built in three
years. Getting the shape right before there is a log full of records is cheap. Afterwards it is not
possible.

## User story

As a developer, I want one protobuf schema generating the server, the CLI, the agent SDK, and the
future client, so that a contract change is one reviewable diff rather than four that drift.

## Scope

### In scope
- The `.proto` sources under `docs/specs/protocol/`, in ADR-0007's package layout.
- `andara.game.v1` — the `Game` service and its request/response messages.
- `andara.admin.v1` — the `Admin` service, minimal.
- `andara.log.v1` — `LoggedCommand`, `Event`, `TickCompleted`.
- `andara.state.v1` — the snapshot **envelope** only.
- `andara.content.v1` — blobs, version manifests, active pointer, and `ZoneDefinition`.
- `buf` configuration: lint, and `buf breaking` against `main` so ADR-0007's additive-only rule is
  mechanical rather than a review habit.
- Codegen for Go, Python, and TypeScript, committed (ADR-0007 rule 5).
- The determinism rules for anything that feeds the State Hash, stated in the schema itself.

### Out of scope
- The gRPC/Connect server, TLS, session lifecycle, negotiation, interceptors, drain — `AW-SRV-005`.
- The canonical-encoding **helper**, which is Go and therefore implementation lane. This story states the rule and
  constrains the schema so the rule is satisfiable; `AW-SRV-005` implements and tests it.
- Registering schemas with the registry — `AW-INF-004`, which this story unblocks.
- Any message for a `draft` story: Characters, Accounts, Items, Behaviors, session persistence. Naming
  a field for a story that has not been groomed is how a permanent number gets assigned to a guess.

## Acceptance criteria

1. **Given** the `.proto` sources **when** `make proto` runs **then** Go, Python, and TypeScript are
   generated into `gen/`, and a following `make proto-check` exits 0.
2. **Given** a `.proto` edited to remove or renumber a field **when** `make check` runs **then** it
   exits 1 naming the field and the offending message. ADR-0007's additive-only rule is enforced by
   `buf breaking` against `main`, not by review.
3. **Given** a `.proto` that violates the style rules **when** `make check` runs **then** `buf lint`
   fails naming the file and rule.
4. **Given** any message under `andara.log.v1` or `andara.state.v1` **when** the schema is inspected
   **then** it contains no `map` field, no `float` or `double`, and no `google.protobuf.Any` — the
   three things that make a serialization non-canonical or non-deterministic (ADR-0007 rule 3). This is
   asserted mechanically, not by reading.
5. **Given** the generated Go **when** `make check` runs **then** `gen/go` compiles, `go vet` passes,
   and the `server/sim` depguard boundary is unaffected.
6. **Given** `AW-SRV-001`'s loader **when** it is written **then** `ZoneDefinition`, `RoomDefinition`,
   and `ExitDefinition` exist with a `format_version`, Rooms keyed by ID, and Exits carrying a
   `direction` string and a cross-Zone-capable target.
7. **Given** `AW-SRV-003`'s pipeline **when** it is written **then** `SubmitRequest` carries the
   player's **raw text**, and `andara.log.v1.LoggedCommand` carries the **parsed** command as a
   `oneof`. The wire is text; the log is structured.
8. **Given** a clone with no protobuf toolchain **when** `make bootstrap` runs **then** `buf` is
   installed at the pinned version and `make proto` works.

## Interface contract

### Layout

```
docs/specs/protocol/
  buf.yaml
  andara/game/v1/game.proto        # Game service, Submit, Subscribe    (ADR-0003)
  andara/game/v1/event.proto       # EventEnvelope and event payloads   (AW-SRV-004)
  andara/admin/v1/admin.proto      # Admin service                      (ADR-0003)
  andara/log/v1/log.proto          # LoggedCommand, Event, TickCompleted (ADR-0002)
  andara/state/v1/snapshot.proto   # snapshot envelope                  (ADR-0002)
  andara/content/v1/content.proto  # blobs, versions, active pointer    (ADR-0004)
  andara/content/v1/zone.proto     # ZoneDefinition                     (AW-SRV-001)
```

Generated output goes to `gen/go`, `gen/python`, `gen/ts` and is committed.

### The distinction this story exists to fix

`AW-SRV-003` defines `Intent{SessionID, Raw string}` as untrusted input and puts `Parse` at the
Gateway, before the log. So:

| | Carries | Why |
|---|---------|-----|
| `game.v1.SubmitRequest` | the player's **raw text** | It has crossed no trust boundary. A MUD's wire protocol is text, and parsing is a Gateway stage the player cannot skip. |
| `log.v1.LoggedCommand` | a **parsed, authorized** `oneof` | It is past parse and authorize. `Validate` and `Apply` accept only this, so the log boundary cannot be bypassed by accident. |

Building a structured intent on the wire would move parsing to the client, which is a client-authority
violation (CLAUDE.md §1) dressed as a schema decision.

### Determinism rules, enforced by the schema

Anything that feeds the State Hash — `state.v1`, `log.v1.TickCompleted`, `log.v1.Event` — obeys:

| Rule | Reason |
|------|--------|
| No `map` fields | Protobuf does not specify map ordering; two encodings of one value would hash differently (ADR-0007 rule 3). Use a `repeated` message sorted by key. |
| No `float` / `double` | Floating-point representation and arithmetic are not reproducible across platforms, which breaks replay (ADR-0002). |
| No `google.protobuf.Any` | Its encoding depends on a type registry that can differ between binaries. |
| Prefer `int64`/`uint64` over `Timestamp` | One field rather than two, and no well-known-type dependency in a hashed record. |

### Lint exceptions, and why

`buf`'s `STANDARD` lint is enabled with three exceptions, recorded in `buf.yaml`:

| Rule | Why it is excepted |
|------|--------------------|
| `SERVICE_SUFFIX` | The services are `Game` and `Admin`. `andara.game.v1.Game/OpenSession` is written into ADR-0003, `AW-SRV-005`'s operator commands, and `AW-CLI-004`. A linter's naming preference does not get to rewrite an accepted interface contract, and the fully-qualified name already carries the package. |
| `RPC_RESPONSE_STANDARD_NAME` | `Subscribe` streams `EventEnvelope` directly. A `SubscribeResponse` wrapper whose only field is the envelope adds a layer to every Event on the hot path to satisfy a naming convention. |
| `ENUM_VALUE_PREFIX` | Enum values are namespaced by their enum rather than by a package-wide prefix. |

Everything else in `STANDARD` is enforced.

### Versioning

- Package paths carry the major version. A v2 is a new package, never a mutation of v1.
- Within a major version: additive only. A removed field becomes `reserved`, and its number is never
  reused (ADR-0007 rule 1).
- `state_version` on the snapshot envelope is a separate integer for changes protobuf cannot absorb —
  a change in how state is *interpreted* rather than encoded (ADR-0002 §4).
- Protocol version in `OpenSessionRequest` is a third, separate integer, for wire-compatibility
  negotiation. `AW-SRV-005` owns its semantics.

## Data / state impact

Defines the record types that will fill `andara.commands.v1` and `andara.events.v1` for the life of the
project, and the snapshot envelope. This story writes no runtime state and performs no migration, but
every field number it assigns is a permanent commitment.

Deliberately **not** defined here: the snapshot *body* (`AW-SRV-006`), Character and Account records
(`AW-SRV-008`, `AW-SRV-014`), Item and Behavior definitions (`EPIC-05`, `EPIC-09`), and anything
implied by ADR-0010, which is `proposed`. Reserved ranges mark where they will attach.

## Observability requirements

This story produces no runtime component. Its observability is build-time:

- **Metrics** — none. Explicitly none.
- **Logs** — `buf lint` and `buf breaking` failures name the file, the message, and the field on one
  line each.
- **Traces** — none. Trace *fields* are defined here (`trace_id` on log records) so that `AW-SRV-005`
  and `AW-SRV-010` can correlate a Command to the Session that submitted it, but nothing here emits.
- **Alerts** — none.

## Test plan

- **Unit:** none — there is no logic. The schema's correctness is asserted by the tooling below.
- **Integration:**
  - `make proto && make proto-check` — regeneration is a no-op against committed output.
  - `buf lint` over the whole module.
  - `buf breaking` against `main`, exercised by removing a field in a scratch tree and asserting a
    named failure.
  - The determinism guard (AC-4) run over `log/` and `state/`.
  - `gen/go` compiles and `go vet` passes as part of `make check`.
- **Manual/operator:**
  ```
  make bootstrap && make proto && make check
  buf lint docs/specs/protocol
  ```

## Definition of done

CLAUDE.md §8, plus:
- Generated code is committed and `make proto-check` gates merges.
- `buf breaking` runs in CI against `main`, with CI fetching enough history for the comparison to be
  real rather than vacuously passing.
- No message under `log/` or `state/` violates the determinism rules.

## Open questions

- `[ASSUMPTION]` Connect's Go implementation (`buf.build/connectrpc/go`), serving gRPC, gRPC-Web, and
  Connect from one definition per ADR-0003. `AW-CLI-001` and `AW-SRV-005` both already assume it.
- `[ASSUMPTION]` `buf` remote plugins for generation. They need network access at `make proto` time.
  Committed generated code means a clone still builds offline; only regenerating needs the network.
- `[NEEDS BRIAN]` The canonical `Direction` set, carried from `AW-SRV-001`. `direction` is a string in
  this schema, which is what lets the loader reject an unknown value with a real error rather than the
  schema rejecting it as an unknown enum member. If Direction becomes a closed enum later, that is an
  additive change to the loader's validation, not to the wire.
