---
id: AW-SRV-008
title: Account store, registration modes, and authentication
epic: EPIC-08
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-005]
blocks: [AW-SRV-009, AW-SRV-013, AW-SRV-014]
lane: implementation
risk: high
---

> `status: draft` — unblocked by ADR-0006 and scoped. Groomed to `ready` when M2 approaches. This is the
> one story where grooming should include a security review before it reaches `ready`, not after it
> reaches `done`.

## Context

ADR-0006 decided: closed at launch, then invite-only beta, then open registration; Argon2id credentials;
self-hosted rather than federated; no email dependency in Phase 1.

This is the first story that persists genuinely sensitive data, and ADR-0006 is emphatic on one
structural point: **Account state is not World state.** It lives on `andara.accounts.v1`, separate from
the Command log, because a World rollback must not roll back credentials and a World snapshot shared for
debugging must not carry them.

## User story

As a player, I want to create an account and log in, so that my characters are mine.

## Scope

### In scope
- Account records on `andara.accounts.v1` (compacted), with Argon2id password hashing at a stated cost.
- Registration modes `closed`, `invite`, `open`, switchable without a deploy, every change audited.
- Invite codes: single-use, expiring, revocable, Account-scoped; issuance and redemption audited.
- Authentication returning a short-lived session token and a longer-lived refresh token.
- Roles: `player`, `builder`, `game_master`, `operator`, `agent`, checked in the `authorize` stage
  before a Command reaches the log.
- Operator-driven account creation and password reset via `Admin`, audited.
- Audit Events for every privileged action.

### Out of scope
- Character rosters and selection — `AW-SRV-014`.
- Session lifecycle and linkdead — `AW-SRV-015`.
- Email flows, including self-service password reset. ADR-0006 makes this a known cliff before the
  `open` transition, with its own story then.
- OIDC. A second credential type against the same Account record if ever wanted.

## Acceptance criteria (known now; completed at grooming)

1. **Given** any code path **when** an authentication succeeds or fails **then** no credential material
   appears in any log line, metric label, trace attribute, or error message. Asserted by a test that
   scans emitted telemetry, not by review.
2. **Given** a failed authentication **when** it is reported **then** the response does not distinguish
   an unknown Account from a wrong credential, and the timing does not either.
3. **Given** `registration_mode: closed` **when** self-registration is attempted **then** it is rejected,
   and the only path to an Account is an audited `Admin` call.
4. **Given** `registration_mode: invite` and a valid unredeemed code **when** registration is attempted
   **then** it succeeds and the code is marked redeemed atomically — a code must not be redeemable twice
   under concurrent attempts.
5. **Given** an expired or revoked invite code **when** registration is attempted **then** it is rejected
   with a message that does not reveal whether the code ever existed.
6. **Given** a role-restricted Command **when** submitted by an Account without the role **then** it is
   rejected at `authorize`, **before** it reaches the log, and one record is written to
   `andara.audit.v1`.
7. **Given** a World snapshot **when** it is inspected **then** it contains no Account or credential data.

## Interface contract

To be written at grooming, and reviewed for security before this story reaches `ready`. It will cover:
the Account record schema, the auth RPCs, token lifetimes and refresh semantics, the Argon2id parameters
and how they are re-tuned, and the invite-code lifecycle.

## Data / state impact

`andara.accounts.v1` is compacted and keyed by Account ID. Projected to Postgres by `AW-SRV-018` for
admin queries; the topic remains authoritative.

The separation from World state is the load-bearing property of this story. A test should assert it
directly rather than trusting that nobody will put an Account into a Zone.

## Observability requirements

- **Metrics:** `andara_auth_attempts_total` (counter, label `outcome`), `andara_registrations_total`
  (counter, label `mode`), `andara_invite_redemptions_total` (counter, label `outcome`),
  `andara_privileged_actions_total` (counter, label `action`). Account ID, character name, and
  credential material are rejected as labels — a metric label is a permanent, widely-replicated,
  low-security store.
- **Logs:** authentication attempts at `info` with outcome, never with the credential. Privileged actions
  at `info`, always, with actor and target.
- **Traces:** `session.authenticate` as a child of `session.lifetime`. No credential material in span
  attributes.
- **Alerts:** an auth-failure-rate alert is a security signal and needs a defined normal rate first. It
  belongs to the same grooming pass that defines the Session availability SLO.

## Test plan

The telemetry-scanning negative test from AC-1; concurrent invite redemption asserting single use;
timing-equivalence for AC-2; an audit-completeness test asserting every privileged action produces an
audit record; a snapshot-inspection test for AC-7.

## Definition of done

CLAUDE.md §8, plus: a security review of the auth path, and an explicit test that Account state and World
state are separately stored.

## Open questions

- `[ASSUMPTION]` Argon2id parameters chosen against a stated target verification time on production
  hardware, re-tuned as an explicit, reviewed change rather than drifting.
- **Resolved 2026-09-07 (Brian): `andara-cli` stores credentials, and acting as another identity
  always records who was really acting.** Two requirements land on this story from that. First, an
  acting-as request is only ever accepted from an authenticated caller — there is no anonymous
  impersonation, because there would be nobody to record. Second, every audit record produced under
  acting-as names **both** identities: the authenticated Account that acted and the identity it acted
  as. One field is not enough; an audit trail that records only the acted-as identity is worse than
  none, because it looks complete.

  `AW-CLI-001` owns where the credential file lives and its `0600` requirement. This story owns what a
  credential *is*. The consequence for sequencing: `andara-cli play` ships with no `--as` at all until
  this story lands, rather than an anonymous one that later grows identity.

- `[NEEDS BRIAN]` Token lifetimes. Short session tokens with refresh is the shape; the numbers are a
  security-versus-annoyance call.
- `[NEEDS BRIAN]` Whether Behavior Agent credentials are per-deployment (ADR-0005's assumption) or
  per-NPC. Per-deployment is far simpler and means a compromised Agent compromises its whole NPC set.
