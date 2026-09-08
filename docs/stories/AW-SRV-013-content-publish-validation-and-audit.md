---
id: AW-SRV-013
title: Content publish path — server-side validation, versioning, and audit
epic: EPIC-05
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-008, AW-SRV-012]
blocks: [AW-CLI-003]
assignee: cursor
risk: high
---

> `status: draft` — unblocked by ADR-0004 and scoped. Groomed to `ready` when M3 approaches.

## Context

ADR-0004 lets Builders publish content without repository access. That makes the publish path a security
boundary, not a convenience: a client-side validation check is something a Builder can skip, so the
authoritative gate is server-side.

This story implements publish and rollback over `Admin`: write blobs, write a version manifest, move the
Active Pointer — each step authorized and audited.

## User story

As a builder, I want to publish a content version and roll it back with one command, so that a mistake
costs a minute rather than a deploy.

## Scope

### In scope
- `Admin` RPCs: publish blobs, publish a version manifest, move the Active Pointer, list versions.
- **Server-side validation** at publish, rejecting the write, using `AW-SRV-001`'s validator unchanged.
- Authorization: the `builder` role from ADR-0006, scoped per content pack.
- Audit records for every publish and every pointer move, with author, pack, version, and blob hashes.
- Version manifest linkage: `parent_version`, so history is a walkable chain.
- Rollback: move the pointer to any prior version, which is one record.
- Blob deduplication — a hash already present is not rewritten.

### Out of scope
- The Builder-facing CLI and the human-authorable format — `AW-CLI-003`.
- Content resolution and reload — `AW-SRV-012`.
- Concurrent editing with conflict resolution. ADR-0004 is explicit: last-pointer-move-wins is the whole
  concurrency model.

## Acceptance criteria (known now; completed at grooming)

1. **Given** a Builder publishing a version with a dangling Exit **when** the publish is attempted
   **then** it is rejected server-side with the same findings `AW-SRV-001` would produce, and nothing is
   written to any content topic.
2. **Given** a valid publish **when** it completes **then** blobs exist keyed by hash, a version manifest
   exists keyed `packID@version` naming its `parent_version`, and the Active Pointer has **not** moved.
   Publishing and activating are separate operations.
3. **Given** a published version **when** the Active Pointer is moved to it **then** one record is
   written to `andara.content.active.v1` and one to `andara.audit.v1`.
4. **Given** a rollback to version `N-3` **when** it is requested **then** it succeeds with one pointer
   move, and version `N` remains intact and re-activatable. Rollback is not deletion.
5. **Given** a Builder without the `builder` role for that pack **when** they publish **then** it is
   rejected and one audit record is written.
6. **Given** a blob whose hash already exists **when** it is published again **then** it is not rewritten
   and the publish still succeeds.
7. **Given** the version history for a pack **when** it is listed **then** every version ever published
   is present, because the versions topic is keyed such that compaction retains all of it (ADR-0004).

## Interface contract

To be written at grooming. Committed now: publish uses `sim.BuildWorld` for validation, so the CLI, the
publish gate, and the load path share one implementation — the equivalence `AW-CLI-002` AC-4 asserts.

## Data / state impact

Blob storage grows monotonically: content-addressed blobs are never removed by compaction. ADR-0004
accepts this for auditability and flags that it needs a retention story before the topic becomes the
largest thing in the cluster — and that large binaries, if Phase 2 art ever ships this way, belong in
object storage with hashes in the manifest instead.

## Observability requirements

- **Metrics:** `andara_content_publishes_total` (counter, label `outcome`),
  `andara_content_pointer_moves_total` (counter, label `direction` — `forward` or `rollback`),
  `andara_content_blob_bytes_total` (counter), `andara_content_validation_failures_total` (counter, label
  `code`, reusing `AW-SRV-001`'s taxonomy). Pack ID is a bounded label; blob hash is not and is rejected.
- **Logs:** every publish and pointer move at `info` with author, pack, version. Rejections at `warn`
  with the findings.
- **Traces:** `content.publish` with `content.validate` and `content.write_blobs` children.
- **Alerts:** none directly. A failing publish is a Builder's problem, visible to them immediately. A
  pointer move to a version that then fails to load alerts via `AW-SRV-012`'s `ContentLoadFailing`.

## Test plan

Server-side rejection asserting no topic writes; publish-then-activate as separate steps; rollback
asserting the newer version survives; role enforcement; blob dedup; version history retained across a
forced compaction, which is the test that would catch the compaction trap ADR-0004 warns about.

## Definition of done

CLAUDE.md §8, plus: the compaction test from AC-7 runs against a broker with compaction forced, because
"we keyed it correctly" is exactly the kind of belief that should be verified rather than reasoned about.

## Open questions

- `[NEEDS BRIAN]` Whether Builders are scoped per pack or trusted across all content. Per-pack is safer
  and is more machinery.
- **Resolved 2026-09-07 (Brian): a second approver is required to activate.** Publishing stays a
  single-Builder action; moving the Active Pointer requires a distinct second identity to approve. The
  reasoning Brian gave is worth recording because it changes what this feature is for: it lets more
  people contribute content while keeping a human moderation step in front of the live World. It is a
  contribution-scaling mechanism, not a compliance control.

  This story owns the rule, because the server is the security boundary and the CLI is a client.
  Specifically: an activation request from the same identity that published is rejected; the approval
  is itself an audited action on `andara.audit.v1` carrying approver, pack, version, and blob hashes;
  and an approval is bound to one `packID@version`, so re-publishing invalidates it. `AW-CLI-003`
  surfaces the approval state and the rejection — it does not enforce anything.

  Three sub-decisions this opens, none of which block grooming: whether an Operator may override in an
  incident (recommendation: yes, loudly audited, since the alternative is that a bad activation cannot
  be rolled back at 3am by whoever is awake); whether rollback to a previously-approved version needs
  fresh approval (recommendation: no — it was approved once and the content is byte-identical); and
  whether approval expires.

- `[ASSUMPTION]` Version numbers are monotonic integers per pack, assigned by the server, not by the
  Builder.
