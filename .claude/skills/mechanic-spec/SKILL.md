---
name: mechanic-spec
description: Write the formal server-facing spec for an Andara's World mechanic from a mechanic brief whose decisions Brian has made, under docs/mechanics/ with rules, tunables, and testable given/when/then scenarios that the server PM and architecture can cut into stories. Trigger on "/mechanic-spec nomic-strain", "write the requirements for the strain mechanic", "spec this for the server", "turn the brief into scenarios". Requires the server repo and the game-designer role. Not for exploring options or teaching theory (mechanic-brief), balance numbers (balance-model), cutting stories (pm), engine design (architecture), or player-facing text.
disable-model-invocation: true
allowed-tools: Bash(.claude/bin/role:*) Bash(gh api:*) Bash(git fetch:*)
---

# Mechanic spec

## Preflight
0. Run `.claude/bin/role require game-designer ${CLAUDE_SESSION_ID}`. If it fails, stop and tell Brian to run `/role game-designer`.
1. Confirm this is the server repo: `docs/mechanics/` is writable by the role. In the content repo, stop: finish the brief, then run this in a server session.
2. `git fetch origin`; work from `origin/main`.
3. Read `references/canon-rules.md`, `references/template.md`, and the existing spec for this mechanic, if any.

## Inputs
- A brief at `Status: proposed` or later in Notion, with **every decision marked decided by Brian**. If any decision is open, stop and list it. A spec is not a way to decide for him.
- The approved canon passage the brief cites.
- Feasibility sources: CLAUDE.md, ADRs, `docs/specs/`, `docs/roadmap.md`, `docs/builders/reference.md`.

## Steps
1. **Restate** the design intent and the canon source (page, version).
2. **Model the state.** Entities, the state each holds, and ranges. Name each rule's trigger.
3. **Rules.** One numbered rule per behavior, each stating trigger, effect, and order of evaluation when rules interact. No prose that two engineers could read differently.
4. **Tunables.** A table: name, starting value `[PROPOSED]`, range, the observation that says to move it. Rules refer to tunables by name, never by literal.
5. **Scenarios.** For each rule, given/when/then with exact values (using starting tunables). Cover: the normal case, each boundary (zero, cap, exactly-at-threshold), each failure case, and at least one multi-actor case. Give each scenario an ID (`<MECH>-S01`); stories cite them as acceptance criteria.
6. **Feasibility.** For each rule, `exists`, `needs engine work` (link the ADR or file an architecture question), or `needs Content Language work` (note it in the spec's gaps section as a `content-gap` candidate).
7. **Out of scope.** What the mechanic deliberately doesn't do.
8. **Write** `docs/mechanics/<name>.md` with `Status: proposed`. Never "approved"; Brian sets it, and the spec quotes his confirmation. Branch `game-designer/<name>`, label the PR `role:game-designer`; run `/pre-pr` before pushing.

## Output
The spec path and PR URL; counts of rules, tunables, scenarios, and gaps; which sections are independently shippable (for the PM); canon questions routed. One next action.
