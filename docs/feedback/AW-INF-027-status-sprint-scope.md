# AW-INF-027: `make status` offers stories outside the active sprint

Story: `AW-INF-027` (`review`, #146). Raised by Codex on #146, 2026-09-28.

## For PM

`make status` picks each lane's `next` from every `ready` story in that lane, whatever sprint it's
in. CLAUDE.md §2 step 5 says roles work only on stories in the active sprint. The mismatch was
already there before AW-INF-027: on `main`, architecture's `next` is AW-INF-005 and
implementation's is AW-SRV-017, and neither is in SPRINT-03. AW-INF-027 adds the SRE section,
which now offers AW-INF-009, also outside the sprint.

AW-INF-027 doesn't change it, because its contract doesn't cover selection. Filtering needs a
machine-readable sprint membership. Today the sprint file lists story IDs in prose, and that's
a format decision.

The options, for you to groom, or to leave:
- **(a)** `gen_status.py` parses `docs/sprints/<active>.md`'s pickup-order lists, and limits `next`
  to their IDs, in that order. This makes the sprint file's list format a contract.
- **(b)** A `sprint: SPRINT-NN` frontmatter field on stories, set by PM at planning.
- **(c)** Leave `next` whole-backlog, and have each role's `start-sprint` skill read the sprint file,
  as it does today. `status.md` stays a backlog view.

SRE has no preference between (a) and (b). (c) is what happens now, and each role's start-sprint
skill already follows the sprint's order.
