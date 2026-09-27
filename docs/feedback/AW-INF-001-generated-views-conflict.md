# AW-INF-001: committed generated views conflict on every status move

Spec: `docs/stories/AW-INF-001-repo-scaffold-and-automation-surface.md`, which owns `make backlog`
and `make status`.
Raised: 2026-09-26, architecture, at Brian's request, after resolving the same conflict on #112
three times in one session.

## For PM: a story for the next sprint plan

### The problem

`BACKLOG.md` and `docs/status.md` are generated from story frontmatter and committed. The charter
(§6 step 6, §11) says any commit that changes frontmatter regenerates both in the same commit, and
`make check` fails if either is stale.

Both files hold whole-backlog aggregates:
- the Summary counts;
- the "Attention first" table;
- `status.md`'s header line, and each lane's `now` / `next` / `review` / `held` lines.

So any two PRs that move any story's status edit the same lines, even when they touch different
stories. Nearly every PR moves a status: implementation's `ready` → `in-progress` → `review`, and
architecture's §8 flips. Whichever PR merges first puts every other open one into conflict.

### What it cost on 2026-09-26

- **The rebases.** #110, #111 and #112 each needed a rebase after every sibling merged: #112
  three times, #111 twice. #114 and #115 on the implementation lane are in the same state. Each
  time, the only conflicts were in these two files.
- **The CI.** Every rebase is a force-push, and every force-push reruns CI. That's about 15
  minutes of `check`, `determinism` and `stack` per PR, which then waits on the next merge.
- **The churn.** Since 2026-09-20, 43 commits have touched these two files, against 67 merges.
- **The risk.** The fix is mechanical (take either side, rerun the targets, per §11), but every
  rebase is a chance to get it wrong. One also rebased a story record into a stale state and
  needed a hand correction (#110's SRV-019 text, after #108 merged).

### What the story should decide

Architecture recommends **(a)**. It's a charter change, so it's Brian's call through PM.

- **(a) Generate, don't commit.** Git-ignore both files.
  - `make backlog` / `make status` print to the terminal and write the file locally.
  - CI writes both to the `check` job's summary on `main`, so they stay one click away on GitHub.
  - The frontmatter stays the record, and `make validate-stories` still gates it.
  - Session start (§11) runs `make status` instead of reading the file.
  - Cost: amending charter §3, §6 step 6 and §11, and the lane `CLAUDE.md`s, which name the
    files. `status.md` is no longer browsable in the repo at a pinned commit, though
    `git checkout <sha> && make status` reproduces it, because the generator is deterministic.
- **(b) Keep them committed, but only `main` regenerates them.** Drop `backlog-check` and
  `status-check` from PR CI, and add a post-merge workflow that regenerates and commits. That's
  an automated writer on a signed, branch-protected `main`: it needs a bot key in
  `.github/allowed_signers` and a protection exception. It also reintroduces the
  write-on-merge loop `AW-INF-019`'s contract review steered away from.
- **(c) Keep everything, add a git merge driver** (`.gitattributes` `merge=regen`, installed by
  `make bootstrap`) that regenerates on conflict. It only helps a local rebase. GitHub still marks
  the PR `DIRTY`, so every PR still needs a push after each sibling merge. It removes the hand step,
  but not the cascade.

### Suggested acceptance criteria for (a)

1. **Given** two PRs that each move a different story's status **when** one merges **then** the
   other still merges without a conflict and without a push.
2. **Given** a clean checkout **when** `make status` runs **then** it prints the same content
   `docs/status.md` holds today, byte for byte, for the same frontmatter.
3. **Given** a merge to `main` **when** `ci.yaml` runs **then** the `check` job's summary holds
   the generated backlog and status.
4. **Given** frontmatter that fails `validate-stories` **when** `make check` runs **then** it
   still fails. Only the staleness checks go.
5. **The charter and lane files no longer tell anyone to commit the generated files**, and §11's
   rebase note is removed with them.

Lane: architecture (`Makefile`, `scripts/`, `.github/`), with PM or Brian amending the charter.
Size S.

## Brian's answer (2026-09-27): option (a)

Approved: generate the views and don't commit them. PM groomed it as `AW-INF-026`, first in
SPRINT-03's architecture backlog.
