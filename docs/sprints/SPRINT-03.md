# SPRINT-03 — Builder content on dev
Status: closed
Dates: 2026-09-27 → 2026-10-02

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

## Re-planned 2026-09-28: the SRE lane
#139 added the SRE role after this sprint was planned in #138. So the plan had no observability
review step and no SRE backlog, and every infra story sat in architecture's list. This revision
moves the work that builds, ships, operates or observes to SRE (CLAUDE.md §2). It doesn't change
the demo goal, add scope, or drop any story. The one new story, AW-INF-027, exists only because the
story tooling can't yet say `lane: sre`. Until it merges, `make status` still lists the SRE stories
under architecture. This file is the plan.

AW-INF-023, the Builder's Guide, stays with architecture: Brian assigned the guide to architecture
on 2026-09-26. AW-INF-005 and AW-INF-007 each mix contract, SRE and implementation work. They aren't
in this sprint, and PM splits them at the SPRINT-04 boundary.

## Re-planned 2026-09-29: AW-INF-023's tooling
Architecture's contract review of AW-INF-023 sent its tooling to PM (Blocked by, items 2 and 3).
PM wrote it as AW-CLI-009 (`andara-cli content reference`) and AW-INF-028 (`make builder-reference`
and `make guide-check`). Both are at `draft` and **not in this sprint**. Implementation's list is
already full, and AW-INF-028 can't merge until Brian changes the `.claude/` generated-files list
upstream. The demo needs neither story. PM proposes moving AW-INF-023's AC-1 and AC-2 to AW-INF-028
so that the guide ships this sprint. That's a contract change, so it's architecture's call, in
`docs/feedback/AW-INF-023-builders-guide.md`. Both stories go to SPRINT-04's contract review.

## Contract review (SRE observability, then architecture — first)
SRE reviews each draft's Observability requirements (§7) first. Architecture's contract review
follows, and moves each draft to `ready` or `blocked`.

- AW-SRV-042 — an empty content store waits, unready (added 2026-09-30). **Review it first.** It's
  on the demo's critical path, ahead of AW-INF-021's merge. Its contract is architecture's ruling in
  `docs/feedback/AW-INF-021-dev-content-store.md`, lifted as written, plus AC-7 for drain.
- AW-INF-027 — the story tooling knows the SRE lane (new, SRE). Review it first, because SRE can't
  move its own stories until it merges. Architecture confirms its re-lane table, which moves
  AW-INF-019 and AW-INF-008 to `sre` while they're at `review`.
- AW-INF-026 — generated views stop being committed (Brian chose option (a), 2026-09-27). Amended
  2026-09-28: `.claude/` is now installed, so its charter edits cover CLAUDE.md only. It now depends
  on AW-INF-027, since both change the status tooling.
- AW-INF-025 — operating the state projector: stop, rebuild, start, and its volumes (#80). Its
  open question, how an in-cluster projector reads snapshot rounds, is decided here.
- AW-SRV-037 — Purgatory, the spawn Zone, in the test content. Confirm whether its merge supplies
  AW-INF-019's AC-4, since it also edits tests. If it doesn't, name the fixture-only change that
  does.
- AW-SRV-035 — an Operator grants a Builder their packs (implementation)
- AW-INF-020 — Builders download `andara-cli` without a Go toolchain
- AW-INF-024 — every environment spawns new Characters in Purgatory
- AW-INF-021 — `dev` serves its content from the content store
- AW-INF-022 — the Content Repository
- AW-INF-023 — the Builder's Guide (architecture). Answer
  `docs/feedback/AW-INF-023-builders-guide.md` items 1–5, and the proposal in item 1 first. It
  decides whether the guide waits on AW-CLI-009 and AW-INF-028.
- AW-SRV-036 — `goto`: a Builder jumps to any Room (implementation)
- AW-SRV-038 — a move describes the destination Room to the mover (implementation; Brian
  2026-09-27)
- AW-SRV-013 and AW-CLI-003 (`ready`):
  - Brian's Operator self-approval, in `docs/feedback/AW-SRV-013-operator-self-approval.md`.
    Record it in both story bodies, plus a dated note in ADR-0004.
  - The activation refusals and `server info`, in `docs/feedback/AW-SRV-013-activation-refusals.md`.
- For AW-INF-020 to AW-INF-023: answer items 1–5 in `docs/feedback/AW-INF-021-dev-content-store.md`
  first. SRE gives its operability view on items 1, 2 and 4 before architecture decides them.
  1. How core reaches `dev`'s store on every roll.
  2. Moving `dev`'s log off `dir`.
  3. Where the fixture's source lives.
  4. Where the Content Repository's CI gets core.
  5. Where the guide lives.

## Architecture backlog (pickup order)
After the contract review above:

1. The §8 review of each story as it reaches `review`, starting with the carryover: AW-SRV-019,
   AW-INF-019 and AW-INF-008, once SRE has recorded their instrumentation checks.
2. AW-INF-023 — the Builder's Guide — depends on AW-CLI-003, AW-SRV-035, AW-SRV-036 (implementation
   items 8, 7, 9) and AW-INF-020 to AW-INF-022 and AW-INF-024 (SRE items 6–9). It's last so it
   describes what was built. Its AC-3 walk-through is the demo.

## SRE backlog (pickup order)
Before this list: the observability review of every draft above, then the instrumentation check of
AW-SRV-019, AW-INF-019 and AW-INF-008, recorded in each story's §8 record.

1. AW-INF-027 — the story tooling knows the SRE lane — depends on AW-INF-001 (`done`). First,
   because until it merges no story can say `lane: sre`.
2. AW-INF-026 — generated views stop being committed — depends on AW-INF-027. Early, because every
   later PR pays the rebase cost until it lands.
3. AW-INF-019 (carryover) — AC-4 — needs implementation item 1's merge, or the fixture-only change
   the contract review names.
4. AW-INF-025 — projector targets and volumes (#80) — depends on AW-SRV-019 (carryover, `review`)
   and AW-INF-018 (`done`)
5. AW-INF-008 (carryover) — AC-2, the state projector on `dev` — after item 4
6. AW-INF-020 — `andara-cli` release binaries — depends on AW-INF-013 (`done`)
7. AW-INF-024 — spawn in Purgatory everywhere — depends on AW-SRV-037 (implementation item 1)
8. AW-INF-021 — `dev` serves content from the store — depends on AW-SRV-013, AW-SRV-035 and
   AW-CLI-003 (implementation items 4, 7 and 8) and AW-INF-019 (item 3)
9. AW-INF-022 — the Content Repository — depends on AW-CLI-002 (implementation item 6), AW-INF-020
   and AW-INF-021

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
11. AW-SRV-042 — an empty content store waits, unready, plus `andara-cli --tls-server-name` (added
    2026-09-30). It depends on AW-SRV-012 (`done`), AW-SRV-013 and AW-CLI-001. **On the demo's
    critical path:** AW-INF-021 depends on it (architecture adds the edge when it moves this story to
    `ready`), and AW-INF-022 and AW-INF-023 come after that. Without
    it, `dev` can't be seeded, because a kafka server with an empty store exits before it serves
    Admin. It's architecture's ruling in #289, and SRE's chart and seed halves are in #288.
12. #143 — the state projector diverges one tick after bootstrapping from a snapshot round
    (added 2026-09-30). The fix belongs in round capture or restore (AW-SRV-006 code), not in the
    projector. It comes with the regression test named in AW-SRV-019's 2026-09-29 §8 pass. It holds
    AW-INF-025 ACs 2 and 4, AW-INF-008 AC-2 and AW-SRV-019 AC-6, and in SPRINT-04 it holds M2's
    "matching State Hash". `dev`'s projector is at 0 replicas until it's fixed.
    Source: `docs/feedback/AW-INF-025-projector-operations.md`, "For PM".
13. #267 — the publish gate accepts `BlobRef.path` values that escape the pack (added 2026-09-30).
    The contract is in the issue: refuse `INVALID_ARGUMENT` `validation`, audited as `reject`, with
    `unsafe_source_path`'s rule. It's server-side hardening under AW-SRV-013, and `fetch` keeps its
    own guard.
14. #326 — the state projector publishes and activates `andara.core` at boot, racing the server
    (added 2026-10-02, found by AC-7's first run). The projector loads content read-only: no
    `bootCore`, and no write to `andara.content.*` or `andara.audit.v1`. A test asserts that a
    projector boot on an empty store writes nothing there. It holds AW-INF-021 AC-7, which needs the
    server to win the core publish, and it would fail under ADR-0011's ACLs (AW-INF-030). Ruled by architecture on the issue: on a
    store without Zone-bearing content, the projector waits, unready, as AW-SRV-042's server does,
    instead of crash-looping. It's tested by a high-water-mark assertion of no writes, and by Ready
    after the seed with no restart. It's ahead of #308.
15. #308 — `core_version_mismatch` tells a Builder to downgrade `andara-cli` after a core bump
    (added 2026-10-01). The contract is in the issue and in `errors.md` §3.1: the remedy follows the
    direction of the skew, with a test for each direction. It's small, in `content/lang`. It's not on
    the demo path, since `dev`'s core is still `andara.core@1`. But the first core bump turns every
    Builder's `check` red with backwards advice (AW-INF-022 AC-8). Last on the list, and the first to
    carry over if implementation runs short.

**Risk:** implementation has ten items, and AW-SRV-013 and AW-CLI-003 are the size of the sprint.
If it runs short, AW-SRV-038 carries first, then #128. The demo needs items 1 and 4–9. SRE's items
8 and 9 wait on implementation's items 4, 7 and 8, so SRE's early items (1–7) are the slack.

## Re-planned 2026-09-30: architecture's asks
Every item in implementation's original list is at `review` or `done`, so implementation takes on
#143 and #267 (items 11 and 12). Neither needs a contract review. Both are defects against stories
whose contracts already hold. #143 comes first because it holds four ACs in this sprint and M2 in
the next.

PM groomed four more stories from architecture's reviews, all at `draft`. They are **not in this
sprint**. None is on the demo path, and SRE, which would review their observability, holds the
demo's real risk: AW-INF-021 and AW-INF-022 haven't started, and the Builder's Guide (AW-INF-023,
now `ready`) and Brian's walk-through come after them. They go to SPRINT-04's contract review:
- AW-SRV-039 — Admin acting-as: `andara-act-as` metadata and `ContentVersion.publisher`. It
  includes AW-CLI-003's deferred `--as`.
- AW-SRV-040 — every Admin account write audits outside the Account write lock
- AW-SRV-041 — a bystander reads `<name> has arrived.` when there's no Exit back (Brian, 2026-09-30)
- AW-INF-029 — `content/core/VERSIONS` is append-only, enforced by `make check`

## Re-ordered 2026-10-01: AC-7's first run before the demo; the confirming run after
**The order in force** (2026-10-02, the update below): the walk-through ran first, and AC-7's
*confirming* rebuild runs after it. That's the order AW-INF-021's §8 record already states ("after
SPRINT-03's demo… it doesn't hold AW-INF-023"), so the story record and this plan agree. What
follows is the history.

Brian's decision on 2026-10-01 was that a *first* run of AW-INF-021 AC-7, the rebuild of `dev` from
nothing, would go before the M3 walk-through (AW-INF-023 AC-3). AC-7 ends at the
state the walk-through starts from (`andara.core` and `town`), so the demo runs on an environment
rebuilt from nothing. And a defect in the uninstall, Secrets and first-sync path surfaces before the
demo rather than after it. The rebuild clears Brian's Builder Account and grants. Guide section 2
re-creates them, and the walk-through exercises that. If AC-7 finds a defect, the walk-through waits
for its fix.

**Update 2026-10-02: AC-7's first run.** SRE rebuilt `dev` from a deleted namespace and seeded it.
`dev` is healthy, with `andara.core@1` and `town@1`, and AC-1 and AC-2 hold. AC-7 itself didn't
pass. It needed hand steps, and it found three defects:
1. `kafka-operator` skipped Strimzi's RoleBindings after the namespace delete. SRE's fix is on
   `sre/kafka-operator-rebinds-namespaces`.
2. `argocd-install` waits for Healthy, which an empty store can't reach before the seed. SRE's to
   fix.
3. #326, the projector racing the server to publish the core. Implementation's, item 14 above.

**The procedure must delete the namespace.** AC-7 as written (`argocd-uninstall`, fresh topics,
`argocd-install`) keeps the namespace: `argocd-uninstall` doesn't cascade, and `argocd-install`
leaves existing Secrets as they are (`scripts/argocd.py`). So that procedure never recreates the
Secrets or the namespace. The first run deleted `andara-dev` outright (Brian, in SRE's session),
which is what found defect 1. The confirming run deletes it too. Making AC-7's text say so is
architecture's, in `docs/feedback/AW-INF-021-dev-content-store.md` (found by Codex on #325).

A clean pass needs all three fixed and a second full rebuild, which would erase the walk-through's
work. So the rebuild that confirms AC-7 runs **after** the demo. The order to close is now:
1. Brian walks through the guide on the rebuilt `dev`, and architecture moves AW-INF-023 to `done`.
2. SRE's two fixes and #326 merge. Then SRE re-runs AC-7, and architecture moves AW-INF-021 to
   `done`.
3. PM closes the sprint. If step 2 isn't done by then, AW-INF-021 carries over to SPRINT-04 on AC-7
   alone, with #326 beside it.

## Carryover from SPRINT-02
Three stories, all at `review`. The reasons are in SPRINT-02's close-out:
- AW-INF-019 — AC-4 (SRE item 3). Re-laned to `sre` by AW-INF-027.
- AW-SRV-019 — the AC-5 test (implementation item 2); its production line needs AW-INF-025
- AW-INF-008 — AC-2 (SRE item 5), after AW-INF-025. Re-laned to `sre` by AW-INF-027.

## Close-out
Closed 2026-10-02 by PM, against `origin/main` at `ba7e4a4`.

**Demo goal: met.** The M3 gate passed on `dev` with no §9 defects (`SPRINT-03-demo.md`). Brian ran
it from no clone of the code repository: publish, self-approve, activate, `play` with `goto`, change,
and roll back, with `server info` naming the content at each step and no deploy. PM re-ran the local
steps at close-out against `andara-cli ba7e4a4`. PM couldn't re-run the `dev` steps without Brian's
logins, and Brian accepted his walk-through as the run.

**Stories: 20 of 20 done.** All 15 implementation items are closed, including the five issues (#128,
#143, #267, #326 and #308). The board reads `Current (SPRINT-03): 20/20 done — 20 done`.

### Final status on `origin/main`

| Story | Status | Built in | §8 record (moved to `done`) |
|-------|--------|----------|-----------------------------|
| AW-SRV-037 | **done** | #157 | #268 |
| AW-SRV-019 | **done** | #147, #307 | #320 |
| AW-SRV-013 | **done** | #173, #284, #322 | #296 |
| AW-SRV-034 | **done** | #162 | #268 |
| AW-CLI-002 | **done** | #178, #285, #335 | #315 |
| AW-SRV-035 | **done** | #266, #281 | #293 |
| AW-CLI-003 | **done** | #271, #283 | #296 |
| AW-SRV-036 | **done** | #274 | #296 |
| AW-SRV-038 | **done** | #278 | #296 |
| AW-SRV-042 | **done** | #298 | #320 |
| AW-INF-027 | **done** | #146 | #154 |
| AW-INF-026 | **done** | #160 | #167 |
| AW-INF-019 | **done** | #157 (AC-4's fixture) | #176 |
| AW-INF-025 | **done** | #171 | #320 |
| AW-INF-008 | **done** | — (AC-2 on `dev`; verified in #314) | #320 |
| AW-INF-020 | **done** | #158 | #167 |
| AW-INF-024 | **done** | #269 | #279 |
| AW-INF-021 | **done** | #165, #288, #327, #328, #337 | #338 |
| AW-INF-022 | **done** | #313, `andara.solo7.media` #11 | #320 |
| AW-INF-023 | **done** | #310, #323, #330 | #338 |

| Issue | Fixed in |
|-------|----------|
| #128 | #166 |
| #143 | #307 |
| #267 | #322 |
| #326 | #334 |
| #308 | #335, #336 |

### Carryover to SPRINT-04
None. SPRINT-04 takes on the M2 server-kill stories that were moved there on 2026-09-27
(AW-SRV-026, AW-SRV-028 and AW-SRV-007), and the drafts groomed during this sprint.

### Status defects (reported, not fixed)
None in the story frontmatter. The board's mirror issues for six stories that were already `done`
(#219–#221, #223, #245, #304) were still open. The close-out sync closed them.

### Triage
**Issues opened during SPRINT-03:**
- Planned in SPRINT-04:
  - #312, publish showing other packs' findings (architecture rules on the wording, then
    implementation);
  - #172, the snapshot stall-budget test that fails under load, now 4 of 5 local `make check` runs
    (architecture and SRE decide how it measures, then implementation);
  - #319, a test-only race (implementation);
  - #287 and #290 (implementation; the contracts hold);
  - #299, the wait's `no_zones_found` line, now repeated about once a second by the waiting
    projector (SRE amends `AW-SRV-042`'s §7, then implementation).
- **#152 (`content-gap`) is closed as resolved by AW-CLI-002.** `andara-cli` embeds `andara.core`, so a pack
  compiles offline with no cache, and `fetch-core --help` no longer names `content/core` as a
  source.

**Feedback items:**
- Follow-ups that didn't hold their stories, from seven §8 reviews, are gathered into
  `AW-SRV-046` (draft, SPRINT-04). Each source feedback file says so.
- `AW-SRV-007-recovery-scale.md`'s items 1 and 2 are still open, now with items 3 and 4 (the
  `AW-SRV-043` edge, and `make stack-recover`). They're in SPRINT-04's contract review.
- `AW-INF-027-status-sprint-scope.md`: Brian chose (c). `make status` stays a backlog view.
- The roadmap's "two identities" note for Phase 1 exit criterion 4 now records Operator
  self-approval (`AW-SRV-013-operator-self-approval.md`).
- The AW-INF-005 and AW-INF-007 split is routed to architecture in
  `docs/feedback/AW-INF-005-007-split.md`. Both are `ready`, which PM can't rewrite, and neither is
  pickable in SPRINT-04.

### Other findings
- **The demo found what the tests couldn't.** The walk-through found nine points the guide didn't
  cover. AC-7's first rebuild found three defects in the uninstall and first-sync path. All twelve
  were closed inside the sprint. Running the gate on the real environment, from the user's
  position, earns its cost.
- **The plan changed four times mid-sprint** (the "Re-planned" and "Re-ordered" sections above).
  None of the changes moved the demo goal. Implementation finished its original list early, and took
  on items 11–15, which architecture's and SRE's reviews had found.
- **Still Brian's:** Purgatory's description is a placeholder that every new Character reads first.
