---
id: ADR-0006
title: Identity, accounts, characters, and session lifecycle
status: accepted
date: 2026-09-07
deciders: [brian]
gates: []
---

## Context

Before a Character can persist, there must be an Account to own it and a Session lifecycle defining
what happens when a connection drops mid-combat. This is a Phase 1 exit dependency and the one
decision with a real security surface.

Brian has decided: **closed at launch, then invite-only beta, then open registration. Up to five
Characters per Account. A linkdead grace period that times out, at which point the Character is
removed from the World.**

## Decision

### Registration model — staged, one mechanism

Three phases, one code path with a mode switch. Building three registration systems would be worse
than building one with a gate.

| Phase | `auth.registration_mode` | Behavior |
|-------|--------------------------|----------|
| Closed | `closed` | No self-registration. Accounts created by an Operator via `andara-cli`. |
| Invite beta | `invite` | Registration requires a valid, unredeemed, single-use invite code. |
| Open | `open` | Self-registration. |

Invite codes are Account-scoped, single-use, expiring, and revocable, with issuance and redemption
both audited. The mode is server configuration, changeable without a deploy, and every change is an
audit Event.

### Auth model — self-hosted credentials, tokens for sessions

Username plus password, hashed with **Argon2id**, tuned to a stated cost and re-tuned as an explicit
change. Authentication yields a short-lived session token and a longer-lived refresh token; the
Session's gRPC stream carries the session token.

`[ASSUMPTION]` Self-hosted rather than OIDC. A closed launch does not benefit from federation, and
adding an identity-provider dependency before there are players is cost without return. OIDC can be
added as a second credential type against the same Account record; nothing here forecloses it.

**No email dependency in Phase 1.** No password reset by email, no verification. In `closed` and
`invite` modes an Operator resets a password through `andara-cli`, audited. This is a real limitation
at the `open` transition and needs its own story before that switch is flipped — flagged rather than
half-built now.

### Characters — five owned, one live

An Account owns **up to five** Characters. `[ASSUMPTION]` **One is live at a time.** Brian specified
the cap, not the concurrency; one-live is MUD convention and is the conservative reading — it avoids
self-trade, self-buffing, and one player occupying a room five times over. Lifting it later is a
config change plus a design conversation. Assuming the permissive version and retracting it later is
not.

Character names are globally unique, immutable after creation, and reserved on deletion. Deletion is
soft: the Character leaves the roster, the name stays reserved, World state is retained for a stated
window.

### Session lifecycle

```
                    authenticate        select character
   [connected] ──────────────────▶ [authenticated] ──────────────▶ [playing]
                                                                      │
                                                        stream drops  │
                                                                      ▼
                                                                 [linkdead]
                                          ┌───────────────────────────┴───────────┐
                          reconnect within grace                    grace expires
                                          ▼                                       ▼
                                     [playing]                              [despawned]
```

- **Linkdead grace period.** On stream loss the Character remains in the World, marked linkdead, for
  `session.linkdead_grace`. Reconnecting within it rebinds the Session to the same Character and
  resumes the Event stream.
- **On expiry the Character is removed from the World** — despawned. Its state is persisted; it is not
  deleted. "Removed" means it leaves the Room and stops being a target. A `CharacterDespawned` Event
  is emitted so everyone present sees it happen.
- **Default grace: 180 seconds.** Long enough to survive a dropped connection, short enough that a body
  is not a trade dummy for ten minutes. Configurable.

  This number is load-bearing beyond player experience: **the grace period must exceed the RTO**, or a
  routine server restart despawns every connected player. At the recommended RTO of 60 s p99
  (`docs/specs/slo/recovery.md`), 180 s gives 3× margin. Lowering either number without checking the
  other is how "we tightened the grace period" becomes "every deploy kicks everyone out."
- It must also exceed `egress.resume_window` in effect (`AW-SRV-011`), or every linkdead reconnect
  resyncs instead of resuming.
### Combat and the linkdead grace period

Decided by Brian on 2026-09-07: **combat extends the timer, but the Character can still time out.** A
linkdead Character in combat stays in the World and remains attackable — it takes damage — but it does
eventually despawn. Dropping mid-fight means "you get hurt, and you might not die."

Three parameters, doing three different jobs:

| Key | Default | Meaning |
|-----|--------:|---------|
| `session.linkdead_grace` | 180 s | base grace, out of combat |
| `session.linkdead_combat_extension` | 60 s | each combat interaction refreshes remaining grace to at least this |
| `session.linkdead_max` | 300 s | **hard ceiling** on total linkdead duration, combat or not |

The extension **refreshes rather than accumulates**: remaining grace becomes `max(remaining, extension)`
on each combat interaction. A Character under sustained attack therefore keeps a rolling 60-second window
and despawns 60 seconds after the last blow, or at `linkdead_max`, whichever comes first.

- **Extension** stops the disconnect being an instant escape. An attacker gets a real window.
- **Ceiling** stops a linkdead body being a punching bag indefinitely. This is the "maybe not die" half.
- **Base grace** is unchanged and still governs the ordinary case, a dropped connection out of combat.

**The tension to know about when tuning:** `linkdead_max` is the combat-logging knob. Too low and pulling
the network cable becomes a viable escape from a losing fight; too high and a disconnected player is a
guaranteed corpse, which is the experience the grace period exists to prevent. 300 s makes disconnecting
a bad bet without making it a death sentence, and it should be retuned once combat pacing exists to
measure it against.

The deadline is stored as a Tick and extended by a Command applied inside the tick, so the mechanic is
deterministic under replay (ADR-0002). A wall-clock deadline here would make a recovered World diverge
from the one players were in.

`[NEEDS BRIAN]` — whether a linkdead Character defends itself, flees, or is wholly inert. The decision
above makes the timeout the survivability mechanism, which implies inert; if a Character should fight
back, that is a Behavior and it rebalances all three numbers.

### Authorization and audit

Roles are Account attributes: `player`, `builder`, `game_master`, `operator`. Every privileged action
emits to `andara.audit.v1` with actor Account, role, action, target, and Session correlation ID.
Privilege is checked in the `authorize` stage, before the Command reaches the log — an unauthorized
Command should not consume a log offset.

**Behavior Agents (ADR-0005) authenticate as Accounts too**, with an `agent` role, credentials issued
per Agent deployment rather than per NPC. This keeps the "no back door" rule intact — an NPC's
Commands go through exactly the path a player's do.

### Account state is not World state

`andara.accounts.v1` is a compacted topic, separate from the World's Command log, projected into
Postgres. This separation is non-negotiable for two reasons: a World rollback must not roll back
credentials, and a World snapshot shared for debugging must not carry them.

## Consequences

- Credential material never appears in a log line, metric label, trace attribute, or error message.
  Asserted by test, not by review.
- Failed authentication does not distinguish unknown Account from wrong credential.
- The `open` transition is gated on account recovery existing, which is deliberately not in Phase 1.
  That is a known cliff, and it has a story before the switch is flipped.
- Five Characters per Account with one live means the roster is Account state and the live Character is
  Session state. Losing a Session must not lose either.
- `linkdead_max >= linkdead_grace > RTO > 0` is a standing invariant across ADR-0006 and the recovery
  SLO. A startup check asserts it rather than leaving it to whoever next edits a values file.
- Combat now reaches into Session lifecycle, so the combat system and `AW-SRV-015` share an interface:
  combat emits an interaction Event that refreshes the deadline. Whichever epic defines combat inherits
  that contract rather than inventing its own.
- **We are foreclosing** email-based flows, federated identity, and multi-live characters in Phase 1 —
  each recoverable, none free.

## Revisit when

- The `invite` → `open` transition approaches: account recovery and abuse controls both need to exist
  first.
- Sustained credential-stuffing appears, which makes a second factor or an IdP cheaper than the abuse.
- One-live-Character blocks a design goal.
- RTO rises to within 3× of the linkdead grace period, at which point one of the two numbers must move.
- Players start disconnecting to escape fights — `linkdead_max` is too short.
- Disconnected players routinely die — it is too long.
