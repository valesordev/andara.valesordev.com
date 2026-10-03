# AW-SRV-047, AW-CLI-012, AW-CLI-013: Items

Stories: `AW-CLI-012` (spec, architecture), `AW-CLI-013` (compiler and `andara.core@2`,
implementation), and `AW-SRV-047` (Items in the World, implementation). All three are `draft`.
Raised: 2026-10-03, PM, from Brian's request for an Items story to drive upcoming content.

## Brian's decisions (2026-10-03)
1. **Verbs:** `get`, `drop`, `inventory`, `examine`. Wear and wield come with combat (M4).
2. **Respawn:** a placed Item appears when its pack version is activated. Once taken, it's gone until
   the pack's next activation. Timed resets are a later story.
3. **Limits:** none yet. Carrying is unlimited, there's no stacking, and one placement makes one
   Item Instance.

Brian wants these stories ready for SPRINT-05, but not in it unless content needs them sooner.

## For architecture: the spec (AW-CLI-012)
1. **The names Component:** its name, its fields (a short name, keywords, a description), and
   whether keywords are one string or a list. A list needs a field kind ADR-0010's model doesn't
   have.
2. **The placement declaration:** its keyword, its canonical form, and its field in
   `RoomDefinition`.
3. **Per-Room overrides on a placement** (a "rusty" lantern in one Room). PM proposes none in v1.

## For architecture: the server (AW-SRV-047)
1. **Is an Item Instance an Entity in world state?** The glossary's **Item** entry says an Item
   Instance is "a specific Entity in the World with its own ID and state". `AW-SRV-022` rules that
   "an Item is not an Entity", for Template kinds: `andara.core.Item` is its own root. Decide
   whether an Instance is an `EntityState` with an ITEM Template, or a new `ItemState`, and how a
   carried Item's holder is represented (a Room or a holder Entity ID). PM corrects the glossary to
   match.
2. **A new activation and the previous version's Items.** PM proposes:
   - untaken Instances from the pack's previous placements are removed, and the new version's
     placements are placed;
   - carried Instances stay, with their `content_version`, even if the new version drops their
     Template.

   That's how "taken is gone until the next activation" reads without duplicating untaken Items.
3. **Deterministic Item Instance IDs**, so replay gives the same ID to the same placement in the same
   activation.
4. **A deleted Character's carried Items:** route to `AW-SRV-032` (`ready`), or decide here.
5. **The contract sketch**: the commands, Events, rejection codes and `RoomDescribed.items` in
   `AW-SRV-047`'s Interface contract. They're PM's proposal, for your review.

## For SRE
`AW-SRV-047`'s Observability section proposes `andara_items{location}` (two series) and reuses the
command metrics. `AW-CLI-013` is the first `andara.core` bump, and inherits `AW-INF-021` AC-6.
