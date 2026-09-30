---
id: AW-CLI-002
title: andara-cli content validate and inspect
epic: EPIC-05
component: cli
type: feature
status: review
size: S
depends_on: [AW-CLI-001, AW-CLI-006, AW-SRV-001, AW-SRV-034, AW-SRV-013]
blocks: [AW-CLI-003, AW-INF-010, AW-INF-022, AW-CLI-009]
lane: implementation
risk: low
---

## Context

`AW-SRV-001` put the content validator in the simulation core specifically so it could be reused. This is
the first reuse: a Builder validating locally before publishing, and CI validating on every content
change.

ADR-0004 makes the server-side check at publish the authoritative gate, since Builders are untrusted. That
does not make this command redundant — it makes it the fast feedback loop. A Builder should find a dangling
Exit in a second on their laptop, not in a round trip to a server. With ADR-0009 and `AW-CLI-006`,
"content" on the laptop is Content Language source, so `validate --path` compiles first and validates
second, and both stages report through one diagnostic format.

## User story

As a builder, I want to validate my content locally and be told exactly what is wrong, so that I never
publish a Zone that fails to load.

## Scope

### In scope
- `andara-cli content validate [--path DIR | --pack ID --version N]`: compile (`AW-CLI-006`) then
  `sim.BuildWorld`; findings rendered per `AW-CLI-001`, human and JSON.
- `andara-cli content inspect zone|room|template <ref> [--pack --version]`: print the resolved
  definition — flattened Template with per-field provenance (ADR-0010 §9), Room with Components and
  Exits, Zone summary.
- Offline operation for `--path` against the core pack `andara-cli` embeds: the compiled
  `content/core/` and its `content/core/VERSION`, the same files the server embeds and publishes at
  boot (ADR-0004 and ADR-0010 §8, amended 2026-09-28). The cache (`--cache`, `ANDARA_CONTENT_CACHE`,
  filled by `content fetch-core --from`, `AW-CLI-006`) stays as the second place a core version is
  looked up.
- `andara-cli version` prints the embedded core as `andara.core@<N>`.
- The three-way equivalence fixture: one fixture set, three runners (CLI, CI job, server publish gate),
  identical findings.

### Out of scope
- Publishing — `AW-CLI-003`. The compiler — `AW-CLI-006`.
- Any validation logic of its own.

## Acceptance criteria

1. **Given** source with a dangling Exit **when** `content validate --path` runs **then** it exits `1`
   and prints one line per finding as `file:line:col: CODE message` naming Room, Direction, and target.
2. **Given** valid source **when** it runs **then** it exits `0` and prints `N zones, M rooms, T
   templates, core andara.core@V`.
3. **Given** `--output json` **when** validation fails **then** stdout is a JSON array of `Diagnostic`
   and nothing else; stderr carries nothing but the exit summary.
4. **Given** the equivalence fixture **when** run by the CLI, by `make content-conformance`, and by
   `AW-SRV-013`'s gate **then** all three produce identical diagnostics (code, position, chain).
5. **Given** no network, no cache, and a pack declaring `requires andara.core@<N>`, where `N` is the
   build's `content/core/VERSION` **when** `validate --path` runs **then** it succeeds against the
   embedded core. **Given** a pack requiring `andara.core@M`, `M ≠ N`, with no cached `M` **then** it
   fails with `core_version_mismatch` naming `M` and `N`, and the hint `this andara-cli embeds
   andara.core@N; use the andara-cli release that embeds andara.core@M`, exit `1`. **Given** a cached
   `M` **then** it validates against the cache. *(Amended 2026-09-28: the embedded core replaces
   the cached-core-or-fail contract.)*
6. **Given** `--pack town --version 8` **when** `validate` runs **then** it fetches the version over
   `Admin.GetVersion` and the blobs over `Admin.GetBlob` (`AW-SRV-013`), validates, and exits `0`/`1` as above; server unreachable is exit
   `3`.
7. **Given** `inspect template town.Merchant` **when** it runs **then** each Component field shows the
   ancestor that set it, e.g. `Dialogue.greeting = "Fine wares!"  (town.Merchant)` and
   `Aggro.threshold = 3  (andara.core.Npc)`.
8. **Given** `inspect room market/square` **when** it runs **then** Exits are listed in the closed
   Direction order with reverse-Exit presence marked.
9. **Given** a build whose `content/core/VERSION` is `N` **when** `andara-cli version` runs **then**
   it prints `core:     andara.core@<N>` after `built_at`, and `--output json` carries
   `"core_version": N`. `AW-INF-022`'s check compares this with `dev`'s active core.
10. **Given** the embedded core **when** `make check` runs **then** a test holds its digest (sha256 of
    the sorted blob hashes, as `AW-SRV-013` defines it) equal to the `content/core/VERSIONS` line
    for `VERSION`, the same check `AW-SRV-013` AC-19 makes for the server. The two binaries can't
    embed different cores under one number.

## Interface contract

```
andara-cli content validate [--path DIR | --pack ID --version N] [--output human|json]
andara-cli content inspect zone <zone>          [--path DIR | --pack ID --version N]
andara-cli content inspect room <zone>/<room>   [...]
andara-cli content inspect template <pack>.<name> [...]
```

| Exit | Meaning |
|-----:|---------|
| `0` | valid / printed |
| `1` | diagnostics (compile or validation) |
| `2` | usage or IO |
| `3` | server unreachable (`AW-CLI-001` taxonomy) |

Core lookup for `requires andara.core@M`, in order:
1. the embedded core, if `M` is the build's `VERSION`;
2. the cache, `<cache>/andara.core/<M>/` (`AW-CLI-006`'s layout);
3. otherwise `core_version_mismatch` (AC-5).

`content fetch-core` keeps `--from` (`AW-CLI-006`). It never fetches over Admin: the embedded core
is the network-free source, and an older or newer core comes with the `andara-cli` release that
embeds it. Without `--from`, its `core_fetch_unavailable` message names the embedded core and the
releases instead of `AW-SRV-013`.

Diagnostics are `lang.Diagnostic` from `AW-CLI-006`; `sim.ValidationError` findings are mapped into the
same shape with `file:line:col` recovered from the source map the compiler emits. JSON schema is
`AW-CLI-001`'s error envelope with `diagnostics: []`.

## Data / state impact

Read-only.

## Observability requirements

Per `AW-CLI-001`: no metrics, structured stderr diagnostics, `cli.command` root span with
`content.compile` and `content.validate` children carrying counts.

## Test plan

- **Unit:** finding-to-diagnostic mapping with source map; output formatting both modes.
- **Integration:** the three-way equivalence test (AC-4) in CI; offline run in a network-less container
  with no cache (AC-5), and the mismatch case with a pack requiring another core; `--pack` path against a throwaway Redpanda (AC-6).
- **Manual/operator:**
  ```
  andara-cli content validate --path ./town            # expect: "3 zones, 41 rooms, 7 templates, core andara.core@3"
  andara-cli content inspect template town.Merchant    # expect: fields with provenance
  ```

## Definition of done

CLAUDE.md §8, plus: the equivalence test runs in CI using the same fixtures `AW-SRV-001`, `AW-SRV-013`,
and `AW-CLI-005`'s corpus use.

## Open questions

- **Resolved 2026-09-07 (Brian):** no Builder has repository access; `--path` is a Builder's own working
  directory of `.aw` source, `--pack/--version` is what is already published.

- **Closed by `AW-SRV-034` (2026-09-29, architecture):** the loader and the compiler apply one
  `orphan_room` rule and share `duplicate_direction`, and the corpus agrees (#156). AC-4's
  equivalence has nothing left to wait on. *As found at the `AW-CLI-005` review, 2026-09-24:* AC-4 could not hold yet.
  The loader and the compiler disagree on `orphan_room`: the loader warns on a Room nothing enters,
  while the compiler warns only on a Room with no Exit either way and skips one-Room Zones. The
  loader also has no `duplicate_direction`. `AW-SRV-034` makes them agree, on the rule now in
  `errors.md` §3.3. Pick this story up after that one lands.

## Contract review (architecture, 2026-09-28)

A contract change after `ready`, recorded here (CLAUDE.md §6). Implementation hasn't started.

1. **The CLI embeds `andara.core`** (ADR-0004 and ADR-0010 §8, amended 2026-09-28;
   `docs/feedback/AW-INF-021-dev-content-store.md` item 4). The Content Repository's CI and a Builder
   validate offline with no network, no credential, and no cache step. `andara.core@N` means the same
   bytes in the CLI and on every server, because both embed `content/core/` under one `VERSION`, and
   AC-10 holds them to `VERSIONS`.
2. **AC-5 is rewritten.** Its `fetch-core` hint pointed at an Admin fetch no story defined
   (`docs/feedback/AW-CLI-006-content-language-compiler.md` §7). The hint now names the release that
   embeds the required core. `fetch-core --from` and the cache stay, since `AW-CLI-006` built them.
3. **`andara-cli version` prints the embedded core** (AC-9). It's here rather than in `AW-INF-020`,
   because embedding is Go source under `cmd/` and `admin/`, and `AW-INF-020` only builds and
   publishes the binary. `AW-INF-022`'s core check reads this line.
4. **`AW-SRV-013` is added to `depends_on`.** AC-6 already needed its `GetVersion` and `GetBlob`
   against a throwaway Redpanda, and AC-5 and AC-10 need its `content/core/VERSION` and `VERSIONS`.
   The sprint already orders `AW-SRV-013` (item 4) before this story (item 6). If PM splits the core
   boot out of `AW-SRV-013`, this story depends on both halves.
5. `errors.md`'s `core_version_mismatch` row and `semantics.md` §2 are amended to match.

## Implementation record (2026-09-30)

On `impl/aw-cli-002-content-validate-inspect`. `validate` compiles with `content/lang` and then
runs `content.Validate`, the Loader's own validator factored out of `Loader.build`, over blobs
decoded by the gate's decoder (`content.ResolveBlobs`). The CLI has no validation logic of its
own. The compiler's new source map (`lang.SourceMap`), keyed by declaration chain, places each
validator finding on its `.aw` line. `sim.ValidationError` gained the Exit direction, so a loader
chain reaches the Exit the way the compiler's does. Decisions the contract didn't make are in
`docs/feedback/AW-CLI-002-content-validate-inspect.md`, with the questions for architecture and SRE.

| AC | Covered by | Result |
|----|------------|--------|
| 1 | `TestContentValidate_DanglingExit`: exit 1, and the first line is `…/z.aw:5:19: unknown_room Room "r" exits north to nowhere, and Zone "z" has no Room "nowhere"` | pass |
| 2 | `TestContentValidate_ValidPrintsCounts`: `4 zones, 7 rooms, 3 templates, core andara.core@1` for the dev fixture | pass |
| 3 | `TestContentValidate_JSONIsTheArrayAlone`: stdout decodes as one array and nothing follows it, and stderr is exactly the summary. Built as AC-3, not as the envelope the Interface contract names (feedback, For architecture 1) | pass |
| 4 | `internal/contentequiv`'s fixture set (47 cases) held to its expected findings by the compiler (`TestCompilerAgrees`, through `lang.Conformance`), by the CLI (`TestContentValidate_AgreesWithTheEquivalenceFixture`), and by the gate (`server/content` `TestPublishGateAgreesWithTheEquivalenceFixture`, 17 cases that compile). Mutation-checked: when Exit findings are placed on their Room, the CLI and gate runners both fail | pass; scope in feedback, For architecture 3 |
| 5 | `TestContentValidate_EmbeddedCoreThenCache`: with no cache and no server, `@1` validates against the embedded core. `@2` fails `core_version_mismatch` with the release hint, then validates once `@2` is cached | pass |
| 6 | `TestContentValidate_PublishedVersion`: the real gateway and `content.Admin` over in-memory topics. A valid version exits 0, with its warning placed on `town@1/purgatory.aw:6:5`. A version with a dangling Exit and no sources exits 1 on `town.json`. Valid blobs with a source that doesn't compile exit 0, with the source's finding as a warning. A source path that leaves the pack is refused as `unsafe_source_path` before anything is written; mutation-checked, since without the guard the file lands outside the pack. With the server stopped, exit 3. `TestContentValidate_PublishedVersionOverRedpanda` (`-tags integration`) runs the same flow over throwaway Redpanda topics and passes locally | pass; CI doesn't run the Redpanda test yet (feedback, For SRE 1) |
| 7 | `TestContentInspect_TemplateProvenance`: `Behavior.name = "town.merchant"  (town.Merchant)` and `Memory  (andara.core.Npc)`, with an inherited field attributed to the ancestor that set it | pass |
| 8 | `TestContentInspect_RoomExitsInDirectionOrder`: north, west, up in that order (written up, west, north), each marked `back: <dir>` or `one-way` | pass |
| 9 | `TestVersion_NamesTheEmbeddedCore`: `core:     andara.core@1` after `built_at`, and `"core_version": 1` in the JSON | pass |
| 10 | `TestEmbeddedCoreIsTheOneVERSIONSRecords` (`admin/cli`): the Templates the CLI resolves against, re-encoded, hash to `VERSIONS`' line for `VERSION`. `content/core`'s test makes the same check for the server | pass |

`make check` passes. `TestSnapshotCopyStaysInsideTheStallBudget` failed once under full-suite load
(#172, which fails on `main` too) and passed alone and on the rerun. `server/content`'s integration
tests pass against the local Redpanda after the `Loader.build` refactor.

**Instrumentation:** `cli.command` is the root span, with children `content.compile` (`files`,
`zones`, `rooms`, `templates`, `diagnostics`) and `content.validate` (`zones`, `rooms`,
`templates`, `error_count`, `warning_count`), asserted by `TestContentValidate_EmitsTheSpans`. There
are no metrics, per `AW-CLI-001`.

**Not done here, and why:**
- **The Redpanda variant of AC-6 isn't in CI.** `make test-integration` doesn't list `./admin/cli/`,
  and the Makefile is SRE's (feedback, For SRE 1).
- **The gate still reports compiled-blob positions.** Placing them is the job of whoever holds the
  source: `content validate` does it now, and `content publish` (AW-CLI-003) will
  (feedback, For architecture 2).

## §8 instrumentation check (2026-09-30, SRE): satisfied

On `sre/aw-cli-002-verify`, with `andara-cli` built from `main` at `49bfafb`. Per `AW-CLI-001`, a CLI
has no metrics and exports no spans: its real backend is its streams and its exit code, with the
trace ID in the log line for the operator's environment to collect. So the check is those, run
live, and the span assertion running in CI.

| Signal | How | Observed |
|--------|-----|----------|
| Exit codes | `content validate --path` on `content/fixtures/town/`, on a copy with one Exit sent to `nowhere`, and on `/nonexistent` | `0`, `1` and `2`, as the Interface contract says |
| Human output | the same runs | the finding on stdout as `town.aw:6:19: unknown_room …` with chain `town`, `plaza`, `north`; the valid run ends `4 zones, 7 rooms, 3 templates, core andara.core@1` |
| `--output json` | the bad copy | stdout is the one `Diagnostic` array. stderr is only `…: 1 finding(s) refuse the pack` (AC-3). A usage error is the `AW-CLI-001` envelope on stdout, with stderr empty |
| Structured log | `--output json --log-level debug` | `{"ts","level":"debug","msg":"command completed in …","command":"content validate","trace_id":"ffa75256…"}` carries `AW-CLI-001`'s required fields |
| Spans | `TestContentValidate_EmitsTheSpans`, in `make test` | `cli.command` → `content.compile`, `content.validate`, with the counts, asserted. The CLI exports nowhere by design, so a span can't be seen on Tempo |
| AC-6 over Redpanda | `TestContentValidate_PublishedVersionOverRedpanda` | **now in CI.** `./admin/cli/` is added to `make test-integration`, which the `stack` workflow runs (feedback, For SRE 1). Passed locally in 1.56 s, and the whole target passes |

**Noted for architecture, not holding the story.** In `--output json` mode the stderr summary is a
plain line, not an `AW-CLI-001` structured line with `trace_id`. AC-3 asks for exactly that, and it's
the envelope question implementation already raised (feedback, For architecture 1). Whichever way
that's ruled, the stderr contract should say whether the summary is structured.

`cli.command` reaching the server as `traceparent` on `--pack` isn't observed here. `dev` and compose
have no store-backed server to call, so `AW-INF-021` observes it, with `AW-SRV-013`'s RPC span tree.
