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
read the newest `docs/sprints/SPRINT-*-demo.md` (for SPRINT-01 there is none;
the existing M1 gate, `make stack-play`, is the baseline). Say why you chose
it, and say what it is not.

## 2. Groom only what the goal needs

- Pull `ready` stories as they are.
- Write new stories in full at `draft` and list them under "Contract review"
  (SRE observability, then architecture).
- A story that needs a decision no ADR covers stays out of the sprint, with
  its question in `docs/feedback/`.
- Include only stories whose `depends_on` are `done`, are ahead of them in the
  same sprint, or are carried over.

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

- `docs/sprints/<NEXT>.md` in the role file's sprint format, with
  `Status: active` and today's date on the `Dates:` line. Its "Carryover from
  <previous>" section lists CARRYOVER with reasons. For SPRINT-01 it's the
  snapshot the calling skill took.
- Run `make backlog status check`. Fix what it reports in files you own. If it
  fails on something you don't own, report it and don't touch it.
- After committing, confirm with `.claude/bin/sprint-state --ref HEAD`: it must
  report `state: active` and `current: <NEXT>`.
