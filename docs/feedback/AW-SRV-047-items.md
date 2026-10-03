# AW-SRV-047, AW-CLI-012, AW-CLI-013: Items

Stories: `AW-CLI-012` (spec, architecture), `AW-CLI-013` (compiler, the server's component registry,
and `andara.core@2`, implementation), and `AW-SRV-047` (Items in the World, implementation). All three
are `draft`. Raised: 2026-10-03, PM, from Brian's request for an Items story to drive upcoming content.

## Brian's decisions (2026-10-03)
1. **Verbs:** `get`, `drop`, `inventory`, `examine`. Wear and wield come with combat (M4).
2. **Respawn:** a placed Item appears when its pack version is activated. Once taken, it's gone until
   the pack's next activation. Timed resets are a later story.
3. **Limits:** none yet. Carrying is unlimited, there's no stacking, and one placement makes one
   Item Instance.

Brian wants these stories ready for SPRINT-05, but not in it unless content needs them sooner.

## For architecture: the spec (`AW-CLI-012`'s Open questions, same numbers)
1. **When the corpus cases land, and how the anchors move. This blocks the contract.**
   - Go tests run the formatter over every corpus `.aw` file, `pending/` included
     (`TestContentFmtCheckIsCleanOverTheCorpus`, `corpusSources`). So a placement case fails
     `make check` until the parser accepts it.
   - The first core bump also has to move three roles' files together:
     - the corpus anchors `valid/core` and `valid/town` (architecture's);
     - `content/core/templates`, `testdata/` and their anchor tests (implementation's, listed in
       `AW-CLI-013`'s Scope);
     - `scripts/content_grammar_check.py`'s `KEY_ORDER` for the new `RoomDefinition` key (SRE's).
   - `content-grammar-check` requires every code declared in `errors.md` §3.1–3.4 to be raised by a
     corpus case, so the new codes' rows land with their cases.
   - `perceives` set a precedent: an implementation prerequisite parsed the syntax and dropped it, so
     its pending cases could land.

   Decide the sequence and the PR split. `semantics.md` §9's wording on pending cases ("the story
   that landed it") may need amending for compiler-gated cases.
2. **The names Component:** its name, its fields, its defaults on `andara.core.Item`, and whether
   keywords are one string or a list. A list needs a field kind `ComponentField` (`AW-SRV-021`)
   doesn't have.
3. **The placement declaration:** its keyword and its canonical form.
4. **Per-Room overrides on a placement** (a "rusty" lantern in one Room). PM proposes none in v1.

## For architecture: the server (`AW-SRV-047`'s Open questions, same numbers)
1. **Is an Item Instance an Entity in world state?** Before this PR, the glossary called an Item
   Instance "a specific Entity in the World", and its **Entity** entry listed "an Item instance".
   `AW-SRV-022` rules "an Item is not an Entity", for Template kinds: `andara.core.Item` is its own
   root. This PR marks the glossary entries as open. Decide:
   - whether an Instance is an `EntityState` with an ITEM Template, or a new `ItemState`;
   - how a carried Item's holder is represented;
   - how carried Items travel with their holder in `Arrive` and across a partition handoff;
   - whether `state_version` moves (ADR-0007 rule 2; `AW-SRV-015` added hashed fields without a bump).

   PM corrects the glossary to match.
2. **A new activation and the previous version's Items.** PM proposes:
   - untaken Instances from the pack's previous placements are removed, and the new version's
     placements are placed;
   - carried Instances stay, with their `content_version`, even if the new version drops their
     Template.

   Still open: does a placed Item that was taken and then dropped count as untaken? If it doesn't,
   the next activation duplicates it. And what happens to Items lying in a Room the new content
   removes?
3. **Deterministic Item Instance IDs**, so replay gives the same ID to the same placement in the same
   activation.
4. **A deleted Character's carried Items:** route to `AW-SRV-032` (`ready`), or decide here.
5. **Placements and `ContentDigest`.** Restore rebuilds the digest from the content in effect, and
   replay checks each logged swap's `world_digest`. Do placements enter `CanonicalBytes`? If they do,
   what's the migration that keeps rounds and swaps from before `AW-SRV-047` verifying?

Also: the Interface contract's commands, Events, codes, `ArgWord` and `RoomDescribed.items` are PM's
proposal, for review. The oneof and payload numbers are yours to assign, because `AW-SRV-009` already
claims `LoggedCommand` 20–22 and two payload Events.

## For SRE
- `AW-SRV-047`'s Observability section proposes `andara_items{location}` (two series), and reuses the
  command metrics.
- `AW-CLI-013` is the first `andara.core` bump, and inherits `AW-INF-021` AC-6.
- `scripts/content_grammar_check.py`'s `KEY_ORDER` needs the new `RoomDefinition` key, in whatever
  sequence spec question 1 sets.

## For Brian
- **`i` for `inventory`.** MUDs often use it, but today `i` means `in`, as a unique prefix of the
  Direction. `AW-SRV-047` leaves it as `in` and gives `inventory` the alias `inv`. Say if you want `i`
  for inventory. The Direction keeps `in` either way.
