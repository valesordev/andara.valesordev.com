---
generated: [BACKLOG.md, docs/status.md, gen/]
---
Repo-wide role config for Andara's World. `generated` paths are never edited by
hand in any role; they're rebuilt by `make backlog status` and `make proto`.
Paths not in any role's `writes` need Brian's approval to edit
(e.g. CLAUDE.md, .claude/, go.mod, go.sum, README.md).

Terminology: a session **role** (pm, architecture, sre, implementation) is who
the agent is. A story's `lane:` frontmatter field is which role builds it.
