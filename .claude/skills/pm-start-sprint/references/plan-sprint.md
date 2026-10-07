# Plan a sprint (shared by /pm-start-sprint and /pm-close-sprint)

Inputs from the calling skill:

- **NEXT**: the sprint to write, from `.claude/bin/sprint-state` (`next:`)
- **CARRYOVER**: the stories NEXT inherits, with why each one is unfinished
- **DEFECTS**: any `§9 defect → AW-INF-NNN` stories the last demo produced

Never assume NEXT is SPRINT-01, and never reuse a number `sprint-state` didn't give you.

## 1. Demo goal

Choose the milestone gate from `docs/roadmap.md`, or a slice of one, that is
the nearest thing an operator can actually run once CARRYOVER, DEFECTS, and
the fewest new stories land. Build on the last sprint's demo where you can:
read the newest `docs/sprints/SPRINT-*-demo.md`. Say why you chose
it, and say what it is not.

## 2. Groom only what the goal needs

- Pull `ready` stories as they are (`story list --status ready`).
- Draft new stories in full, in the repo's story contract format (repo §4).
  They go through contract review (SRE observability, then architecture)
  first thing in the sprint.
- A story that needs a decision no ADR covers stays out of the sprint, with
  its question posted `--to architecture`.
- Include only stories whose blockers (`story show` → `Blocked by:`) are
  `done`, are ahead of them in the same sprint, or are carried over.

## 3. Order and size

Pickup order in each role's backlog: CARRYOVER first, then DEFECTS, then the
new demoable slice. Keep the sprint small enough to finish: the §8 queue plus
one demoable slice, not everything that's ready.

## 4. Checkpoint: stop before writing

Send Brian one message (the calling skill may add to it) containing:

- NEXT's demo goal, why, and what it is not
- the story list per role (architecture, SRE, implementation), in pickup order
- any game-design questions (up to 3, per repo §6)

Wait for his answer, then write.

## 5. Write

Every step is a board change with `.claude/bin/story`, made after Brian's answer:

1. New stories: write each contract to a file in your scratch directory, then
   `story create --comp <SRV|CLI|INF|CLT> --title … --lane … --size … --risk …
   --component … --body-file <f> --epic EPIC-NN --depends <ID,…>`.
2. The sprint issue, unless the state was `planned`:
   `story sprint plan <n> --title "<goal>" --body-file <f>`, in the role
   file's sprint issue format. Its carryover section lists CARRYOVER with
   reasons; for SPRINT-01 it's the snapshot the calling skill took.
3. Each NEXT story: `story set <ID> Sprint=<n> Plan=Current Rank=<k>`, ranked
   in the order of step 3 above. Stories groomed for later get `Plan=Next` or
   `Plan=Later` and no Sprint. Stories the last sprint left behind that aren't
   carried over get `Plan=Later` and their Sprint cleared (`Sprint=`).
4. `story sprint activate <n>`.
5. Confirm with `.claude/bin/sprint-state`: it must report `state: active`
   and `current: <NEXT>`. Then `story list --sprint <n>` must show every story
   you planned, in order.
