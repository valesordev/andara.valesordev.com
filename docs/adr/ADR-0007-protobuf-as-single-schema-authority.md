---
id: ADR-0007
title: Protobuf as the single schema authority for wire, log, snapshot, and content
status: accepted
date: 2026-09-07
deciders: [brian]
gates: []
---

## Context

Four decisions landed together and each needs a serialization format:

- ADR-0003 puts gRPC on the wire, which means protobuf for client messages.
- ADR-0002 puts Commands and Events in Kafka, which need a durable record format that survives years
  of schema evolution.
- ADR-0002 also needs a snapshot format with a migration path.
- ADR-0004 puts content manifests in Kafka, authored by Builders outside the repository.

Choosing independently for each gives four evolution disciplines, four sets of compatibility bugs, and
four code-generation stories. The messages also overlap heavily: a Command on the wire and a Command in
the log are the same Command.

## Decision

**One protobuf schema, in `docs/specs/protocol/`, generating Go for the server and CLI, Python for
Behavior Agents, and TypeScript for the Phase 2 client. It defines the gRPC services, the Kafka record
values, the snapshot format, and the content manifests.**

### Why protobuf earns the log too

The property that matters is not size, it is that **field numbers make additive evolution safe by
construction**. A log record written in 2026 must be readable by a binary built in 2029. Protobuf's
unknown-field tolerance and reserved-number discipline give that without a bespoke versioning scheme,
and they give it identically on the wire and on disk.

### Rules

1. **Additive only within a major version.** Fields are added with new numbers. Removing a field means
   `reserved`, never reuse. Changing a field's meaning is prohibited; add a new field.
2. **`state_version` on snapshots and `TickCompleted` records** (ADR-0002 §4) is a separate integer
   from protobuf field evolution. It gates changes protobuf cannot absorb — a semantic change to how
   state is interpreted.
3. **Deterministic serialization where it is hashed.** Protobuf serialization is not canonical by
   default; map field ordering in particular is unspecified. Anything that feeds `state_hash` uses a
   canonical encoder with sorted keys, or avoids `map` fields entirely. This is a determinism
   requirement (ADR-0002), not a preference, and it is asserted by test.
4. **A schema registry fronts the Kafka topics.** Builders author content outside the repository
   (ADR-0004), so the repository cannot be the only place a schema is validated. Producers register;
   incompatible schemas are rejected at publish, not discovered at load.
5. **Generated code is committed**, so a clone builds without a codegen toolchain, and so a schema
   change is visible in a diff.

### Layout

```
docs/specs/protocol/
  andara/game/v1/*.proto      # Game service, Intents, Events        (ADR-0003)
  andara/admin/v1/*.proto     # Admin service                        (ADR-0003)
  andara/log/v1/*.proto       # Command and Event records, TickCompleted (ADR-0002)
  andara/state/v1/*.proto     # snapshot format                      (ADR-0002)
  andara/content/v1/*.proto   # blobs, version manifests, active pointer (ADR-0004)
```

## Consequences

- **Protobuf's weaknesses are now ours.** No canonical encoding without care (see rule 3), a weak
  type system for domain constraints, and `oneof` as the only sum type. Validation of anything beyond
  shape — a Direction being a real direction, an Exit resolving — stays in the sim core's validator
  (`AW-SRV-001`), which was already the plan.
- **A codegen step in CI** that fails if generated code is stale.
- **The schema registry is a new operational dependency** on the content publish path.
- **Content authored by a Builder is now protobuf**, not hand-written YAML. `andara-cli` must offer a
  human-authorable surface that compiles to it, or Builders will hate this. That is a real cost of the
  decision and it belongs in `AW-CLI-003`'s scope, not discovered later.
- **We are foreclosing** JSON on the wire and in the log. `andara-cli --output json` still emits JSON,
  because that is a human and scripting surface, not a protocol.

## Revisit when

- The canonical-encoding requirement in rule 3 proves harder to hold than a purpose-built snapshot
  encoder would be.
- Builders reject the authoring surface, which is a signal about the tooling rather than the format.
