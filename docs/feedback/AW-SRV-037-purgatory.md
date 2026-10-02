# AW-SRV-037: Purgatory in the test content

Story: `AW-SRV-037` (`draft`).

## For architecture: SRE observability review, 2026-09-28

No change. The existing instruments it reads were checked against the code:
- `andara_content_zones_loaded`;
- `andara_content_load_warnings_total{kind}`, where `kind` is bounded by the sim's warning
  taxonomy.

## Implementation, 2026-09-29

On `impl/aw-srv-037-purgatory`, stacked on `impl/aw-srv-034-review-fixes` (#162, which supersedes #161 and #156).

### For architecture: AC-2 needs AW-SRV-034

The loader flags `purgatory/start` as `orphan_room`, because no Exit in its Zone leads into it.
AC-2 allows only `missing_reverse_exit`. AW-SRV-034's rule, that a Zone of one Room has no orphan,
is what clears it, and AW-SRV-037 doesn't list that story in `depends_on`. So this branch sits on
AW-SRV-034's and merges after it. The frontmatter is yours to amend.

### For architecture: what AC-5 took

AC-5 holds each compiled blob byte-equal to `testdata/content/valid/`. Two things weren't true on
`main`, and this branch makes them true:
- **The three existing Zone files weren't in the compiler's bytes.** They held the same Rooms and
  Exits, hand-formatted and in a different order. They now hold the compiler's output, which is
  also the corpus case's `expected/`.
- **The fixture had no `town.*` Templates.** They lived only in `testdata/templates/templates/`.
  The three are now also in `testdata/content/valid/templates/`, byte-identical to that copy.
  The dev World loads 7 Templates, where it loaded 4, and the Helm chart's `andara-content`
  ConfigMap renders three more keys.

With both changes the full Go suite ran green before AW-SRV-034's rebase. Only the Zone and Room
counts moved (`zones_loaded` 3 → 4, `room_count` 6 → 7, `LoadDir` inputs 3 → 4), which the story's
scope names. The test names stay as they are (`TestLoadContent_ValidThreeZones` and so on),
because AW-SRV-012's verification record cites them.

### How each AC is covered
- AC-1, AC-2: `TestLoadContent_ValidThreeZones`. It checks four Zones, `purgatory/start`, and
  exactly one warning series, `missing_reverse_exit`, logged at warn on `purgatory/start`.
- AC-3: `TestPurgatoryWalksOutToThePlaza`. It builds the Engine over the World the fixture's files
  describe, sends `out` from `purgatory/start`, and asserts a plaza-scoped arrival and the
  bystander's `look` naming the arriving Character. `from_direction` comes out `in`, and the test
  doesn't assert it. Mutation-checked: the test fails with the Exit's direction changed.
- AC-4: the default `character.spawn_room` is unchanged.
- AC-5, AC-6: `TestDevFixtureSourceMatchesTestContent`. It compiles `content/fixtures/town/`
  against the shipped `content/core/templates/` seed, since no embedded core exists yet.
  Mutation-checked: a one-character change to `purgatory.json` fails it, naming the file.

### For implementation: `dev` loads 4 Templates, not 7 (SRE, 2026-09-29)

"For architecture: what AC-5 took" says the dev World loads 7 Templates and that `andara-content`
renders three more keys. On `dev` at `4576941` it doesn't. `andara-content` takes the top-level
`*.json` of `testdata/content/valid/` only, so it holds the four Zones. `andara-content-templates`
is linked to `content/core/templates/` (`deploy/helm/andara/files/`), so it holds the four
`andara.core` Templates. The three `town.*` Templates reach no ConfigMap, and the server logs
`templates loaded pack=andara.core templates=4`.

Nothing breaks, because no Zone places a `town.*` Template. If `dev` should load them before
AW-INF-021 moves it to the store, a chart change is needed: SRE's `deploy/` plus a ruling from
architecture on where they belong. Otherwise the line above should say 4. Your call which.

## For SRE and implementation: the `town.*` Templates on `dev` (architecture, 2026-09-30)

**Ruling: no chart change.** `dev` loads the `town.*` Templates when `AW-INF-021` moves it to the
content store, because pack `town` is published whole, Templates included. Until then
`andara-content-templates` stays linked to `content/core/templates/`, and `dev` loads 4 Templates.
Nothing places a `town.*` Template, so there's nothing to see before then. A second link under
`deploy/helm/andara/files/` would be chart work thrown away by `AW-INF-021`.

**For implementation:** the "7 Templates … three more keys" line above is true of dir-mode and
local loads of `testdata/content/valid/`. It isn't true of `dev`: `andara-content` gained one key
(`purgatory.json`), and `dev` loads 4 Templates. That's corrected here, and in the story's §8 review.

**For implementation, a follow-up that doesn't hold the story:**
`TestDevFixtureSourceMatchesTestContent` compiles against `content/core/templates/` on disk. AC-5
says "with the embedded `andara.core`", and the embed now exists (`content/core`, `AW-SRV-013`).
Switch the test to it on the next touch of `server/content/`.

## Carried by `AW-SRV-046` (PM, 2026-10-02)

The "not holding the story" follow-ups for implementation above are now items in `AW-SRV-046`
(draft, SPRINT-04), which records each one as done here when it merges.
