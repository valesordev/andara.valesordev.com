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
