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
