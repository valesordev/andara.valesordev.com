# SPRINT-04 — The M2 gate on the local stack
Status: active
Dates: 2026-10-02 →

## Demo goal
**`kill -9` the server; the World comes back as it was.** On a fresh local stack, an operator runs
`make stack-recover`. Two players are in the World, and one has moved away from the spawn Room. The
target waits for a complete Snapshot Round, then sends `SIGKILL` to `andara-server` and starts it
again. It asserts that:
- the server is ready within 120 s of the kill;
- recovery used that snapshot plus the log tail, and the State Hash matched
  (`andara_recovery_state_hash_match 1`), with no acknowledged Command lost;
- both players' `play` clients reconnect, and their Characters rebind where they stood. Neither
  despawns.

The `stack` workflow runs it on every merge, after `stack-linkdead`.

This is `docs/roadmap.md`'s **M2 gate**, "`kill -9` the server; the World returns within 120 s with a
matching State Hash, and every linkdead Character rebinds rather than despawning". SPRINT-02 delivered
its linkdead half (`make stack-linkdead`). It's also Phase 1 exit criterion 3, at M2's RTO of 120 s.

Why this goal: Brian moved the server-kill stories here on 2026-09-27 so that SPRINT-03 could
deliver M3. They're the nearest gate, and it's demoable once AW-SRV-026, AW-SRV-028, AW-SRV-043 and
AW-SRV-007 land, with one new SRE target.

**What it's not:**
- A kill on `dev`. A pod restart on Kubernetes is the deploy lifecycle, which belongs to AW-INF-007
  and AW-INF-011, and both wait on AW-SRV-007.
- Phase 1's 60 s RTO.
- The Redis and Postgres projections (AW-SRV-017, AW-SRV-018), even though the roadmap lists Redis
  under M2. The gate doesn't need them.
- ADR-0011's broker authentication chain (AW-SRV-044, AW-INF-030, AW-INF-031).

## Contract review (SRE observability, then architecture — first)
SRE reviews each draft's Observability requirements (§7) first. Architecture's contract review then
moves each draft to `ready` or `blocked`.

- **AW-SRV-043** — a restore verifies its round. **Review it first.** It's on the demo's critical
  path. Its Open questions 1 and 2 are architecture's (exit codes 5 and 6, and the AW-SRV-007 edge).
- **AW-INF-032** — `make stack-recover`, the M2 gate as a target (new, SRE). It's the demo. It
  depends on AW-SRV-007.
- **AW-SRV-007** (`ready`) — answer `docs/feedback/AW-SRV-007-recovery-scale.md` items 1–4:
  1. the Entity count the 120 s is measured at;
  2. `SeekAfter`;
  3. the `AW-SRV-043` edge;
  4. the operator test pointing to `make stack-recover`.

  Record the changes in the story body, since it's `ready`.
- **AW-CLI-009** — `andara-cli content reference`. With AW-INF-028, it takes back AW-INF-023's ACs
  1 and 2.
- **AW-INF-028** — `make builder-reference` and `make guide-check`. Its Open question 1 is stale:
  `docs/builders/reference.md` is now a generated file in `.claude/roles/_repo.md` and CLAUDE.md.
- **AW-INF-029** — `content/core/VERSIONS` is append-only, enforced by `make check`.
- **AW-SRV-046** — the owed follow-ups from SPRINT-03's reviews (new). It needs no decision, and
  each item cites the ruling it comes from.

## Architecture backlog (pickup order)
After the contract review above:

1. **#172**: decide how the snapshot stall-budget test measures, either run alone or on CPU time,
   with SRE's view on CI. It now fails 4 of 5 local `make check` runs, which taxes every lane.
   Implementation's item 6 waits on this.
2. **#312**: rule on how `duplicate_zone` names the two packs, and how findings are placed on the
   publisher's own source. "Root finding only" already holds (`errors.md` §1 rule 7). Implementation's
   item 8 waits on this.
3. **`docs/feedback/AW-INF-005-007-split.md`**: name the split lines. PM grooms the parts at the
   SPRINT-05 boundary.
4. The §8 review of each story as it reaches `review`.

## SRE backlog (pickup order)
Before this list: the observability review of every draft above, then the instrumentation check of
each story at `review`.

1. **#299**: amend AW-SRV-042's Observability section (§7) for the Loader's `no_zones_found` line
   while a boot waits. The waiting projector now repeats it about once a second (AW-INF-021's §8
   close). Implementation's item 9 makes the code change.
2. **`argocd-install`'s closing status** prints `stalled … argocd-recover` beside the `waiting` line
   on an empty store. It's a one-line follow-up from AW-INF-021's §8 close, with no story.
3. **AW-INF-029** — `content/core/VERSIONS` append-only — depends on AW-SRV-013 (`done`).
4. **AW-INF-028** — `make builder-reference` and `make guide-check` — depends on AW-CLI-009
   (implementation item 7).
5. **AW-INF-032** — `make stack-recover` — depends on AW-SRV-007 (implementation item 4) and
   AW-INF-017 (`done`). Last, because it waits on the sprint's largest story, and it's the demo.

## Implementation backlog (pickup order)
1. **AW-SRV-026** — exit into exact recovery when a Tick Boundary Record is lost — depends on
   AW-SRV-002 (`done`). S.
2. **AW-SRV-028** — durable cross-Zone handoff — depends on AW-SRV-003 (`done`). M.
3. **AW-SRV-043** — a restore verifies its round — depends on AW-SRV-006 and AW-SRV-019 (both
   `done`). M. After its contract review.
4. **AW-SRV-007** — recovery from snapshot and log tail, verified in CI — depends on AW-SRV-006 and
   AW-SRV-015 (`done`), items 1 and 2, and item 3 once architecture adds the edge. M. **On the demo's
   critical path:** SRE's item 5 waits on it.
5. **#319**: `TestReadiness_WaitsForTheCoreInEffect` races on its log buffer. It's test-only, XS.
6. **#172**: the stall-budget test, as architecture's item 1 rules.
7. **AW-CLI-009** — `andara-cli content reference` — depends on AW-CLI-002 (`done`). S. SRE's item 4
   waits on it.
8. **#312**: publish shows other packs' findings as the Builder's own, as architecture's item 2 rules.
   Every Builder publish shows it today, and the guide works around it.
9. **#299**: the code half, after SRE's item 1.
10. **#287** (`rooms_loaded` has no series during a content swap; ruled 2026-10-01), then **#290**
    (`state.verify` spans). The contracts hold for both.
11. **AW-SRV-046** — the owed follow-ups from SPRINT-03's reviews. S.

**Risk:** AW-SRV-028 and AW-SRV-007 are each M, and together they're most of the sprint. The demo
needs items 1–4. If implementation runs short, item 11 carries over first, then item 10, then
item 9. SRE's items 1–4 are the slack while it waits on item 4.

## Carryover from SPRINT-03
None. Every SPRINT-03 story is `done` (see its close-out). The stories that moved here on
2026-09-27 are AW-SRV-026, AW-SRV-028 and AW-SRV-007, at implementation items 1, 2 and 4.

**Groomed, not in this sprint:**
- AW-SRV-039, AW-SRV-040, AW-SRV-041, AW-SRV-044 and AW-SRV-045;
- AW-CLI-010 and AW-CLI-011;
- AW-INF-030 and AW-INF-031.

They're for SPRINT-05. None is on M2's path, and implementation's list is full.

## Close-out
(filled in by the next PM session)
