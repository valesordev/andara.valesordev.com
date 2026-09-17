---
id: AW-SRV-001
title: World model types and zone definition loading with referential validation at boot
epic: EPIC-02
component: server
type: feature
status: done
size: M
depends_on: [AW-INF-001, AW-SRV-020]
blocks: [AW-SRV-002, AW-SRV-003, AW-SRV-006, AW-SRV-012, AW-CLI-002, AW-SRV-021, AW-SRV-024]
lane: implementation
risk: medium
---

## Context

The simulation core needs something to simulate. This story establishes the Room graph — Zones,
Rooms, Exits, and the Zone-scoped state boundary that ADR-0001 makes load-bearing — and the boot
path that turns authored Zone Definitions into that graph, refusing to start on a broken World.

Fail-fast is the whole point, and ADR-0004 raised the stakes: Builders publish content without
repository access, so this validator is the security gate as well as the convenience. It runs in three
places — the Builder's CLI, server-side at publish, and at load — from **one implementation**, written
once, here, in the sim core. A rule that exists in one of the three and not the others is a defect.

## User story

As a builder, I want the server to refuse to boot on a Zone with a broken reference and tell me
exactly which file, room, and exit is wrong, so that I find my mistake before anyone plays it.

## Scope

### In scope
- `Zone`, `Room`, `Exit`, `EntityID`, `RoomID`, `ZoneID` types in the sim core.
- A Zone Definition file format sufficient for Rooms and Exits, with a `format_version` field.
- A loader that reads a directory of Zone Definition files into an immutable World topology.
- A validator: schema conformance, referential integrity, uniqueness, orphan detection.
- Cross-Zone Exits resolved at load time as ZoneID+RoomID pairs, never as pointers (ADR-0001 seam).
- The Zone→Partition mapping, `hash(ZoneID) % 64`, computed at load and exposed on the Zone.

### Out of scope
- Items, NPCs, Behaviors, dialogue — those are Content, and their definition formats arrive with
  `EPIC-05` under ADR-0004.
- Hot reload of content into a running World — ADR-0004 decides whether that exists at all.
- Content delivery. Per ADR-0004 content lives in Kafka, resolved by `AW-SRV-012`. This story takes
  `[]ZoneDefinition` values and does not care where they came from — which is exactly what makes the
  same validator usable from the CLI, from the publish path, and at load.
- Any mutable World state. This story produces topology only; mutable state arrives with `AW-SRV-002`.

## Acceptance criteria

1. **Given** a directory containing one Zone file with 40 Rooms **when** the World is loaded **then**
   all 40 Rooms are resolvable by `RoomID` and every Exit resolves to an existing Room.
2. **Given** a Zone file with an Exit whose target Room ID does not exist **when** the server boots
   **then** boot fails with exit code 1 and a log line at `error` naming the file path, the source
   Room ID, the Exit direction, and the unresolved target ID.
3. **Given** two Zone files that both declare `RoomID` `market-square` in the same Zone **when** the
   World is loaded **then** load fails with exit code 1 naming both file paths and the duplicated ID.
4. **Given** a Zone file declaring an Exit to a Room in a different, present Zone **when** the World
   is loaded **then** the Exit resolves and is marked as crossing a Zone boundary.
5. **Given** a Zone file declaring an Exit to a Room in a Zone that is not loaded **when** the World
   is loaded **then** load fails with exit code 1 naming the missing `ZoneID`.
6. **Given** a Zone containing a Room reachable by no Exit from any other Room in that Zone **when**
   the World is loaded **then** load succeeds and emits a `warn` log line naming the orphan Room ID.
   Orphans are legal (a Builder may be mid-work) but never silent.
7. **Given** a Zone file whose `format_version` is greater than the loader supports **when** the
   World is loaded **then** load fails with exit code 1 naming both the file's version and the
   supported range.
8. **Given** a Zone file that is not valid syntax for the chosen format **when** the World is loaded
   **then** load fails with exit code 1 naming the file and the line number.
9. **Given** an empty content set **when** the server boots **then** boot fails with exit code 1
   stating that no Zones were found and naming the content source. An empty World is a configuration
   error, not an empty World.
10. **Given** the same content loaded twice in the same process **when** the resulting topologies are
    compared **then** they are byte-identical when serialized, including the order of Exits within a
    Room. Load order must not be map-iteration dependent.
11. **Given** two Rooms in the same Zone **when** their Partition is computed **then** it is identical,
    because a Zone is the unit of simulation authority (ADR-0001) and a Zone split across Partitions
    would be unsimulatable.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation

package sim

type ZoneID string
type RoomID string   // unique within a Zone
type Direction string

// RoomRef addresses a Room across Zone boundaries. Cross-Zone references are values,
// never pointers — ADR-0001 seam invariant.
type RoomRef struct {
    Zone ZoneID
    Room RoomID
}

type Exit struct {
    Direction Direction
    To        RoomRef
    CrossZone bool     // computed at load; To.Zone != containing Zone
}

type Room struct {
    ID          RoomID
    Title       string
    Description string
    Exits       []Exit   // stable, sorted by Direction — load must be deterministic
}

type Zone struct {
    ID        ZoneID
    Name      string
    Rooms     map[RoomID]*Room
    Partition int32   // hash(ID) % 64 — the unit of simulation authority (ADR-0001)
}

// World is the immutable topology. Mutable state lives elsewhere (AW-SRV-002).
type World struct {
    Zones map[ZoneID]*Zone
}

func (w *World) Resolve(ref RoomRef) (*Room, bool)

// ValidationError carries everything a Builder needs to fix the problem without
// opening the loader source.
type ValidationError struct {
    File    string
    Line    int      // 0 when not line-scoped
    Zone    ZoneID
    Room    RoomID
    Code    ErrCode
    Detail  string
}

type ErrCode string

const (
    ErrUnknownRoom      ErrCode = "unknown_room"
    ErrUnknownZone      ErrCode = "unknown_zone"
    ErrDuplicateRoom    ErrCode = "duplicate_room"
    ErrDuplicateZone    ErrCode = "duplicate_zone"
    ErrUnsupportedVersion ErrCode = "unsupported_format_version"
    ErrMalformed        ErrCode = "malformed_file"
    ErrEmptyContent     ErrCode = "no_zones_found"
)
```

Loading lives outside `server/sim` — it touches Kafka and the filesystem, which the sim core may not.
The sim core exposes `func BuildWorld(defs []ZoneDefinition) (*World, []ValidationError)`; callers
supply `ZoneDefinition` values from wherever they have them. This is what makes one validator serve
`andara-cli content validate` on a Builder's laptop, the server-side publish gate, and the boot load.

`ZoneDefinition` is a generated protobuf type from `AW-SRV-020` — a dependency that lived only in this
prose until 2026-09-09 and is now declared in frontmatter, where `make validate-stories` can see it. So
the same definition travels over the
wire, through Kafka, and into the validator without a translation layer.

### Configuration

| Key | Env | Default | On failure |
|-----|-----|---------|------------|
| `content.source` | `ANDARA_CONTENT_SOURCE` | `kafka` | `kafka` or `dir` (dir is for tests and the CLI) |
| `content.path` | `ANDARA_CONTENT_PATH` | `./content` | used only when `content.source=dir` |
| `content.strict_orphans` | `ANDARA_STRICT_ORPHANS` | `false` | when `true`, orphan Rooms are errors, not warnings |

### Exit codes

`0` success. `1` any validation or load failure. The process prints every validation error before
exiting, not just the first — a Builder fixing ten broken exits should need one boot, not ten.

## Data / state impact

No persistent state. Topology is derived from content at every boot and is never written back. There
is no migration because there is nothing stored.

`format_version` in the Zone Definition file is the forward-compatibility hook and must be present
from the first file ever authored. The loader supports a declared closed range; widening it is a
deliberate change.

## Observability requirements

### Metrics
- `andara_content_zones_loaded` — gauge. Labels: none. Cardinality: 1.
- `andara_content_rooms_loaded` — gauge. Labels: `zone`. Cardinality: bounded by Zone count;
  acceptable because Zones are authored and countable in the tens. Room ID as a label is rejected.
- `andara_content_load_duration_seconds` — histogram. Labels: none. Cardinality: 1 series.
- `andara_content_validation_errors_total` — counter. Labels: `code` (the `ErrCode` enumeration).
  Cardinality: bounded by the enum, currently 7.

### Logs
Structured, one line per validation finding, at `error` for failures and `warn` for orphans.
Required fields: `ts`, `level`, `msg`, `service`, `env`, `code`, `file`, `zone`, `room`, `detail`,
and `line` when line-scoped. Boot-path logs carry no `session_id` — there is no Session yet — but
must carry the boot `trace_id`.

### Traces
- `content.load` — root span for the boot load, attributes `zone_count`, `room_count`.
- `content.validate` — child span, attribute `error_count`.
Per-Zone spans are acceptable (tens). Per-Room spans are not.

### Alerts
None. A boot failure is visible as a crash-looping pod, which `AW-INF-003` alerts on as a symptom.
An alert on content validation specifically would be an alert on a cause (CLAUDE.md §7).

## Test plan

- **Unit:** `BuildWorld` against fixtures for every `ErrCode`; determinism test asserting two loads
  of the same input serialize identically; Exit sort stability; cross-Zone reference resolution;
  orphan detection with `strict_orphans` both true and false.
- **Integration:** boot the server against a fixture content directory of three Zones with a
  cross-Zone Exit; assert exit 0 and correct metric values. Boot against each broken fixture; assert
  exit 1 and the exact log fields.
- **Manual/operator:**
  ```
  ANDARA_CONTENT_PATH=./testdata/content/valid   ./andara-server --validate-only ; echo $?   # 0
  ANDARA_CONTENT_PATH=./testdata/content/dangling ./andara-server --validate-only ; echo $?  # 1
  ```
  `--validate-only` loads and validates, prints findings, and exits without starting the Tick Loop.

## Definition of done

CLAUDE.md §8, plus:
- The determinism test from AC-10 is in CI and gates merges.
- The import boundary from `AW-INF-001` passes: `server/sim` imports no filesystem package.
- Every new noun (`Zone`, `Room`, `Exit`, `Direction`, `Zone Definition`) is in `docs/glossary.md`.

### Checked at review, 2026-09-10

Every item holds except one, which cannot yet be satisfied by anything:

- §8 "config changes reflected in the Helm values schema" — **deferred, not met.** `deploy/helm/`
  holds a `.gitkeep`; there is no chart until `AW-INF-003`, which is still `draft`. The three keys
  this story adds — `content.source`, `content.path`, `content.strict_orphans` — are documented in
  `server/README.md` and must land in the values schema when the chart does. **`AW-INF-003` inherits
  that obligation.** Holding this story at `review` for it was considered and rejected: a dependency
  counts as met at `review`, so waiting unblocks nobody and leaves a finished story looking unfinished.
- §8 "instrumentation emitting and verified against a real backend" — met the hard way. The four
  metrics were read back out of Prometheus's TSDB after a real scrape of `andara-server:8080`, both
  spans out of Tempo with their attributes and parent/child edge intact, and an `orphan_room` finding
  line's `trace_id` resolved to its own trace. That exercise found a defect no unit test could:
  traces carried no resource, so they arrived as `unknown_service:andara-server` and were unfindable
  by the service name the dashboards query. Fixed, and pinned by a test that reads the resource off
  an exported span.

## Open questions

All three resolved on 2026-09-10. Kept rather than deleted: a story that shows what it decided and
what it handed on is worth more later than one that shows only its conclusion.

- ~~`[ASSUMPTION]` One Zone per definition.~~ **Confirmed by Brian, 2026-09-10.** One Zone per
  definition file is the decision, not an assumption. The loader enforces it — a second definition
  declaring a ZoneID already seen is `duplicate_zone` — and `zone.proto` encodes it by giving
  `ZoneDefinition` a single `id`. `format_version` remains the hook if multi-Zone bundles are ever
  wanted; they would be a new format version, not a reinterpretation of this one.
- **Resolved 2026-09-10 (Brian): Rooms and Zones do carry Components** (ADR-0010 decision 8, now
  accepted). The instruction this story gave itself — *do not design `Room`, `Zone`, or the content
  schema in a way that makes adding a component set a breaking change* — was followed, and it paid:
  `zone.proto` left `RoomDefinition` field 5 open and unreserved, and `Room` is a plain struct that
  gains a field. On the implementation side the same instruction shaped `CanonicalBytes`, which
  serializes named fields and hashes no field set, so a Room that gains a component set does not
  change the encoding of a Room that has none. **This story is not reopened.** `AW-SRV-021` adds the
  component set.
- **Resolved 2026-09-10 (Brian): the canonical `Direction` set is closed** — the twelve in
  `docs/glossary.md`, each with a reverse. `direction` stays a string on the wire; the set is enforced
  in the loader, so `norht` becomes a boot failure naming the file and line. That enforcement does not
  exist yet — this story shipped with Direction opaque — and it is `AW-SRV-021`'s to add.
- Per ADR-0007 the canonical format is protobuf. The Builder-facing authoring surface that compiles
  to it is `AW-CLI-003`'s problem — flagged here because protobuf is not hand-authorable and pretending
  otherwise would make Builders hate this. Unchanged; dir-mode protobuf JSON is the test and CLI
  surface, not the Builder language (ADR-0009).

### Found at review, handed to the architecture lane

Neither is a defect in this story's code; both are places the contract and the automation should
catch up, and neither is the implementation lane's to change.

1. **This story's own manual/operator command does not work as written.** The Test plan shows
   `ANDARA_CONTENT_PATH=./testdata/content/valid ./andara-server --validate-only` expecting `0`. It
   exits `1`: `content.source` defaults to `kafka`, so it fails with `no_zones_found` naming kafka.
   It needs `ANDARA_CONTENT_SOURCE=dir`, which both component READMEs have right — which is exactly
   why nobody would notice.
2. **The stack CI job does not assert this story's instrumentation.** `.github/workflows/stack.yaml`
   round-trips a *synthetic* `command.execute` span posted by `curl`, and its scrape-target check
   still says "andara-server is expected down until AW-SRV-005 lands" — stale, since the server
   profile is live and the container is healthy. The real `content.load` span and
   `andara_content_zones_loaded` in Prometheus were verified by hand for this review; a job that
   asserts them is what stops the next person having to.
