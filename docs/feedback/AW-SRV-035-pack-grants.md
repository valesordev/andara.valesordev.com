# AW-SRV-035 — Pack Grants: implementation notes

Built on `impl/aw-srv-035-account-set-packs`. The story is at `review`. What follows is what building
it decided that the contract didn't.

## For architecture

### 1. AC-1 and AC-4 print `<username>`, and nothing returns one. Built with the account ID.

AC-1 expects `<username>: builder packs docks, town`. The command takes an account ID, and the
pinned `SetBuilderPacksResponse` carries `builder_packs`, `record_version` and `builder_role`, but
no username. There's no `GetAccount` either (the story's own open question). So the CLI prints
`<account-id>: builder packs docks, town`, as `set-roles` prints the account ID. If the username
matters, it's one field on the response (`string username = 4`). Please amend the ACs or the
proto.

### 2. AC-2's CLI half belongs to AW-CLI-003

AC-2 has the CLI exit `1` with `error.code` `pack_not_held` on a publish, but `content publish` is
AW-CLI-003's. This story tests the server half. `server/content`'s `TestPackGrant_*` tests grant
through the real Account store and publish through `Admin.PublishVersion`, and get
`PERMISSION_DENIED`, reason `pack_not_held`. AW-CLI-003 carries the exit code.

### 3. The ErrorInfo domain

The reasons table names no domain. **Built:** `andara.accounts`, alongside `andara.content` and
`andara.character`. The five reasons ride on `SetBuilderPacks` refusals only. The other Account
RPCs are unchanged.

### 4. Audit outcomes, per refusal

| Refusal | `outcome` |
|---------|-----------|
| not an operator | `denied` (AC-5) |
| account not found | `denied`, as `SetRoles` records it |
| invalid pack ID, `andara.core` | `invalid` |
| stale record version | `conflict` |

A caller without `operator` is audited here, as AC-5 requires. The other Admin operations don't
audit that case (`server/auth/admin.go`'s header comment), so this RPC is the exception, as the
story asks. A call with no Principal at all is still unaudited.

### 5. `accounts.write` covers the write only

The span wraps the Account store produce, so only a grant that reaches the store has one.
`outcome` is `ok`, or `error` when the produce fails. A refusal never reaches the store, so it has
no span. The refusal is in the audit record, the counter and the `warn` line.

### 6. No audit write under the Account store's write lock (Codex on #266)

AC-5 audits a non-operator's refusal. The first build wrote that record while holding `wmu`, so on
a slow audit topic any authenticated account could make every Account writer (login and refresh
included) wait up to the audit timeout on each call. Now a non-operator never takes `wmu`, and
every refusal or grant is audited after the lock is released.
`TestSetBuilderPacks_ARefusalDoesNotHoldTheWriteLock` stalls the audit topic on a refusal and
requires an Operator's grant to go through meanwhile. With the lock put back, the test fails.
`SetRoles` and the other Admin writes still audit under `wmu`, but only for Operators. Those
aren't changed here; one story can do all of them.

## Architecture's §8 review (2026-09-30)

Items 1–6 are ruled in the story's §8 review:
1. The Account ID, and the ACs are amended.
2. Owed as a test, and it holds this story (revised on review of #275).
3. `andara.accounts`, recorded in the contract.
4. Accepted.
5. Accepted, and SRE confirms.
6. Accepted, and the rest go to PM as one story.

### For implementation: owed before `done`
- **AC-2 at the CLI:** a test where `content publish` by a Builder without the pack exits 1 with
  `error.code` `pack_not_held`. It's also on `AW-CLI-003`'s list, and it closes both stories.

With that and SRE's record, the story closes.

### For implementation, not holding the story
- `TestSetBuilderPacks_ARefusalDoesNotHoldTheWriteLock`: wait on `stall.entered` in a `select`
  with a deadline, so that a regression fails instead of hanging.
- Assert the log fields the implementation record lists (`acting_as_account_id`, `session_id`,
  `trace_id`), or correct the record.
- `server/README.md`'s Accounts section: `accounts.write` and the `andara.accounts` domain.

### For PM
- **Audit outside the Account write lock for every Admin write** (`SetRoles` and the rest), as
  `SetBuilderPacks` now does (5bebac0). Implementation, `server`. Not on the demo's path.

## Carried by `AW-SRV-046` (PM, 2026-10-02)

The "not holding the story" follow-ups for implementation above are now items in `AW-SRV-046`
(draft, SPRINT-04), which records each one as done here when it merges.
