---
id: AW-SRV-035
title: An Operator grants a Builder their packs
epic: EPIC-06
component: server
type: feature
status: draft
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
- `andara-cli account set-packs <account-id> --pack ID [--pack ID ...]`, and `--clear`.
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
   `PERMISSION_DENIED`, and the CLI exits `4`.
3. **Given** `set-packs <id> --pack town` on an Account holding `docks, town` **when** it runs
   **then** `builder_packs` is exactly `[town]`. The call replaces the set; it doesn't add to it.
4. **Given** `set-packs <id> --clear` **when** it runs **then** `builder_packs` is empty, it prints
   `<username>: builder packs none`, and a publish to `town` by that Builder is `PERMISSION_DENIED`.
5. **Given** a caller without `operator` **when** it calls `SetBuilderPacks` **then**
   `PERMISSION_DENIED`, the Account is unchanged, and one audit record is written with
   `outcome=denied`.
6. **Given** `--pack andara.core` **when** it runs **then** `INVALID_ARGUMENT`, `andara.core is
   operator-only and can't be granted` (`AW-SRV-013` AC-11), exit `2`.
7. **Given** a pack ID that isn't a valid pack identifier (Content Language `semantics.md` §1)
   **when** it runs **then** `INVALID_ARGUMENT` naming it, exit `2`, and the Account is unchanged.
8. **Given** an Account without `builder` **when** packs are granted **then** the grant is stored
   and it prints `<username>: builder packs town (inactive: no builder role)`. Roles and packs are
   set independently, so revoking the role doesn't lose the grant.
9. **Given** any successful change **when** the audit topic is read **then** one record carries
   `action=set_builder_packs`, the actor, the target, and the before and after sets.
10. **Given** a grant and then a server restart **when** the Builder publishes to `town` **then**
    the publish passes authorization. The grant lives in the Account store, as roles do.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation. Additions to andara/admin/v1/admin.proto
rpc SetBuilderPacks(SetBuilderPacksRequest) returns (SetBuilderPacksResponse);

message SetBuilderPacksRequest {
  string account_id = 1;
  repeated string packs = 2;   // the whole new set; empty clears
}
message SetBuilderPacksResponse {
  repeated string builder_packs = 1;   // as stored: sorted, deduplicated
}
```

```
andara-cli account set-packs <account-id> (--pack ID)... | --clear
```

| Exit | Meaning |
|-----:|---------|
| `0` | set |
| `2` | usage, invalid pack ID, `andara.core` |
| `3` | server unreachable |
| `4` | refused: not an operator, account not found |

| Condition | gRPC code |
|-----------|-----------|
| caller lacks `operator` | `PERMISSION_DENIED` |
| account not found | `NOT_FOUND` |
| invalid pack ID, `andara.core` | `INVALID_ARGUMENT` |

`--output json` returns the `AW-CLI-001` envelope with `builder_packs`. No new configuration.

## Data / state impact

`Account.builder_packs = 12` is added by `AW-SRV-013`. This story only writes it. An Account written
before that field exists reads as empty, so no migration is needed. Rolling back to a build without
`SetBuilderPacks` leaves the stored grants in place, and `AW-SRV-013`'s checks still read them.

## Observability requirements

- **Metrics:** none new. The Gateway's RED series, `andara_grpc_requests_total{method,code}` and
  `andara_grpc_request_duration_seconds{method}`, gain one more bounded `method` value.
- **Logs:** `info` `builder packs set` with `actor_account_id`, `target_account_id`, `before`,
  `after`, `session_id`, `trace_id`. `warn` on denial.
- **Traces:** the RPC's server span, with the Account store write as a child, as `SetRoles` has.
- **Alerts:** none.

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

- `[ASSUMPTION]` Replace, not add or remove, matching `SetRoles`. An Operator re-states the set. If
  architecture prefers `--add`/`--remove`, that changes the CLI flags only.
- There's no `account show` and no `GetAccount` RPC, so an Operator can't read a grant back except
  through `set-packs`' own output. That's left out here. If the guide (`AW-INF-023`) needs a read,
  it's a separate story.
