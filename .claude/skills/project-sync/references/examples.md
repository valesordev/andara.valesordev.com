# Examples

**Prompt:** "Sync the github project, I want to see where we are."
**Behavior:** Runs `git fetch`, then a dry run: `5 add, 9 close, 70 set field, 16 update`. Brian asked for the sync, so it applies, and the second dry run ends `in sync`. Reports `Current (SPRINT-03): 12/20 done — 2 ready, 6 review, 12 done` and the 8 open stories by rank, with lanes and PRs. Notes that AW-INF-022 and AW-INF-023 are still `ready` on the demo path, and that #143 holds AW-SRV-019 and AW-INF-025, per their feedback files. One next action: "SRE: pull AW-INF-022."

**Prompt:** (pm-close-sprint, preflight) —
**Behavior:** The close-out runs the Sync steps. It applies without asking again, because Brian started the sprint skill. It then runs `--offline` and copies `Current (SPRINT-03): 20/20 done — 20 done` into the close-out. Once Brian merges the PM PR, `/project-sync` is the final sync: the board then shows SPRINT-04 as Current.

**Prompt:** "Is the board up to date?" (the model finds 3 pending changes)
**Behavior:** Shows `3 set field` from the dry run and asks before applying, since Brian asked a question rather than requesting a sync.

**Prompt:** (content repo) "/project-sync"
**Behavior:** `project-mirror: not configured for this repo`. It stops there.

**Non-trigger:** "Move AW-INF-022 to in-progress on the board" → change the story file's `status:` in the owning role's PR. The board follows once that PR merges.
