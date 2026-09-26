# SPRINT-02 — A dropped connection is survivable
Status: active
Dates: 2026-09-25 →

## Demo goal
An Operator runs two players in `town/plaza` on a fresh local stack and drops the first one's
connection without a quit. The second player sees `<name> goes linkdead.`, and `look` shows
`<name> (linkdead)` under `Here:`. The first player runs `andara-cli play --character <name>` again
within the 180 s grace, and is back in the same Room with a body that never left. The bystander
reads `<name> reconnects.`, and a clean quit after that reads `<name> leaves the world.`
`make stack-linkdead` asserts the whole run in the `stack` workflow.

This is the **linkdead slice of the M2 gate** in `docs/roadmap.md`: "every linkdead Character
rebinds rather than despawning". It's also the disconnect-and-reconnect clause of Phase 1 exit
criterion 1. AW-SRV-015 is the mechanism, AW-CLI-008 renders it, and AW-INF-017 makes the drop a
target instead of a hand-typed `kill`.

What it is not:
- **Not the server-kill half of M2.** `kill -9` of the server, recovery from a snapshot within
  120 s, and a matching State Hash need AW-SRV-006's fixes, AW-SRV-026, AW-SRV-028, AW-SRV-007 and
  #74. That's SPRINT-03's slice. The rebind-after-restart case (AW-SRV-015 AC-9) goes with it.
- **Not grace expiry, live.** The despawn at 180 s and the combat extension are asserted by
  AW-SRV-015's integration tests on Ticks, not by the demo.
- **Not a stream resume.** A new `play` process has no `last_event_id`. "No gap" is AW-CLI-004's
  in-process reconnect, and a dropped process doesn't get it.

## Contract review (architecture, first)
- AW-SRV-015: its three contract items in `docs/feedback/AW-SRV-015-linkdead.md`, plus §3 of
  `docs/feedback/AW-SRV-014-character-roster.md`. Record the answers in the story body. It stays
  `ready`, or goes to `blocked` if AC-9 stays tied to AW-SRV-007.
  1. `mark_linkdead = 15` collides with `bind_character`.
  2. AC-9 needs AW-SRV-007, which `depends_on` doesn't list.
  3. The Events carry no `character_name`.
  - SRV-014 §3: `already_live` right after a clean quit.
- AW-CLI-008 — `andara-cli play` renders linkdead, reconnect, and despawn (new, implementation)
- AW-INF-016 — `make up` builds the server image from the tree it runs in (new, architecture;
  SPRINT-01's §9 defect)
- AW-INF-018 — `make topics-apply` applies declared config drift to existing topics (new,
  architecture; #77)
- AW-INF-017 — `make stack-linkdead` (new, architecture)

## Architecture backlog (pickup order)
Carryover first. The §8 queue, in the order its evidence is ready:

1. AW-CLI-007 — its first §8. It closes AW-SRV-014's live lines.
2. AW-CLI-004 — moves to `done` in the same pass as CLI-007 (feedback §1).
3. AW-CLI-005 — status defect: the closing condition was met in #75. Flip it.
4. AW-SRV-012 — the owed items were delivered in #95. It moves to `done` at this §8 with no further
   review.
5. AW-SRV-014 — after implementation item 1.
6. AW-CLI-006 — after implementation item 2.
7. AW-SRV-006 — after implementation item 3.
8. AW-SRV-019 — after implementation item 4. It stays at `review` if its production line still
   waits on #80 (SPRINT-03) and AC-9's carrier.

Then the §9 defects:

9. AW-INF-016 — `make up` builds the tree (#73)
10. AW-INF-018 — `topics-apply` applies drift (#77). This comes before the box session because
    AW-INF-014's AC-3 re-confirmation and AW-INF-008's AC-2 need it.

Then the box session with Brian, which is carryover:

11. AW-INF-013, AW-INF-014, AW-INF-008 — in the run order #81 records. Brian brings
    `ANDARA_BOOTSTRAP_OPERATOR` for `dev` and a `GRAFANA_CLOUD_READ_TOKEN`.

Then the demo slice:

12. AW-INF-017 — `make stack-linkdead` — depends on AW-SRV-015, AW-CLI-007, AW-CLI-008

## Implementation backlog (pickup order)
Carryover first. These are the owed items that keep SPRINT-01's stories at `review`:

1. AW-SRV-014 — the bind-applied `info` log (`docs/feedback/AW-SRV-014-character-roster.md` §1)
2. AW-CLI-006 — the `admin/README.md` Commands table and flags (`docs/feedback/AW-CLI-006-…` §14
   item 2)
3. AW-SRV-006 — AC-3 (hash over the amended superset, with the proto-field tripwire) and AC-8 (the
   round gated on the broker's ack, with boundary/timeout abandonment), plus the stale README
   (`docs/feedback/AW-SRV-006-zone-snapshots.md` §13)
4. AW-SRV-019 — the implementation-owed items in its §8 record (#81):
   - the AC-5 forced-compaction assertion
   - AC-6 at the stated scale
   - the sticky divergence (feedback §2)
   - `andara_state_topic_bytes` reading 0
   - an AC-7 re-check after #93

   Also in this item: **#78**, removing the 35 MB `andara-projector` binary from the repo root and
   ignoring it.

Then the demo slice:

5. AW-SRV-015 — Session lifecycle and linkdead grace — depends on AW-SRV-014. Start it only after
   architecture's contract review records the three amendments in the story.
6. AW-CLI-008 — `play` renders linkdead, reconnect, and despawn — depends on AW-CLI-004 and
   AW-SRV-015. It needs the amended Event fields.

**Risk:** items 3 and 4 aren't small, and they come before the demo slice. If the sprint runs short,
the demo slips before the carryover does. PM will carry AW-SRV-015 before cutting a §8 fix.

## Carryover from SPRINT-01
Eleven stories, all at `review`. The reasons are in SPRINT-01's close-out:

- AW-CLI-007, AW-CLI-004, AW-SRV-014, AW-SRV-012, AW-CLI-006, AW-CLI-005, AW-SRV-006, AW-SRV-019,
  AW-INF-013, AW-INF-014, AW-INF-008.

SPRINT-01's §9 defect, AW-INF-016, is in the architecture backlog as item 9.

## Game-design questions for Brian (batched per §6; none blocks this sprint)
1. **Does a move describe the destination Room?** Today the mover reads `leaves north` /
   `arrives from the south` and has to `look`. A move that describes the destination is a server
   change: the sim would emit the destination's `RoomDescribed` to the mover.
2. **The linkdead lines' wording.** AW-CLI-008 uses `goes linkdead.`, `reconnects.`,
   `leaves the world.` (quit) and `fades from the world.` (grace expired) as placeholders. Changing
   them changes a golden file, not a contract.
3. **When a Builder deletes a Zone, where do the Characters in it go?** Deletion is refused today
   (AW-SRV-012), and allowing it needs your evacuation policy. This gates a later story, not this
   sprint.

## Close-out
(filled in by the next PM session)
