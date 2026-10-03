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
2. **Not satisfied yet.** See the §8 record in the story. Two items are owed:
   - **For implementation:** the `--output json` stderr summary as one structured line (`ts`,
     `level`, `msg`, `command`, `trace_id`), per `AW-CLI-001`, with a test that decodes it.
   - **For architecture:** whether CLI spans are exported (OTLP when the environment configures it)
     or verified in-process only. No exporter exists, so as the requirement reads today, §8 can't
     verify it against a backend.

## Architecture's §8 review (2026-09-30)

Items 1–6 are ruled in the story's §8 review:
1. The array alone, as built.
2. The design is accepted. The Exit direction in the chain is recorded against `AW-SRV-013`.
3. The skips are accepted, and in-memory stands.
4. The layout is as built.
5. Accepted.
6. Filed as #267.

### For implementation: owed before `done`
- **AC-4's error-level half.** Add blob-level twins of the `invalid/semantic` cases whose codes the
  loader also raises (`errors.md` §3.2). Feed them straight to the gate, and hold them to the same
  sidecars on code and chain. Today no error-level finding passes through the gate runner.

### For implementation, not holding the story
- `admin/README.md`'s command table: `version` now prints the embedded core.
- The failure summary "N finding(s) refuse the pack" counts warnings. Count errors only.

### For implementation, also owed before `done` (from SRE's record)
- `--output json`'s stderr summary as one structured line (`ts`, `level`, `msg`, `command`,
  `trace_id`), per `AW-CLI-001`, with a test that decodes it.

### For SRE: your item 2 ruled (architecture, 2026-09-30)
**CLI spans stay in-process. The CLI gets no OTLP exporter.** The in-process assertion in CI is
their verification. The backend observation of a CLI command is the server's spans in Tempo under
the trace ID the CLI propagates. The full ruling is in the story's §8 review. Your item 1 is done
(#265). Once implementation's structured-stderr fix lands, record the instrumentation item as
satisfied.

## Implementation, 2026-09-30: the §8 items owed

On `impl/aw-cli-002-owed`. The twins found two places where the loader disagreed with the
compiler. Both are fixed in the loader, and the contract changes architecture should see are the
first three bullets:

- **`sim.ValidationError` gains `Component` and `Chain`**, additive like `Exit`.
  `invalid_component_field` carries its Component type, and `chain_too_deep` carries every
  ancestor. `content.FindingChain` composes them, so `PublishFindings`' chains now end in the
  Component (`[z, r, andara.core.Dark]`, `[p.T, andara.core.Behavior]`), and a deep chain's is the
  whole chain. Both are as the compiler reports them.
- **`fallback_missing`'s chain is the Zone alone.** The loader sets `Room` to the missing fallback.
  That's a reference, not a declaration, and the compiler's chain is `[market]`.
- **No `invalid_provenance` cascade.** A Template Component refused as invalid isn't carried, and
  the loader used to also report its provenance as `invalid_provenance`. One defect, two findings.
  The compiler raises one. Provenance for a refused Component is now skipped.
- **The `--output json` summary** on stderr, for `validate` and any command whose stdout is already
  its answer, is one JSON line: `{ts, level, msg, command, trace_id}`. Its level is `info` on
  success and `error` on failure, and it's written whatever `--log-level` is, since it's the exit
  summary rather than a log.

## Carried by `AW-SRV-046` (PM, 2026-10-02)

The "not holding the story" follow-ups for implementation above are now items in `AW-SRV-046`
(draft, SPRINT-04), which records each one as done here when it merges.

## Done in `AW-SRV-046` (implementation, 2026-10-03)

Both "not holding the story" items are done:
- **The `admin/README.md` `version` row** now names the embedded `andara.core`.
- **The failure summary counts errors only.** `TestContentValidate_SummaryCountsErrorsOnly`
  asserts `mypack: 2 finding(s) refuse the pack` for 2 errors beside 3 warnings. It drives the
  summary directly, because no input can reach it with both.
  - `mergeDiagnostics` has dropped non-errors on every refusal since this story (2d5a72f), so on
    every reachable path the old count was already errors only, and the review's premise wasn't
    reachable.
  - The count is now errors-only by construction rather than by the merge.
