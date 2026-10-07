# Stories on GitHub

This applies to a repo whose `.claude/roles/_repo.md` has `stories: github` and
`sprints: project`. Stories, epics, and sprints are issues on the org Project
named by `project:`. Change them only with `.claude/bin/story`. Raw `gh issue` /
`gh project` / `gh api` writes to them are denied in role sessions, and there
are no story files to edit.

## Where things live

| Thing | Where | Who changes it |
|---|---|---|
| Story contract | the issue body (`story show <ID>`) | PM writes it at `draft`; architecture amends it in contract review (`story body`); SRE amends only its Observability requirements section |
| Status | the Project's Status field | per role `transitions:`; `in-progress → review` only by the story-merge Action |
| Sprint, Plan, Rank | Project fields | PM (`story set <ID> Sprint=N Plan=Current Rank=k`) |
| Dependencies | GitHub blocked-by links (`story show` prints `Blocked by:`) | PM at `story create --depends` |
| Feedback and questions | comments on the story: `story comment <ID> --to <role> --body …` | any role |
| Records (§8, observability review, verification) | comments: `story comment <ID> --record "<name>" --body …` | the role that did the work |
| Sprint goal and demo goal | the sprint issue `SPRINT-NN — <goal>` | PM (`story sprint plan/activate/close/body`) |
| Close-out and demo | `docs/sprints/SPRINT-NN-closeout.md`, `docs/sprints/SPRINT-NN-demo.md` | PM, by PR |
| New work, bugs | a plain GitHub issue (not a story); PM triages it | any role |

## Reading the sprint

- `.claude/bin/sprint-state`: `state`, `current`, `next` from the sprint issues.
- `.claude/bin/story list --sprint <n> --lane <lane>`: a lane's backlog, in
  pickup order (Rank).
- `.claude/bin/story list --sprint <n> --status draft`: the contract-review list.
- `.claude/bin/story list --status review`: the §8 queue.
- `.claude/bin/story show <ID> --comments 20`: the contract, blockers, and
  the feedback thread.

## A story's life

1. **draft**: PM creates it (`story create … --depends …`), with Sprint, Plan,
   and Rank.
2. **Observability review**: SRE checks the contract's Observability
   requirements, amends that section if needed, and records
   `--record "Observability review"`.
3. **Contract review**: architecture amends the body, then moves it with
   `story status <ID> ready` (or `blocked --note <what it waits on>`), once SRE's
   record is there.
4. **in-progress**: the lane runs `story start <ID>` when it picks the story up.
5. **review**: the lane's PR carries `Story: <ID>` (body or commit trailer) and
   its `role:<lane>` label. When it merges into `main`, the story-merge Action
   moves the story. A lane never moves its own story to `review`.
6. **§8**: SRE verifies instrumentation, `--record "§8 instrumentation"`.
   Architecture runs the rest, `--record "§8"`, then `story status <ID> done`.
   A §8 PR that also fixes something can carry `Story-Done: <ID>` instead.

A story that can't be built as written gets a comment `--to architecture`,
and the lane moves on. A question for PM, or work that isn't in the sprint,
gets a comment `--to pm` on the related story, or a plain issue.
