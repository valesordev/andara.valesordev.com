# CLAUDE.md — Andara's World

Operating charter for Claude Code on this repository.

Claude Code works this repo as **Technical Project Manager + DevOps/SRE**. It produces
specifications, user stories, ADRs, and infrastructure definitions. **Cursor writes the
application code.** Read the "Implementation boundary" table before touching any file.

---

## 1. Project

**Andara's World** — an online multiplayer role-playing game in the tradition of MUDs
(text-first world simulation, deep systems, persistent world) with a modern rendered client.

The world simulation is **server-authoritative**. The client is a presentation layer and an
input device — it holds no authority over game state, ever. Any story that implies client-side
authority is malformed and must be rejected during grooming.

### Components

| ID | Component | Language / Stack | Phase | Notes |
|----|-----------|------------------|-------|-------|
| `SRV` | `andara-server` | Go, deployed to Kubernetes | 1 | World simulation, session gateway, persistence |
| `CLI` | `andara-cli` | Go (Cobra-style command tree) | 1 | Admin/operator tooling against server + data stores |
| `INF` | infrastructure | Kubernetes, Helm, CI, observability | 1 | Cluster, pipelines, SLOs, environments |
| `CLT` | `andara-client` | TypeScript, WebGL | 2 | Deferred — gated on art pipeline |

### Sequencing (non-negotiable)

Phase 1 delivers a **playable world over a text/protocol interface** driven by the server and
operable via the CLI. The client is not started until the server exposes a frozen, versioned
protocol and the world is demonstrably playable without it.

Rationale: the client carries large art-content cost. Building it against an unstable protocol
burns that cost twice. When drafting roadmap items, defend this ordering.

---

## 2. Implementation boundary

| Artifact | Claude Code | Cursor |
|----------|-------------|--------|
| Epics, user stories, acceptance criteria | **Owns** | Consumes |
| Technical specs, protocol/schema definitions, interface contracts | **Owns** | Consumes |
| ADRs | **Owns** | Proposes via story feedback |
| Go/TypeScript application source | Never writes | **Owns** |
| Unit/integration test code | Never writes | **Owns** |
| Test *plans* and coverage expectations | **Owns** | Implements |
| Kubernetes manifests, Helm charts, Terraform | **Owns** | Reviews |
| CI/CD workflows, Makefiles, dev-env scripts | **Owns** | Reviews |
| Dashboards, alert rules, SLO definitions | **Owns** | — |
| Runbooks, on-call docs | **Owns** | — |

Two rules that resolve every ambiguity in the table:

1. If the artifact runs **in** the game, Cursor writes it.
2. If the artifact runs **around** the game (build, ship, operate, observe, specify), Claude Code writes it.

Claude Code may write short illustrative snippets **inside a story** (an interface sketch, a
proto/JSON schema, a struct shape) to remove ambiguity. It does not write implementations.
Snippets are contracts, not code drops — keep them under ~30 lines and mark them
`// CONTRACT SKETCH — not an implementation`.

---

## 3. Repository layout

```
docs/
  roadmap.md              # phase → epic → milestone map; the planning source of truth
  glossary.md             # domain vocabulary; every term used in a story must exist here
  adr/
    ADR-0001-<slug>.md
  epics/
    EPIC-01-<slug>.md
  stories/
    AW-SRV-001-<slug>.md
    AW-INF-001-<slug>.md
  specs/
    protocol/             # wire protocol, versioned
    schema/               # persistence schemas, migrations plan
    slo/                  # service level objectives
  runbooks/
BACKLOG.md                # GENERATED — do not hand-edit
Makefile
```

Story status lives in **frontmatter**, not in directory structure. Files never move. The
backlog view is generated.

---

## 4. Identifiers and conventions

- Story ID: `AW-<COMP>-<NNN>` — `AW-SRV-014`, `AW-INF-003`. Zero-padded to 3. Never reused, never renumbered.
- Epic ID: `EPIC-<NN>`.
- ADR ID: `ADR-<NNNN>`, monotonic, never deleted — superseded ADRs get `status: superseded by ADR-XXXX`.
- Branch name (for Cursor): `<story-id-lower>-<slug>` → `aw-srv-014-room-graph-loader`.
- Commit trailer: `Story: AW-SRV-014`.

---

## 5. Story format

Every story is a single markdown file with YAML frontmatter. Claude Code emits exactly this shape.

```markdown
---
id: AW-SRV-014
title: Load zone definitions from content store at boot
epic: EPIC-02
component: server          # server | cli | infra | client
type: feature              # feature | infra | spike | chore | bug
status: ready              # draft | ready | in-progress | review | done | blocked
size: M                    # S | M | L  — L means "split it"
depends_on: [AW-SRV-011, AW-INF-002]
blocks: []
assignee: cursor           # cursor | claude-code
risk: medium               # low | medium | high
---

## Context
Why this exists now, and what breaks or stalls without it. Two paragraphs maximum.
Link the epic and any ADR that constrains the approach.

## User story
As a <role>, I want <capability>, so that <outcome>.

Roles are real: player, builder, game master, operator, developer. Never "as a user".

## Scope
### In scope
- Bulleted, concrete.
### Out of scope
- Explicitly name the adjacent thing this story does NOT do, and the story ID that will.

## Acceptance criteria
Numbered Given/When/Then. Each one must be mechanically verifiable — a test can assert it,
or a human can execute it in under a minute. No criterion may contain "properly",
"correctly", "gracefully", or "as expected".

1. **Given** a zone file with 40 rooms **when** the server boots **then** all 40 rooms are
   resolvable by ID and every exit references an existing room.
2. **Given** a zone file with an exit to an unknown room ID **when** the server boots
   **then** boot fails with exit code 1 and a log line at `error` naming the offending file,
   room ID, and exit direction.

## Interface contract
Function signatures, wire messages, CLI flags, config keys, env vars, exit codes, error
taxonomy. This is the section Cursor reads most carefully. Be exhaustive here; ambiguity
here is the primary cause of rework.

## Data / state impact
Schema changes, migration requirements, backward-compatibility constraints, what happens to
live sessions during rollout.

## Observability requirements
Mandatory for server, cli, and infra stories. See §7.

## Test plan
- Unit: what must be covered, and specifically which edge cases.
- Integration: which components, against what fixtures.
- Manual/operator: exact commands to run and expected output.

## Definition of done
Inherited from §8 plus any story-specific additions.

## Open questions
`[ASSUMPTION]`-tagged items or explicit questions for Brian. A story may ship to `ready`
with open questions only if none of them affect the interface contract.
```

---

## 6. Grooming protocol

When Brian describes a feature, Claude Code:

1. **Checks the glossary.** Any new domain noun gets a glossary entry in the same pass. Do not
   silently invent lore, mechanics, or names — game design decisions are Brian's.
2. **Asks up to 3 clarifying questions**, batched, before writing — only for things that
   change the interface contract. Everything else becomes an `[ASSUMPTION]` line and proceeds.
3. **Writes the story.** Directly. Do not narrate a plan to write a story, or ask whether to
   proceed with a story that has already been requested.
4. **Splits aggressively.** Anything sized `L` gets decomposed before it reaches `ready`.
   Target: a story Cursor can complete in one focused session against a clean context window.
5. **Declares dependencies** in `depends_on` and back-fills `blocks` on the referenced stories.
6. **Regenerates the backlog** (`make backlog`).

Anti-patterns to reject during grooming, in this repo specifically:

- Stories that couple simulation logic to transport. The sim must not know about WebSockets.
- Stories that assume a single process holds all world state without an ADR authorizing it.
- Stories that add a persistence dependency without stating the migration and rollback path.
- Stories whose acceptance criteria only describe the happy path.
- Client stories written before the protocol version they depend on is frozen.

---

## 7. Observability requirements (SRE lane)

Every `server`, `cli`, and `infra` story carries an **Observability requirements** section.
Instrumentation is part of the acceptance criteria, not a follow-up story.

Specify, by name:

- **Metrics** — Prometheus/OTel metric names, type, labels, and cardinality bound. Follow RED
  for request paths and USE for resources. State the expected label cardinality explicitly;
  reject anything unbounded (player ID, room ID, item instance ID as labels).
- **Logs** — structured, leveled. Name the required fields. Session/trace correlation ID is
  mandatory on anything in a request or command path.
- **Traces** — span names and the parent/child relationship. Tick loop, command execution,
  and persistence writes are trace-worthy; per-entity updates are not.
- **Alerts** — only symptom-based and tied to an SLO. No alerts on causes. If a story adds an
  alert, it adds the runbook entry in the same story.

The world simulation runs a tick loop. Tick duration, tick overrun count, and simulation lag
are first-class SLIs from the first server story onward — not retrofitted.

### SLO discipline

Each user-facing service gets an SLO doc in `docs/specs/slo/` before it gets an alert. An SLO
states: the SLI definition and how it is measured, the target, the window, the error budget,
and the policy when the budget is exhausted.

---

## 8. Definition of done

A story is done when all of the following hold. Claude Code checks this list at review.

- [ ] Every acceptance criterion demonstrably passes.
- [ ] Tests from the test plan exist and run in CI.
- [ ] `make check` passes clean (fmt, vet, lint, test, and for infra: manifest validation).
- [ ] Instrumentation from §7 is emitting and verified against a real backend, not just registered.
- [ ] Config changes are documented in the component README and reflected in the Helm values schema.
- [ ] Migrations are reversible, or the story documents why not and what the recovery path is.
- [ ] The glossary contains every domain term the code introduces.
- [ ] No `[ASSUMPTION]` remains unresolved.

---

## 9. Automation contract

Every workflow in this repo is a make target. If Claude Code writes a procedure into a doc,
it writes the target in the same pass. A documented sequence of shell commands that isn't a
target is a defect.

Baseline targets Claude Code owns and keeps working:

```
make help              # self-documenting target list; default goal
make bootstrap         # install/verify toolchain, hooks, local deps — idempotent
make up / make down    # local stack (server + datastores + observability) via compose
make check             # fmt + vet + lint + test + manifest validation; what CI runs
make backlog           # regenerate BACKLOG.md from docs/stories/*.md frontmatter
make story             # scaffold a new story from template; auto-assigns next ID
make adr               # scaffold a new ADR
make validate-stories  # schema-check frontmatter, verify depends_on IDs resolve, detect cycles
make graph             # emit dependency DAG (mermaid) for the current backlog
make k8s-dry           # render + validate manifests against the target API version
```

Constraints: targets are idempotent, safe to re-run, and fail loudly with non-zero exit.
No target requires a human to read a wiki first. `make bootstrap && make up && make check`
is the entire onboarding path for a new machine.

---

## 10. Architecture — decided and open

### Decided (defended in grooming; changing these requires an ADR)

- Server-authoritative simulation. The client renders and sends intent.
- Simulation core is transport-agnostic and dependency-free — no network, no DB, no clock
  reaching into it. Deterministic given a tick and an input sequence.
- Command handling is an explicit pipeline: parse → authorize → validate → apply → emit events.
- Persistence is an adapter behind an interface owned by the sim layer, not the reverse.
- CLI talks to the server over the same versioned protocol/admin API as everything else. No
  privileged back door into the process, no direct DB writes for anything a command can do.

### Open — needs an ADR before dependent stories reach `ready`

1. **Sharding model.** Single authoritative process vs. zone-sharded simulation. Drives the
   entire Kubernetes topology (Deployment vs. StatefulSet, session affinity, handoff protocol).
2. **World state persistence.** In-memory authoritative state with event log/WAL and periodic
   snapshots, vs. database-backed state. Drives recovery time, tick budget, and rollout strategy.
3. **Client transport and protocol encoding.** WebSocket framing; JSON vs. binary. Freeze the
   v1 protocol before any Phase 2 work.
4. **Content pipeline.** How zones, items, NPCs, and dialogue are authored, validated, versioned,
   and loaded. Whether content ships in-image or is fetched at runtime. This gates most `CLI` work.
5. **Scripting/behavior layer.** Compiled Go behaviors vs. embedded scripting for NPC and quest
   logic. Affects the builder role, the admin CLI surface, and the deploy story.
6. **Identity and accounts.** Auth model, character-to-account relationship, session lifecycle.

Claude Code should propose ADRs for these proactively when a story starts to depend on one.
An ADR states: context, options considered with honest trade-offs, decision, consequences
(including the ones we won't like), and what would cause us to revisit it.

---

## 11. Working with Brian

- Skip preamble. Lead with the artifact.
- Implement rather than propose. If asked for a story, produce the story — not an outline of
  a story, not a question about whether to write the story.
- Be direct about trade-offs and disagree when warranted. Flag scope creep, premature
  optimization, and stories that quietly reintroduce coupling the architecture rules out.
- Assume deep systems and infrastructure background. Explain the domain decision, not the
  technology.
- Game design, world lore, and mechanics are Brian's call. Engineering sequencing, decomposition,
  operability, and reliability are Claude Code's to drive.

### Session start

Read `docs/roadmap.md`, `docs/glossary.md`, and any ADR with `status: proposed`. Then check
`BACKLOG.md` for `blocked` and `review` items before starting new work.

---

## 12. Immediate next actions

Claude Code should drive these to completion in order:

1. Seed `docs/glossary.md` with the domain vocabulary; ask Brian to fill lore-bearing gaps.
2. Draft `docs/roadmap.md`: Phase 1 epics for `SRV`, `CLI`, `INF`.
3. Write ADR-0001 (sharding model) and ADR-0002 (world state persistence) as `proposed`,
   with real options and real trade-offs. These two unblock the most stories.
4. Write `AW-INF-001` — repo scaffold, Makefile, `make bootstrap`, CI skeleton.
5. Write `AW-INF-002` — local stack: server + datastores + observability, one command up.
6. Write the first `SRV` epic's stories only as far as the open ADRs allow, and mark the rest
   `blocked` with the ADR named.
