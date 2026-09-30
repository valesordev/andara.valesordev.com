---
id: AW-SRV-035
title: An Operator grants a Builder their packs
epic: EPIC-06
component: server
type: feature
status: review
size: S
depends_on: [AW-SRV-013]
blocks: [AW-INF-021, AW-INF-023]
lane: implementation
risk: low
---

## Context

`AW-SRV-013` authorizes every content write on two things: the `builder` role, and the pack being in
`Account.builder_packs` (decided 2026-09-11: Builder authority is scoped per pack). It adds the field,
but no RPC sets it. `AW-SRV-008`'s `SetRoles` grants the role and nothing grants a pack, so once
`AW-SRV-013` lands, no Builder can publish anything. The only way around that would be an Operator
acting on every pack under `override`, which defeats the per-pack scope.

This story is the missing grant. It mirrors `SetRoles`: an Operator action over `Admin`, audited,
and exposed as `andara-cli account set-packs`. It's the first step a new Builder needs on `dev`
(`AW-INF-023`'s guide starts here).

## User story

As an operator, I want to grant and revoke the Content Packs a Builder may publish to, so that a new
Builder can publish their pack and nobody else's.

## Scope

### In scope
- `Admin.SetBuilderPacks`: replaces an Account's `builder_packs` with the given set. Operator only.
- `andara-cli account set-packs <account-id> --pack ID [--pack ID ...]`, and `--clear`, with
  `--expected-version` as `set-roles` has.
- An audit record per change on `andara.audit.v1`, as `SetRoles` writes one.

### Out of scope
- The checks that read `builder_packs`: `AW-SRV-013`.
- Creating a pack. A pack exists once its first version is published. Granting a pack ID nobody
  has published yet is how a Builder is given a new pack.
- Self-service requests for a pack. A Builder asks an Operator (the guide says how, `AW-INF-023`).

## Acceptance criteria

1. **Given** an Account holding `builder` and no packs **when** an Operator runs
   `account set-packs <id> --pack town --pack docks` **then** it exits 0, prints
   `<username>: builder packs docks, town`, and that Builder's `content publish` for `town` passes
   `AW-SRV-013`'s authorization.
2. **Given** that Builder **when** they publish to `wilds` **then** `AW-SRV-013` AC-7 holds:
   `PERMISSION_DENIED`, and the CLI exits `1` with `error.code` `pack_not_held`.
3. **Given** `set-packs <id> --pack town` on an Account holding `docks, town` **when** it runs
   **then** `builder_packs` is exactly `[town]`. The call replaces the set; it doesn't add to it.
4. **Given** `set-packs <id> --clear` **when** it runs **then** `builder_packs` is empty, it prints
   `<username>: builder packs none`, and a publish to `town` by that Builder is `PERMISSION_DENIED`.
5. **Given** a caller without `operator` **when** it calls `SetBuilderPacks` **then**
   `PERMISSION_DENIED`, the Account is unchanged, and one audit record is written with
   `outcome=denied`.
6. **Given** `--pack andara.core` **when** it runs **then** `INVALID_ARGUMENT`, `andara.core is
   published by the server and can't be granted` (`AW-SRV-013` AC-11), exit `1`, and the Account is
   unchanged.
7. **Given** a pack ID that isn't a valid pack identifier, `LOWER_ID` segments joined by `.`
   (Content Language `semantics.md` §1) **when** it runs **then** `INVALID_ARGUMENT` naming it, exit
   `1`, and the Account is unchanged. The CLI doesn't pre-check it: the server is the boundary.
8. **Given** an Account without `builder` **when** packs are granted **then** the grant is stored
   and it prints `<username>: builder packs town (inactive: no builder role)`. Roles and packs are
   set independently, so revoking the role doesn't lose the grant.
9. **Given** any successful change **when** the audit topic is read **then** one record carries
   `action=set_builder_packs`, the actor, the target, and the before and after sets.
10. **Given** a grant and then a server restart **when** the Builder publishes to `town` **then**
    the publish passes authorization. The grant lives in the Account store, as roles do.
11. **Given** `--expected-version 3` on an Account at record version `4` **when** it runs **then**
    `ABORTED`, exit `1`, and the Account is unchanged, as `set-roles` behaves (`AW-SRV-008`).

## Interface contract

**Pinned 2026-09-28** in `docs/specs/protocol/andara/admin/v1/admin.proto`. The audit fields are in
`audit.proto` (`builder_packs_before = 27`, `builder_packs_after = 28`).

```protobuf
// CONTRACT SKETCH — not an implementation. The pinned form is admin.proto.
rpc SetBuilderPacks(SetBuilderPacksRequest) returns (SetBuilderPacksResponse);

message SetBuilderPacksRequest {
  string account_id = 1;
  repeated string packs = 2;              // the whole new set; empty clears
  uint64 expected_record_version = 3;     // 0: any, as SetRoles
}
message SetBuilderPacksResponse {
  repeated string builder_packs = 1;      // as stored: sorted, deduplicated
  uint64 record_version = 2;
  bool builder_role = 3;                  // false: stored and inert (AC-8)
}
```

```
andara-cli account set-packs <account-id> ((--pack ID)... | --clear) [--expected-version N]
```

Exit codes are `AW-CLI-001`'s shared taxonomy:

| Exit | Meaning |
|-----:|---------|
| `0` | set |
| `1` | the server refused: not an operator, account not found, invalid pack ID, `andara.core`, stale record version. `error.code` is the reason below |
| `2` | usage: neither `--pack` nor `--clear`, or both |
| `3` | server unreachable |
| `4` | timeout |

| Condition | gRPC code | `ErrorInfo.reason` |
|-----------|-----------|--------------------|
| caller lacks `operator` | `PERMISSION_DENIED` | `operator_only` |
| account not found | `NOT_FOUND` | `account_not_found` |
| invalid pack ID | `INVALID_ARGUMENT` | `invalid_pack_id` |
| `andara.core` | `INVALID_ARGUMENT` | `core_not_grantable` |
| record version stale | `ABORTED` | `record_version` |

The audit record is `action=set_builder_packs`, `target=<account_id>`, and `outcome` one of `ok`,
`denied`, `invalid`, `conflict`. It also carries `builder_packs_before` and `builder_packs_after`,
with `after` empty unless `ok`.

`--output json` returns the `AW-CLI-001` envelope with `builder_packs`. No new configuration.

## Data / state impact

`Account.builder_packs = 12` is added by `AW-SRV-013`. This story only writes it. An Account written
before that field exists reads as empty, so no migration is needed. Rolling back to a build without
`SetBuilderPacks` leaves the stored grants in place, and `AW-SRV-013`'s checks still read them.

## Observability requirements

*(SRE observability review, 2026-09-28: the privileged-action counter added, and the trace
corrected. `SetRoles` has no store-write span to copy.)*

- **Metrics:** none new.
  - The Gateway's RED series, `andara_grpc_requests_total{method,code}` and
    `andara_grpc_request_duration_seconds{method}`, gain one bounded `method` value.
  - `andara_privileged_actions_total{action}` gains `action="set_builder_packs"`. The value is added
    to `auth.AllActions`, so it's pre-seeded at 0 from the first scrape, as every other action is.
  - Pack IDs and account IDs are never labels. They appear only in logs, span attributes, and the
    audit record.
- **Logs:**
  - `info` `builder packs set`, with `actor_account_id`, `acting_as_account_id` (empty unless
    `--as`), `target_account_id`, `before`, `after`, `session_id`, `trace_id`.
  - `warn` on denial, with the same fields minus `after`, plus `reason`.
- **Traces:**
  - The Gateway interceptor's server span for `SetBuilderPacks`, parented by the CLI's `cli.command`
    through `traceparent`, as for every Admin RPC.
  - Under it, one new child span, `accounts.write`, around the Account store produce, with
    attributes `action=set_builder_packs` and `outcome`. CLAUDE.md §7 makes persistence writes
    trace-worthy.
  - `SetRoles` has no such span today. Adding one there is out of scope, and it isn't a reason to
    omit it here.
- **Alerts:** none. A denied grant is audited and counted, not alerted.

## Test plan

- **Unit:** replace semantics, sort and deduplication, `andara.core` refused, pack-ID validation,
  and the operator check.
- **Integration:** against Redpanda. Grant, publish (AC-1), refuse on an ungranted pack (AC-2),
  replace (AC-3), clear (AC-4), a restart keeping the grant (AC-10), and the audit record (AC-9),
  polled per `live-assertions.md`.
- **Manual/operator:**
  ```
  andara-cli account set-roles <id> --role builder
  andara-cli account set-packs <id> --pack town        # "<username>: builder packs town"
  ```

## Definition of done

CLAUDE.md §8.

## Open questions

- **Resolved 2026-09-28 (architecture): replace**, matching `SetRoles`. `expected_record_version`
  makes a replace safe against a concurrent grant, which an add/remove pair wouldn't need but a
  replace does.
- There's no `account show` and no `GetAccount` RPC, so an Operator can't read a grant back except
  through `set-packs`' own output. That's left out here. If the guide (`AW-INF-023`) needs a read,
  it's a separate story.

## Contract review (architecture, 2026-09-28)

SRE's observability review is in `docs/feedback/AW-INF-021-dev-content-store.md` and in the story's
Observability section: the privileged-action counter and the `accounts.write` span. The story is
`ready`.

1. **The proto is pinned** in `admin.proto`, with `expected_record_version`, `record_version` and
   `builder_role` added. Every other Account write in `Admin` has optimistic concurrency, and a
   replace without it silently drops a grant made between an Operator's read and write (AC-11).
   `builder_role` is how the CLI knows to print AC-8's `(inactive: no builder role)` without a
   second read.
2. **Exit codes follow `AW-CLI-001`.** The draft used `4` for "refused", which is a timeout in the
   shared taxonomy and in `admin/cli/errors.go`. A server refusal is `1` with the reason as
   `error.code`. `2` stays for invocations the CLI can reject on its own.
3. **AC-6's message follows the core decision** (ADR-0004, 2026-09-28): `andara.core` is published by
   the server, so no Account holds a grant to it. That includes Operators, who move its pointer
   without one.
4. **The audit record's before and after sets are fields** (`audit.proto` 27, 28), not free text in
   `detail`. AC-9 is asserted on them.
5. `AW-SRV-013` supplies `Account.builder_packs = 12`, now pinned. The glossary's **Pack Grant**
   already names it.

## Implementation record (2026-09-30)

On `impl/aw-srv-035-account-set-packs`. `auth.Store.SetBuilderPacks` is the grant, next to
`SetRoles`. `auth.Admin.SetBuilderPacks` serves it and the Gateway delegates to it. Refusals carry
`ErrorInfo` (domain `andara.accounts`) with the contract's reasons. `andara-cli account set-packs`
passes each reason through as `error.code`. Decisions the contract didn't make are in
`docs/feedback/AW-SRV-035-pack-grants.md`.

| AC | Covered by | Result |
|----|------------|--------|
| 1 | `TestAccountSetPacks_GrantReplaceClear`: exit 0, `<id>: builder packs docks, town`. The publish passes authorization in `server/content` `TestPackGrant_IsWhatThePublishPathAuthorizesOn` and `TestPackGrant_AgainstABroker` | pass; prints the account ID, not the username (feedback 1) |
| 2 | `TestPackGrant_*`: a publish to `wilds` is `PERMISSION_DENIED` `pack_not_held` | pass for the server; the CLI exit is `content publish`'s, AW-CLI-003 (feedback 2) |
| 3 | `TestSetBuilderPacks_ReplacesSortsAndAudits`; `TestAccountSetPacks_GrantReplaceClear` | pass |
| 4 | the same two tests (`builder packs none`, empty set); the `TestPackGrant_*` tests refuse the publish after clearing | pass |
| 5 | `TestSetBuilderPacks_OperatorOnly`: `operator_only`, the Account unchanged, one `denied` record. `TestAccountSetPacks_Refusals` logs in as the Builder: exit 1, `operator_only`. Mutation-checked: without the refusal's audit, the tests fail | pass |
| 6 | `TestSetBuilderPacks_Refusals`, `TestAccountSetPacks_Refusals`: `core_not_grantable` with the contract's message, exit 1. Mutation-checked | pass |
| 7 | the same two: uppercase, an empty segment, a leading digit, a comma, empty. Each is `invalid_pack_id` naming the value, exit 1. The CLI passes `--pack` through as given | pass |
| 8 | `TestSetBuilderPacks_IndependentOfTheRole`: the grant survives the role coming and going. The CLI prints `(inactive: no builder role)` | pass |
| 9 | `TestSetBuilderPacks_ReplacesSortsAndAudits`: `action=set_builder_packs`, actor, target, `builder_packs_before` and `builder_packs_after`. `TestPackGrant_AgainstABroker` reads the three records back from Redpanda, polled | pass |
| 10 | `TestSetBuilderPacks_SurvivesARestart` over in-memory logs. `TestPackGrant_AgainstABroker` reopens the store from the broker and publishes again | pass |
| 11 | `TestSetBuilderPacks_Refusals` and `TestAccountSetPacks_Refusals`: `ABORTED` `record_version`, exit 1, unchanged | pass |

**Instrumentation**, asserted by `TestSetBuilderPacks_Instrumentation`:
- `andara_privileged_actions_total{action="set_builder_packs"}` is pre-seeded at 0 and counts each
  call.
- The `accounts.write` span carries `action` and `outcome`.
- The `info` `builder packs set` line carries `actor_account_id`, `acting_as_account_id`,
  `target_account_id`, `before`, `after`, `session_id` and `trace_id`.
- `TestSetBuilderPacks_OperatorOnly` asserts the `warn` `builder packs refused` line with its
  `reason`.
- The Gateway's RED series and server span come from its interceptor, as for every Admin RPC.

`make check` passes. `TestSnapshotCopyStaysInsideTheStallBudget` (#172) failed once under
full-suite load and passed on the rerun. `server/content`'s broker tests pass against the local
Redpanda.

## §8 instrumentation check (2026-09-30, SRE): satisfied

On `sre/sprint-03-srv035-cli003-verify`, against the compose stack built from `main` at `fa9911e`,
with fresh volumes. `andara-cli account set-packs` was run as the bootstrap Operator on a new
Account `sre-builder`, then as that Builder, then with `andara.core`, then with no flags.

| Signal | Backend | Observed |
|--------|---------|----------|
| `andara_privileged_actions_total{action="set_builder_packs"}` | local Prometheus | `0` before any call (pre-seeded), `3` after the three calls |
| `andara_grpc_requests_total{method="andara.admin.v1.Admin/SetBuilderPacks",code}` | local Prometheus | `ok` 1, `permission_denied` 1, `invalid_argument` 1. `…_duration_seconds_count` 3 |
| `info` `builder packs set` | local Loki | `actor_account_id` (the Operator), `acting_as_account_id` empty, `target_account_id`, `before=[]`, `after=["sre.docks","sre.town"]`, `trace_id` |
| `warn` `builder packs refused` | local Loki | the Builder's call: `reason=operator_only`, with `before` and no `after`. The core grant: `reason=core_not_grantable` |
| Trace | local Tempo (`6a21e921…`) | `andara.admin.v1.Admin/SetBuilderPacks` → `session.authenticate`, `accounts.write` (`action=set_builder_packs`, `outcome=ok`). Its parent is the CLI's `cli.command`, through `traceparent`. The denied call's trace has no `accounts.write`, since nothing was written |
| CLI exits | — | `0` set, `1` denied and `andara.core`, `2` neither `--pack` nor `--clear` |

**Noted for architecture, not holding the story.** `session_id` is empty on every Admin log line,
here and on `create_account` and `set_roles` too. An Admin call runs in no Game Session, so the
field can't be filled, and `trace_id` does the correlating. The Observability sections that list
`session_id` for Admin paths (this story, `AW-SRV-013`) should say it's empty on Admin, or name
what fills it.
