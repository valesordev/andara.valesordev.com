# SPRINT-03 — Builder content on dev
Status: active
Dates: 2026-09-27 →

## Demo goal
**Builder content on dev: the M3 gate, on the box.** Brian starts with no clone of the code
repository, only the Content Repository and the Builder's Guide. From there, he:
1. writes a Zone in his own pack;
2. has `check` pass on its pull request;
3. publishes it to `dev` with `andara-cli`;
4. approves it with his Operator identity (self-approval, while he's the only Builder), and
   activates it;
5. spawns in Purgatory, and uses `goto` to reach the Zone with `andara-cli play`;
6. changes it and publishes again;
7. rolls it back.

`dev` does this with no deploy. `server info` names the content in effect at each step.

This is `docs/roadmap.md`'s **M3 gate**, "a Builder with no repository access publishes a Zone,
sees it live, and rolls it back". It's also Phase 1 exit criterion 4. Argo CD already runs `dev`
from `main` (AW-INF-019).

**Not in this sprint: the server-kill half of M2.** AW-SRV-026, AW-SRV-028 and AW-SRV-007 move to
SPRINT-04 (Brian, 2026-09-27). Content on `dev` comes first, and the sprint can't hold both.

## Contract review (architecture, first)
- AW-INF-026 — generated views stop being committed (new; Brian chose option (a), 2026-09-27)
- AW-INF-025 — operating the state projector: stop, rebuild, start, and its volumes (new; #80). Its
  open question, how an in-cluster projector reads snapshot rounds, is decided here.
- AW-SRV-037 — Purgatory, the spawn Zone, in the test content (new). Confirm whether its merge
  supplies AW-INF-019's AC-4, since it also edits tests. If it doesn't, name the fixture-only change
  that does.
- AW-SRV-035 — an Operator grants a Builder their packs (new, implementation)
- AW-INF-020 — Builders download `andara-cli` without a Go toolchain (new)
- AW-INF-024 — every environment spawns new Characters in Purgatory (new)
- AW-INF-021 — `dev` serves its content from the content store (new)
- AW-INF-022 — the Content Repository (new)
- AW-INF-023 — the Builder's Guide (new)
- AW-SRV-036 — `goto`: a Builder jumps to any Room (new, implementation)
- AW-SRV-038 — a move describes the destination Room to the mover (new, implementation; Brian
  2026-09-27)
- AW-SRV-013 and AW-CLI-003 (`ready`):
  - Brian's Operator self-approval, in `docs/feedback/AW-SRV-013-operator-self-approval.md`.
    Record it in both story bodies, plus a dated note in ADR-0004.
  - The activation refusals and `server info`, in `docs/feedback/AW-SRV-013-activation-refusals.md`.
- For AW-INF-020 to AW-INF-023: answer items 1–5 in `docs/feedback/AW-INF-021-dev-content-store.md`
  first.
  1. How core reaches `dev`'s store on every roll.
  2. Moving `dev`'s log off `dir`.
  3. Where the fixture's source lives.
  4. Where the Content Repository's CI gets core.
  5. Where the guide lives.

## Architecture backlog (pickup order)
Carryover and process first, then the §9 defect, then the Builder content track.

1. AW-INF-026 — generated views stop being committed — depends on AW-INF-001 (`done`). It's first
   because every later PR pays the rebase cost until it lands.
2. AW-INF-019 (carryover) — AC-4 — needs implementation item 1's merge, or the fixture-only change
   the contract review names.
3. AW-INF-025 — projector targets and volumes (#80) — depends on AW-SRV-019 (carryover, `review`)
   and AW-INF-018 (`done`)
4. AW-INF-008 (carryover) — AC-2, the state projector on `dev` — after item 3
5. AW-INF-020 — `andara-cli` release binaries — depends on AW-INF-013 (`done`)
6. AW-INF-024 — spawn in Purgatory everywhere — depends on AW-SRV-037 (implementation item 1)
7. AW-INF-021 — `dev` serves content from the store — depends on AW-SRV-013, AW-SRV-035 and
   AW-CLI-003 (implementation items 4, 7 and 8) and AW-INF-019 (item 2)
8. AW-INF-022 — the Content Repository — depends on AW-CLI-002, AW-INF-020 and AW-INF-021
9. AW-INF-023 — the Builder's Guide — depends on all of the above, and AW-SRV-036. It's last so it
   describes what was built. Its AC-3 walk-through is the demo.

## Implementation backlog (pickup order)
AW-SRV-035 to AW-SRV-038 are new; the rest are `ready` or carried over.

1. AW-SRV-037 — Purgatory in the test content — depends on AW-SRV-014 (`done`). It's first because
   AW-INF-024 waits on it, and its merge may supply AW-INF-019's AC-4.
2. AW-SRV-019 (carryover) — the AC-5 test, restored against the lowered floor
   (`docs/feedback/AW-SRV-019-state-projector.md`)
3. #128 — snapshot rounds stop completing after a broker disruption until the server restarts
   (AW-SRV-006 code). `dev` is affected today.
4. AW-SRV-013 — the content publish path — depends on AW-SRV-008, AW-SRV-012 (both `done`)
5. AW-SRV-034 — the loader and compiler agree on `orphan_room` and `duplicate_direction` — depends
   on AW-CLI-006 (`done`)
6. AW-CLI-002 — `andara-cli content validate` and `inspect` — depends on AW-SRV-034
7. AW-SRV-035 — `account set-packs` — depends on AW-SRV-013
8. AW-CLI-003 — `andara-cli content publish`, `approve`, `activate`, `rollback`, `history`, `diff`,
   and `fetch` — depends on AW-CLI-002, AW-SRV-013
9. AW-SRV-036 — `goto` — depends on AW-SRV-003, AW-SRV-014 (both `done`)
10. AW-SRV-038 — a move describes the destination Room — depends on AW-SRV-003 (`done`). Last,
    because the demo doesn't need it.

**Risk:** implementation has ten items, and AW-SRV-013 and AW-CLI-003 are the size of the sprint.
If it runs short, AW-SRV-038 carries first, then #128. The demo needs items 1 and 4–9.

## Carryover from SPRINT-02
Three stories, all at `review`. The reasons are in SPRINT-02's close-out:
- AW-INF-019 — AC-4 (architecture item 2)
- AW-SRV-019 — the AC-5 test (implementation item 2); its production line needs AW-INF-025
- AW-INF-008 — AC-2 (architecture item 4), after AW-INF-025

## Close-out
(filled in by the next PM session)
