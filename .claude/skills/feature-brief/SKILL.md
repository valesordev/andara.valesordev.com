---
name: feature-brief
description: Write or revise a product feature brief (docs/product/features/FEAT-NN-slug.md) that states the problem, the player-visible outcome, scope and non-scope, a success signal, the release it belongs to, and what it needs from design, content, and architecture, so delivery planners can cut stories from it. Trigger on "/feature-brief", "write up a feature for…", "brief the trading feature", "what should party play look like as a feature", "revise FEAT-03". Requires the product role. Not for release scope or ordering (release-plan), readiness (release-review), mechanics and tuning (game-designer), or cutting stories (pm, producer).
argument-hint: "[FEAT-NN | idea]"
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*) Bash(gh api:*) Bash(gh issue list:*) Bash(git fetch:*)
---

# Feature brief

Request: `$ARGUMENTS`

## Preflight
0. Run `.claude/bin/role require product ${CLAUDE_SESSION_ID}`. If it fails,
   stop and tell Brian to run `/role product`.
1. `git fetch origin` and work from `origin/main`. Read `docs/product/vision.md`
   and `docs/product/releases.md`. If the vision doesn't exist, stop: run
   `/release-plan` first, which drafts it.
2. Read `references/template.md`, and the existing brief if the request names
   one. Pick the next number from `docs/product/features/` for a new brief.
3. Triage open `product` issues (`gh issue list --label product`) that touch
   this feature. Fold them in, and list them in the brief's Sources.

## Steps
1. **Problem.** Who has it and what they can't do today. Check today against
   the newest demo and the boards (the sources your role file lists), not
   against the roadmap's intent.
2. **Outcome.** What the player can do afterwards, in their words. One to
   three sentences. If it needs "and also", split it into two features.
3. **Success signal.** Observable: a demo step, a player action, a number
   with a threshold. Say how it's checked and by whom.
4. **Scope and non-scope.** In: the capabilities the outcome needs, and no
   more. Out: the obvious neighbors you're deliberately leaving, each with a
   reason ("later release", "not this audience").
5. **Release.** The `R<n>` it belongs to, and whether the release's promise
   depends on it (`must`) or can ship without it (`cut-first`).
6. **Dependencies.** Everything the outcome needs that another role owns
   (your role file's Handoffs): design, content, domain or world rules,
   engine and protocol. For each, the owner and whether it exists, linked
   (a spec, ADR, story, or Notion page) or marked `needs <role>`. Link;
   don't restate.
7. **Decide.** Make the product calls the brief needs yourself. Record each
   under Decisions with a one-line reason. Anything that's Brian's per your
   role file goes under Open for Brian, with your recommendation.
8. **Write** the brief with `Status: proposed` (a new brief) or the change
   noted in its log. Update `releases.md` if you added the feature to a
   release. Branch `product/feat-NN-<slug>`, label the PR `role:product`, run
   `/pre-pr` before pushing. The PR body lists "Decided this session".

Statuses: `proposed` (in a PR or merged, not yet planned) → `planned` (a
delivery planner cut work for it) → `shipped` (its success signal held, per a
release review) or `cut` (with the reason). Brian approves by merging, so a
merged `proposed` brief is approved for planning.

## Output
The brief path and PR URL, the release and must/cut-first, decisions made,
anything open for Brian, and dependencies with no owner yet. One next action,
usually "pm: cut stories for FEAT-NN" or the design or canon gap that blocks it.
