# SPRINT-01 — M1 in the operator's hands
Status: active
Dates: 2026-09-24 →

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
(filled in by the next PM session)
