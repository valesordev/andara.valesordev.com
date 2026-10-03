---
id: AW-CLI-009
title: andara-cli content reference — the Builder's reference from the binary
epic: EPIC-06
component: cli
type: feature
status: review
size: S
depends_on: [AW-CLI-002]
blocks: [AW-INF-028]
lane: implementation
risk: low
---

## Context

The Builder's Guide (`AW-INF-023`) has a reference section (section 7) that lists the Direction
set with reverses, the Component types with their fields, the `andara.core` Templates with their
chains, and every diagnostic code with its severity. Its AC-2 says those tables are generated and
can't go stale. Two of the four sources exist only in the code. The Component types are
`sim.ComponentTypes()`, the server-defined registry (ADR-0010 decision 7). The complete set of
diagnostic codes is the `sim.ErrCode` constants plus the compiler-only `lang.Code*` constants.
Today two codes the loader raises are missing from `errors.md` (`zone_removed` and
`spawn_room_removed`, `AW-SRV-012`), so a reference built from `errors.md` would already be
incomplete. Architecture's
contract review of `AW-INF-023` (Blocked by, item 3) routes reading the code to implementation.

This story adds one command that prints all four tables from what the binary carries. Builders can
use it offline, since the guide and the binary can disagree while the binary can't disagree with
itself. It's also the only source `make builder-reference` (`AW-INF-028`) reads. It depends on
`AW-CLI-002`, which makes `andara-cli` embed the compiled `content/core/` and its `VERSION`.

## User story

As a builder, I want `andara-cli` to print the Directions, Component types, core Templates and
diagnostic codes that it actually enforces, so that I can check what's legal without the network
or a guide that may be older than my binary.

## Scope

### In scope
- `andara-cli content reference [--output human|json]`, printing four sections: `directions`,
  `component_types`, `core` and `diagnostics`.
- A closed, declared table of every diagnostic code, giving each code's severity and whether the
  compiler raises it, the loader does, or both. The table lives in the code, next to the constants
  it describes, and a test holds it complete against those constants (AC-5).
- Human output: four headed tables, in the same order and content as the JSON.

### Out of scope
- Rendering the JSON into the guide, and checking the guide against it. That's `AW-INF-028`.
- Explaining the triggers and fixes for each code. That prose stays in `errors.md`, and the guide
  links to it (`AW-INF-023`, section 7).
- Any change to a code, a severity, a Direction or a Component type. This story reports what
  exists.
- Anything fetched from a server. A server's active core may differ from the embedded one.
  `andara-cli version` and `AW-INF-022` already compare the two.

## Acceptance criteria

1. **Given** no config file, no credentials and no network **when**
   `andara-cli content reference --output json` runs **then** it exits `0` and stdout is one JSON
   object matching the Interface contract. It has nothing else on stdout and nothing on stderr.
2. **Given** the same run **then** `directions` holds exactly the twelve entries of
   `sim.Directions()`, in that order. Each entry's `reverse` equals `Direction.Reverse()`.
3. **Given** the same run **then** `component_types` holds one entry per `sim.ComponentTypes()`
   element, sorted by `type`. Each entry's `fields` equal `sim.ComponentFieldNames(t)` with each
   field's `sim.ComponentFieldKind` rendered as `string`, `int` or `bool`. A marker Component has
   `"fields": []`.
4. **Given** a build whose `content/core/VERSION` is `N` **then** `core.version` is `N`, which is the
   value `andara-cli version` prints. `core.templates` holds every embedded core Template, sorted by
   `name`, each with its `kind` spelled as the source keyword (`entity`, `item`, `behavior`) and its
   `chain` root-first, ending with the Template itself.
5. **Given** a new `sim.ErrCode` constant, a new `lang.Code*` constant, or a new registered
   Component type or Direction with no matching entry in the output **when** `make check` runs
   **then** a test fails, naming the missing item. The test finds constants by parsing the `sim` and
   `lang` packages' source, not from a hand-kept list.
6. **Given** the diagnostics table **then** `orphan_room` and `missing_reverse_exit` have
   `severity: warning` and every other code has `severity: error`. A test holds the warning set
   equal to `sim`'s `warningCodes`.
7. **Given** the diagnostics table **then** each code's `raised_by` is decided by its string value.
   It's `compiler` for a value only `lang`'s `Code*` names hold, `loader` for a value only a
   `sim.ErrCode` holds, and `both` for a value in both sets. A value can be in both because `lang`
   quotes the `sim` constant (`CodeOrphanRoom`) or because it repeats the literal (`CodePackMismatch`,
   `CodeFallbackMissing`). A test derives this from the parsed source and compares it with the
   table.
8. **Given** two runs of the same binary **then** their JSON outputs are byte-identical. A golden
   file under `admin/cli/testdata/` holds the output, and the test fails with a diff when it
   changes.
9. **Given** `--output human`, or no `--output` with no configured default **then** it exits `0` and
   prints the four sections, under the headings `Directions`, `Component types`,
   `Core (andara.core@N)` and `Diagnostics`, with the same rows as the JSON.
10. **Given** an extra positional argument or an unknown flag **then** it exits `2`, with
    `AW-CLI-001`'s usage error.

## Interface contract

```
andara-cli content reference [--output human|json]
```

`--output` is the existing global flag (`human` or `json`), and a config default applies the same
way it does for every other command. The command dials nothing, sends no credentials, and never
reads the content cache. It reports only the core it embeds.

It runs `AW-CLI-001`'s root pre-run like every command. That pre-run loads the config file if one
exists, and inspects the credential file if one exists. A pre-run refusal, such as a malformed
config or a credential file looser than `0600`, exits `2` with `AW-CLI-001`'s error envelope, as
it does for `content validate`. With no config file and no credential file, the pre-run reads
nothing, which is AC-1. *(Amended at contract review, 2026-10-02. The draft said "reads no
credentials", which the shared pre-run contradicts. Exempting one command from it is the
divergence §7 already declined.)*

| Exit | Meaning |
|-----:|---------|
| `0` | printed |
| `1` | never raised. There are no diagnostics to report (kept for parity with `AW-CLI-002`) |
| `2` | usage, a pre-run refusal (`AW-CLI-001`), or writing to stdout failed |

It doesn't use exits `3` or `4`, because it never dials anything.

```jsonc
// CONTRACT SKETCH — not an implementation
{
  "format_version": 1,
  "directions": [                              // sim.Directions() order
    {"name": "north", "reverse": "south"}
  ],
  "component_types": [                         // sorted by type
    {"type": "andara.core.Behavior",
     "fields": [{"name": "name", "kind": "string"}]}   // sorted by name; kind: string|int|bool
  ],
  "core": {
    "pack": "andara.core",
    "version": 3,                              // content/core/VERSION, as `andara-cli version`
    "templates": [                             // sorted by name
      {"name": "andara.core.Character", "kind": "entity",
       "chain": ["andara.core.Entity", "andara.core.Character"]}
    ]
  },
  "diagnostics": [                             // sorted by code
    {"code": "orphan_room", "severity": "warning", "raised_by": "both"}
    // severity: error|warning   raised_by: compiler|loader|both
  ]
}
```

- `format_version` goes up only for a breaking change to this shape. Adding an entry to any list
  isn't a breaking change, and `AW-INF-028` must accept one.
- Directions are in canonical compass order rather than alphabetical. That order is the one
  `semantics.md`, `sim.Directions()` and `AW-CLI-002` AC-8 use, and it's stable. Every other list is
  sorted by the key named above.
- The `severity` is the one a code has when nothing promotes it. `--strict-orphans` promotes
  `orphan_room` at load time. That's a load policy, not a property of the code, so it isn't in this
  shape.
- The diagnostics table's placement and Go shape are implementation's choice. It must be the one
  table, and AC-5 to AC-7 must hold it complete.

## Data / state impact

None. It's read-only and uses no storage.

## Observability requirements

*SRE review, 2026-10-02: amended. The change is recorded in
`docs/feedback/AW-CLI-009-content-reference.md`.*

- **Metrics / Alerts:** none. It's a local, offline command. It dials nothing and serves nothing.
- **Traces:** `AW-CLI-001`'s `cli.command` root, opened for every command in the root pre-run
  (`admin/cli/root.go`, `admin/cli/telemetry.go`), with `command`, `outcome`, and `exit_code`.
  This command inherits it, as `content validate` does, and adds no child span. It doesn't opt out:
  an exemption in the pre-run would be new code that makes one command differ from the rest of
  the tree, and a span that exports nowhere by default costs nothing.
- **Logs:** `AW-CLI-001`'s `debug` `command completed in <d>` line, as every command emits. It carries
  `trace_id` under `--output json`. A usage error goes to stderr in `AW-CLI-001`'s error envelope, like every other
  command's.

## Test plan

- **Unit:**
  - The golden-file test on `--output json` (AC-8).
  - A completeness test. It parses `server/sim` and `content/lang` with `go/parser` and fails when a
    `sim.ErrCode` constant, a `lang.Code*` constant, a registered Component type, or a Direction is
    missing from the output. It also fails when the output names one that no longer exists
    (AC-2, AC-3, AC-5).
  - The `raised_by` derivation (AC-7) and the warning set (AC-6).
  - The human rendering against its own golden file (AC-9), and the usage errors (AC-10).
- **Integration:** a run in CI's network-less job with `HOME` empty and no config (AC-1).
- **Manual/operator:**
  ```
  andara-cli content reference                       # expect: four tables, "Core (andara.core@N)"
  andara-cli content reference -o json | jq '.diagnostics | length'   # expect: every code, currently 37
  ```

## Definition of done

CLAUDE.md §8, plus: the golden file is committed, and the completeness test runs in `make check`.

## Open questions

- `[ASSUMPTION]` The flag values are `human|json`, not `text|json`. `human` is the global
  `--output` flag's existing value (`admin/cli/root.go`), and a second spelling for one command
  would split the CLI's vocabulary.
- `[ASSUMPTION]` `andara.core` Templates carry no Components or fields in the reference. Section 7
  asks for each Template's chain only, and `content inspect template` (`AW-CLI-002` AC-7) already
  shows the flattened fields.
- `[ASSUMPTION]` The count of 37 in the manual step is today's count: 24 `sim.ErrCode` values plus
  the 13 `lang.Code*` literals that no `sim.ErrCode` shares. It will drift. The step checks that the command runs,
  not that the count is 37.

## Verification record — 2026-10-03 (implementation; `review` until the §8 checklist passes)

Branch `impl/aw-cli-009-content-reference`.

| AC | Test (`admin/cli/contentreference_test.go`) | What it asserts |
|----|---------------------------------------------|-----------------|
| 1 | `TestContentReference_JSONIsStableAndGolden` | With an empty `HOME`, no config and no credentials: exit `0`, one JSON object on stdout with no unknown fields, and nothing on stderr |
| 2 | `TestContentReference_Directions` | The twelve `sim.Directions()`, in order, each with `Direction.Reverse()` |
| 3 | `TestContentReference_ComponentTypes` | One entry per `sim.ComponentTypes()`, sorted. Fields come from `sim.ComponentFieldNames`, with kinds `string`/`int`/`bool`, and a marker has `[]` |
| 4 | `TestContentReference_Core` | `core.version` is `content/core/VERSION`. Every embedded Template is listed, sorted, with its kind keyword and a chain ending in itself |
| 5 | `TestContentReference_DiagnosticsAreComplete` | It parses `server/sim` and `content/lang` with `go/parser`. Every `sim.ErrCode` and `lang.Code*` value is listed once, sorted. Any listed value no constant holds fails |
| 6 | `TestContentReference_WarningsAreSims` | The warning set equals what `sim.IsWarning` says over the parsed codes: `missing_reverse_exit` and `orphan_room` |
| 7 | `TestContentReference_DiagnosticsAreComplete` | `raised_by` follows from which package's parsed constants hold the value |
| 8 | `TestContentReference_JSONIsStableAndGolden` | Two runs are byte-identical, and equal to `admin/cli/testdata/reference/reference.json` |
| 9 | `TestContentReference_Human` | `--output human` and the default are identical: four headings in order, the JSON's row count, and the golden `reference.txt` |
| 10 | `TestContentReference_UsageErrors` | An extra argument or an unknown flag exits `2` with nothing on stdout |

**The table:** `lang.Diagnostics` (`content/lang/reference.go`), next to `lang`'s codes and quoting
`sim`'s. It has 37 rows:
- 16 `both`;
- 8 `loader`;
- 13 `compiler`.

**Mutations, each caught by a test that names the cause:**
- a new `sim.ErrCode` with no row;
- a new `lang.Code*` with no row;
- a row removed;
- a wrong `raised_by`;
- an extra warning.

A new Component type needs no row. The command reads `sim`'s registry, so it appears in the
output, and only the golden flags it.

**Outstanding before `done`:**
- AC-1's run in CI's network-less job is SRE's to wire. Here it's the unit test above, run with
  an empty `HOME` and no config.
- AW-INF-028 consumes the JSON.
