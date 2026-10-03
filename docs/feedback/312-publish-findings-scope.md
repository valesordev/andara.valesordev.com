# #312: what the publish gate reports

Ruling: `docs/specs/content-language/v1/errors.md` §1 rule 10 (architecture, 2026-10-03; revised after
the pre-PR review). The cascade's cause is ordering. `inputsOf` (`server/content/loader.go`) sorts
every pack by name, and `sim.BuildWorld` keeps the first Zone it sees. The issue's publisher
`sre.verify` sorts before the fixture's `town`, so the **fixture's** Zone was the one dropped and its
Exits failed. Ordering alone doesn't fix everything, so the rule also has a filter (10.4) and an
attribution (10.2). The `admin.proto` comments are updated and `gen/` regenerated (comments only).

## For implementation

SPRINT-04 item 8.
- **Where:** `CheckPublish` → `Loader.build` (`server/content/check.go`, `loader.go`). `inputsOf` serves
  boot, load, activation and `content validate` too, and they keep sorted-by-name order. The
  incumbents-first order is the gate's alone.
- **Tag each input with its pack** when the gate builds them; `sim.Input.File` stays the path.
- **The filter (10.4)** needs the set of Zone ids dropped as cross-pack duplicates, and each Exit's
  target from the publisher's `Resolved` Zone definitions (or a `Target` field on `ValidationError`).
  It must not parse `Detail`.
- **The message (10.5):** `sim` words `duplicate_zone` from two file names, so the gate rewrites its
  `Detail` after `BuildWorld`, from the pack tags and `Resolved.Version`.
- **No empty refusal:** `Admin.rejected` indexes `refusing[0]`. Rule 10.6 guarantees a refusal always
  has a finding. Keep it that way, and test it (test 8).
- **Only at the gate:** `build` is also called with `moving=false` and a nil candidate
  (`loader.go`). The new order applies where `candidate != nil && !moving`.
- **`andara-cli`:** `content publish` prints a finding from another pack as rule 10.6 says
  (`placeAll` falls back to the blob path, and `writeDiagnostics` prefixes the publisher's pack
  label today, which is the mislabel in the issue). Key it on `Diagnostic.pack` (new, field 8).

Fixtures. The incumbent is pack `town` with Zone `town` (Rooms `plaza`, `shop`), and Zones `docks` and
`wilds` with Exits into `town.plaza`. The publisher is pack `acme`, which **sorts before `town`**, as
`sre.verify` does. Without that, the fixture passes before the fix.

1. **Given** `acme` declares a Zone `town` with one Room `market` **when** it publishes **then** the
   refusal carries exactly one finding: `duplicate_zone`, attributed to `acme`'s blob, with message
   `ZoneID town declared in pack acme and in active pack town@N`. `content publish` prints it on `acme`'s
   source (the `z.aw:1:1` form) and exits `1`. `validation_failures_total{duplicate_zone}` is `1`, every
   other code `0`, and the audit `findings_count` `1`.
2. **Given** (1) with `acme`'s `town` also declaring a Room `plaza` **when** it publishes **then** still
   exactly one finding, with no `duplicate_room`.
3. **Given** (1) with another `acme` Zone `glade` whose Exit targets `town.market` **when** it publishes
   **then** still one finding. **When** `acme` then renames its `town` **and** publishes **then** the
   Exit's own `unknown_room` is reported, because `town.market` doesn't exist in the World.
4. **Given** one pack with two files declaring Zone `x` and a Room twice **when** it publishes **then**
   `duplicate_zone` and `duplicate_room` are both reported, as today (rule 10.4).
5. **Given** `town` and `acme` both have a blob `town.json`, `town`'s holding a `missing_reverse_exit`
   warning, and `acme`'s Zone is valid **when** `acme` publishes **then** `warnings` is empty and
   `content publish` prints no line from `town`. A path filter would get this wrong.
6. **Given** `acme` has its own `orphan_room` **when** it publishes **then** that warning is printed on
   `acme`'s source.
7. **Given** pack `acme@1` declares Zone `glade` and active pack `town` has an Exit into `glade.x` **when**
   `acme@2` publishes without `glade` **then** the publish is refused and `town`'s finding is reported
   with `pack` `town`, an empty chain, and `line` and `col` `0`. `content publish` prints
   `town/<file>: unknown_zone …`, not under `acme/`. `--output json` carries `pack`. **And** `acme`'s own
   pack-level finding (a `pack_mismatch`) still prints under `acme/`.
8. **Given** `town` is active, its Zone has at least two Rooms, one of them an orphan, and
   `content.strict_orphans` is turned on afterwards **when** `acme` publishes a valid pack **then** the
   publish is refused with that finding reported with `pack` `town` (rule 10.6), not with an empty
   refusal and no panic.
9. **Mutation checks:** restoring sorted order at the gate makes 1 fail (the cascade returns); removing
   the dropped-Zone filter makes 2 and 3 fail; filtering by path alone makes 5 fail; leaving the
   `<pack>/` prefix on foreign findings makes 7 fail; dropping the 10.6 report makes 8 fail; keying the CLI on `line` `0` instead of `pack` makes the `pack_mismatch` half of 7 fail.

On merge, `docs/builders/04-your-first-zone.md` loses its "ignore it" paragraph about the
`purgatory.json` warning. That edit is architecture's (a `docs/builders` path), so say so on the PR.
Boot, load and activation are out of scope. Activation of a version that clashed with a rival that
wasn't yet active when it passed the gate can show the old cascade. File it as its own issue if seen.

## For SRE

None. The Observability section's names and labels don't change; only what the counter counts.
`AW-INF-021`'s §8 record cites `unknown_room` 3 beside `duplicate_zone` 1 for the old behavior, and
that record stands as written.
