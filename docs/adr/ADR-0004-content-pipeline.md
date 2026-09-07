---
id: ADR-0004
title: Content authoring, versioning, and delivery through the log
status: accepted
date: 2026-09-07
deciders: [brian]
gates: []
---

## Context

Zones, Rooms, Items, NPCs, and dialogue are authored artifacts. How they are written, validated,
versioned, and delivered determines the Builder's workflow and most of the `andara-cli` surface.

Brian has decided: **content is authored outside the repository and stored in the data layer**, so
that Builders need no repository access. That requires content versioning, and the mechanism is
compacted Kafka topics.

That answers the two questions the draft said would eliminate half the option space. Builders are not
Developers, and content ships independently of code — which, as the draft predicted, also forces
ADR-0005's hand.

## Decision

### Content lives in Kafka, addressed by hash, pointed at by a mutable head

Three topics, all compacted. The shape matters, because the obvious single-topic design does not
actually version anything:

| Topic | Key | Value | Why |
|-------|-----|-------|-----|
| `andara.content.blobs.v1` | `sha256` of the body | the content body | Keys never repeat, so compaction never removes anything. Blobs are immutable and permanent. |
| `andara.content.versions.v1` | `packID@version` | manifest: blob refs, parent version, author, timestamp | Keys never repeat, so full version history is retained. |
| `andara.content.active.v1` | `packID` | `{version}` | The only mutable pointer. Compaction keeps the latest, which is exactly what "active" means. |

The trap worth naming: a compacted topic keyed by `packID` holding the content itself would keep only
the newest value. Compaction *deletes* prior versions — that is what it is for. Keying history by
`packID@version` and separating the mutable head into its own topic is what turns compaction from a
history-eraser into a history-preserver.

The result is a content-addressed store with a linked version history and a one-record rollback:
publishing is "write blobs, write a version manifest, move the pointer"; rolling back is "move the
pointer." Both are auditable, both are atomic at the granularity that matters, and neither requires
a deploy.

### Validation runs in three places, from one implementation

`AW-SRV-001` puts the validator in the simulation core specifically so it is reusable. It runs:

1. in `andara-cli content validate`, on the Builder's machine, before publish;
2. server-side at publish time, rejecting the write — this is the authoritative gate, because
   Builders are untrusted and a client-side check is a convenience, not a control;
3. at load, when the server resolves the active version.

A rule that exists in one of the three and not the others is a defect.

### Load model: restart-free, pointer-driven

The server watches `andara.content.active.v1`. A pointer move triggers a content reload. Because
topology is immutable and derived (`AW-SRV-001`), a reload builds a new `World` and swaps it at a tick
boundary.

The hard part is not the load, it is what happens to Entities standing in a Room that the new version
deleted. **Decision: content changes that remove a Room or an Exit are applied, and Entities in a
removed Room are relocated to a designated Zone fallback Room with an Event.** The alternative —
refusing the load — means one Builder mistake blocks every other Builder's work. `[NEEDS BRIAN]` on
whether the player-facing story for that relocation is "you are shunted somewhere" or something with
in-world justification.

### Authoring surface

Out of scope for this ADR and for Phase 1: `andara-cli` is the publish path. Whether a web editor or
an in-game Builder mode is layered on later is a product decision, and both would publish through the
same three topics, so neither is foreclosed. `[NEEDS BRIAN]` — in-game building is a MUD tradition and
if it is a design goal rather than a convenience, it deserves its own epic.

## Consequences

- **The content store is now a security boundary.** Builders can write to it without repository
  access, which means publish-time authorization and audit are Phase 1 concerns, not later ones. Every
  publish emits to `andara.audit.v1` with author, pack, version, and blob hashes.
- **Content and code can now disagree.** A content pack authored against a newer `format_version` than
  the running server supports must be rejected at load with both versions named, and — because the
  pointer may have already moved — the server must continue serving the last version it could load
  rather than failing to boot. This is a new failure mode the in-repo design did not have.
- **Blob storage grows monotonically.** Content-addressed blobs are never deleted by compaction. That
  is the correct trade for auditability, but it needs a retention story before the topic becomes the
  largest thing in the cluster. Large binaries — if Phase 2 art ships this way — do not belong in
  Kafka at all; they belong in object storage with the hash in the manifest.
- **We are foreclosing** content in the server image, and with it the guarantee that a given binary
  always sees a given world. Reproducing a bug now requires naming a content version as well as a
  build. Both go in `andara_build_info` and in every bug report.
- ADR-0005 is now constrained: content ships independently of code, so Behaviors cannot be compiled Go.

## Revisit when

- Content publish rate or blob size makes Kafka the wrong store for bodies (the likely trigger is
  Phase 2 art assets; move bodies to object storage and keep hashes in the manifest).
- Builders need concurrent editing with conflict resolution, which this design does not provide —
  last-pointer-move-wins is the whole concurrency model.
- In-game building becomes a design goal rather than a convenience.
