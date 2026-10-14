# Examples

**Prompt:** "/sprint-run" (solo7.media, SPRINT-01 active)
**Behavior:** `coordinator plan` shows an SRE lane (two drafts need an observability review). It plans a `visual-designer` lane per ready brand story (one per Component) and an inbox lane when a builder has asked it a question, and lists `S7M-VAL-004` as marked blocked (PM). The agent shows the plan, asks "Start these lanes?", and waits. On "yes" it runs `coordinator run --apply` in the background.

**Prompt:** "/sprint-run --roles implementation --max-lanes 2 --yes"
**Behavior:** No confirmation step. Two implementation lanes start for the first two `ready` stories in Rank order that have different Components. Each lane is its own session with `role: implementation`, working one story. The agent reports the table from the run report, checks any PR it names with `gh pr view`, and lists the stories still waiting on architecture's contract review.

**Prompt:** "run the sprint" (every sprint story is at draft)
**Behavior:** `coordinator plan` reports that no implementation lane can start: all stories wait on architecture's contract review, and plans one architecture lane for it (or SRE's lane first, when SRE hasn't reviewed them: architecture then runs on the next /sprint-run). After the owner confirms, the architecture lane readies the stories it can (or marks them blocked) and ends with `LANE RESULT: recorded <n>`. The agent reports which stories are now `ready`. Next action: "Run /sprint-run again: implementation can pick up the stories architecture readied."

**Prompt:** (a lane ended `LANE RESULT: blocked: npx wrangler deploy denied`)
**Behavior:** The report's Needs-you section names the lane and its branch, which has local commits and may have a PR. The agent doesn't run the command for it and doesn't widen the lane's permissions. Next action: "Run it from your own session, or give the repo a script for it (`"deploy": "wrangler deploy"` in `package.json`) and add `Bash(npm run deploy:*)` to the role's `lane_allow` in a reviewed change." (`npx`, `pnpx`, `bunx` and `uvx` entries are refused: they fetch and run any package.)

**Prompt:** (a lane ended with `PR open, Codex pending`)
**Behavior:** The report names the lane and the PR. The agent doesn't call it clean and doesn't start another lane for that story. Next action: `codex-review wait <PR>`, or comment `@codex review`.

**Prompt:** "tell SRE the chart keys are missing before you start the lanes" (dispatcher session, no role)
**Behavior:** The agent doesn't set a role. It writes the note to a scratch file and runs `.claude/bin/coordinator comment GS-SRV-031 --to sre --body-file <file>`, which posts `**For sre:**` stamped `from: coordinator`. SRE's lane sees the question and answers `--to owner`. Then it plans the run as usual.

**Non-trigger:** "Pick up GS-SRV-042 and implement it" → `/impl-start-sprint`, a single role session.
**Non-trigger:** "Close the sprint" → `/pm-close-sprint`.

**Prompt:** (a `visual-designer` lane ended with an open, Codex-clean PR for the illustration system)
**Behavior:** The report counts the lane as done and adds under Needs attention: `<PR> awaits you: the owner approves the audit and the illustration system, selects every candidate, …`. The agent lists the PR for the owner to review, doesn't call the system approved, and doesn't start the art-slot stories that wait on it.

**Prompt:** (two issues labeled `product` are open and the product lane ends `LANE RESULT: triaged 2`)
**Behavior:** The report counts the lane as done only because the coordinator re-read the queue and both issues were commented on or closed. An issue the lane left untouched reports `left untouched: #N` and is not done. The agent lists what is the owner's: issues the lane left open for their call, and the product PR's "Decided this session" list, if it opened one.
