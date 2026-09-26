# SPRINT-01 — M1 in the operator's hands
Status: closed
Dates: 2026-09-24 → 2026-09-25

## Demo goal
An Operator on a fresh local stack creates a Character with `andara-cli character create`, enters
the World with `andara-cli play --character`, reads a Room, types `north`, and reads a different
Room, with the Command having gone through the log. `make stack-play` asserts the same run in the
`stack` workflow, with a second client seeing the arrival and the departure.

This is the **M1 gate** in `docs/roadmap.md`, whole. It can be proved today only up to the
refusal: on `origin/main`, `stack-play`'s Protocol half passes (`internal/smoke`,
`TestLive_M1Gate`), while `andara-cli play` cannot select a body, so every Intent it sends is
refused with "you are not in the world". `AW-CLI-007` is the only story between that and the gate,
and it needs nothing new groomed.

What it is not:
- **Not M2.** A restart still replays the whole log. There is no linkdead grace (`AW-SRV-015`) and
  no recovery from a snapshot (`AW-SRV-007`). A dropped connection ends the Session.
- **Not on the box.** The demo runs on the compose stack from `make up`. `AW-INF-014`'s `dev`
  namespace is in the §8 queue, not in the demo.
- **Not multi-Zone, not authored content, not a protocol freeze.** Those are M2–M4.

## Contract review (architecture, first)
None. The demo needs no new story. `AW-INF-015` stays `draft` and out of this sprint.

## Architecture backlog (pickup order)
The §8 queue first. The order follows the demo, then the order things unblocked each other:

1. AW-SRV-014 — Character creation, selection, and binding — merged #43. Its lines inherited from
   `AW-CLI-004` and `AW-SRV-011` are closed by `AW-CLI-007`'s Definition of done (full gate in
   `stack-play`), so they may keep it at `review` until that lands.
2. AW-CLI-004 — `andara-cli play` — merged #39
3. AW-SRV-011 — Event egress, streaming with backpressure — merged #37
4. AW-SRV-031 — Submit idempotency — merged #38
5. AW-INF-013 — Publish the server image to ghcr — merged #60. AC-1 was owed its first run on
   `main`, and `publish` has since succeeded on `9a8ad9d`, `629741f` and `ace7dd1`.
6. AW-INF-014 — A Kafka broker on the box — merged #62. AC-4 and AC-5 are owed and need
   `ANDARA_BOOTSTRAP_OPERATOR` set for `dev`, which is Brian's.
7. AW-INF-008 — Cluster observability wiring — merged #56. Box ACs 1–4 and 6 are owed and were
   waiting on 013 and 014. They need a `GRAFANA_CLOUD_READ_TOKEN`. Run 6, 7 and 8 in one box
   session with Brian.
8. AW-SRV-006 — Zone snapshots keyed to offsets — merged #46/#47/#53/#54/#59
9. AW-SRV-019 — State projector and compacted state topic — merged #64
10. AW-CLI-006 — Content Language compiler — merged #57. Closes after implementation item 2.
11. AW-CLI-005 — Content Language v1 spec — flips with no further review once AC-9's `valid/`
    runner is in `make check` (PR #58's record).

Then the carryover:

12. AW-SRV-012 — the architecture-owned items in
    `docs/feedback/AW-SRV-012-content-resolution-and-reload.md`:
    - §1: the `content-freshness` SLO
    - §2: the `content-load-failing` runbook
    - §3: the three protocol additions (`fallback_room` = 6, `content_swap` = 17,
      `EntityRelocated` = 19)
    - §4: the corpus move
    - §5: whether `content reload` becomes an Admin RPC
    - §7: the `content.source` default
    - §9: the doubly claimed metric

    Its second half can't start without §3.

## Implementation backlog (pickup order)
1. AW-CLI-007 — `andara-cli character create`/`list` and `play --character`. Depends on
   AW-CLI-004 and AW-SRV-014, both at `review`, so it's pickable now. **This is the demo.**
2. AW-CLI-006 — the inherited Definition-of-done line from the AW-CLI-005 review: AC-9's
   decompile-and-recompile pass over `corpus/valid/`, run in `make check`. It sits at `review`, so
   this is follow-up work on it, not a new story. It closes AW-CLI-006 and AW-CLI-005.
3. AW-SRV-012 — carryover, `in-progress`: AC-2, AC-3, AC-9 and AC-10, plus starting
   `Follow`/`Watch` from `server/boot`. Pickable once architecture item 12 §3 lands. This is
   stretch work: if it's still open at close-out, it carries again.

## Carryover from SPRINT-00
No sprint preceded this one. This section is a snapshot of `origin/main` at `ace7dd1`
(2026-09-24): 56 stories, 1 `in-progress`, 11 `review`, 26 `ready`, 1 `draft`, 17 `done`.

**In progress**
- AW-SRV-012 — Content resolution from the store and reload at a tick boundary. PR #63 landed the
  half that doesn't depend on the protocol: the resolver, the blob cache and the retained-version
  rule, meeting AC-1, AC-4–8 and AC-11. AC-2, AC-3, AC-9 and AC-10 wait on the protocol additions
  in its feedback file §3. Status corrected to `in-progress` in #65.

**At `review` — architecture's §8 list**

| Story | Lane | Merged in | Known owed items |
|-------|------|-----------|------------------|
| AW-SRV-014 | impl | #43 | inherited lines closed by AW-CLI-007 |
| AW-CLI-004 | impl | #39 | — |
| AW-SRV-011 | impl | #37 | — |
| AW-SRV-031 | impl | #38 | — |
| AW-INF-013 | arch | #60 | AC-1–3, AC-7: first run on `main`, now available |
| AW-INF-014 | arch | #62 | AC-4, AC-5: `ANDARA_BOOTSTRAP_OPERATOR` for `dev` (Brian) |
| AW-INF-008 | arch | #56 | AC-1–4, AC-6 on the box: read token |
| AW-SRV-006 | impl | #46 | — |
| AW-SRV-019 | impl | #64 | status corrected to `review` in #65 |
| AW-CLI-006 | impl | #57 | AC-9 `valid/` runner (inherited) |
| AW-CLI-005 | arch | #44 | closes with AW-CLI-006's runner (PR #58) |

**Status defects**
None open. `make status` on `ace7dd1` is clean. No `ready` story has code merged under its ID.
Two defects were found and fixed before this snapshot, both in #65: AW-SRV-012 merged in #63
while still `ready`, and AW-SRV-019 merged in #64 while still `in-progress`.

**Other findings**
- The remote branch `origin/claude/gallant-volta-q1hz5t` outlives its merge (#64) and doesn't use
  a §4 agent prefix. It's the owning lane's to delete.
- No open GitHub issues to triage.

## Close-out
Closed 2026-09-25 by PM, against `origin/main` at `343cdbe`.

**Demo goal: met.** Every step of `docs/sprints/SPRINT-01-demo.md` passed on a fresh clone. The M1
gate runs in `andara-cli` by hand and in `make stack-play`. One step needed a hand-written rebuild
(#73), marked `§9 defect → AW-INF-016` and carried into SPRINT-02.

**Stories: 2 of 13 done.** Most of the sprint's work merged. Most of it didn't pass §8.

### Final status on `origin/main`

| Story | Status | Merged in | §8 record |
|-------|--------|-----------|-----------|
| AW-SRV-011 | **done** | #37 | #71 |
| AW-SRV-031 | **done** | #38 | #71 |
| AW-CLI-007 | review | #72, #83 | none yet |
| AW-CLI-004 | review | #39 | #71 |
| AW-SRV-014 | review | #43 | #71 |
| AW-SRV-012 | review | #63, #79, #82, #85–#91, #93, #95 | #94 |
| AW-CLI-006 | review | #57, #75 | #76 |
| AW-CLI-005 | review | #44 | #71; see status defects |
| AW-SRV-006 | review | #46, #47, #53, #54, #59 | #76 |
| AW-SRV-019 | review | #64 | #81 |
| AW-INF-013 | review | #60 | #81 |
| AW-INF-014 | review | #62 | #81 |
| AW-INF-008 | review | #56 | #76 |

### Carryover to SPRINT-02, and why
- **AW-CLI-007:** no §8 run yet. It merged on 2026-09-25 (#72, with `stack_play.sh` in #83).
- **AW-CLI-004:** its one failing line, the full M1 gate in CI, is met by #83. It moves to `done` in
  the same pass as CLI-007's §8 (`docs/feedback/AW-CLI-004-play.md` §1).
- **AW-SRV-014:** implementation owes the bind-applied `info` log
  (`docs/feedback/AW-SRV-014-character-roster.md` §1). The live lines AW-CLI-007 enumerates close
  at CLI-007's §8. #70 is closed (fixed in #93).
- **AW-SRV-012:** the §8 in #94 left an owed list, which #95 delivered. The record says it moves to
  `done` at the next §8 with no further review. The dir-source series are carried to AW-SRV-013 as
  an inherited line.
- **AW-CLI-006:** the `valid/` runner landed in #75. It still owes `admin/README.md` command and
  flag docs (§8 #76, feedback §14 item 2).
- **AW-CLI-005:** see status defects below.
- **AW-SRV-006:** §8 #76 found two failures. AC-3: `StateHash()` covers `ZoneCanonicalBytes` only,
  not the amended superset. AC-8: the round is gated on `Publish` returning, not on the broker's
  ack, and has no boundary/timeout abandonment. Implementation owes the fixes
  (`docs/feedback/AW-SRV-006-zone-snapshots.md` §13).
- **AW-SRV-019:** §8 #81 found these, recorded in `docs/feedback/AW-SRV-019-state-projector.md`:
  - AC-7 failed on #70, which is now fixed, so it needs a re-check.
  - AC-5's forced-compaction assertion is owed.
  - AC-6 is owed at the stated scale.
  - The divergence must become sticky.
  - `andara_state_topic_bytes` reads 0.
  - The production DoD line waits on #77 and #80.
  - AC-9 has no carrier story.
- **AW-INF-013:** AC-3 and AC-7 are owed on the box (#81).
- **AW-INF-014:** AC-4, 5, 6 (server half) and 9 (Grafana half) are owed on the box. They need
  `ANDARA_BOOTSTRAP_OPERATOR` set for `dev`, which is Brian's, and #77 for the AC-3
  re-confirmation. #81 records the box session's run order.
- **AW-INF-008:** the box ACs need Brian's operator credential and a `GRAFANA_CLOUD_READ_TOKEN`
  (§8 #76). AC-2 also needs #77.

### Status defects (reported, not fixed)
- **AW-CLI-005 is `review` though its closing condition is met.** Its §8 record (and PR #58) says it
  flips to `done` with no further review once AC-9's `valid/` runner is in `make check`. #75 put
  `diffRecompile` into `Conformance()`, which `make content-conformance` and `TestConformance` both
  run under `make check`. The flip is architecture's.
- **`docs/status.md` prints `AW-SRV-019  .`** under "Decisions the lanes are waiting on".
  `scripts/gen_status.py`'s `NEEDS_RE` captures the text after a `[NEEDS BRIAN]` tag that ends its
  sentence ("The projection-freshness target stays `[NEEDS BRIAN]`."). That's architecture's
  script. It isn't frontmatter, but it's the same honesty problem.

### Other findings
- **`make up` runs a stale server image (#73).** The demo hit it at step 2. It's the §9 defect
  behind AW-INF-016.
- **A relaunch within about 300 ms of a clean quit is refused `already_live`.** It reproduced 4 of 4
  after a session that moved. It's contract-conformant, but a trap. It's in
  `docs/feedback/AW-SRV-014-character-roster.md` §3 for architecture, to settle with AW-SRV-015
  AC-5.
- **PR #64 merged from `claude/gallant-volta-q1hz5t`**, with no §4 lane prefix. It wrote 434
  unreviewed lines into architecture-owned paths and a 35 MB binary at the repo root (#78).
  Architecture reviewed the paths in #81. The branch has since been deleted.
- **The sprint's shape:** 12 of 13 stories were already merged when it began, so it was mostly a
  §8 sprint. §8 found real failures in three stories (AW-SRV-006, AW-SRV-019, AW-SRV-012's first
  pass). That, and the box session that never happened, is why so little reached `done`.
- **The demo run tore down the shared compose stack** with `make down VOLUMES=1`, because the
  project name is fixed (Brian's call, 2026-09-25). Lanes re-run `make up` from their own clones.

### GitHub issues opened this sprint

| Issue | Lane | Disposition |
|-------|------|-------------|
| #69 Egress/boot tests wait on the Hub gauge | implementation | Backlog. Latent, and it has never failed on `main`. |
| #70 Empty `content_version` on spawn | implementation | Closed, fixed in #93. |
| #73 `make up` runs a stale image | architecture | SPRINT-02 as AW-INF-016 (§9 defect, found in the demo). |
| #74 Compose never completes a snapshot round | architecture | SPRINT-03, with AW-SRV-007's recovery slice. |
| #77 No target applies topic config (§9) | architecture | SPRINT-02 as AW-INF-018. It gates the box session. |
| #78 35 MB binary at the repo root | implementation | SPRINT-02 chore, with AW-SRV-019's rework. |
| #80 Projector targets and Deployment volumes (§9) | architecture | SPRINT-03. Its volume half needs AW-INF-007's snapshot-store decision. |

### Unanswered `docs/feedback/` items

| Item | Owes | Disposition |
|------|------|-------------|
| AW-CLI-004 §2: `perceived_from` renderer row | architecture (via AW-SRV-029's contract change) | Out; SRV-029 isn't in SPRINT-02. PM's answer is appended. |
| AW-CLI-006 §14 item 2: `admin/README.md` docs | implementation | SPRINT-02 carryover. |
| AW-CLI-006 §14 item 3: content-read RPC | architecture, at AW-SRV-013's contract review | Out; M3. Groom it with AW-SRV-013. |
| AW-CLI-007 §3: demo wording and moves | PM, Brian | Answered: the demo uses `look`, and the design question is batched to Brian. |
| AW-CLI-007 §4: `count` on `CreateCharacterResponse` | architecture (optional) | Out; nothing waits on it. |
| AW-SRV-006 §13: AC-3 and AC-8 | implementation | SPRINT-02 carryover. |
| AW-SRV-012: SRV-013 activation refusals, `server info`, prepare-phase observation | PM | Out; M3. Groom them into AW-SRV-013 and AW-CLI-003 when they enter a sprint. |
| AW-SRV-012: Zone deletion | Brian, then PM | Out; SPRINT-02 game-design question 3. |
| AW-SRV-014 §1: bind-applied `info` log | implementation | SPRINT-02 carryover. |
| AW-SRV-014 §3: `already_live` after quit (new) | architecture | Settle it with AW-SRV-015 AC-5 in SPRINT-02's contract review. |
| AW-SRV-015 §1–3: field collision, AC-9's dependency, Event names (new) | architecture | SPRINT-02 contract review, first. |
| AW-SRV-019 §5: SASL story | architecture, then PM | Out; no ADR covers broker authentication, and the question is written. |
| AW-SRV-019 §6: #77 and #80 | architecture | #77 in SPRINT-02, #80 in SPRINT-03. |
| AW-INF-011 §2–3: AC-10's lane and how 25,000 Entities reach a server | architecture | Out; the story also depends on AW-SRV-007 (SPRINT-03). |
