# AW-SRV-015 — linkdead: contract questions before implementation

Spec: `docs/stories/AW-SRV-015-session-lifecycle-and-linkdead-grace.md` (`ready`)
Raised: 2026-09-25, PM, grooming SPRINT-02, where this story is the demo slice.

The story is `ready`, so PM doesn't amend it (lane rule). These three items change its interface
contract, and SPRINT-02 lists them under "Contract review (architecture, first)". Implementation
shouldn't start the story until they're answered here and in the story body.

## For architecture

### 1. `MarkLinkdead mark_linkdead = 15` collides

The sketch adds `MarkLinkdead mark_linkdead = 15` to `LoggedCommand`'s oneof. On `main`,
`docs/specs/protocol/andara/log/v1/log.proto` has `bind_character = 15`, `unbind_character = 16`
and `content_swap = 17`, and 13/14 are held for `AW-SRV-028`'s handoff acks. This is the same
collision `AW-SRV-012`'s sketch had with `content_swap = 16` (its feedback §3). The next free number
is 18.

`EntityState.linkdead_since_tick = 7` agrees with `zone_state.proto`'s comment, which leaves 7 for
it. The three new Events have no numbers in the sketch. `event.proto`'s payload oneof runs to
`entity_relocated = 19`.

### 2. AC-9 (and possibly AC-6) needs `AW-SRV-007`, which `depends_on` doesn't list

- **AC-9:** "a full restart completing within RTO … Exercised in CI on top of `AW-SRV-007`'s test".
  `AW-SRV-007` is `ready` and not started, and its dependencies `AW-SRV-006` (at `review`, AC-3/AC-8
  failing), `AW-SRV-026` and `AW-SRV-028` aren't done either. As written, this story can't reach
  `done` before `AW-SRV-007` does, and `depends_on: [AW-SRV-014]` hides that.
- **AC-6** (restart mid-grace, deadline exactly as snapshot and log say): a full-log replay can
  satisfy it without `AW-SRV-007`, since recovery works from M1. The story should say which.

**Recommendation:** move AC-9 to `AW-SRV-007` as an inherited Definition-of-done line, the way §8
already handles instruments with no caller yet, and state AC-6 against full-log replay. Then this
story can close in SPRINT-02, and `AW-SRV-007` carries the restart-within-RTO proof in SPRINT-03,
where it belongs. The alternative is adding `AW-SRV-007` to `depends_on`, which moves the whole
story, and SPRINT-02's demo, out a sprint.

### 3. The three Events carry no name a bystander's client can render

The sketch has `CharacterLinkdead { character_id, deadline_tick }`,
`CharacterReconnected { character_id }` and `CharacterDespawned { character_id, reason }`.
`CharacterArrived` and `CharacterLeft`, the Room-scoped Events a bystander already reads, carry
`zone_id`, `room_id` and `character_name`, and no `character_id`. With only an id, `andara-cli play`
can't write "Aldric goes linkdead." (`AW-CLI-008`, new in SPRINT-02). And an id sent to every
bystander is an identifier the existing Room-scoped Events deliberately don't expose.

**Recommendation:** give all three `zone_id`, `room_id` and `character_name`, matching
`CharacterArrived`. Keep `character_id` and `deadline_tick` only if a consumer needs them;
`AW-CLI-008` doesn't.

## Architecture's answers (2026-09-26, contract review)

Recorded in the story body, under "Contract review". The story stays `ready`, and implementation can
start it.

1. **Numbers:** `mark_linkdead = 18`. The Events are `character_linkdead = 20`,
   `character_reconnected = 21`, `character_despawned = 22`. `EntityState` takes 7
   (`linkdead_since_tick`) and two more, 11 and 12, because of the durations (below). The protos and
   `gen/` are on `main` with this review.
2. **AC-9 moved to `AW-SRV-007`** as an inherited DoD line, as recommended. AC-6 is stated against
   full-log replay. `depends_on` is unchanged.
3. **Names, not ids:** all three Events carry `zone_id`, `room_id`, `character_name`, and nothing
   else except `CharacterDespawned.reason`, a string. No `character_id`, no `deadline_tick`: combat
   moves the deadline without an Event, so a carried one goes stale.

Beyond the three, the review changed:
- `MarkLinkdead` carries `grace_ticks`, `extension_ticks`, `max_ticks`. The sim doesn't read
  them from config at apply, or a replay under retuned config would fail its State Hash.
- `ErrNotLinkdead` is struck. It would have broken `AW-SRV-014`'s crash path.
- A drain produces `MarkLinkdead`, and a revoke produces `UnbindCharacter{QUIT}` (AC-15).
- Selecting another Character while one is linkdead is `already_live` (AC-16).
- `(linkdead)` comes from `RoomDescribed.linkdead`, not from a suffix in `occupants`. That touches
  `AW-CLI-008`, amended in the same review.

## For PM

`AW-SRV-007` gained an inherited Definition-of-done line from this review: 015's old AC-9, plus two
things the move exposed. A linkdead body must recover from a *snapshot* with its linkdead fields
intact. And a body a crash leaves present with no Session must be marked linkdead at recovery. Today
it stays present forever (`AW-SRV-014`: "stays present until the next BindCharacter takes it"). That
last one is new behavior. Size `AW-SRV-007` with it when SPRINT-03 is planned, or split it out.


## Implementation, 2026-09-26: started

### For architecture: the `state_version` bump would make every existing log unreplayable

The story's Data / state impact says "`state_version` bumps by one with a zero-fill migration". The
snapshot side of that works: `store.migrations[1]` is a zero-fill, and `hashers[1]` is today's
`BodyStateHash`. The log side does not:
- `WorldState.CanonicalBytes` writes `state_version` into the State Hash of every tick.
- `Engine.ReplayEach` refuses any boundary whose `state_version` isn't the binary's
  (`ErrStateVersion`, the projector's exit 4).

So a binary at version 2 can't replay a single tick of a log written at version 1. The server
recovers by full-log replay at boot (M1, and this story's AC-6), so any World with history on its
log, including the local stack and `dev`, would refuse to start after the upgrade. The projector
would exit 4 on every existing log as well. Nothing in the repo replays across a version change.

What I'm building meanwhile, reversibly:
- **No bump.** `EntityCanonicalBytes` writes a `linkdead` record only when `linkdead_since_tick`
  is non-zero, the way `entity_dormant` is written only for a dormant body. Every existing hash,
  boundary and snapshot keeps its value.
- **All four fields hashed, and snapshotted.** `BodyStateHash` drops their refusal, and the
  tripwire holds.
- ADR-0007 rule 2 and `store/migrate.go` both say `state_version` moves when the *meaning* of
  state changes, not for a field protobuf absorbs. A zero-valued linkdead field means "not
  linkdead" both before and after this story, so I read this as additive.

If you want the bump anyway, it's `StateVersion = 2` plus the two map entries. But it needs a
decision on cross-version replay first: either replay accepts a boundary one version back, or an
upgrade requires a fresh snapshot round and a log cut. That decision is yours.

## Architecture, 2026-09-26: a contract detail from `AW-SRV-006`'s second §8 pass

Recorded in the story's Interface contract, beside the `EntityState` fields. #102 changed how
this story's four fields must be hashed:
- `sim.BodyStateHash` now **refuses** a snapshot body that carries any of the four non-zero. The
  tripwire `TestBodyHashCoversEveryProtoField` fails for any proto field the hash neither covers
  nor refuses.
- This story hashes them in `EntityCanonicalBytes` as a linkdead record, written **only when
  `linkdead_deadline_tick` is non-zero**, as the dormant record is. Existing hashes and the golden
  sequence don't move.
- Drop the four from the refusal. Keep refusing the inconsistent case: deadline zero with any
  other field non-zero.

## For implementation: §8, 2026-09-26 — one item holds the story at `review`

Everything else in §8 holds (the record is in the story). `state_version`: **no bump**, as you
built it; the story's Data / state impact is amended.

**The despawn line at the deadline or ceiling has empty `session_id` and `trace_id`.**
`expireLinkdead` → `despawn` (`server/sim/linkdead.go`) sets neither, so the `info` line the
Observability section requires for a despawn carries `session_id=""` and `trace_id=""`. The
contract, clarified:
- `session_id` is the Session that went linkdead. The roster's hold already keeps it.
- **After a restart** (a body recovered linkdead, `Roster.SeedLinkdead`), there's no Session: none
  survives a restart, and nothing in Zone state names one. That expiry line omits `session_id`
  and carries `recovered=true`; `character_id` and `deadline_tick` are its correlation.
  *(Added in review of #120.)*
- `trace_id` is the trace of the tick span that applied the expiry. No request is in flight at
  expiry, and the tick is the traced unit (charter §7).
- Assert the expiry line in both cases, held and recovered, so neither regresses silently.

Not blocking: `Entity.Linkdead()` keys on `LinkdeadSince != 0`, while the hash keys on the
deadline. They agree only because no Command applies at Tick 0. Keying both on the deadline
removes that reasoning.

### Also holding the story: #121

`TestRun_Linkdead` failed once in CI (on #119, which touches no Go) with the reconnect's resume
answered `Resync{no_history}`. It passes 20 of 20 locally. The issue has the log order and the
ask: widen the window until it fails every run, then fix the ordering between the park, the
adoption and the new Session's `Subscribe`.

## For PM: a game-design question for Brian (§8, 2026-09-26)

**Is a linkdead Character wholly inert, or does it defend itself or flee?** ADR-0006 marks it
`[NEEDS BRIAN]`. Its timeout decision implies inert, and inert is what `AW-SRV-015` built: the
body takes damage and does nothing. A "yes, inert" resolves the story's last `[ASSUMPTION]` with
no code change. Anything else is a Behavior story that rebalances `linkdead_grace`, the combat
extension and `linkdead_max`, and ADR-0006 is amended either way. Please batch it with SPRINT-02's
game-design questions.

### Brian's answer (2026-09-26): inert

**A linkdead Character is wholly inert.** It neither defends itself nor flees: it takes damage and
does nothing, which is what `AW-SRV-015` built. This resolves the story's last `[ASSUMPTION]`, with
no code change and no Behavior story. For architecture, at the next §8 pass:
- record the answer in the story's Open questions;
- add a dated note to ADR-0006 closing its `[NEEDS BRIAN]`.

## Architecture, §8 second pass (2026-09-26): closed

All three holds are closed: #123 fixed the expiry line, #124 fixed the reconnect race (#121), and
Brian answered inert (#122). The story is `done`, and ADR-0006 carries the answer. The record is
in the story.
