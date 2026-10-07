---
generated: [gen/, docs/builders/reference.md]
check: make check
project: Andara's World
story_prefix: AW
github_repo: valesordev/andara.valesordev.com
stories: github
sprints: project
---
Repo-wide role config for Andara's World. `generated` paths are never edited by
hand in any role; they're rebuilt by `make proto` and `make builder-reference`.
A committed generated file belongs to no role: the PR that changes its source
runs the target and commits the output, whatever that PR's role (CLAUDE.md §2).
Paths not in any role's `writes` need Brian's approval to edit
(e.g. CLAUDE.md, .claude/, go.mod, go.sum, README.md).

Terminology: a session **role** (pm, architecture, sre, implementation) is who
the agent is. A story's **Lane** (a Project field) is which role builds it.

## Stories and sprints
Stories (`AW-<comp>-NNN`), epics (`EPIC-NN`), and sprints (`SPRINT-NN — <goal>`)
are issues in this repo on the shared "Andara's World" org Project, next to the
content repo's `AWC-*` stories. GitHub is their source of truth since the
cutover at the SPRINT-05 boundary: `docs/stories/` and `docs/epics/` are gone
(the last commit with them is tagged `stories-final`), and nothing mirrors into
the board. Change stories and sprints only with `.claude/bin/story`; the
protocol is `.claude/skills/role/references/stories-on-github.md`.

- `docs/feedback/` is a frozen archive. Each open story that had a feedback
  file links it in a `Feedback (cutover)` comment. New feedback is a story
  comment (`story comment <ID> --to <role>`); don't add or edit files there.
- `docs/sprints/SPRINT-01.md` … `SPRINT-04.md` are those sprints' plan and
  close-out records. From SPRINT-05 on, a sprint's plan is its issue plus the
  board (Sprint, Plan, Rank), and its record is `SPRINT-NN-closeout.md` and
  `SPRINT-NN-demo.md`.
