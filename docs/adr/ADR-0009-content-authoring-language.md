---
id: ADR-0009
title: A purpose-built text language for authoring content
status: accepted
date: 2026-09-07
deciders: [brian]
gates: []
---

## Context

ADR-0007 made protobuf the single schema authority for the wire, the log, snapshots, and content, and
named the cost in the same breath: **protobuf is not hand-authorable.** ADR-0004 then put content
outside the repository and gave Builders no access to it, so the only way a Zone reaches the World is
through `andara-cli`.

Those two decisions together create an obligation neither of them discharges. A Builder has to write
*something*, that something has to become canonical protobuf, and nobody is going to hand-write
protobuf text for a room description. `AW-CLI-003` names this as in scope rather than a later nicety,
which is correct: if world-building is unpleasant, the decision to keep Builders out of the repository
has bought nothing, because there will be no Builders.

The constraint that makes this non-obvious is that the authoring format is a **second schema
surface**. Whatever it is, it has its own syntax, its own versioning, its own error messages, and its
own compatibility obligations — and content authored in it lives longer than any binary that reads it.

## Options considered

### Option A — YAML mapped structurally onto the protobuf messages

Builders write YAML whose shape mirrors the generated types; a mechanical mapping compiles it.

**Good:** no grammar, no parser, no editor tooling to build. Familiar to anyone who has touched
Kubernetes. Diffs well. Schema-generatable from the protobuf, so the two cannot drift.
**Bad:** the shape of a wire format and the shape of a pleasant authoring format are not the same
shape. Rooms and Exits are a graph; YAML makes you write a graph as nested maps with string
cross-references, and every mistake is a runtime lookup failure rather than a syntax error.
**Costs us:** every awkwardness of the wire format becomes an awkwardness the Builder feels, forever.
Prose — which is most of what a MUD's content *is* — sits badly in YAML: indentation-sensitive
multi-line strings are the single most reliable source of "why is my text mangled".

### Option B — Protobuf text format directly

Builders write `.textproto`.

**Good:** zero translation layer. Impossible to drift.
**Bad:** it is a serialization format wearing a costume. No prose ergonomics, no domain vocabulary,
error messages in terms of field numbers.
**Costs us:** the Builder population, immediately.

### Option C — A purpose-built text language that compiles to canonical protobuf

A small language whose vocabulary is the domain — Zones, Rooms, Exits, Items, NPCs, dialogue — with a
compiler in `andara-cli` that emits the canonical protobuf `AW-SRV-013` accepts.

**Good:** the authoring surface can be shaped around how world-building actually reads, and
cross-references can be checked at compile time with an error that names a line rather than at load
time with an error that names a field. Domain vocabulary means the language teaches the model.
**Bad:** it is a real compiler — grammar, parser, error reporting, formatter, editor support — and all
of it is ours to build and maintain.
**Costs us:** a second thing to version. And error-message quality becomes a feature with an owner,
because a compiler with bad errors is worse than YAML with bad errors: at least YAML's failures are
familiar.

## Decision

**Option C.** Content is authored in a purpose-built, text-based language that compiles to the
canonical protobuf. Text-based is explicit: it is diffable, greppable, reviewable, and storable in
whatever a Builder uses to keep their work — none of which a binary or graphical format would be.

ADR-0007 is unchanged. Protobuf remains the single schema authority; this language is a front end to
it and is never itself stored in the content topics. What gets published is the compiled output and
the source, so a Builder can fetch back what they wrote.

`AW-CLI-003` owns the grammar specification. This ADR deliberately does not sketch syntax — that is a
grooming decision with the Builder in the room, not an architecture decision.

## Consequences

- **There are now two schema surfaces, and ADR-0007's additive-only rule has to hold for both.** A
  language change that invalidates previously-authored source is as breaking as removing a protobuf
  field, and it is worse in one way: protobuf compatibility is machine-checkable with `buf breaking`,
  and language compatibility is not, unless we build a corpus of source files that must keep
  compiling. That corpus is the equivalent of `buf breaking` for this decision and should exist from
  the first release of the language.
- **The compiler is on the Builder's critical path and in `andara-cli`.** A compiler bug is a
  world-building outage. It also means `andara-cli` gains a component with a genuinely different
  character from the rest of it — the CLI is otherwise a thin protocol client.
- **Error messages are a product feature.** The whole justification for Option C over Option A is that
  a Builder gets a compile error naming a line instead of a load failure naming a field. If the errors
  are bad, we have paid for a compiler and bought nothing.
- **`content validate` now means two things**: the language compiles, and the compiled content passes
  `sim.BuildWorld`. `AW-CLI-002` must report both without making the Builder care which stage failed
  more than necessary.
- **We are foreclosing** a schema-generated authoring format, and with it the guarantee that the
  authoring surface cannot drift from the wire format. Keeping them in step is now a maintenance
  obligation with a person attached, not a property of the build.
- **The language must express Templates and Components** — `extends`, a component set, and field-level
  overrides on inherited components — specified in ADR-0010 (2026-09-08). It **stays declarative**:
  components hold data, and logic lives in Go systems or Python Behaviors (ADR-0005). This is a
  type-and-composition feature, not an expression language, and the distinction is the difference
  between a grammar and a compiler with a runtime.

  Two things this makes the grammar responsible for that a plain data language would not be:
  component **namespacing**, so `andara.core.Wieldable` and a Builder's `pets.Aggro` cannot collide;
  and readable **merge semantics**, because a Builder stating one field of an inherited component has
  to be able to predict what the other fields become.
- The corpus of source files that must keep compiling — this ADR's only mechanical check against
  breaking changes — has to cover inheritance chains specifically, because that is where breaking
  changes will actually appear.

## Revisit when

- Builders are routinely working around the language rather than in it — asking for raw protobuf, or
  generating source with scripts because the language cannot say what they mean.
- The compiler's defect rate is high enough that Builders stop trusting `content validate`, at which
  point the compile-time-checking argument for Option C has evaporated.
- A visual or in-game authoring surface (ADR-0004) becomes the primary path rather than a supplement.
  A language optimised for humans typing it is not automatically a good compile target for a tool.
