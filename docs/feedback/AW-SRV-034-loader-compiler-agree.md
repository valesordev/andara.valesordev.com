# AW-SRV-034 — the loader and compiler agree on `orphan_room` and `duplicate_direction`

## Implementation, 2026-09-29

On `impl/aw-srv-034-loader-compiler-agree`. The loader and the compiler now share one
`orphan_room` rule and one `duplicate_direction` code. `make check` is red on exactly one case,
the corpus sidecar the story puts in this PR, because implementation can't write `docs/specs/`.

### For architecture: the sidecar (blocks the merge)

The ownership hook refuses implementation's edit to
`docs/specs/content-language/v1/corpus/valid/warn-missing-reverse-exit/expected.errors`. It has to
land with the compiler change, so `main` never sees one without the other. Its content, as the
compiler now emits it:

```
z.aw:4:3: orphan_room
  z
  loft
z.aw:7:5: missing_reverse_exit
  z
  loft
  down
```

The position is `4:3`, not the story's `2:3`. `loft`'s `room` keyword is on line 4, because
line 2 is `fallback loft`.

One way to land it without committing to another role's branch: branch `arch/aw-srv-034-sidecar`
from this branch's head, add the one commit, and open that PR in place of mine. Merging it merges
both. `TestConformance` is the only failure; nothing else in `go test ./...` moves.

### For architecture: the Context doesn't match the loader

The Context says the loader "accepts [duplicate Directions]: it sorts and keeps both". It didn't.
`BuildWorld` already refused the second Exit, as `malformed_file` ("duplicate exit direction").
So for the loader AC-3 changes the code, not the outcome, and the Data / state impact's "a pack
... that boots today will be refused" doesn't arise. `malformed_file` was fatal before, and
`duplicate_direction` is fatal now.

### What changed
- `server/sim`: `ErrDuplicateDirection`, raised where `malformed_file` was. The orphan check
  skips a Zone of one Room. The inbound rule and the self-loop exclusion are unchanged.
- `content/lang`: `CodeDuplicateDirection` moves to the block quoted from `sim`. `warnOrphans`
  counts inbound Exits only, keeps the single-Room exemption, and cites this story.
- Tests: `TestBuildWorld_LoaderAgreesWithCompiler` (AC-1, AC-2, AC-3, AC-5) and
  `TestOrphanIsInbound` (the chute, the one-Room Zone, the self-loop).

Three existing tests expected `orphan_room` on a one-Room Zone. Each now uses two Rooms, keeping
what it asserts:
- `TestBuildWorld_StrictOrphansFatal` adds an unconnected `attic`.
- `TestBuildWorld_SelfLoopIsStillAnOrphan` gives the self-looping `plaza` an Exit to `hall`, so
  `plaza` is the one orphan.
- `TestLoadContent_OrphanWarnNotFatal` adds an unconnected `attic`.

### Not done here
- `AW-CLI-002`'s Open questions entry about equivalence should name this story as closed (the
  story's Definition of done). It's a contract section, so it's architecture's edit.
- `errors.md` prose (§3.1 → §3.2, the §3.3 chute note). The story leaves that to architecture.

## Architecture, 2026-09-29

On `arch/aw-srv-034-sidecar`, branched from `impl/aw-srv-034-loader-compiler-agree` at `84d1649`.
It supersedes #156, and merging it merges both.
- **The sidecar** is as you gave it. `4:3` is right: `pack.aw` is a separate file, and in `z.aw` the
  `room loft` keyword is on line 4. The story's scope bullet is corrected. `TestConformance`: 72
  cases pass, 4 pending.
- **The Context was wrong, and it's corrected.** The loader refused a duplicate Direction as
  `malformed_file`. So AC-3 changes the code, not the outcome, and the Data / state impact says so.
  This is a contract correction after implementation started, so it's recorded here and in the
  story's body. Your code already matches the corrected text, so nothing changes for you.
- **`errors.md`:** `duplicate_direction` moves to §3.2, owned by this story, and the §3.3 chute
  note is replaced by the rule as it now holds.
- **`AW-CLI-002`'s Open questions** entry names this story as closed.

The §8 review follows once this merges, and SRE's instrumentation check has nothing new to verify
(Observability: none new).

## For implementation: two fixes from the review of #161 (architecture, 2026-09-29)

Codex left two comments on #161. Both are accepted, and both are application source, so they're
yours. The contract is amended (AC-3, `errors.md` §1 rule 7) in #161's architecture commit.
1. **`BuildWorld` reports no warning when it reports an error.** The "two Exits north" case must
   then expect `[duplicate_direction]` alone, with no orphans. Codex suggested making the
   compiler's `warnOrphans` skip refused Exits instead. That's harmless, but it changes nothing
   observable, because rule 7 already drops the compiler's warnings on a failed compile. The
   disagreement is the loader's warnings, so fix it there. Keep strict-mode orphans, which are
   errors (AC-5).
2. **`duplicate_direction`'s detail names both targets.** Keep the first Exit's target in
   `seenDir`, e.g. `duplicate exit direction "north": to hall, and again to yard`. The wording is
   yours, as long as it names the Direction and both targets.

**How to land it without committing to architecture's branch:** branch
`impl/aw-srv-034-review-fixes` from `origin/arch/aw-srv-034-sidecar`, add the fix commit, and open
that PR as superseding #161, the way #161 superseded #156. Merging it merges everything.
Close #161 then.


## Implementation, 2026-09-29: the two fixes

On `impl/aw-srv-034-review-fixes`, branched from `origin/arch/aw-srv-034-sidecar`. The PR
supersedes #161.
1. **A refused load reports only its errors.** When any finding is fatal, `BuildWorld` returns
   those findings and drops the warnings. Strict-mode orphans are errors, so they stay. In
   `TestBuildWorld_LoaderAgreesWithCompiler`:
   - "two Exits north" now expects `[duplicate_direction]` alone, with no orphans;
   - "the chute under strict_orphans" expects `[orphan_room]` alone, because the chute's
     `missing_reverse_exit` is a warning on a refused load.
2. **`duplicate_direction`'s detail names both targets:**
   `duplicate exit direction "north": to hall, and again to yard`. A cross-Zone target is written
   `zone/room`, as the loader's other details write it. The table test asserts this detail.

Nothing else in `go test ./...` moved.
