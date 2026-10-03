# AW-SRV-013 — building the publish path

Implementation started AW-SRV-013 on 2026-09-29, on `impl/aw-srv-013-publish-path`. It runs as one
story, unsplit (Brian, 2026-09-29). A survey of the code found things the contract needs that
aren't there yet. Most are implementation's to add, and are listed at the end so no one else takes
them. The ones below need another role.

## For SRE

### 1. The blobs topic can't hold an 8 MiB blob (blocks AC-12's upper end)

`content.max_blob_bytes` is 8 MiB (AW-SRV-012), but `andara.content.blobs.v1` declares no
`max.message.bytes`. So the broker default applies: 1 MiB on Kafka (1048588) and on Redpanda
(`kafka_batch_max_bytes`). A blob over about 1 MiB would be accepted by `PublishBlob` and then
refused by the broker with `MESSAGE_TOO_LARGE`.

- **Asked:** `max.message.bytes` on `andara.content.blobs.v1` at `content.max_blob_bytes` plus
  envelope headroom. The record is a `contentv1.Blob` (hash, media type, body), so 9 MiB
  (`9437184`) covers it. Apply it in every environment, locally too.
- **Implementation's side:** the producer's batch limit and the resolver's fetch limit. They're
  client settings, and this branch sets them.

### 2. `keys.yaml` needs `content.operator_self_approval` before this branch can pass `make check`

`make values-schema-check` fails on an `ANDARA_*` the code reads that `keys.yaml` doesn't list.
The row, as the contract pins it:

```yaml
- {key: content.operator_self_approval, env: ANDARA_CONTENT_OPERATOR_SELF_APPROVAL, type: bool, default: true, story: AW-SRV-013}
```

`content.max_pack_bytes` and `content.core_pack` are already listed.

### 3. Also, from the story body

The image-rollback order, pointer first and then image, still needs its line in
`docs/runbooks/server-unavailable.md` (Data / state impact).

## For architecture

None of these blocks the start. Each has a working assumption in the code, marked where it's used,
that changes in one place.

### 1. Admin's read limit and `PublishBlob`'s chunk size

The gateway reads every RPC under `grpc.max_recv_bytes`, 64 KiB by default. At that limit:
- a `HasBlobs` request at its documented maximum of 10,000 hashes (about 340 KB) is refused;
- a `PublishVersion` for a pack of a few thousand blobs is refused;
- the contract doesn't size a `PublishBlob` data chunk, so `AW-CLI-003` can't know what's safe.

**Assumed:** the Admin handler gets its own read limit of 2 MiB, fixed, with no key.
`grpc.max_recv_bytes` keeps guarding Game. A `PublishBlob` data chunk is at most 1 MiB, the same
bound `GetBlob` has. Please pin the chunk size, or say otherwise.

### 2. Acting-as on Admin, and the publishing Session's real actor

AC-4 and AC-13 compare the approver with the manifest's `author` *and* the real actor behind the
Session that published. Two gaps:
- **Nothing makes an Admin call acting-as today.** `Store.Verify` honours an `act` claim, but no
  token carries one. The only live acting-as path is `Game.OpenSession.act_as_account_id`. So
  "operator `O` publishes as `B`" can't happen over Admin yet. How does `andara-cli --as` reach
  Admin: a token carrying `act`, or a request header?
- **The real actor isn't on the manifest.** `ContentVersion` has `author` (the acted-as Account)
  and nothing else. The audit record holds the real actor, but nothing reads the audit topic back.
  **Assumed:** the server keeps the publishing actor in memory, beside its version index, and
  rebuilds it at boot from the `andara.audit.v1` publish records. Say if you'd rather it were a
  `ContentVersion` field.

### 3. What `ActivateVersion` does with findings outside AC-14's three reasons

**Assumed:** activation runs the Loader's own evaluation of `pack@version` against the World in
effect, so it refuses exactly what the swap would. Two outcomes aren't in the contract:
- **Findings a later change made fatal:** a version valid at publish can meet findings at activation
  that aren't one of the three reasons. For example, another pack's activation removed a Zone this
  one's Exit targets. **Assumed:** `INVALID_ARGUMENT`, reason `validation`, with `PublishFindings`,
  as at publish.
- **A pack compiled against a core newer than the active one** (the Loader's `ErrCoreVersion`).
  **Assumed:** `FAILED_PRECONDITION`, reason `core_version`, with subject
  `andara.core@<compiled>`. AC-14 names this reason only for the reverse direction.

### 4. The three-way equivalence fixture

The Definition of done asks for "the three-way equivalence fixture from `AW-CLI-002` AC-4". That
fixture is `AW-CLI-002`'s, and `AW-CLI-002` comes after this story in the sprint. **Assumed:** this
story ships the server's half, a test that the publish gate's findings equal the Loader's on the
same content, and `AW-CLI-002` wires the CLI in when it lands. Please move the DoD line, or say how
it should land here.

## Implementation's own, noted so no one else picks them up

- A content writer and index over `recordlog`: blobs, manifests, and pointer history, with version
  numbers assigned under one in-process lock. The server is a single replica (ADR-0001), which is
  the same basis `AW-SRV-008`'s single-writer lock rests on.
- The audit record's content fields (20–26), and the content actions on
  `andara_privileged_actions_total{action}`: `publish`, `approve`, `activate`, `rollback`,
  `reject`, `override`.
- An exported account lookup for `builder_packs`, read for the effective account.
- An exported publish check and activation check on the Loader.

## Implementation, 2026-09-29: built

The story is at `review`. The record is in the story, under "Implementation record". Every
assumption above is in the code as stated. Two were settled by building it:
- **Architecture 2, the real actor.** Built as assumed: the registry rebuilds who published each
  version, and every pointer move, from `andara.audit.v1` at boot. Nothing reads acting-as off an
  Admin token yet; the code takes it from the Principal, so it works whichever way you decide.
- **Architecture 1, the read limit.** Admin reads up to 2 MiB (`gateway.AdminReadMaxBytes`), and
  `PublishBlob` refuses a data chunk over 1 MiB (`content.BlobChunkBytes`). Each is one constant.

Also decided in building, for your review:
- A non-Operator asking for `override` is `PERMISSION_DENIED` `operator_only`. An `override` with
  no `reason` is `INVALID_ARGUMENT` `validation`.
- A `HasBlobs` over 10,000 hashes is `INVALID_ARGUMENT` `validation`.
- A second `ApproveVersion` of an approved version returns the first approval, unchanged.
- An Operator acting on a pack their effective Account doesn't hold is audited `override=true`,
  as the story's "audited as `override`" asks.
- Rollback needs no fresh approval only to a version that has one. A version once activated by
  `override` needs `override` again.

### For SRE
Items 1 and 2 above still stand: `make check` stays red at `values-schema-check` until the
`keys.yaml` row lands.

## For architecture: 5. The Data section's rollback order (2026-09-29)

From Codex's review of #174 and SRE's comment on #173. The story's Data / state impact says "pointer
first, then image". A core rollback is refused `core_version` while any active pack pins the newer
core (AC-14; `ErrCoreRollback`), so that first step fails. The order is every pack the refusal names
(`andara-cli content rollback <pack>`), then `andara.core`, then the image, all while the newer build
still serves. `docs/runbooks/server-unavailable.md` has it right now. The code needs no change: the
refusal already names every stranded `pack@version`. The story line is yours to amend.

## SRE, 2026-09-29: done in #174

`max.message.bytes: 9437184` on `andara.content.blobs.v1` (applied locally and on `dev`), the
`content.operator_self_approval` row, and the rollback order in the runbook.

## Architecture's §8 review (2026-09-30): items 1–5 answered

The rulings are in the story's §8 review, and bind as contract. In short:
1. **As built.** Admin reads 2 MiB, fixed. A `PublishBlob` chunk is at most 1 MiB, and over that is
   `INVALID_ARGUMENT` `validation`.
2. **Acting-as over Admin is gRPC metadata `andara-act-as: <account_id>`,** honoured for `operator`
   and `game_master` only, with `OpenSession`'s refusals. Not the token's `act` claim. The real actor
   goes on the manifest as `ContentVersion.publisher = 10`, since rebuilding it from audit doesn't
   survive retention or `auth.store=memory`. It ships with the acting-as metadata, so every
   manifest without it was published by its `author`.
3. **As built,** for both outcomes.
4. **The DoD line moves to `AW-CLI-002`** (its AC-4).
5. **Corrected** in the story and in `AW-INF-021`.

Your other building decisions are accepted as they stand.

### For implementation: owed before `done`
- **AC-15's readiness half has no test.** Add one: a server whose `andara.core` isn't yet in the
  World in effect reports not-ready, then ready once it is. Mutation-check it (drop `coreInEffect`
  from readiness, and it fails).

### For implementation, not holding the story
- `TestActivateVersion_RefusesWhatTheLoaderWouldRefuse`: assert exactly one audit record per
  refusal, not "each record found says `refused`".

### For SRE
- **§8 instrumentation record** for this story: the metrics and spans in its Observability section,
  from the integration suite against the local stack. What `dev` can't show until it's store-backed
  is now `AW-INF-021`'s inherited Definition-of-done line.
- **`andara_content_activations_refused_total{reason}`**: activation findings outside the three
  reasons are refused `validation` and aren't counted on it. Add `validation` to the closed set, or
  leave them to `andara_content_validation_failures_total{code}`? That's your call, and either way
  the Observability section should say which.

### For PM: new work from this review
1. **Admin acting-as, as one story** (implementation, `server`): the `andara-act-as` metadata in
   the gateway (ruling 2), and `ContentVersion.publisher = 10` written on every publish (ruling 3).
   They're one story because they're only safe together. Metadata honoured before the field exists
   would write acted-as manifests with no durable real actor. `AW-CLI-003`'s `andara-cli --as
   <builder> content publish` needs it, so it lands before `AW-CLI-003` or inside it. Which one is
   your call. If the SPRINT-03 demo has Brian publishing `--as` a Builder, it's on the demo's path.
2. **`content/core/VERSIONS` is append-only, mechanically.** `make check` fails when a line other
   than a new last one changes against the merge base. SRE's CI supplies the base. Not on the
   demo's path.

## SRE, 2026-09-30: for implementation, the §8 instrumentation check's owed assertions

SRE's §8 instrumentation check (story body, 2026-09-30) is **not satisfied**. The RPC path has no
in-cluster caller yet, so CLAUDE.md §8 accepts the integration suite in its place, but only if that
suite asserts on the telemetry. Today the RPC path's metric assertions are unit tests over
in-memory topics, no test asserts its spans, and `TestPublishPath_AgainstABroker` asserts no
telemetry. What's owed, against the local Redpanda:
- a rejected publish and a refused (`unapproved`) activation alongside the successful path;
- the metric objects and the span tree listed in the story's record.

The story stays `review` until that lands. `AW-INF-021` then carries the live observation on `dev`.

## SRE, 2026-09-30: `activations_refused_total{reason}` gains `validation`

This answers architecture's "For SRE" question, the second bullet. Today `Admin.refused` returns
early for `RefusalValidation`, so an activation refused on findings outside AC-14's three reasons
increments **no counter**. It's only in the audit record and the `warn` line. A refused move of the
World's content should be visible on the dashboard like the other four.

**Decided:** `validation` joins the closed set, which is now `unapproved`, `zone_removed`,
`spawn_room_removed`, `core_version` and `validation`, pre-seeded at 0. The finding codes stay off it.
`validation_failures_total{code}` stays the publish gate's ("Refusing findings at publish", as
its Help says), so a failed publish and a refused activation aren't summed into one series. The
story's Observability section says so, dated.

**For implementation** (with the §8 assertions owed above, same broker test):
- `RefusedValidation = "validation"` in `publish_metrics.go`'s pre-seed list, and `Inc()` on the
  validation branch of `Admin.refused`;
- the `server/README.md` metrics table row;
- an assertion on `ActivationsRefused.WithLabelValues("validation")` in the test that refuses one.
- the `warn` line `content activation refused` gains `code`, the first refusing finding's code, as
  `rejected` logs it at publish. Today it has only `reason` and `findings_count`, and the audit
  record has only the count, so once the RPC response is gone, a refusal's codes are nowhere.

## Carried by `AW-SRV-046` (PM, 2026-10-02)

The "not holding the story" follow-ups for implementation above are now items in `AW-SRV-046`
(draft, SPRINT-04), which records each one as done here when it merges.

## Done in `AW-SRV-046` (implementation, 2026-10-03)

`TestActivateVersion_RefusesWhatTheLoaderWouldRefuse` now asserts exactly one audit record per
refusal, each `refused`. Recording each refusal twice fails it.
