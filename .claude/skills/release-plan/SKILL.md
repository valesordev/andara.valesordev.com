---
name: release-plan
description: Define or re-cut the product's releases in docs/product/releases.md (each release's promise, audience, bet, features, cut line, success signals), drafting docs/product/vision.md first if it doesn't exist, then check that the delivery planners' milestones each target a release and hand the gaps to the planners once the plan merges. Trigger on "/release-plan", "what's in the first release", "plan the releases", "re-cut R2", "what comes after phase 1", "does the roadmap still serve the players". Requires the product role. Not for a single feature's definition (feature-brief), readiness of a release in progress (release-review), or sprint and cycle planning (pm, producer).
argument-hint: "[R<n> | change]"
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*) Bash(gh api:*) Bash(gh issue list:*) Bash(gh pr list:*) Bash(git fetch:*)
---

# Release plan

Request: `$ARGUMENTS`

## Preflight
0. Run `.claude/bin/role require product ${CLAUDE_SESSION_ID}`. If it fails,
   stop and tell Brian to run `/role product`.
1. `git fetch origin` and work from `origin/main`. Read everything in
   `docs/product/`, then the sources in the order your role file lists them,
   including every delivery planner's roadmap.
2. Read `references/formats.md`. Triage open `product` issues
   (`gh issue list --label product`), and open the pending handoffs your role
   file describes.

## Vision (when `docs/product/vision.md` doesn't exist)
Draft it from what's already decided: the repo's `CLAUDE.md`, roadmaps, and the
Notion pages your role file names. Mark every claim you inferred rather than
found `[ASSUMED]`. The vision is Brian's call, so it goes in its own PR, ahead
of the releases, and the checkpoint below asks him to confirm or correct each
`[ASSUMED]` line. Don't plan releases on an unmerged vision; stop after that PR.

## Steps
1. **Where players are today.** From the newest demo and the boards: what a
   player can actually do now. One short paragraph, with sources.
2. **Releases.** Two, or at most three, ahead of today. For each: the audience,
   the promise (what they can do), the bet it tests, the features (`FEAT-NN`,
   `must` or `cut-first`), the cut line, success signals. Existing releases keep
   their numbers; a re-cut changes the entry and logs why.
3. **Features without briefs.** List them as `FEAT-?? — <name>` under the release,
   so `/feature-brief` picks them up. Don't write briefs here.
4. **Coverage.** Build the mapping your role file's delivery planners need to
   act on:
   - each planner milestone and the release it names (`Release: R<n>`); flag
     a milestone that names none, or a release that doesn't exist
   - each `must` feature and the milestones or stories that deliver it; flag a
     feature nothing delivers
   - each milestone with no player-visible change and no feature it enables;
     recommend dropping or re-homing it
5. **Decide.** Make the calls that are yours (scope, order, cut lines) with a
   one-line reason each. Brian's calls (vision, adding or dropping a release,
   anything outward-facing) go to the checkpoint with a recommendation.
6. **Checkpoint, only if a call is Brian's.** One message: each question, your
   recommendation, and what you'll do if he says no. Wait for his answer.
   Otherwise go straight to writing; he reviews the PR.
7. **Write** `releases.md`. Branch `product/release-plan-<slug>`, label
   `role:product`, run `/pre-pr`, open the PR with "Decided this session" and
   the coverage findings in the body.
8. **Hand off after merge.** Put every coverage finding a planner has to act
   on under "Release changes (after merge)" in the PR body: per planner, the
   milestone, the release and feature, and the change you're asking for. Open
   no issues now. Once Brian merges the PR, the next product session opens one
   `release-change` issue per planner from that section (your role file's
   pending handoffs), or this one does if he merges while it's still running.

## Output
The releases (one line each), the PR URL, the release changes waiting on
merge, decisions made, and anything waiting on Brian. One next action, usually
Brian's review of the PR.
