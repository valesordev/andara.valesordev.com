---
id: AW-CLI-002
title: andara-cli content validate and inspect
epic: EPIC-05
component: cli
type: feature
status: ready
size: S
depends_on: [AW-CLI-001, AW-CLI-006, AW-SRV-001]
blocks: [AW-CLI-003, AW-INF-010]
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
- Offline operation for `--path` against the cached core pack.
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
5. **Given** no network and a cached core **when** `validate --path` runs **then** it succeeds; with no
   cache it fails with `E_CORE_VERSION` and the `fetch-core` hint, exit `1`.
6. **Given** `--pack town --version 8` **when** `validate` runs **then** it fetches the version over
   `Admin.GetVersion` and the blobs, validates, and exits `0`/`1` as above; server unreachable is exit
   `3`.
7. **Given** `inspect template town.Merchant` **when** it runs **then** each Component field shows the
   ancestor that set it, e.g. `Dialogue.greeting = "Fine wares!"  (town.Merchant)` and
   `Aggro.threshold = 3  (andara.core.Npc)`.
8. **Given** `inspect room market/square` **when** it runs **then** Exits are listed in the closed
   Direction order with reverse-Exit presence marked.

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
  (AC-5); `--pack` path against a throwaway Redpanda (AC-6).
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
