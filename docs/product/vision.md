# Vision — Andara's World

Every line marked `[ASSUMED]` was inferred from the repo, the roadmaps, or Notion canon rather than
found stated. Brian confirms or corrects each one in the PR.

## For
- Players who like MUDs: text-first, deep systems, a persistent world other people live in. A player
  here reads, types, and thinks; they don't watch. `[ASSUMED]` Brian himself is the first player, and
  the early audience is a handful of invited people he knows, not the public.
- Closed at launch, then invite-only beta, then open registration (ADR-0006, Brian's decision). Each
  step widens the audience; none is promised a date.
- Builders and Operators are users too. They shape and run the world, and a release is stated for
  players unless it exists only to serve them.
- Not for, yet: anyone who needs a rendered client, a web page, or a mobile app. Phase 2's client waits
  on a frozen protocol (`CLAUDE.md` §1). Players today run a binary (`andara-cli play`), so early
  players are comfortable with a terminal. `[ASSUMED]`

## The experience
A player creates a Character and steps into a frontier settlement that exists because travelers need
it: caravans, salvagers back from the badlands, mercenaries, psychics, nonhuman traders. Technology,
magic, and psionics sit side by side and none is a stage of progress. They explore, trade, fight, and
learn what the village is hiding by listening, finding things, and asking the right people. The
village and the world around it keep going while they're away. When they log off and return, the world
is where they left it, and what they did has left a mark. `[ASSUMED]` The core loop is: go out, find
something, come back changed, and have the settlement react.

The first region is meant to be "a small functioning model of Andara" (Content Development Tracker,
"Definition of First Playable Region"), not a starting town.

## Is / isn't
- Is: a persistent, server-authoritative world. Nothing a player was told succeeded is ever lost.
- Is: text-first. The text interface is permanent, not scaffolding (decided 2026-09-07).
- Is: built by Builders without repository access, from a content pipeline that publishes and rolls
  back live.
- Is: a world that explains itself through play (NPCs, rumors, ruins, items), not exposition.
- Isn't (yet): a rendered client. Phase 2, gated on Phase 1 exit.
- Isn't (yet): open to the public, or reachable through telnet or a browser.
- Isn't (yet): more than one region, or more than one gameplay loop. Phase 1's loop is combat (Brian,
  2026-10-03); trade, exploration, and social play follow.
- Isn't, ever: client-authoritative. The client presents and sends intent.
- Isn't, ever: a place where a player's progress can be silently rolled back by a deploy or crash.
  `[ASSUMED]` stated from the zero-RPO target.

## Principles
Ordered. When two conflict, the earlier one wins. `[ASSUMED]` The ordering is mine; the individual
rules come from the repo and canon.
1. **The world is the product.** A smaller world that is persistent, consistent, and reacts beats a
   larger one that doesn't. Playable depth in one region comes before breadth.
2. **Players' actions count.** What a player did persists and is visible to the world and to other
   players. Losing it is worse than any missing feature.
3. **Playable before pretty.** Every release is playable by someone at a terminal. Presentation
   follows a stable, playable world, never leads it.
4. **Canon first.** Content follows approved canon; the plan never invents lore or mechanics. Game
   design and canon are Brian's.
5. **Discovery over exposition.** The world teaches through what players find and who they talk to.
6. **Ship the narrowest thing that proves a bet.** Each release tests one claim about the product.
7. **The operator can run it.** A world nobody can safely deploy, inspect, or recover isn't live.

## Log
- 2026-10-07: first draft. Awaiting Brian's review.
