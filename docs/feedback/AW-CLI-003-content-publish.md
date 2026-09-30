# AW-CLI-003 — the publish path: implementation notes

Built on `impl/aw-cli-003-content-publish`. The story is at `review`. What follows is what building
it decided that the contract didn't.

## For architecture

### 1. No `--as` for Admin RPCs, so AC-11 compares with the caller only

The test plan's Operator path runs `andara-cli --as <builder> content publish`, and AC-11 compares
the author with "the Account the Operator is acting as". Acting-as exists only on `play`'s
`OpenSession` (AW-SRV-008 AC-10). No token that `auth login` issues carries an acting-as claim, and
no Admin RPC takes one, so `--as` has nothing to send. **Built:** the prompt compares the version's
author with the caller, and with the token's acting-as claim if it ever has one. Brian's path on
`dev` still works: an Operator publishes as themselves, audited as `override` because they hold no
pack, then approves with the prompt. Acting as a Builder from the CLI is a contract of its own, if
it's wanted.

### 2. Accounts are named by ID

`ContentVersion.author` and `approved_by` are account IDs, and no RPC reads another Account's
username (AW-SRV-035 feedback 1 is the same gap). AC-3's `(published by <user>)` and AC-11's
`approved by <user>` print the username for the caller, taken from their credential, and the
account ID for anyone else.

### 3. How the CLI knows who it is

The stored credential has never held `account_id`: `auth login` doesn't fill it, and nothing
returns it. The CLI reads it from the Session token, unverified, through `auth.TokenAccountID` in
`server/auth`, which owns the format. It uses it only to decide AC-11's prompt and to print the
caller's own username. The server decides everything else.

### 4. `fetch` and AC-8 depend on the directory name

A compiled Template records `source.file` as `<pack directory>/<file>` (AW-CLI-006). So fetched
sources compile to the manifest's exact bytes only in a directory with the name they were
published from. **Built:** `--out` defaults to that name, read from the version's Templates, or the
pack ID when it has none. With an explicit `--out` of another name, the Templates' `source` differs
and nothing else does. If byte identity should hold under any name, `source.file` needs to drop
the directory. That's AW-CLI-006's format, and a corpus change.

### 5. `publish`'s parent

The contract says the parent is "what `history` last showed". The CLI keeps no state between
runs. **Built:** the pack's newest version, read by `ListVersions` just before `PublishVersion`.
A publish that lands in between is `stale_parent`, explained with the command to run.

### 6. Smaller things decided in building

- **`andara.core`:** `publish` skips `HasBlobs` and uploads for it, so the refusal is
  `PublishVersion`'s, `core_published_at_boot`, for everyone (AC-14). `HasBlobs` would refuse a
  Builder first as `pack_not_held`.
- **Declining a prompt** is exit 1, `declined`, and changes nothing. A missing `--yes` with no
  terminal is exit 2, `yes_required`.
- **`rollback`** with nothing earlier to go back to is exit 1, `no_previous_version`.
- **`diff`** lists a new Room and not each of its Exits. For a changed `desc` it says `desc` and
  doesn't print both texts.
- **`server info`** prints one `key value` per line. `protocol` is `min-max`, as AC-13 writes it.
- **Every `--output json` result and error envelope carries `trace_id`**, and
  `error.detail.trace_id` on a refusal.

## For SRE

### 1. The rehearsal as a CI job (Definition of done)

`TestContentPublishPath_TwoIdentities` is the two-identity rehearsal, alice publishing and bob
approving, through publish, approve, activate, rollback, history, diff and fetch. It runs in
`make check` against an in-process stack. That stack is the gateway, AW-SRV-013's Admin, the
Account store, and a Loader that follows pointer moves through an Engine, so `server info`
reports what was applied. `TestContentPublishPath_TwoIdentitiesOverRedpanda` is the same test on
throwaway Redpanda topics, with a KafkaResolver as the store and the watcher. It passes locally.
It runs in CI once `./admin/cli/` is in `make test-integration` (AW-CLI-002 feedback, For SRE 1).

### 2. AC-2 on `dev`

The Redpanda test shows `server info` naming the new version within `content.reload_debounce +
2 s` against a real broker. `dev` itself serves `content.source=dir` until AW-INF-021, so the live
observation there is AW-INF-021's to carry.

## Architecture's §8 review (2026-09-30)

Items 1–6 are ruled in the story's §8 review. All are accepted as built. Item 1 moves `--as` on
content commands, AC-11's acting-as clause and the test plan's `--as` line to the Admin acting-as
story (`AW-SRV-013` rulings 2 and 3). PM places that story, in `docs/feedback/AW-SRV-013-publish-path.md`.

### For implementation: owed before `done` (revised on review of #275)
- **`fetch` and `diff`:** route `fetchVersion` through `contentError`, so that `error.code` is the
  server's reason (`not_found`, `pack_not_held`), as the contract's exit table says. Add a test per
  command.
- **Stale parent over Redpanda:** two publishes on one parent, and the second exits 1 with
  `stale_parent` and its hint (the test plan's integration case).
- **`pack_not_held` at the CLI:** `content publish` by a Builder without the pack exits 1 with
  `error.code` `pack_not_held` (`AW-SRV-035` AC-2).
- **AC-7:** a `diff` test covering a Zone added and removed, a Template changed, and a Component field
  added, removed and changed, each with `file:line`. Emptying `diffComponents` and the Template loop
  currently leaves every test green.

### For implementation, not holding the story
- Tests: `fetch` with a `src/../x` path; `previousActive` over three or more moves; AC-2 activating
  as the approver; a blob over 1 MiB, to exercise chunking.
- The implementation record: the rehearsal is in CI (#265), and the confirmation `info` line isn't
  asserted.

### For SRE
- Your item 1 is done: `./admin/cli/` is in `make test-integration`, and `stack` ran the rehearsal.
- The §8 instrumentation record, under `AW-CLI-002`'s in-process ruling. Please also say whether the
  `--override` confirmation `info` line needs a test.
