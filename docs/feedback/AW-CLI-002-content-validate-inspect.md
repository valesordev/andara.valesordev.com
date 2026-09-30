# AW-CLI-002 — content validate and inspect: implementation notes

Built on `impl/aw-cli-002-content-validate-inspect`. The story is at `review`. What follows is what
building it decided that the contract didn't, for architecture to confirm or amend, and one ask of
SRE.

## For architecture

### 1. AC-3 and the Interface contract disagree on the JSON shape. Built as AC-3.

AC-3 says a failed `--output json` puts "a JSON array of `Diagnostic` and nothing else" on stdout,
and "nothing but the exit summary" on stderr. The Interface contract, and `errors.md` §1, say the
JSON is "`AW-CLI-001`'s error envelope with `diagnostics: []`". Stdout can't be both. `content
compile` already uses the envelope, with its findings in `error.detail.diagnostics`.

**Built:** AC-3, because it's the one §8 checks mechanically. `validate`'s JSON stdout is always
the array, and it holds warnings when the pack is valid. The one-line summary goes to stderr in
both cases. Usage, IO and connection failures (exit 2 and 3) still use the envelope. Please amend
whichever text you don't mean. If the envelope wins, the change is one function,
`reportValidated`.

### 2. How the gate's findings get source positions (AC-4)

The contract has the CLI map `sim.ValidationError` findings "with `file:line:col` recovered from the
source map the compiler emits". No source map existed, and compiled blobs carry no positions. The
publish gate decodes them without positions at all: `Resolve` sets no `sim.Input.Pos`.

**Built:**
- **The source map is keyed by declaration chain**, not by compiled line. `lang.Output.SourceMap`
  records where each Zone, Room, Exit and Template was declared, and the chain the compiler reports
  it under. `SourceMap.Place(code, message, chain, severity)` returns the finding at the position
  the compiler uses for that code: an unresolved Exit target at the reference, every other Exit
  finding at `exit`, and `fallback_missing` at the `fallback` reference.
- **`sim.ValidationError` gained `Exit Direction`.** `BuildWorld` sets it on Exit-scoped findings,
  and `server/content.Diagnostics` appends it to the chain. So the gate's `PublishFindings` now
  carry `[zone, room, direction]` for an Exit finding, as the compiler does. That's additive on
  the wire (a third chain element), but it does change AW-SRV-013's response.
- **The gate itself reports compiled-blob positions, unchanged.** Whoever holds the source places
  them. `content validate` does that now, and `content publish` (AW-CLI-003) can do the same with
  the compile it just ran.
- **The CLI runs the gate's validator, not a copy.** `content.Validate` is the Loader's template and
  World build, factored out of `Loader.build`, without the transition checks (a removed Zone, the
  spawn Room). Those need a World in effect. Blobs go through `content.ResolveBlobs`, which is
  `Resolve`'s decoder.

If you'd rather the gate compile the published `src/` and return source positions itself, that's a
change to AW-SRV-013's response, and a story of its own.

### 3. The equivalence fixture's scope (AC-4)

`internal/contentequiv` holds the fixture set: every `valid/` and `invalid/semantic/` corpus case,
plus `content/fixtures/town`, the source of AW-SRV-001's and AW-SRV-013's
`testdata/content/valid`. Each case lists its expected findings: the corpus sidecar, or
`internal/contentequiv/testdata/town.errors`. Three runners are held to the fixture, not to each
other, so a failure names the runner that moved:

| Runner | Test | Cases |
|--------|------|------:|
| compiler (`make content-conformance`) | `internal/contentequiv` `TestCompilerAgrees`: the case's `lang.Conformance` result, and `lang.Compile` for the dev fixture | 47 |
| `andara-cli content validate --path` | `admin/cli` `TestContentValidate_AgreesWithTheEquivalenceFixture` | 47 |
| the publish gate, `Admin.PublishVersion` | `server/content` `TestPublishGateAgreesWithTheEquivalenceFixture` | 17 |

The gate runs only the cases that compile, because a pack that doesn't compile has no blobs to
publish. Two corpus cases are left out, and the fixture says why:
- `invalid/semantic/pack-mismatch` exists to show that `pack_mismatch` fires when the caller names
  the pack. `--path` has no pack name except the one the source declares.
- `valid/core`, from the gate only: andara.core isn't publishable.

Mutation-checked: when Exit findings are placed on their Room instead, both the CLI runner and the
gate runner fail.

### 4. The cache layout

The contract names the cache as `<cache>/andara.core/<M>/` and calls that "AW-CLI-006's layout".
AW-CLI-006's layout is actually `<cache>/andara.core@M/templates/`. **Built:** AW-CLI-006's actual
layout, unchanged. Only the contract text needs amending.

### 5. The embedded core applies to `compile` and `decompile` too

AC-5's lookup (embedded, then cache, then `core_version_mismatch`) is in `findCore`, and every
`content` command that resolves a core uses it. A `compile` with no cache and no network now works
against the embedded core. It used to fail pointing at `fetch-core`, and
`TestContentCompileOfflineWithoutCache` is rewritten to match. The finding's wording is in `lang` (`Options.EmbeddedCore`). Callers
that don't embed a core, such as the conformance run and the server, keep the old message.

### 6. Smaller things decided in building

- **AC-1's "naming Room, Direction, and target".** The compiler's `unknown_room` and `unknown_zone`
  messages now name all three, as in `Room "r" exits north to nowhere, and Zone "z" has no Room
  "nowhere"`. Sidecars don't pin wording, so the corpus is unchanged.
- **AC-7's short type.** `inspect template` prints `Behavior.name`, as AC-7 does, not
  `andara.core.Behavior.name`. `--output json` carries the full type. A marker Component, which
  sets no field, is printed as `Memory  (andara.core.Npc)`, naming the first Template in the chain
  that carries it.
- **AC-8's "reverse-Exit presence marked"** is `back: <direction>` or `one-way: no Exit back`.
- **`--pack` with no published sources**, as andara.core has, validates the blobs against the core
  its manifest names. Findings stay on the blob path.
- **New `content` error codes:** `validation_failed`, `blob_hash_mismatch` (a fetched blob that
  doesn't hash to its manifest entry), `unsafe_source_path` and `not_found` (an `inspect` ref), all
  exit 1. They're in `admin/README.md`.
- **`--pack` with sources that don't compile** (from Codex's review of #178). The gate validates
  the compiled blobs and keeps the sources without loading them. So `validate` still runs the
  blobs through the validator, and the blobs decide the exit. The source's findings are reported as
  warnings, prefixed `the published source does not compile:`, and without a source map the
  validator's findings stay on the blob paths.
- **Published source paths are untrusted** (Codex, #178). `checkRefs` accepts any unique path, so
  `--pack` writes a source only when its path stays under the scratch directory
  (`filepath.IsLocal`), and refuses the version as `unsafe_source_path` otherwise. The gate taking
  such a path at all is arguably AW-SRV-013's to refuse. Say if you want that as a bug.

## For SRE

### 1. `admin/cli` in `make test-integration`

AC-6's test plan asks for the `--pack` path against a throwaway Redpanda. That's
`admin/cli/validate_integration_test.go`, `TestContentValidate_PublishedVersionOverRedpanda`, under
`-tags integration`. It passes locally against the stack's Redpanda:

```
ANDARA_KAFKA_BROKERS=localhost:9092 go test -tags integration -race -count=1 \
  -run TestContentValidate_PublishedVersionOverRedpanda ./admin/cli
```

`test-integration`'s package list doesn't include `./admin/cli/`, so CI doesn't run it. Please add
it. The same flow over in-memory topics, `TestContentValidate_PublishedVersion`, runs in `make
check` today.

### 2. Instrumentation (§7)

Per `AW-CLI-001`, there are no metrics. `cli.command` is the root span, with two children:
- `content.compile` carries `files`, `zones`, `rooms`, `templates` and `diagnostics`.
- `content.validate` carries `zones`, `rooms`, `templates`, `error_count` and `warning_count`.

`TestContentValidate_EmitsTheSpans` asserts both children, their parent, and the counts.

## SRE, 2026-09-30: answered

1. **Done** on `sre/aw-cli-002-verify`. `./admin/cli/` is in `make test-integration`, so the `stack`
   workflow runs `TestContentValidate_PublishedVersionOverRedpanda`. No other package has
   integration-tagged tests the target misses.
2. **Accepted.** The §8 instrumentation check is in the story, and it's satisfied.
