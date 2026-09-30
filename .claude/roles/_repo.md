---
generated: [BACKLOG.md, docs/status.md, gen/, docs/builders/reference.md]
---
Repo-wide role config for Andara's World. `generated` paths are never edited by
hand in any role; they're rebuilt by `make backlog status`, `make proto`, and
`make builder-reference`. A committed generated file belongs to no role: the PR
that changes its source runs the target and commits the output, whatever that
PR's role (CLAUDE.md §2).
Paths not in any role's `writes` need Brian's approval to edit
(e.g. CLAUDE.md, .claude/, go.mod, go.sum, README.md).

Terminology: a session **role** (pm, architecture, sre, implementation) is who
the agent is. A story's `lane:` frontmatter field is which role builds it.
