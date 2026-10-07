# SPRINT-04 — The M2 gate on the local stack
Status: closed
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
- **AW-INF-033** — the Content Repository's `make bootstrap` sets up commit signing (new, SRE). It's
  SPRINT-03's demo §9 defect. Its contract is SRE's own spec on `andara.solo7.media` #14.

## Architecture backlog (pickup order)
After the contract review above:

1. **#172**: decide how the snapshot stall-budget test measures, either run alone or on CPU time,
   with SRE's view on CI. Under load it fails most local `make check` runs (4 of 5 at load average
   4–11, per the issue), which taxes every lane.
   Implementation's item 6 waits on this.
2. **#312**: rule on how `duplicate_zone` names the two packs, and how findings are placed on the
   publisher's own source. "Root finding only" already holds (`errors.md` §1 rule 7). Implementation's
   item 8 waits on this.
3. **`docs/feedback/AW-INF-005-007-split.md`**: name the split lines. PM grooms the parts at the
   SPRINT-05 boundary.
4. The §8 review of each story as it reaches `review`.
5. **`docs/feedback/AW-SRV-011-first-look-before-subscribe.md`** (added 2026-10-03): rule what a
   client may rely on before its first command (#116, #117). Four options are listed there, and
   most change `AW-SRV-011`'s Egress seam or `AW-CLI-007`'s verified order. Implementation's item
   13 waits on this, so take it after item 3 and ahead of item 4.

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
5. **AW-INF-033** — the Content Repository's `make bootstrap` signs commits — depends on
   AW-INF-022 and AW-INF-023 (both `done`). It's SPRINT-03's §9 defect, and the work is in
   `andara.solo7.media`.
6. **AW-INF-032** — `make stack-recover` — depends on AW-SRV-007 (implementation item 4) and
   AW-INF-017 (`done`). Last, because it waits on the sprint's largest story, and it's the demo.

## Implementation backlog (pickup order)
1. **AW-SRV-026** — exit into exact recovery when a Tick Boundary Record is lost — depends on
   AW-SRV-002 (`done`). S.
2. **AW-SRV-028** — durable cross-Zone handoff — depends on AW-SRV-003 (`done`). M.
3. **AW-SRV-043** — a restore verifies its round — depends on AW-SRV-006 and AW-SRV-019 (both
   `done`). M. After its contract review.
4. **AW-SRV-007** — recovery from snapshot and log tail, verified in CI — depends on AW-SRV-006 and
   AW-SRV-015 (`done`), items 1 and 2, and item 3 once architecture adds the edge. M. **On the demo's
   critical path:** SRE's item 6 waits on it.
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
12. **#69** (added 2026-10-03): seven egress and boot test waits on the Hub's `Subscribers` gauge
    before an emit, in `egress_test.go`, `gateway_test.go` and `ingress_test.go`. It was latent
    until 2026-10-03, when `TestScope_RoomDelivery` (`egress_test.go:295`) failed a local
    `make check` under load. #370 recorded that and is closed as a duplicate. The fix is to wait on
    the egress's `Streams` gauge wherever an emit follows (`server/README.md`, Event egress).
    Test-only, S.
13. **#116 and #117** (added 2026-10-03): `play`'s first `look` can reach the server before its
    stream attaches, which failed `TestPlay_SelectsBeforeSubscribe` in CI on #107 and #108, two PRs
    that don't touch that code. The outcome: the automatic `look`'s answer always reaches the
    stream (`AW-CLI-007` AC-4), and the test asserts the order deterministically. It waits on
    architecture's item 5. Size after that ruling: S for a CLI and test-side fix, M if the seam
    changes.

**Added 2026-10-03: CI stability (Brian).** Items 12 and 13 are test flakes that every lane pays
for: item 13 in CI, and item 12 in `make check` under load. #319 and #172 (items 5 and 6) are the
others. Pick up item 12 after item 6, or as soon as item 6 is waiting on architecture's ruling,
ahead of item 7. Item 13 follows once architecture's item 5 rules. Until then, implementation skips
it. The items aren't renumbered, so the cross-references above still hold. #101
(`TestKafka_ConcurrentSubmitsOrdered`'s fixture deadline) stays out.

**Risk:** AW-SRV-028 and AW-SRV-007 are each M, and together they're most of the sprint. The demo
needs items 1–4. If implementation runs short, item 11 carries over first, then item 10, then
item 9. Item 12 doesn't carry: it goes ahead of items 7–11. Item 13 carries over only if
architecture's item 5 hasn't ruled by the sprint's last week, or if the ruling makes it M (a seam
change to a `done` story), which would be a third M beside AW-SRV-028 and AW-SRV-007. SRE's items
1–5 are the slack while it waits on item 4.

## Carryover from SPRINT-03
None. Every SPRINT-03 story is `done` (see its close-out). The stories that moved here on
2026-09-27 are AW-SRV-026, AW-SRV-028 and AW-SRV-007, at implementation items 1, 2 and 4.

## Groomed, not in this sprint
These drafts are for later sprints: AW-SRV-039, AW-SRV-040, AW-SRV-041, AW-SRV-044, AW-SRV-045,
AW-CLI-010, AW-CLI-011, AW-INF-030 and AW-INF-031. None is on this sprint's M2 path on the local
stack, and implementation's list is full. They're in prose under their own heading because
`project-mirror` counts a list item that opens with a story ID in a Carryover or backlog section as
sprint work. (Fixed 2026-10-02: the first version listed them under Carryover, and the board
showed 19 stories in SPRINT-04.)

AW-INF-034, the M2 gate on `dev`, is SPRINT-05's demo (Brian, 2026-10-03). It waits on this
sprint's AW-INF-032.

## Close-out
Closed 2026-10-05 by PM, against `origin/main` at `ee865ea`.

**Demo goal: met** (`SPRINT-04-demo.md`), on CI's evidence and not PM's own run. `make stack-recover` passed
in the `stack` workflow's run 37392791066 on `e007216` (the last code merge on `main`): the server was
ready 1.3 s after `SIGKILL` (RTO 120 s), from round 4239 with a matching State Hash, and both players
rebound at the Town Hall. PM didn't re-run it on a clean checkout. The only local stack on this host is
the SRE clone's, on ports 8080 and 8443, and a second `make up` would collide with it. Brian chose to
accept CI's run (2026-10-05, his answer "c"), as he did for SPRINT-03's `dev` steps.

**Stories: 10 of 10 done.** The board reads `Current (SPRINT-04): 10/10 done — 10 done`.

### Final status on `origin/main`

| Story | Status | Built in | §8 record (moved to `done`) |
|-------|--------|----------|-----------------------------|
| AW-SRV-043 | **done** | #365, #381 (verify), #397 (operator step) | #398 |
| AW-INF-032 | **done** | #418, #432 | #433 |
| AW-SRV-007 | **done** | #416, #428, #431, #435 | #436 |
| AW-CLI-009 | **done** | #367, #383 (verify) | #394 |
| AW-INF-028 | **done** | #392, #402 (verify) | #410 |
| AW-INF-029 | **done** | #361 | #380 |
| AW-SRV-046 | **done** | #368, #388 (verify) | #394 |
| AW-INF-033 | **done** | #399 | #441 |
| AW-SRV-026 | **done** | #355, #371 (verify) | #380 |
| AW-SRV-028 | **done** | #407, #442 (the summary `warn`, #409) | #444 |

| Issue | Fixed in |
|-------|----------|
| #172 | #386 (architecture's ruling #382, amendment #390) |
| #312 | #395 (ruling #384) |
| #299 | #369 (SRE's amendment #391) |
| #319 | #358 |
| #287 | #359 |
| #290 | #360 |
| #69 | #379 |
| #116 | #393 |

### Carryover to SPRINT-05
None. Every SPRINT-04 story is `done`.

### Status defects (reported, not fixed)
- **#117 is open although its fix merged.** #393's body says "Closes #116 and #117", but GitHub linked only
  #116, so #117 (`play`'s first `look` reaching the server before its Subscribe attaches) is still open.
  `AW-SRV-011` AC-11 and #393 are its fix. It needs closing by whoever triages issues: a comment citing
  #393, then close.
- No story frontmatter defects. The board's mirror was synced at `ee865ea`, so every SPRINT-04 story's
  issue is closed.

### Triage
Issues opened during SPRINT-04, and who owes each:
| Issue | Owes | In SPRINT-05? |
|-------|------|---------------|
| #363 `dev` stops booting once `andara.events.v1` retention expires its first segment | architecture, then implementation | **Yes.** It can break the M2 gate on `dev`; architecture rules whether it still holds now that `AW-SRV-007` has shipped |
| #362 runbooks can't stop a crash-looping server on `dev`: Argo CD self-heal undoes `kubectl scale` | SRE | **Yes, as AW-INF-037** (groomed in this PR, `lane: sre`). Not on the demo's path |
| #423 `uint64` log attributes above 2^63 are rounded in Loki | SRE | No. Observability polish, held |
| #422 projector integration tests time out waiting for the first commit (intermittent) | SRE | No. The stack workflow is green; held |
| #396 `projector-rebuild` reports success without proving the restore verified | SRE | No. Held |
| #385 `TestLogExport_BoundedQueueDropsAndCounts` fails under CPU load | implementation | No. A test flake under CPU load; held |
| #101 `TestKafka_ConcurrentSubmitsOrdered`'s fixture deadline | implementation | No. Stayed out of SPRINT-04 and stays out |
| #437–#440, #445, #446 | the mirror's issues for AW-INF-035, AW-SRV-048 to 050, AW-INF-036 and AW-SRV-051 | See SPRINT-05's contract review |

Unanswered `docs/feedback/` items, and who owes each:
- **Architecture:** `docs/feedback/AW-SRV-048-transit-orphan-design.md`, `AW-SRV-049-roster-observation.md`
  and `AW-SRV-051-failure-injection-surface.md` (the last has its rulings recorded, so PM folds them in
  at grooming), and AW-SRV-027's re-size after it grew (`AW-SRV-028-handoff-contract.md`).
- **PM:** `docs/feedback/AW-INF-005-007-split.md`, the nine children. SPRINT-05 defers them to the next PM PR
  and says why.

### Other findings
- **The `argocd-install` closing status** item (SPRINT-04's SRE item 2) merged as #353.
- **`dev` must be reset** with the deploy that carries `AW-SRV-028`: a log written before it that holds
  a cross-Zone move won't replay (recovery exits 6). Brian confirmed the reset on 2026-10-05 and SPRINT-05
  plans it ahead of `AW-INF-034`.
