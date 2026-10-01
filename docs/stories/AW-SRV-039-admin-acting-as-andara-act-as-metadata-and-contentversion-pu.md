---
id: AW-SRV-039
title: Admin acting-as — andara-act-as metadata and ContentVersion.publisher
epic: EPIC-08
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-013, AW-CLI-003]
blocks: []
lane: implementation
risk: medium
---

## Context

An Operator can open a game Session as another Account (`Game.OpenSession.act_as_account_id`,
`AW-SRV-008`). No Admin call can act as anyone, though. So `andara-cli content publish --as
<builder>` doesn't exist, and `AW-CLI-003` shipped without `--as` (its §8 review, ruling 1). The demo
doesn't need it, because the Operator publishes as themselves and self-approves (AC-11).

Architecture's §8 review of `AW-SRV-013` ruled the contract (rulings 2 and 3). Acting-as over Admin
is per-call gRPC metadata, not a token claim. The real actor goes on the manifest as
`ContentVersion.publisher`, because rebuilding it from the audit topic doesn't survive the topic's
365-day retention or a restart under `auth.store=memory`. The two ship together and in no other
order. If the metadata were honoured before the field exists, acted-as manifests would be written
with no durable real actor. Sources: `docs/feedback/AW-SRV-013-publish-path.md` ("For PM", item 1),
and `AW-CLI-003`'s §8 review.

## User story

As an operator, I want to run an Admin command as a Builder and have the manifest record both of us,
so that a pack's history shows who published it and who actually did it.

## Scope

### In scope
- **Gateway:** the `andara-act-as: <account_id>` gRPC metadata on Admin calls, resolved into the
  Principal the content code already reads.
- **`ContentVersion.publisher = 10`,** written on every publish.
- **`andara-cli`:** `--as <account_id>` on the content commands that write (`publish`, `approve`,
  `activate`, `rollback`), which sends the metadata.
- **`AW-CLI-003` AC-11's acting-as clause:** the CLI's self-approval prompt compares the manifest's
  `publisher` and `author` with the caller and with its own `--as` value.
- **Removing `isCaller`'s `act`-claim check** (`admin/cli/contentpublish.go`). It's dead code today.

### Out of scope
- Acting-as on non-content Admin calls through the CLI. The gateway honours the metadata on every
  Admin call, but only the content commands gain `--as` here.
- Username lookup for `--as`. Accounts are named by ID (`AW-CLI-003` ruling 2), the same gap
  `AW-SRV-035` has.
- Any change to `Game.OpenSession.act_as_account_id`.

## Acceptance criteria

1. **Given** an Operator's token and `andara-act-as: <builder>` on `PublishVersion` **when** it
   succeeds **then** the manifest's `author` is `<builder>`, its `publisher` is the Operator, and
   the audit record names both identities.
2. **Given** a publish with no metadata **then** `publisher` equals `author`.
3. **Given** a principal without `operator` or `game_master` sending `andara-act-as` on any Admin
   call **then** the call fails `PERMISSION_DENIED`, nothing is written, and the refusal is audited
   (`ActionActAs`, `denied`, "requires operator or game_master"), the same as `OpenSession`'s
   refusal.
4. **Given** `andara-act-as` naming an unknown or inactive Account **then** the call fails
   `PERMISSION_DENIED`, nothing is written, and the refusal is audited "no such active account".
5. **Given** `andara-act-as` on a Game RPC **then** it's ignored. Acting-as on Game is
   `OpenSession`'s field only.
6. **Given** a manifest written before this story (no `publisher`) **when** it's read **then**
   `publisher` is reported equal to `author`. No migration runs.
7. **Given** `andara-cli --as <builder> content publish` **when** it runs **then** it sends the
   metadata, and the manifest shows `author=<builder>` and `publisher=<operator>`.
8. **Given** an Operator approving a version that they published `--as` a Builder **when**
   `content approve` runs **then** the CLI's self-approval prompt (AC-11) appears, and the server
   audits it as a self-approval while `content.operator_self_approval` holds.
9. **Given** the CLI **then** no code path reads the token's `act` claim. `isCaller` is gone, and a
   test fails if the claim is consulted.
10. **Given** `content history --output json` **then** each entry carries `publisher`.

## Interface contract

- **Metadata:** `andara-act-as: <account_id>`, per call, on the Admin service only. It's honoured for
  principals holding `operator` or `game_master`, with `OpenSession`'s refusals (`PERMISSION_DENIED`,
  audited). The gateway's existing `ActAs(ctx, p, target)` resolves it.
- **Protocol:** `ContentVersion` gains `string publisher = 10;`. It's the real actor, always set on
  write, and equals `author` when nobody acted as anyone. Architecture lands the `content.proto`
  amendment at contract review, and this story's PR regenerates `gen/` with `make proto`.
- **CLI:** `--as <account_id>` on `content publish|approve|activate|rollback`. Exit codes as
  `AW-CLI-003`. A refusal is exit `1` with the server's message.
- **Audit:** every acted-as call writes one record with `actor_account_id` (the real actor) and
  `acting_as_account_id`, as `OpenSession` does.

## Data / state impact

`ContentVersion` adds field 10, which is additive. Old manifests read with `publisher` empty, which
is reported as `author` (AC-6). That's true, not a fallback, because no acted-as manifest could exist
before this story. Rolling back the binary leaves field 10 unread and harmless.

## Observability requirements

- **Metrics:** `andara_admin_act_as_total{outcome}` (counter), with `outcome` in
  `ok|denied|unknown_account`. Its cardinality is 3, and it's pre-seeded at 0.
- **Logs:** an `info` line, `admin act-as`, with `actor`, `acting_as`, `method` and `trace_id`, on
  every honoured call. A `warn` line with the same fields plus `reason` on a refusal.
- **Traces:** the existing Admin RPC span gains the attributes `auth.acting_as` (the account ID, a
  span attribute only, never a metric label) and `auth.act_as_outcome`.
- **Alerts:** none.

## Test plan

- **Unit:**
  - the gateway's metadata resolution for each of ACs 3–5;
  - `publisher` on write, and its read default (AC-2, AC-6);
  - the CLI's `--as` plumbing and the prompt (AC-8);
  - a test that fails if the CLI reads `act` (AC-9).
- **Integration:** against the local stack, an acted-as publish, approve and activate, asserting the
  manifest, the audit records, and `andara_admin_act_as_total` (AC-1, AC-7).
- **Manual/operator:**
  ```
  andara-cli --as <builder-id> content publish --path ./mypack   # expect: version N, author <builder-id>
  andara-cli content history mypack -o json | jq '.[-1].publisher'   # expect: the operator's account ID
  ```

## Definition of done

CLAUDE.md §8.

## Open questions

- `[ASSUMPTION]` `--as` goes on the write commands only. The read commands (`history`, `diff`,
  `fetch`) gain nothing from acting as someone.
- `[ASSUMPTION]` The metric and the span attributes are PM's proposal, for SRE's observability
  review.
