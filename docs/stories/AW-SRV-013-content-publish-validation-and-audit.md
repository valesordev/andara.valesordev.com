---
id: AW-SRV-013
title: Content publish path — server-side validation, versioning, approval, and audit
epic: EPIC-05
component: server
type: feature
status: review
size: M
depends_on: [AW-SRV-008, AW-SRV-012]
blocks: [AW-CLI-003, AW-SRV-009, AW-SRV-035, AW-INF-021, AW-CLI-002]
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
pack** (2026-09-11). Three more were made at SPRINT-03's contract review (2026-09-28, below): an
**Operator may approve their own publish** (Brian, 2026-09-26); **activation refuses what the Loader
would refuse**; and **the server publishes its own `andara.core` at boot**, numbered by the build.

## User story

As a builder, I want to publish a content version and roll it back with one command, so that a mistake
costs a minute rather than a deploy.

## Scope

### In scope
- `Admin` RPCs: `HasBlobs`, `PublishBlob` (client-streaming), `PublishVersion`, `ApproveVersion`,
  `ActivateVersion`, `ListVersions`, `GetVersion`, `GetBlob` (server-streaming), `ReloadContent`.
- Server-side validation at `PublishVersion` using `AW-SRV-012`'s resolver over the just-written blobs
  and `AW-SRV-001`/`021`'s validator, rejecting before the manifest is written.
- Authorization: `builder` role **and** the pack in `Account.builder_packs`; `operator` may act on any
  pack, audited as `override`.
- The two-person rule: `ApproveVersion` by a distinct identity holding the pack; `ActivateVersion`
  requires `approved_by` set, or `operator` with `override=true`. Rollback to a previously-approved
  version needs no fresh approval. Approvals do not expire; they are bound to `packID@version`.
- Operator self-approval (ADR-0004, amended 2026-09-26): an `operator` may approve a version they
  published, directly or while acting as the publisher. Flagged and counted apart, and switched by
  `content.operator_self_approval`.
- Activation refusals: `ActivateVersion` refuses `zone_removed`, `spawn_room_removed`, and
  `core_version` before the pointer moves, with `AW-SRV-012`'s codes.
- `andara.core` at boot (ADR-0004 and ADR-0010 §8, amended 2026-09-28): the server publishes the
  core it embeds, as `andara.core@<content/core/VERSION>`, and activates it under the rules below. No
  RPC publishes core; an `operator` may only move its pointer, with no approver.
- `content/core/VERSION` and the append-only `content/core/VERSIONS`, and the `make check` test that
  holds the embedded core's digest to them.
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
4. **Given** `ApproveVersion` by a caller without `operator` who is the publisher, or the real actor
   behind the Session that published as the publisher **then** `PERMISSION_DENIED`, reason
   `self_approval`, counted `approvals_total{outcome="self"}`; by a distinct Builder holding the pack
   **then** `approved_by` and `approved_at_unix_nano` are set on the manifest and audited.
   *(Amended 2026-09-28: an Operator's own approval is AC-13.)*
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
11. **Given** `andara.core` **when** anyone calls `PublishBlob` or `PublishVersion` for it **then**
    `PERMISSION_DENIED`, reason `core_published_at_boot`, and one audit record; **when** a Builder calls
    `ActivateVersion` on it **then** `PERMISSION_DENIED`; **when** an `operator` activates any published
    core version **then** no approval is required, and the audit record names the operator.
    *(Amended 2026-09-28: the deploy tag is now on the boot's record, AC-15.)*
12. **Given** a blob over `content.max_blob_bytes` **when** streamed **then** `RESOURCE_EXHAUSTED`
    before any bytes are produced.
13. **Given** a version published by Account `B`, either by `B` or by operator `O` acting as `B`,
    **when** `O` calls `ApproveVersion` **then** `approved_by` is `O`, the response and the audit record
    carry `self_approval=true`, `approvals_total{outcome="self_operator"}` increments, and a `warn`
    line carries `self_approval=true`. `O` approving a version `O` published directly is the same.
    **With** `content.operator_self_approval=false` **then** it is AC-4's refusal, counted `self`.
14. **Given** the World in effect **when** `ActivateVersion` names a version that removes a Zone the
    active version has, that drops the Room `character.spawn_room` names, or that moves `andara.core`
    below the `core_version` of an active pack **then** `FAILED_PRECONDITION` with reason
    `zone_removed`, `spawn_room_removed`, or `core_version`, an `ActivationRefusal` detail naming the
    Zones, the Room, or every stranded `pack@version`, the pointer unmoved, one audit record with
    `outcome=refused`, and `activations_refused_total{reason}` incremented. `override` doesn't bypass
    these: it bypasses approval only, and the Loader would refuse the move anyway.
15. **Given** a store with no `andara.core` **when** a server with `content.source=kafka` boots
    **then** it publishes `andara.core@<VERSION>` with `author=server`, activates it with
    `activated_by=server`, writes one audit record per step with `actor_account_id=server` and
    `reason=boot <build version>`, and reports ready only once the World in effect includes it. A
    second boot of the same build publishes nothing and moves nothing.
16. **Given** the store holds `andara.core@<VERSION>` with a digest other than the build's **when**
    the server boots **then** it exits `1` with an `error` line naming both digests, and writes nothing.
17. **Given** active `andara.core@M` with `M < VERSION` **when** the server boots **then** it activates
    `VERSION` if `M`'s pointer was moved by `server`, and leaves `M` if an Account moved it, with a
    `warn` line naming `M`, `VERSION`, and that Account. **Given** `M > VERSION` (an older build)
    **then** it leaves `M` and logs it at `info`. It never moves core's pointer backwards.
18. **Given** a Builder holding `town` **when** `GetBlob` names `town@8` and a hash in that manifest
    **then** the body streams in chunks of at most 1 MiB and hashes to the request's hash; a hash not
    in that manifest is `NOT_FOUND`, whether or not the store holds it; a Builder without `town` is
    `PERMISSION_DENIED`.
19. **Given** `content/core/` changed without a new line in `content/core/VERSIONS` and a new
    `content/core/VERSION` **when** `make check` runs **then** it fails naming the digest the build
    embeds and the one `VERSIONS` records for `VERSION`.

## Interface contract

**Pinned 2026-09-28** in `docs/specs/protocol/`, with every field number assigned. `gen/` is
regenerated. The files are the contract; this is the summary.

```protobuf
// CONTRACT SKETCH — not an implementation. The pinned form is admin.proto.
rpc HasBlobs(HasBlobsRequest) returns (HasBlobsResponse);                  // pack_id, hashes → present[]
rpc PublishBlob(stream PublishBlobRequest) returns (PublishBlobResponse);  // header{pack_id,path,media_type,size_bytes,hash}, then data
rpc PublishVersion(PublishVersionRequest) returns (PublishVersionResponse); // pack_id, blobs[], parent_version → version, core_version, warnings[]
rpc ApproveVersion(ApproveVersionRequest) returns (ApproveVersionResponse); // → approved_by, approved_at, self_approval
rpc ActivateVersion(ActivateVersionRequest) returns (ActivateVersionResponse); // override, reason → previous_version, rollback
rpc ListVersions(ListVersionsRequest) returns (ListVersionsResponse);      // → versions[], active_version, activations[]
rpc GetVersion(GetVersionRequest) returns (GetVersionResponse);
rpc GetBlob(GetBlobRequest) returns (stream GetBlobResponse);              // pack_id, version, hash → data chunks
rpc ReloadContent(ReloadContentRequest) returns (ReloadContentResponse);
// status details: PublishFindings{findings[]} on INVALID_ARGUMENT; ActivationRefusal{reason, subjects[]}
```

- `andara/accounts/v1/account.proto`: `Account.builder_packs = 12`.
- `andara/content/v1/content.proto`: `Diagnostic` and `Severity`, the errors.md §1 shape the three
  runners compare. `ContentVersion.core_version` is read by the server from the compiled pack, not
  taken from the caller.
- `andara/audit/v1/audit.proto`: `pack_id = 20` through `self_approval = 26`.

### `andara.core` at boot

Runs only with `content.source=kafka`, under `AW-SRV-008`'s single-writer lock, before readiness.
`N` is the build's `content/core/VERSION`. A core's **digest** is the sha256 of the sorted list of its
blob hashes, the same value as the audit record's `blob_hashes_sha256`.

1. The store holds `andara.core@N` with the build's digest: publish nothing.
2. The store holds `andara.core@N` with another digest: exit `1` (AC-16). Never overwrite.
3. The store lacks `@N`: write the blobs and the manifest, `author=server`, `parent_version` the
   newest core version below `N` (`0` if none).
4. Activate `@N` if nothing is active, or if the active `M < N` and `ActiveVersion.activated_by` is
   `server`. Otherwise leave it (AC-17).
5. Readiness waits until the World in effect includes `andara.core` at the active version.

`server` is a reserved principal: not an Account, never authenticated. Account IDs are 32 hex
characters, so they can't collide with it. `content/core/VERSIONS` is one line per core ever
shipped, `<N> <digest-hex>`, append-only. The build embeds `content/core/` and `VERSION`, and
`andara-cli` embeds the same (`AW-CLI-002`).

### Authorization matrix

| RPC | `builder` with pack | `builder` without | `operator` |
|-----|:---:|:---:|:---:|
| `HasBlobs`, `PublishBlob`, `PublishVersion`, `ListVersions`, `GetVersion` | ✓ | ✗ | ✓ |
| `GetBlob` (hash in the named manifest) | ✓ | ✗ | ✓ |
| `ApproveVersion` | ✓ if ≠ publisher | ✗ | ✓, including their own publish while `content.operator_self_approval` |
| `ActivateVersion` | ✓ if approved | ✗ | ✓; unapproved needs `override` + `reason` |
| `PublishBlob`, `PublishVersion` on `andara.core` | ✗ | ✗ | ✗ (the server publishes it at boot) |
| `ActivateVersion` on `andara.core` | ✗ | ✗ | ✓, no approval |
| read RPCs on `andara.core` | ✓ | ✓ | ✓ |
| `ReloadContent` | ✗ | ✗ | ✓ |

"Publisher" is the manifest's `author` or the real actor behind the Session that published it. An
approval by either is a self-approval. Every activation refusal in AC-14 applies to every column,
`override` included.

### Audit record

`{actor_account_id, acting_as_account_id, action, pack_id, version, blob_hashes_sha256 (hash of the
sorted list), override, reason, outcome, findings_count, self_approval, session_id, trace_id}` on
`andara.audit.v1`, keyed by actor. `action ∈ {publish, approve, activate, rollback, reject, override}`.
`outcome ∈ {ok, denied, refused, rejected, stale_parent, too_large}`. The boot's records have
`actor_account_id=server` and `reason=boot <build version>`.

### Configuration

| Key | Env | Default |
|-----|-----|---------|
| `content.max_blob_bytes` | `ANDARA_CONTENT_MAX_BLOB_BYTES` | `8388608` (shared with `AW-SRV-012`) |
| `content.max_pack_bytes` | `ANDARA_CONTENT_MAX_PACK_BYTES` | `268435456` (256 MiB per version) |
| `content.core_pack` | `ANDARA_CONTENT_CORE_PACK` | `andara.core` |
| `content.operator_self_approval` | `ANDARA_CONTENT_OPERATOR_SELF_APPROVAL` | `true` (ADR-0004, amended 2026-09-26; turning it off is a values change) |

### Error taxonomy

Every error carries `ErrorInfo{domain: "andara.content", reason}`. The CLI prints the reason and
maps the code (`AW-CLI-003`).

| Condition | gRPC code | `reason` |
|-----------|-----------|----------|
| validation findings | `INVALID_ARGUMENT` (`PublishFindings` in details) | `validation` |
| body hash differs from the header's | `INVALID_ARGUMENT` | `blob_hash_mismatch` |
| pack not held | `PERMISSION_DENIED` | `pack_not_held` |
| self-approval without `operator`, or with it switched off | `PERMISSION_DENIED` | `self_approval` |
| any publish of `andara.core` | `PERMISSION_DENIED` | `core_published_at_boot` |
| Builder activating `andara.core`, non-operator `ReloadContent` | `PERMISSION_DENIED` | `operator_only` |
| unapproved activation | `FAILED_PRECONDITION` | `unapproved` |
| `parent_version` stale | `FAILED_PRECONDITION` | `stale_parent` |
| activation refused (AC-14) | `FAILED_PRECONDITION` (`ActivationRefusal` in details) | `zone_removed`, `spawn_room_removed`, `core_version` |
| blob or pack too large | `RESOURCE_EXHAUSTED` | `blob_too_large`, `pack_too_large` |
| version, or hash in that version, not found | `NOT_FOUND` | `not_found` |

## Data / state impact

**An image rollback across a core bump** (2026-09-28). The older build finds a newer core active and
leaves it (AC-17). If it can load it, it runs. If it can't, it exits `1` with nothing loadable. The
order is therefore packs, then core, then image, all while the newer build still serves. First,
`andara-cli content rollback <pack>` for every `pack@version` that pins the newer core: a core
rollback is refused `core_version` while any active pack pins it, and the refusal names each one
(AC-14). Then `andara-cli content rollback andara.core`, then roll the image back. If the image went
first, roll forward, move the pointers, and roll back again. *(Corrected 2026-09-30, §8 review: this
said "pointer first, then image", which AC-14's refusal makes fail at the first step.
`docs/runbooks/server-unavailable.md` already has the right order.)* **SRE:** please put that order in `docs/runbooks/server-unavailable.md`.
It's the one failure here that pages as `AndaraServerUnavailable`.

**`content/core/VERSIONS` is append-only.** Removing or rewriting a line would let two builds publish
different bytes as the same `andara.core@N`. AC-16 catches that at boot, but only after it has
shipped.

Blob storage grows monotonically; ADR-0004 accepts this and flags a retention story before the topic is
the largest thing in the cluster. `andara_content_blob_bytes_total` is the number to watch. The
`ContentVersion` message already carries `approved_by` (7), `approved_at_unix_nano` (8), and
`core_version` (9); no schema change on the content topics. Version numbers are server-assigned,
monotonic per pack, under the `AW-SRV-008` single-writer lock.

## Observability requirements

*(SRE observability review, 2026-09-28: label sets closed, the self-approval outcome added
pending architecture's decision, correlation fields and the persistence-write spans named. The
metric names are unchanged.)*

### Metrics
- RED for every new `Admin` RPC comes from the Gateway's existing
  `andara_grpc_requests_total{method,code}` and `andara_grpc_request_duration_seconds{method}`: eight
  more bounded `method` values. The counters below are the domain outcomes RED can't tell apart.
- `andara_content_publishes_total{outcome}`: `ok`, `rejected`, `denied`, `too_large`, `stale_parent`.
- `andara_content_approvals_total{outcome}`: `ok`, `self` (refused), `denied`, and
  `self_operator` (an Operator approving their own build, AC-13). It's a distinct value, never
  folded into `ok`, so the temporary rule's use is visible on a dashboard and not only in the
  audit topic.
- `andara_content_pointer_moves_total{direction, override}`: `forward` or `rollback`, × `true` or
  `false`. A refused activation doesn't move the pointer, and counts on
  `andara_content_activations_refused_total{reason}`. `reason` is the closed set `unapproved`,
  `zone_removed`, `spawn_room_removed`, `core_version` (AC-3, AC-14), and `validation` for any
  other refusing finding at activation (architecture's ruling 4). *(Added 2026-09-30, SRE:
  those refusals incremented no counter at all.)* `validation_failures_total{code}` stays the
  publish gate's, so it counts one thing. The finding codes themselves reach only the caller, in
  the RPC's `PublishFindings` detail. Telemetry keeps their count (`findings_count` in the audit
  record and the `warn` line). The `warn` line also carries the first finding's `code`, as a
  rejected publish's does, so a refusal can be triaged from Loki without the RPC response.
  *(Corrected before merge, from Codex on #270: this first said the audit record and the log kept
  the codes. They keep only the count, and the log line had no `code`.)* The boot's own activation
  (AC-15) counts on `pointer_moves_total{direction="forward",override="false"}`.
- `andara_content_blob_bytes_total`: counter, bytes accepted after deduplication. It's the number
  ADR-0004's retention question watches. No alert until that story exists.
- `andara_content_validation_failures_total{code}`: reuses `AW-SRV-001`'s closed taxonomy.
- Every label set above is closed and pre-seeded at 0. Pack ID is **not** a label on any of these
  counters: publishes are rare, and the audit topic answers "which pack". Blob hash, version, and
  account are rejected as labels.

### Logs
- Every line carries `actor_account_id`, `acting_as_account_id` (empty unless acting as),
  `pack_id`, `version`, `session_id`, and `trace_id`. The Admin path is a command path
  (CLAUDE.md §7).
- `info` per publish, approve, and activate.
- `warn` per rejection, with `findings_count` and the first finding's code. The findings
  themselves go in the RPC's status details and the audit record, not the log.
- `warn` per override, with `reason`.
- `warn` per self-approval, with `self_approval=true` (AC-13).
- `warn` per activation refused under AC-14, with `reason` and the subjects.
- The boot's core line, once per boot, at `info`:
  `content core: andara.core@<N> <published|present>; <activated|active andara.core@<M> by <who>>`.
  At `warn` when AC-17 leaves an Account's pointer. At `error` for AC-16, with both digests.

### Traces
All spans are under the Gateway interceptor's server span for the RPC, which the CLI's
`cli.command` parents through `traceparent`.
- `content.publish_blob`, per `PublishBlob` stream: `bytes` and `deduplicated` attributes, and one
  child `content.write_blob` per produce.
- `content.publish`, under `PublishVersion`, in order:
  - `content.validate`: the same validator, and the same span name, as the Loader's; the parent
    tells them apart;
  - then `content.write_manifest`.
- `content.approve` → `content.write_manifest`.
- `content.activate` → `content.write_pointer`. `rollback` is the same span with
  `direction=rollback`.
- The audit write is a child of each operation span: `audit.write`.
- `content.write_*` spans are persistence writes (CLAUDE.md §7), one per produce, never one per
  blob chunk.
- `content.get_blob`, per `GetBlob` stream, with `bytes`.
- `content.core_boot`, a root span at boot, with `content.write_blob`, `content.write_manifest`,
  `content.write_pointer` and `audit.write` children for whatever it wrote. It's the one trace of
  the core publish, since no RPC carries it.

### Alerts
None directly. A failing publish is visible to the Builder; a bad activation alerts via `AW-SRV-012`'s
`ContentLoadFailing`.

## Test plan

- **Unit:** authorization matrix as a table, the self-approval rows included; audit record shape per
  action; version assignment; the boot's rules 1–5 as a table over store states (AC-15–17); the
  `VERSIONS` digest test (AC-19) as part of `make check`.
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
  And, as Brian on `dev` (AC-13): publish as `--as <builder>`, `content approve` as the Operator,
  `content activate`; the audit record carries `self_approval=true`.

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
- **Resolved 2026-09-30 (architecture, §8 review):** Operator override exists, is loud, and requires
  a reason (AC-8). The alternative is that a bad activation can't be rolled back at 3 am. ADR-0004
  names the activation `override` as distinct from approval, and the glossary's Approval entry states
  it.
- **Resolved 2026-09-30 (architecture, §8 review):** rollback to a previously-approved version needs
  no fresh approval, and approvals don't expire. Both follow from binding an approval to
  byte-identical content. A version activated only by `override` was never approved, so rolling back
  to it needs `override` again.

## Contract review (architecture, 2026-09-28)

A contract change after `ready`, recorded here (CLAUDE.md §6). Implementation hasn't started. SRE's
observability review is in `docs/feedback/AW-SRV-013-operator-self-approval.md`, and its two
conditional additions are now unconditional.

1. **Operator self-approval is adopted** (Brian, 2026-09-26; ADR-0004's dated note). AC-4 narrows to
   callers without `operator`. AC-13 is the Operator's case. The approver is compared against both
   the manifest's author and the real actor behind the publishing Session, so `--as` can't hide one
   person doing both. `content.operator_self_approval` exists now: turning it off when other
   Builders join is a values change, not a code change.
2. **Activation refuses what the Loader would refuse** (feedback, activation refusals item 1). AC-14
   uses `AW-SRV-012`'s codes. `override` doesn't bypass them, because the Loader would refuse the
   same move after the pointer had moved, and the Builder would learn it from an alert instead of
   an error.
3. **`andara.core` is published by the server at boot, numbered by the build** (ADR-0004 and
   ADR-0010 §8, 2026-09-28; `docs/feedback/AW-INF-021-dev-content-store.md` item 1). AC-11 is
   amended, and AC-15 to AC-17 and AC-19 are new. No RPC publishes core. The five boot rules are
   under Interface contract.
4. **`GetBlob` is added.** Nothing served a published blob's body. `AW-CLI-003`'s `fetch` and `diff`,
   `AW-CLI-002`'s `validate --pack`, and `AW-CLI-006`'s deferred `decompile --pack` all needed one
   (`docs/feedback/AW-CLI-006-content-language-compiler.md` §7, §14 item 3). It's authorized on
   `pack@version` plus manifest membership, because a hash-only read would leak other packs'
   blobs. `fetch-core` over Admin isn't needed any more: the CLI embeds core.
5. **The proto is pinned**, not sketched: `admin.proto`, `content.proto` (`Diagnostic`),
   `account.proto` (`builder_packs = 12`), and `audit.proto` (20–26). `make proto-check` passes with
   no breaking change. `PublishVersion` takes `parent_version` for the stale-parent check, which the
   sketch left implicit. It no longer takes `core_version` from the caller: the server reads it
   from the compiled pack, which the caller can't forge.
6. **Error reasons are pinned** in one `ErrorInfo` domain, so `AW-CLI-003` maps reasons rather than
   parsing messages.
7. **This story grew.** PM: see `docs/feedback/AW-SRV-013-operator-self-approval.md`, *For PM*, for the
   split architecture recommends.
   *(Decided 2026-09-29, Brian: **one story, not split.** Implementation delivers AC-1 to AC-19 as
   `AW-SRV-013`. It's larger than `M` in practice. The frontmatter stays `M` because
   `validate-stories` refuses an `L` at `ready` (CLAUDE.md §6), and the split an `L` would force is
   what Brian decided against. The *`andara.core` at boot* section stays
   self-contained, so implementation may still land it as a second PR on this story. Nothing in the
   contract changes.)*

## Implementation record (2026-09-29)

On `impl/aw-srv-013-publish-path`, as one story (Brian, 2026-09-29). Unit tests run over in-memory
topics, with a real Loader and Engine behind the publish path. The broker test runs against the
local Redpanda over throwaway topics. Questions for SRE and architecture, and the working assumptions
the code carries until they're answered, are in `docs/feedback/AW-SRV-013-publish-path.md`.

| AC | Covered by | Result |
|----|------------|--------|
| 1 | `TestPublishVersion_ADanglingExitIsRefusedWithTheLoadersFindings`: the refusing findings equal `sim.BuildWorld`'s on the same Zones; no manifest; the blobs stay | pass |
| 2 | `TestPublishVersion_AValidVersionIsWrittenUnapprovedAndInactive`; `TestPublishPath_AgainstABroker` | pass |
| 3 | `TestActivateVersion_UnapprovedIsRefusedForAnyoneWithoutOverride`: the publisher, another Builder, and an Operator without `override` | pass |
| 4 | `TestApproveVersion_ABuilderCannotApproveTheirOwnAndAnotherCan`; `TestApproveVersion_ActingAsDoesNotHideTheSamePerson` | pass |
| 5 | `TestActivateVersion_AnApprovedVersionMovesThePointer` | pass |
| 6 | `TestActivateVersion_RollbackNeedsNoFreshApprovalAndLeavesNewerIntact` (`N-3`); `TestPublishPath_AgainstABroker` | pass |
| 7 | `TestPublishVersion_ABuilderWithoutThePackIsDenied`; `TestAuthorizationMatrix` | pass |
| 8 | `TestActivateVersion_AnOperatorOverridesApprovalWithAReason` | pass |
| 9 | `TestPublishBlob_APresentBlobIsReportedAndDeduplicated` | pass |
| 10 | `TestPublishPath_AgainstABroker`: forced compaction of the versions topic until `town@1`'s superseded record is gone, then a restart lists every version with its approval. Mutation-checked: without forced compaction it times out with two records left | pass |
| 11 | `TestCore_NoRPCPublishesItAndOnlyAnOperatorMovesIt` | pass |
| 12 | `TestPublishBlob_ABlobOverTheLimitIsRefusedBeforeAnythingIsProduced` (header, and a lying header); `TestPublishPath_AgainstABroker` (a 5 MiB blob through the broker, and 8 MiB + 1 produced nowhere) | pass; 8 MiB on a real topic needs SRE's `max.message.bytes` |
| 13 | `TestApproveVersion_AnOperatorApprovesTheirOwnPublish`: published directly, published acting as a Builder, and switched off | pass |
| 14 | `TestActivateVersion_RefusesWhatTheLoaderWouldRefuse`: `zone_removed`, `spawn_room_removed`, `core_version`, with `override` too | pass |
| 15 | `TestBootCore_AnEmptyStoreGetsTheBuildsCore`; `TestPublishPath_AgainstABroker` | pass; readiness (rule 5) is `ReconcileContent`'s check, not yet exercised by a test |
| 16 | `TestBootCore_AnotherDigestForTheSameVersionExitsAndWritesNothing` | pass |
| 17 | `TestBootCore_WhoseCorePointerMoves`: the server's pointer, an Account's, an older build, and a parent across a gap | pass |
| 18 | `TestGetBlob_StreamsAManifestsBlobInBoundedChunks`; `TestPublishPath_AgainstABroker` after a restart | pass |
| 19 | `content/core`'s `TestEmbeddedCoreIsTheOneVERSIONSRecords`. Mutation-checked: an added template fails it, naming both digests | pass |

The wire is `server/gateway`'s `TestContentAdmin_*`: `ErrorInfo` in the `andara.content` domain,
`PublishFindings`, `ActivationRefusal`, both streams at 1 MiB chunks, and a 10,000-hash `HasBlobs`.
Mutation-checked: with Admin's read limit back at `grpc.max_recv_bytes`, the stream test fails.

**Not done here, and why:**
- **`values-schema-check`:** SRE added the `keys.yaml` row in #174. The chart's generated
  `values.schema.json` and `_env.tpl` change once this code reads the key, so they're regenerated on
  this branch with `make values-schema` (SRE on #173).
  `TestSnapshotCopyStaysInsideTheStallBudget` fails intermittently under full-suite load, on `main`
  too (#172).
- **Blobs over about 1 MiB fail on the real topics** until `andara.content.blobs.v1` declares
  `max.message.bytes` (feedback, For SRE 1). The producer side is set.
- **The observations inherited from `AW-SRV-012`'s §8 pass** need a `content.source=kafka` server
  with a Builder pack, which `dev` gets with `AW-INF-021`. That story carries them.
- **The three-way equivalence fixture:** this story has the server's half (AC-1 above). The CLI's
  half is `AW-CLI-002`'s (feedback, For architecture 4).
- **The Data section's image-rollback order is incomplete** (Codex and SRE on #174). A core rollback
  is refused `core_version` while any active pack pins the newer core. That's AC-14, and the code
  does refuse it, naming each pack in the `ActivationRefusal`. So the order is every pack the refusal
  names, then `andara.core`, then the image. `docs/runbooks/server-unavailable.md` says so now. The
  Data section is architecture's to amend (feedback, For architecture 5).
- **The boot's core audit records** go to `andara.audit.v1` before the account store opens, through
  an Auditor with no registry, so they aren't counted on `andara_privileged_actions_total`. The log
  line, the `content.core_boot` trace, and `pointer_moves_total` carry them.

## §8 review (architecture, 2026-09-30): stays `review`

Against `main` at `79fd622`. Merged in #173 (`8c06bb7`). `check` and `stack` are green on the merge,
and `stack`'s `make test-integration` ran `TestPublishPath_AgainstABroker` (14.3 s). Re-run in this
review: the `server/content` unit tests with `-race`, `server/gateway`'s `TestContentAdmin_*`,
`content/core`'s test, and the broker test against local Redpanda (9.2 s). The failed `kind` run on
the merge (36667804698) was `argocd-redis` pulling a 429 from `ecr-public`, not this story. The PR's
own `kind` run passed, and so did the next scheduled one.

| AC | Evidence | Result |
|----|----------|--------|
| 1–9, 11–13, 17, 18 | the `admin_test.go` / `coreboot_test.go` case named for each in the implementation record | pass |
| 10 | `TestPublishPath_AgainstABroker`: compaction forced to one record per key, restart, v2 and v1 listed with their approvals | pass |
| 14 | `TestActivateVersion_RefusesWhatTheLoaderWouldRefuse` | pass. Weak test: the audit loop checks the records it finds, not that there's exactly one, so it'd pass on none. The code writes one |
| 15 | `TestBootCore_AnEmptyStoreGetsTheBuildsCore` | **gap.** Publish, activate, audit, and the second boot writing nothing are tested. "Reports ready only once the World in effect includes it" (`coreInEffect`, `boot/tick.go`) has no test, as the implementation record says |
| 16 | `…AnotherDigestForTheSameVersionExitsAndWritesNothing` | pass. The test checks the error, nothing written, and both digests. Exit `1` is `Run`'s `bootCore` → `ExitFail` path, shared by every boot failure, so this review accepts it by reading |
| 19 | `content/core/core_test.go` | pass as written. **Finding:** editing `VERSIONS`' existing line for `N` to the new digest also passes, and that's the append-only break the Data section warns of. Only AC-16 catches it, at boot, after it's shipped |

Checklist, beyond the ACs: the four config keys are in `server/README.md`, `keys.yaml`, the values
schema and `_env.tpl`, and the defaults match. No migration: audit fields 20–26 are additive, and
`VERSIONS` is documented append-only. The glossary gains Core Pack and the self-approval rule. Error
reasons, `ErrorInfo` domain, audit fields, action and outcome values, and every metric name and
label set match the contract. The two `[ASSUMPTION]`s are resolved above, in *Open questions*.

**Contract rulings** on implementation's questions (`docs/feedback/AW-SRV-013-publish-path.md`). All
of them are recorded as the contract from here on:
1. **Read limit and chunk size: pinned as built.** Admin reads up to 2 MiB, fixed, with no key
   (`gateway.AdminReadMaxBytes`). `grpc.max_recv_bytes` keeps guarding Game. A `PublishBlob` data
   chunk is at most 1 MiB (`content.BlobChunkBytes`), matching `GetBlob`. A larger chunk is
   `INVALID_ARGUMENT` `validation`. `AW-CLI-003` sends chunks of at most 1 MiB.
2. **Acting-as on Admin: gRPC metadata `andara-act-as: <account_id>`,** per call. It's honoured on
   Admin only, and only for a principal holding `operator` or `game_master`, the same principals and
   refusals as `Game.OpenSession.act_as_account_id`. The gateway resolves it into the Principal the
   content code already reads. Every acted-as call is audited with both identities. The token's `act`
   claim isn't used for this: it would need a mint and refresh path for a per-call choice. The
   gateway half isn't built, and PM places it (feedback file).
3. **The real actor survives the audit topic.** Rebuilding it from `andara.audit.v1` at boot, as
   built, loses it after the topic's 365-day retention, and at every restart under
   `auth.store=memory`. When it's lost, AC-4's acting-as clause stops seeing that an Operator
   approving as themselves published as a Builder. So it goes on the manifest:
   `ContentVersion.publisher = 10` (`string`), the real actor, always set, and equal to `author`
   when nobody acted as anyone. **It ships in the same story as acting-as on Admin (ruling 2),
   and no earlier.** Today no Admin call can act as anyone, so every manifest written so far was
   published by its `author`. With the two together, no acted-as manifest can exist without a
   `publisher`. An empty `publisher` therefore reads as `author`, and that's true, not a fallback.
   It needs no backfill, and the audit rebuild isn't needed for this any more. The story
   that ships the field must keep the two inseparable: the metadata can't be honoured on
   `PublishVersion` until the field is written. That's additive, with no migration. It's new work,
   routed to PM. The proto changes with that story, not before it. *(Revised 2026-09-30 on review
   of #176. It first read an empty `publisher` as "rebuild from audit", which leaves exactly the gap
   this ruling closes.)*
4. **Findings outside AC-14's three reasons: as built.** They're `INVALID_ARGUMENT` `validation` with
   `PublishFindings`, the same as at publish. A pack compiled against a core newer than the active
   one is `FAILED_PRECONDITION` `core_version`, subject `andara.core@<compiled>`. Whether
   `activations_refused_total` gains a `validation` reason is SRE's (feedback file). Until then it
   counts only the three.
5. **The other decisions made while building: accepted.** A non-Operator asking for `override` is
   `PERMISSION_DENIED` `operator_only`. An `override` without a `reason` is `INVALID_ARGUMENT`
   `validation`, and so is a `HasBlobs` over 10,000 hashes. A second approval returns the first,
   unchanged, with no audit record. An Operator acting on a pack their effective Account doesn't hold
   is audited `override=true`. Rollback to a version activated only by `override` needs `override`.
6. **The three-way equivalence fixture:** this story's Definition-of-done line moves to `AW-CLI-002`,
   whose AC-4 already carries it. This story's half, the publish gate's findings equal to the
   Loader's, is AC-1's test.
7. **The rollback order** in *Data / state impact* is corrected: packs, then core, then image. The
   same line is corrected in `AW-INF-021`.

**The deferred observations** from this story's inherited Definition of done, and its own series, go
to `AW-INF-021` as an explicit inherited line (added there today). `dev` isn't store-backed until
`AW-INF-021`, so no running server here can move a real Active Pointer (CLAUDE.md §8).

**What closes it:**
1. **AC-15's readiness half, with a test** (implementation): a server whose core isn't in the World
   in effect reports not-ready, then ready once it is. Mutation-checked.
2. **SRE's §8 instrumentation record:** the series and spans above, emitting against the local
   stack's backends in the integration suite, with the rest named as carried by `AW-INF-021`.

**Follow-ups that don't hold the story** (implementation, feedback file): tighten AC-14's audit
count to exactly one. And a mechanical append-only check on `content/core/VERSIONS`, against the
merge base, so that rewriting a line fails `make check`. It needs SRE's CI to hand it the base, so
it's routed to PM as a story.

## §8 instrumentation check (2026-09-30, SRE): not satisfied; the RPC path's backend assertions are owed

On `sre/sprint-03-review-verify`, against `main` at `79fd622`. The publish path exists only in a
`content.source=kafka` server (`OpenContentAdmin`, `bootCore`). Compose and `dev` both run `dir`, and
no client of `PublishBlob`/`PublishVersion`/`ApproveVersion`/`ActivateVersion` exists before
`AW-CLI-003`. So the RPC half is CLAUDE.md §8's "no caller yet" case. That case is satisfied only by
the integration suite exercising the story's code against the local stack's backends, with
assertions on the metric objects. Carrying the live observation forward doesn't replace it.

**Observed live.** The compose stack's server image ran once with `ANDARA_CONTENT_SOURCE=kafka`,
`ANDARA_CONTENT_PACKS=*` against a fresh local stack:

| Signal | Backend | Observed |
|--------|---------|----------|
| `content.core_boot` trace | local Tempo (trace `7549b55bf753752d…`) | root `content.core_boot` (`pack_id=andara.core`, `version=1`), with children `content.write_blob` ×4, `content.write_manifest`, `content.write_pointer` (`direction=forward`) and `audit.write` ×2. One `write_blob` per produce, as §7 requires |
| The core line | local Loki | `content core: andara.core@1 published; activated` at `info`, with `pack_id`, `version`, `activated_by=server` and `trace_id`. A second boot logged `andara.core@1 present; active andara.core@1 by server` |
| The publish counters | — | **not scraped.** With no Builder pack, a `kafka` World can't recover (`no_zones_found`, then `no manifest for dir@0`), and the process exits about 0.1 s after the core line, inside one 5 s scrape interval |

The stack was then restored with `make down VOLUMES=1 && make up`.

**Covered by tests, not observed.** The unit tests (in-memory topics) assert the metric objects for
`publishes_total{rejected,stale_parent}`, `approvals_total{ok,self,self_operator}`,
`pointer_moves_total` (three of the four label pairs), `activations_refused_total`, `blob_bytes_total`
and `validation_failures_total{unknown_room}`. No test asserts the RPC path's span tree, which is
`content.publish` → `content.validate`, `content.write_manifest`; `content.approve`;
`content.activate` → `content.write_pointer`; `content.publish_blob`; `content.get_blob`; and
`audit.write` under each. `TestPublishPath_AgainstABroker` asserts no telemetry.

**Owed by implementation, before architecture moves the story.** A broker-backed integration test
(`TestPublishPath_AgainstABroker`, or a sibling beside it, against the local Redpanda) that drives
the RPC path, including one rejected publish and one refused activation, and asserts:
1. the metric objects: `publishes_total{ok,rejected}`, `approvals_total{ok,self_operator}`,
   `pointer_moves_total{forward,false}` and `{rollback,false}`, `activations_refused_total{unapproved}`,
   `validation_failures_total{<the rejected publish's code>}`, and `blob_bytes_total` equal to the
   bytes accepted after deduplication;
2. the span tree, on a recording tracer: `content.publish` → `content.validate`, `content.write_manifest`;
   `content.approve` → `content.write_manifest`; `content.activate` → `content.write_pointer`
   (`direction` set); `content.publish_blob` → `content.write_blob`; and `audit.write` under each
   operation span.

Routed in `docs/feedback/AW-SRV-013-publish-path.md` ("SRE, 2026-09-30").

**Inherited Definition-of-done line, carried by `AW-INF-021`,** the first story with a
store-backed server and a caller (`make content-seed`, through `AW-CLI-003`). On `dev`, run this
sequence against the fixture pack:
1. a publish refused by validation;
2. a valid publish;
3. an activation of the unapproved version, refused `unapproved`;
4. the Operator's self-approval;
5. activate;
6. rollback.

Each of these moves on Prometheus:
- `publishes_total{rejected}` and `{ok}`;
- `validation_failures_total{code}` for the refused publish's code;
- `activations_refused_total{unapproved}`;
- `approvals_total{self_operator}`;
- `pointer_moves_total{forward,false}` and `{rollback,false}`;
- `blob_bytes_total`.

The RPC span tree reaches Tempo under the CLI's `cli.command`. The `info` and `warn` lines reach Loki
carrying `actor_account_id`, `pack_id`, `version`, `session_id` and `trace_id`, including the
rejection's `findings_count`, the refusal's `reason` and `self_approval=true`.

**Noted, not blocking.** The core line on stdout has no `trace_id`. The OTLP record in Loki
does, so it correlates on the backend, but not in `kubectl logs`.

The instrumentation item is **not satisfied.** The boot's core path is verified live. The RPC path
needs the backend assertions above (implementation). Its live observation on `dev` is then carried
by `AW-INF-021`. *(Revised before merge, from Codex on #177. The first push called this
satisfied, and its inherited line named only a successful sequence, which can't move
`activations_refused_total` or `validation_failures_total`.)*
