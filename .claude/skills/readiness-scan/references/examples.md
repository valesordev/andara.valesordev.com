# Examples

**Prompt:** (pm role, SPRINT-07 active) "/readiness-scan"
**Behavior:** `role require pm` passes. The agent lists SPRINT-07's ready stories (GS-SRV-061, GS-SRV-062, GS-INF-014) and review stories (GS-SRV-058). It finds:
- GS-SRV-061's contract adds `uint32 region = 7;` to `SessionState`, and `git grep -n "= 7;" origin/main -- proto/session.proto` shows field 7 is already `reserved`. That's architecture's (`role owners proto/session.proto`).
- GS-INF-014's AC 3 runs `make chaos-drill`, which isn't in the Makefile on `origin/main`. GS-INF-015, still `ready`, builds it, so the finding is SRE's.
- `story check GS-SRV-058 done` lists `§8 instrumentation (sre)`.

GS-SRV-062 is clean. The table lists SRE first (1 ready, 1 review), then architecture (1 ready), and says run SRE first: it has the most stories waiting. The owner says go. The agent moves GS-SRV-061 and GS-INF-014 to `blocked` with notes, and posts a `--to architecture` and a `--to sre` comment with the evidence. GS-SRV-058 gets only the comment to SRE. Next action: `/role sre`, then `/sre-start-sprint`.

**Prompt:** (/sprint-run, no role, following this skill with `--report`) "run the sprint"
**Behavior:** The same checks and table, with no writes and no wait. The dispatcher shows it before the lane plan. Because GS-SRV-061 would start an implementation lane, it doesn't start lanes. Next action: `/role pm`, then `/readiness-scan` to mark it.

**Prompt:** (pm role) "which role should I run first?" (every ready story is clean, and one review story is missing architecture's §8)
**Behavior:** The table has one architecture row for the review story. Nothing is marked. Run first: architecture, for the §8 review.

**Non-trigger:** "Plan the next sprint" → `/pm-start-sprint`, which runs this scan at its checkpoint.
**Non-trigger:** "Do the §8 review" → `/arch-start-sprint`.
