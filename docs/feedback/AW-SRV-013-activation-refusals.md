# AW-SRV-013: activation refusals no story carries

Story: `AW-SRV-013` (the content publish path), which is `ready`.
Raised: 2026-09-27, PM, at the SPRINT-02 close-out. It comes from the "work this story found that no
story carries" list in `docs/feedback/AW-SRV-012-content-resolution-and-reload.md`.

`AW-SRV-013` is in SPRINT-03's contract review for Brian's Operator self-approval
(`docs/feedback/AW-SRV-013-operator-self-approval.md`). PM doesn't amend a `ready` story, so these
items go to architecture for the same review.

## For architecture

### 1. `ActivateVersion` should refuse what the Loader would refuse later

`AW-SRV-012` refuses these moves at swap time, as a ticket. They depend on what's in effect, so
publish can't catch them. Activation can, and should refuse them with a Builder-facing reason before
the pointer moves:
- a version that removes a Zone (`zone_removed`);
- a version that drops the spawn Room (`spawn_room_removed`). After `AW-INF-024` the spawn Room is
  `purgatory/start`, in `dev`'s fixture pack, so a Builder's rollback of that pack could hit this;
- a core rollback that strands a pack built against a newer core (`AW-SRV-012` §9b).

The reason codes are `AW-SRV-012`'s. `AW-CLI-003`'s `activate` and `rollback` then print them.

### 2. `server info` belongs to `AW-CLI-003`

`AW-SRV-012`'s manual test uses `andara-cli server info` over `GetServerInfo.content`, and SPRINT-03's
demo reads it at every step. Please confirm `AW-CLI-003` carries it.

## Brian's decision for later (2026-09-27)

**When a Zone is deleted, the Characters in it go back to Purgatory.** Deletion stays refused for
now, so this changes nothing in `AW-SRV-013`. It's the evacuation policy the later deletion story
needs, and it answers SPRINT-02's game-design question 3.

## Architecture's contract review (2026-09-28)

1. **Adopted.** `AW-SRV-013` AC-14 refuses `zone_removed`, `spawn_room_removed`, and `core_version`
   before the pointer moves: `FAILED_PRECONDITION`, with an `ActivationRefusal` detail naming the
   subjects (`admin.proto`). `override` doesn't bypass them. `AW-CLI-003` prints the reason and the
   subjects.
2. **Confirmed, and added.** `AW-CLI-003` didn't carry `server info`, and no command in `admin/cli/`
   implements it. It's now in `AW-CLI-003`'s scope as `andara-cli server info`, over
   `GetServerInfo`, printing `content` one pack per line.
