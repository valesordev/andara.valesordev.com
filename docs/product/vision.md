# Vision — Andara's World

Every line marked `[ASSUMED]` was inferred from the repo, the roadmaps, or Notion canon rather than
found stated. Brian confirms or corrects each one in the PR. Canon, mechanics, and architecture have
owners; this file links to them and doesn't restate them.

## For
- Players who like MUDs: text-first, deep systems, a persistent world other people live in. `[ASSUMED]`
  A player here reads and types rather than watches (the text interface is permanent: ADR-0003).
- Closed at launch, then invite-only beta, then open registration (ADR-0006, Brian's decision). Each
  step widens the audience; none is promised a date. `[ASSUMED]` Brian himself is the first player, and
  the early audience is a handful of invited people he knows, not the public.
- Builders and Operators are users too. Releases are stated in player terms unless they exist only for
  Builders or Operators.
- Not for, yet: anyone who needs a rendered client, a browser, or a mobile app. Phase 2's client waits
  on a frozen protocol (`CLAUDE.md` §1). Players today run a binary (`andara-cli play`), so early
  players are comfortable with a terminal. `[ASSUMED]`

## The experience
`[ASSUMED]` A player creates a Character, enters a frontier settlement, and learns the world by
exploring it, talking to people, and finding things, then comes back to a settlement that has noticed.
The core loop is: go out, find something, come back changed. What the world is, and what "noticed"
means, are canon and mechanics, which Brian owns. See the approved canon under Solo7 › The Hearth ›
Andara's World › World Foundation, and the Content Development Tracker's "Definition of First Playable
Region".

If a player reconnects within the linkdead grace period, their Character is where they left it. After
it, the Character's state is kept but they re-enter the World rather than stay in it (ADR-0006).

## Is / isn't
- Is: a persistent, server-authoritative world. The target is that no action the server acknowledged is
  lost (`docs/specs/slo/recovery.md`, a proposed target, not yet validated).
- Is: text-first. The text interface is permanent (ADR-0003, decided 2026-09-07).
- Is: built by Builders who have no repository access (ADR-0004).
- Is `[ASSUMED]`: a world that explains itself through play (NPCs, rumors, ruins, items), not exposition.
- Isn't (yet): a rendered client or a browser game. Phase 2, gated on Phase 1 exit.
- Isn't (yet): open to the public.
- Isn't, for now: reachable by telnet or other MUD clients. ADR-0003 forecloses them, to be revisited at
  open registration (`docs/roadmap.md`, "What the decisions cost").
- Isn't (yet) `[ASSUMED]`: more than one gameplay loop. Phase 1's loop is combat (Brian, 2026-10-03,
  `docs/roadmap.md` "Resolved on 2026-10-03"). Trade, exploration, and social play aren't in Phase 1.
- Isn't, ever: client-authoritative. The client presents and sends intent.
- Isn't, ever `[ASSUMED]`: a place where a deploy or crash silently loses what the server acknowledged.

## Principles
Ordered. When two conflict, the earlier one wins. `[ASSUMED]` The ordering is mine; the individual
rules come from the repo and canon.
1. **The world is the product.** A smaller world that is persistent, consistent, and reacts beats a
   larger one that doesn't. Depth in what exists comes before breadth.
2. **Players' actions persist.** What a player did survives crashes and deploys. Whether and how others
   see it is a design decision for Brian and the game designer. `[ASSUMED]`
3. **Playable before pretty.** Every release is playable by someone at a terminal. Presentation
   follows a stable, playable world, never leads it.
4. **Canon first.** Content follows approved canon; the plan never invents lore or mechanics. Game
   design and canon are Brian's.
5. **Discovery over exposition.** The world teaches through what players find and who they talk to.
6. **Ship the narrowest thing that proves a bet.** Each release tests one claim about the product.
7. **The operator can run it.** A world nobody can safely deploy, inspect, or recover isn't live.

## Log
- 2026-10-07: first draft. Awaiting Brian's review.
