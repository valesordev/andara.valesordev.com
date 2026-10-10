# CLAUDE.md — Andara's World

Operating charter for Claude Code on this repository.

Four Claude Code agents work this repo, each in its own clone or cloud session: **project
management** grooms stories and plans sprints, **architecture** owns the contracts, **SRE** owns
how the game is built, shipped, operated, and observed, and **implementation** builds the game.
Every session starts with `/role <pm|architecture|sre|implementation>`, which loads the role
charter from `.claude/roles/` that narrows this file to one agent's job. Read §2 before touching
any file.

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

Work in this repo belongs to one of three lanes. Every story declares which in its `Lane` field
on the Project, and `story list --lane <lane>` shows each lane's work. A lane names the role that
builds a story. Project management is a role but not a lane: it decides what gets built and in
what order, and it builds nothing, so no story ever has `lane: pm`.

| Lane | Produces | Examples |
|------|----------|----------|
| `architecture` | The contract's technical truth | ADRs, protocol and schema definitions, contract review, the §8 review |
| `sre` | Everything that builds, ships, operates, or observes what runs to the contract | Helm charts, CI, Makefiles, dashboards, alerts, SLOs, runbooks |
| `implementation` | The thing built to the contract | Go and TypeScript application source, unit and integration tests |

One rule resolves every ambiguity: if the artifact runs **in** the game it is
`implementation`; if it runs **around** the game, it is `architecture` when it *specifies*
and `sre` when it *builds, ships, operates, or observes*.

Under that rule, `content/` is implementation, like `server/`, `admin/`, and `client/`.
`content/lang` compiles what the game loads, and `content/core` is the `andara.core` seed it loads.
The language's specification and conformance corpus, `docs/specs/content-language/`, are
architecture's. *(Stated 2026-09-24; `AW-CLI-006` found no directory list naming `content/`.)*

**Why the lanes exist.** The split was never really about who held the keyboard; it was about
not letting the contract be written by the code. The rule that carries that forward is short:

> A story reaches `ready` — its interface contract written — *before* its
> implementation starts. Writing both in one pass means the contract is whatever the code
> happened to do, and there is nothing left to check the code against.

Two habits enforce it in practice:

1. Groom and implement in **separate sessions**. The four roles make this structural: PM
   writes the story, SRE reviews its observability, architecture reviews its contract and moves
   it to `ready`, and only then does the role named in its Lane build it. Grooming a story and then implementing it in the
   same context would make the story a memory of intent rather than a specification anything
   can be verified against.
2. A story still contains **contracts, not code drops**. Illustrative snippets inside a story
   (an interface sketch, a proto or JSON schema, a struct shape) stay under ~30 lines and are
   marked `// CONTRACT SKETCH — not an implementation`. If a snippet grows past that, it wants
   to be the implementation, and the implementation belongs on a branch.

### Agents and ownership

| Role | Branch prefix | Owns | Status moves |
|------|---------------|------|--------------|
| PM | `pm/` | `docs/sprints/` (close-outs and demos), `docs/roadmap.md`, epics, new stories in full (contract included), sprint issues, GitHub milestones and triage | creates stories at `draft` |
| Architecture | `arch/` | Contract review; `docs/adr/`, `docs/specs/` (except `docs/specs/slo/`), `docs/builders/`, `buf.gen.yaml`; Lane `architecture` stories; the §8 review for every lane | `draft` → `ready` / `blocked`; `review` → `done` |
| SRE | `sre/` | Observability review of drafts; `deploy/`, `.github/`, `Makefile`, `scripts/`, `docs/runbooks/`, `docs/specs/slo/`; Lane `sre` stories; the §8 instrumentation check | its own stories: `ready` → `in-progress` |
| Implementation | `impl/` | `server/`, `internal/`, `cmd/`, `admin/`, `content/`, `agents/`, `client/`, `testdata/`; Lane `implementation` stories | its own stories: `ready` → `in-progress` |

Architecture and SRE move their own stories to `in-progress` the same way implementation does
(`story start <ID>`). No role moves its own story to `review`: the story-merge Action does, when
the lane's PR carrying `Story: <ID>` and its `role:<lane>` label merges. A question for another
agent is a comment on the story, `story comment <ID> --to <role>`. `docs/feedback/` is a frozen
archive from before the cutover; open stories link their old files in a comment.

Ownership is enforced, not just documented: the `.claude/roles/` charters list each role's
writable paths, and a hook denies edits to another role's paths, asks Brian before edits to
unowned paths (this file, `.claude/`, `go.mod`), and denies hand edits to generated files
(`gen/`, `docs/builders/reference.md`). A committed generated file
belongs to no role. The PR that changes its source runs its target (`make proto`,
`make builder-reference`) and commits the output, whatever that PR's role. `.claude/` is installed
from automate.bashburn.com (`make install TARGET=andaras-world`); don't edit it here.

### The sprint cycle

Work runs in sprints. A sprint is an issue, `SPRINT-NN — <goal>`, labeled `sprint` and
`sprint:planned` or `sprint:active`; at most one is active, and a closed issue is a closed sprint
(`.claude/bin/sprint-state`). Its stories carry its number in the Project's `Sprint` field, in
`Rank` order, with `Plan` `Current` (or `Next` for the planned one).

1. **PM, at the boundary** (one session, one `pm/sprint-NN-<slug>` PR): writes
   `docs/sprints/SPRINT-NN-closeout.md` and `docs/sprints/SPRINT-NN-demo.md` for what the active
   sprint delivered, closes its issue, grooms, and plans the next sprint
   (`/pm-close-sprint`, `/pm-start-sprint`). Each sprint has at least one demo goal an operator can run, tied to a milestone
   gate in `docs/roadmap.md` or a slice of one.
2. **SRE** reviews the Observability requirements of the sprint's drafts first, then verifies
   instrumentation for anything at `review`, then works its own backlog.
3. **Architecture** reviews the contracts of the sprint's drafts once SRE's review is in, since
   implementation waits on them. Next comes the §8 review of anything at `review`, then its
   own backlog.
4. **Implementation** takes the first story in its lane's sprint list
   (`story list --sprint N --lane implementation`, by Rank) that is `ready` with every blocker at
   `review` or later.
5. Architecture, SRE, and implementation work only on stories in the active sprint. A role with
   nothing pickable stops and reports; it doesn't pull work from outside the sprint. No active
   sprint means PM hasn't planned one yet, so stop. The sprint ends when every story is `done`
   or PM carries it over.

Demo instructions obey §9: every step is a `make` target or a product command. A step that
needs a hand-written shell sequence is marked `§9 defect → AW-INF-NNN`, and that `lane: sre`
story goes into the next sprint.

---

## 3. Repository layout

```
docs/
  roadmap.md              # phase → epic → milestone map; the planning source of truth
  glossary.md             # domain vocabulary; every term used in a story must exist here
  adr/
    ADR-0001-<slug>.md
  specs/
    protocol/             # wire protocol, versioned
    schema/               # persistence schemas, migrations plan
    slo/                  # service level objectives
  builders/               # the Builder's Guide (AW-INF-023): README.md, one file per section;
                          #   reference.md is GENERATED by `make builder-reference`
  runbooks/
  sprints/
    SPRINT-01.md          # SPRINT-01 … 04: plan and close-out, from before the cutover
    SPRINT-05-closeout.md # what a sprint delivered, written at close-out
    SPRINT-05-demo.md     # operator demo instructions, written at close-out
  feedback/               # frozen archive from before the cutover; feedback is story comments now
Makefile
.claude/                  # INSTALLED from automate.bashburn.com — do not hand-edit
  roles/                  # role charters + writable paths (pm, architecture, sre, implementation)
  skills/                 # /role, /pm-start-sprint, /pm-close-sprint, /arch-start-sprint, …
  bin/role                # role state + ownership hooks
  bin/story               # stories and sprints on the Project; the only way to change them
  settings.json           # hooks merged in by the installer; other keys are this repo's
```

Stories, epics, and sprints are **issues** on the "Andara's World" org Project, next to the content
repo's `AWC-*` stories. Status, Lane, Sprint, Plan, and Rank are Project fields. Nothing about a
story lives in a file: the story files were removed at the SPRINT-05 boundary, and their last
commit is tagged `stories-final`.

---

## 4. Identifiers and conventions

- Story ID: `AW-<COMP>-<NNN>` — `AW-SRV-014`, `AW-INF-003`. Zero-padded to 3. Never reused, never renumbered.
- Epic ID: `EPIC-<NN>`.
- ADR ID: `ADR-<NNNN>`, monotonic, never deleted — superseded ADRs get `status: superseded by ADR-XXXX`.
- Branch name: `<prefix>/<story-id-lower>-<slug>` → `impl/aw-srv-014-room-graph-loader`, with
  the owning role's prefix from §2. PM branches are `pm/sprint-NN-<slug>`, §8 reviews go
  on `arch/…-review`, and SRE's instrumentation checks on `sre/…-verify`. `release/dev` is the one
  exception: a throwaway branch reset to a `main` commit plus one promotion commit, owned by Brian
  (`make promote`, ADR-0013 §7). Never commit to another agent's branch or directly to `main`.
- Commit trailer: `Story: AW-SRV-014`, or `Sprint: SPRINT-NN` for PM commits. The story-merge
  Action reads `Story:` (and `Story-Done:`) lines from a merged PR to move the story.
- Sprint ID: `SPRINT-<NN>`, monotonic.
- **Commits are signed.** `main` is branch-protected to require signatures (2026-09-22), so an
  unsigned commit cannot merge. `make bootstrap` configures per-repo SSH signing from the key
  that already pushes to origin, and `.github/allowed_signers` is the tracked list of trusted
  keys. History before that date is unsigned and stays that way: re-signing 152 commits would
  change every SHA, break the other lane's clone, and dangle the commit references the
  verification records in the story issues cite.

---

## 5. Story format

Every story is an issue titled `AW-SRV-014 — <title>`. Its fields are set when PM creates it:

```
story create --comp SRV --title "Load zone definitions from content store at boot" \
  --lane implementation --size M --risk medium --component server \
  --epic EPIC-02 --depends AW-SRV-011,AW-INF-002 --body-file story.md
```

| Field | Values |
|-------|--------|
| `--comp` | `SRV` \| `CLI` \| `INF` \| `CLT` (the ID's component code) |
| Lane | `architecture` (contracts) \| `sre` (infra, ops) \| `implementation` (source) |
| Size | `S` \| `M` \| `L` — L means "split it" |
| Risk | `low` \| `medium` \| `high` |
| Component | `server` \| `cli` \| `infra` \| `client` |
| Status | `draft` \| `ready` \| `in-progress` \| `review` \| `done` \| `blocked` |
| Epic | the `EPIC-NN` issue it's a sub-issue of |
| Depends | GitHub blocked-by links; GitHub shows the reverse side itself |

The issue body is exactly this shape. Claude Code emits it verbatim.

```markdown
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
   covers, the story stays `draft`, its question goes to architecture as a comment
   (`story comment <ID> --to architecture`), and it stays out of the sprint. Otherwise write it directly. Do not narrate a plan to write a story, or ask whether to
   proceed with a story that has already been requested.
4. **Splits aggressively.** Anything sized `L` gets decomposed before it reaches `ready`.
   Target: a story one focused session can complete against a clean context window.
5. **Declares dependencies** with `story create --depends`. GitHub records them as blocked-by
   links and shows the blocking side itself.
6. **Places it** with `story set <ID> Sprint=N Plan=… Rank=k`. Stories need no PR: `story create`
   writes the issue. A PM PR carries only `docs/sprints/` and `docs/roadmap.md`.

SRE reviews the story's Observability requirements against §7 and amends only that section.
Architecture's contract review then checks the same list, amends what's wrong, and moves the
story to `ready`, or to `blocked` naming what it waits on. A contract change after `ready` is made
with `story body`, which posts a Contract comment, plus a comment `--to <lane>` if implementation
has started.

Anti-patterns to reject during grooming, in this repo specifically:

- Stories that couple simulation logic to transport. The sim must not know about WebSockets.
- Stories that assume a single process holds all world state without an ADR authorizing it.
- Stories that add a persistence dependency without stating the migration and rollback path.
- Stories whose acceptance criteria only describe the happy path.
- Client stories written before the protocol version they depend on is frozen.

---

## 7. Observability requirements (SRE lane)

Every `server`, `cli`, and `infra` story carries an **Observability requirements** section.
Instrumentation is part of the acceptance criteria, not a follow-up story. SRE owns this section:
it reviews it before the contract review, and verifies it at §8.

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
every lane's stories, on an `arch/…-review` branch, and moves the story from `review` to `done`.
The instrumentation item is SRE's: it verifies it on an `sre/…-verify` branch and records the
result as a story comment (`--record "§8 instrumentation"`) before architecture records its own
`--record "§8"` and moves the story.

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
integration suite exercising the story's own code against the local kind cluster's services and
the `solo7dev` Grafana Cloud stack for telemetry, read through `gcx`, with assertions on the metric
objects, plus a scrape of the same registry's sibling series from the server. The story's §8
record says exactly which series the server itself has not yet emitted, and the first story
that can make the live observation carries it as an inherited Definition-of-done line. Deferring the observation this way is not deferring the check; holding a
story in `review` until a caller two milestones away lands is what `review` does not mean.

---

## 9. Automation contract

Every workflow in this repo is a make target. If Claude Code writes a procedure into a doc,
it writes the target in the same pass. A documented sequence of shell commands that isn't a
target is a defect.

Baseline targets the SRE role owns and keeps working:

```
make help              # self-documenting target list; default goal
make bootstrap         # install/verify toolchain, hooks, local deps — idempotent
make up / make down    # local environment (ADR-0012 §14; ADR-0013 names the target)
make check             # fmt + vet + lint + test + manifest validation; what CI runs
make adr               # scaffold a new ADR
make k8s-dry           # render + validate manifests against the target API version
```

Stories aren't files, so they have no make targets: `.claude/bin/story` and the Project's views
are their tools.

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
  drive; contracts are architecture's; operability and reliability are SRE's.

### Session start

Brian starts the session with `/role <name>`. Until he does, don't pick up work, and don't choose
a role yourself. Then `git fetch origin` and start from `origin/main`. Run `.claude/bin/sprint-state`,
then `story list --sprint N --lane <your lane>`: the active sprint is the work, in Rank order. Then
`docs/roadmap.md`, `docs/glossary.md`, and any ADR with `status: proposed`.

The board is only as honest as the moves, so status travels with the work: `story start <ID>`
when you pick a story up, and `Story: <ID>` plus your `role:<lane>` label on the PR that delivers
it, so the merge moves it to `review`.

