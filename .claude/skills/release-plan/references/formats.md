# Product doc formats

## docs/product/vision.md

```markdown
# Vision — <product>

## For
Who it's for, and who it isn't for yet.

## The experience
What a session feels like for them, in a paragraph. The core loop.

## Is / isn't
- Is: …
- Isn't: … (what it will never be, and what it won't be yet)

## Principles
Ordered tie-breakers for product calls. e.g. "A smaller world that reacts beats a
larger one that doesn't."

## Log
- YYYY-MM-DD: <change> (approved by Brian in PR #NN)
```

## docs/product/releases.md

```markdown
# Releases — <product>

Today: <what players can do now, one paragraph, with the demo it comes from>.

## R<n> — <name> *(status: proposed | committed | shipping | shipped)*
For: <audience>
Promise: <what they can do, in their words>
Bet: <what this release tests about the product>
Features:
- FEAT-NN — <name> (must)
- FEAT-?? — <name> (cut-first)   # no brief yet
Cut line: what it deliberately leaves out, and why
Success signals: observable checks, and who runs them
Delivered by: filled in from the planners' roadmaps (M<n>, C<n>); never planned here

## Log
- YYYY-MM-DD: <change and why>
```

Statuses: `proposed` (in the plan), `committed` (a planner milestone targets it),
`shipping` (its milestones are in progress), `shipped` (Brian's go, after a
`/release-review`). Exactly one release is ever `shipping`.

`Delivered by` repeats what the roadmaps say so a reader sees it in one place.
If it disagrees with them, the roadmaps are right and this line is stale.
