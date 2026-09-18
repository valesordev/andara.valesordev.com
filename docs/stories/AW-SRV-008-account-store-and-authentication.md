---
id: AW-SRV-008
title: Account store, registration modes, and authentication
epic: EPIC-08
component: server
type: feature
status: done
size: M
depends_on: [AW-SRV-005]
blocks: [AW-SRV-003, AW-SRV-009, AW-SRV-013, AW-SRV-014, AW-SRV-025]
lane: implementation
risk: high
---

## Context

ADR-0006 decided: closed at launch, then invite-only beta, then open registration; Argon2id credentials;
self-hosted rather than federated; no email dependency in Phase 1.

This is the first story that persists genuinely sensitive data, and ADR-0006 is emphatic on one
structural point: **Account state is not World state.** It lives on `andara.accounts.v1`, separate from
the Command log, because a World rollback must not roll back credentials and a World snapshot shared for
debugging must not carry them.

This story is also where `AW-SRV-005`'s stub verifier is replaced. Every later story that says "an
authenticated Session" means the `Principal` defined here.

## User story

As a player, I want to create an account and log in, so that my characters are mine.

## Scope

### In scope
- `andara.auth.v1.Auth` service: `Register`, `Authenticate`, `Refresh`, `Revoke`.
- Account records on `andara.accounts.v1`, compacted, keyed by Account ID, with Argon2id credentials at
  a stated cost stored alongside the hash so re-tuning is a rehash on next login.
- Registration modes `closed`, `invite`, `open`, held as a record on the same topic so the switch is an
  audited write, not a deploy.
- Invite codes, Account-scoped: single-use, expiring, revocable; issuance and redemption audited.
- Session tokens (signed, stateless, short-lived) and refresh tokens (opaque, hashed at rest,
  revocable). The session token is what `OpenSessionRequest.auth_token` carries.
- Roles `player`, `builder`, `game_master`, `operator`, `agent` as Account attributes; the
  `authorize` stage checks them against the verb table before a Command reaches the log.
- `agent` Accounts scoped to one Content Pack (ADR-0005, ADR-0006, and the 2026-09-11 decision that
  Builders write Behaviors), with two credential kinds: `WORKLOAD_JWT` (projected service-account
  token, the cluster path ADR-0005 asks for) and `API_KEY` (for `make up`, where there is no issuer).
- Acting-as: an authenticated `operator` or `game_master` may open a Session as another Account; every
  audit record names both identities.
- `Admin` RPCs: create account, reset password, grant and revoke roles, issue and revoke invites, set
  registration mode. All audited.

### Out of scope
- Character rosters and selection — `AW-SRV-014`. Session lifecycle and linkdead — `AW-SRV-015`.
- Email flows including self-service password reset — the ADR-0006 cliff before `open`; its own story.
- OIDC and mTLS workload identity — later credential kinds against the same record.
- The Postgres projection of accounts — `AW-SRV-018`.

## Acceptance criteria

1. **Given** any code path **when** an authentication succeeds or fails **then** no credential
   material, token, or hash appears in any log line, metric label, trace attribute, or error message.
   Asserted by a test that scans emitted telemetry for every secret it planted.
2. **Given** a failed authentication **when** it is reported **then** the response is
   `UNAUTHENTICATED` with one fixed message for unknown Account and wrong credential alike, and the
   median response times for the two differ by less than 10% over 1,000 attempts.
3. **Given** `registration_mode=closed` **when** `Register` is called **then** it returns
   `FAILED_PRECONDITION` and the only path to an Account is `Admin.CreateAccount`, which writes an audit
   record.
4. **Given** `registration_mode=invite` and a valid unredeemed code **when** 50 concurrent `Register`
   calls present it **then** exactly one succeeds and the code is marked redeemed before the successful
   response is sent.
5. **Given** an expired, revoked, or never-issued invite code **when** `Register` is called **then** it
   returns `PERMISSION_DENIED` with one fixed message for all three.
6. **Given** a verb whose table entry requires `builder` **when** a `player` Session submits it **then**
   `authorize` returns `ErrNotAuthorized`, nothing is produced to `andara.commands.v1`, and one record
   is written to `andara.audit.v1` naming actor, verb, and Session.
7. **Given** a World snapshot round **when** every Zone object is decoded **then** no `Account`,
   credential, or token bytes are present. Asserted by planting a known username and searching the
   bytes.
8. **Given** a session token issued before a server restart **when** the restarted server verifies it
   **then** it is accepted until its expiry, so a linkdead reconnect within `session.linkdead_grace`
   never fails on authentication.
9. **Given** a refresh token that has been revoked **when** `Refresh` is called **then** it returns
   `UNAUTHENTICATED` and the attempt is audited.
10. **Given** an `operator` opening a Session with `act_as_account_id` set **when** any audited action
    occurs in that Session **then** the audit record carries both `actor_account_id` and
    `acting_as_account_id`; **given** a `player` setting the same field **then** `OpenSession` returns
    `PERMISSION_DENIED`.
11. **Given** an `agent` Account scoped to pack `P` **when** it attempts to bind an Entity whose
    Template is not from `P` **then** `authorize` rejects it and audits it.
12. **Given** an Account set `DISABLED` or a role removed via `Admin` **when** a still-valid session
    token for it is presented to `OpenSession` **then** `UNAUTHENTICATED`; and every open Session for
    that Account is closed within `auth.recheck_interval` with `SubscriberDropped{reason=REVOKED}`.
    The token is a bearer of *identity*; roles and status are read from the in-memory Account index on
    every `OpenSession` and re-read per Session on the recheck interval.
13. **Given** Argon2id parameters changed in config **when** an Account with the old parameters
    authenticates successfully **then** its record is rewritten with the new parameters, and a record
    that has not logged in keeps the old ones and still verifies.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation; andara/auth/v1/auth.proto
service Auth {
  rpc Register(RegisterRequest) returns (RegisterResponse);          // username, password, invite_code
  rpc Authenticate(AuthenticateRequest) returns (AuthenticateResponse); // username, password → {TokenPair tokens}
  rpc Refresh(RefreshRequest) returns (RefreshResponse);             // refresh_token → {TokenPair tokens}
  rpc Revoke(RevokeRequest) returns (RevokeResponse);                // refresh_token, all: every token on its Account
}
message TokenPair { string session_token = 1; int64 session_expires_unix = 2;
                    string refresh_token = 3; int64 refresh_expires_unix = 4; }

// andara/accounts/v1/account.proto — the andara.accounts.v1 record, key = account_id
message AccountRecord { oneof record { Account account = 1; AuthConfig config = 2; } }
message Account {
  string account_id = 1; string username = 2; Credential credential = 3;
  repeated Role roles = 4;                     // sorted; enum Role { PLAYER=1; BUILDER=2; GAME_MASTER=3; OPERATOR=4; AGENT=5; }
  AccountStatus status = 5;                    // ACTIVE, DISABLED
  int64 created_unix = 6;
  repeated RefreshTokenRecord refresh_tokens = 7;   // hash, issued, expires, revoked; sorted by hash
  repeated Invite invites = 8;                 // codes this Account issued; sorted by code_hash
  string agent_pack_id = 9;                    // non-empty only for AGENT
  string workload_subject = 13;                // AGENT with WORKLOAD_JWT: expected JWT `sub`
  uint64 record_version = 10;                  // optimistic concurrency for Admin writes
}
message Credential { CredentialKind kind = 1; bytes hash = 2; bytes salt = 3; Argon2Params params = 4; }  // kind: PASSWORD, API_KEY, WORKLOAD_JWT
message AuthConfig { RegistrationMode mode = 1; string changed_by = 2; int64 changed_unix = 3; }
```

`OpenSessionRequest` gains `string act_as_account_id = 4`. `auth_token` carries the session token.

*Corrected 2026-09-14 during implementation:* `Authenticate` and `Refresh` wrap `TokenPair` in a
per-RPC response because buf's `RPC_REQUEST_RESPONSE_UNIQUE` rule refuses one response type on two
RPCs. `Revoke` identifies the caller by the presented refresh token — possessing it is the authority
to revoke it — and `all=true` revokes every refresh token on that Account; there is no separate
"caller" identity because the RPC is unauthenticated by design. `Refresh` rotates: the presented token
is retired and a new one issued, so a leaked refresh token is good for one use. The record is as
sketched, with `Invite.redeemed_by_account_id` added so the burn names who came in on the code.

### Tokens

| Token | Form | Lifetime | Storage |
|-------|------|---------:|---------|
| session | signed: `base64url(payload).base64url(HMAC-SHA256)`; payload = account_id, acting_as, exp, key_id, nonce — **not roles**; those come from the Account index at use (AC-12). `acting_as` is empty on every token issued today: acting-as is decided at `OpenSession` (AC-10), not at `Authenticate` | `auth.session_ttl` = 1 h | none — stateless (AC-8) |
| refresh | 32 random bytes, base64url | `auth.refresh_ttl` = 30 d | SHA-256 hash in the Account record |
| API key (`agent`, local/dev) | `ak_` + 32 random bytes | until revoked | Argon2id hash in `credential` |
| workload JWT (`agent`, cluster) | projected Kubernetes service-account token | token `exp` | none — verified against `auth.k8s_issuer`; `Account.workload_subject` must match `sub` |

Signing keys come from `auth.token_key_file`, a file of `key_id: base64` lines; the first is current,
the rest verify only. Rotation is: add a key, deploy, make it first, deploy, remove the old one after
`session_ttl`.

### Verifier

```go
// CONTRACT SKETCH — not an implementation
package auth
type Principal struct { AccountID string; Roles []Role; ActingAs string; AgentPackID string; SessionExp time.Time }
type Verifier interface {
    Verify(ctx context.Context, token string) (Principal, error)
    ActAs(ctx context.Context, p Principal, target string) (Principal, error)   // AC-10; audited either way
    Recheck(p Principal) error                                                  // AC-12; the Gateway polls it
}
// Authorize replaces AW-SRV-003's stub: verb table row -> required role; agent scope -> pack check.
type Authorizer struct { Table VerbRoles; Audit *Auditor }                     // VerbRoles = map[verb]Role
func (a *Authorizer) Authorize(ctx, verb string, p Principal, sessionID string) error
func (a *Authorizer) AuthorizeBind(ctx, p Principal, templatePackID, sessionID string) error
```

*Corrected 2026-09-14 during implementation:* `Authorize` takes the verb and the Session ID rather
than `sim.Command` and `*gateway.Session` — the Gateway imports `auth` for its Verifier, so `auth`
importing the Gateway would be a cycle, and `sim.Command` does not exist until AW-SRV-003. The
information is the same. When acting as another Account the Session takes the **target's** roles
(an operator sees what the player sees) and the audit record names both; confirmed by Brian
2026-09-18. Roles are a set, not a ladder: an operator needing a builder-gated verb holds
`builder` too.

### Admin RPCs

`CreateAccount`, `ResetPassword`, `SetRoles`, `SetAccountStatus`, `IssueInvite`, `RevokeInvite`,
`SetRegistrationMode`, `CreateAgentAccount` (returns the API key exactly once). Each requires
`operator`; each writes one `andara.audit.v1` record `{actor, acting_as, action, target, outcome,
session_id, trace_id, ts, detail}` — on refusal as well as success, because a refused privileged
action is itself worth knowing about. `andara-cli account …`, `andara-cli invite …`, and
`andara-cli registration set …` wrap them one-to-one; `andara-cli auth login|refresh|logout|whoami`
wraps `Auth`.

**The first operator.** Every Admin RPC requires `operator`, so the story as groomed had no way to
create the first one. `auth.bootstrap_operator` (`username:password`) creates it at boot while the
index holds no operator, and is ignored once one exists — so it can stay configured without being a
back door. The chart injects it from a Secret (`secrets.bootstrapOperator`); `make up` sets
`operator:andara-local`. *(Added 2026-09-14 during implementation.)*

**The credential file.** AW-CLI-001 left its contents to this story: one entry per server address
with `account_id`, `username`, `session_token`, `session_expires`, `refresh_token`, `refresh_expires`,
written `0600`, never printed. `andara-cli auth login` writes it; the Admin wrappers send the
session token as bearer.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `auth.session_ttl` | `ANDARA_AUTH_SESSION_TTL` | `1h` | must exceed `session.linkdead_max` |
| `auth.refresh_ttl` | `ANDARA_AUTH_REFRESH_TTL` | `720h` | 30 d |
| `auth.token_key_file` | `ANDARA_AUTH_TOKEN_KEY_FILE` | — | required; 0400 |
| `auth.argon2.memory_kib` | `ANDARA_AUTH_ARGON2_MEMORY_KIB` | `65536` | 64 MiB |
| `auth.argon2.time` | `ANDARA_AUTH_ARGON2_TIME` | `3` | |
| `auth.argon2.threads` | `ANDARA_AUTH_ARGON2_THREADS` | `4` | |
| `auth.rate_limit` | `ANDARA_AUTH_RATE_LIMIT` | `10/m` | per username and per peer address; no lockout — a lockout is a denial-of-service lever |
| `auth.invite_ttl` | `ANDARA_AUTH_INVITE_TTL` | `168h` | 7 d |
| `auth.recheck_interval` | `ANDARA_AUTH_RECHECK_INTERVAL` | `30s` | bound on AC-12; every open Session re-reads status and roles |
| `auth.k8s_issuer` / `auth.k8s_jwks_url` | `ANDARA_AUTH_K8S_ISSUER` / `…_JWKS_URL` | — | `WORKLOAD_JWT` verification; unset disables the kind |
| `auth.store` | `ANDARA_AUTH_STORE` | `kafka` | `kafka` or `memory`; `memory` loses every Account on restart and warns at boot. *(Added 2026-09-14.)* |
| `auth.bootstrap_operator` | `ANDARA_AUTH_BOOTSTRAP_OPERATOR` | — | `username:password`, applied only while no operator exists. *(Added 2026-09-14.)* |
| `kafka.brokers` | `ANDARA_KAFKA_BROKERS` | — | required with `auth.store=kafka`; the first story to read it. *(Added 2026-09-14.)* |
| `session.linkdead_max` | `ANDARA_LINKDEAD_MAX` | `300s` | read here for the assertion below; AW-SRV-015 owns its meaning. *(Added 2026-09-14.)* |

Startup asserts `auth.session_ttl > session.linkdead_max` and refuses to boot otherwise.

### Error taxonomy

| Condition | gRPC code |
|-----------|-----------|
| bad username/password, bad or expired token | `UNAUTHENTICATED` |
| registration closed | `FAILED_PRECONDITION` |
| invite invalid/expired/revoked, role missing, act-as without privilege, agent out of scope | `PERMISSION_DENIED` |
| username taken | `ALREADY_EXISTS` |
| rate limited | `RESOURCE_EXHAUSTED` |
| `record_version` conflict on an Admin write | `ABORTED` |
| Admin write naming an unknown Account or Invite | `NOT_FOUND` |
| malformed request (username, password policy, count, mode) | `INVALID_ARGUMENT` |

## Data / state impact

`andara.accounts.v1` is compacted, keyed by `account_id`, with one extra key `config/registration`.
The server is the single writer (ADR-0001), holds an in-memory index rebuilt from the topic at boot,
and serializes writes behind one lock; AC-4's atomicity is that lock plus `acks=all` before the
response. Invite redemption writes the issuer's record (code burned) before the new Account; a crash
between the two burns a code without an Account, which an Operator re-issues — stated rather than
hidden. Projected to Postgres by `AW-SRV-018`; the topic stays authoritative.

Migration: none. Rollback: the record is additive; an older binary ignores unknown fields.

*Corrected 2026-09-14 during implementation:* `deploy/kafka/schemas.yaml` had `andara.audit.v1`
pending on AW-SRV-013, but this story writes the first audit record (AC-3, AC-6, AC-9) and so defines
`andara.audit.v1.AuditRecord`; AW-SRV-013 extends it additively (numbers 20+ are left for it).
`make up` now starts the server only after `topics-apply`, because the server replays
`andara.accounts.v1` at boot and refuses a topic it would have had to create.

## Observability requirements

### Metrics
- `andara_auth_attempts_total` — counter, label `outcome` (`ok`, `bad_credential`, `rate_limited`,
  `disabled`).
- `andara_auth_verify_duration_seconds` — histogram, the Argon2id cost as seen in production.
- `andara_registrations_total` — counter, label `mode`.
- `andara_invite_redemptions_total` — counter, label `outcome` (`ok`, `invalid`, `race_lost`).
- `andara_privileged_actions_total` — counter, label `action`. Bounded by the Admin RPC list.
- `andara_accounts_total` — gauge, label `role`.
Account ID, username, token, and hash are rejected as labels.

### Logs
- `info` per authentication with `outcome`, `account_id` on success only, never the username on
  failure (it may be a password typed in the wrong box).
- `info` per privileged action with `actor_account_id`, `acting_as_account_id`, `action`, `target`.
- Required fields: `ts`, `level`, `msg`, `service`, `env`, `session_id`, `trace_id`.

### Traces
- `session.authenticate` child of `session.lifetime`; `auth.verify_credential` child of it with
  `argon2.memory_kib` as an attribute. No token, hash, or username attributes.

### Alerts
None here. An auth-failure-rate alert needs a normal rate to compare against; it lands with the Session
availability SLO in `EPIC-07`.

## Test plan

- **Unit:** token sign/verify with key rotation; refresh hash and revoke; Argon2id rehash-on-login
  (AC-13); disable-then-present (AC-12); `Authorize` table for every role × verb row; agent scope check (AC-11).
- **Integration:** the telemetry-scanning test (AC-1); timing test (AC-2); 50-way concurrent
  redemption against a throwaway Redpanda (AC-4); audit-completeness test asserting every Admin RPC
  yields exactly one audit record; snapshot byte-search (AC-7); restart-then-verify (AC-8).
- **Manual/operator:**
  ```
  andara-cli auth login --username operator                        # the bootstrap operator `make up` configured
  andara-cli account create --username brian --role operator      # expect: prompt for password, audit line in logs
  andara-cli invite issue --count 3                                # expect: three codes, 7 d expiry
  andara-cli auth login --username brian                           # expect: token stored 0600 by AW-CLI-001
  andara-cli play --as <account>                                   # expect: audit records name both identities (AW-CLI-004)
  ```

### Verification record (2026-09-14; §8 clean, moved to `done` 2026-09-18)

Every AC passes in-process except where a dependency does not exist yet, and those are stated
rather than claimed:

- **AC-1, 2, 3, 4, 5, 8, 9, 10, 12, 13** — `server/auth` tests (`TestNoSecretLeaks`,
  `TestAuthenticate_ConstantCostOnFailure`, `TestRegister_*`, `TestVerify_SurvivesRestart`,
  `TestRefreshAndRevoke`, `TestActAs`, `TestDisableAndRecheck`, `TestAuthenticate_RehashOnLogin`);
  AC-10 and AC-12 at the Gateway in `server/gateway/auth_test.go`.
- **AC-6** — `Authorizer.Authorize` returns `ErrNotAuthorized` and writes the audit record naming
  actor, verb, and Session (`TestAuthorize`). "Nothing is produced to `andara.commands.v1`" is
  vacuous until AW-SRV-010 has a producer; that story wires `Authorize` into `Submit` and asserts it.
- **AC-7** — `TestSnapshotBytesCarryNoAccountState` plants a username, password, and tokens and
  searches `sim.CanonicalBytes(world)`. Zone Snapshots proper are AW-SRV-006; the assertion is on
  the bytes a Snapshot is built from.
- **AC-11** — `AuthorizeBind` rejects and audits an agent reaching outside its pack. Entity
  binding is AW-SRV-014 and Templates AW-SRV-022; they call it.
- **AC-12** — read as: a `DISABLED` Account's still-valid token is `UNAUTHENTICATED` at
  `OpenSession`; a role change does not refuse `OpenSession` (the new Session simply gets the new
  roles, read from the index) but does close every open Session within the interval, because those
  Sessions hold stale roles. Refusing a login because a role was *removed* would lock a demoted
  builder out of the game rather than out of building. *(Reading recorded 2026-09-14.)*
  `SubscriberDropped{reason=REVOKED}` — the Session closes with outcome `revoked`; the Event on the
  stream is AW-SRV-011's, which owns delivering Events.
- **Against the running stack:** index replay after a restart, Prometheus scraping the auth
  metrics, Tempo holding `session.authenticate` and `auth.verify_credential`, the audit record read
  back from the broker (`make stack-smoke`), the record log on a throwaway compacted topic
  (`make test-integration`), and the manual/operator sequence with the built `andara-cli`.

## Definition of done

CLAUDE.md §8, plus: a security review of the auth path recorded in the PR; an explicit test that Account
state and World state are separately stored (AC-7); the key-rotation procedure in the server README.

## Open questions

- **Resolved 2026-09-07 (Brian): `andara-cli` stores credentials, and acting as another identity
  always records who was really acting.** AC-10 is that decision. `AW-CLI-001` owns where the
  credential file lives; this story owns what a credential is.
- **Resolved 2026-09-11 (Brian): Builders write Behaviors.** Agent credentials are therefore scoped
  per Content Pack (`agent_pack_id`), not merely per deployment: one Agent deployment per pack, one
  `agent` Account per deployment, and a Builder's code can drive only that pack's NPCs. ADR-0006's
  "per deployment" wording stands; the deployment unit became the pack.
- **Resolved 2026-09-18 (review pass):** token lifetimes 1 h / 30 d, Argon2id 64 MiB / t=3 / p=4
  (~100 ms on the kind box), and the username and password rules (3–32 of `a-z 0-9 _ -`,
  case-insensitive; passwords ≥ 8). Each is one config key or one constant, AC-13 makes the Argon2
  parameters safe to re-tune, and none is in the wire contract.
- **Resolved 2026-09-18 (review pass):** stateless signed session tokens rather than a server-side
  session table — AC-8 requires tokens to survive a restart and a table would put tokens in a topic.
- **Resolved 2026-09-18 (Brian): both halves confirmed.** Acting as another Account gives the
  Session the *target's* roles ("see what the player sees"); roles are a set, not a ladder — an
  operator who needs to build is granted `builder`. Pinned by `TestActAs` and `TestAuthorize`.
- **For AW-INF-006 — missed, then found (2026-09-18):** the per-peer half of `auth.rate_limit` keys
  on the direct TCP peer, so behind an ingress every player shares one bucket. `AW-INF-006` closed
  without picking this up; the review pass also found the bucket keyed on `ip:port`, so each new
  connection was its own bucket even without a proxy. `AW-SRV-025` (the Gateway's trusted-proxy
  client address) and `AW-INF-012` (the chart's CIDRs, verified on kind) close both.
- **For AW-SRV-013:** that story reads `Account.builder_packs`, which this record does not carry.
  Additive, and AW-SRV-013's to add.
