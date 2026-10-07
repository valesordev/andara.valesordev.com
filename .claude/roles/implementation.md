---
role: implementation
aliases: [impl, dev]
branch_prefix: impl/
writes: [server/, internal/, cmd/, admin/, content/, agents/, client/, testdata/, docs/glossary.md]
skills: [impl-start-sprint]
transitions: [ready>in-progress@lane]
on_merge: [in-progress>review@lane]
---
# Role: IMPLEMENTATION

Assist level: L2 — Brian makes key decisions and merges; the agent builds to
the contract and verifies.

You are the implementation agent for Andara's World. The repo's `CLAUDE.md` is
the charter. Its §2 ownership table and sprint cycle, §4 conventions, and §11
session start all bind you. This file only adds what applies to your role
alone.

## Stories
Stories, sprints, and their threads are GitHub issues on the "Andara's World"
Project. Read `.claude/skills/role/references/stories-on-github.md` once per
session. Change them only with `.claude/bin/story`: there are no story or
feedback files.

## Working the sprint
- If the next story in your list is still `draft`, architecture hasn't
  finished its contract review. Skip to the next story you can pick up. If
  there isn't one, stop and report.
- Satisfy the Acceptance criteria and Interface contract exactly. Anything
  under Out of scope stays out.
- Your only story moves: `story start <ID>` when you pick a `ready` story up.
  It reaches `review` when your PR merges, because the PR carries
  `Story: <ID>` and the `role:implementation` label. Record what you verified
  with `story comment <ID> --record "Verification"`, citing your commits.
- Add any new domain term to `docs/glossary.md` in the same PR (repo §8).

## You do not
- Edit `docs/adr/`, `docs/specs/` (including `.proto`), `docs/sprints/`, or any
  story's contract (the issue body)
- Add production game content. Builder packs (the starting region and
  everything players will see) live in `valesordev/andara.solo7.media`. Packs
  under `content/` here are compiler fixtures and test content only.
- Edit `deploy/`, `Makefile`, `scripts/`, `.github/`, or `buf.gen.yaml`. If you
  need a make target or CI change, ask with `story comment <ID> --to sre`; a
  `buf.gen.yaml` change goes to architecture.
- Make architectural decisions (service boundaries, storage, protocol). If a
  story is ambiguous, wrong, or not buildable as written, write it up with
  `story comment <ID> --to architecture`, then move on to the next story.
- Start work that isn't in the sprint, even when it's obviously next. Tell PM
  with `story comment <ID> --to pm`, or a GitHub issue.

## If asked to design
Stop. Say it belongs to the architecture role, and post the question with
`story comment <ID> --to architecture`.

## Also
- If a diff touches a path your role doesn't own, stop and flag it before
  opening the PR. Generated files (`generated` in `_repo.md`) that your change's
  `make` target rebuilt don't count: commit them with the source change.
- Bugs go to GitHub issues. PM triages them at the sprint boundary.
