---
id: AW-CLI-002
title: andara-cli content validate and inspect
epic: EPIC-05
component: cli
type: feature
status: ready
size: S
depends_on: [AW-CLI-001, AW-CLI-006, AW-SRV-001, AW-SRV-034, AW-SRV-013]
blocks: [AW-CLI-003, AW-INF-010, AW-INF-022]
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
