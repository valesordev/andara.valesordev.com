# CLAUDE.md — Andara's World

Operating charter for Claude Code on this repository.

Three Claude Code agents work this repo, each in its own clone: **project management** grooms
stories and plans sprints, **architecture** owns contracts and everything around the game, and
**implementation** builds the game. Each clone's parent directory holds a lane `CLAUDE.md` that
narrows this file to one agent's job. Read §2 before touching any file.

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

## 2. Lane discipline

Work in this repo belongs to one of two lanes. Every story declares which in its `lane`
frontmatter field, and `docs/status.md` reports the two separately. Project management is an
agent, not a lane: it decides what gets built and in what order, and it builds nothing, so no
story ever has `lane: pm`.

| Lane | Produces | Examples |
|------|----------|----------|
| `architecture` | The contract's technical truth, and everything that builds, ships, operates, or observes what runs to it | ADRs, protocol and schema definitions, contract review, Helm charts, CI, Makefiles, dashboards, SLOs, runbooks |
| `implementation` | The thing built to the contract | Go and TypeScript application source, unit and integration tests |

One rule resolves every ambiguity: if the artifact runs **in** the game it is
`implementation`; if it runs **around** the game — build, ship, operate, observe, specify —
it is `architecture`.

Under that rule, `content/` is implementation, like `server/`, `admin/`, and `client/`.
`content/lang` compiles what the game loads, and `content/core` is the `andara.core` seed it loads.
The language's specification and conformance corpus, `docs/specs/content-language/`, are
architecture's. *(Stated 2026-09-24; `AW-CLI-006` found no directory list naming `content/`.)*

**Why the lanes exist.** The split was never really about who held the keyboard; it was about
not letting the contract be written by the code. The rule that carries that forward is short:

> A story reaches `status: ready` — its interface contract written — *before* its
> implementation starts. Writing both in one pass means the contract is whatever the code
> happened to do, and there is nothing left to check the code against.

Two habits enforce it in practice:

1. Groom and implement in **separate sessions**. The three agents make this structural: PM
   writes the story, architecture reviews its contract and moves it to `ready`, and only then
   does the lane named in `lane:` build it. Grooming a story and then implementing it in the
   same context would make the story a memory of intent rather than a specification anything
   can be verified against.
2. A story still contains **contracts, not code drops**. Illustrative snippets inside a story
   (an interface sketch, a proto or JSON schema, a struct shape) stay under ~30 lines and are
   marked `// CONTRACT SKETCH — not an implementation`. If a snippet grows past that, it wants
   to be the implementation, and the implementation belongs on a branch.

### Agents and ownership

| Agent | Branch prefix | Owns | Status moves |
|-------|---------------|------|--------------|
| PM | `pm/` | `docs/sprints/`, `docs/roadmap.md`, `docs/epics/`, new stories in full (contract included), GitHub milestones and triage | creates stories at `draft` |
| Architecture | `arch/` | Contract review; `docs/adr/`, `docs/specs/`, `docs/runbooks/`; `deploy/`, `Makefile`, `scripts/`, `.github/`, `buf.gen.yaml`; `lane: architecture` stories; the §8 review for both lanes | `draft` → `ready` / `blocked`; `review` → `done` |
| Implementation | `impl/` | `server/`, `internal/`, `cmd/`, `admin/`, `content/`, `agents/`, `client/`, `testdata/`; `lane: implementation` stories | its own stories: `ready` → `in-progress` → `review` |

Architecture moves its own `lane: architecture` stories through `in-progress` and `review` the
same way implementation does. A question for another agent goes in
`docs/feedback/<story-id>-<slug>.md`, under a heading naming the agent that should answer.

### The sprint cycle

Work runs in sprints. `docs/sprints/SPRINT-NN.md` is the plan, and exactly one sprint file
carries `Status: active`.

1. **PM, at the boundary** (one session, one `pm/sprint-NN-<slug>` PR): closes out the active
   sprint, writes `docs/sprints/SPRINT-NN-demo.md` for what it delivered, grooms, and plans the
   next sprint. Each sprint has at least one demo goal an operator can run, tied to a milestone
   gate in `docs/roadmap.md` or a slice of one.
2. **Architecture** reviews the contracts of the sprint's drafts first, since implementation
   waits on them. Next comes the §8 review of anything at `review`, then its own backlog.
3. **Implementation** takes the first story in its sprint list that is `ready` with every
   `depends_on` at `review` or later.
4. Architecture and implementation work only on stories in the active sprint. A lane with
   nothing pickable stops and reports; it doesn't pull work from outside the sprint. No active
   sprint means PM hasn't planned one yet, so stop. The sprint ends when every story is `done`
   or PM carries it over.

Demo instructions obey §9: every step is a `make` target or a product command. A step that
needs a hand-written shell sequence is marked `§9 defect → AW-INF-NNN`, and that story goes
into the next sprint.

---

## 3. Repository layout

```
docs/
  status.md               # GENERATED — two-lane state, one screen
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
  sprints/
    SPRINT-01.md          # plan and close-out; exactly one is `Status: active`
    SPRINT-01-demo.md     # operator demo instructions, written at close-out
  feedback/
    AW-SRV-012-<slug>.md  # cross-agent questions and deviations for one story
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
- Branch name: `<prefix>/<story-id-lower>-<slug>` → `impl/aw-srv-014-room-graph-loader`, with
  the owning agent's prefix from §2. PM branches are `pm/sprint-NN-<slug>`, and §8 reviews go
  on `arch/…-review`. Never commit to another agent's branch or directly to `main`.
- Commit trailer: `Story: AW-SRV-014`, or `Sprint: SPRINT-NN` for PM commits.
- Sprint ID: `SPRINT-<NN>`, monotonic.
- **Commits are signed.** `main` is branch-protected to require signatures (2026-09-22), so an
  unsigned commit cannot merge. `make bootstrap` configures per-repo SSH signing from the key
  that already pushes to origin, and `.github/allowed_signers` is the tracked list of trusted
  keys. History before that date is unsigned and stays that way: re-signing 152 commits would
  change every SHA, break the other lane's clone, and dangle the commit references the
  verification records in `docs/stories/` cite.

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
lane: implementation       # architecture (contracts, infra) | implementation (source)
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
taxonomy. This is the section the implementation lane reads most carefully, and the only
defence against a story that means whatever the code turns out to do. Be exhaustive here;
ambiguity here is the primary cause of rework.

## Data / state impact
Schema changes, migration requirements, backward-compatibility constraints, what happens to
live sessions during rollout.

## Observability requirements
Mandatory for server, cli, and infra stories. See §7.

## Test plan
- Unit: what must be covered, and specifically which edge cases.
- Integration: which components, against what fixtures.
- Manual/operator: exact commands to run and expected output.

Any criterion asserting on a metric, projection, stream, or stored object follows
`docs/specs/testing/live-assertions.md` — poll the assertion itself to a deadline, never read once.

## Definition of done
Inherited from §8 plus any story-specific additions.

## Open questions
`[ASSUMPTION]`-tagged items or explicit questions for Brian. A story may ship to `ready`
with open questions only if none of them affect the interface contract.
```

---

## 6. Grooming protocol

PM grooms, from Brian's feature descriptions and from the roadmap. Stories are written in full,
Interface contract included, and left at `draft`. Architecture's contract review moves them to
`ready`. When Brian describes a feature, PM:

1. **Checks the glossary.** Any new domain noun gets a glossary entry in the same pass. Do not
   silently invent lore, mechanics, or names — game design decisions are Brian's.
2. **Asks up to 3 clarifying questions**, batched, before writing — only for things that
   change the interface contract. Everything else becomes an `[ASSUMPTION]` line and proceeds.
3. **Writes the story** from decided ADRs and specs. If the contract needs a decision no ADR
   covers, the story stays `draft`, its question goes to architecture in `docs/feedback/`, and
   it stays out of the sprint. Otherwise write it directly. Do not narrate a plan to write a story, or ask whether to
   proceed with a story that has already been requested.
4. **Splits aggressively.** Anything sized `L` gets decomposed before it reaches `ready`.
   Target: a story one focused session can complete against a clean context window.
5. **Declares dependencies** in `depends_on` and back-fills `blocks` on the referenced stories.
6. **Regenerates the backlog and status** (`make backlog status`).

Architecture's contract review checks the same list, amends what's wrong, and moves the story to
`ready`, or to `blocked` naming what it waits on. A contract change after `ready` is recorded in
the story's body, and in its feedback file if implementation has started.

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

A story is done when all of the following hold. Architecture runs this checklist at review for
both lanes' stories, on an `arch/…-review` branch, and moves the story from `review` to `done`.

- [ ] Every acceptance criterion demonstrably passes.
- [ ] Tests from the test plan exist and run in CI.
- [ ] `make check` passes clean (fmt, vet, lint, test, and for infra: manifest validation).
- [ ] Instrumentation from §7 is emitting and verified against a real backend, not just registered.
- [ ] Config changes are documented in the component README and reflected in the Helm values schema.
- [ ] Migrations are reversible, or the story documents why not and what the recovery path is.
- [ ] The glossary contains every domain term the code introduces.
- [ ] No `[ASSUMPTION]` remains unresolved.

**"Verified against a real backend" when no in-cluster caller exists yet.** Stories land
bottom-up, so a story's instruments are often unreachable from the running server until a later
story supplies the caller (a bound Character, a subscriber). The check is then satisfied by the
integration suite exercising the story's own code against the local stack's backends (Redpanda,
Tempo) with assertions on the metric objects, plus a scrape of the same registry's sibling series
from the server. The story's §8 record says exactly which series the server itself has not yet
emitted, and the first story that can make the live observation carries it as an inherited
Definition-of-done line. Deferring the observation this way is not deferring the check; holding a
story in `review` until a caller two milestones away lands is what `review` does not mean.

---

## 9. Automation contract

Every workflow in this repo is a make target. If Claude Code writes a procedure into a doc,
it writes the target in the same pass. A documented sequence of shell commands that isn't a
target is a defect.

Baseline targets the architecture agent owns and keeps working:

```
make help              # self-documenting target list; default goal
make bootstrap         # install/verify toolchain, hooks, local deps — idempotent
make up / make down    # local stack (server + datastores + observability) via compose
make check             # fmt + vet + lint + test + manifest validation; what CI runs
make backlog           # regenerate BACKLOG.md from docs/stories/*.md frontmatter
make status            # regenerate docs/status.md — what to prompt next in each lane
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

Architecture proposes ADRs for these proactively when a story starts to depend on one.
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
- Game design, world lore, and mechanics are Brian's call. Sequencing and decomposition are PM's to
  drive; operability, reliability, and contracts are architecture's.

### Session start

`git fetch origin` and start from `origin/main`. Read the `Status: active` sprint in
`docs/sprints/` first: it is the work, in order. Then `docs/status.md`, which names the story in
flight in each lane and the decisions the lanes are waiting on. Then `docs/roadmap.md`, `docs/glossary.md`, and any
ADR with `status: proposed`. `BACKLOG.md` is the full view when `status.md` is not enough.

`status.md` is only as honest as story frontmatter, so status travels with the work: `ready` →
`in-progress` in the branch's first commit, and `in-progress` → `review` in the PR that delivers
it. A story that merges still at `ready` makes `status.md` offer finished work as next. Any commit
that changes frontmatter runs `make backlog status` in the same commit, and `make check` fails if
either is stale. On a rebase conflict in `BACKLOG.md` or `docs/status.md`, never merge by hand:
take either side and re-run the targets.

