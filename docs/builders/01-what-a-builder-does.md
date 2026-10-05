# 1. What a Builder does here

[← The Builder's Guide](README.md)

## Packs, versions, and the Active Pointer

You write content in **packs**. A pack is a directory of `.aw` files with one `pack` line naming
it, and it's the unit you publish, approve, activate and roll back. You can publish only to packs an
Operator has granted you.

Every publish makes a new, numbered **Content Version** of the pack: `glade@1`, `glade@2`, and so on.
A version never changes once it's published. It carries the compiled content and the sources it was
compiled from, so anyone holding the pack can fetch it back.

Publishing doesn't change the World. What the World runs is whatever each pack's **Active Pointer**
names, and moving that pointer is a separate step, **activation**. Rolling back is just moving the
pointer to an earlier version.

## One person publishes, two people activate

Publishing is yours alone. Activation needs an **Approval** first: a second Builder who holds the
same pack, or an Operator, signs off on that exact version. An approval is bound to the version's
bytes, so it never needs renewing, and rolling back to a version that was approved needs no new
approval.

On `dev`, while Brian is the only Builder, an Operator may approve a version they published. That's
a temporary arrangement, recorded as a self-approval. It isn't how a team works, and it goes away
when others build ([section 4](04-your-first-zone.md#approve-it) shows it).

An Operator can also activate without approval, with `--override` and a reason. That's audited, and
it's for emergencies, not for routine work.

## What content can express today

- **Zones, Rooms and Exits.** A Zone is a named set of Rooms. Exits join Rooms by a Direction, and
  each Exit goes one way. Exits can cross into another Zone of the same pack, but not into another
  pack.
- **Components** on Rooms, Zones and Templates: `Dark`, `Indoors`, `NoMagic`, `NoRecall`, `Memory`
  and `Behavior` (the full list is in [section 7](reference.md#component-types)).
- **Templates** that extend `andara.core`'s base types: `Npc`, `Character`, `Item` and `Entity`.

What waits:
- **Behaviors** (the Python that makes an NPC act) aren't built yet. A Template can name its
  Behavior, and the name is recorded, but nothing runs it.
- **Items and NPCs in play** arrive with milestone M4. You can write their Templates now, and they
  compile and load, but nothing places them in a Room yet.
- **Doors, locks and conditions on Exits** aren't decided yet.

The [specification's section 10](../specs/content-language/v1/semantics.md#10-what-the-language-deliberately-cannot-say)
lists what the language deliberately can't say.
