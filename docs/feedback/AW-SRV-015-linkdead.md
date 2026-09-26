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

