# Examples

**Prompt:** "/product-triage"
**Behavior:** Requires the product role. Lists the open `product` issues, decides each one (answer and close, fold into a brief on a `product/` branch, or close with the reason) and comments on every one. Checks merged `role:product` PRs for "Release changes (after merge)" sections and opens the missing `release-change` issues. If `docs/product/` changed, opens one PR with a "Decided this session" list and runs it through `/pre-pr`. Ends with a `LANE RESULT:` line.

**Prompt:** (the sprint coordinator starts the product lane with two open `product` issues)
**Behavior:** Same flow, headless. Both issues get a comment. The lane never merges and never touches paths outside `docs/product/`. If a call is the owner's (a release added or dropped), it leaves the issue open with one recommendation.

**Prompt:** "write the brief for the trading feature"
**Behavior:** Non-trigger. That is `/feature-brief`.

**Non-trigger:** "are we ready to ship R1?" → `/release-review`.
