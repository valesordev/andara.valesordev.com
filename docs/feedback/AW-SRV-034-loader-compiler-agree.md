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
