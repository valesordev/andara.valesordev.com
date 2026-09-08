---
id: ADR-0010
title: Game Object type system — inheritance, overrides, and where Builder logic runs
status: proposed
date: 2026-09-08
deciders: [brian]
gates: [AW-CLI-003]
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

## The requirement, decomposed

Brian named four capabilities across an open set of kinds. They do not all cost the same, and two of
them already have homes:

| Capability | Where it lives | Status |
|-----------|----------------|--------|
| Override an inherited **value** | Content data, resolved before the sim sees it | New — this ADR |
| Extend with **new values** | Content data | New — this ADR |
| Override an inherited **function** | Python `class Behavior` subclass in a Behavior Agent | **Already served by ADR-0005** |
| Extend with **new functions** | Same | **Already served by ADR-0005** |

The RABID_DOG example resolves cleanly against what is already decided. "Reacts differently to
characters" is a Behavior: a Builder writes `class RabidDog(Dog)` in Python, overrides `on_event`,
and calls `super()` where it wants the base reaction. Python's inheritance *is* the mechanism, the
process boundary is the sandbox, and determinism is unaffected because the log records the Commands an
Agent submitted rather than the Python that produced them. Nothing new is needed for this case.

The FIRE_SWORD example is the one that does not resolve on its own, and it is the whole decision.

## The discriminating question

**When FIRE_SWORD "adds fire damage", is that a value in a table the combat system already reads, or a
formula the Builder writes?**

```
# Option 1 — a value the sim already understands
FIRE_SWORD extends SWORD {
    damage.fire = 5
}

# Option 2 — a function the Builder writes
FIRE_SWORD extends SWORD {
    fn on_hit(target) { ... arbitrary logic ... }
}
```

These are not two syntaxes for one thing. They are two architectures.

### Option 1 — data-driven mechanics; Builders add values, never in-tick code

Combat, and every other system a Builder can reach, is table-driven. Damage has types; types have
resistances; modifiers are data. A Builder composes existing mechanics with new numbers. Anything
genuinely new in *behavior* is a Behavior Agent, one tick later.

**Good:** ADR-0005 stands exactly as accepted. The sim never runs Builder code, determinism is
unconditional, the tick has no new bound to enforce, and the DSL stays declarative — `extends`, value
overrides, new values, no expression language. Builders are untrusted (ADR-0004) and this is the only
design where that costs nothing.
**Bad:** a Builder can only combine mechanics the server already has. "Fire damage" works if a damage
type system exists; "this sword hums when a dwarf holds it" does not, until someone adds a mechanic
for it. World-building becomes partly a feature-request pipeline into `SRV`.
**Costs us:** the ceiling is set by how data-driven the mechanics are, and that is a design burden
paid continuously, not once. Every system built with a hard-coded assumption is a system Builders
cannot extend, and they will find them faster than we do.

### Option 2 — Builder functions execute in the tick

The type system carries code. Something evaluates it inside the simulation.

**Good:** Builders are genuinely unbounded. The mechanics ceiling disappears.
**Bad:** it reopens ADR-0005 in full. Untrusted code inside `server/sim` needs a sandboxed,
deterministic, step-bounded interpreter — no clock, no randomness outside the seeded PRNG, no
allocation without a ceiling — and a tick budget of 50 ms (ADR-0008) that a Builder's loop must not
exhaust. It also reopens ADR-0002: replay must re-execute that code and get the identical answer,
which means the interpreter itself becomes part of the state contract and cannot change behavior
across versions.
**Costs us:** the in-tick reflex layer ADR-0005 deliberately deferred, plus a language runtime, plus
every determinism bug that follows. This is the largest single piece of engineering anyone has
proposed for this project, and it would be built to serve content that does not exist yet.

### Recommendation

**Option 1**, with one addition that keeps the door open: **Builder-added values the sim does not
recognize are carried, not rejected.** A subtype may declare `hums_near_dwarves = true`; the sim
stores it, exposes it through perception scoping, and does nothing with it — but a Behavior Agent can
read it and act. That gives Builders an extension point for genuinely new behavior at the cost of one
tick of latency, using machinery that already exists, without putting a single line of Builder code
inside the tick.

If that is not enough, the honest answer is that Option 2 needs its own ADR and its own epic, not a
paragraph here.

## Decision

Proposed, pending Brian's answer to the discriminating question. Everything below assumes Option 1.

**1. One type system, generic over kinds.** Entity, Item, Behavior, and whatever comes next are *Game
Object kinds*. Inheritance is a property of the type system, not re-implemented per kind. Brian
flagged that the kind set is incomplete, so it is open by construction: adding a kind must not require
touching the inheritance machinery.

**2. Single inheritance, deep chains allowed.** `FIRE_SWORD extends SWORD extends WEAPON` is fine.
Multiple inheritance is **not offered** — the diamond problem forces a resolution order that every
Builder then has to learn, and the failure mode is silent: content that works until two parents both
define the same value. Cross-cutting composition — "flaming" applied to both a sword and a dog — is
the obvious next request and is deliberately left open below.

**3. A subtype may override and extend. It may never remove.** Removal breaks substitutability, which
is the entire value of the hierarchy: anything holding a SWORD must keep working when handed a
FIRE_SWORD.

**4. Cycles and unbounded depth are compile-time errors**, reported with a file and line like every
other content error (`AW-SRV-001`'s `ValidationError`). Depth is bounded by a stated constant.

**5. Base types are published as a content pack, not compiled into the binary.** The server publishes
`andara.core` through the same three topics as any other pack (ADR-0004), versioned like any other
pack. A Builder pack records the core version it compiled against.

The alternative — base types existing only as Go structs — fails a requirement that is already
committed: `AW-CLI-002` says `content validate` must work on a laptop with no server and no cluster
access. A compiler that cannot see DOG cannot resolve `extends DOG`, so it would need a generated
description of the base types shipped inside the CLI binary — which is the core pack again, with the
worse property that it is versioned with the CLI rather than with the server. Publishing it as content
makes offline validation work against a cached copy and turns version skew into a legible error:
*compiled against `andara.core@7`, server runs `andara.core@9`*.

Two carve-outs this forces, both worth stating rather than discovering:

- Activating `andara.core` is part of a **deploy**, not a moderated Builder action. The two-person
  approval rule (`AW-SRV-013`) applies to Builder packs; requiring an approver to ship a server
  release would be an accident, not a policy.
- `AW-INF-007`'s deploy lifecycle gains a step: publish and activate the core pack for the version
  being deployed, before the server reports ready.

**6. Resolution happens at compile time, and the resolved form is what is published** — with the
inheritance chain retained in the manifest for provenance. The sim receives fully-resolved definitions
and needs no resolver, which keeps `server/sim` free of a subsystem it would otherwise have to hold.
The cost is that a change to DOG does not reach RABID_DOG until RABID_DOG is recompiled and
republished; the manifest records the chain so that "what needs rebuilding after DOG changes" is an
answerable question rather than an archaeology exercise.

## Consequences

- **Server types become public API for Builders, and this is the loudest consequence here.** Every
  field on a base type is a contract with content that lives outside the repository and outlives any
  binary. ADR-0007's additive-only rule now binds **server type definitions**, not only the wire
  format: renaming or removing a field on `DOG` breaks every Builder subtype of it, silently, in
  content nobody on the team has read. This changes how `SRV` code is written from the very first
  Entity onward, and no `ready` story says so yet.
- **The mechanics ceiling is now a design commitment.** Under Option 1, what Builders can express is
  bounded by how data-driven `SRV`'s systems are. That has to be a standing constraint on how
  mechanics get built, or the ceiling arrives by accident.
- **ADR-0009's language grows a type-hierarchy feature and stays declarative.** `extends`, value
  overrides, new values. It does **not** grow an expression language — that only happens under Option
  2. The corpus-of-source compatibility check ADR-0009 requires now has to cover inheritance chains,
  which is where breaking changes will actually appear.
- **`content validate` gains a resolution stage**, and its errors must name the type in the chain that
  is wrong rather than the resolved output, or a Builder debugging a three-deep hierarchy is reading a
  flattened blob (`AW-CLI-002`).
- **Behavior Agents gain a reason to read arbitrary content values.** Carried-but-unrecognized values
  are only useful if the Agent SDK exposes them, which is a small addition to `AW-SRV-016` and worth
  making deliberately rather than as a bug fix.
- **We are foreclosing** multiple inheritance, value removal, and — under Option 1 — any Builder code
  inside the tick. The first two are cheap to relax later; the third is not, and reversing it means
  building what Option 2 describes.

## Open questions

- `[NEEDS BRIAN]` **The discriminating question above.** Everything here assumes Option 1.
- `[NEEDS BRIAN]` Cross-cutting traits. Single inheritance means "flaming" cannot be applied to both a
  sword and a dog without duplicating it. A trait or mixin mechanism solves that and is a real
  addition to the language. It is the most likely next request, and knowing now whether it is wanted
  is much cheaper than adding it after content exists.
- `[NEEDS BRIAN]` Whether Builders may subtype *Behaviors* freely, given ADR-0005 already lets them
  write Python. The type system says yes; the security posture is unchanged either way, because the
  process boundary does the containment. Worth confirming rather than assuming.

## Revisit when

- Builders are routinely requesting new mechanics rather than composing existing ones — the concrete
  signal that Option 1's ceiling has been reached and Option 2's cost is worth paying.
- A base type needs a breaking change, at which point the "server types are public API" consequence
  stops being theoretical and we learn what migrating Builder content actually costs.
- Compile-time resolution proves wrong because base types change often enough that republishing every
  subtype is a chore rather than an event.
