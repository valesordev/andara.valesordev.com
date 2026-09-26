# SPRINT-03 — Builder content on dev, and the server-kill half of M2
Status: planned
Dates: after SPRINT-02 closes →

This sprint is part-planned. The Builder content track below was groomed on 2026-09-26 at Brian's
request, ahead of the boundary. The SPRINT-02 close-out session finishes the plan:
- it adds the server-kill slice of M2 that SPRINT-02 defers here (AW-SRV-006's fixes, AW-SRV-026,
  AW-SRV-028, AW-SRV-007 and #74);
- it adds SPRINT-02's carryover;
- it checks every dependency against `origin/main` as it stands then;
- it sets `Status: active`.

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
sees it live, and rolls it back". It's also Phase 1 exit criterion 4. It needs SPRINT-02's
AW-INF-019, so that `dev` is running and following `main`.

The server-kill demo goal (M2) is added by the boundary session.

## Contract review (architecture, first)
- AW-SRV-035 — an Operator grants a Builder their packs (new, implementation)
- AW-INF-020 — Builders download `andara-cli` without a Go toolchain (new, architecture)
- AW-INF-021 — `dev` serves its content from the content store (new, architecture)
- AW-INF-022 — the Content Repository (new, architecture)
- AW-INF-023 — the Builder's Guide (new, architecture)
- AW-SRV-036 — `goto`: a Builder jumps to any Room (new, implementation; Brian 2026-09-26)
- AW-SRV-037 — Purgatory, the spawn Zone, in the test content (new, implementation; Brian
  2026-09-26)
- AW-INF-024 — every environment spawns new Characters in Purgatory (new, architecture)
- AW-SRV-013 and AW-CLI-003 (`ready`): Brian's Operator self-approval, in
  `docs/feedback/AW-SRV-013-operator-self-approval.md`. Record it in both story bodies, plus a dated
  note in ADR-0004.
- For AW-INF-020 to AW-INF-023: answer items 1–5 in `docs/feedback/AW-INF-021-dev-content-store.md` first.
  1. How core reaches `dev`'s store on every roll.
  2. Moving `dev`'s log off `dir`.
  3. Where the fixture's source lives.
  4. Where the Content Repository's CI gets core.
  5. Where the guide lives.
  - AW-INF-023's two `[NEEDS BRIAN]` items were answered on 2026-09-26: `goto` and Purgatory, and
    Operator self-approval.

## Architecture backlog (pickup order)
The Builder content track:

1. AW-INF-020 — `andara-cli` release binaries — depends on AW-INF-013 (SPRINT-02, box session)
2. AW-INF-024 — spawn in Purgatory everywhere — depends on AW-SRV-037 (implementation item 1)
3. AW-INF-021 — `dev` serves content from the store — depends on AW-SRV-013, AW-SRV-035,
   AW-CLI-003 (implementation items 3, 5 and 6) and AW-INF-019 (SPRINT-02)
4. AW-INF-022 — the Content Repository — depends on AW-CLI-002, AW-INF-020, AW-INF-021
5. AW-INF-023 — the Builder's Guide — depends on all of the above, and AW-SRV-036. It's last so it
   describes what was built. Its AC-3 walk-through is the demo.

## Implementation backlog (pickup order)
The Builder content track. AW-SRV-035, AW-SRV-036 and AW-SRV-037 are new; the rest are `ready`.

1. AW-SRV-037 — Purgatory in the test content — depends on AW-SRV-014 (SPRINT-02 §8). It's first
   because it's small and AW-INF-024 waits on it.
2. AW-SRV-034 — the loader and compiler agree on `orphan_room` and `duplicate_direction` — depends
   on AW-CLI-006 (SPRINT-02 §8)
3. AW-SRV-013 — the content publish path: server-side validation, versioning, approval, and
   audit — depends on AW-SRV-008, AW-SRV-012 (both `done`)
4. AW-CLI-002 — `andara-cli content validate` and `inspect` — depends on AW-SRV-034
5. AW-SRV-035 — `account set-packs` — depends on AW-SRV-013
6. AW-CLI-003 — `andara-cli content publish`, `approve`, `activate`, `rollback`, `history`, `diff`,
   and `fetch` — depends on AW-CLI-002, AW-SRV-013
7. AW-SRV-036 — `goto` — depends on AW-SRV-003 (`done`) and AW-SRV-014

## Carryover from SPRINT-02
(filled in by the boundary session)

## Close-out
(filled in by the next PM session)
