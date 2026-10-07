---
name: release-review
description: Review a release's readiness across every delivery planner's board and repo (each feature's success signal, the milestones and stories delivering it, blockers and who owns them) and recommend go, no-go, or cut, written to docs/product/reviews/ (one file per review). Trigger on "/release-review", "where are we on R1", "are we ready to ship", "what's blocking the release", "go/no-go", and when a planner closes the last milestone a release names. Requires the product role. Not for sprint or cycle status (project-sync, producer-close-cycle), changing release scope (release-plan), or shipping or announcing anything (Brian).
argument-hint: "[R<n>]"
allowed-tools: Bash(.claude/bin/role:*) Bash(.claude/bin/story list:*) Bash(.claude/bin/story show:*) Bash(.claude/bin/sprint-state:*) Bash(gh api:*) Bash(gh issue list:*) Bash(gh pr list:*) Bash(gh run list:*) Bash(git fetch:*) Bash(git log:*)
---

# Release review

Request: `$ARGUMENTS`

## Preflight
0. Run `.claude/bin/role require product ${CLAUDE_SESSION_ID}`. If it fails,
   stop and tell Brian to run `/role product`.
1. `git fetch origin` and work from `origin/main`. Open the pending handoffs
   your role file describes. Pick the release, first match wins:
   1. the one named in the request
   2. the release at `shipping` in `docs/product/releases.md`
   3. the release named by the milestone a planner is working now (the active
      sprint's or cycle's goal), if it's still `committed`; this review moves
      it to `shipping`
   4. the lowest-numbered `committed` release

   If none of these exists, stop and say so.
2. Read the release, every brief it lists, and the sources your role file
   names, for each delivery planner: its roadmap, its board, and its newest
   demo or close-out.

## Evidence rules
Status comes from the boards, merged PRs, demo docs, workflow runs, and
recorded decisions only. Never from a story's prose, a roadmap's intent, or
what should be done by now. Say what each claim rests on. A merged PR whose
story isn't at `review` or later is a status defect: report it to its planner,
don't adjust for it.

## Steps
1. **Per feature:** its success signal and whether it holds now (`holds`,
   `not yet`, `can't tell`), with the evidence. Then the milestones and stories
   delivering it, their state, and each blocker with its owning role.
2. **Per milestone the release names:** the planner's gate and whether it's met.
3. **Gaps:** dependencies with no owner, `content-gap`/`content-need`/`product`
   issues still open against the release, and work in flight that targets no
   release feature.
4. **Recommendation:**
   - `go`: every `must` feature's signal holds
   - `no-go`: name what's missing and the shortest path, with its owners
   - `cut`: drop named `cut-first` features to ship sooner, with what it
     costs the promise

   The ship call itself is Brian's.
5. **Write** `docs/product/reviews/R<n>-<YYYY-MM-DD>-<NN>.md` (format below),
   with `<NN>` one past the highest number for that release and day on
   `origin/main` or an open `product/review-` branch (`01` first), so a second review the same day is a new record. Set the
   release's status if it changed (`committed → shipping` when its first
   milestone starts; `shipped` only on Brian's go, quoting it), and mark briefs
   whose signal now holds `shipped`. Branch `product/review-r<n>-<date>-<NN>`,
   label `role:product`, `/pre-pr`, PR.
6. **Hand off.** A `cut` that Brian accepts goes through `/release-plan`.
   A blocker whose owner needs to change a plan goes under "Release changes
   (after merge)" in the PR body, handed off as your role file describes.
   Otherwise the review is the record.

## Review format
```markdown
# R<n> review — YYYY-MM-DD (NN)
Recommendation: go | no-go | cut — <one line>

## Features
| Feature | Signal | Evidence | Delivered by | Blockers (owner) |

## Milestones
| Milestone | Planner | Gate | Met? | Evidence |

## Gaps and defects

## If no-go: shortest path
1. … (owner)
```

## Output
The recommendation with its one-line reason, the review path and PR URL, and
one next action: Brian's ship decision on `go`, otherwise the top blocker and
the role that pulls it.
