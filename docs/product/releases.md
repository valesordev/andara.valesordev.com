# Releases — Andara's World

Today: a player (in practice Brian and CI) runs `andara-cli play`, creates a Character, enters the dev
content's test town (reached from Purgatory), moves between Rooms, sees a bystander arrive and leave, drops the connection and
rebinds within the linkdead grace, and finds the World as it was after a `kill -9` of the server (M1 and M2
gates, passed; `docs/sprints/SPRINT-04-demo.md`, on CI's evidence). A Builder, with test content, can
publish a Zone, see it live and roll it back (M3's gate, SPRINT-03). There are no Items, no NPCs that act,
no combat, and no real authored settlement: the World is dev content. SPRINT-05 is active and has no demo
yet. Sources: that demo, `docs/roadmap.md`, `docs/sprints/`, and the content roadmap's C0 to C3.

Vision: [vision.md](vision.md). Its `[ASSUMED]` lines were merged unconfirmed, so every release below
inherits them: Brian is the first player, and early players use a terminal.

## R1 — The Village *(status: proposed)*
For: Brian, alone. Closed launch (ADR-0006). Nobody else is invited at R1.
Promise: I log in, make a Character, and walk a real settlement of several connected zones whose Rooms
were written for it, not dev placeholders. I see other Characters in the Room with me. When I drop or the
server dies, I come back to where I stood.
Bet: a persistent, authored text world is already a place worth returning to, before it has anything to
fight or collect. If walking it feels empty, the later releases are building on the wrong thing.
Features:
- FEAT-?? — Enter the world as a Character (create, select, play via `andara-cli play`) (must)
- FEAT-01 — Walk a multi-zone settlement (must)
- FEAT-?? — See who else is here (arrivals, departures, linkdead marker) (must)
- FEAT-?? — Come back to the world as I left it (reconnect, crash recovery) (must)
- FEAT-?? — Builders publish a world and roll it back without a deploy (must; Builder-facing, and it is
  how the settlement reaches the server)
- FEAT-?? — Manage my Characters (delete, name retention, switch bodies) (must; in flight as AW-SRV-032,
  inside M2, so cutting it frees no scope)
Cut line: no NPCs that act, no Items, no combat, no second gameplay loop; room text may carry
`TODO(brian)` placeholders where the content roadmap's C1 allows it. Reason: each of those tests a
different bet, and R2 owns the first of them.
Success signals (Brian runs them):
- On `dev`, a Builder publishes the real settlement pack, Brian walks from the settlement's arrival Room (Start Location) to every
  settlement zone by exits alone, and rolls the pack back.
- On `dev` with the real settlement pack loaded, Brian kills the server pod and his Character rebinds
  where it stood (the recovery check on the real pack, not dev content; `AW-INF-034` is the nearest
  target).
- Brian logs two Accounts in on `dev`, and each sees the other arrive, go linkdead and leave.
- Regression guard, true today and not a signal for the bet: `make stack-play`, `make stack-linkdead` and
  `make stack-recover` keep passing on `main`.
- Brian starts a second session within 7 days of his first without being prompted, counted from the
  server's session log, and records in the release review whether he would keep playing. `[ASSUMED]` With
  one player, this is the only honest signal for the bet.
Delivered by: M1, M2, M3 (`docs/roadmap.md`); C0, C1 (content roadmap).

## R2 — The First Fight *(status: proposed)*
For: Brian plus a handful of invited people he knows, who get Operator-created Accounts in ADR-0006's
`closed` mode, so no invite-code work gates R2. Inviting them is Brian's decision at go/no-go, not this
plan's.
Promise: I meet people in the village who do things without a player driving them, pick things up, carry
them, and go and fight something. How the fight ends is up to the game, and the world remembers the
result when I return.
Bet: one complete gameplay loop, combat (Brian, 2026-10-03), is enough to make a small group play together
and want to come back. This is the Phase 1 exit: it is the first thing that tests the game rather than
the server.
Features:
- FEAT-?? — Living NPCs (Builder-written behaviors) (must)
- FEAT-?? — Items I can find, carry and use (must)
- FEAT-?? — The first fight (the combat loop) (must; which outcomes exist, and what attack, damage and
  death mean, are Brian's and the game designer's, not yet decided)
- FEAT-?? — Players can see one another fight and linkdead extends in combat (must; ADR-0006 decides the
  timer, visibility of actions is a design call)
- FEAT-?? — The world stays up for guests (deploys with warning, runbooks, SLOs) (must; Operator-facing,
  exit criteria 5 and 6)
- FEAT-?? — More than the village: surrounding zones (cut-first)
Cut line: no trade, exploration or social loop (decided 2026-10-03); not the full §18 room counts or the
First Playable Region (C3); no open registration; nothing rendered. Reason: one loop is the bet.
Success signals:
- Two invited players are in the village at once; each completes one fight to an outcome the game defines,
  and each logs out and back in with that outcome intact. A demo step an operator runs, then Brian
  watches once with guests.
- An Operator runs `make deploy` during that session and no Character loses state; each player's client
  prints a line on receiving `ServerStopping` at least `deploy.notice_lead` before the stream closes. This
  waits on `AW-INF-007` (the Event is a contract sketch, not built) and on Brian's wording of the message
  (`docs/roadmap.md`, open questions).
- Phase 1 exit criteria 1, 3, 5 and 6 hold on `dev`.
Delivered by: the gameplay half of M4 (`docs/roadmap.md`), C2 (content roadmap). M4's own gate also needs
criterion 2, so M4 closes after R2 ships.

## Not in a release
- **Protocol frozen at v1** (Phase 1 exit criterion 2). No player sees it. It gates Phase 2, not R2, so it
  is Phase 1 exit work and closes M4 after R2. Recommendation to `pm`, who owns the order: sequence it
  after R2's must features so it never delays them.
- **Milestone M0** (scaffold). No player-visible change, it enables every feature. It is done; no release
  names it.
- **C3, First Playable Region.** It begins after R2 ships and gets a release only when Brian adds one.

## Log
- 2026-10-08: first plan. Two releases, both `proposed`. R1 is the walkable authored village; R2 is the
  first combat loop. Splitting Phase 1 exit into two lets R1's bet (a place) be tested before R2's (a
  loop).
