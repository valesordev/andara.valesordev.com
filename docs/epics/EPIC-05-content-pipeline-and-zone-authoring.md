---
id: EPIC-05
title: Content pipeline and zone authoring
phase: 1
component: server
milestone: M3
status: ready
adr_gates: []
adr_refs: [ADR-0004, ADR-0007]
---

## Goal
A Builder with **no repository access** publishes a Content Pack version, sees it live without a
deploy, and rolls it back with one command.

## Why now
ADR-0004 put content in the data layer specifically so Builders need not be Developers. That makes
publish-time authorization and validation Phase 1 security concerns rather than later conveniences.

## In scope
- Content Blob, Content Version, and Active Pointer topics and their record shapes.
- Publish path: write blobs, write a version manifest, move the pointer — each audited.
- **Server-side** validation at publish. Builders are untrusted; a client-side check is a convenience.
- Content resolution and reload at a tick boundary when the Active Pointer moves.
- Relocation policy for Entities standing in a Room that the new version removed.
- `format_version` skew handling: reject the load, keep serving the last version that loaded, and say
  so loudly — never fail to boot.

## Out of scope
- The authoring UX beyond `andara-cli` — `EPIC-06`.
- In-game building. `[NEEDS BRIAN]` on whether it is a design goal; it would publish through these
  same topics, so nothing here forecloses it.
- Large binary assets. ADR-0004 flags that Phase 2 art belongs in object storage with hashes in the
  manifest, not in Kafka.

## Done when
A Zone with a dangling Exit is rejected by `andara-cli content validate`, rejected again server-side at
publish, and would be rejected at load — all three from one validator implementation, all three naming
the file, Room ID, and direction.

## Stories
`AW-SRV-012`, `AW-SRV-013`, `AW-CLI-002`, `AW-CLI-003`
