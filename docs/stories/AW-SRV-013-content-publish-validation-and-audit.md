---
id: AW-SRV-013
title: Content publish path — server-side validation, versioning, approval, and audit
epic: EPIC-05
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-008, AW-SRV-012]
blocks: [AW-CLI-003, AW-SRV-009]
lane: implementation
risk: high
---

## Context

ADR-0004 lets Builders publish content without repository access. That makes the publish path a security
boundary, not a convenience: a client-side validation check is something a Builder can skip, so the
authoritative gate is server-side.

This story implements publish, approval, activation, and rollback over `Admin`: write blobs, write a
version manifest, approve, move the Active Pointer — each step authorized and audited. Two decisions
shape it: **activation requires a second approver** (2026-09-07) and **Builder authority is scoped per
pack** (2026-09-11).

## User story

As a builder, I want to publish a content version and roll it back with one command, so that a mistake
costs a minute rather than a deploy.

## Scope

### In scope
- `Admin` RPCs: `HasBlobs`, `PublishBlob` (client-streaming), `PublishVersion`, `ApproveVersion`,
  `ActivateVersion`, `ListVersions`, `GetVersion`, `ReloadContent`.
- Server-side validation at `PublishVersion` using `AW-SRV-012`'s resolver over the just-written blobs
  and `AW-SRV-001`/`021`'s validator, rejecting before the manifest is written.
- Authorization: `builder` role **and** the pack in `Account.builder_packs`; `operator` may act on any
  pack, audited as `override`.
- The two-person rule: `ApproveVersion` by a distinct identity holding the pack; `ActivateVersion`
  requires `approved_by` set, or `operator` with `override=true`. Rollback to a previously-approved
  version needs no fresh approval. Approvals do not expire; they are bound to `packID@version`.
- `andara.core` carve-out (ADR-0010 §8): publish and activate by `operator` only, no second approver;
  it is a deploy step (`AW-INF-007`).
- Audit records for every publish, approval, activation, rejection, and override.
- Blob deduplication and size limits.

### Out of scope
- The CLI and the Content Language — `AW-CLI-003`, `AW-CLI-005`.
- Resolution and reload — `AW-SRV-012`.
- Concurrent editing. Last-pointer-move-wins is the whole concurrency model (ADR-0004).

## Acceptance criteria

1. **Given** a Builder publishing a version with a dangling Exit **when** `PublishVersion` runs **then**
   it returns `INVALID_ARGUMENT` with the same findings `AW-SRV-001` produces, no manifest is written,
   and the blobs already written are left (they are content-addressed and harmless).
2. **Given** a valid publish **when** it completes **then** blobs exist keyed by hash, a manifest exists
   keyed `packID@version` with `parent_version` = the pack's current newest version, `approved_by` is
   empty, and the Active Pointer has not moved.
3. **Given** an unapproved version **when** `ActivateVersion` is called by its publisher or anyone else
   without `override` **then** `FAILED_PRECONDITION` names the missing approval and one audit record is
   written.
4. **Given** `ApproveVersion` by the same Account that published **then** `PERMISSION_DENIED`; by a
   distinct Builder holding the pack **then** `approved_by` and `approved_at_unix_nano` are set on the
   manifest and audited.
5. **Given** an approved version **when** `ActivateVersion` runs **then** one record is written to
   `andara.content.active.v1` with `activated_by` = the caller and one to `andara.audit.v1`.
6. **Given** a rollback to a previously-activated version `N-3` **when** requested **then** it succeeds
   with one pointer move and no new approval; version `N` remains intact and re-activatable.
7. **Given** a Builder whose `builder_packs` lacks the pack **when** they publish **then**
   `PERMISSION_DENIED` and one audit record.
8. **Given** an `operator` activating an unapproved version with `override=true` **when** it runs
   **then** it succeeds and the audit record carries `override=true` and the stated `reason`.
9. **Given** a blob whose hash exists **when** `HasBlobs` is called **then** it is reported present and
   `PublishBlob` for it is not required; publishing it anyway is idempotent.
10. **Given** the versions topic **when** compaction is forced **then** every version ever published is
    still listed by `ListVersions`.
11. **Given** `andara.core` **when** a Builder attempts any write **then** `PERMISSION_DENIED`; **when**
    an `operator` activates it **then** no approval is required and the audit record names the deploy
    tag.
12. **Given** a blob over `content.max_blob_bytes` **when** streamed **then** `RESOURCE_EXHAUSTED`
    before any bytes are produced.

## Interface contract

```protobuf
// CONTRACT SKETCH — additions to andara/admin/v1/admin.proto
rpc HasBlobs(HasBlobsRequest) returns (HasBlobsResponse);                 // hashes → present[]
rpc PublishBlob(stream PublishBlobChunk) returns (PublishBlobResponse);  // first chunk: pack_id, path, media_type, size; then bytes
rpc PublishVersion(PublishVersionRequest) returns (PublishVersionResponse); // pack_id, blobs[], core_version → version, findings[]
rpc ApproveVersion(ApproveVersionRequest) returns (ApproveVersionResponse); // pack_id, version
rpc ActivateVersion(ActivateVersionRequest) returns (ActivateVersionResponse); // pack_id, version, override, reason
rpc ListVersions(ListVersionsRequest) returns (ListVersionsResponse);     // pack_id → ContentVersion[], active
rpc GetVersion(GetVersionRequest) returns (ContentVersion);
rpc ReloadContent(ReloadContentRequest) returns (ReloadContentResponse); // operator; re-resolve without a pointer move

// andara/accounts/v1/account.proto (AW-SRV-008) Account: next free number
repeated string builder_packs = 12;   // sorted
```

### Authorization matrix

| RPC | `builder` with pack | `builder` without | `operator` |
|-----|:---:|:---:|:---:|
| `HasBlobs`, `PublishBlob`, `PublishVersion`, `ListVersions`, `GetVersion` | ✓ | ✗ | ✓ |
| `ApproveVersion` | ✓ if ≠ publisher | ✗ | ✓ if ≠ publisher |
| `ActivateVersion` | ✓ if approved | ✗ | ✓; unapproved needs `override` + `reason` |
| any on `andara.core` | ✗ | ✗ | ✓, no approval |
| `ReloadContent` | ✗ | ✗ | ✓ |

### Audit record

`{actor_account_id, acting_as_account_id, action, pack_id, version, blob_hashes_sha256 (hash of the
sorted list), override, reason, outcome, findings_count, session_id, trace_id}` on `andara.audit.v1`,
keyed by actor. `action ∈ {publish, approve, activate, rollback, reject, override}`.

### Configuration

| Key | Env | Default |
|-----|-----|---------|
| `content.max_blob_bytes` | `ANDARA_CONTENT_MAX_BLOB_BYTES` | `8388608` (shared with `AW-SRV-012`) |
| `content.max_pack_bytes` | `ANDARA_CONTENT_MAX_PACK_BYTES` | `268435456` (256 MiB per version) |
| `content.core_pack` | `ANDARA_CONTENT_CORE_PACK` | `andara.core` |

### Error taxonomy

| Condition | gRPC code |
|-----------|-----------|
| validation findings | `INVALID_ARGUMENT` (findings in details) |
| pack not held, self-approval, Builder on core | `PERMISSION_DENIED` |
| unapproved activation, `parent_version` stale | `FAILED_PRECONDITION` |
| blob or pack too large | `RESOURCE_EXHAUSTED` |
| version not found | `NOT_FOUND` |

## Data / state impact

Blob storage grows monotonically; ADR-0004 accepts this and flags a retention story before the topic is
the largest thing in the cluster. `andara_content_blob_bytes_total` is the number to watch. The
`ContentVersion` message already carries `approved_by` (7), `approved_at_unix_nano` (8), and
`core_version` (9); no schema change on the content topics. Version numbers are server-assigned,
monotonic per pack, under the `AW-SRV-008` single-writer lock.

## Observability requirements

### Metrics
- `andara_content_publishes_total{outcome}` (`ok`, `rejected`, `denied`, `too_large`).
- `andara_content_approvals_total{outcome}` (`ok`, `self`, `denied`).
- `andara_content_pointer_moves_total{direction, override}` (`forward`/`rollback` × `true`/`false`).
- `andara_content_blob_bytes_total` — counter. `andara_content_validation_failures_total{code}` reusing
  `AW-SRV-001`'s taxonomy. Pack ID is a bounded label; blob hash and account are rejected.

### Logs
- `info` per publish, approve, activate with actor, pack, version; `warn` per rejection with findings;
  `warn` per override with reason.

### Traces
- `content.publish` → `content.write_blobs`, `content.validate`; `content.activate`.

### Alerts
None directly. A failing publish is visible to the Builder; a bad activation alerts via `AW-SRV-012`'s
`ContentLoadFailing`.

## Test plan

- **Unit:** authorization matrix as a table; audit record shape per action; version assignment.
- **Integration:** against a throwaway Redpanda — every AC, including forced compaction (AC-10) and
  the streamed size limit (AC-12); the three-way equivalence fixture from `AW-CLI-002` AC-4 asserting
  server findings equal CLI findings.
- **Manual/operator:**
  ```
  andara-cli content publish ./town            # as builder A: version 8, "awaiting approval"
  andara-cli content approve pack.town 8       # as builder B
  andara-cli content activate pack.town 8      # as A or B: pointer moves; audit shows both
  andara-cli content rollback pack.town        # pointer to 7, no approval needed
  ```

## Definition of done

CLAUDE.md §8, plus: the compaction test (AC-10) against a broker with compaction forced, and the
three-way equivalence fixture wired in.

**Inherited from `AW-SRV-012`'s §8 pass (2026-09-25):** this story is the first that makes the `kafka`
content source live on a running stack. Publish, then activate, moves a real Active Pointer. Its
§8 shows, from the running server:
- `andara_content_pending_seconds{pack}` rising, then clearing on apply;
- `andara_content_active_version{pack}` and `andara_build_info{pack,content_version}` moving on the swap;
- `andara_content_reload_stall_seconds` and `andara_content_load_phase_duration_seconds{phase}` observing resolve, validate, build and swap;
- `andara_content_cache_hits_total{outcome}`;
- a refused version counted on `andara_content_load_failures_total{reason}`;
- `andara_content_relocations_total{zone}`, with its `warn` line, for a version that removes an occupied Room;
- in Tempo, `content.load` → `content.resolve`, `content.validate` → `content.build`, and `content.swap` under the load.

**Inherited from `AW-CLI-006`'s §8 pass (2026-09-24):** the publish gate imports `content/lang`
itself, so the three-way equivalence test compiles with the same package the CLI ships, not a copy.
`server/content/lang_test.go` already shows the import compiles.

## Open questions

- **Resolved 2026-09-07 (Brian): a second approver is required to activate.** AC-3–5.
- **Resolved 2026-09-11 (Brian): Builders are scoped per pack.** `builder_packs` and AC-7.
- `[ASSUMPTION]` Operator override exists, is loud, and requires a reason (AC-8) — the alternative is
  that a bad activation cannot be rolled back at 3 am.
- `[ASSUMPTION]` Rollback to a previously-approved version needs no fresh approval; approvals do not
  expire. Both follow from binding an approval to byte-identical content.
