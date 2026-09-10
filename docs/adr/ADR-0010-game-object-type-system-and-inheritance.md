---
id: ADR-0010
title: Game Object type system — inheritance, overrides, and where Builder logic runs
status: accepted
date: 2026-09-08
deciders: [brian]
gates: []
---

## Context

Brian, 2026-09-08:

> There will be Entities, Behaviors, and probably some other Game Object types I haven't thought of
> yet. This means inheritance with being able to override values, override functions, and extend with
> new values and functions. This allows a builder to take an NPC of type DOG and make a RABID_DOG that
> has aggro and reacts differently to characters. It allows for a SWORD to become a FIRE_SWORD and be
> able to add fire damage.

That is a clear product requirement and it is the right one — subtyping is how a MUD's content stays
DRY, and "take the dog and make it rabid" is exactly the loop that makes world-building feel fast.

It also touches three accepted ADRs at once, and one of the contacts is a genuine collision rather
than a detail:

- **ADR-0005** decided that *the simulation never executes Python* and forecloses any Builder code
  inside `server/sim`. Behavior lives in out-of-process Behavior Agents.
- **ADR-0007** made protobuf the schema authority. **Protobuf has no inheritance.** Whatever the
  hierarchy is, it is resolved somewhere before it becomes a protobuf message.
- **ADR-0009** chose a purpose-built authoring language. The language now has to express `extends`.

The reason this needs deciding before `AW-CLI-003` writes a grammar is compatibility, not
convenience: a language that cannot express subtyping has to change to add it, and changing the
language is a breaking change to every piece of content already authored.

## The model

Brian, 2026-09-08, specifying the shape:

> Builders can subtype Behaviors freely. I'm going to be mixing two styles with this DSL and that is
> having types as templates that contain components, that way we can allow for component composition
> in an inheritance framework.

So there are two axes, and they do different jobs:

- **Templates** are named Game Types in a single-inheritance hierarchy. `FIRE_SWORD extends SWORD`.
- **Components** are the composable units a Template contains. `FireDamage`, `Wieldable`, `Aggro`.

Inheritance gives you *identity and specialization* — a FIRE_SWORD is a SWORD, and anything that
works on a SWORD works on it. Composition gives you *cross-cutting capability* — `FireDamage` attaches
to a sword and to a dragon without either being related to the other.

This resolves, rather than defers, the question this ADR previously left open about traits. Components
are the trait mechanism. "Flaming" applied to both a sword and a dog is one component attached twice,
not a parent class that both awkwardly descend from.

It also removes the main argument anyone would make for multiple inheritance. Multiple inheritance
exists to let a type acquire capability from more than one place; that is precisely what composition
does, without the diamond problem or a resolution order Builders must memorise. **Single inheritance
plus components is a stronger position than single inheritance alone**, and it is the reason the
decision below is now stated with more confidence than the first draft of this ADR carried.

### Why this fits ADR-0005 unusually well

The component/system split is load-bearing here, not decoration. In the discipline this model comes
from, **components hold data and systems hold logic** — and that is the same line ADR-0005 already
draws between content and behavior.

| | Holds | Written by | Runs |
|---|-------|-----------|------|
| **Component** | data only | Builder, in the DSL | nowhere — it is read |
| **System** | logic over components | Developer, in Go | in the tick |
| **Behavior** | logic over Events | Builder, in Python | in an Agent, out of tick |

The sim never executes anything a Builder wrote, because nothing a Builder writes in the DSL is
executable. That is not a restriction bolted onto the model; it is what the model already says.

### How Behaviors attach

A Behavior binding is a component: `Behavior{class: "andara.builder.pets.RabidDog"}`. That gives one
clean seam between two inheritance systems that otherwise have nothing to do with each other — the
Template hierarchy in content, and the Python class hierarchy in the Agent (ADR-0005).

**Builders may subtype Behaviors freely** (Brian, 2026-09-08). `class RabidDog(Dog)` overriding
`on_event` and calling `super()` is the intended use. The security posture is unchanged by how deep
the Python hierarchy goes: containment is the process boundary and the network policy, not the class
graph.

## The remaining question

The component model narrows the earlier FIRE_SWORD fork to something much sharper:

> **May Builders define new component *types*, or only compose from the ones `andara.core` provides?**

If only core components exist, the ceiling is hard: a Builder can build anything the systems already
understand and nothing else, and every new idea is a feature request into `SRV`.

If Builders may define new component types, the extension path is complete without a line of Builder
code in the tick. A Builder defines `HumsNearDwarves{}`, attaches it to a Template, and the sim
**carries it as opaque data** — stores it, includes it in perception scoping, hashes it into state,
and acts on it not at all. No Go system reads it. A *Behavior* reads it and acts, one tick later,
through the Command pipeline like everything else.

**Recommendation: Builders may define new component types, and unrecognised components are carried
rather than rejected.** This is the whole extension story, it costs nothing in the tick, and it uses
machinery that already exists. The alternative — Builder logic executing inside the tick — is the
Option 2 that the first draft of this ADR costed, and it remains what it was: a sandboxed,
step-bounded, deterministic interpreter whose own behavior becomes part of the state contract
forever. If that is genuinely needed, it deserves its own ADR and its own epic.

The honest cost of the recommendation: **an inert component is invisible until a Behavior exists to
read it**, so a Builder can write something that looks meaningful and does nothing. `content validate`
should warn — not fail — when a Template carries a component no system and no bound Behavior reads.

## Decision

Proposed, pending Brian on the question above. Everything else here is decided.

**Accepted 2026-09-10 (Brian).** Both open questions are answered and appear as decisions 7 and 8.

**1. Templates and Components.** A Game Type is a Template: a named definition in a single-inheritance
hierarchy that contains a set of Components. Components are data. Kinds — Entity, Item, Behavior, and
whatever comes next — are open by construction, since Brian flagged the kind set as incomplete.

**2. Single inheritance, deep chains allowed. Multiple inheritance is not offered**, because
composition already does the job it would be there for.

**3. A Template's components are keyed by component type.** At most one `FireDamage` per Template.
Two of a thing is a list *inside* one component, never two components of the same type — because
override semantics on a bag of duplicates have no good answer, and the bad answers are all silent.

**4. Overriding a component merges field by field; a subtype states only what changes.** Restating
every field to change one is the ergonomic failure this whole model exists to avoid. Two consequences
of merge that have to be stated or they will be discovered: a list-valued field is **replaced, not
appended** — appending is surprising and there is no way to un-append — and merge is shallow past one
level of nesting, which is a constraint on how components are designed rather than a limit on merge.

**5. A subtype may override and extend. It may never remove** — not a value, not a component.
Substitutability is the point: anything holding a SWORD keeps working when handed a FIRE_SWORD. A
`CURSED_SWORD` that cannot be wielded is `Wieldable{enabled: false}`, not a Template with `Wieldable`
deleted. That is a real constraint on component design: **components need their own off switch**, or
Builders will ask for removal within the month.

**6. Components are namespaced by the pack that defines them.** `andara.core.FireDamage` and a
Builder's `pets.Aggro` cannot collide, and provenance is readable at a glance.

**7. Component *types* are defined on the server. Builders compose them; they do not create them.**
Decided 2026-09-10 by Brian, against the recommendation this ADR carried. The recommendation was to
let Builders define new component types carried as opaque data — so record what the decision costs
rather than pretending it is free: **a Builder who needs a component that does not exist files a
GitHub issue and waits for a server release.** That is consistent with the 2026-09-07 decision that
Builders have no repository access, and it makes the core component vocabulary a bottleneck by
design.

What Builders keep: composing existing components onto new Templates, overriding their fields, and
subtyping Behaviors freely (decided 2026-09-08). That is most of the expressive power; what is
withheld is inventing new *kinds* of data.

The tradeoff bought: every component in the World is a type the server understands, so validation,
the State Hash, projections, and the wire format all have a closed vocabulary. Opaque
Builder-defined components would have been unvalidatable by construction — the server cannot check
the invariants of a type it has never seen. The `Revisit when` trigger below is already written for
this: Builders routinely requesting new components is the signal the vocabulary is too thin.

**8. The component model covers Rooms and Zones, not only Entities and Items.** Decided 2026-09-10
by Brian. A Room carrying `andara.core.Dark{}` or `andara.core.NoMagic{}` is the motivating case, and
a Zone carrying Zone-wide properties follows the same shape.

This is additive, not a rewrite, because the seam was left open deliberately:
`andara/content/v1/zone.proto` reserves nothing at `RoomDefinition` field 5 and says in a comment
that ADR-0010's component set attaches there. `AW-SRV-001` shipped `Room` and `Zone` as plain structs
and is in `review`; it is not reopened. `AW-SRV-021` adds the component set to both, and to the
content schema, as new scope.

One constraint this puts on Rooms specifically: a Room's component set feeds the State Hash like
everything else the sim reads, so it obeys ADR-0007 rule 3 — sorted by component type, no maps, no
floats.

**7. Cycles and unbounded depth are compile-time errors**, with a file and line like every other
content error (`AW-SRV-001`'s `ValidationError`). Depth is bounded by a stated constant.

**8. Base types are published as a content pack, not compiled into the binary.** The server publishes
`andara.core` — base Templates and core Component definitions — through the same three topics as any
other pack (ADR-0004), versioned like any other pack. A Builder pack records the core version it
compiled against.

The alternative, base types existing only as Go structs, fails a commitment already made:
`AW-CLI-002` requires `content validate` to work on a laptop with no server and no cluster access, and
a compiler that cannot see `SWORD` cannot resolve `extends SWORD`. Publishing base types as content
makes offline validation work against a cached copy and turns version skew into a legible error —
*compiled against `andara.core@7`, server runs `andara.core@9`*.

Two carve-outs this forces, worth stating rather than discovering:

- Activating `andara.core` is part of a **deploy**, not a moderated Builder action. The two-person
  approval rule (`AW-SRV-013`) governs Builder packs; requiring an approver to ship a server release
  would be an accident, not a policy.
- `AW-INF-007`'s deploy lifecycle gains a step: publish and activate the core pack for the version
  being deployed, before the server reports ready.

**9. Resolution happens at compile time; the resolved component set is what is published**, with the
inheritance chain and each component's originating ancestor retained in the manifest. The sim receives
flat Templates and needs no resolver, which keeps `server/sim` free of a subsystem it would otherwise
have to carry. Provenance matters more under merge than it did under plain value inheritance: "why
does this Template have `Aggro{threshold: 3}`" is a question a Builder will ask about a value that
appears in none of the files they wrote.

## Consequences

- **Core Components become public API for Builders, and this is the loudest consequence here.** Every
  field on `andara.core.Wieldable` is a contract with content that lives outside the repository and
  outlives any binary. ADR-0007's additive-only rule now binds **component definitions**, not only the
  wire format: renaming or removing a field breaks every Builder Template that overrides it, silently,
  in content nobody on the team has read. This changes how `SRV` code is written from the first
  component onward, and no `ready` story says so yet.
- **Component design becomes the mechanics-ceiling decision, made continuously.** What Builders can
  express is bounded by what the core components represent and what systems read them. A component
  designed without an off switch cannot be disabled by a subtype (decision 5); a system that reads a
  hard-coded constant instead of a component field is a wall Builders will hit before we do.
- **ADR-0009's language grows Templates and Components and stays declarative.** `extends`, a component
  set, field-level overrides. It does **not** grow an expression language: components hold data, and
  logic lives in Go systems or Python Behaviors. The corpus-of-source compatibility check ADR-0009
  requires now has to cover inheritance chains and component merges specifically, because that is
  where breaking changes will actually appear.
- **`content validate` gains a resolution stage** — resolve the chain, merge components — and its
  errors must name the Template and component in the chain that is wrong rather than the flattened
  output. It should also **warn on inert components**: a component no system and no bound Behavior
  reads is almost always a mistake, and it is silent by construction (`AW-CLI-002`).
- **The Agent SDK must expose arbitrary components**, including ones it has no generated type for.
  Carried-but-unrecognised components are the entire Builder extension path, and they are useless if
  a Behavior cannot read them. That is a deliberate addition to `AW-SRV-016`, not a later bug fix.
- **`AW-SRV-001`'s world model is now a scoping question, not just a loader.** Rooms, Zones, and Exits
  are currently plain structs. Whether they are Game Objects with components — a Room with a
  `Dark{}` or `NoMagic{}` component is an obvious want — or whether the component model covers only
  Entities and Items, changes that story's data model. It is `ready` and unblocked, so this needs an
  answer before it is picked up. See Open questions.
- **We are foreclosing** multiple inheritance, removal of values and components, and any Builder code
  inside the tick. The first two are cheap to relax later; the third is not, and reversing it means
  building the sandboxed interpreter this ADR declined.

## Open questions

- **Resolved 2026-09-10 (Brian):** Builders may **not** define new component types — see decision 7,
  which records the cost, because the answer went against this ADR's recommendation.
- **Resolved 2026-09-10 (Brian):** the component model **does** cover Rooms and Zones — decision 8.
  It arrived in time: `zone.proto` had left `RoomDefinition` field 5 open for exactly this, so
  `AW-SRV-001` needed no rework and `AW-SRV-021` carries the addition.
- **Resolved 2026-09-08:** cross-cutting traits — components are the mechanism, so the question is
  closed rather than deferred.
- **Resolved 2026-09-08 (Brian):** Builders may subtype Behaviors freely.

## Revisit when

- Builders are routinely requesting new *components* rather than composing existing ones — the signal
  that the core component vocabulary is too thin, which is a content-design problem with a cheap fix.
- Builders are routinely requesting new *systems* — the signal that data-driven composition has hit
  its ceiling and in-tick execution is worth its cost, which is the expensive one.
- A core component needs a breaking change, at which point "core components are public API" stops
  being theoretical and we learn what migrating Builder content actually costs.
- Field-level merge proves too clever — if Builders cannot predict what a three-deep override
  produces, whole-component replacement is the simpler model and this is where we switch to it.
