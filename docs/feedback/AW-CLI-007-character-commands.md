# AW-CLI-007: character commands and `play --character`, handoff and deviations

Spec: `docs/stories/AW-CLI-007-character-commands-and-play-character.md`
Branch: `impl/aw-cli-007-character-commands`
Raised: 2026-09-24, implementation lane.

The code is complete against the Interface contract, and every AC has a test over the test
gateway. Four items belong to other agents. §1 is the story's Definition of done, and the `stack`
workflow is red until it lands.

---

## 1. Architecture: `scripts/stack_play.sh` must change in step with this PR (blocks DoD; `stack` is red until then)

The Definition of done says to extend `scripts/stack_play.sh` into the full M1 gate. `scripts/` is
architecture's (CLAUDE.md §2), so this branch does not touch it. The script's **play half fails
against this branch as it stands.** It runs `bin/andara-cli play` as `operator`, an Account with no
Character, and AC-6 now makes that exit 2 with `no_character` where the script expects 0 and the
"you are not in the world" refusal. `stack.yaml` runs on `admin/**`, so this PR's `stack` job fails
at `make stack-play`. Required status checks are not enabled on `main`, so it doesn't block the
merge, but the two changes should land together or back to back.

What the play half needs, all of it product commands:

1. **Accounts.** Create two player Accounts with random suffixes, as the Protocol half does. Don't
   use `operator`: names are reserved forever and a roster holds five, so a Character on the
   operator Account runs out on a long-lived stack. Names must match `^[\p{L}][\p{L}' -]{2,23}$`,
   so the suffix must be **letters only**. A hex suffix is refused `name_invalid`, which is how the
   live run below found it.
2. **Create.** `andara-cli character create <A>` prints `<A> (1 of 5)` and exits 0.
   `character list` prints `<A>  dormant  town/plaza`.
3. **Second client.** B runs `play --character <B>` on a held-open FIFO, as the restart section
   already does.
4. **The walk.** Pipe `north` and `look` into `play --character <a>` (lower case: resolution
   ignores case). Assert:
   - a connection notice matching
     `^-- Connected to [^ ]\+ as <account>, playing <A> (session [0-9a-f]\{32\}, protocol 1)\.$`.
     **This format changed**: the old regex (`as operator (session …`) no longer matches.
   - `» SelectCharacter session_id=… character_id=…` before `» Subscribe` under `--show-protocol`.
   - the plaza (`Market Plaza`), then `<A> leaves north.`, then the second Room from the `look`.
     The server sends no `RoomDescribed` after a move (see §3), so the second Room needs a `look`.
5. **B's view.** B's transcript shows `<A> arrives.` and `<A> leaves north.`
6. **Launch `already_live`.** `play --character <B>` from a second shell while B is live exits 1,
   and stderr is `a character is already live on this account: <B>`.
7. **Restart section.** It needs `--character` and an Account with one Character (or AC-5's
   single-Character default). AC-8's re-selection is then what gets exercised there. The existing
   asserts still hold: two `Connected` lines, and a `look` on the new Session.
8. **Counters.** The `rejected_authz` +3 assertion no longer holds. With a bound Character, the
   Intents are accepted, and a bad exit is a post-log `CommandRejected`, not an authz refusal.
   `rejected_parse` for `frobnicate` is unchanged.

The live run in the story's verification record did steps 1–6 by hand against the compose stack.
A scratch copy of that sequence can be handed over on request. It isn't committed, because
`scripts/` isn't this lane's.

## 2. Architecture: AC-4 can't hold as worded against the real server (contract correction)

*Update:* #68 (merged) amended the `Here:` half the same way, and the implementation already
matches it. What #68 doesn't cover is the second bullet below: the
Character's own arrival can precede the Room, so "the first thing the player reads is the Room"
still doesn't hold on the live stack.

AC-4: "…and the first thing the player reads is the Room with `Here: Aldric`." Neither half holds
against the server as built:

- **`Here: Aldric`.** `server/sim/verbs.go` `describe` leaves the viewer out of its own
  description ("the viewer is not an occupant of its own description"). Aldric's `look` never
  lists Aldric. `Here:` names *other* occupants, which is what `AW-SRV-014`'s own test plan asserts
  ("`look` with `Here: Brin`").
- **"The first thing the player reads."** The Scope says the arrival Event is "rendered like any
  other". The `BindCharacter` and the automatic `look` are consecutive in one partition and were
  applied **in the same tick** in the live run (tick 12926). The arrival is routed to the Session
  at bind time (`egress.Rebind`), so the player reads `Aldric arrives.` and *then* the Room. Whether
  the arrival lands on the stream depends on `Subscribe` beating the tick, so the order isn't even
  stable.

The implementation does what the Scope says. The arrival renders like any other, and the Room is
the automatic `look`'s answer. The test over the fake gateway asserts the order `SelectCharacter` <
`Subscribe` < `Submit look`, that the ack never reaches the player's transcript, and that the Room
comes before any other game output **when no arrival precedes it**. It does not fake a self-listing
`Here:`.

Proposed amendment, architecture's call: "…the ack is shown only under protocol visibility, and
the automatic `look`'s answer is the Room, with `Here:` naming the Room's other occupants; the
Character's own arrival may precede it." The alternative is for the server not to deliver a
Character's bind arrival to its own Session. That's a perception rule and belongs to `AW-SRV-014`,
not to the client. Suppressing it client-side would be the client deciding what a player
perceives, which CLAUDE.md §1 rules out.

## 3. PM: the sprint demo's "types `north`, and reads a different Room"

`SPRINT-01.md`'s demo goal reads "…reads a Room, types `north`, and reads a different Room." A move
emits `CharacterLeft` and `CharacterArrived`, not a `RoomDescribed`. The mover reads `<A> leaves
north.` / `<A> arrives from the south.` and sees the new Room only after typing `look`. The M1 gate
in `internal/smoke` does the same (`look` after `north`). The demo steps need `north` then `look`,
unless a move is meant to describe the destination. That would be a game-design call and a
server story, not this one.

Also a renderer point for PM, with no action in this story: the mover's own departure and arrival
render in third person (`Implaevludn leaves north.`), because `AW-CLI-004`'s renderer is pure and
the Event names the Character. A second-person rendering for the viewer's own Character would need
the client to know which name is "you". `play` now knows that, so it's a small later story if Brian
wants it.

## 4. Architecture (optional): `CreateCharacterResponse` has the cap but not the count

AC-1's `Aldric (1 of 5)` needs the roster's size. `CreateCharacterResponse` carries
`max_per_account` only. `character create` therefore calls `ListCharacters` before
`CreateCharacter` in the same Session, and reports the listed count plus one. That's one RPC more
than the contract's "call the RPC". It's harmless, and if listing fails the command stops before
creating anything. A concurrent create from another Session of the same Account could make the
count off by one. A `count` field on `CreateCharacterResponse` would remove both the extra RPC and
the race. That's a protocol change, so it's architecture's to decide, and nothing here waits on it.

---

## 5. Decisions made within the contract (for the §8 review)

- **`[ASSUMPTION]` resolved as written.** `--character` is the display name, matched with
  `strings.EqualFold` against `ListCharacters`. The server's NFKC fold covers more than case,
  but a name the server reserves is the one it returns, so an exact or case-varied spelling
  resolves. A `character_id` is never typed.
- **`--character` naming no Character** exits 1 with `no_such_character`, the server's reason code
  from the contract's list, and names `andara-cli character list`. The client decides it from the
  list, because `SelectCharacter` is never called without an ID.
- **Deleted Characters** (`CHARACTER_STATUS_DELETED`) are left out of `list` and of resolution, so
  `character delete` (`AW-SRV-032`) needs no client change.
- **An empty `list`** prints a one-line hint in human mode (`No characters yet; create one with …`).
  JSON is `{"characters": [], …}`.
- **The connection notice** now comes after the selection, so it can name the Character:
  `-- Connected to <server> as <you>, playing <name> (session …, protocol 1).`
- **Reconnect.** A Session opened on reconnect is never subscribed until its selection succeeds.
  The reconnect loop now retries `OpenSession` → `SelectCharacter` as a unit. Before this, a
  failed reconnect fell through to a `Subscribe` on the old Session, which was harmless when there
  was nothing to select and would now subscribe an unbound Session. An `UNAUTHENTICATED` on the
  re-selection (the new Session gone too) is a connection loss and retried, not the
  expired-credential exit.

## PM, 2026-09-25 (SPRINT-01 close-out)

On §3: `docs/sprints/SPRINT-01-demo.md` uses `north` then `look`, which is what the server does.
Whether a move should describe the destination is batched to Brian as a SPRINT-02 game-design
question. The second-person rendering is parked behind that, in `AW-CLI-008`'s out-of-scope list.

## Architecture, 2026-09-26 (§8 pass)

- **§1 (`stack_play.sh`):** landed in `d36a914` and `8b86212`. The transitional pre-007 path is
  removed in this pass, as its comment promised for the first architecture PR after #72. The
  `rejected_authz` count is now asserted *unchanged* for a bound Character instead of being dropped.
- **§2 (AC-4 arrival order):** settled by the #72 amendment ("Aldric's own arrival may be read just
  before it"). No further change.
- **§4 (`count` on `CreateCharacterResponse`): declined for now.** The extra `ListCharacters` costs
  one RPC on a command a player runs about five times in the life of an Account. The race it opens
  is a wrong number in one line of human output, and it never refuses or creates anything wrongly:
  the cap is enforced server-side. A field added to the Protocol stays forever (ADR-0007), so it
  should wait for a consumer that needs the count to be exact. The Phase 2 client's roster screen is
  the likely one.
